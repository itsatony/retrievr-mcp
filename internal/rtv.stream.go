package internal

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Streaming search — Cycle 3 task #23 (plan §6.1.3).
//
// Router.Stream fans out to plugins concurrently and forwards results as
// each provider returns, instead of buffering for cross-source dedup +
// merge. Useful for slow providers (Perplexity Sonar median 5–13s) where
// the caller wants to render hits incrementally.
//
// Trade-offs vs. Search:
//   - No cross-source dedup (would require buffering the full set).
//   - No final-pass sort (results stream in arrival order).
//   - No fallback WALK after fan-out (the cycle-2 fallback semantics rely on knowing
//     primary fan-out's full result set; streaming can't make that
//     decision incrementally without lookahead). ⚠ It DOES promote the
//     fallback rung when the primary rung resolves to empty — that
//     decision needs no lookahead at all (see the promotion note in Stream).
//   - EU-mode gate IS applied (Hook #2 still runs pre-fanout).
//   - Refusal path IS applied (Hook #5 still runs).
//   - Audit event IS emitted (Hook #3 — final event after channel close).
//   - HTTP hygiene IS applied (Hook #4 — every plugin uses NewEgressClient).
//
// MCP wrapper does NOT expose Stream — MCP tool results aren't streaming.
// retrievr-cli exposes via --stream (cycle-3 cli enhancement, optional).
// ---------------------------------------------------------------------------

// StreamEvent carries a single per-source result OR a terminal error/info
// event. Consumers iterate the channel until it closes.
type StreamEvent struct {
	// Source is the source ID that produced the event.
	Source string

	// Result is the per-source search result. Nil when Err is set.
	Result *SearchResult

	// Err carries a per-source failure. Other sources continue independently.
	Err error
}

// Stream runs a search and forwards per-source SearchResults as they
// arrive on the returned channel. The channel closes when all in-flight
// sources have completed (success or error) or when ctx is cancelled.
//
// Cycle 3 ships the basic shape; cycle 4 may add per-result fan-out
// (forwarding individual Publications rather than per-source SearchResults)
// once we have a concrete consumer that needs that granularity.
func (r *Router) Stream(
	ctx context.Context,
	params SearchParams,
	sources []string,
	creds *CallCredentials,
) (<-chan StreamEvent, error) {
	// Refusal path (Hook #5).
	if err := validateEUModeSources(sources, r.plugins, r.euMode, r.euIncludePublicResearch); err != nil {
		return nil, err
	}

	// Resolve sources — same precedence as Search. Streaming still does not WALK a
	// fallback chain after the fan-out (that needs the primary set's full result set,
	// which streaming cannot know incrementally), but it does PROMOTE the fallback rung
	// when the primary rung is empty. See the promotion note below.
	var resolved, fallbackList []string
	switch {
	case len(sources) > 0:
		resolved = r.resolveSources(sources)
	case params.Intent != "":
		resolved, fallbackList = r.resolveByIntent(params.Intent)
	default:
		resolved = r.resolveSources(nil)
	}
	// ⚠ THE STATED BLOCKER DOES NOT COVER THIS CASE, WHICH IS WHY THE TWIN IS FIXABLE.
	// The file header explains that streaming cannot walk a fallback because it "can't
	// make that decision incrementally without lookahead" — true, and irrelevant here.
	// When the primary rung filters to EMPTY, nothing about the decision is incremental:
	// it is fully determined at resolve time, before a single request is made, by which
	// plugins this tenant registered. Substituting the fallback rung as the fan-out set is
	// strictly smaller than walking one, and without it `intent="quick_lookup"` is a
	// guaranteed error on the streaming surface exactly as it was on Search.
	if len(resolved) == 0 && len(fallbackList) > 0 {
		resolved = fallbackList
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrSearchFailed, errDetailNoValidSources)
	}

	// Apply EU-mode gate (Hook #2).
	gate := applyEUGate(resolved, r.plugins, r.euMode, r.euIncludePublicResearch)
	resolved = gate.Admitted
	if len(resolved) == 0 {
		// Still emit an audit event so callers can correlate the empty
		// stream to a recorded gating decision.
		r.emitAuditEvent(ctx, params, nil, gate.Skipped, nil, false, false, false)
		out := make(chan StreamEvent)
		close(out)
		return out, nil
	}

	requestID := GenerateRequestID()
	ctx = WithRequestID(ctx, requestID)

	// Stream channel — buffered up to len(resolved) so providers can write
	// without blocking when the consumer is slow.
	out := make(chan StreamEvent, len(resolved))

	// Fan out: each provider runs independently and pushes either a
	// result event or an error event before the goroutine exits.
	fanoutParams := params
	if fanoutParams.Limit == 0 {
		fanoutParams.Limit = DefaultSearchLimit
	}

	var wg sync.WaitGroup
	wg.Add(len(resolved))

	invokedMu := sync.Mutex{}
	var invoked []string
	var failed []string

	for _, srcID := range resolved {
		go func(sourceID string) {
			defer wg.Done()
			res := r.searchOneSource(ctx, sourceID, fanoutParams, creds)

			invokedMu.Lock()
			invoked = append(invoked, sourceID)
			if res.err != nil {
				failed = append(failed, sourceID)
			}
			invokedMu.Unlock()

			ev := StreamEvent{Source: sourceID}
			if res.err != nil {
				ev.Err = res.err
			} else {
				ev.Result = res.result
			}

			select {
			case out <- ev:
			case <-ctx.Done():
				// Drop the event — caller is gone. The audit event below
				// still runs in the closer goroutine.
			}
		}(srcID)
	}

	// Closer goroutine: waits for all fan-out goroutines, emits the final
	// audit event, then closes the channel.
	go func() {
		wg.Wait()
		// Audit event after all providers finish (Hook #3).
		invokedMu.Lock()
		_ = r.emitAuditEvent(ctx, params, invoked, gate.Skipped, failed, false, false, false)
		invokedMu.Unlock()

		// Tiny grace period so any in-flight provider goroutines that
		// raced ctx.Done() finish their selects cleanly. Empirically not
		// needed — wg.Wait above guarantees all goroutines are done — but
		// reserved for future fan-out-per-result modes.
		_ = time.Millisecond

		close(out)
	}()

	return out, nil
}
