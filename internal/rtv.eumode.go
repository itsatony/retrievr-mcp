package internal

import (
	"context"
	"fmt"
	"log/slog"
)

// ---------------------------------------------------------------------------
// EU-mode gate + refusal path — Hooks #2 and #5 (plan §3.7).
//
// The gate runs BEFORE Router fan-out: it filters the resolved source set
// down to providers admissible under the configured mode. Skipped sources
// surface in MergedSearchResult.SourcesSkipped with a structured reason so
// callers can render UI hints / compliance reports without parsing logs.
//
// The refusal path runs at validation time: when a caller in eu_strict
// passes an explicit Sources arg containing non-EU providers, Router
// returns ErrEUModeProviderConflict with structured Requested/Blocked
// detail. We never silently drop non-EU sources from an explicit Sources
// list; callers either accept the rejection or relax the mode.
// ---------------------------------------------------------------------------

// EUMode literal-string equivalents shared with rtv_v2.md plan §3.7. The
// canonical type lives in pkg/retrievr (so adopters can import the enum
// directly) but the gate operates on plain strings to avoid an import
// dependency the wrong way around.
const (
	EUModeOff       = "off"
	EUModePreferred = "eu_preferred"
	EUModeStrict    = "eu_strict"
)

// IsValidEUMode reports whether the string is one of the three legal modes.
// Empty string is valid (means "off") so callers don't have to set a default.
func IsValidEUMode(m string) bool {
	switch m {
	case "", EUModeOff, EUModePreferred, EUModeStrict:
		return true
	}
	return false
}

// EUGateResult is the typed output of an EU-mode filter pass: the admitted
// set, the skipped set with reasons, and the chosen mode (echoed for audit).
type EUGateResult struct {
	Admitted []string
	Skipped  []SkipNote
	Mode     string
}

// applyEUGate filters the candidate source IDs against the configured EU
// mode. Pure function: takes the plugin map (for residency lookup), returns
// admitted + skipped lists. Called from Router.Search after source
// resolution and BEFORE any plugin invocation.
//
// Semantics:
//   - "off": every candidate admitted; Skipped is nil.
//   - "eu_strict": only providers with Region.IsEU() admitted, plus public-
//     research-infrastructure if includePublicResearch=true.
//   - "eu_preferred": same admission rules as off (i.e., everyone admitted)
//     — the "prefer EU" semantic plays out at the result level, not the
//     candidate level. Cycle 2 lifts this into Router.Search where it can
//     observe per-source results before deciding to walk to non-EU
//     fallbacks. The gate function itself is called with mode=off in that
//     case to skip filtering. (Hook #2 contract is satisfied: the gate
//     IS evaluated; it just admits everyone in preferred mode.)
//
// candidates is filtered + ordered as input; the returned Admitted preserves
// the input order so downstream fan-out is deterministic.
func applyEUGate(
	candidates []string,
	plugins map[string]SourcePlugin,
	mode string,
	includePublicResearch bool,
) EUGateResult {
	if mode == "" || mode == EUModeOff || mode == EUModePreferred {
		return EUGateResult{Admitted: append([]string(nil), candidates...), Mode: mode}
	}
	// eu_strict: admit only EU/UK-adequacy + (optional) public-research.
	admitted := make([]string, 0, len(candidates))
	skipped := make([]SkipNote, 0)
	for _, id := range candidates {
		p, ok := plugins[id]
		if !ok {
			skipped = append(skipped, SkipNote{ID: id, Reason: "unknown_source"})
			continue
		}
		tag := p.Residency()
		switch {
		case tag.Region.IsEU():
			admitted = append(admitted, id)
		case tag.Region.IsPublicResearch() && includePublicResearch:
			admitted = append(admitted, id)
		default:
			skipped = append(skipped, SkipNote{ID: id, Reason: skipReasonEUStrict})
		}
	}
	return EUGateResult{Admitted: admitted, Skipped: skipped, Mode: mode}
}

// validateEUModeSources is the refusal path (Hook #5). When a caller
// explicitly listed sources AND we're in eu_strict, we reject the call
// rather than silently dropping non-EU providers — silent dropping would
// produce thin/no results without making the cause visible.
//
// Returns nil when the call is permissible. Returns *EUModeProviderConflictError
// (which satisfies errors.Is(err, ErrEUModeProviderConflict)) when blocked.
func validateEUModeSources(
	requested []string,
	plugins map[string]SourcePlugin,
	mode string,
	includePublicResearch bool,
) error {
	if mode != EUModeStrict || len(requested) == 0 {
		return nil
	}
	blocked := make([]string, 0)
	for _, id := range requested {
		p, ok := plugins[id]
		if !ok {
			// Unknown sources are caught later by resolveSources. Don't
			// double-report here.
			continue
		}
		tag := p.Residency()
		if tag.Region.IsEU() {
			continue
		}
		if tag.Region.IsPublicResearch() && includePublicResearch {
			continue
		}
		blocked = append(blocked, id)
	}
	if len(blocked) == 0 {
		return nil
	}
	return &EUModeProviderConflictError{
		Mode:      mode,
		Requested: append([]string(nil), requested...),
		Blocked:   blocked,
	}
}

// logEUGateDecision emits a structured log line summarising the gate's
// admission decision. Lower priority than the audit event (which is the
// authoritative record); this is operator-facing breadcrumb logging.
func logEUGateDecision(ctx context.Context, logger *slog.Logger, requestID string, gate EUGateResult) {
	_ = ctx // reserved for future tracing integration
	if logger == nil || gate.Mode == "" || gate.Mode == EUModeOff {
		return
	}
	skippedIDs := make([]string, len(gate.Skipped))
	for i, s := range gate.Skipped {
		skippedIDs[i] = fmt.Sprintf("%s:%s", s.ID, s.Reason)
	}
	logger.Debug("retrievr eu-mode gate applied",
		slog.String(LogKeyRequestID, requestID),
		slog.String("mode", gate.Mode),
		slog.Any("admitted", gate.Admitted),
		slog.Any("skipped", skippedIDs),
	)
}
