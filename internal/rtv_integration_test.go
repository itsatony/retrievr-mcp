//go:build integration

package internal

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Integration test constants
// ---------------------------------------------------------------------------

const (
	integrationTimeout        = 30 * time.Second
	integrationRateLimitDelay = 4 * time.Second
	integrationSearchQuery    = "transformer attention mechanism"
	integrationSearchLimit    = 3

	integrationArxivRPS   = 0.33
	integrationArxivBurst = 1
	integrationS2RPS      = 1.0
	integrationS2Burst    = 3
	integrationOARPS      = 10.0
	integrationOABurst    = 5
	integrationEMCRPS     = 10.0
	integrationEMCBurst   = 5

	integrationOAEmail = "test@example.com"
)

// NOTE: Integration tests intentionally omit t.Parallel() because they hit live
// upstream APIs with strict rate limits. Running them concurrently would cause
// rate limit errors and flaky failures. Sequential execution with explicit
// delays between tests is the only reliable approach for live API testing.

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func integrationLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func integrationRouterConfig() RouterConfig {
	return RouterConfig{
		DefaultSources:   []string{SourceArXiv},
		PerSourceTimeout: Duration{Duration: integrationTimeout},
		DedupEnabled:     true,
		CacheEnabled:     false,
	}
}

func integrationRateLimitManager(sourceID string, rps float64, burst int) *SourceRateLimitManager {
	mgr := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	mgr.Register(RateLimiterConfig{
		SourceID:          sourceID,
		RequestsPerSecond: rps,
		Burst:             burst,
	})
	mgr.Start(DefaultCleanupInterval)
	return mgr
}

// ---------------------------------------------------------------------------
// TestIntegrationArXivSearch
// ---------------------------------------------------------------------------

func TestIntegrationArXivSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &ArXivPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationArxivRPS,
	}))

	result, err := plugin.Search(ctx, SearchParams{
		Query:       integrationSearchQuery,
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       integrationSearchLimit,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result.Results), 1)

	for _, pub := range result.Results {
		assert.NotEmpty(t, pub.ID)
		assert.True(t, strings.HasPrefix(pub.ID, SourceArXiv+prefixedIDSeparator))
		assert.NotEmpty(t, pub.Title)
		assert.Equal(t, SourceArXiv, pub.Source)
		assert.NotEmpty(t, pub.Published)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationS2Search
// ---------------------------------------------------------------------------

func TestIntegrationS2Search(t *testing.T) {
	// Sequential rate limit spacing between live API tests.
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &S2Plugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationS2RPS,
	}))

	result, err := plugin.Search(ctx, SearchParams{
		Query:       integrationSearchQuery,
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       integrationSearchLimit,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result.Results), 1)

	for _, pub := range result.Results {
		assert.NotEmpty(t, pub.ID)
		assert.True(t, strings.HasPrefix(pub.ID, SourceS2+prefixedIDSeparator))
		assert.NotEmpty(t, pub.Title)
		assert.Equal(t, SourceS2, pub.Source)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationOpenAlexSearch
// ---------------------------------------------------------------------------

func TestIntegrationOpenAlexSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &OpenAlexPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationOARPS,
		Extra:     map[string]string{"email": integrationOAEmail},
	}))

	result, err := plugin.Search(ctx, SearchParams{
		Query:       integrationSearchQuery,
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       integrationSearchLimit,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result.Results), 1)

	for _, pub := range result.Results {
		assert.NotEmpty(t, pub.ID)
		assert.True(t, strings.HasPrefix(pub.ID, SourceOpenAlex+prefixedIDSeparator))
		assert.NotEmpty(t, pub.Title)
		assert.Equal(t, SourceOpenAlex, pub.Source)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationEuropePMCSearch
// ---------------------------------------------------------------------------

func TestIntegrationEuropePMCSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &EuropePMCPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationEMCRPS,
	}))

	result, err := plugin.Search(ctx, SearchParams{
		Query:       integrationSearchQuery,
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       integrationSearchLimit,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result.Results), 1)

	for _, pub := range result.Results {
		assert.NotEmpty(t, pub.ID)
		assert.True(t, strings.HasPrefix(pub.ID, SourceEuropePMC+prefixedIDSeparator))
		assert.NotEmpty(t, pub.Title)
		assert.Equal(t, SourceEuropePMC, pub.Source)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationArXivGet
// ---------------------------------------------------------------------------

func TestIntegrationArXivGet(t *testing.T) {
	// Sequential rate limit spacing between live API tests.
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &ArXivPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationArxivRPS,
	}))

	// First search to get a real ID.
	result, err := plugin.Search(ctx, SearchParams{
		Query:       integrationSearchQuery,
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       1,
	}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, result.Results)

	time.Sleep(integrationRateLimitDelay)

	// Get by raw ID (strip prefix).
	rawID := stripSourcePrefix(result.Results[0].ID)
	pub, err := plugin.Get(ctx, rawID, nil, FormatNative, nil)
	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.NotEmpty(t, pub.Title)
	assert.NotEmpty(t, pub.Authors)
	assert.NotEmpty(t, pub.Published)
	assert.NotEmpty(t, pub.Abstract)
}

