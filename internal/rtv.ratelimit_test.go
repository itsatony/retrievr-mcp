package internal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// RateLimiter unit tests
// ---------------------------------------------------------------------------

func TestRateLimiterBasicRate(t *testing.T) {
	t.Parallel()

	const (
		rps       = 10.0
		burst     = 1
		waitCount = 5
		timeout   = 5 * time.Second
	)

	rl := NewRateLimiter(RateLimiterConfig{
		SourceID:          SourceArXiv,
		RequestsPerSecond: rps,
		Burst:             burst,
	}, DefaultCredentialBucketTTL)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for i := range waitCount {
		err := rl.Wait(ctx, "test-key")
		require.NoError(t, err, "Wait %d should succeed", i)
	}
}

func TestRateLimiterBurst(t *testing.T) {
	t.Parallel()

	const (
		rps        = 0.1 // very slow refill
		burst      = 5
		maxElapsed = 100 * time.Millisecond
	)

	rl := NewRateLimiter(RateLimiterConfig{
		SourceID:          SourceArXiv,
		RequestsPerSecond: rps,
		Burst:             burst,
	}, DefaultCredentialBucketTTL)

	ctx := context.Background()

	start := time.Now()
	for i := range burst {
		err := rl.Wait(ctx, "burst-key")
		require.NoError(t, err, "burst Wait %d should succeed", i)
	}
	elapsed := time.Since(start)

	assert.Less(t, elapsed, maxElapsed,
		"first %d requests (burst) should complete near-instantly, took %v", burst, elapsed)
}

func TestRateLimiterPerCredentialIsolation(t *testing.T) {
	t.Parallel()

	const (
		rps   = 1000.0
		burst = 1
	)

	rl := NewRateLimiter(RateLimiterConfig{
		SourceID:          SourceS2,
		RequestsPerSecond: rps,
		Burst:             burst,
	}, DefaultCredentialBucketTTL)

	ctx := context.Background()

	// Exhaust cred-a's burst token.
	err := rl.Wait(ctx, "cred-a")
	require.NoError(t, err)

	// cred-b should still get a token immediately since buckets are isolated.
	start := time.Now()
	err = rl.Wait(ctx, "cred-b")
	require.NoError(t, err)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 50*time.Millisecond,
		"cred-b should not be blocked by cred-a's exhaustion")
}

func TestRateLimiterContextCancellation(t *testing.T) {
	t.Parallel()

	const (
		rps   = 0.01 // extremely slow refill
		burst = 1
	)

	rl := NewRateLimiter(RateLimiterConfig{
		SourceID:          SourcePubMed,
		RequestsPerSecond: rps,
		Burst:             burst,
	}, DefaultCredentialBucketTTL)

	ctx := context.Background()

	// Exhaust the burst token.
	err := rl.Wait(ctx, "cancel-key")
	require.NoError(t, err)

	// Now try with an already-canceled context.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err = rl.Wait(canceledCtx, "cancel-key")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded),
		"error should wrap ErrRateLimitExceeded, got: %v", err)
}

func TestRateLimiterContextDeadline(t *testing.T) {
	t.Parallel()

	const (
		rps     = 0.01 // extremely slow refill
		burst   = 1
		timeout = 1 * time.Millisecond
	)

	rl := NewRateLimiter(RateLimiterConfig{
		SourceID:          SourceOpenAlex,
		RequestsPerSecond: rps,
		Burst:             burst,
	}, DefaultCredentialBucketTTL)

	ctx := context.Background()

	// Exhaust the burst token.
	err := rl.Wait(ctx, "deadline-key")
	require.NoError(t, err)

	// Wait with a very short deadline.
	deadlineCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err = rl.Wait(deadlineCtx, "deadline-key")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded),
		"error should wrap ErrRateLimitExceeded, got: %v", err)
}

