package internal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// chainPluginMW composition
// ---------------------------------------------------------------------------

func TestChainPluginMW_OrderingIsOuterToInner(t *testing.T) {
	t.Parallel()

	var trace []string

	mark := func(label string) pluginMW {
		return func(next pluginOp) pluginOp {
			return func(ctx context.Context) error {
				trace = append(trace, label+":enter")
				err := next(ctx)
				trace = append(trace, label+":exit")
				return err
			}
		}
	}

	chain := chainPluginMW(mark("outer"), mark("middle"), mark("inner"))
	op := func(ctx context.Context) error {
		trace = append(trace, "leaf")
		return nil
	}
	require.NoError(t, chain(op)(context.Background()))

	assert.Equal(t, []string{
		"outer:enter",
		"middle:enter",
		"inner:enter",
		"leaf",
		"inner:exit",
		"middle:exit",
		"outer:exit",
	}, trace)
}

func TestChainPluginMW_EmptyChainIsIdentity(t *testing.T) {
	t.Parallel()

	called := false
	op := func(ctx context.Context) error {
		called = true
		return nil
	}
	require.NoError(t, chainPluginMW()(op)(context.Background()))
	assert.True(t, called)
}

// ---------------------------------------------------------------------------
// withTimeout
// ---------------------------------------------------------------------------

func TestWithTimeout_AppliesPerAttemptDeadline(t *testing.T) {
	t.Parallel()

	op := func(ctx context.Context) error {
		dl, ok := ctx.Deadline()
		require.True(t, ok, "child ctx must carry a deadline")
		assert.WithinDuration(t, time.Now().Add(50*time.Millisecond), dl, 20*time.Millisecond)
		return nil
	}
	require.NoError(t, withTimeout(50*time.Millisecond)(op)(context.Background()))
}

func TestWithTimeout_ZeroDurationIsPassthrough(t *testing.T) {
	t.Parallel()

	op := func(ctx context.Context) error {
		_, ok := ctx.Deadline()
		assert.False(t, ok, "zero timeout must not impose a deadline")
		return nil
	}
	require.NoError(t, withTimeout(0)(op)(context.Background()))
}

// ---------------------------------------------------------------------------
// withRetry
// ---------------------------------------------------------------------------

func TestWithRetry_SucceedsOnSecondAttempt(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	op := func(ctx context.Context) error {
		n := attempts.Add(1)
		if n < 2 {
			return errors.New("flaky")
		}
		return nil
	}

	cfg := RetryConfig{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond, JitterFraction: 0}
	err := withRetry(cfg, nil, "test-source", nil)(op)(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(2), attempts.Load())
}

func TestWithRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	sentinel := errors.New("always fails")
	op := func(ctx context.Context) error {
		attempts.Add(1)
		return sentinel
	}

	cfg := RetryConfig{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond, JitterFraction: 0}
	err := withRetry(cfg, nil, "test-source", nil)(op)(context.Background())
	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, int32(3), attempts.Load())
}

func TestWithRetry_DoesNotRetryNonTransient(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	permanent := errors.New("permanent")
	op := func(ctx context.Context) error {
		attempts.Add(1)
		return permanent
	}

	cfg := RetryConfig{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond, JitterFraction: 0}
	pred := func(err error) bool { return false } // never retry
	err := withRetry(cfg, nil, "test-source", pred)(op)(context.Background())
	require.ErrorIs(t, err, permanent)
	assert.Equal(t, int32(1), attempts.Load())
}

func TestWithRetry_RespectsContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var attempts atomic.Int32
	op := func(ctx context.Context) error {
		attempts.Add(1)
		return errors.New("transient")
	}

	cfg := RetryConfig{MaxAttempts: 3, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second, JitterFraction: 0}
	err := withRetry(cfg, nil, "test-source", nil)(op)(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(0), attempts.Load(), "cancelled ctx must short-circuit before first attempt")
}

