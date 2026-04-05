package internal

import (
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func testSearchResult(total int) *SearchResult {
	return &SearchResult{
		Total: total,
		Results: []Publication{
			{
				ID:          "test:1",
				Source:      SourceArXiv,
				ContentType: ContentTypePaper,
				Title:       "Test",
				Authors:     []Author{{Name: "A"}},
				Published:   "2024-01-01",
				URL:         "https://example.com",
			},
		},
		HasMore: false,
	}
}

// defaultEnabledCache returns a Cache suitable for most tests.
func defaultEnabledCache(maxEntries int, ttl time.Duration) *Cache {
	return NewCache(CacheConfig{
		MaxEntries: maxEntries,
		TTL:        ttl,
		Enabled:    true,
	}, nil)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCacheGetSetHit(t *testing.T) {
	t.Parallel()
	c := defaultEnabledCache(DefaultCacheMaxEntries, DefaultCacheTTL)
	result := testSearchResult(1)

	c.Set("key1", result)
	got, ok := c.Get("key1")

	require.True(t, ok, "expected cache hit")
	assert.Equal(t, result, got)

	m := c.Metrics()
	assert.Equal(t, uint64(1), m.Hits)
	assert.Equal(t, uint64(0), m.Misses)
	assert.Equal(t, uint64(0), m.Evictions)
}

func TestCacheGetMiss(t *testing.T) {
	t.Parallel()
	c := defaultEnabledCache(DefaultCacheMaxEntries, DefaultCacheTTL)

	got, ok := c.Get("nonexistent")

	assert.False(t, ok, "expected cache miss")
	assert.Nil(t, got)

	m := c.Metrics()
	assert.Equal(t, uint64(0), m.Hits)
	assert.Equal(t, uint64(1), m.Misses)
}

func TestCacheTTLExpiry(t *testing.T) {
	t.Parallel()
	const shortTTL = 50 * time.Millisecond
	const sleepDuration = 60 * time.Millisecond

	c := defaultEnabledCache(DefaultCacheMaxEntries, shortTTL)
	result := testSearchResult(1)

	c.Set("key1", result)

	// Immediate get — should hit.
	got, ok := c.Get("key1")
	require.True(t, ok, "expected hit before TTL expiry")
	assert.Equal(t, result, got)

	// Wait for TTL to elapse.
	time.Sleep(sleepDuration)

	// Get after expiry — should miss and count as eviction.
	got, ok = c.Get("key1")
	assert.False(t, ok, "expected miss after TTL expiry")
	assert.Nil(t, got)

	m := c.Metrics()
	assert.Equal(t, uint64(1), m.Hits)
	assert.Equal(t, uint64(1), m.Misses)
	assert.Equal(t, uint64(1), m.Evictions)
}

func TestCacheLRUEviction(t *testing.T) {
	t.Parallel()
	c := defaultEnabledCache(3, DefaultCacheTTL)

	// Fill the cache to capacity: a, b, c (a is LRU).
	c.Set("a", testSearchResult(1))
	c.Set("b", testSearchResult(2))
	c.Set("c", testSearchResult(3))

	// Insert "d" — should evict "a" (least-recently used).
	c.Set("d", testSearchResult(4))

	// "a" must be gone.
	got, ok := c.Get("a")
	assert.False(t, ok, "expected 'a' to be evicted")
	assert.Nil(t, got)

	// "d" must be present.
	got, ok = c.Get("d")
	require.True(t, ok, "expected 'd' to be in cache")
	assert.Equal(t, 4, got.Total)

	assert.Equal(t, 3, c.Len())

	m := c.Metrics()
	assert.Equal(t, uint64(1), m.Evictions)
}

func TestCacheLRUAccessOrder(t *testing.T) {
	t.Parallel()
	c := defaultEnabledCache(3, DefaultCacheTTL)

	// Fill cache: a (oldest/LRU), b, c (newest/MRU).
	c.Set("a", testSearchResult(1))
	c.Set("b", testSearchResult(2))
	c.Set("c", testSearchResult(3))

	// Access "a" — it becomes the MRU; "b" is now LRU.
	_, ok := c.Get("a")
	require.True(t, ok)

	// Insert "d" — should evict "b", not "a".
	c.Set("d", testSearchResult(4))

	// "b" must be gone.
	_, ok = c.Get("b")
	assert.False(t, ok, "expected 'b' to be evicted (LRU)")

	// "a", "c", "d" must remain.
	_, ok = c.Get("a")
	assert.True(t, ok, "expected 'a' to be present")
	_, ok = c.Get("c")
	assert.True(t, ok, "expected 'c' to be present")
	_, ok = c.Get("d")
	assert.True(t, ok, "expected 'd' to be present")
}

func TestCacheUpdateExisting(t *testing.T) {
	t.Parallel()
	c := defaultEnabledCache(DefaultCacheMaxEntries, DefaultCacheTTL)

	value1 := testSearchResult(1)
	value2 := testSearchResult(99)

	c.Set("x", value1)
	c.Set("x", value2) // Update in place.

	got, ok := c.Get("x")
	require.True(t, ok)
	assert.Equal(t, 99, got.Total)

	// Only one entry despite two Sets.
	assert.Equal(t, 1, c.Len())
}

func TestCacheDelete(t *testing.T) {
	t.Parallel()
	c := defaultEnabledCache(DefaultCacheMaxEntries, DefaultCacheTTL)

	c.Set("key1", testSearchResult(1))

	deleted := c.Delete("key1")
	assert.True(t, deleted, "expected Delete to return true for existing key")

	got, ok := c.Get("key1")
	assert.False(t, ok, "expected miss after delete")
	assert.Nil(t, got)

	assert.Equal(t, 0, c.Len())

	// Delete non-existent key.
	deleted = c.Delete("key1")
	assert.False(t, deleted, "expected Delete to return false for absent key")
}

func TestCacheClear(t *testing.T) {
	t.Parallel()
	c := defaultEnabledCache(DefaultCacheMaxEntries, DefaultCacheTTL)

	c.Set("a", testSearchResult(1))
	c.Set("b", testSearchResult(2))
	c.Set("c", testSearchResult(3))

	// Accumulate some metrics before clearing.
	c.Get("a")
	c.Get("nonexistent")

	c.Clear()

	// Metrics must be reset immediately after Clear, before any further operations.
	m := c.Metrics()
	assert.Equal(t, uint64(0), m.Hits, "hits must be 0 after Clear")
	assert.Equal(t, uint64(0), m.Misses, "misses must be 0 after Clear")
	assert.Equal(t, uint64(0), m.Evictions, "evictions must be 0 after Clear")

	// Len must be 0 and all previously inserted keys must miss.
	assert.Equal(t, 0, c.Len())

	_, ok := c.Get("a")
	assert.False(t, ok)
	_, ok = c.Get("b")
	assert.False(t, ok)
	_, ok = c.Get("c")
	assert.False(t, ok)
}

func TestCacheDisabled(t *testing.T) {
	t.Parallel()
	c := NewCache(CacheConfig{
		MaxEntries: DefaultCacheMaxEntries,
		TTL:        DefaultCacheTTL,
		Enabled:    false,
	}, nil)

	// Set must be a no-op.
	c.Set("key1", testSearchResult(1))

	// Get must always miss.
	got, ok := c.Get("key1")
	assert.False(t, ok)
	assert.Nil(t, got)

	// Len must always be 0.
	assert.Equal(t, 0, c.Len())

	// No metrics tracked when disabled.
	m := c.Metrics()
	assert.Equal(t, uint64(0), m.Hits)
	assert.Equal(t, uint64(0), m.Misses)
}

func TestCacheMetrics(t *testing.T) {
	t.Parallel()
	// maxEntries=1 to trigger eviction easily.
	c := defaultEnabledCache(1, DefaultCacheTTL)

	// Miss on empty cache.
	c.Get("a")

	// Set "a".
	c.Set("a", testSearchResult(1))

	// Hit on "a".
	c.Get("a")

	// Set "b" — evicts "a".
	c.Set("b", testSearchResult(2))

	// Miss on "a" (evicted).
	c.Get("a")

	m := c.Metrics()
	assert.Equal(t, uint64(1), m.Hits, "expected exactly 1 hit")
	assert.Equal(t, uint64(2), m.Misses, "expected exactly 2 misses")
	assert.Equal(t, uint64(1), m.Evictions, "expected exactly 1 eviction")
}

func TestCacheConcurrentAccess(t *testing.T) {
	t.Parallel()
	const goroutines = 100

	c := defaultEnabledCache(DefaultCacheMaxEntries, DefaultCacheTTL)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			key := "key-" + string(rune('0'+n%10))
			c.Set(key, testSearchResult(n))
			c.Get(key)
		}(i)
	}

	wg.Wait()
	// No assertions on exact values — we just verify no panics and no races.
	_ = c.Len()
	_ = c.Metrics()
}

