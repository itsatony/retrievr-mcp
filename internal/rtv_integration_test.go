//go:build integration

package internal

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	integrationS2RPS      = 5.0
	integrationS2Burst    = 5
	integrationOARPS      = 10.0
	integrationOABurst    = 5
	integrationEMCRPS     = 10.0
	integrationEMCBurst   = 5

	integrationOAEmail = "contact@vaudience.ai"

	// Environment variable names for optional API keys.
	integrationEnvS2APIKey  = "RETRIEVR_S2_API_KEY"
	integrationEnvADSAPIKey = "RETRIEVR_ADS_API_KEY"

	// v2.7.0 smart-filter integration env keys. Each test skips when its
	// key is unset, so a partial credential set still produces a green
	// `go test -tags=integration` run for the providers we have keys for.
	integrationEnvBraveKey     = "RETRIEVR_BRAVE_API_KEY"
	integrationEnvExaKey       = "RETRIEVR_EXA_API_KEY"
	integrationEnvYouTubeKey   = "RETRIEVR_YOUTUBE_API_KEY"
	integrationEnvRedditKey    = "RETRIEVR_REDDIT_API_KEY"
	integrationEnvEuropeanaKey = "RETRIEVR_EUROPEANA_API_KEY"
	// Mastodon and Bluesky search require no credentials on v4+ public
	// instances / public ATProto endpoints, so no env key is required.

	// Stable channel id used in YouTube channel-filter live test
	// (TechWorld with Nana — long-lived k8s educational channel).
	integrationYouTubeStableChannelID = "UCdngmbVKX1Tgre699-XLlUA"
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
	})
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
		APIKey:    os.Getenv(integrationEnvS2APIKey),
		RateLimit: integrationS2RPS,
	}))

	result, err := plugin.Search(ctx, SearchParams{
		Query:       integrationSearchQuery,
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       integrationSearchLimit,
	})

	// S2 has very aggressive anonymous rate limits. If we get a rate limit
	// or auth error, skip rather than fail — this is expected without a
	// valid API key.
	if err != nil && (strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "403")) {
		t.Skipf("S2 rate limited or API key invalid (expected without key): %v", err)
	}

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
	})
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
	})
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
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Results)

	time.Sleep(integrationRateLimitDelay)

	// Get by raw ID (strip prefix).
	rawID := stripSourcePrefix(result.Results[0].ID)
	pub, err := plugin.Get(ctx, rawID, nil, FormatNative)
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
	})
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

	// Use OpenAlex + EuropePMC for multi-source test — both are free and
	// have generous rate limits, unlike S2 which aggressively 429s.
	oaPlugin := &OpenAlexPlugin{}
	require.NoError(t, oaPlugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationOARPS,
		Extra:     map[string]string{oaExtraKeyMailto: integrationOAEmail},
	}))

	emcPlugin := &EuropePMCPlugin{}
	require.NoError(t, emcPlugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationEMCRPS,
	}))

	plugins := map[string]SourcePlugin{
		SourceOpenAlex:  oaPlugin,
		SourceEuropePMC: emcPlugin,
	}

	mgr := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	mgr.Register(RateLimiterConfig{SourceID: SourceOpenAlex, RequestsPerSecond: integrationOARPS, Burst: integrationOABurst})
	mgr.Register(RateLimiterConfig{SourceID: SourceEuropePMC, RequestsPerSecond: integrationEMCRPS, Burst: integrationEMCBurst})
	mgr.Start(DefaultCleanupInterval)
	defer mgr.Stop()

	cfg := RouterConfig{
		DefaultSources:   []string{SourceOpenAlex, SourceEuropePMC},
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
	assert.Contains(t, merged.SourcesQueried, SourceOpenAlex)
	assert.Contains(t, merged.SourcesQueried, SourceEuropePMC)
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

// ---------------------------------------------------------------------------
// TestIntegrationCrossRefSearch
// ---------------------------------------------------------------------------

func TestIntegrationCrossRefSearch(t *testing.T) {
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &CrossRefPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationOARPS,
	}))

	result, err := plugin.Search(ctx, SearchParams{
		Query:       integrationSearchQuery,
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       integrationSearchLimit,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result.Results), 1)

	for _, pub := range result.Results {
		assert.NotEmpty(t, pub.ID)
		assert.True(t, strings.HasPrefix(pub.ID, SourceCrossRef+prefixedIDSeparator))
		assert.NotEmpty(t, pub.Title)
		assert.Equal(t, SourceCrossRef, pub.Source)
		assert.NotEmpty(t, pub.DOI)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationDBLPSearch
// ---------------------------------------------------------------------------

func TestIntegrationDBLPSearch(t *testing.T) {
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &DBLPPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationOARPS,
	}))

	result, err := plugin.Search(ctx, SearchParams{
		Query:       integrationSearchQuery,
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       integrationSearchLimit,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result.Results), 1)

	for _, pub := range result.Results {
		assert.NotEmpty(t, pub.ID)
		assert.True(t, strings.HasPrefix(pub.ID, SourceDBLP+prefixedIDSeparator))
		assert.NotEmpty(t, pub.Title)
		assert.Equal(t, SourceDBLP, pub.Source)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationADSSearch
// ---------------------------------------------------------------------------

func TestIntegrationADSSearch(t *testing.T) {
	apiKey := os.Getenv(integrationEnvADSAPIKey)
	if apiKey == "" {
		t.Skipf("ADS API key not set (%s), skipping", integrationEnvADSAPIKey)
	}

	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &ADSPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		APIKey:    apiKey,
		RateLimit: integrationOARPS,
	}))

	result, err := plugin.Search(ctx, SearchParams{
		Query:       "dark matter halo formation",
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       integrationSearchLimit,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result.Results), 1)

	for _, pub := range result.Results {
		assert.NotEmpty(t, pub.ID)
		assert.True(t, strings.HasPrefix(pub.ID, SourceADS+prefixedIDSeparator))
		assert.NotEmpty(t, pub.Title)
		assert.Equal(t, SourceADS, pub.Source)
	}
}