func TestRateLimiterTTLEviction(t *testing.T) {
	t.Parallel()

	const (
		ttl      = 1 * time.Millisecond
		sleepDur = 5 * time.Millisecond
	)

	rl := NewRateLimiter(RateLimiterConfig{
		SourceID:          SourceArXiv,
		RequestsPerSecond: DefaultRateLimitRPS,
		Burst:             DefaultRateLimitBurst,
	}, ttl)

	ctx := context.Background()

	// Create two buckets.
	require.NoError(t, rl.Wait(ctx, "old-1"))
	require.NoError(t, rl.Wait(ctx, "old-2"))
	assert.Equal(t, 2, rl.BucketCount())

	// Let them expire.
	time.Sleep(sleepDur)

	evicted := rl.CleanupExpired()
	assert.Equal(t, 2, evicted, "both old buckets should be evicted")
	assert.Equal(t, 0, rl.BucketCount())

	// Create a new bucket — it should survive cleanup immediately.
	require.NoError(t, rl.Wait(ctx, "fresh"))
	assert.Equal(t, 1, rl.BucketCount())

	evicted = rl.CleanupExpired()
	assert.Equal(t, 0, evicted, "fresh bucket should survive cleanup")
	assert.Equal(t, 1, rl.BucketCount())
}

func TestRateLimiterRemaining(t *testing.T) {
	t.Parallel()

	const burst = 5

	rl := NewRateLimiter(RateLimiterConfig{
		SourceID:          SourceArXiv,
		RequestsPerSecond: 0.1, // slow refill so tokens don't replenish during test
		Burst:             burst,
	}, DefaultCredentialBucketTTL)

	// Unknown bucket should report full burst capacity.
	remaining := rl.Remaining("unknown-key")
	assert.Equal(t, float64(burst), remaining)

	// After one Wait, remaining should decrease.
	ctx := context.Background()
	require.NoError(t, rl.Wait(ctx, "known-key"))

	remaining = rl.Remaining("known-key")
	assert.Less(t, remaining, float64(burst),
		"remaining should be less than burst after a Wait")
}

func TestRateLimiterBucketCount(t *testing.T) {
	t.Parallel()

	rl := NewRateLimiter(RateLimiterConfig{
		SourceID:          SourceArXiv,
		RequestsPerSecond: DefaultRateLimitRPS,
		Burst:             DefaultRateLimitBurst,
	}, DefaultCredentialBucketTTL)

	assert.Equal(t, 0, rl.BucketCount())

	ctx := context.Background()
	require.NoError(t, rl.Wait(ctx, "key-1"))
	assert.Equal(t, 1, rl.BucketCount())

	require.NoError(t, rl.Wait(ctx, "key-2"))
	assert.Equal(t, 2, rl.BucketCount())

	// Same key again — count should not increase.
	require.NoError(t, rl.Wait(ctx, "key-1"))
	assert.Equal(t, 2, rl.BucketCount())

	require.NoError(t, rl.Wait(ctx, "key-3"))
	assert.Equal(t, 3, rl.BucketCount())
}

// ---------------------------------------------------------------------------
// SourceRateLimitManager unit tests
// ---------------------------------------------------------------------------

func TestSourceRateLimitManagerRegister(t *testing.T) {
	t.Parallel()

	mgr := NewSourceRateLimitManager(DefaultCredentialBucketTTL)

	mgr.Register(RateLimiterConfig{
		SourceID:          SourceArXiv,
		RequestsPerSecond: 10.0,
		Burst:             5,
	})
	mgr.Register(RateLimiterConfig{
		SourceID:          SourcePubMed,
		RequestsPerSecond: 5.0,
		Burst:             3,
	})

	ctx := context.Background()

	err := mgr.Wait(ctx, SourceArXiv, "user-1")
	assert.NoError(t, err, "Wait on registered arxiv should succeed")

	err = mgr.Wait(ctx, SourcePubMed, "user-1")
	assert.NoError(t, err, "Wait on registered pubmed should succeed")
}