// ---------------------------------------------------------------------------
// TestIntegrationBibTeXGet
// ---------------------------------------------------------------------------

func TestIntegrationBibTeXGet(t *testing.T) {
	// Sequential rate limit spacing between live API tests.
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// Use ArXiv for BibTeX test.
	plugin := &ArXivPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationArxivRPS,
	}))

	// Search for a paper.
	result, err := plugin.Search(ctx, SearchParams{
		Query:       integrationSearchQuery,
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       1,
	}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, result.Results)

	time.Sleep(integrationRateLimitDelay)

	// Get the paper via Router (which handles BibTeX centrally).
	rateLimits := integrationRateLimitManager(SourceArXiv, integrationArxivRPS, integrationArxivBurst)
	defer rateLimits.Stop()

	plugins := map[string]SourcePlugin{SourceArXiv: plugin}
	router := NewRouter(
		integrationRouterConfig(),
		plugins,
		nil,
		nil,
		rateLimits,
		&CredentialResolver{},
		nil,
		integrationLogger(),
	)

	pub, err := router.Get(ctx, result.Results[0].ID, nil, FormatBibTeX, nil)
	require.NoError(t, err)
	require.NotNil(t, pub)
	require.NotNil(t, pub.FullText)
	assert.Equal(t, FormatBibTeX, pub.FullText.ContentFormat)
	assert.True(t, strings.HasPrefix(pub.FullText.Content, bibtexEntryArticle+bibtexEntryOpen))
	assert.Contains(t, pub.FullText.Content, bibtexFieldAuthor+bibtexFieldAssign)
	assert.Contains(t, pub.FullText.Content, bibtexFieldTitle+bibtexFieldAssign)
	assert.Contains(t, pub.FullText.Content, bibtexFieldYear+bibtexFieldAssign)
	assert.Contains(t, pub.FullText.Content, bibtexFieldEprint+bibtexFieldAssign)
}

// ---------------------------------------------------------------------------
// TestIntegrationMultiSourceSearch
// ---------------------------------------------------------------------------

