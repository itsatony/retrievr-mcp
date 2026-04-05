package internal

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

const (
	mockSourceA = "arxiv"
	mockSourceB = "s2"
	mockSourceC = "openalex"

	testTimeout         = 10 * time.Second
	testShortTimeout    = 50 * time.Millisecond
	testPluginSleepLong = 200 * time.Millisecond

	testDOI1    = "10.1234/test-doi-1"
	testDOI2    = "10.1234/test-doi-2"
	testArXivID = "2401.12345"
)

// ---------------------------------------------------------------------------
// Mock plugin
// ---------------------------------------------------------------------------

// mockPlugin implements SourcePlugin with configurable behavior for testing.
type mockPlugin struct {
	id               string
	name             string
	description      string
	contentTypes     []ContentType
	capabilities     SourceCapabilities
	nativeFormat     ContentFormat
	availableFormats []ContentFormat
	healthState      SourceHealth

	searchFunc func(ctx context.Context, params SearchParams, creds *CallCredentials) (*SearchResult, error)
	getFunc    func(ctx context.Context, id string, include []IncludeField, format ContentFormat, creds *CallCredentials) (*Publication, error)
	initFunc   func(ctx context.Context, cfg PluginConfig) error
}

func (m *mockPlugin) ID() string                            { return m.id }
func (m *mockPlugin) Name() string                          { return m.name }
func (m *mockPlugin) Description() string                   { return m.description }
func (m *mockPlugin) ContentTypes() []ContentType           { return m.contentTypes }
func (m *mockPlugin) Capabilities() SourceCapabilities      { return m.capabilities }
func (m *mockPlugin) NativeFormat() ContentFormat           { return m.nativeFormat }
func (m *mockPlugin) AvailableFormats() []ContentFormat     { return m.availableFormats }
func (m *mockPlugin) Health(_ context.Context) SourceHealth { return m.healthState }

func (m *mockPlugin) Search(ctx context.Context, params SearchParams, creds *CallCredentials) (*SearchResult, error) {
	if m.searchFunc != nil {
		return m.searchFunc(ctx, params, creds)
	}
	return &SearchResult{}, nil
}

func (m *mockPlugin) Get(ctx context.Context, id string, include []IncludeField, format ContentFormat, creds *CallCredentials) (*Publication, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, id, include, format, creds)
	}
	return nil, fmt.Errorf("%w: not implemented", ErrGetFailed)
}

func (m *mockPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	if m.initFunc != nil {
		return m.initFunc(context.Background(), cfg)
	}
	return nil
}

// newMockPlugin creates a mock plugin that returns the given publications on Search.
func newMockPlugin(sourceID string, results []Publication) *mockPlugin {
	return &mockPlugin{
		id:           sourceID,
		name:         sourceID + " (mock)",
		description:  "Mock " + sourceID + " plugin for testing",
		contentTypes: []ContentType{ContentTypePaper},
		capabilities: SourceCapabilities{
			SupportsSortRelevance: true,
			SupportsSortDate:      true,
			SupportsPagination:    true,
			MaxResultsPerQuery:    100,
			NativeFormat:          FormatJSON,
			AvailableFormats:      []ContentFormat{FormatJSON},
		},
		nativeFormat:     FormatJSON,
		availableFormats: []ContentFormat{FormatJSON},
		healthState: SourceHealth{
			Enabled:   true,
			Healthy:   true,
			RateLimit: 10.0,
		},
		searchFunc: func(_ context.Context, _ SearchParams, _ *CallCredentials) (*SearchResult, error) {
			return &SearchResult{
				Total:   len(results),
				Results: results,
				HasMore: false,
			}, nil
		},
		getFunc: func(_ context.Context, id string, _ []IncludeField, _ ContentFormat, _ *CallCredentials) (*Publication, error) {
			for _, pub := range results {
				// Strip prefix for comparison.
				_, rawID, err := ParsePrefixedID(pub.ID)
				if err == nil && rawID == id {
					return &pub, nil
				}
			}
			return nil, fmt.Errorf("%w: not found", ErrGetFailed)
		},
	}
}

// ---------------------------------------------------------------------------
// Test router factory
// ---------------------------------------------------------------------------

// testRouterConfig returns a RouterConfig with sensible test defaults.
func testRouterConfig() RouterConfig {
	return RouterConfig{
		DefaultSources:   []string{mockSourceA, mockSourceB},
		PerSourceTimeout: Duration{Duration: testTimeout},
		DedupEnabled:     true,
		CacheEnabled:     false,
	}
}

// discardLogger returns a logger that discards all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

const (
	testRateLimitRPS   = 1000.0 // high RPS for fast tests
	testRateLimitBurst = 100    // high burst for concurrent tests
)