// ---------------------------------------------------------------------------
// TestIntegrationBioRxivGet
// ---------------------------------------------------------------------------

func TestIntegrationBioRxivGet(t *testing.T) {
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &BioRxivPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		RateLimit: integrationOARPS,
		Extra:     map[string]string{biorxivExtraKeyServers: biorxivServerBiorxiv},
	}))

	// Use a known bioRxiv DOI.
	pub, err := plugin.Get(ctx, "10.1101/2024.01.03.574089", nil, FormatNative)
	if err != nil {
		t.Skipf("bioRxiv get failed (may be rate limited): %v", err)
	}

	require.NotNil(t, pub)
	assert.NotEmpty(t, pub.Title)
	assert.NotEmpty(t, pub.Authors)
	assert.Equal(t, SourceBioRxiv, pub.Source)
}

// ---------------------------------------------------------------------------
// v2.7.0 smart-filter live integration tests
// ---------------------------------------------------------------------------

// TestIntegrationBraveDomainFilter exercises Brave's include_domains. Every
// returned URL must be served by the requested domain (host or sub-host).
func TestIntegrationBraveDomainFilter(t *testing.T) {
	apiKey := os.Getenv(integrationEnvBraveKey)
	if apiKey == "" {
		t.Skipf("set %s to run this test", integrationEnvBraveKey)
	}
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &BravePlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{Enabled: true, APIKey: apiKey, RateLimit: 1}))

	res, err := plugin.Search(ctx, SearchParams{
		Query: "ingress controller",
		Limit: 5,
		Filters: SearchFilters{
			IncludeDomains: []string{"kubernetes.io"},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Results)
	for _, pub := range res.Results {
		assert.Contains(t, pub.URL, "kubernetes.io", "every URL must be from the included domain")
	}
}

// TestIntegrationBraveDateFilter exercises the freshness wiring.
func TestIntegrationBraveDateFilter(t *testing.T) {
	apiKey := os.Getenv(integrationEnvBraveKey)
	if apiKey == "" {
		t.Skipf("set %s to run this test", integrationEnvBraveKey)
	}
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &BravePlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{Enabled: true, APIKey: apiKey, RateLimit: 1}))

	from := time.Now().AddDate(0, -1, 0).Format(time.DateOnly)
	res, err := plugin.Search(ctx, SearchParams{
		Query:   "kubernetes release",
		Limit:   5,
		Filters: SearchFilters{DateFrom: from},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Results, "freshness=pm should still surface results for k8s news")
}

