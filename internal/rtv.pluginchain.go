package internal

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"
)

// ---------------------------------------------------------------------------
// Plugin-invocation middleware chain (Cycle 1 task #3)
//
// The old Router.searchOneSource hard-coded the order: rate-limit → timeout →
// plugin.Search. With the new map-based credential surface AND the resilience
// requirements from project_plan/retrievr_v2.md §3.2, we lift that into a
// composable chain so we can:
//
//   - Add retry above rate-limit (each retry attempt consumes its own token,
//     matching liz DC-145).
//   - Add per-attempt timeout (the parent ctx deadline still bounds the whole
//     call; each attempt gets a fresh sub-deadline).
//   - Plug additional layers (cache, fallback, metrics) at higher levels in
//     cycles 2-3 without re-touching searchOneSource.
//
// We use a closure-of-error operation rather than a generic Handler[T] to
// keep the surface small. Search and Get both produce different result
// types; the chain only cares about whether the operation succeeded. The
// caller captures the actual *SearchResult / *Publication via closure.
// ---------------------------------------------------------------------------

// pluginOp is the unit of work the middleware chain executes.
//
// Implementations capture their result types via closure:
//
//	var result *SearchResult
//	op := func(ctx context.Context) error {
//	    var e error
//	    result, e = plugin.Search(ctx, params)
//	    return e
//	}
type pluginOp func(ctx context.Context) error

// pluginMW decorates a pluginOp.
type pluginMW func(next pluginOp) pluginOp

// chainPluginMW composes middlewares left-to-right; the leftmost is the
// outermost wrapper. Calling chainPluginMW()(op) returns op unchanged.
func chainPluginMW(mws ...pluginMW) pluginMW {
	return func(final pluginOp) pluginOp {
		for i := len(mws) - 1; i >= 0; i-- {
			final = mws[i](final)
		}
		return final
	}
}

// ---------------------------------------------------------------------------
// withTimeout — innermost in the chain.
//
// Bounds a single attempt. The retry middleware wrapping it gives each
// attempt a fresh budget; the parent ctx deadline still caps the whole call.
// ---------------------------------------------------------------------------

func withTimeout(d time.Duration) pluginMW {
	return func(next pluginOp) pluginOp {
		return func(ctx context.Context) error {
			if d <= 0 {
				return next(ctx)
			}
			childCtx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return next(childCtx)
		}
	}
}

// ---------------------------------------------------------------------------
// withRateLimit — token bucket per (sourceID, bucketKey).
//
// Sits below retry: each retry attempt consumes its own token, so backoff
// actually slows us down rather than burst-then-stall.
// ---------------------------------------------------------------------------

func withRateLimit(rl *SourceRateLimitManager, metrics *Metrics, sourceID, bucketKey string) pluginMW {
	return func(next pluginOp) pluginOp {
		return func(ctx context.Context) error {
			if rl == nil {
				return next(ctx)
			}
			throttled, err := rl.Wait(ctx, sourceID, bucketKey)
			if err != nil {
				return err
			}
			if throttled && metrics != nil {
				metrics.RecordRateLimitWait(sourceID)
			}
			return next(ctx)
		}
	}
}

// ---------------------------------------------------------------------------
// withRetry — equal-jitter exponential backoff.
//
// Default 3 attempts, 250ms base, 8s max. Jitter is "equal" (sleep =
// random[0, computed]) which empirically avoids thundering-herd resync under
// shared load — see liz DC-145 + AWS Architecture Blog "Exponential Backoff
// And Jitter".
// ---------------------------------------------------------------------------

