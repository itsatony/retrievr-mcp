package internal

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Cache constants
// ---------------------------------------------------------------------------

const (
	// cacheKeyHashLength is the number of hex characters used from the SHA-256 hash.
	// 32 hex chars = 16 bytes of the 32-byte SHA-256 digest.
	cacheKeyHashLength = 32
)

// ---------------------------------------------------------------------------
// Cache types
// ---------------------------------------------------------------------------

// CacheConfig holds configuration for the LRU cache.
type CacheConfig struct {
	MaxEntries int
	TTL        time.Duration
	Enabled    bool
}

// CacheMetrics holds hit/miss/eviction counters.
type CacheMetrics struct {
	Hits      uint64
	Misses    uint64
	Evictions uint64
}

// cacheEntry is stored as the Value in list.Element.
type cacheEntry struct {
	key       string
	value     *SearchResult
	expiresAt time.Time
}

// Cache is a thread-safe in-memory LRU cache with TTL expiration.
type Cache struct {
	mu          sync.Mutex
	maxEntries  int
	ttl         time.Duration
	enabled     bool
	items       map[string]*list.Element
	evictList   *list.List
	metrics     CacheMetrics
	promMetrics *Metrics
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewCache creates a new Cache with the given configuration.
// If cfg.MaxEntries is <= 0, DefaultCacheMaxEntries is used.
// The metrics parameter is optional (nil disables Prometheus counter updates).
func NewCache(cfg CacheConfig, metrics *Metrics) *Cache {
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultCacheMaxEntries
	}

	return &Cache{
		maxEntries:  maxEntries,
		ttl:         cfg.TTL,
		enabled:     cfg.Enabled,
		items:       make(map[string]*list.Element),
		evictList:   list.New(),
		promMetrics: metrics,
	}
}

// ---------------------------------------------------------------------------
// Cache key generation
// ---------------------------------------------------------------------------

// GenerateCacheKey produces a deterministic, fixed-length hex key from
// SearchParams and a list of source IDs. The sources slice is copied and
// sorted so that source order does not affect the key.
func GenerateCacheKey(params SearchParams, sources []string) (string, error) {
	// Copy the sources slice to avoid mutating the caller's slice.
	sortedSources := make([]string, len(sources))
	copy(sortedSources, sources)
	sort.Strings(sortedSources)

	payload := struct {
		Params  SearchParams `json:"params"`
		Sources []string     `json:"sources"`
	}{
		Params:  params,
		Sources: sortedSources,
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrCacheKeyGeneration, err)
	}

	sum := sha256.Sum256(b)
	full := hex.EncodeToString(sum[:])
	return full[:cacheKeyHashLength], nil
}

// ---------------------------------------------------------------------------
// Cache operations
// ---------------------------------------------------------------------------

// Get retrieves a cached SearchResult by key.
// Returns (nil, false) when the cache is disabled, the key is not present,
// or the entry has expired (the expired entry is evicted on access).
func (c *Cache) Get(key string) (*SearchResult, bool) {
	if !c.enabled {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		c.metrics.Misses++
		c.promMetrics.RecordCacheMiss()
		return nil, false
	}

	entry := elem.Value.(*cacheEntry)
	if time.Now().After(entry.expiresAt) {
		// Expired: remove and report as miss + eviction.
		c.evictList.Remove(elem)
		delete(c.items, key)
		c.metrics.Misses++
		c.metrics.Evictions++
		c.promMetrics.RecordCacheMiss()
		return nil, false
	}

	// Cache hit: move to front (most-recently used).
	c.evictList.MoveToFront(elem)
	c.metrics.Hits++
	c.promMetrics.RecordCacheHit()
	return entry.value, true
}

// Set stores a SearchResult in the cache under the given key.
// If the cache is disabled, Set is a no-op.
// If the key already exists, the value and expiry are updated in-place.
// If adding a new entry would exceed maxEntries, the least-recently-used
// entry is evicted first.
func (c *Cache) Set(key string, value *SearchResult) {
	if !c.enabled {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry.
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*cacheEntry)
		entry.value = value
		entry.expiresAt = time.Now().Add(c.ttl)
		c.evictList.MoveToFront(elem)
		return
	}

	// Evict LRU entry if at capacity.
	if len(c.items) >= c.maxEntries {
		back := c.evictList.Back()
		if back != nil {
			evicted := back.Value.(*cacheEntry)
			c.evictList.Remove(back)
			delete(c.items, evicted.key)
			c.metrics.Evictions++
		}
	}

	// Insert new entry at front.
	entry := &cacheEntry{
		key:       key,
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
	elem := c.evictList.PushFront(entry)
	c.items[key] = elem
}

// Delete removes the entry with the given key from the cache.
// Returns true if the key existed and was removed, false otherwise.
func (c *Cache) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return false
	}

	c.evictList.Remove(elem)
	delete(c.items, key)
	return true
}

// Len returns the number of entries currently in the cache.
// Returns 0 if the cache is disabled.
func (c *Cache) Len() int {
	if !c.enabled {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Metrics returns a snapshot of the current hit/miss/eviction counters.
func (c *Cache) Metrics() CacheMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.metrics
}

// Clear removes all entries from the cache and resets all metrics counters.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*list.Element)
	c.evictList = list.New()
	c.metrics = CacheMetrics{}
}