func TestSourceRateLimitManagerWaitUnknownSource(t *testing.T) {
	t.Parallel()

	mgr := NewSourceRateLimitManager(DefaultCredentialBucketTTL)

	err := mgr.Wait(context.Background(), "nonexistent", "key")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceNotFound),
		"error should wrap ErrSourceNotFound, got: %v", err)
}

func TestSourceRateLimitManagerStartStop(t *testing.T) {
	t.Parallel()

	const (
		ttl             = 1 * time.Millisecond
		cleanupInterval = 10 * time.Millisecond
		settleTime      = 50 * time.Millisecond
	)

	mgr := NewSourceRateLimitManager(ttl)

	mgr.Register(RateLimiterConfig{
		SourceID:          SourceArXiv,
		RequestsPerSecond: 1000.0,
		Burst:             10,
	})

	ctx := context.Background()

	// Create some buckets.
	require.NoError(t, mgr.Wait(ctx, SourceArXiv, "bucket-1"))
	require.NoError(t, mgr.Wait(ctx, SourceArXiv, "bucket-2"))

	mgr.Start(cleanupInterval)

	// Let the cleanup goroutine run at least once and the TTL expire.
	time.Sleep(settleTime)

	// After cleanup, the expired buckets should have been evicted.
	// Verify by checking that Remaining returns burst (new bucket default).
	remaining := mgr.Remaining(SourceArXiv, "bucket-1")
	assert.Equal(t, float64(10), remaining,
		"bucket-1 should have been evicted and Remaining should return burst")

	// Stop should not hang.
	done := make(chan struct{})
	go func() {
		mgr.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success — Stop returned.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2 seconds")
	}
}

func TestSourceRateLimitManagerConcurrent(t *testing.T) {
	t.Parallel()

	const (
		goroutines = 50
		rps        = 1000.0
		burst      = 50 // enough for all goroutines
	)

	mgr := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	mgr.Register(RateLimiterConfig{
		SourceID:          SourceS2,
		RequestsPerSecond: rps,
		Burst:             burst,
	})

	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			bucketKey := "cred-" + string(rune('A'+idx%26))
			errs[idx] = mgr.Wait(ctx, SourceS2, bucketKey)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d should succeed", i)
	}
}

func TestNewRateLimiterDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		rps           float64
		burst         int
		expectedRPS   float64
		expectedBurst int
	}{
		{
			name:          "zero RPS gets default",
			rps:           0,
			burst:         5,
			expectedRPS:   DefaultRateLimitRPS,
			expectedBurst: 5,
		},
		{
			name:          "negative RPS gets default",
			rps:           -1.0,
			burst:         5,
			expectedRPS:   DefaultRateLimitRPS,
			expectedBurst: 5,
		},
		{
			name:          "sub-minimum RPS gets default",
			rps:           0.001,
			burst:         5,
			expectedRPS:   DefaultRateLimitRPS,
			expectedBurst: 5,
		},
		{
			name:          "zero burst gets default",
			rps:           10.0,
			burst:         0,
			expectedRPS:   10.0,
			expectedBurst: DefaultRateLimitBurst,
		},
		{
			name:          "negative burst gets default",
			rps:           10.0,
			burst:         -3,
			expectedRPS:   10.0,
			expectedBurst: DefaultRateLimitBurst,
		},
		{
			name:          "both zero get defaults",
			rps:           0,
			burst:         0,
			expectedRPS:   DefaultRateLimitRPS,
			expectedBurst: DefaultRateLimitBurst,
		},
		{
			name:          "valid values are kept",
			rps:           5.0,
			burst:         10,
			expectedRPS:   5.0,
			expectedBurst: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rl := NewRateLimiter(RateLimiterConfig{
				SourceID:          SourceArXiv,
				RequestsPerSecond: tt.rps,
				Burst:             tt.burst,
			}, DefaultCredentialBucketTTL)

			assert.Equal(t, tt.expectedRPS, rl.rps, "RPS mismatch")
			assert.Equal(t, tt.expectedBurst, rl.burst, "burst mismatch")
		})
	}
}