// testRateLimits creates a SourceRateLimitManager with all given plugin IDs registered
// at high limits so rate limiting doesn't slow down tests.
func testRateLimits(plugins map[string]SourcePlugin) *SourceRateLimitManager {
	mgr := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	for id := range plugins {
		mgr.Register(RateLimiterConfig{
			SourceID:          id,
			RequestsPerSecond: testRateLimitRPS,
			Burst:             testRateLimitBurst,
		})
	}
	return mgr
}

// testRouter creates a Router with test defaults.
func testRouter(plugins map[string]SourcePlugin) *Router {
	return NewRouter(
		testRouterConfig(),
		plugins,
		nil, // no server defaults
		nil, // no cache
		testRateLimits(plugins),
		&CredentialResolver{},
		nil, // no metrics
		discardLogger(),
	)
}

// testRouterWithCache creates a Router with an enabled cache.
func testRouterWithCache(plugins map[string]SourcePlugin) *Router {
	cfg := testRouterConfig()
	cfg.CacheEnabled = true
	cache := NewCache(CacheConfig{
		MaxEntries: DefaultCacheMaxEntries,
		TTL:        DefaultCacheTTL,
		Enabled:    true,
	}, nil)
	return NewRouter(
		cfg,
		plugins,
		nil,
		cache,
		testRateLimits(plugins),
		&CredentialResolver{},
		nil, // no metrics
		discardLogger(),
	)
}

// testRouterNoDedupNoCache creates a Router with dedup and cache disabled.
func testRouterNoDedupNoCache(plugins map[string]SourcePlugin) *Router {
	cfg := testRouterConfig()
	cfg.DedupEnabled = false
	return NewRouter(
		cfg,
		plugins,
		nil,
		nil,
		testRateLimits(plugins),
		&CredentialResolver{},
		nil, // no metrics
		discardLogger(),
	)
}

// testPub creates a test Publication with the given source, prefixed ID, and DOI.
func testPub(source, id, doi string, citations *int) Publication {
	return Publication{
		ID:            id,
		Source:        source,
		ContentType:   ContentTypePaper,
		Title:         "Test Paper " + id,
		Authors:       []Author{{Name: "Test Author"}},
		Published:     "2024-01-15",
		URL:           "https://example.com/" + id,
		DOI:           doi,
		CitationCount: citations,
	}
}

// intPtr returns a pointer to the given int.
func intPtr(v int) *int { return &v }

// ---------------------------------------------------------------------------
// ParsePrefixedID tests
// ---------------------------------------------------------------------------

func TestParsePrefixedID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantSource string
		wantRawID  string
		wantErr    error
	}{
		{
			name:       "valid arxiv",
			input:      "arxiv:2401.12345",
			wantSource: SourceArXiv,
			wantRawID:  "2401.12345",
		},
		{
			name:       "valid s2",
			input:      "s2:abc123def",
			wantSource: SourceS2,
			wantRawID:  "abc123def",
		},
		{
			name:       "valid huggingface with slash",
			input:      "huggingface:paper/2401.12345",
			wantSource: SourceHuggingFace,
			wantRawID:  "paper/2401.12345",
		},
		{
			name:       "multiple colons — only first splits",
			input:      "arxiv:2401:12345",
			wantSource: SourceArXiv,
			wantRawID:  "2401:12345",
		},
		{
			name:    "no separator",
			input:   "noseparator",
			wantErr: ErrInvalidID,
		},
		{
			name:    "empty source",
			input:   ":12345",
			wantErr: ErrInvalidID,
		},
		{
			name:    "empty rawID",
			input:   "arxiv:",
			wantErr: ErrInvalidID,
		},
		{
			name:    "unknown source",
			input:   "unknown:123",
			wantErr: ErrSourceNotFound,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: ErrInvalidID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sourceID, rawID, err := ParsePrefixedID(tc.input)
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantSource, sourceID)
			assert.Equal(t, tc.wantRawID, rawID)
		})
	}
}

// ---------------------------------------------------------------------------
// Router.Search tests
// ---------------------------------------------------------------------------

func TestRouterSearchSingleSourceSuccess(t *testing.T) {
	t.Parallel()

	pubs := []Publication{
		testPub(mockSourceA, "arxiv:1", testDOI1, intPtr(10)),
		testPub(mockSourceA, "arxiv:2", testDOI2, intPtr(20)),
	}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubs),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA}, nil)

	require.NoError(t, err)
	assert.Len(t, result.Results, 2)
	assert.Contains(t, result.SourcesQueried, mockSourceA)
	assert.Empty(t, result.SourcesFailed)
}