func TestWithRetry_MaxAttemptsOneIsNoop(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	op := func(ctx context.Context) error {
		attempts.Add(1)
		return errors.New("err")
	}

	cfg := RetryConfig{MaxAttempts: 1, BaseDelay: time.Millisecond}
	err := withRetry(cfg, nil, "test-source", nil)(op)(context.Background())
	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load())
}

// ---------------------------------------------------------------------------
// computeBackoff — equal-jitter math
// ---------------------------------------------------------------------------

func TestComputeBackoff_GrowsGeometrically(t *testing.T) {
	t.Parallel()

	cfg := RetryConfig{
		MaxAttempts:    5,
		BaseDelay:      100 * time.Millisecond,
		MaxDelay:       10 * time.Second,
		JitterFraction: 0, // deterministic for assertion
	}

	assert.Equal(t, 100*time.Millisecond, computeBackoff(cfg, 1))
	assert.Equal(t, 200*time.Millisecond, computeBackoff(cfg, 2))
	assert.Equal(t, 400*time.Millisecond, computeBackoff(cfg, 3))
	assert.Equal(t, 800*time.Millisecond, computeBackoff(cfg, 4))
}

func TestComputeBackoff_HonorsMaxDelay(t *testing.T) {
	t.Parallel()

	cfg := RetryConfig{
		BaseDelay:      time.Second,
		MaxDelay:       2 * time.Second,
		JitterFraction: 0,
	}
	// 1s, 2s (capped), 2s, 2s, ...
	assert.Equal(t, time.Second, computeBackoff(cfg, 1))
	assert.Equal(t, 2*time.Second, computeBackoff(cfg, 2))
	assert.Equal(t, 2*time.Second, computeBackoff(cfg, 3))
	assert.Equal(t, 2*time.Second, computeBackoff(cfg, 5))
}

func TestComputeBackoff_EqualJitterStaysWithinBounds(t *testing.T) {
	t.Parallel()

	cfg := RetryConfig{
		BaseDelay:      100 * time.Millisecond,
		MaxDelay:       time.Second,
		JitterFraction: 1.0,
	}
	// At attempt=2 the deterministic value is 200ms; equal-jitter sweeps [0, 200ms].
	for i := 0; i < 100; i++ {
		got := computeBackoff(cfg, 2)
		assert.GreaterOrEqual(t, got, time.Duration(0))
		assert.LessOrEqual(t, got, 200*time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// resolveRetryConfig — YAML zero-value substitution
// ---------------------------------------------------------------------------

func TestResolveRetryConfig_ZeroValuesGetDefaults(t *testing.T) {
	t.Parallel()

	got := resolveRetryConfig(RouterRetryConfig{})
	def := DefaultRetryConfig()
	assert.Equal(t, def, got)
}

func TestResolveRetryConfig_NonZeroValuesPassThrough(t *testing.T) {
	t.Parallel()

	cfg := RouterRetryConfig{
		MaxAttempts:    7,
		BaseDelay:      Duration{Duration: 500 * time.Millisecond},
		MaxDelay:       Duration{Duration: 30 * time.Second},
		JitterFraction: 0.5,
	}
	got := resolveRetryConfig(cfg)
	assert.Equal(t, 7, got.MaxAttempts)
	assert.Equal(t, 500*time.Millisecond, got.BaseDelay)
	assert.Equal(t, 30*time.Second, got.MaxDelay)
	assert.InDelta(t, 0.5, got.JitterFraction, 1e-9)
}

// ---------------------------------------------------------------------------
// isTransientError — context errors are not retried
// ---------------------------------------------------------------------------

func TestIsTransientError_ContextErrorsAreNotTransient(t *testing.T) {
	t.Parallel()

	assert.False(t, isTransientError(context.Canceled))
	assert.False(t, isTransientError(context.DeadlineExceeded))
	assert.False(t, isTransientError(nil))
	assert.True(t, isTransientError(errors.New("network")))
}
