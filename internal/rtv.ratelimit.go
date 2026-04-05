package internal

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ---------------------------------------------------------------------------
// Rate limiter constants
// ---------------------------------------------------------------------------

const (
	// RateLimitMinRPS is the minimum sane rate to prevent misconfiguration.
	// Exported for use by cmd/retrievr-mcp when applying rate limit defaults.
	RateLimitMinRPS = 0.01
)

// ---------------------------------------------------------------------------
// RateLimiterConfig
// ---------------------------------------------------------------------------

// RateLimiterConfig holds configuration for a single source's rate limiter.
type RateLimiterConfig struct {
	SourceID          string
	RequestsPerSecond float64
	Burst             int
}

// ---------------------------------------------------------------------------
// credentialBucket
// ---------------------------------------------------------------------------

// credentialBucket pairs a rate.Limiter with its last-access time for TTL eviction.
// lastAccess is only written under RateLimiter.mu. Reads in CleanupExpired also hold the lock.
type credentialBucket struct {
	limiter    *rate.Limiter
	lastAccess time.Time
}

// ---------------------------------------------------------------------------
// RateLimiter — per-source, per-credential token-bucket rate limiting
// ---------------------------------------------------------------------------

// RateLimiter manages per-credential token-bucket rate limiters for a single source.
// Thread-safe.
type RateLimiter struct {
	mu       sync.Mutex
	sourceID string
	rps      float64
	burst    int
	buckets  map[string]*credentialBucket
	ttl      time.Duration
}

// NewRateLimiter creates a RateLimiter for a single source.
// Sub-minimum RPS or non-positive burst values are replaced with defaults.
func NewRateLimiter(cfg RateLimiterConfig, bucketTTL time.Duration) *RateLimiter {
	rps := cfg.RequestsPerSecond
	if rps < RateLimitMinRPS {
		rps = DefaultRateLimitRPS
	}

	burst := cfg.Burst
	if burst <= 0 {
		burst = DefaultRateLimitBurst
	}

	return &RateLimiter{
		sourceID: cfg.SourceID,
		rps:      rps,
		burst:    burst,
		buckets:  make(map[string]*credentialBucket),
		ttl:      bucketTTL,
	}
}

// Wait blocks until the rate limiter allows the request for the given bucket key,
// or the context is canceled/expired. The bucket is created on first access.
func (rl *RateLimiter) Wait(ctx context.Context, bucketKey string) error {
	rl.mu.Lock()
	bucket := rl.getOrCreateBucket(bucketKey)
	rl.mu.Unlock()

	// Wait OUTSIDE the lock — this may block.
	if err := bucket.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrRateLimitExceeded, err)
	}

	return nil
}

// getOrCreateBucket returns the bucket for the given key, creating it if needed.
// The caller MUST hold rl.mu.
func (rl *RateLimiter) getOrCreateBucket(bucketKey string) *credentialBucket {
	bucket, exists := rl.buckets[bucketKey]
	if exists {
		bucket.lastAccess = time.Now()
		return bucket
	}

	bucket = &credentialBucket{
		limiter:    rate.NewLimiter(rate.Limit(rl.rps), rl.burst),
		lastAccess: time.Now(),
	}
	rl.buckets[bucketKey] = bucket
	return bucket
}

// Remaining returns the approximate number of tokens available for the given bucket key.
// Returns float64(rl.burst) if the bucket has not been created yet (new callers start full).
func (rl *RateLimiter) Remaining(bucketKey string) float64 {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, exists := rl.buckets[bucketKey]
	if !exists {
		return float64(rl.burst)
	}

	return bucket.limiter.Tokens()
}

// CleanupExpired removes credential buckets that have not been accessed within the TTL.
// Returns the number of evicted buckets.
func (rl *RateLimiter) CleanupExpired() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	evicted := 0
	now := time.Now()
	for key, bucket := range rl.buckets {
		if now.Sub(bucket.lastAccess) > rl.ttl {
			delete(rl.buckets, key)
			evicted++
		}
	}
	return evicted
}

// BucketCount returns the number of active credential buckets.
func (rl *RateLimiter) BucketCount() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	return len(rl.buckets)
}

// ---------------------------------------------------------------------------
// SourceRateLimitManager — manages rate limiters for all sources
// ---------------------------------------------------------------------------

// SourceRateLimitManager manages rate limiters for all sources. Thread-safe.
type SourceRateLimitManager struct {
	mu        sync.RWMutex
	limiters  map[string]*RateLimiter // key = sourceID
	ttl       time.Duration
	stopCh    chan struct{}
	doneCh    chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

// NewSourceRateLimitManager creates a new manager with the given credential bucket TTL.
func NewSourceRateLimitManager(bucketTTL time.Duration) *SourceRateLimitManager {
	return &SourceRateLimitManager{
		limiters: make(map[string]*RateLimiter),
		ttl:      bucketTTL,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Register adds or replaces a rate limiter for a source.
func (m *SourceRateLimitManager) Register(cfg RateLimiterConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.limiters[cfg.SourceID] = NewRateLimiter(cfg, m.ttl)
}

// Wait blocks until the rate limiter for the given source allows the request,
// or returns an error if the source is unknown or the context is canceled.
func (m *SourceRateLimitManager) Wait(ctx context.Context, sourceID, bucketKey string) error {
	m.mu.RLock()
	limiter, exists := m.limiters[sourceID]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("%w: source %q", ErrSourceNotFound, sourceID)
	}

	return limiter.Wait(ctx, bucketKey)
}

// Remaining returns the approximate number of tokens remaining for a source/bucket.
// Returns -1 if the source is not registered.
func (m *SourceRateLimitManager) Remaining(sourceID, bucketKey string) float64 {
	m.mu.RLock()
	limiter, exists := m.limiters[sourceID]
	m.mu.RUnlock()

	if !exists {
		return -1
	}

	return limiter.Remaining(bucketKey)
}

// Start launches a background goroutine that periodically evicts expired credential
// buckets. Safe to call multiple times — only the first call starts the goroutine.
func (m *SourceRateLimitManager) Start(interval time.Duration) {
	m.startOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			defer close(m.doneCh)

			for {
				select {
				case <-ticker.C:
					m.CleanupAllExpired()
				case <-m.stopCh:
					return
				}
			}
		}()
	})
}

// Stop signals the cleanup goroutine to exit and waits for it to finish.
// Safe to call multiple times and safe to call even if Start() was never called.
func (m *SourceRateLimitManager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	// If Start was never called, doneCh was never closed by a goroutine.
	// Use startOnce to detect: if Do runs, Start was never called, so close doneCh.
	started := true
	m.startOnce.Do(func() {
		started = false
		close(m.doneCh)
	})
	if started {
		<-m.doneCh
	}
}

// CleanupAllExpired evicts expired credential buckets across all sources.
// Returns the total number of evicted buckets.
func (m *SourceRateLimitManager) CleanupAllExpired() int {
	// Snapshot limiters under read lock, then release before calling CleanupExpired
	// which acquires each limiter's own mutex.
	m.mu.RLock()
	snapshot := make([]*RateLimiter, 0, len(m.limiters))
	for _, limiter := range m.limiters {
		snapshot = append(snapshot, limiter)
	}
	m.mu.RUnlock()

	total := 0
	for _, limiter := range snapshot {
		total += limiter.CleanupExpired()
	}
	return total
}