func TestRouterSearchMultiSourceSuccess(t *testing.T) {
	t.Parallel()

	pubsA := []Publication{testPub(mockSourceA, "arxiv:1", "10.1/a", intPtr(5))}
	pubsB := []Publication{testPub(mockSourceB, "s2:1", "10.1/b", intPtr(15))}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubsA),
		mockSourceB: newMockPlugin(mockSourceB, pubsB),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	assert.Len(t, result.Results, 2)
	assert.Len(t, result.SourcesQueried, 2)
	assert.Empty(t, result.SourcesFailed)
}

func TestRouterSearchEmptySourcesUsesDefaults(t *testing.T) {
	t.Parallel()

	pubsA := []Publication{testPub(mockSourceA, "arxiv:1", "", nil)}
	pubsB := []Publication{testPub(mockSourceB, "s2:1", "", nil)}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubsA),
		mockSourceB: newMockPlugin(mockSourceB, pubsB),
	}
	r := testRouter(plugins)

	// Pass nil sources → uses defaultSources.
	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, nil, nil)

	require.NoError(t, err)
	assert.Len(t, result.Results, 2)
	assert.Len(t, result.SourcesQueried, 2)
}

func TestRouterSearchDedupByDOI(t *testing.T) {
	t.Parallel()

	// Both sources return paper with same DOI.
	pubsA := []Publication{testPub(mockSourceA, "arxiv:1", testDOI1, intPtr(10))}
	pubsB := []Publication{testPub(mockSourceB, "s2:1", testDOI1, intPtr(50))}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubsA),
		mockSourceB: newMockPlugin(mockSourceB, pubsB),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortCitations,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	assert.Len(t, result.Results, 1, "duplicate DOI should be deduplicated")

	primary := result.Results[0]
	assert.NotEmpty(t, primary.AlsoFoundIn, "should track the other source")
	// Highest citation count wins.
	require.NotNil(t, primary.CitationCount)
	assert.Equal(t, 50, *primary.CitationCount)
}

func TestRouterSearchDedupByArXivID(t *testing.T) {
	t.Parallel()

	pubA := testPub(mockSourceA, "arxiv:1", "", intPtr(5))
	pubA.ArXivID = testArXivID
	pubB := testPub(mockSourceB, "s2:1", "", intPtr(30))
	pubB.ArXivID = testArXivID

	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, []Publication{pubA}),
		mockSourceB: newMockPlugin(mockSourceB, []Publication{pubB}),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	assert.Len(t, result.Results, 1, "duplicate ArXiv ID should be deduplicated")
	assert.NotEmpty(t, result.Results[0].AlsoFoundIn)
}

func TestRouterSearchDedupDisabled(t *testing.T) {
	t.Parallel()

	// Same DOI from two sources, but dedup is disabled.
	pubsA := []Publication{testPub(mockSourceA, "arxiv:1", testDOI1, intPtr(10))}
	pubsB := []Publication{testPub(mockSourceB, "s2:1", testDOI1, intPtr(50))}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubsA),
		mockSourceB: newMockPlugin(mockSourceB, pubsB),
	}
	r := testRouterNoDedupNoCache(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	assert.Len(t, result.Results, 2, "dedup disabled — both should appear")
}

func TestRouterSearchDedupMergesCitationCountHighestWins(t *testing.T) {
	t.Parallel()

	pubsA := []Publication{testPub(mockSourceA, "arxiv:1", testDOI1, intPtr(10))}
	pubsB := []Publication{testPub(mockSourceB, "s2:1", testDOI1, intPtr(50))}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubsA),
		mockSourceB: newMockPlugin(mockSourceB, pubsB),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.NotNil(t, result.Results[0].CitationCount)
	assert.Equal(t, 50, *result.Results[0].CitationCount)
}

func TestRouterSearchDedupMergesCitationCountNilAndValue(t *testing.T) {
	t.Parallel()

	pubsA := []Publication{testPub(mockSourceA, "arxiv:1", testDOI1, nil)}
	pubsB := []Publication{testPub(mockSourceB, "s2:1", testDOI1, intPtr(42))}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubsA),
		mockSourceB: newMockPlugin(mockSourceB, pubsB),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.NotNil(t, result.Results[0].CitationCount)
	assert.Equal(t, 42, *result.Results[0].CitationCount)
}

func TestRouterSearchDedupMergesSourceMetadata(t *testing.T) {
	t.Parallel()

	pubA := testPub(mockSourceA, "arxiv:1", testDOI1, nil)
	pubA.SourceMetadata = map[string]any{"arxiv_cat": "cs.AI"}
	pubB := testPub(mockSourceB, "s2:1", testDOI1, nil)
	pubB.SourceMetadata = map[string]any{"s2_tldr": "summary"}

	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, []Publication{pubA}),
		mockSourceB: newMockPlugin(mockSourceB, []Publication{pubB}),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	meta := result.Results[0].SourceMetadata
	require.NotNil(t, meta)
	assert.Contains(t, meta, "arxiv_cat")
	assert.Contains(t, meta, "s2_tldr")
}