// TestIntegrationExaDomainFilter exercises Exa's includeDomains/excludeDomains.
func TestIntegrationExaDomainFilter(t *testing.T) {
	apiKey := os.Getenv(integrationEnvExaKey)
	if apiKey == "" {
		t.Skipf("set %s to run this test", integrationEnvExaKey)
	}
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &ExaPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{Enabled: true, APIKey: apiKey, RateLimit: 1}))

	res, err := plugin.Search(ctx, SearchParams{
		Query: "service mesh",
		Limit: 5,
		Filters: SearchFilters{
			IncludeDomains: []string{"kubernetes.io"},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Results)
	for _, pub := range res.Results {
		assert.Contains(t, pub.URL, "kubernetes.io")
	}
}

// TestIntegrationYouTubeChannelFilter exercises channelId scoping. Every
// result's channel ID must match the requested channel.
func TestIntegrationYouTubeChannelFilter(t *testing.T) {
	apiKey := os.Getenv(integrationEnvYouTubeKey)
	if apiKey == "" {
		t.Skipf("set %s to run this test", integrationEnvYouTubeKey)
	}
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &YouTubePlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{Enabled: true, APIKey: apiKey, RateLimit: 1}))

	res, err := plugin.Search(ctx, SearchParams{
		Query: "kubernetes",
		Limit: 5,
		Filters: SearchFilters{
			Channels: []string{integrationYouTubeStableChannelID},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Results)
	for _, pub := range res.Results {
		// Channel ID stored under SourceMetadata
		channelID, _ := pub.SourceMetadata[smetaChannelID].(string)
		assert.Equal(t, integrationYouTubeStableChannelID, channelID,
			"every result must be from the requested channel")
	}
}

// TestIntegrationRedditSubredditFilter exercises subreddit-path routing.
func TestIntegrationRedditSubredditFilter(t *testing.T) {
	apiKey := os.Getenv(integrationEnvRedditKey)
	if apiKey == "" {
		t.Skipf("set %s to run this test", integrationEnvRedditKey)
	}
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &RedditPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{Enabled: true, APIKey: apiKey, RateLimit: 1}))

	res, err := plugin.Search(ctx, SearchParams{
		Query:   "channels",
		Limit:   5,
		Filters: SearchFilters{Subreddits: []string{"golang"}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Results)
	for _, pub := range res.Results {
		sub, _ := pub.SourceMetadata[smetaSubreddit].(string)
		assert.Equal(t, "golang", sub, "every post must be from r/golang")
	}
}

// TestIntegrationMastodonLanguageFilter exercises the client-side language
// post-filter. mastodon.social returns multilingual content; filtering to
// "de" should leave only German posts (or posts with no language tag).
func TestIntegrationMastodonLanguageFilter(t *testing.T) {
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &MastodonPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{Enabled: true, RateLimit: 5}))

	res, err := plugin.Search(ctx, SearchParams{
		Query:   "kubernetes",
		Limit:   10,
		Filters: SearchFilters{Language: "de"},
	})
	if err != nil {
		t.Skipf("mastodon search failed (likely instance throttling): %v", err)
	}
	for _, pub := range res.Results {
		lang, _ := pub.SourceMetadata[smetaLanguage].(string)
		assert.True(t, MatchesLanguagePrefix(lang, "de"),
			"post language %q must match filter or be empty (fail-open)", lang)
	}
}

// TestIntegrationBlueskyLanguageFilter exercises the lang query param.
func TestIntegrationBlueskyLanguageFilter(t *testing.T) {
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &BlueskyPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{Enabled: true, RateLimit: 5}))

	res, err := plugin.Search(ctx, SearchParams{
		Query:   "kubernetes",
		Limit:   10,
		Filters: SearchFilters{Language: "de"},
	})
	if err != nil {
		t.Skipf("bluesky search failed: %v", err)
	}
	require.NotEmpty(t, res.Results, "bluesky must return at least some matching German posts")
}