// RetryConfig governs retry behavior in the plugin chain.
type RetryConfig struct {
	// MaxAttempts is the upper bound on tries (1 = no retry).
	MaxAttempts int

	// BaseDelay is the seed delay for backoff. Effective delay grows
	// geometrically from this value, then jitter is applied.
	BaseDelay time.Duration

	// MaxDelay caps the per-attempt sleep regardless of geometric growth.
	MaxDelay time.Duration

	// JitterFraction in [0,1] picks how much of the computed delay is
	// randomized. 0 = no jitter (deterministic); 1 = equal-jitter
	// (sleep ∈ [0, fullDelay]).
	JitterFraction float64
}

// DefaultRetryConfig returns the cycle-1 default retry config.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:    retryDefaultMaxAttempts,
		BaseDelay:      retryDefaultBaseDelay,
		MaxDelay:       retryDefaultMaxDelay,
		JitterFraction: retryDefaultJitterFraction,
	}
}

const (
	retryDefaultMaxAttempts    = 3
	retryDefaultBaseDelay      = 250 * time.Millisecond
	retryDefaultMaxDelay       = 8 * time.Second
	retryDefaultJitterFraction = 1.0

	logKeyAttempt   = "attempt"
	logKeyMaxTries  = "max_attempts"
	logKeyBackoff   = "backoff_ms"
	logMsgRetryWait = "plugin op retrying after error"

	backoffGrowth = 2.0
)

// withRetry wraps an op so it is retried up to cfg.MaxAttempts times on
// transient errors. The shouldRetry predicate decides whether a given error
// is transient; pass nil to retry on any non-context error.
func withRetry(cfg RetryConfig, logger *slog.Logger, sourceID string, shouldRetry func(error) bool) pluginMW {
	if cfg.MaxAttempts <= 1 {
		// Disabled — return a no-op middleware.
		return func(next pluginOp) pluginOp { return next }
	}
	if shouldRetry == nil {
		shouldRetry = isTransientError
	}
	return func(next pluginOp) pluginOp {
		return func(ctx context.Context) error {
			var lastErr error
			for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				lastErr = next(ctx)
				if lastErr == nil {
					return nil
				}
				if !shouldRetry(lastErr) {
					return lastErr
				}
				if attempt == cfg.MaxAttempts {
					break
				}
				delay := computeBackoff(cfg, attempt)
				if logger != nil {
					logger.Debug(logMsgRetryWait,
						slog.String(LogKeySource, sourceID),
						slog.Int(logKeyAttempt, attempt),
						slog.Int(logKeyMaxTries, cfg.MaxAttempts),
						slog.Int64(logKeyBackoff, delay.Milliseconds()),
						slog.String(LogKeyError, lastErr.Error()),
					)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay):
				}
			}
			return lastErr
		}
	}
}

// computeBackoff returns the equal-jitter exponential backoff delay for the
// given attempt number (1-indexed).
//
//	full = min(base * growth^(attempt-1), max)
//	delay = (1-jitterFraction)*full + rand[0, jitterFraction*full]
//
// With JitterFraction=1.0 (default) this collapses to delay ∈ [0, full].
// With JitterFraction=0 it returns the deterministic full delay.
func computeBackoff(cfg RetryConfig, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	growth := 1.0
	for i := 1; i < attempt; i++ {
		growth *= backoffGrowth
	}
	full := time.Duration(float64(cfg.BaseDelay) * growth)
	if cfg.MaxDelay > 0 && full > cfg.MaxDelay {
		full = cfg.MaxDelay
	}
	jitter := cfg.JitterFraction
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	staticPart := time.Duration(float64(full) * (1 - jitter))
	jitterRange := full - staticPart
	if jitterRange <= 0 {
		return staticPart
	}
	// rand/v2 is non-crypto and fine for jitter — only used for de-syncing.
	return staticPart + time.Duration(rand.Int64N(int64(jitterRange)+1))
}

// isTransientError is the default retry predicate. A nil error is never
// retried (caller already returned). ctx.Cancelled / DeadlineExceeded are
// not retried — they reflect caller intent. Everything else is retried;
// cycle 2 will narrow this once we wrap upstream HTTP errors with typed
// RetryableError variants.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}