func TestRouterSearchSortRelevanceRoundRobin(t *testing.T) {
	t.Parallel()

	// Source A returns [A1, A2, A3], Source B returns [B1, B2, B3].
	pubsA := []Publication{
		testPub(mockSourceA, "arxiv:a1", "10.1/a1", nil),
		testPub(mockSourceA, "arxiv:a2", "10.1/a2", nil),
		testPub(mockSourceA, "arxiv:a3", "10.1/a3", nil),
	}
	pubsB := []Publication{
		testPub(mockSourceB, "s2:b1", "10.1/b1", nil),
		testPub(mockSourceB, "s2:b2", "10.1/b2", nil),
		testPub(mockSourceB, "s2:b3", "10.1/b3", nil),
	}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubsA),
		mockSourceB: newMockPlugin(mockSourceB, pubsB),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 6)

	// Round-robin interleaving (alphabetical source order: arxiv before s2):
	// arxiv-rank0, s2-rank0, arxiv-rank1, s2-rank1, arxiv-rank2, s2-rank2
	assert.Equal(t, mockSourceA, result.Results[0].Source)
	assert.Equal(t, mockSourceB, result.Results[1].Source)
	assert.Equal(t, mockSourceA, result.Results[2].Source)
	assert.Equal(t, mockSourceB, result.Results[3].Source)
	assert.Equal(t, mockSourceA, result.Results[4].Source)
	assert.Equal(t, mockSourceB, result.Results[5].Source)
}

func TestRouterSearchSortDateDesc(t *testing.T) {
	t.Parallel()

	pubsA := []Publication{
		{ID: "arxiv:1", Source: mockSourceA, Published: "2024-01-01", DOI: "10.1/a", ContentType: ContentTypePaper},
		{ID: "arxiv:2", Source: mockSourceA, Published: "2024-06-15", DOI: "10.1/b", ContentType: ContentTypePaper},
	}
	pubsB := []Publication{
		{ID: "s2:1", Source: mockSourceB, Published: "2024-03-10", DOI: "10.1/c", ContentType: ContentTypePaper},
	}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubsA),
		mockSourceB: newMockPlugin(mockSourceB, pubsB),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortDateDesc,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 3)
	assert.Equal(t, "2024-06-15", result.Results[0].Published)
	assert.Equal(t, "2024-03-10", result.Results[1].Published)
	assert.Equal(t, "2024-01-01", result.Results[2].Published)
}

func TestRouterSearchSortDateAsc(t *testing.T) {
	t.Parallel()

	pubsA := []Publication{
		{ID: "arxiv:1", Source: mockSourceA, Published: "2024-06-15", DOI: "10.1/a", ContentType: ContentTypePaper},
		{ID: "arxiv:2", Source: mockSourceA, Published: "2024-01-01", DOI: "10.1/b", ContentType: ContentTypePaper},
	}
	pubsB := []Publication{
		{ID: "s2:1", Source: mockSourceB, Published: "2024-03-10", DOI: "10.1/c", ContentType: ContentTypePaper},
	}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubsA),
		mockSourceB: newMockPlugin(mockSourceB, pubsB),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortDateAsc,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 3)
	assert.Equal(t, "2024-01-01", result.Results[0].Published)
	assert.Equal(t, "2024-03-10", result.Results[1].Published)
	assert.Equal(t, "2024-06-15", result.Results[2].Published)
}

func TestRouterSearchSortCitationsNilLast(t *testing.T) {
	t.Parallel()

	pubs := []Publication{
		testPub(mockSourceA, "arxiv:1", "10.1/a", nil),
		testPub(mockSourceA, "arxiv:2", "10.1/b", intPtr(100)),
		testPub(mockSourceA, "arxiv:3", "10.1/c", intPtr(5)),
		testPub(mockSourceA, "arxiv:4", "10.1/d", nil),
	}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubs),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortCitations,
	}, []string{mockSourceA}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 4)
	// 100, 5, nil, nil
	require.NotNil(t, result.Results[0].CitationCount)
	assert.Equal(t, 100, *result.Results[0].CitationCount)
	require.NotNil(t, result.Results[1].CitationCount)
	assert.Equal(t, 5, *result.Results[1].CitationCount)
	assert.Nil(t, result.Results[2].CitationCount)
	assert.Nil(t, result.Results[3].CitationCount)
}

func TestRouterSearchTruncateToLimit(t *testing.T) {
	t.Parallel()

	const resultCount = 10
	const requestLimit = 5
	pubs := make([]Publication, resultCount)
	for i := range resultCount {
		pubs[i] = testPub(mockSourceA, fmt.Sprintf("arxiv:%d", i), fmt.Sprintf("10.1/%d", i), nil)
	}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubs),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: requestLimit, Sort: SortRelevance,
	}, []string{mockSourceA}, nil)

	require.NoError(t, err)
	assert.Len(t, result.Results, requestLimit)
	assert.True(t, result.HasMore)
}

