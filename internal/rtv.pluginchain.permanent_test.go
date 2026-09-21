package internal

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Reproduction (issue #1, part 2): a permanent refusal was retried 3 times.
//
// withRetry's default predicate returned true for every non-context error, so
// a source with no get-by-id API burned retryDefaultMaxAttempts attempts and
// two backoff sleeps before surfacing a refusal that could never have changed.
// ---------------------------------------------------------------------------

// countingOp returns a pluginOp that always fails with err, and the counter it
// increments. The counter is the whole assertion: a test that only checked the
// returned error would have been green before AND after the fix.
func countingOp(err error) (pluginOp, *int) {
	calls := 0
	return func(context.Context) error {
		calls++
		return err
	}, &calls
}

// retryTestConfig is DefaultRetryConfig with the sleep removed — the subject
// under test is the ATTEMPT COUNT, and a real 250 ms backoff would only make
// the test slow without making it stronger.
func retryTestConfig() RetryConfig {
	cfg := DefaultRetryConfig()
	cfg.BaseDelay = time.Microsecond
	cfg.MaxDelay = time.Microsecond
	cfg.JitterFraction = 0
	return cfg
}

func TestWithRetry_PermanentRefusalIsAttemptedExactlyOnce(t *testing.T) {
	t.Parallel()

	permanent := []struct {
		name string
		err  error
	}{
		{"get_unsupported_typed", NewGetUnsupportedError("linkup has no per-result Get API")},
		{"get_unsupported_sentinel", fmt.Errorf("%w: linkup", ErrGetUnsupported)},
		{"format_unsupported", fmt.Errorf("%w: linkup", ErrFormatUnsupported)},
		{"credential_required", fmt.Errorf("%w: linkup", ErrCredentialRequired)},
		{"credential_invalid", fmt.Errorf("%w: linkup returned 401", ErrCredentialInvalid)},
		{"invalid_id", fmt.Errorf("%w: %q", ErrInvalidID, "nope")},
		{"invalid_published_at", fmt.Errorf("%w", ErrInvalidPublishedAt)},
		{"source_disabled", fmt.Errorf("%w: linkup", ErrSourceDisabled)},
		// Wrapped by the plugin's own ErrGetFailed envelope — the shape the
		// scholarly plugins actually return.
		{"wrapped_in_get_failed", fmt.Errorf("%w: %w", ErrGetFailed, ErrFormatUnsupported)},
	}

	for _, tc := range permanent {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			op, calls := countingOp(tc.err)
			got := withRetry(retryTestConfig(), nil, "linkup", nil)(op)(context.Background())
			require.Error(t, got)
			assert.Equal(t, 1, *calls,
				"a permanent refusal must be attempted exactly once, got %d attempts", *calls)
		})
	}
}

// TestWithRetry_TransientErrorStillBurnsEveryAttempt is the CONTROL. Without
// it the assertion above could be satisfied by disabling retry altogether.
func TestWithRetry_TransientErrorStillBurnsEveryAttempt(t *testing.T) {
	t.Parallel()

	transient := []struct {
		name string
		err  error
	}{
		{"rate_limited", fmt.Errorf("%w: linkup", ErrRateLimitExceeded)},
		{"upstream_timeout", fmt.Errorf("%w: linkup", ErrUpstreamTimeout)},
		{"transport", errors.New("linkup: http: connection reset")},
		{"not_found", fmt.Errorf("%w", ErrArxivNotFound)},
		{"json_parse", fmt.Errorf("%w", ErrCrossRefJSONParse)},
	}

	for _, tc := range transient {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			op, calls := countingOp(tc.err)
			got := withRetry(retryTestConfig(), nil, "linkup", nil)(op)(context.Background())
			require.Error(t, got)
			assert.Equal(t, retryDefaultMaxAttempts, *calls,
				"a transient error must still be retried, got %d attempts", *calls)
		})
	}
}

// TestIsPermanentError_ClassifiesTheDocumentedSet pins the classifier itself so
// a future edit to permanentSentinels cannot quietly move an entry across the
// boundary.
func TestIsPermanentError_ClassifiesTheDocumentedSet(t *testing.T) {
	t.Parallel()

	assert.False(t, IsPermanentError(nil), "nil is not an error at all")

	require.NotEmpty(t, permanentSentinels, "the permanent set must not be empty")
	for _, sentinel := range permanentSentinels {
		assert.True(t, IsPermanentError(sentinel),
			"%v is declared permanent and must classify as permanent", sentinel)
		assert.False(t, isTransientError(sentinel),
			"%v is permanent and must not be retried", sentinel)
	}

	// ErrSearchFailed / ErrGetFailed are WRAPPERS around transport failures.
	// Classifying either permanent would disable retry across the whole tree.
	assert.False(t, IsPermanentError(ErrSearchFailed), "ErrSearchFailed is a wrapper, not a verdict")
	assert.False(t, IsPermanentError(ErrGetFailed), "ErrGetFailed is a wrapper, not a verdict")
	assert.False(t, IsPermanentError(ErrRateLimitExceeded), "429 is exactly what backoff is for")
	assert.False(t, IsPermanentError(ErrUpstreamTimeout), "an upstream timeout may clear")
}