// ---------------------------------------------------------------------------
// GenerateCacheKey tests
// ---------------------------------------------------------------------------

func TestGenerateCacheKey(t *testing.T) {
	t.Parallel()
	baseParams := SearchParams{
		Query:       "neural networks",
		ContentType: ContentTypePaper,
		Filters:     SearchFilters{DateFrom: "2020-01-01"},
		Sort:        SortRelevance,
		Limit:       10,
		Offset:      0,
	}

	tests := []struct {
		name      string
		params    SearchParams
		sources   []string
		compareTo *struct {
			params  SearchParams
			sources []string
		}
		expectEqual bool
		// If compareTo is nil, the test just checks format constraints.
	}{
		{
			name:    "same params and sources produce same key",
			params:  baseParams,
			sources: []string{SourceArXiv, SourceS2},
			compareTo: &struct {
				params  SearchParams
				sources []string
			}{baseParams, []string{SourceArXiv, SourceS2}},
			expectEqual: true,
		},
		{
			name: "different query produces different key",
			params: SearchParams{
				Query:       "transformers",
				ContentType: ContentTypePaper,
				Sort:        SortRelevance,
				Limit:       10,
			},
			sources: []string{SourceArXiv},
			compareTo: &struct {
				params  SearchParams
				sources []string
			}{baseParams, []string{SourceArXiv}},
			expectEqual: false,
		},
		{
			name:    "different sources produce different key",
			params:  baseParams,
			sources: []string{SourcePubMed},
			compareTo: &struct {
				params  SearchParams
				sources []string
			}{baseParams, []string{SourceArXiv}},
			expectEqual: false,
		},
		{
			name:    "source order does not matter",
			params:  baseParams,
			sources: []string{SourceArXiv, SourceS2},
			compareTo: &struct {
				params  SearchParams
				sources []string
			}{baseParams, []string{SourceS2, SourceArXiv}},
			expectEqual: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, err := GenerateCacheKey(tc.params, tc.sources)
			require.NoError(t, err)

			// Format checks — always.
			assert.Len(t, key, cacheKeyHashLength, "key must be exactly cacheKeyHashLength chars")
			_, hexErr := hex.DecodeString(key)
			assert.NoError(t, hexErr, "key must be valid hex")

			if tc.compareTo != nil {
				otherKey, err := GenerateCacheKey(tc.compareTo.params, tc.compareTo.sources)
				require.NoError(t, err)

				if tc.expectEqual {
					assert.Equal(t, key, otherKey, "keys should be equal")
				} else {
					assert.NotEqual(t, key, otherKey, "keys should differ")
				}
			}
		})
	}
}

func TestGenerateCacheKeyDeterministic(t *testing.T) {
	t.Parallel()
	params := SearchParams{
		Query:       "large language models",
		ContentType: ContentTypeAny,
		Sort:        SortDateDesc,
		Limit:       20,
		Offset:      5,
	}
	sources := []string{SourceArXiv, SourceOpenAlex, SourceS2}

	key1, err := GenerateCacheKey(params, sources)
	require.NoError(t, err)

	key2, err := GenerateCacheKey(params, sources)
	require.NoError(t, err)

	assert.Equal(t, key1, key2, "GenerateCacheKey must be deterministic")
}

func TestGenerateCacheKeyDoesNotMutateSourcesSlice(t *testing.T) {
	t.Parallel()
	params := SearchParams{Query: "test", Limit: 5}
	sources := []string{SourceS2, SourceArXiv, SourcePubMed}

	original := make([]string, len(sources))
	copy(original, sources)

	_, err := GenerateCacheKey(params, sources)
	require.NoError(t, err)

	assert.Equal(t, original, sources, "GenerateCacheKey must not mutate the caller's sources slice")
}