func TestRouterSearchPartialFailure(t *testing.T) {
	t.Parallel()

	pubsA := []Publication{testPub(mockSourceA, "arxiv:1", testDOI1, nil)}
	failPlugin := newMockPlugin(mockSourceB, nil)
	failPlugin.searchFunc = func(_ context.Context, _ SearchParams, _ *CallCredentials) (*SearchResult, error) {
		return nil, fmt.Errorf("upstream error")
	}

	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubsA),
		mockSourceB: failPlugin,
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err, "partial failure should not be a top-level error")
	assert.Len(t, result.Results, 1)
	assert.Contains(t, result.SourcesFailed, mockSourceB)
	assert.Contains(t, result.SourcesQueried, mockSourceA)
}

func TestRouterSearchAllSourcesFail(t *testing.T) {
	t.Parallel()

	failPlugin := func(id string) *mockPlugin {
		mp := newMockPlugin(id, nil)
		mp.searchFunc = func(_ context.Context, _ SearchParams, _ *CallCredentials) (*SearchResult, error) {
			return nil, fmt.Errorf("upstream error")
		}
		return mp
	}

	plugins := map[string]SourcePlugin{
		mockSourceA: failPlugin(mockSourceA),
		mockSourceB: failPlugin(mockSourceB),
	}
	r := testRouter(plugins)

	_, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAllSourcesFailed)
}

func TestRouterSearchSourceTimeout(t *testing.T) {
	t.Parallel()

	slowPlugin := newMockPlugin(mockSourceA, nil)
	slowPlugin.searchFunc = func(ctx context.Context, _ SearchParams, _ *CallCredentials) (*SearchResult, error) {
		select {
		case <-time.After(testPluginSleepLong):
			return &SearchResult{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	fastPub := testPub(mockSourceB, "s2:1", testDOI1, nil)
	plugins := map[string]SourcePlugin{
		mockSourceA: slowPlugin,
		mockSourceB: newMockPlugin(mockSourceB, []Publication{fastPub}),
	}

	cfg := testRouterConfig()
	cfg.PerSourceTimeout = Duration{Duration: testShortTimeout}
	r := NewRouter(cfg, plugins, nil, nil,
		testRateLimits(plugins),
		&CredentialResolver{}, nil, discardLogger())

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA, mockSourceB}, nil)

	require.NoError(t, err)
	assert.Contains(t, result.SourcesFailed, mockSourceA)
	assert.Len(t, result.Results, 1)
}

func TestRouterSearchCacheHit(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	plugin := newMockPlugin(mockSourceA, nil)
	plugin.searchFunc = func(_ context.Context, _ SearchParams, _ *CallCredentials) (*SearchResult, error) {
		callCount.Add(1)
		return &SearchResult{
			Total:   1,
			Results: []Publication{testPub(mockSourceA, "arxiv:1", testDOI1, nil)},
		}, nil
	}

	plugins := map[string]SourcePlugin{mockSourceA: plugin}
	r := testRouterWithCache(plugins)
	params := SearchParams{Query: "test", Limit: 10, Sort: SortRelevance}
	sources := []string{mockSourceA}

	// First call — cache miss.
	result1, err := r.Search(context.Background(), params, sources, nil)
	require.NoError(t, err)
	assert.Len(t, result1.Results, 1)
	assert.Equal(t, int32(1), callCount.Load())

	// Second call — cache hit.
	result2, err := r.Search(context.Background(), params, sources, nil)
	require.NoError(t, err)
	assert.Len(t, result2.Results, 1)
	assert.Equal(t, int32(1), callCount.Load(), "plugin should not be called again on cache hit")
}

func TestRouterSearchCacheDisabled(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	plugin := newMockPlugin(mockSourceA, nil)
	plugin.searchFunc = func(_ context.Context, _ SearchParams, _ *CallCredentials) (*SearchResult, error) {
		callCount.Add(1)
		return &SearchResult{
			Total:   1,
			Results: []Publication{testPub(mockSourceA, "arxiv:1", testDOI1, nil)},
		}, nil
	}

	plugins := map[string]SourcePlugin{mockSourceA: plugin}
	r := testRouter(plugins) // no cache
	params := SearchParams{Query: "test", Limit: 10, Sort: SortRelevance}
	sources := []string{mockSourceA}

	_, _ = r.Search(context.Background(), params, sources, nil)
	_, _ = r.Search(context.Background(), params, sources, nil)
	assert.Equal(t, int32(2), callCount.Load(), "plugin should be called both times without cache")
}

func TestRouterSearchNoValidSources(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, nil),
	}
	r := testRouter(plugins)

	_, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{"nonexistent"}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSearchFailed)
}