// TestIntegrationEuropeanaLanguageFilter exercises the lang query param.
func TestIntegrationEuropeanaLanguageFilter(t *testing.T) {
	apiKey := os.Getenv(integrationEnvEuropeanaKey)
	if apiKey == "" {
		t.Skipf("set %s to run this test", integrationEnvEuropeanaKey)
	}
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &EuropeanaPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{Enabled: true, APIKey: apiKey, RateLimit: 5}))

	res, err := plugin.Search(ctx, SearchParams{
		Query:   "Vermeer",
		Limit:   5,
		Filters: SearchFilters{Language: "nl"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Results)
}

// TestIntegrationStackExchangeSearch exercises a live Stack Exchange query.
// Per project_plan/retrievr_v5.md §5 test plan: "kubernetes ingress" must
// return ≥3 stackoverflow results with QA tags populated.
func TestIntegrationStackExchangeSearch(t *testing.T) {
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	apiKey := os.Getenv("RETRIEVR_STACKEXCHANGE_API_KEY") // optional; lifts daily quota
	plugin := &StackExchangePlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{
		Enabled:   true,
		APIKey:    apiKey,
		RateLimit: 0.5,
		Extra:     map[string]string{stackExchangeExtraDefaultSite: stackExchangeTestSite},
	}))

	res, err := plugin.Search(ctx, SearchParams{
		Query: "kubernetes ingress",
		Limit: 10,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(res.Results), 3, "expected at least 3 SO results for 'kubernetes ingress'")

	for _, pub := range res.Results {
		assert.NotEmpty(t, pub.Title)
		assert.NotEmpty(t, pub.URL)
		assert.NotEmpty(t, pub.SourceMetadata[MetaKeyQAQuestionID], "every QA result must carry a namespaced dedup key")
		assert.Equal(t, stackExchangeTestSite, pub.SourceMetadata[smetaQASite])
		tags := pub.Categories
		assert.NotNil(t, tags, "stackoverflow questions for 'kubernetes ingress' must surface at least one tag")
	}
}

// TestIntegrationHackerNewsSearch exercises a live HN Algolia query.
// Per project_plan/retrievr_v5.md §5 test plan: "rust async" must return
// ≥5 results with score and tags populated.
func TestIntegrationHackerNewsSearch(t *testing.T) {
	time.Sleep(integrationRateLimitDelay)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()

	plugin := &HackerNewsPlugin{}
	require.NoError(t, plugin.Initialize(ctx, PluginConfig{Enabled: true, RateLimit: 5}))

	res, err := plugin.Search(ctx, SearchParams{
		Query: "rust async",
		Limit: 10,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(res.Results), 5, "expected at least 5 HN results for 'rust async'")

	scoreSeen := false
	tagsSeen := false
	for _, pub := range res.Results {
		assert.NotEmpty(t, pub.Title)
		assert.NotEmpty(t, pub.SourceMetadata[MetaKeyQAQuestionID])
		assert.Equal(t, hackerNewsSiteForDedup, pub.SourceMetadata[smetaQASite])
		if v, ok := pub.SourceMetadata[smetaQAScore].(int); ok && v > 0 {
			scoreSeen = true
		}
		if len(pub.Categories) > 0 {
			tagsSeen = true
		}
	}
	assert.True(t, scoreSeen, "at least one HN result must have a non-zero score")
	assert.True(t, tagsSeen, "at least one HN result must have tags")
}

// TestIntegrationListSourcesCapabilities asserts the v2.7.0 capability flags
// are exposed via Router.ListSources() for the providers we wired. No upstream
// calls — local registry only.
func TestIntegrationListSourcesCapabilities(t *testing.T) {
	type sourceCaps struct{ Domain, Channel, Language bool }
	expected := map[string]sourceCaps{
		SourceBrave:              {Domain: true, Channel: false, Language: true},
		SourceExa:                {Domain: true, Channel: false, Language: false},
		SourceYouTube:            {Domain: false, Channel: true, Language: true},
		SourceScrapingdogYouTube: {Domain: false, Channel: true, Language: true},
		SourceReddit:             {Domain: false, Channel: true, Language: false},
		SourceMastodon:           {Domain: false, Channel: false, Language: true},
		SourceBluesky:            {Domain: false, Channel: false, Language: true},
		SourceEuropeana:          {Domain: false, Channel: false, Language: true},
	}
	factories := PluginFactories()
	for id, want := range expected {
		factory, ok := factories[id]
		require.True(t, ok, "plugin %s missing from registry", id)
		caps := factory().Capabilities()
		got := sourceCaps{caps.SupportsDomainFilter, caps.SupportsChannelFilter, caps.SupportsLanguageFilter}
		assert.Equal(t, want, got, "capability flags for %s", id)
	}
}