func TestIntegrationMultiSourceSearch(t *testing.T) {
	// Sequential rate limit spacing between live API tests.
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	// Initialize S2 and OpenAlex (free, fast sources).
	s2Plugin := &S2Plugin{}
	require.NoError(t, s2Plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationS2RPS,
	}))

	oaPlugin := &OpenAlexPlugin{}
	require.NoError(t, oaPlugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationOARPS,
		Extra:     map[string]string{"email": integrationOAEmail},
	}))

	plugins := map[string]SourcePlugin{
		SourceS2:       s2Plugin,
		SourceOpenAlex: oaPlugin,
	}

	mgr := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	mgr.Register(RateLimiterConfig{SourceID: SourceS2, RequestsPerSecond: integrationS2RPS, Burst: integrationS2Burst})
	mgr.Register(RateLimiterConfig{SourceID: SourceOpenAlex, RequestsPerSecond: integrationOARPS, Burst: integrationOABurst})
	mgr.Start(DefaultCleanupInterval)
	defer mgr.Stop()

	cfg := RouterConfig{
		DefaultSources:   []string{SourceS2, SourceOpenAlex},
		PerSourceTimeout: Duration{Duration: integrationTimeout},
		DedupEnabled:     true,
		CacheEnabled:     false,
	}

	router := NewRouter(cfg, plugins, nil, nil, mgr, &CredentialResolver{}, nil, integrationLogger())

	merged, err := router.Search(ctx, SearchParams{
		Query:       integrationSearchQuery,
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       integrationSearchLimit * 2,
	}, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, merged)
	assert.GreaterOrEqual(t, len(merged.Results), 1)
	assert.Contains(t, merged.SourcesQueried, SourceS2)
	assert.Contains(t, merged.SourcesQueried, SourceOpenAlex)
	assert.Empty(t, merged.SourcesFailed)

	// Verify results come from multiple sources.
	sources := make(map[string]bool)
	for _, pub := range merged.Results {
		sources[pub.Source] = true
	}
	assert.Greater(t, len(sources), 1, "expected results from multiple sources")
}

// ---------------------------------------------------------------------------
// TestIntegrationMetricsEndpoint
// ---------------------------------------------------------------------------

func TestIntegrationMetricsEndpoint(t *testing.T) {
	metrics := NewMetrics()
	require.NotNil(t, metrics)

	// Record some data so metrics are populated.
	metrics.RecordSearch(SourceArXiv, metricStatusSuccess, time.Second)
	metrics.RecordGet(SourceArXiv, metricStatusSuccess)
	metrics.RecordCacheHit()
	metrics.RecordRateLimitWait(SourceArXiv)

	// Build a minimal server config for the test.
	SetVersionForTesting("0.11.0-test", "test", "test")
	t.Cleanup(ResetVersionForTesting)

	cfg := &Config{
		Server: ServerConfig{
			Name:      DefaultServerName,
			HTTPAddr:  ":0",
			LogLevel:  LogLevelInfo,
			LogFormat: LogFormatJSON,
		},
		Router: RouterConfig{
			DefaultSources:   []string{SourceArXiv},
			PerSourceTimeout: Duration{Duration: integrationTimeout},
		},
		Sources: map[string]PluginConfig{
			SourceArXiv: {Enabled: true},
		},
	}

	router := NewRouter(cfg.Router, nil, nil, nil, nil, &CredentialResolver{}, metrics, integrationLogger())
	srv := NewServer(cfg, router, nil, metrics, integrationLogger())

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// GET /metrics
	resp, err := http.Get(ts.URL + metricsEndpointPath)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	bodyStr := string(body)

	// Verify Prometheus text format contains our metrics.
	assert.Contains(t, bodyStr, metricsNamespace+"_"+metricSearchTotal)
	assert.Contains(t, bodyStr, metricsNamespace+"_"+metricSearchDurationSeconds)
	assert.Contains(t, bodyStr, metricsNamespace+"_"+metricGetTotal)
	assert.Contains(t, bodyStr, metricsNamespace+"_"+metricRateLimitWaitsTotal)
	assert.Contains(t, bodyStr, metricsNamespace+"_"+metricCacheHitsTotal)
	assert.Contains(t, bodyStr, metricsNamespace+"_"+metricCacheMissesTotal)

	// GET /health should still work.
	healthResp, err := http.Get(ts.URL + healthEndpointPath)
	require.NoError(t, err)
	defer healthResp.Body.Close()
	assert.Equal(t, http.StatusOK, healthResp.StatusCode)

	var health healthResponse
	require.NoError(t, json.NewDecoder(healthResp.Body).Decode(&health))
	assert.Equal(t, healthStatusOK, health.Status)
}