func TestRouterSearchLimitMultipliedForHeadroom(t *testing.T) {
	t.Parallel()

	const requestLimit = 5
	var receivedLimit int

	plugin := newMockPlugin(mockSourceA, nil)
	plugin.searchFunc = func(_ context.Context, params SearchParams, _ *CallCredentials) (*SearchResult, error) {
		receivedLimit = params.Limit
		return &SearchResult{}, nil
	}

	plugins := map[string]SourcePlugin{mockSourceA: plugin}
	r := testRouter(plugins)

	_, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: requestLimit, Sort: SortRelevance,
	}, []string{mockSourceA}, nil)

	require.NoError(t, err)
	assert.Equal(t, requestLimit*dedupHeadroomMultiplier, receivedLimit)
}

func TestRouterSearchEmptyResults(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, nil),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{mockSourceA}, nil)

	require.NoError(t, err)
	assert.Empty(t, result.Results)
	assert.Equal(t, 0, result.TotalResults)
	assert.False(t, result.HasMore)
}

// ---------------------------------------------------------------------------
// Router.Get tests
// ---------------------------------------------------------------------------

func TestRouterGetSuccess(t *testing.T) {
	t.Parallel()

	pub := testPub(mockSourceA, "arxiv:2401.12345", testDOI1, intPtr(10))
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, []Publication{pub}),
	}
	r := testRouter(plugins)

	result, err := r.Get(context.Background(), "arxiv:2401.12345",
		[]IncludeField{IncludeAbstract}, FormatNative, nil)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, pub.Title, result.Title)
}

func TestRouterGetInvalidIDFormat(t *testing.T) {
	t.Parallel()

	r := testRouter(map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, nil),
	})

	_, err := r.Get(context.Background(), "nocolon", nil, FormatNative, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidID)
}

func TestRouterGetUnknownSource(t *testing.T) {
	t.Parallel()

	r := testRouter(map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, nil),
	})

	_, err := r.Get(context.Background(), "unknown:123", nil, FormatNative, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSourceNotFound)
}

func TestRouterGetPluginNotRegistered(t *testing.T) {
	t.Parallel()

	// s2 is a valid source ID but no plugin registered for it.
	r := testRouter(map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, nil),
	})

	_, err := r.Get(context.Background(), "s2:abc123", nil, FormatNative, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSourceNotFound)
}

func TestRouterGetPluginError(t *testing.T) {
	t.Parallel()

	failPlugin := newMockPlugin(mockSourceA, nil)
	failPlugin.getFunc = func(_ context.Context, _ string, _ []IncludeField, _ ContentFormat, _ *CallCredentials) (*Publication, error) {
		return nil, fmt.Errorf("upstream failure")
	}

	r := testRouter(map[string]SourcePlugin{mockSourceA: failPlugin})

	_, err := r.Get(context.Background(), "arxiv:123", nil, FormatNative, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGetFailed)
}

func TestRouterGetPrefixStripped(t *testing.T) {
	t.Parallel()

	var receivedID string
	plugin := newMockPlugin(mockSourceA, nil)
	plugin.getFunc = func(_ context.Context, id string, _ []IncludeField, _ ContentFormat, _ *CallCredentials) (*Publication, error) {
		receivedID = id
		return &Publication{ID: "arxiv:" + id, Source: mockSourceA}, nil
	}

	r := testRouter(map[string]SourcePlugin{mockSourceA: plugin})

	_, err := r.Get(context.Background(), "arxiv:2401.12345", nil, FormatNative, nil)
	require.NoError(t, err)
	assert.Equal(t, "2401.12345", receivedID, "plugin should receive raw ID without prefix")
}

func TestRouterGetBibTeXFormat(t *testing.T) {
	t.Parallel()

	var receivedFormat ContentFormat
	plugin := newMockPlugin(mockSourceA, nil)
	plugin.getFunc = func(_ context.Context, _ string, _ []IncludeField, format ContentFormat, _ *CallCredentials) (*Publication, error) {
		receivedFormat = format
		return &Publication{
			ID:          "arxiv:2401.12345",
			Source:      mockSourceA,
			ContentType: ContentTypePaper,
			Title:       "Test Paper on Attention",
			Authors:     []Author{{Name: "Alice Smith"}},
			Published:   "2024-06-15",
			ArXivID:     testArXivID,
			Categories:  []string{"cs.AI"},
		}, nil
	}

	r := testRouter(map[string]SourcePlugin{mockSourceA: plugin})

	pub, err := r.Get(context.Background(), "arxiv:2401.12345", nil, FormatBibTeX, nil)
	require.NoError(t, err)
	require.NotNil(t, pub)

	// Plugin must receive FormatNative (Router intercepts BibTeX).
	assert.Equal(t, FormatNative, receivedFormat,
		"plugin should receive FormatNative when BibTeX is requested")

	// Router must generate BibTeX in FullText.
	require.NotNil(t, pub.FullText)
	assert.Equal(t, FormatBibTeX, pub.FullText.ContentFormat)
	assert.True(t, len(pub.FullText.Content) > 0)
	assert.Contains(t, pub.FullText.Content, bibtexEntryArticle+bibtexEntryOpen)
	assert.Contains(t, pub.FullText.Content, bibtexFieldAuthor+bibtexFieldAssign)
	assert.Contains(t, pub.FullText.Content, bibtexFieldTitle+bibtexFieldAssign)
	assert.Contains(t, pub.FullText.Content, bibtexFieldEprint+bibtexFieldAssign+testArXivID)
	assert.Equal(t, len(pub.FullText.Content), pub.FullText.ContentLength)
	assert.False(t, pub.FullText.Truncated)
}

func TestRouterGetBibTeXMetrics(t *testing.T) {
	t.Parallel()

	plugin := newMockPlugin(mockSourceA, nil)
	plugin.getFunc = func(_ context.Context, _ string, _ []IncludeField, _ ContentFormat, _ *CallCredentials) (*Publication, error) {
		return &Publication{
			ID:          "arxiv:2401.12345",
			Source:      mockSourceA,
			ContentType: ContentTypePaper,
			Title:       "Test Paper",
			Published:   "2024-06-15",
		}, nil
	}

	metrics := NewMetrics()
	cfg := testRouterConfig()
	plugins := map[string]SourcePlugin{mockSourceA: plugin}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, metrics, discardLogger())

	_, err := r.Get(context.Background(), "arxiv:2401.12345", nil, FormatBibTeX, nil)
	require.NoError(t, err)

	// Verify metrics were recorded.
	families, gatherErr := metrics.Registry.Gather()
	require.NoError(t, gatherErr)

	var foundGetTotal bool
	for _, f := range families {
		if f.GetName() == metricsNamespace+"_"+metricGetTotal {
			foundGetTotal = true
			require.NotEmpty(t, f.GetMetric())
		}
	}
	assert.True(t, foundGetTotal, "get_total metric should be recorded")
}

// ---------------------------------------------------------------------------
// Router.ListSources tests
// ---------------------------------------------------------------------------

func TestRouterListSourcesReturnsAllPlugins(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, nil),
		mockSourceB: newMockPlugin(mockSourceB, nil),
		mockSourceC: newMockPlugin(mockSourceC, nil),
	}
	r := testRouter(plugins)

	infos := r.ListSources(context.Background())
	assert.Len(t, infos, 3)
}

func TestRouterListSourcesFieldsPopulated(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, nil),
	}
	r := testRouter(plugins)

	infos := r.ListSources(context.Background())
	require.Len(t, infos, 1)

	info := infos[0]
	assert.Equal(t, mockSourceA, info.ID)
	assert.NotEmpty(t, info.Name)
	assert.NotEmpty(t, info.Description)
	assert.True(t, info.Enabled)
	assert.NotEmpty(t, info.ContentTypes)
	assert.NotEmpty(t, info.NativeFormat)
	assert.NotEmpty(t, info.AvailableFormats)
}

func TestRouterListSourcesSortedByID(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		mockSourceC: newMockPlugin(mockSourceC, nil),
		mockSourceA: newMockPlugin(mockSourceA, nil),
		mockSourceB: newMockPlugin(mockSourceB, nil),
	}
	r := testRouter(plugins)

	infos := r.ListSources(context.Background())
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.ID
	}
	assert.True(t, sort.StringsAreSorted(ids), "sources should be sorted by ID: %v", ids)
}

// ---------------------------------------------------------------------------
// SourceAcceptsCredentials tests
// ---------------------------------------------------------------------------

func TestSourceAcceptsCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sourceID string
		expected bool
	}{
		{SourcePubMed, true},
		{SourceS2, true},
		{SourceOpenAlex, true},
		{SourceHuggingFace, true},
		{SourceArXiv, false},
		{SourceEuropePMC, false},
	}

	for _, tc := range tests {
		t.Run(tc.sourceID, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, SourceAcceptsCredentials(tc.sourceID))
		})
	}
}

// ---------------------------------------------------------------------------
// Dedup helper tests
// ---------------------------------------------------------------------------

func TestMergeCitationCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		primary  *int
		dup      *int
		expected *int
	}{
		{"both nil", nil, nil, nil},
		{"primary nil", nil, intPtr(42), intPtr(42)},
		{"dup nil", intPtr(42), nil, intPtr(42)},
		{"primary higher", intPtr(50), intPtr(10), intPtr(50)},
		{"dup higher", intPtr(10), intPtr(50), intPtr(50)},
		{"equal", intPtr(42), intPtr(42), intPtr(42)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := mergeCitationCount(tc.primary, tc.dup)
			if tc.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, *tc.expected, *result)
			}
		})
	}
}

func TestMergeSourceMetadata(t *testing.T) {
	t.Parallel()

	t.Run("both nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, mergeSourceMetadata(nil, nil))
	})

	t.Run("primary nil", func(t *testing.T) {
		t.Parallel()
		dup := map[string]any{"key": "value"}
		result := mergeSourceMetadata(nil, dup)
		assert.Equal(t, "value", result["key"])
	})

	t.Run("dup nil", func(t *testing.T) {
		t.Parallel()
		primary := map[string]any{"key": "value"}
		result := mergeSourceMetadata(primary, nil)
		assert.Equal(t, "value", result["key"])
	})

	t.Run("merge with primary precedence", func(t *testing.T) {
		t.Parallel()
		primary := map[string]any{"shared": "primary", "a": 1}
		dup := map[string]any{"shared": "dup", "b": 2}
		result := mergeSourceMetadata(primary, dup)
		assert.Equal(t, "primary", result["shared"], "primary should win on conflict")
		assert.Equal(t, 1, result["a"])
		assert.Equal(t, 2, result["b"])
	})
}

// ---------------------------------------------------------------------------
// NewRouter edge cases
// ---------------------------------------------------------------------------

func TestNewRouterNilLogger(t *testing.T) {
	t.Parallel()

	r := NewRouter(testRouterConfig(), nil, nil, nil,
		NewSourceRateLimitManager(DefaultCredentialBucketTTL),
		&CredentialResolver{}, nil, nil)

	// Should not panic — logger is replaced with discard logger.
	assert.NotNil(t, r)
}

func TestNewRouterDefensiveCopy(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, nil),
	}
	r := NewRouter(testRouterConfig(), plugins, nil, nil,
		NewSourceRateLimitManager(DefaultCredentialBucketTTL),
		&CredentialResolver{}, nil, discardLogger())

	// Mutate the original map — should not affect the router.
	delete(plugins, mockSourceA)

	infos := r.ListSources(context.Background())
	assert.Len(t, infos, 1, "router should have its own copy of the plugins map")
}

// ---------------------------------------------------------------------------
// Round-robin interleave edge cases
// ---------------------------------------------------------------------------

func TestRoundRobinInterleaveEmpty(t *testing.T) {
	t.Parallel()

	result := roundRobinInterleave(nil)
	assert.Empty(t, result)
}

func TestRoundRobinInterleaveSingleSource(t *testing.T) {
	t.Parallel()

	pubs := []Publication{
		testPub(mockSourceA, "arxiv:1", "10.1/a", nil),
		testPub(mockSourceA, "arxiv:2", "10.1/b", nil),
	}
	result := roundRobinInterleave(pubs)
	assert.Len(t, result, 2)
	assert.Equal(t, "arxiv:1", result[0].ID)
	assert.Equal(t, "arxiv:2", result[1].ID)
}

func TestRoundRobinInterleaveUnevenSources(t *testing.T) {
	t.Parallel()

	pubs := []Publication{
		testPub(mockSourceA, "arxiv:1", "10.1/a1", nil),
		testPub(mockSourceA, "arxiv:2", "10.1/a2", nil),
		testPub(mockSourceA, "arxiv:3", "10.1/a3", nil),
		testPub(mockSourceB, "s2:1", "10.1/b1", nil),
	}
	result := roundRobinInterleave(pubs)
	require.Len(t, result, 4)

	// arxiv-rank0, s2-rank0, arxiv-rank1, arxiv-rank2
	assert.Equal(t, mockSourceA, result[0].Source)
	assert.Equal(t, mockSourceB, result[1].Source)
	assert.Equal(t, mockSourceA, result[2].Source)
	assert.Equal(t, mockSourceA, result[3].Source)
}

// ---------------------------------------------------------------------------
// Concurrent search safety
// ---------------------------------------------------------------------------

func TestRouterSearchConcurrent(t *testing.T) {
	t.Parallel()

	const goroutines = 20

	pubs := []Publication{testPub(mockSourceA, "arxiv:1", testDOI1, intPtr(10))}
	plugins := map[string]SourcePlugin{
		mockSourceA: newMockPlugin(mockSourceA, pubs),
	}
	r := testRouter(plugins)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make([]error, goroutines)

	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = r.Search(context.Background(), SearchParams{
				Query: "test", Limit: 10, Sort: SortRelevance,
			}, []string{mockSourceA}, nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d failed", i)
	}
}
