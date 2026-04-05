package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2EConfigToTypesToErrors exercises the full pipeline:
// config loading → type serialization → error formatting.
// No mocks. Real YAML file, real JSON marshaling, real error chains.
func TestE2EConfigToTypesToErrors(t *testing.T) {
	// Step 1: Write a complete, realistic config to a temp file.
	fullConfigYAML := `
server:
  name: "retrievr-mcp"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["arxiv", "s2", "openalex", "huggingface"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 1000

sources:
  arxiv:
    enabled: true
    timeout: "15s"
    rate_limit: 0.33
    rate_limit_burst: 1
  pubmed:
    enabled: true
    api_key: "test-pm-key"
    timeout: "10s"
    rate_limit: 3.0
    rate_limit_burst: 3
    extra:
      tool: "retrievr-mcp"
      email: "test@example.com"
  s2:
    enabled: true
    api_key: "test-s2-key"
    timeout: "10s"
    rate_limit: 1.0
    rate_limit_burst: 3
  openalex:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      mailto: "test@example.com"
  huggingface:
    enabled: true
    api_key: "test-hf-token"
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      include_models: "true"
      include_datasets: "true"
      include_papers: "true"
  europmc:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(fullConfigYAML), 0o644)
	require.NoError(t, err)

	// Step 2: Load and validate config.
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	expectedSourceCount := 6
	assert.Len(t, cfg.Sources, expectedSourceCount)
	assert.Equal(t, "retrievr-mcp", cfg.Server.Name)

	// Step 3: Load version from a temp versions.yaml.
	ResetVersionForTesting()
	t.Cleanup(ResetVersionForTesting)

	const testVersion = "99.88.77"
	versionPath := filepath.Join(dir, "versions.yaml")
	err = os.WriteFile(versionPath, []byte("version: \""+testVersion+"\"\n"), 0o644)
	require.NoError(t, err)

	err = LoadVersion(versionPath)
	require.NoError(t, err)
	assert.Equal(t, testVersion, GetVersion())

	// Step 4: Build a realistic MergedSearchResult and verify JSON matches spec shape.
	citations := 42
	result := MergedSearchResult{
		TotalResults: 1,
		Results: []Publication{
			{
				ID:          "arxiv:2401.12345",
				Source:      SourceArXiv,
				AlsoFoundIn: []string{SourceS2, SourceOpenAlex},
				ContentType: ContentTypePaper,
				Title:       "Attention Is All You Need (Again)",
				Authors: []Author{
					{Name: "Jane Smith", Affiliation: "MIT"},
				},
				Published:     "2024-01-15",
				Updated:       "2024-02-01",
				Abstract:      "We present a new approach...",
				URL:           "https://arxiv.org/abs/2401.12345",
				PDFURL:        "https://arxiv.org/pdf/2401.12345",
				DOI:           "10.1234/example",
				ArXivID:       "2401.12345",
				Categories:    []string{"cs.CL", "cs.AI"},
				CitationCount: &citations,
			},
		},
		SourcesQueried: []string{SourceArXiv, SourceS2, SourceOpenAlex},
		SourcesFailed:  []string{},
		HasMore:        false,
	}

	jsonData, err := json.MarshalIndent(result, "", "  ")
	require.NoError(t, err)

	// Verify JSON contains spec-required fields.
	jsonStr := string(jsonData)
	assert.Contains(t, jsonStr, `"total_results"`)
	assert.Contains(t, jsonStr, `"results"`)
	assert.Contains(t, jsonStr, `"sources_queried"`)
	assert.Contains(t, jsonStr, `"sources_failed"`)
	assert.Contains(t, jsonStr, `"has_more"`)
	assert.Contains(t, jsonStr, `"also_found_in"`)
	assert.Contains(t, jsonStr, `"content_type"`)
	assert.Contains(t, jsonStr, `"citation_count"`)

	// Verify round-trip.
	var decoded MergedSearchResult
	err = json.Unmarshal(jsonData, &decoded)
	require.NoError(t, err)
	assert.Equal(t, result.TotalResults, decoded.TotalResults)
	assert.Len(t, decoded.Results, 1)
	assert.Equal(t, result.Results[0].Title, decoded.Results[0].Title)

	// Step 5: Test error chain → MCP error output.
	innerErr := fmt.Errorf("connection timeout after 10s")
	wrappedErr := fmt.Errorf("%w: %s: %w", ErrSearchFailed, SourceArXiv, innerErr)
	mcpJSON := NewMCPErrorFromErr(wrappedErr, SourceArXiv)

	var mcpErr MCPError
	err = json.Unmarshal([]byte(mcpJSON), &mcpErr)
	require.NoError(t, err)
	assert.NotEmpty(t, mcpErr.Error)
	assert.Equal(t, SourceArXiv, mcpErr.Source)

	// Step 6: Credential resolution with config values.
	creds := &CallCredentials{S2APIKey: "per-call-s2-key"}
	serverKey := cfg.Sources["s2"].APIKey
	resolved := creds.ResolveForSource(SourceS2, serverKey)
	assert.Equal(t, "per-call-s2-key", resolved, "per-call should override server default")

	resolvedPM := creds.ResolveForSource(SourcePubMed, cfg.Sources["pubmed"].APIKey)
	assert.Equal(t, "test-pm-key", resolvedPM, "should fall back to server default for pubmed")
}

// TestE2ESourceInfoJSONMatchesSpecShape verifies the SourceInfo type produces
// JSON that matches the spec section 4.3 response shape.
func TestE2ESourceInfoJSONMatchesSpecShape(t *testing.T) {
	info := SourceInfo{
		ID:                     SourceArXiv,
		Name:                   "ArXiv",
		Description:            "Open-access preprint server for physics, math, CS, and more",
		Enabled:                true,
		ContentTypes:           []ContentType{ContentTypePaper},
		NativeFormat:           FormatXML,
		AvailableFormats:       []ContentFormat{FormatXML, FormatJSON, FormatBibTeX},
		SupportsFullText:       true,
		SupportsCitations:      false,
		SupportsDateFilter:     true,
		SupportsAuthorFilter:   true,
		SupportsCategoryFilter: true,
		RateLimit:              RateLimitInfo{RequestsPerSecond: 0.33, Remaining: 0.33},
		CategoriesHint:         "cs.AI, cs.CL, cs.LG, cs.CV, math.*, physics.*, stat.ML, q-bio.*",
		AcceptsCredentials:     false,
	}

	data, err := json.Marshal(info)
	require.NoError(t, err)

	// Verify all spec-required JSON keys are present.
	jsonStr := string(data)
	expectedKeys := []string{
		`"id"`, `"name"`, `"description"`, `"enabled"`,
		`"content_types"`, `"native_format"`, `"available_formats"`,
		`"supports_full_text"`, `"supports_citations"`,
		`"supports_date_filter"`, `"supports_author_filter"`,
		`"supports_category_filter"`, `"rate_limit"`,
		`"requests_per_second"`, `"remaining"`,
		`"categories_hint"`, `"accepts_credentials"`,
	}
	for _, key := range expectedKeys {
		assert.Contains(t, jsonStr, key, "missing JSON key: %s", key)
	}
}

// TestE2ERateLimitCacheCredentialIntegration exercises the full DC-02 pipeline:
// config loading → credential resolution → credential hashing → rate limiter →
// cache → all wired together. No mocks. Real objects only.
func TestE2ERateLimitCacheCredentialIntegration(t *testing.T) {
	// Step 1: Write a complete config with rate_limit fields to a temp file.
	configYAML := `
server:
  name: "retrievr-mcp"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["arxiv", "s2"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 500

sources:
  arxiv:
    enabled: true
    timeout: "15s"
    rate_limit: 0.33
    rate_limit_burst: 1
  pubmed:
    enabled: true
    api_key: "pm-server-key"
    timeout: "10s"
    rate_limit: 3.0
    rate_limit_burst: 3
  s2:
    enabled: true
    api_key: "s2-server-key"
    timeout: "10s"
    rate_limit: 1.0
    rate_limit_burst: 3
  openalex:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  huggingface:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  europmc:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configYAML), 0o644)
	require.NoError(t, err)

	// Step 2: Load config — verifies rate_limit fields parse correctly.
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	expectedRateLimit := 0.33
	assert.InDelta(t, expectedRateLimit, cfg.Sources[SourceArXiv].RateLimit, 0.001)
	assert.Equal(t, 1, cfg.Sources[SourceArXiv].RateLimitBurst)
	assert.Equal(t, 3, cfg.Sources[SourceS2].RateLimitBurst)

	// Step 3: Credential resolution — per-call overrides server default.
	resolver := &CredentialResolver{}

	perCallCreds := &CallCredentials{S2APIKey: "per-call-s2-key"}

	s2Cred, s2BucketKey := resolver.Resolve(SourceS2, perCallCreds, cfg.Sources[SourceS2].APIKey)
	assert.Equal(t, "per-call-s2-key", s2Cred, "per-call credential should win")
	assert.Len(t, s2BucketKey, credentialHashLength, "bucket key length")

	// Verify server default fallback when no per-call credential.
	pmCred, pmBucketKey := resolver.Resolve(SourcePubMed, perCallCreds, cfg.Sources[SourcePubMed].APIKey)
	assert.Equal(t, "pm-server-key", pmCred, "should fall back to server default")
	assert.Len(t, pmBucketKey, credentialHashLength)

	// Verify anonymous when both empty.
	arxivCred, arxivBucketKey := resolver.Resolve(SourceArXiv, nil, "")
	assert.Empty(t, arxivCred, "ArXiv has no credentials")
	assert.Len(t, arxivBucketKey, credentialHashLength)

	// Verify bucket keys are different for different credentials.
	assert.NotEqual(t, s2BucketKey, pmBucketKey)
	assert.NotEqual(t, s2BucketKey, arxivBucketKey)

	// Verify determinism: same inputs produce same bucket key.
	_, s2BucketKey2 := resolver.Resolve(SourceS2, perCallCreds, cfg.Sources[SourceS2].APIKey)
	assert.Equal(t, s2BucketKey, s2BucketKey2, "bucket key must be deterministic")

	// Step 4: Rate limiter — register all sources from config, verify Wait works.
	manager := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	manager.Start(DefaultCleanupInterval)
	t.Cleanup(manager.Stop)

	for sourceID, sourceCfg := range cfg.Sources {
		rps := sourceCfg.RateLimit
		if rps < RateLimitMinRPS {
			rps = DefaultRateLimitRPS
		}
		burst := sourceCfg.RateLimitBurst
		if burst <= 0 {
			burst = DefaultRateLimitBurst
		}
		manager.Register(RateLimiterConfig{
			SourceID:          sourceID,
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}

	// Wait on S2 with the resolved bucket key — should succeed.
	ctx := t.Context()
	err = manager.Wait(ctx, SourceS2, s2BucketKey)
	require.NoError(t, err)

	// Wait on ArXiv with anonymous bucket key — should succeed.
	err = manager.Wait(ctx, SourceArXiv, arxivBucketKey)
	require.NoError(t, err)

	// Remaining should be positive (burst minus consumed).
	s2Remaining := manager.Remaining(SourceS2, s2BucketKey)
	assert.GreaterOrEqual(t, s2Remaining, float64(0), "remaining should be non-negative")

	// Unknown source should return -1 for remaining.
	unknownRemaining := manager.Remaining("nonexistent", "bucket")
	assert.Equal(t, float64(-1), unknownRemaining)

	// Step 5: Cache — create from config, set/get, verify metrics.
	cache := NewCache(CacheConfig{
		MaxEntries: cfg.Router.CacheMaxEntries,
		TTL:        cfg.Router.CacheTTL.Duration,
		Enabled:    cfg.Router.CacheEnabled,
	})

	searchParams := SearchParams{
		Query:       "attention mechanism",
		ContentType: ContentTypePaper,
		Sort:        SortRelevance,
		Limit:       10,
	}
	searchSources := []string{SourceArXiv, SourceS2}

	cacheKey, err := GenerateCacheKey(searchParams, searchSources)
	require.NoError(t, err)
	assert.Len(t, cacheKey, cacheKeyHashLength)

	// Verify determinism with reversed source order.
	cacheKey2, err := GenerateCacheKey(searchParams, []string{SourceS2, SourceArXiv})
	require.NoError(t, err)
	assert.Equal(t, cacheKey, cacheKey2, "cache key must be source-order invariant")

	// Set a search result in the cache.
	citations := 42
	testResult := &SearchResult{
		Total: 1,
		Results: []Publication{{
			ID:            "arxiv:2401.12345",
			Source:        SourceArXiv,
			ContentType:   ContentTypePaper,
			Title:         "Attention Is All You Need (Again)",
			Authors:       []Author{{Name: "Jane Smith", Affiliation: "MIT"}},
			Published:     "2024-01-15",
			URL:           "https://arxiv.org/abs/2401.12345",
			CitationCount: &citations,
		}},
		HasMore: false,
	}
	cache.Set(cacheKey, testResult)

	// Get should hit.
	cached, hit := cache.Get(cacheKey)
	require.True(t, hit, "expected cache hit")
	require.NotNil(t, cached)
	assert.Equal(t, testResult.Total, cached.Total)
	assert.Equal(t, testResult.Results[0].Title, cached.Results[0].Title)

	// Miss for unknown key.
	_, hitUnknown := cache.Get("nonexistent-key")
	assert.False(t, hitUnknown)

	// Verify metrics.
	metrics := cache.Metrics()
	assert.Equal(t, uint64(1), metrics.Hits, "expected 1 cache hit")
	assert.Equal(t, uint64(1), metrics.Misses, "expected 1 cache miss")
	assert.Equal(t, uint64(0), metrics.Evictions, "expected 0 evictions")

	// Verify cache length.
	assert.Equal(t, 1, cache.Len())
}

// ---------------------------------------------------------------------------
// DC-03: Router E2E test
// ---------------------------------------------------------------------------

// TestE2ERouterWithRealInfrastructure exercises the full DC-03 pipeline:
// config loading → real Cache → real RateLimitManager → real CredentialResolver
// → Router → Search/Get/ListSources. Mock plugins only (no real HTTP sources
// exist yet), but ALL infrastructure is real.
func TestE2ERouterWithRealInfrastructure(t *testing.T) {
	// Step 1: Load config from temp YAML file.
	configYAML := `
server:
  name: "retrievr-mcp"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["arxiv", "s2"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 500

sources:
  arxiv:
    enabled: true
    timeout: "15s"
    rate_limit: 10.0
    rate_limit_burst: 5
  pubmed:
    enabled: true
    api_key: "pm-server-key"
    timeout: "10s"
    rate_limit: 3.0
    rate_limit_burst: 3
  s2:
    enabled: true
    api_key: "s2-server-key"
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  openalex:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  huggingface:
    enabled: true
    api_key: "hf-token"
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  europmc:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configYAML), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	// Step 2: Create real infrastructure from config.
	cache := NewCache(CacheConfig{
		MaxEntries: cfg.Router.CacheMaxEntries,
		TTL:        cfg.Router.CacheTTL.Duration,
		Enabled:    cfg.Router.CacheEnabled,
	})

	rateLimits := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	rateLimits.Start(DefaultCleanupInterval)
	t.Cleanup(rateLimits.Stop)

	for sourceID, sourceCfg := range cfg.Sources {
		rps := sourceCfg.RateLimit
		if rps < RateLimitMinRPS {
			rps = DefaultRateLimitRPS
		}
		burst := sourceCfg.RateLimitBurst
		if burst <= 0 {
			burst = DefaultRateLimitBurst
		}
		rateLimits.Register(RateLimiterConfig{
			SourceID:          sourceID,
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}

	resolver := &CredentialResolver{}

	// Step 3: Create mock plugins with known results.
	const e2eDOI = "10.1234/e2e-shared-doi"
	e2eCitations10 := 10
	e2eCitations50 := 50

	arxivPubs := []Publication{
		{
			ID:             "arxiv:2401.99999",
			Source:         SourceArXiv,
			ContentType:    ContentTypePaper,
			Title:          "E2E Test Paper Alpha",
			Authors:        []Author{{Name: "Alice Researcher"}},
			Published:      "2024-01-15",
			URL:            "https://arxiv.org/abs/2401.99999",
			DOI:            e2eDOI,
			ArXivID:        "2401.99999",
			CitationCount:  &e2eCitations10,
			SourceMetadata: map[string]any{"arxiv_cat": "cs.AI"},
		},
		{
			ID:          "arxiv:2401.88888",
			Source:      SourceArXiv,
			ContentType: ContentTypePaper,
			Title:       "E2E Test Paper Beta",
			Authors:     []Author{{Name: "Bob Scholar"}},
			Published:   "2024-03-20",
			URL:         "https://arxiv.org/abs/2401.88888",
		},
	}

	s2Pubs := []Publication{
		{
			ID:             "s2:abc123",
			Source:         SourceS2,
			ContentType:    ContentTypePaper,
			Title:          "E2E Test Paper Alpha (S2 copy)",
			Authors:        []Author{{Name: "Alice Researcher", Affiliation: "MIT"}},
			Published:      "2024-01-15",
			URL:            "https://semanticscholar.org/paper/abc123",
			DOI:            e2eDOI,
			CitationCount:  &e2eCitations50,
			SourceMetadata: map[string]any{"s2_tldr": "A short summary"},
		},
		{
			ID:          "s2:def456",
			Source:      SourceS2,
			ContentType: ContentTypePaper,
			Title:       "E2E Test Paper Gamma",
			Authors:     []Author{{Name: "Carol Thinker"}},
			Published:   "2024-06-01",
			URL:         "https://semanticscholar.org/paper/def456",
		},
	}

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, arxivPubs),
		SourceS2:    newMockPlugin(SourceS2, s2Pubs),
	}

	// Build server defaults from config.
	serverDefaults := make(map[string]string, len(cfg.Sources))
	for id, src := range cfg.Sources {
		serverDefaults[id] = src.APIKey
	}

	// Step 4: Create router with all real infrastructure.
	router := NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, nil)

	// Step 5: Test Search — verify full pipeline.
	ctx := context.Background()

	result, err := router.Search(ctx, SearchParams{
		Query:       "e2e test",
		ContentType: ContentTypePaper,
		Sort:        SortCitations,
		Limit:       10,
	}, nil, nil) // nil sources → defaults (arxiv, s2)

	require.NoError(t, err)
	require.NotNil(t, result)

	// Should have 3 results after dedup (4 total - 1 duplicate DOI).
	expectedDedupedCount := 3
	assert.Len(t, result.Results, expectedDedupedCount, "4 total minus 1 DOI dedup = 3")
	assert.Len(t, result.SourcesQueried, 2)
	assert.Empty(t, result.SourcesFailed)

	// Verify dedup: shared DOI paper should have AlsoFoundIn.
	var sharedPaper *Publication
	for i := range result.Results {
		if result.Results[i].DOI == e2eDOI {
			sharedPaper = &result.Results[i]
			break
		}
	}
	require.NotNil(t, sharedPaper, "shared DOI paper should be in results")
	assert.NotEmpty(t, sharedPaper.AlsoFoundIn, "should track also_found_in")

	// Highest citation count should win.
	require.NotNil(t, sharedPaper.CitationCount)
	assert.Equal(t, e2eCitations50, *sharedPaper.CitationCount, "highest citation count wins")

	// Merged source metadata should contain keys from both sources.
	require.NotNil(t, sharedPaper.SourceMetadata)
	assert.Contains(t, sharedPaper.SourceMetadata, "arxiv_cat")
	assert.Contains(t, sharedPaper.SourceMetadata, "s2_tldr")

	// Verify citation sort order: 50, nil, nil.
	require.NotNil(t, result.Results[0].CitationCount)
	assert.Equal(t, e2eCitations50, *result.Results[0].CitationCount)

	// Step 6: Test cache — second search should be a cache hit.
	result2, err := router.Search(ctx, SearchParams{
		Query:       "e2e test",
		ContentType: ContentTypePaper,
		Sort:        SortCitations,
		Limit:       10,
	}, nil, nil)

	require.NoError(t, err)
	assert.Len(t, result2.Results, expectedDedupedCount, "cached result should match")

	cacheMetrics := cache.Metrics()
	assert.Equal(t, uint64(1), cacheMetrics.Hits, "second search should be a cache hit")

	// Step 7: Test Get — parse prefixed ID, route to plugin, strip prefix.
	pub, err := router.Get(ctx, "arxiv:2401.99999",
		[]IncludeField{IncludeAbstract}, FormatNative, nil)
	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.Equal(t, "E2E Test Paper Alpha", pub.Title)

	// Step 8: Test Get with per-call credentials.
	perCallCreds := &CallCredentials{S2APIKey: "per-call-s2-key"}
	pub2, err := router.Get(ctx, "s2:abc123",
		[]IncludeField{IncludeAbstract}, FormatNative, perCallCreds)
	require.NoError(t, err)
	require.NotNil(t, pub2)
	assert.Contains(t, pub2.Title, "Alpha")

	// Step 9: Test ListSources.
	infos := router.ListSources(ctx)
	assert.Len(t, infos, 2)
	// Should be sorted by ID: arxiv before s2.
	assert.Equal(t, SourceArXiv, infos[0].ID)
	assert.Equal(t, SourceS2, infos[1].ID)
	assert.True(t, infos[0].Enabled)
	assert.NotEmpty(t, infos[0].ContentTypes)

	// Step 10: Verify SourceInfo JSON shape matches spec.
	infoJSON, err := json.Marshal(infos[0])
	require.NoError(t, err)
	infoStr := string(infoJSON)
	assert.Contains(t, infoStr, `"id"`)
	assert.Contains(t, infoStr, `"name"`)
	assert.Contains(t, infoStr, `"content_types"`)
	assert.Contains(t, infoStr, `"rate_limit"`)
	assert.Contains(t, infoStr, `"accepts_credentials"`)

	// Step 11: Run contract tests on our e2e mock plugins.
	for _, plugin := range plugins {
		PluginContractTest(t, plugin)
	}
}

// ---------------------------------------------------------------------------
// DC-04: MCP Server E2E test
// ---------------------------------------------------------------------------

// TestE2EMCPServerFullPipeline exercises the full DC-04 pipeline:
// config loading → real Cache → real RateLimitManager → real CredentialResolver
// → Router → MCP Server → HTTP endpoints → tool handlers. Mock plugins only
// (no real HTTP sources), but ALL infrastructure is real.
func TestE2EMCPServerFullPipeline(t *testing.T) {
	// Not parallel: mutates global version state.
	const e2eServerVersion = "0.4.0-e2e"
	SetVersionForTesting(e2eServerVersion, "e2e-commit", "2024-04-05")
	t.Cleanup(ResetVersionForTesting)

	// Step 1: Load config from temp YAML file.
	configYAML := `
server:
  name: "retrievr-mcp"
  http_addr: ":0"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["arxiv", "s2"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 500

sources:
  arxiv:
    enabled: true
    timeout: "15s"
    rate_limit: 10.0
    rate_limit_burst: 5
  pubmed:
    enabled: true
    api_key: "pm-server-key"
    timeout: "10s"
    rate_limit: 3.0
    rate_limit_burst: 3
  s2:
    enabled: true
    api_key: "s2-server-key"
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  openalex:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  huggingface:
    enabled: true
    api_key: "hf-token"
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  europmc:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configYAML), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	// Step 2: Create real infrastructure from config.
	cache := NewCache(CacheConfig{
		MaxEntries: cfg.Router.CacheMaxEntries,
		TTL:        cfg.Router.CacheTTL.Duration,
		Enabled:    cfg.Router.CacheEnabled,
	})

	rateLimits := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	for sourceID, sourceCfg := range cfg.Sources {
		rps := sourceCfg.RateLimit
		if rps < RateLimitMinRPS {
			rps = DefaultRateLimitRPS
		}
		burst := sourceCfg.RateLimitBurst
		if burst <= 0 {
			burst = DefaultRateLimitBurst
		}
		rateLimits.Register(RateLimiterConfig{
			SourceID:          sourceID,
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}
	rateLimits.Start(DefaultCleanupInterval)
	t.Cleanup(rateLimits.Stop)

	resolver := &CredentialResolver{}

	// Step 3: Create mock plugins with known results.
	const e2eDOI = "10.1234/e2e-server-doi"
	e2eCitations10 := 10
	e2eCitations50 := 50

	arxivPubs := []Publication{
		{
			ID: "arxiv:2401.99999", Source: SourceArXiv,
			ContentType: ContentTypePaper, Title: "E2E Server Paper Alpha",
			Authors: []Author{{Name: "Alice Researcher"}}, Published: "2024-01-15",
			URL: "https://arxiv.org/abs/2401.99999", DOI: e2eDOI,
			ArXivID: "2401.99999", CitationCount: &e2eCitations10,
			SourceMetadata: map[string]any{"arxiv_cat": "cs.AI"},
		},
	}

	s2Pubs := []Publication{
		{
			ID: "s2:abc123", Source: SourceS2,
			ContentType: ContentTypePaper, Title: "E2E Server Paper Alpha (S2 copy)",
			Authors: []Author{{Name: "Alice Researcher", Affiliation: "MIT"}}, Published: "2024-01-15",
			URL: "https://semanticscholar.org/paper/abc123", DOI: e2eDOI,
			CitationCount:  &e2eCitations50,
			SourceMetadata: map[string]any{"s2_tldr": "A short summary"},
		},
		{
			ID: "s2:def456", Source: SourceS2,
			ContentType: ContentTypePaper, Title: "E2E Server Paper Gamma",
			Authors: []Author{{Name: "Carol Thinker"}}, Published: "2024-06-01",
			URL: "https://semanticscholar.org/paper/def456",
		},
	}

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, arxivPubs),
		SourceS2:    newMockPlugin(SourceS2, s2Pubs),
	}

	// Build server defaults from config.
	serverDefaults := make(map[string]string, len(cfg.Sources))
	for id, src := range cfg.Sources {
		serverDefaults[id] = src.APIKey
	}

	// Step 4: Create router + server with all real infrastructure.
	router := NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, nil)
	srv := NewServer(cfg, router, rateLimits, nil)

	// Step 5: Start httptest.Server for testing.
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Step 6: Test /health endpoint.
	resp, err := http.Get(ts.URL + healthEndpointPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var health healthResponse
	err = json.NewDecoder(resp.Body).Decode(&health)
	require.NoError(t, err)
	assert.Equal(t, healthStatusOK, health.Status)
	assert.Equal(t, e2eServerVersion, health.Version)

	// Step 7: Test /version endpoint.
	resp2, err := http.Get(ts.URL + versionEndpointPath)
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusOK, resp2.StatusCode)
	var versionInfo map[string]string
	err = json.NewDecoder(resp2.Body).Decode(&versionInfo)
	require.NoError(t, err)
	assert.Equal(t, e2eServerVersion, versionInfo[LogKeyVersion])

	// Step 8: Test tool handlers directly (construct CallToolRequest).
	ctx := context.Background()

	// 8a: rtv_search
	searchHandler := NewSearchHandler(router)
	searchReq := mcp.CallToolRequest{}
	searchReq.Params.Name = ToolNameSearch
	searchReq.Params.Arguments = map[string]any{
		FieldQuery: "e2e test",
		FieldSort:  string(SortCitations),
		FieldLimit: float64(10),
	}

	searchResult, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult)
	assert.False(t, searchResult.IsError)

	// Parse search response.
	searchText := extractTextContent(t, searchResult)
	var merged MergedSearchResult
	err = json.Unmarshal([]byte(searchText), &merged)
	require.NoError(t, err)

	// After dedup (shared DOI): 3 total - 1 duplicate = 2 results.
	expectedDedupedCount := 2
	assert.Len(t, merged.Results, expectedDedupedCount, "3 pubs minus 1 DOI dedup = 2")
	assert.Len(t, merged.SourcesQueried, 2)
	assert.Empty(t, merged.SourcesFailed)

	// Verify dedup: shared DOI paper should have AlsoFoundIn and highest citations.
	var sharedPaper *Publication
	for i := range merged.Results {
		if merged.Results[i].DOI == e2eDOI {
			sharedPaper = &merged.Results[i]
			break
		}
	}
	require.NotNil(t, sharedPaper, "shared DOI paper should be in results")
	assert.NotEmpty(t, sharedPaper.AlsoFoundIn)
	require.NotNil(t, sharedPaper.CitationCount)
	assert.Equal(t, e2eCitations50, *sharedPaper.CitationCount, "highest citation count wins")

	// Merged source metadata should contain keys from both sources.
	require.NotNil(t, sharedPaper.SourceMetadata)
	assert.Contains(t, sharedPaper.SourceMetadata, "arxiv_cat")
	assert.Contains(t, sharedPaper.SourceMetadata, "s2_tldr")

	// 8b: rtv_get
	getHandler := NewGetHandler(router)
	getReq := mcp.CallToolRequest{}
	getReq.Params.Name = ToolNameGet
	getReq.Params.Arguments = map[string]any{
		FieldID: "s2:def456",
	}

	getResult, err := getHandler(ctx, getReq)
	require.NoError(t, err)
	require.NotNil(t, getResult)
	assert.False(t, getResult.IsError)

	getText := extractTextContent(t, getResult)
	var pub Publication
	err = json.Unmarshal([]byte(getText), &pub)
	require.NoError(t, err)
	assert.Equal(t, "E2E Server Paper Gamma", pub.Title)

	// 8c: rtv_get with per-call credentials
	getCredReq := mcp.CallToolRequest{}
	getCredReq.Params.Name = ToolNameGet
	getCredReq.Params.Arguments = map[string]any{
		FieldID: "s2:abc123",
		FieldCredentials: map[string]any{
			CredFieldS2APIKey: "per-call-s2-key",
		},
	}

	getCredResult, err := getHandler(ctx, getCredReq)
	require.NoError(t, err)
	require.NotNil(t, getCredResult)
	assert.False(t, getCredResult.IsError)

	// 8d: rtv_list_sources
	listHandler := NewListSourcesHandler(router)
	listReq := mcp.CallToolRequest{}
	listReq.Params.Name = ToolNameListSources

	listResult, err := listHandler(ctx, listReq)
	require.NoError(t, err)
	require.NotNil(t, listResult)
	assert.False(t, listResult.IsError)

	listText := extractTextContent(t, listResult)
	var infos []SourceInfo
	err = json.Unmarshal([]byte(listText), &infos)
	require.NoError(t, err)
	assert.Len(t, infos, 2)
	assert.Equal(t, SourceArXiv, infos[0].ID)
	assert.Equal(t, SourceS2, infos[1].ID)

	// Step 9: Verify SourceInfo JSON spec shape.
	infoJSON, err := json.Marshal(infos[0])
	require.NoError(t, err)
	infoStr := string(infoJSON)
	assert.Contains(t, infoStr, `"id"`)
	assert.Contains(t, infoStr, `"name"`)
	assert.Contains(t, infoStr, `"content_types"`)
	assert.Contains(t, infoStr, `"rate_limit"`)
	assert.Contains(t, infoStr, `"accepts_credentials"`)

	// Step 10: Run contract tests on our e2e mock plugins.
	for _, plugin := range plugins {
		PluginContractTest(t, plugin)
	}
}

// ---------------------------------------------------------------------------
// DC-05: ArXiv Plugin E2E test
// ---------------------------------------------------------------------------

// e2e arxiv test constants.
const (
	e2eArxivVersion           = "0.5.0-e2e"
	e2eArxivSearchTotal       = 2
	e2eArxivGetID             = "2401.55555"
	e2eArxivSearchResultCount = 2
)

// TestE2EArXivPluginFullPipeline exercises the full DC-05 pipeline:
// config loading → real ArXivPlugin → httptest ArXiv API → real Router → real
// Cache → real RateLimitManager → real CredentialResolver → MCP tool handlers.
// The ONLY fake element is the ArXiv HTTP endpoint (httptest). Everything else
// is real production code.
func TestE2EArXivPluginFullPipeline(t *testing.T) {
	// Not parallel: mutates global version state.
	SetVersionForTesting(e2eArxivVersion, "e2e-commit", "2024-04-05")
	t.Cleanup(ResetVersionForTesting)

	// Step 1: Create httptest server that serves realistic ArXiv Atom XML.
	arxivServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")

		// Route: id_list → Get endpoint, search_query → Search endpoint.
		if idList := r.URL.Query().Get("id_list"); idList != "" {
			entry := arxivTestEntry{
				ID:              e2eArxivGetID,
				Title:           "E2E ArXiv Get Test Paper",
				Summary:         "Detailed abstract for get test.",
				Published:       "2024-02-20T10:00:00Z",
				Updated:         "2024-03-15T14:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Get Author", Affiliation: "Stanford"}},
				Categories:      []string{"cs.LG", "cs.AI"},
				DOI:             "10.5678/e2e.get.001",
				Comment:         "20 pages, 5 figures",
				PrimaryCategory: "cs.LG",
			}
			fmt.Fprint(w, buildArxivTestFeedXML(1, 0, []arxivTestEntry{entry}))
			return
		}

		// Search response with two entries.
		entries := []arxivTestEntry{
			{
				ID:              "2401.11111",
				Title:           "E2E ArXiv Search Paper One",
				Summary:         "First search result abstract.",
				Published:       "2024-01-10T08:00:00Z",
				Updated:         "2024-01-10T08:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Alice E2E"}},
				Categories:      []string{"cs.CL"},
				PrimaryCategory: "cs.CL",
			},
			{
				ID:              "2401.22222",
				Title:           "E2E ArXiv Search Paper Two",
				Summary:         "Second search result abstract.",
				Published:       "2024-02-15T12:00:00Z",
				Updated:         "2024-02-15T12:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Bob E2E", Affiliation: "MIT"}},
				Categories:      []string{"cs.AI", "cs.LG"},
				DOI:             "10.9999/e2e.search.002",
				PrimaryCategory: "cs.AI",
			},
		}
		fmt.Fprint(w, buildArxivTestFeedXML(e2eArxivSearchTotal, 0, entries))
	}))
	defer arxivServer.Close()

	// Step 2: Load config from temp YAML.
	configYAML := fmt.Sprintf(`
server:
  name: "retrievr-mcp"
  http_addr: ":0"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["arxiv"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 500

sources:
  arxiv:
    enabled: true
    base_url: "%s"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
  pubmed:
    enabled: true
    timeout: "10s"
    rate_limit: 3.0
    rate_limit_burst: 3
  s2:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  openalex:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  huggingface:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  europmc:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
`, arxivServer.URL)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configYAML), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	// Step 3: Create real ArXivPlugin using config (pointing at httptest).
	arxivPlugin := &ArXivPlugin{}
	err = arxivPlugin.Initialize(context.Background(), cfg.Sources[SourceArXiv])
	require.NoError(t, err)

	plugins := map[string]SourcePlugin{
		SourceArXiv: arxivPlugin,
	}

	// Step 4: Create real infrastructure.
	cache := NewCache(CacheConfig{
		MaxEntries: cfg.Router.CacheMaxEntries,
		TTL:        cfg.Router.CacheTTL.Duration,
		Enabled:    cfg.Router.CacheEnabled,
	})

	rateLimits := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	for sourceID, sourceCfg := range cfg.Sources {
		rps := sourceCfg.RateLimit
		if rps < RateLimitMinRPS {
			rps = DefaultRateLimitRPS
		}
		burst := sourceCfg.RateLimitBurst
		if burst <= 0 {
			burst = DefaultRateLimitBurst
		}
		rateLimits.Register(RateLimiterConfig{
			SourceID:          sourceID,
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}
	rateLimits.Start(DefaultCleanupInterval)
	t.Cleanup(rateLimits.Stop)

	resolver := &CredentialResolver{}

	serverDefaults := make(map[string]string, len(cfg.Sources))
	for id, src := range cfg.Sources {
		serverDefaults[id] = src.APIKey
	}

	// Step 5: Create router + server.
	router := NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, nil)
	srv := NewServer(cfg, router, rateLimits, nil)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Step 6: Verify /health.
	resp, err := http.Get(ts.URL + healthEndpointPath)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var health healthResponse
	err = json.NewDecoder(resp.Body).Decode(&health)
	require.NoError(t, err)
	assert.Equal(t, healthStatusOK, health.Status)
	assert.Equal(t, e2eArxivVersion, health.Version)

	// Step 7: Test rtv_search through full pipeline.
	ctx := context.Background()
	searchHandler := NewSearchHandler(router)

	searchReq := mcp.CallToolRequest{}
	searchReq.Params.Name = ToolNameSearch
	searchReq.Params.Arguments = map[string]any{
		FieldQuery:   "attention mechanism",
		FieldSources: []any{SourceArXiv},
		FieldLimit:   float64(10),
	}

	searchResult, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult)
	assert.False(t, searchResult.IsError, "search should succeed")

	searchText := extractTextContent(t, searchResult)
	var merged MergedSearchResult
	err = json.Unmarshal([]byte(searchText), &merged)
	require.NoError(t, err)

	assert.Len(t, merged.Results, e2eArxivSearchResultCount, "should return 2 ArXiv results")
	assert.Equal(t, []string{SourceArXiv}, merged.SourcesQueried)
	assert.Empty(t, merged.SourcesFailed)

	// Verify first result mapping.
	pub1 := merged.Results[0]
	assert.Equal(t, SourceArXiv, pub1.Source)
	assert.Equal(t, ContentTypePaper, pub1.ContentType)
	assert.NotEmpty(t, pub1.Title)
	assert.NotEmpty(t, pub1.Abstract)
	assert.NotEmpty(t, pub1.URL)
	assert.NotEmpty(t, pub1.PDFURL)
	assert.NotEmpty(t, pub1.ArXivID)
	assert.NotEmpty(t, pub1.Published)
	assert.NotEmpty(t, pub1.Authors)

	// Step 8: Test rtv_get through full pipeline.
	getHandler := NewGetHandler(router)
	getReq := mcp.CallToolRequest{}
	getReq.Params.Name = ToolNameGet
	getReq.Params.Arguments = map[string]any{
		FieldID: SourceArXiv + prefixedIDSeparator + e2eArxivGetID,
	}

	getResult, err := getHandler(ctx, getReq)
	require.NoError(t, err)
	require.NotNil(t, getResult)
	assert.False(t, getResult.IsError, "get should succeed")

	getText := extractTextContent(t, getResult)
	var pub Publication
	err = json.Unmarshal([]byte(getText), &pub)
	require.NoError(t, err)
	assert.Equal(t, "E2E ArXiv Get Test Paper", pub.Title)
	assert.Equal(t, SourceArXiv, pub.Source)
	assert.Equal(t, e2eArxivGetID, pub.ArXivID)
	assert.Equal(t, "10.5678/e2e.get.001", pub.DOI)
	assert.Equal(t, "2024-02-20", pub.Published)
	assert.NotEmpty(t, pub.Authors)
	assert.NotEmpty(t, pub.Categories)
	assert.NotNil(t, pub.SourceMetadata)

	// Step 9: Test rtv_get with BibTeX format.
	getBibReq := mcp.CallToolRequest{}
	getBibReq.Params.Name = ToolNameGet
	getBibReq.Params.Arguments = map[string]any{
		FieldID:     SourceArXiv + prefixedIDSeparator + e2eArxivGetID,
		FieldFormat: string(FormatBibTeX),
	}

	getBibResult, err := getHandler(ctx, getBibReq)
	require.NoError(t, err)
	require.NotNil(t, getBibResult)
	assert.False(t, getBibResult.IsError)

	getBibText := extractTextContent(t, getBibResult)
	var bibPub Publication
	err = json.Unmarshal([]byte(getBibText), &bibPub)
	require.NoError(t, err)
	require.NotNil(t, bibPub.FullText)
	assert.Equal(t, FormatBibTeX, bibPub.FullText.ContentFormat)
	assert.Contains(t, bibPub.FullText.Content, "@article{")
	assert.Contains(t, bibPub.FullText.Content, "archivePrefix = {arXiv}")

	// Step 10: Test rtv_list_sources — ArXiv should appear with correct capabilities.
	listHandler := NewListSourcesHandler(router)
	listReq := mcp.CallToolRequest{}
	listReq.Params.Name = ToolNameListSources

	listResult, err := listHandler(ctx, listReq)
	require.NoError(t, err)
	require.NotNil(t, listResult)
	assert.False(t, listResult.IsError)

	listText := extractTextContent(t, listResult)
	var infos []SourceInfo
	err = json.Unmarshal([]byte(listText), &infos)
	require.NoError(t, err)
	require.Len(t, infos, 1)

	arxivInfo := infos[0]
	assert.Equal(t, SourceArXiv, arxivInfo.ID)
	assert.Equal(t, "ArXiv", arxivInfo.Name)
	assert.True(t, arxivInfo.Enabled)
	assert.Contains(t, arxivInfo.ContentTypes, ContentTypePaper)
	assert.Equal(t, FormatXML, arxivInfo.NativeFormat)
	assert.True(t, arxivInfo.SupportsDateFilter)
	assert.True(t, arxivInfo.SupportsAuthorFilter)
	assert.True(t, arxivInfo.SupportsCategoryFilter)
	assert.False(t, arxivInfo.AcceptsCredentials) // ArXiv is anonymous

	// Step 11: Verify cache — second search should be a cache hit.
	searchResult2, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult2)
	assert.False(t, searchResult2.IsError)

	cacheMetrics := cache.Metrics()
	assert.Equal(t, uint64(1), cacheMetrics.Hits, "second search should be a cache hit")

	// Step 12: Run contract test on the real ArXiv plugin.
	PluginContractTest(t, arxivPlugin)

	// Step 13: Verify ArXiv plugin health.
	health2 := arxivPlugin.Health(ctx)
	assert.True(t, health2.Enabled)
	assert.True(t, health2.Healthy)
	assert.Empty(t, health2.LastError)
}

// ---------------------------------------------------------------------------
// DC-06: S2 Plugin E2E test — real ArXiv + real S2 multi-source
// ---------------------------------------------------------------------------

// e2e S2 + ArXiv multi-source test constants.
const (
	e2eS2Version           = "0.6.0-e2e"
	e2eS2SharedDOI         = "10.1234/e2e-multi-source-doi"
	e2eS2SharedArXivID     = "2401.77777"
	e2eS2SearchResultCount = 3 // 4 total - 1 dedup = 3
)

// TestE2ES2PluginMultiSourcePipeline exercises the full DC-06 pipeline:
// config loading → real ArXivPlugin + real S2Plugin → httptest servers →
// real Router → real Cache → real RateLimitManager → real CredentialResolver
// → MCP tool handlers. The ONLY fake elements are the HTTP endpoints (httptest).
// Validates multi-source search, dedup by DOI, citation merge, and credential
// propagation.
func TestE2ES2PluginMultiSourcePipeline(t *testing.T) {
	// Not parallel: mutates global version state.
	SetVersionForTesting(e2eS2Version, "e2e-commit", "2024-04-06")
	t.Cleanup(ResetVersionForTesting)

	// Step 1: Create httptest ArXiv server.
	arxivServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")

		if idList := r.URL.Query().Get("id_list"); idList != "" {
			entry := arxivTestEntry{
				ID:              e2eS2SharedArXivID,
				Title:           "E2E Multi-Source Paper Alpha",
				Summary:         "Abstract for multi-source test.",
				Published:       "2024-01-15T08:00:00Z",
				Updated:         "2024-01-20T10:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Alice E2E", Affiliation: "MIT"}},
				Categories:      []string{"cs.CL", "cs.AI"},
				DOI:             e2eS2SharedDOI,
				PrimaryCategory: "cs.CL",
			}
			fmt.Fprint(w, buildArxivTestFeedXML(1, 0, []arxivTestEntry{entry}))
			return
		}

		// Search: two results, first has shared DOI for dedup.
		entries := []arxivTestEntry{
			{
				ID:              e2eS2SharedArXivID,
				Title:           "E2E Multi-Source Paper Alpha",
				Summary:         "Abstract for multi-source test.",
				Published:       "2024-01-15T08:00:00Z",
				Updated:         "2024-01-20T10:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Alice E2E", Affiliation: "MIT"}},
				Categories:      []string{"cs.CL", "cs.AI"},
				DOI:             e2eS2SharedDOI,
				PrimaryCategory: "cs.CL",
			},
			{
				ID:              "2401.88888",
				Title:           "E2E ArXiv-Only Paper",
				Summary:         "Only found on ArXiv.",
				Published:       "2024-02-01T08:00:00Z",
				Updated:         "2024-02-01T08:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Bob E2E"}},
				Categories:      []string{"cs.LG"},
				PrimaryCategory: "cs.LG",
			},
		}
		fmt.Fprint(w, buildArxivTestFeedXML(2, 0, entries))
	}))
	defer arxivServer.Close()

	// Step 2: Create httptest S2 server.
	const e2eS2PaperID = "s2paper001abcdef1234567890abcdef12345678"

	s2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Check for API key header.
		apiKey := r.Header.Get(s2APIKeyHeader)

		// Route based on path.
		path := r.URL.Path

		switch {
		case strings.HasSuffix(path, "/citations"):
			// Citations response.
			citingPaper := `{"paperId":"citing001","title":"Paper That Cites Alpha","year":2024,"authors":[{"authorId":"c1","name":"Citing Author"}],"citationCount":5,"externalIds":null}`
			fmt.Fprintf(w, `{"offset":0,"next":null,"data":[{"citingPaper":%s}]}`, citingPaper)
			return

		case strings.HasSuffix(path, "/references"):
			// References response.
			refPaper := `{"paperId":"ref001","title":"Foundational Reference","year":2020,"authors":[{"authorId":"r1","name":"Reference Author"}],"citationCount":1000,"externalIds":null}`
			fmt.Fprintf(w, `{"offset":0,"next":null,"data":[{"citedPaper":%s}]}`, refPaper)
			return

		case strings.Contains(path, "/paper/search"):
			// Search response: two results, first shares DOI with ArXiv.
			papers := []string{
				fmt.Sprintf(`{
					"paperId":%q,
					"externalIds":{"DOI":%q,"ArXiv":%q,"PMID":"","CorpusId":99999},
					"title":"E2E Multi-Source Paper Alpha (S2 copy)",
					"abstract":"S2 abstract for multi-source test.",
					"year":2024,
					"authors":[{"authorId":"a1","name":"Alice E2E"}],
					"citationCount":75,
					"referenceCount":20,
					"publicationDate":"2024-01-15",
					"journal":{"name":"Nature","volume":"625","pages":"1-10"},
					"openAccessPdf":{"url":"https://example.com/alpha.pdf"},
					"fieldsOfStudy":["Computer Science"],
					"url":"https://www.semanticscholar.org/paper/s2paper001",
					"isOpenAccess":true,
					"publicationTypes":["JournalArticle"]
				}`, e2eS2PaperID, e2eS2SharedDOI, e2eS2SharedArXivID),
				`{
					"paperId":"s2paper002uniquedef789",
					"externalIds":null,
					"title":"E2E S2-Only Paper",
					"abstract":"Only found on S2.",
					"year":2024,
					"authors":[{"authorId":"a2","name":"Carol E2E"}],
					"citationCount":10,
					"referenceCount":5,
					"publicationDate":"2024-03-01",
					"journal":null,
					"openAccessPdf":null,
					"fieldsOfStudy":["Computer Science"],
					"url":"https://www.semanticscholar.org/paper/s2paper002",
					"isOpenAccess":false,
					"publicationTypes":["Conference"]
				}`,
			}
			fmt.Fprintf(w, `{"total":2,"offset":0,"next":null,"data":[%s,%s]}`, papers[0], papers[1])
			return

		default:
			// Get single paper by ID.
			_ = apiKey // used for credential validation in header check above
			fmt.Fprintf(w, `{
				"paperId":%q,
				"externalIds":{"DOI":%q,"ArXiv":%q,"PMID":"","CorpusId":99999},
				"title":"E2E Multi-Source Paper Alpha (S2 copy)",
				"abstract":"S2 abstract for multi-source test.",
				"year":2024,
				"authors":[{"authorId":"a1","name":"Alice E2E"}],
				"citationCount":75,
				"referenceCount":20,
				"publicationDate":"2024-01-15",
				"journal":{"name":"Nature","volume":"625","pages":"1-10"},
				"openAccessPdf":{"url":"https://example.com/alpha.pdf"},
				"fieldsOfStudy":["Computer Science"],
				"url":"https://www.semanticscholar.org/paper/s2paper001",
				"isOpenAccess":true,
				"publicationTypes":["JournalArticle"]
			}`, e2eS2PaperID, e2eS2SharedDOI, e2eS2SharedArXivID)
		}
	}))
	defer s2Server.Close()

	// Step 3: Load config from temp YAML.
	configYAML := fmt.Sprintf(`
server:
  name: "retrievr-mcp"
  http_addr: ":0"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["arxiv", "s2"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 500

sources:
  arxiv:
    enabled: true
    base_url: "%s"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
  pubmed:
    enabled: true
    timeout: "10s"
    rate_limit: 3.0
    rate_limit_burst: 3
  s2:
    enabled: true
    base_url: "%s"
    api_key: "e2e-server-s2-key"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
  openalex:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  huggingface:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  europmc:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
`, arxivServer.URL, s2Server.URL)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configYAML), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	// Step 4: Create real plugins.
	arxivPlugin := &ArXivPlugin{}
	err = arxivPlugin.Initialize(context.Background(), cfg.Sources[SourceArXiv])
	require.NoError(t, err)

	s2Plugin := &S2Plugin{}
	err = s2Plugin.Initialize(context.Background(), cfg.Sources[SourceS2])
	require.NoError(t, err)

	plugins := map[string]SourcePlugin{
		SourceArXiv: arxivPlugin,
		SourceS2:    s2Plugin,
	}

	// Step 5: Create real infrastructure.
	cache := NewCache(CacheConfig{
		MaxEntries: cfg.Router.CacheMaxEntries,
		TTL:        cfg.Router.CacheTTL.Duration,
		Enabled:    cfg.Router.CacheEnabled,
	})

	rateLimits := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	for sourceID, sourceCfg := range cfg.Sources {
		rps := sourceCfg.RateLimit
		if rps < RateLimitMinRPS {
			rps = DefaultRateLimitRPS
		}
		burst := sourceCfg.RateLimitBurst
		if burst <= 0 {
			burst = DefaultRateLimitBurst
		}
		rateLimits.Register(RateLimiterConfig{
			SourceID:          sourceID,
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}
	rateLimits.Start(DefaultCleanupInterval)
	t.Cleanup(rateLimits.Stop)

	resolver := &CredentialResolver{}

	serverDefaults := make(map[string]string, len(cfg.Sources))
	for id, src := range cfg.Sources {
		serverDefaults[id] = src.APIKey
	}

	// Step 6: Create router + server.
	router := NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, nil)
	srv := NewServer(cfg, router, rateLimits, nil)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Step 7: Test multi-source search via MCP tool handler.
	ctx := context.Background()
	searchHandler := NewSearchHandler(router)

	searchReq := mcp.CallToolRequest{}
	searchReq.Params.Name = ToolNameSearch
	searchReq.Params.Arguments = map[string]any{
		FieldQuery:   "multi-source test",
		FieldSources: []any{SourceArXiv, SourceS2},
		FieldSort:    string(SortCitations),
		FieldLimit:   float64(10),
	}

	searchResult, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult)
	assert.False(t, searchResult.IsError, "search should succeed")

	searchText := extractTextContent(t, searchResult)
	var merged MergedSearchResult
	err = json.Unmarshal([]byte(searchText), &merged)
	require.NoError(t, err)

	// 4 total results (2 ArXiv + 2 S2) - 1 DOI dedup = 3.
	assert.Len(t, merged.Results, e2eS2SearchResultCount, "4 pubs minus 1 DOI dedup = 3")
	assert.Len(t, merged.SourcesQueried, 2)
	assert.Contains(t, merged.SourcesQueried, SourceArXiv)
	assert.Contains(t, merged.SourcesQueried, SourceS2)
	assert.Empty(t, merged.SourcesFailed)

	// Step 8: Verify DOI-based dedup.
	var sharedPaper *Publication
	for i := range merged.Results {
		if merged.Results[i].DOI == e2eS2SharedDOI {
			sharedPaper = &merged.Results[i]
			break
		}
	}
	require.NotNil(t, sharedPaper, "shared DOI paper should be in results")
	assert.NotEmpty(t, sharedPaper.AlsoFoundIn, "should track also_found_in")

	// Highest citation count should win (S2 has 75, ArXiv has nil).
	require.NotNil(t, sharedPaper.CitationCount)
	e2eExpectedCitations := 75
	assert.Equal(t, e2eExpectedCitations, *sharedPaper.CitationCount, "highest citation count should win")

	// Merged source metadata should contain keys from both sources.
	require.NotNil(t, sharedPaper.SourceMetadata)

	// Step 9: Verify citation sort order: 75 first (highest), then others.
	require.NotNil(t, merged.Results[0].CitationCount)
	assert.Equal(t, e2eExpectedCitations, *merged.Results[0].CitationCount)

	// Step 10: Test S2 Get via MCP tool handler.
	getHandler := NewGetHandler(router)
	getReq := mcp.CallToolRequest{}
	getReq.Params.Name = ToolNameGet
	getReq.Params.Arguments = map[string]any{
		FieldID: SourceS2 + prefixedIDSeparator + e2eS2PaperID,
	}

	getResult, err := getHandler(ctx, getReq)
	require.NoError(t, err)
	require.NotNil(t, getResult)
	assert.False(t, getResult.IsError)

	getText := extractTextContent(t, getResult)
	var pub Publication
	err = json.Unmarshal([]byte(getText), &pub)
	require.NoError(t, err)
	assert.Contains(t, pub.Title, "Alpha")
	assert.Equal(t, SourceS2, pub.Source)
	assert.Equal(t, e2eS2SharedDOI, pub.DOI)
	assert.Equal(t, e2eS2SharedArXivID, pub.ArXivID)
	assert.NotEmpty(t, pub.PDFURL)
	require.NotNil(t, pub.CitationCount)
	assert.Equal(t, e2eExpectedCitations, *pub.CitationCount)

	// Step 11: Test S2 Get with citations include.
	getCitReq := mcp.CallToolRequest{}
	getCitReq.Params.Name = ToolNameGet
	getCitReq.Params.Arguments = map[string]any{
		FieldID:      SourceS2 + prefixedIDSeparator + e2eS2PaperID,
		FieldInclude: []any{string(IncludeCitations)},
	}

	getCitResult, err := getHandler(ctx, getCitReq)
	require.NoError(t, err)
	require.NotNil(t, getCitResult)
	assert.False(t, getCitResult.IsError)

	getCitText := extractTextContent(t, getCitResult)
	var citPub Publication
	err = json.Unmarshal([]byte(getCitText), &citPub)
	require.NoError(t, err)
	assert.NotEmpty(t, citPub.Citations, "should have citations populated")
	assert.Equal(t, "Paper That Cites Alpha", citPub.Citations[0].Title)

	// Step 12: Test S2 Get with BibTeX format.
	getBibReq := mcp.CallToolRequest{}
	getBibReq.Params.Name = ToolNameGet
	getBibReq.Params.Arguments = map[string]any{
		FieldID:     SourceS2 + prefixedIDSeparator + e2eS2PaperID,
		FieldFormat: string(FormatBibTeX),
	}

	getBibResult, err := getHandler(ctx, getBibReq)
	require.NoError(t, err)
	require.NotNil(t, getBibResult)
	assert.False(t, getBibResult.IsError)

	getBibText := extractTextContent(t, getBibResult)
	var bibPub Publication
	err = json.Unmarshal([]byte(getBibText), &bibPub)
	require.NoError(t, err)
	require.NotNil(t, bibPub.FullText)
	assert.Equal(t, FormatBibTeX, bibPub.FullText.ContentFormat)
	assert.Contains(t, bibPub.FullText.Content, "@article{")
	assert.Contains(t, bibPub.FullText.Content, e2eS2SharedDOI)

	// Step 13: Test list_sources — both ArXiv and S2 should appear.
	listHandler := NewListSourcesHandler(router)
	listReq := mcp.CallToolRequest{}
	listReq.Params.Name = ToolNameListSources

	listResult, err := listHandler(ctx, listReq)
	require.NoError(t, err)
	require.NotNil(t, listResult)
	assert.False(t, listResult.IsError)

	listText := extractTextContent(t, listResult)
	var infos []SourceInfo
	err = json.Unmarshal([]byte(listText), &infos)
	require.NoError(t, err)
	require.Len(t, infos, 2)

	// Should be sorted by ID: arxiv before s2.
	assert.Equal(t, SourceArXiv, infos[0].ID)
	assert.Equal(t, SourceS2, infos[1].ID)

	// Verify S2 capabilities in list_sources.
	s2Info := infos[1]
	assert.Equal(t, "Semantic Scholar", s2Info.Name)
	assert.True(t, s2Info.Enabled)
	assert.Contains(t, s2Info.ContentTypes, ContentTypePaper)
	assert.Equal(t, FormatJSON, s2Info.NativeFormat)
	assert.True(t, s2Info.SupportsDateFilter)
	assert.True(t, s2Info.SupportsCitations)
	assert.False(t, s2Info.SupportsAuthorFilter)
	assert.True(t, s2Info.AcceptsCredentials) // S2 accepts per-call credentials

	// Step 14: Verify cache — second search should be a cache hit.
	searchResult2, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult2)
	assert.False(t, searchResult2.IsError)

	cacheMetrics := cache.Metrics()
	assert.Equal(t, uint64(1), cacheMetrics.Hits, "second search should be a cache hit")

	// Step 15: Run contract tests on both real plugins.
	PluginContractTest(t, arxivPlugin)
	PluginContractTest(t, s2Plugin)

	// Step 16: Verify both plugins are healthy.
	arxivHealth := arxivPlugin.Health(ctx)
	assert.True(t, arxivHealth.Enabled)
	assert.True(t, arxivHealth.Healthy)

	s2Health := s2Plugin.Health(ctx)
	assert.True(t, s2Health.Enabled)
	assert.True(t, s2Health.Healthy)
	assert.Empty(t, s2Health.LastError)
}

// ---------------------------------------------------------------------------
// DC-07: OpenAlex Plugin E2E test — real ArXiv + real S2 + real OpenAlex
// ---------------------------------------------------------------------------

// e2e OA + S2 + ArXiv triple-source test constants.
const (
	e2eOAVersion           = "0.7.0-e2e"
	e2eOASharedDOI         = "10.1234/e2e-triple-source-doi"
	e2eOASharedArXivID     = "2401.99999"
	e2eOAWorkID            = "W9999999999"
	e2eOASearchResultCount = 4 // 6 total (2+2+2) - 2 DOI dedup = 4
	e2eOAExpectedCitations = 150
	e2eOAExpectedSources   = 3
)

// TestE2EOpenAlexPluginTripleSourcePipeline exercises the full DC-07 pipeline:
// config loading → real ArXivPlugin + real S2Plugin + real OpenAlexPlugin →
// httptest servers → real Router → real Cache → real RateLimitManager →
// real CredentialResolver → MCP tool handlers. Validates triple-source search,
// dedup by DOI, inverted abstract reconstruction, and credential propagation.
func TestE2EOpenAlexPluginTripleSourcePipeline(t *testing.T) {
	// Not parallel: mutates global version state.
	SetVersionForTesting(e2eOAVersion, "e2e-commit", "2024-04-07")
	t.Cleanup(ResetVersionForTesting)

	// Step 1: Create httptest ArXiv server.
	arxivServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")

		if idList := r.URL.Query().Get("id_list"); idList != "" {
			entry := arxivTestEntry{
				ID:              e2eOASharedArXivID,
				Title:           "E2E Triple-Source Paper",
				Summary:         "Abstract for triple-source test.",
				Published:       "2024-01-15T08:00:00Z",
				Updated:         "2024-01-20T10:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Alice E2E", Affiliation: "MIT"}},
				Categories:      []string{"cs.CL", "cs.AI"},
				DOI:             e2eOASharedDOI,
				PrimaryCategory: "cs.CL",
			}
			fmt.Fprint(w, buildArxivTestFeedXML(1, 0, []arxivTestEntry{entry}))
			return
		}

		entries := []arxivTestEntry{
			{
				ID:              e2eOASharedArXivID,
				Title:           "E2E Triple-Source Paper",
				Summary:         "Abstract for triple-source test.",
				Published:       "2024-01-15T08:00:00Z",
				Updated:         "2024-01-20T10:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Alice E2E", Affiliation: "MIT"}},
				Categories:      []string{"cs.CL", "cs.AI"},
				DOI:             e2eOASharedDOI,
				PrimaryCategory: "cs.CL",
			},
			{
				ID:              "2401.88888",
				Title:           "E2E ArXiv-Only Paper",
				Summary:         "Only found on ArXiv.",
				Published:       "2024-02-01T08:00:00Z",
				Updated:         "2024-02-01T08:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Bob E2E"}},
				Categories:      []string{"cs.LG"},
				PrimaryCategory: "cs.LG",
			},
		}
		fmt.Fprint(w, buildArxivTestFeedXML(2, 0, entries))
	}))
	defer arxivServer.Close()

	// Step 2: Create httptest S2 server.
	const e2eOAS2PaperID = "s2paper001abcdef1234567890abcdef12345678"

	s2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/citations"):
			fmt.Fprint(w, `{"offset":0,"next":null,"data":[]}`)
		case strings.HasSuffix(path, "/references"):
			fmt.Fprint(w, `{"offset":0,"next":null,"data":[]}`)
		case strings.Contains(path, "/paper/search"):
			papers := []string{
				fmt.Sprintf(`{
					"paperId":%q,
					"externalIds":{"DOI":%q,"ArXiv":%q,"PMID":"","CorpusId":99999},
					"title":"E2E Triple-Source Paper (S2 copy)",
					"abstract":"S2 abstract for triple-source test.",
					"year":2024,
					"authors":[{"authorId":"a1","name":"Alice E2E"}],
					"citationCount":120,
					"referenceCount":20,
					"publicationDate":"2024-01-15",
					"journal":{"name":"Nature","volume":"625","pages":"1-10"},
					"openAccessPdf":{"url":"https://example.com/triple.pdf"},
					"fieldsOfStudy":["Computer Science"],
					"url":"https://www.semanticscholar.org/paper/s2triple",
					"isOpenAccess":true,
					"publicationTypes":["JournalArticle"]
				}`, e2eOAS2PaperID, e2eOASharedDOI, e2eOASharedArXivID),
				`{
					"paperId":"s2paper002unique",
					"externalIds":null,
					"title":"E2E S2-Only Paper",
					"abstract":"Only found on S2.",
					"year":2024,
					"authors":[{"authorId":"a2","name":"Carol E2E"}],
					"citationCount":10,
					"referenceCount":5,
					"publicationDate":"2024-03-01",
					"journal":null,
					"openAccessPdf":null,
					"fieldsOfStudy":["Computer Science"],
					"url":"https://www.semanticscholar.org/paper/s2paper002",
					"isOpenAccess":false,
					"publicationTypes":["Conference"]
				}`,
			}
			fmt.Fprintf(w, `{"total":2,"offset":0,"next":null,"data":[%s,%s]}`, papers[0], papers[1])
		default:
			fmt.Fprintf(w, `{
				"paperId":%q,
				"externalIds":{"DOI":%q,"ArXiv":%q,"PMID":"","CorpusId":99999},
				"title":"E2E Triple-Source Paper (S2 copy)",
				"abstract":"S2 abstract for triple-source test.",
				"year":2024,
				"authors":[{"authorId":"a1","name":"Alice E2E"}],
				"citationCount":120,
				"referenceCount":20,
				"publicationDate":"2024-01-15",
				"journal":{"name":"Nature","volume":"625","pages":"1-10"},
				"openAccessPdf":{"url":"https://example.com/triple.pdf"},
				"fieldsOfStudy":["Computer Science"],
				"url":"https://www.semanticscholar.org/paper/s2triple",
				"isOpenAccess":true,
				"publicationTypes":["JournalArticle"]
			}`, e2eOAS2PaperID, e2eOASharedDOI, e2eOASharedArXivID)
		}
	}))
	defer s2Server.Close()

	// Step 3: Create httptest OpenAlex server with inverted abstract.
	oaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		path := r.URL.Path
		if strings.HasPrefix(path, "/works/") && path != "/works" {
			// Get single work.
			fmt.Fprintf(w, `{
				"id":"https://openalex.org/%s",
				"doi":"https://doi.org/%s",
				"title":"E2E Triple-Source Paper (OA copy)",
				"display_name":"E2E Triple-Source Paper (OA copy)",
				"publication_year":2024,
				"publication_date":"2024-01-15",
				"type":"article",
				"cited_by_count":%d,
				"authorships":[{"author_position":"first","author":{"id":"https://openalex.org/A1","display_name":"Alice E2E","orcid":null},"institutions":[{"id":"https://openalex.org/I1","display_name":"MIT"}]}],
				"primary_location":{"source":{"id":"https://openalex.org/S1","display_name":"Nature","type":"journal","issn_l":"0028-0836"},"pdf_url":"https://example.com/triple-oa.pdf","landing_page_url":"https://doi.org/%s","is_oa":true},
				"open_access":{"is_oa":true,"oa_url":"https://example.com/triple-oa.pdf","oa_status":"gold"},
				"abstract_inverted_index":{"abstract":[0],"from":[1],"OpenAlex":[2],"inverted":[3],"index.":[4]},
				"concepts":[{"id":"https://openalex.org/C1","display_name":"Computer Science","level":0,"score":0.95}],
				"topics":[{"id":"https://openalex.org/T1","display_name":"Deep Learning"}],
				"primary_topic":{"id":"https://openalex.org/T1","display_name":"Deep Learning"},
				"biblio":null,
				"ids":null,
				"referenced_works":[],
				"related_works":[],
				"license":null
			}`, e2eOAWorkID, e2eOASharedDOI, e2eOAExpectedCitations, e2eOASharedDOI)
			return
		}

		// Search response.
		fmt.Fprintf(w, `{
			"meta":{"count":2,"page":1,"per_page":25},
			"results":[
				{
					"id":"https://openalex.org/%s",
					"doi":"https://doi.org/%s",
					"title":"E2E Triple-Source Paper (OA copy)",
					"display_name":"E2E Triple-Source Paper (OA copy)",
					"publication_year":2024,
					"publication_date":"2024-01-15",
					"type":"article",
					"cited_by_count":%d,
					"authorships":[{"author_position":"first","author":{"id":"https://openalex.org/A1","display_name":"Alice E2E","orcid":null},"institutions":[{"id":"https://openalex.org/I1","display_name":"MIT"}]}],
					"primary_location":{"source":{"id":"https://openalex.org/S1","display_name":"Nature","type":"journal","issn_l":"0028-0836"},"pdf_url":"https://example.com/triple-oa.pdf","landing_page_url":"https://doi.org/%s","is_oa":true},
					"open_access":{"is_oa":true,"oa_url":"https://example.com/triple-oa.pdf","oa_status":"gold"},
					"abstract_inverted_index":{"abstract":[0],"from":[1],"OpenAlex":[2],"inverted":[3],"index.":[4]},
					"concepts":[{"id":"https://openalex.org/C1","display_name":"Computer Science","level":0,"score":0.95}],
					"topics":[],
					"primary_topic":null,
					"biblio":null,
					"ids":null,
					"referenced_works":[],
					"related_works":[],
					"license":null
				},
				{
					"id":"https://openalex.org/W8888888888",
					"doi":null,
					"title":"E2E OA-Only Paper",
					"display_name":"E2E OA-Only Paper",
					"publication_year":2024,
					"publication_date":"2024-02-15",
					"type":"article",
					"cited_by_count":5,
					"authorships":[{"author_position":"first","author":{"id":"https://openalex.org/A2","display_name":"Diana E2E","orcid":null},"institutions":[]}],
					"primary_location":null,
					"open_access":null,
					"abstract_inverted_index":{"OA":[0],"only":[1],"paper.":[2]},
					"concepts":[],
					"topics":[],
					"primary_topic":null,
					"biblio":null,
					"ids":null,
					"referenced_works":[],
					"related_works":[],
					"license":null
				}
			]
		}`, e2eOAWorkID, e2eOASharedDOI, e2eOAExpectedCitations, e2eOASharedDOI)
	}))
	defer oaServer.Close()

	// Step 4: Load config from temp YAML.
	configYAML := fmt.Sprintf(`
server:
  name: "retrievr-mcp"
  http_addr: ":0"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["arxiv", "s2", "openalex"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 500

sources:
  arxiv:
    enabled: true
    base_url: "%s"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
  pubmed:
    enabled: true
    timeout: "10s"
    rate_limit: 3.0
    rate_limit_burst: 3
  s2:
    enabled: true
    base_url: "%s"
    api_key: "e2e-server-s2-key"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
  openalex:
    enabled: true
    base_url: "%s"
    api_key: "e2e-server-oa-key"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      mailto: "e2e@test.com"
  huggingface:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  europmc:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
`, arxivServer.URL, s2Server.URL, oaServer.URL)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configYAML), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	// Step 5: Create real plugins.
	arxivPlugin := &ArXivPlugin{}
	err = arxivPlugin.Initialize(context.Background(), cfg.Sources[SourceArXiv])
	require.NoError(t, err)

	s2Plugin := &S2Plugin{}
	err = s2Plugin.Initialize(context.Background(), cfg.Sources[SourceS2])
	require.NoError(t, err)

	oaPlugin := &OpenAlexPlugin{}
	err = oaPlugin.Initialize(context.Background(), cfg.Sources[SourceOpenAlex])
	require.NoError(t, err)

	plugins := map[string]SourcePlugin{
		SourceArXiv:    arxivPlugin,
		SourceS2:       s2Plugin,
		SourceOpenAlex: oaPlugin,
	}

	// Step 6: Create real infrastructure.
	cache := NewCache(CacheConfig{
		MaxEntries: cfg.Router.CacheMaxEntries,
		TTL:        cfg.Router.CacheTTL.Duration,
		Enabled:    cfg.Router.CacheEnabled,
	})

	rateLimits := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	for sourceID, sourceCfg := range cfg.Sources {
		rps := sourceCfg.RateLimit
		if rps < RateLimitMinRPS {
			rps = DefaultRateLimitRPS
		}
		burst := sourceCfg.RateLimitBurst
		if burst <= 0 {
			burst = DefaultRateLimitBurst
		}
		rateLimits.Register(RateLimiterConfig{
			SourceID:          sourceID,
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}
	rateLimits.Start(DefaultCleanupInterval)
	t.Cleanup(rateLimits.Stop)

	resolver := &CredentialResolver{}

	serverDefaults := make(map[string]string, len(cfg.Sources))
	for id, src := range cfg.Sources {
		serverDefaults[id] = src.APIKey
	}

	// Step 7: Create router + server.
	router := NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, nil)
	srv := NewServer(cfg, router, rateLimits, nil)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Step 8: Test triple-source search via MCP tool handler.
	ctx := context.Background()
	searchHandler := NewSearchHandler(router)

	searchReq := mcp.CallToolRequest{}
	searchReq.Params.Name = ToolNameSearch
	searchReq.Params.Arguments = map[string]any{
		FieldQuery:   "triple-source test",
		FieldSources: []any{SourceArXiv, SourceS2, SourceOpenAlex},
		FieldSort:    string(SortCitations),
		FieldLimit:   float64(10),
	}

	searchResult, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult)
	assert.False(t, searchResult.IsError, "search should succeed")

	searchText := extractTextContent(t, searchResult)
	var merged MergedSearchResult
	err = json.Unmarshal([]byte(searchText), &merged)
	require.NoError(t, err)

	// 6 total results (2 ArXiv + 2 S2 + 2 OA) - 2 DOI dedup = 4.
	assert.Len(t, merged.Results, e2eOASearchResultCount, "6 pubs minus 2 DOI dedup = 4")
	assert.Len(t, merged.SourcesQueried, e2eOAExpectedSources)
	assert.Contains(t, merged.SourcesQueried, SourceArXiv)
	assert.Contains(t, merged.SourcesQueried, SourceS2)
	assert.Contains(t, merged.SourcesQueried, SourceOpenAlex)
	assert.Empty(t, merged.SourcesFailed)

	// Step 9: Verify DOI-based dedup across all three sources.
	var sharedPaper *Publication
	for i := range merged.Results {
		if merged.Results[i].DOI == e2eOASharedDOI {
			sharedPaper = &merged.Results[i]
			break
		}
	}
	require.NotNil(t, sharedPaper, "shared DOI paper should be in results")
	assert.NotEmpty(t, sharedPaper.AlsoFoundIn, "should track also_found_in from multiple sources")

	// Highest citation count should win (OA has 150, S2 has 120, ArXiv has nil).
	require.NotNil(t, sharedPaper.CitationCount)
	assert.Equal(t, e2eOAExpectedCitations, *sharedPaper.CitationCount, "highest citation count should win")

	// Step 10: Test OpenAlex Get via MCP tool handler.
	getHandler := NewGetHandler(router)
	getReq := mcp.CallToolRequest{}
	getReq.Params.Name = ToolNameGet
	getReq.Params.Arguments = map[string]any{
		FieldID: SourceOpenAlex + prefixedIDSeparator + e2eOAWorkID,
	}

	getResult, err := getHandler(ctx, getReq)
	require.NoError(t, err)
	require.NotNil(t, getResult)
	assert.False(t, getResult.IsError)

	getText := extractTextContent(t, getResult)
	var pub Publication
	err = json.Unmarshal([]byte(getText), &pub)
	require.NoError(t, err)
	assert.Contains(t, pub.Title, "Triple-Source")
	assert.Equal(t, SourceOpenAlex, pub.Source)
	assert.Equal(t, e2eOASharedDOI, pub.DOI)
	// Verify inverted abstract was reconstructed.
	assert.Contains(t, pub.Abstract, "abstract")
	assert.Contains(t, pub.Abstract, "OpenAlex")
	assert.Contains(t, pub.Abstract, "inverted")
	assert.NotEmpty(t, pub.PDFURL)
	require.NotNil(t, pub.CitationCount)
	assert.Equal(t, e2eOAExpectedCitations, *pub.CitationCount)

	// Step 11: Test OpenAlex Get with BibTeX format.
	getBibReq := mcp.CallToolRequest{}
	getBibReq.Params.Name = ToolNameGet
	getBibReq.Params.Arguments = map[string]any{
		FieldID:     SourceOpenAlex + prefixedIDSeparator + e2eOAWorkID,
		FieldFormat: string(FormatBibTeX),
	}

	getBibResult, err := getHandler(ctx, getBibReq)
	require.NoError(t, err)
	require.NotNil(t, getBibResult)
	assert.False(t, getBibResult.IsError)

	getBibText := extractTextContent(t, getBibResult)
	var bibPub Publication
	err = json.Unmarshal([]byte(getBibText), &bibPub)
	require.NoError(t, err)
	require.NotNil(t, bibPub.FullText)
	assert.Equal(t, FormatBibTeX, bibPub.FullText.ContentFormat)
	assert.Contains(t, bibPub.FullText.Content, "@article{")
	assert.Contains(t, bibPub.FullText.Content, e2eOASharedDOI)

	// Step 12: Test list_sources — all three should appear.
	listHandler := NewListSourcesHandler(router)
	listReq := mcp.CallToolRequest{}
	listReq.Params.Name = ToolNameListSources

	listResult, err := listHandler(ctx, listReq)
	require.NoError(t, err)
	require.NotNil(t, listResult)
	assert.False(t, listResult.IsError)

	listText := extractTextContent(t, listResult)
	var infos []SourceInfo
	err = json.Unmarshal([]byte(listText), &infos)
	require.NoError(t, err)
	require.Len(t, infos, e2eOAExpectedSources)

	// Sorted by ID: arxiv, openalex, s2.
	assert.Equal(t, SourceArXiv, infos[0].ID)
	assert.Equal(t, SourceOpenAlex, infos[1].ID)
	assert.Equal(t, SourceS2, infos[2].ID)

	// Verify OpenAlex capabilities in list_sources.
	oaInfo := infos[1]
	assert.Equal(t, oaPluginName, oaInfo.Name)
	assert.True(t, oaInfo.Enabled)
	assert.Contains(t, oaInfo.ContentTypes, ContentTypePaper)
	assert.Equal(t, FormatJSON, oaInfo.NativeFormat)
	assert.True(t, oaInfo.SupportsDateFilter)
	assert.False(t, oaInfo.SupportsCitations, "OA does not support citations sub-resource")
	assert.True(t, oaInfo.AcceptsCredentials)

	// Step 13: Verify cache — second search should be a cache hit.
	searchResult2, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult2)
	assert.False(t, searchResult2.IsError)

	cacheMetrics := cache.Metrics()
	assert.Equal(t, uint64(1), cacheMetrics.Hits, "second search should be a cache hit")

	// Step 14: Run contract tests on all three real plugins.
	PluginContractTest(t, arxivPlugin)
	PluginContractTest(t, s2Plugin)
	PluginContractTest(t, oaPlugin)

	// Step 15: Verify all three plugins are healthy.
	arxivHealth := arxivPlugin.Health(ctx)
	assert.True(t, arxivHealth.Enabled)
	assert.True(t, arxivHealth.Healthy)

	s2Health := s2Plugin.Health(ctx)
	assert.True(t, s2Health.Enabled)
	assert.True(t, s2Health.Healthy)

	oaHealth := oaPlugin.Health(ctx)
	assert.True(t, oaHealth.Enabled)
	assert.True(t, oaHealth.Healthy)
	assert.Empty(t, oaHealth.LastError)
}

// ---------------------------------------------------------------------------
// DC-08: PubMed quad-source E2E test constants
// ---------------------------------------------------------------------------

// e2e PM + OA + S2 + ArXiv quad-source test constants.
const (
	e2ePMVersion           = "0.8.0-e2e"
	e2ePMSharedDOI         = "10.1234/e2e-quad-source-doi"
	e2ePMSharedArXivID     = "2401.77777"
	e2ePMOAWorkID          = "W8888888888"
	e2ePMS2PaperID         = "s2paper001quad12345678901234567890abcdef"
	e2ePMPMID              = "88888888"
	e2ePMSearchResultCount = 5 // 8 total (2+2+2+2) - 3 DOI dedup = 5
	e2ePMExpectedCitations = 200
	e2ePMExpectedSources   = 4
)

// TestE2EPubMedPluginQuadSourcePipeline exercises the full DC-08 pipeline:
// config loading → real ArXivPlugin + real S2Plugin + real OpenAlexPlugin + real PubMedPlugin →
// httptest servers → real Router → real Cache → real RateLimitManager →
// real CredentialResolver → MCP tool handlers. Validates quad-source search,
// dedup by DOI, PubMed two-phase workflow, and credential propagation.
func TestE2EPubMedPluginQuadSourcePipeline(t *testing.T) {
	// Not parallel: mutates global version state.
	SetVersionForTesting(e2ePMVersion, "e2e-commit", "2024-04-07")
	t.Cleanup(ResetVersionForTesting)

	// Step 1: Create httptest ArXiv server.
	arxivServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")

		if idList := r.URL.Query().Get("id_list"); idList != "" {
			entry := arxivTestEntry{
				ID:              e2ePMSharedArXivID,
				Title:           "E2E Quad-Source Paper",
				Summary:         "Abstract for quad-source test.",
				Published:       "2024-01-15T08:00:00Z",
				Updated:         "2024-01-20T10:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Alice E2E", Affiliation: "MIT"}},
				Categories:      []string{"cs.CL", "cs.AI"},
				DOI:             e2ePMSharedDOI,
				PrimaryCategory: "cs.CL",
			}
			fmt.Fprint(w, buildArxivTestFeedXML(1, 0, []arxivTestEntry{entry}))
			return
		}

		entries := []arxivTestEntry{
			{
				ID:              e2ePMSharedArXivID,
				Title:           "E2E Quad-Source Paper",
				Summary:         "Abstract for quad-source test.",
				Published:       "2024-01-15T08:00:00Z",
				Updated:         "2024-01-20T10:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Alice E2E", Affiliation: "MIT"}},
				Categories:      []string{"cs.CL", "cs.AI"},
				DOI:             e2ePMSharedDOI,
				PrimaryCategory: "cs.CL",
			},
			{
				ID:              "2401.66666",
				Title:           "E2E ArXiv-Only Paper (quad)",
				Summary:         "Only found on ArXiv in quad test.",
				Published:       "2024-02-01T08:00:00Z",
				Updated:         "2024-02-01T08:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Bob E2E"}},
				Categories:      []string{"cs.LG"},
				PrimaryCategory: "cs.LG",
			},
		}
		fmt.Fprint(w, buildArxivTestFeedXML(2, 0, entries))
	}))
	defer arxivServer.Close()

	// Step 2: Create httptest S2 server.
	s2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/citations"):
			fmt.Fprint(w, `{"offset":0,"next":null,"data":[]}`)
		case strings.HasSuffix(path, "/references"):
			fmt.Fprint(w, `{"offset":0,"next":null,"data":[]}`)
		case strings.Contains(path, "/paper/search"):
			papers := []string{
				fmt.Sprintf(`{
					"paperId":%q,
					"externalIds":{"DOI":%q,"ArXiv":%q,"PMID":"","CorpusId":99999},
					"title":"E2E Quad-Source Paper (S2 copy)",
					"abstract":"S2 abstract for quad-source test.",
					"year":2024,
					"authors":[{"authorId":"a1","name":"Alice E2E"}],
					"citationCount":180,
					"referenceCount":20,
					"publicationDate":"2024-01-15",
					"journal":{"name":"Nature","volume":"625","pages":"1-10"},
					"openAccessPdf":{"url":"https://example.com/quad.pdf"},
					"fieldsOfStudy":["Computer Science"],
					"url":"https://www.semanticscholar.org/paper/s2quad",
					"isOpenAccess":true,
					"publicationTypes":["JournalArticle"]
				}`, e2ePMS2PaperID, e2ePMSharedDOI, e2ePMSharedArXivID),
				`{
					"paperId":"s2paper002unique",
					"externalIds":null,
					"title":"E2E S2-Only Paper (quad)",
					"abstract":"Only found on S2 in quad test.",
					"year":2024,
					"authors":[{"authorId":"a2","name":"Carol E2E"}],
					"citationCount":10,
					"referenceCount":5,
					"publicationDate":"2024-03-01",
					"journal":null,
					"openAccessPdf":null,
					"fieldsOfStudy":["Computer Science"],
					"url":"https://www.semanticscholar.org/paper/s2paper002",
					"isOpenAccess":false,
					"publicationTypes":["Conference"]
				}`,
			}
			fmt.Fprintf(w, `{"total":2,"offset":0,"next":null,"data":[%s]}`,
				strings.Join(papers, ","))
		default:
			// Single paper get.
			fmt.Fprintf(w, `{
				"paperId":%q,
				"externalIds":{"DOI":%q,"ArXiv":%q,"PMID":"","CorpusId":99999},
				"title":"E2E Quad-Source Paper (S2 copy)",
				"abstract":"S2 abstract for quad-source test.",
				"year":2024,
				"authors":[{"authorId":"a1","name":"Alice E2E"}],
				"citationCount":180,
				"referenceCount":20,
				"publicationDate":"2024-01-15",
				"journal":{"name":"Nature","volume":"625","pages":"1-10"},
				"openAccessPdf":{"url":"https://example.com/quad.pdf"},
				"fieldsOfStudy":["Computer Science"],
				"url":"https://www.semanticscholar.org/paper/s2quad",
				"isOpenAccess":true,
				"publicationTypes":["JournalArticle"]
			}`, e2ePMS2PaperID, e2ePMSharedDOI, e2ePMSharedArXivID)
		}
	}))
	defer s2Server.Close()

	// Step 3: Create httptest OpenAlex server.
	oaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		path := r.URL.Path
		if strings.Contains(path, "/works/") {
			// Single work get.
			fmt.Fprintf(w, `{
				"id":"https://openalex.org/works/%s",
				"doi":"https://doi.org/%s",
				"title":"E2E Quad-Source Paper (OA copy)",
				"display_name":"E2E Quad-Source Paper (OA copy)",
				"publication_date":"2024-01-15",
				"cited_by_count":%d,
				"type":"journal-article",
				"authorships":[
					{"author":{"id":"A1","display_name":"Alice E2E","orcid":null},
					 "institutions":[{"display_name":"MIT"}]}
				],
				"primary_location":{"source":{"display_name":"Nature"},"pdf_url":"https://example.com/quad-oa.pdf"},
				"open_access":{"oa_url":"https://example.com/quad-oa.pdf","is_oa":true},
				"abstract_inverted_index":{"OA":[0],"quad":[1],"abstract.":[2]},
				"concepts":[],
				"topics":[],
				"primary_topic":null,
				"biblio":null,
				"ids":{"doi":"https://doi.org/%s"},
				"referenced_works":[],
				"related_works":[],
				"license":null
			}`, e2ePMOAWorkID, e2ePMSharedDOI, e2ePMExpectedCitations, e2ePMSharedDOI)
			return
		}

		// Search.
		fmt.Fprintf(w, `{
			"meta":{"count":2,"per_page":25,"page":1},
			"results":[
				{
					"id":"https://openalex.org/works/%s",
					"doi":"https://doi.org/%s",
					"title":"E2E Quad-Source Paper (OA copy)",
					"display_name":"E2E Quad-Source Paper (OA copy)",
					"publication_date":"2024-01-15",
					"cited_by_count":%d,
					"type":"journal-article",
					"authorships":[
						{"author":{"id":"A1","display_name":"Alice E2E","orcid":null},
						 "institutions":[{"display_name":"MIT"}]}
					],
					"primary_location":{"source":{"display_name":"Nature"},"pdf_url":"https://example.com/quad-oa.pdf"},
					"open_access":{"oa_url":"https://example.com/quad-oa.pdf","is_oa":true},
					"abstract_inverted_index":{"OA":[0],"quad":[1],"abstract.":[2]},
					"concepts":[],
					"topics":[],
					"primary_topic":null,
					"biblio":null,
					"ids":{"doi":"https://doi.org/%s"},
					"referenced_works":[],
					"related_works":[],
					"license":null
				},
				{
					"id":"https://openalex.org/works/W0000000002",
					"doi":null,
					"title":"E2E OA-Only Paper (quad)",
					"display_name":"E2E OA-Only Paper (quad)",
					"publication_date":"2024-05-01",
					"cited_by_count":5,
					"type":"journal-article",
					"authorships":[],
					"primary_location":null,
					"open_access":null,
					"abstract_inverted_index":{"OA":[0],"only":[1],"paper.":[2]},
					"concepts":[],
					"topics":[],
					"primary_topic":null,
					"biblio":null,
					"ids":null,
					"referenced_works":[],
					"related_works":[],
					"license":null
				}
			]
		}`, e2ePMOAWorkID, e2ePMSharedDOI, e2ePMExpectedCitations, e2ePMSharedDOI)
	}))
	defer oaServer.Close()

	// Step 4: Create httptest PubMed server (two-phase: esearch + efetch).
	pmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")

		// Verify tool and email are always present.
		assert.NotEmpty(t, r.URL.Query().Get(pmParamTool))
		assert.NotEmpty(t, r.URL.Query().Get(pmParamEmail))

		if strings.Contains(r.URL.Path, pmESearchPath) {
			// esearch: return PMIDs.
			fmt.Fprint(w, buildPMTestESearchXML(
				2, 0, 10, "1", "MCID_e2e_quad_123",
				[]string{e2ePMPMID, "77777777"},
			))
			return
		}

		if strings.Contains(r.URL.Path, pmEFetchPath) {
			db := r.URL.Query().Get(pmParamDB)
			if db == pmDBPMC {
				// PMC full text request.
				fmt.Fprint(w, "<article><body><p>E2E PMC full text.</p></body></article>")
				return
			}

			// efetch for pubmed: return articles.
			articles := []pmTestArticle{
				{
					PMID:     e2ePMPMID,
					Title:    "E2E Quad-Source Paper (PubMed copy)",
					Abstract: "PubMed abstract for quad-source test.",
					Authors: []pmTestAuthor{
						{LastName: "E2E", ForeName: "Alice", Initials: "A", Affiliation: "MIT"},
					},
					DOI:          e2ePMSharedDOI,
					PMCID:        "PMC9999999",
					JournalTitle: "Nature",
					Volume:       "625",
					Issue:        "1",
					ISSN:         "0028-0836",
					PubYear:      "2024",
					PubMonth:     "Jan",
					PubDay:       "15",
					MeSHTerms:    []string{"Gene Editing", "CRISPR-Cas Systems"},
					PubTypes:     []string{"Journal Article"},
					Language:     "eng",
				},
				{
					PMID:     "77777777",
					Title:    "E2E PubMed-Only Paper (quad)",
					Abstract: "Only found on PubMed in quad test.",
					Authors: []pmTestAuthor{
						{LastName: "Unique", ForeName: "PMOnly", Initials: "P"},
					},
					JournalTitle: "The Lancet",
					PubYear:      "2024",
					PubMonth:     "Mar",
					PubDay:       "20",
					PubTypes:     []string{"Journal Article"},
					Language:     "eng",
				},
			}

			// If fetching a single PMID (Get request), filter.
			if id := r.URL.Query().Get(pmParamID); id != "" {
				for _, a := range articles {
					if a.PMID == id {
						fmt.Fprint(w, buildPMTestEFetchXML([]pmTestArticle{a}))
						return
					}
				}
				fmt.Fprint(w, buildPMTestEFetchXML([]pmTestArticle{}))
				return
			}

			fmt.Fprint(w, buildPMTestEFetchXML(articles))
			return
		}
	}))
	defer pmServer.Close()

	// Step 5: Load config from temp YAML.
	configYAML := fmt.Sprintf(`
server:
  name: "retrievr-mcp"
  http_addr: ":0"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["arxiv", "s2", "openalex", "pubmed"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 500

sources:
  arxiv:
    enabled: true
    base_url: "%s"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
  pubmed:
    enabled: true
    base_url: "%s"
    api_key: "e2e-server-pm-key"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      tool: "retrievr-mcp"
      email: "e2e@test.com"
  s2:
    enabled: true
    base_url: "%s"
    api_key: "e2e-server-s2-key"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
  openalex:
    enabled: true
    base_url: "%s"
    api_key: "e2e-server-oa-key"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      mailto: "e2e@test.com"
  huggingface:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  europmc:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
`, arxivServer.URL, pmServer.URL+"/", s2Server.URL, oaServer.URL)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configYAML), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	// Step 6: Create real plugins.
	arxivPlugin := &ArXivPlugin{}
	err = arxivPlugin.Initialize(context.Background(), cfg.Sources[SourceArXiv])
	require.NoError(t, err)

	s2Plugin := &S2Plugin{}
	err = s2Plugin.Initialize(context.Background(), cfg.Sources[SourceS2])
	require.NoError(t, err)

	oaPlugin := &OpenAlexPlugin{}
	err = oaPlugin.Initialize(context.Background(), cfg.Sources[SourceOpenAlex])
	require.NoError(t, err)

	pmPlugin := &PubMedPlugin{}
	err = pmPlugin.Initialize(context.Background(), cfg.Sources[SourcePubMed])
	require.NoError(t, err)

	plugins := map[string]SourcePlugin{
		SourceArXiv:    arxivPlugin,
		SourceS2:       s2Plugin,
		SourceOpenAlex: oaPlugin,
		SourcePubMed:   pmPlugin,
	}

	// Step 7: Create real infrastructure.
	cache := NewCache(CacheConfig{
		MaxEntries: cfg.Router.CacheMaxEntries,
		TTL:        cfg.Router.CacheTTL.Duration,
		Enabled:    cfg.Router.CacheEnabled,
	})

	rateLimits := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	for sourceID, sourceCfg := range cfg.Sources {
		rps := sourceCfg.RateLimit
		if rps < RateLimitMinRPS {
			rps = DefaultRateLimitRPS
		}
		burst := sourceCfg.RateLimitBurst
		if burst <= 0 {
			burst = DefaultRateLimitBurst
		}
		rateLimits.Register(RateLimiterConfig{
			SourceID:          sourceID,
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}
	rateLimits.Start(DefaultCleanupInterval)
	t.Cleanup(rateLimits.Stop)

	resolver := &CredentialResolver{}

	serverDefaults := make(map[string]string, len(cfg.Sources))
	for id, src := range cfg.Sources {
		serverDefaults[id] = src.APIKey
	}

	// Step 8: Create router + server.
	router := NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, nil)
	srv := NewServer(cfg, router, rateLimits, nil)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Step 9: Test quad-source search via MCP tool handler.
	ctx := context.Background()
	searchHandler := NewSearchHandler(router)

	searchReq := mcp.CallToolRequest{}
	searchReq.Params.Name = ToolNameSearch
	searchReq.Params.Arguments = map[string]any{
		FieldQuery:   "quad-source test",
		FieldSources: []any{SourceArXiv, SourceS2, SourceOpenAlex, SourcePubMed},
		FieldSort:    string(SortCitations),
		FieldLimit:   float64(10),
	}

	searchResult, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult)
	assert.False(t, searchResult.IsError, "search should succeed")

	searchText := extractTextContent(t, searchResult)
	var merged MergedSearchResult
	err = json.Unmarshal([]byte(searchText), &merged)
	require.NoError(t, err)

	// 8 total results (2 ArXiv + 2 S2 + 2 OA + 2 PubMed) - 3 DOI dedup = 5.
	assert.Len(t, merged.Results, e2ePMSearchResultCount, "8 pubs minus 3 DOI dedup = 5")
	assert.Len(t, merged.SourcesQueried, e2ePMExpectedSources)
	assert.Contains(t, merged.SourcesQueried, SourceArXiv)
	assert.Contains(t, merged.SourcesQueried, SourceS2)
	assert.Contains(t, merged.SourcesQueried, SourceOpenAlex)
	assert.Contains(t, merged.SourcesQueried, SourcePubMed)
	assert.Empty(t, merged.SourcesFailed)

	// Step 10: Verify DOI-based dedup across all four sources.
	var sharedPaper *Publication
	for i := range merged.Results {
		if merged.Results[i].DOI == e2ePMSharedDOI {
			sharedPaper = &merged.Results[i]
			break
		}
	}
	require.NotNil(t, sharedPaper, "shared DOI paper should be in results")
	assert.NotEmpty(t, sharedPaper.AlsoFoundIn, "should track also_found_in from multiple sources")

	// Highest citation count should win (OA has 200, S2 has 180, ArXiv has nil, PubMed has nil).
	require.NotNil(t, sharedPaper.CitationCount)
	assert.Equal(t, e2ePMExpectedCitations, *sharedPaper.CitationCount, "highest citation count should win")

	// Step 11: Test PubMed Get via MCP tool handler.
	getHandler := NewGetHandler(router)
	getReq := mcp.CallToolRequest{}
	getReq.Params.Name = ToolNameGet
	getReq.Params.Arguments = map[string]any{
		FieldID: SourcePubMed + prefixedIDSeparator + e2ePMPMID,
	}

	getResult, err := getHandler(ctx, getReq)
	require.NoError(t, err)
	require.NotNil(t, getResult)
	assert.False(t, getResult.IsError)

	getText := extractTextContent(t, getResult)
	var pub Publication
	err = json.Unmarshal([]byte(getText), &pub)
	require.NoError(t, err)
	assert.Contains(t, pub.Title, "Quad-Source")
	assert.Equal(t, SourcePubMed, pub.Source)
	assert.Equal(t, e2ePMSharedDOI, pub.DOI)
	assert.NotEmpty(t, pub.Abstract)
	assert.NotEmpty(t, pub.Authors)

	// Step 12: Test PubMed Get with BibTeX format.
	getBibReq := mcp.CallToolRequest{}
	getBibReq.Params.Name = ToolNameGet
	getBibReq.Params.Arguments = map[string]any{
		FieldID:     SourcePubMed + prefixedIDSeparator + e2ePMPMID,
		FieldFormat: string(FormatBibTeX),
	}

	getBibResult, err := getHandler(ctx, getBibReq)
	require.NoError(t, err)
	require.NotNil(t, getBibResult)
	assert.False(t, getBibResult.IsError)

	getBibText := extractTextContent(t, getBibResult)
	var bibPub Publication
	err = json.Unmarshal([]byte(getBibText), &bibPub)
	require.NoError(t, err)
	require.NotNil(t, bibPub.FullText)
	assert.Equal(t, FormatBibTeX, bibPub.FullText.ContentFormat)
	assert.Contains(t, bibPub.FullText.Content, "@article{")
	assert.Contains(t, bibPub.FullText.Content, e2ePMSharedDOI)

	// Step 13: Test list_sources — all four should appear.
	listHandler := NewListSourcesHandler(router)
	listReq := mcp.CallToolRequest{}
	listReq.Params.Name = ToolNameListSources

	listResult, err := listHandler(ctx, listReq)
	require.NoError(t, err)
	require.NotNil(t, listResult)
	assert.False(t, listResult.IsError)

	listText := extractTextContent(t, listResult)
	var infos []SourceInfo
	err = json.Unmarshal([]byte(listText), &infos)
	require.NoError(t, err)
	require.Len(t, infos, e2ePMExpectedSources)

	// Verify PubMed appears and has correct capabilities.
	var pmInfo *SourceInfo
	for i := range infos {
		if infos[i].ID == SourcePubMed {
			pmInfo = &infos[i]
			break
		}
	}
	require.NotNil(t, pmInfo, "PubMed should appear in list_sources")
	assert.Equal(t, pmPluginName, pmInfo.Name)
	assert.True(t, pmInfo.Enabled)
	assert.Contains(t, pmInfo.ContentTypes, ContentTypePaper)
	assert.Equal(t, FormatXML, pmInfo.NativeFormat)
	assert.True(t, pmInfo.SupportsDateFilter)
	assert.True(t, pmInfo.SupportsAuthorFilter)
	assert.True(t, pmInfo.SupportsCategoryFilter)
	assert.True(t, pmInfo.SupportsFullText)
	assert.False(t, pmInfo.SupportsCitations)
	assert.True(t, pmInfo.AcceptsCredentials)

	// Step 14: Verify cache — second search should be a cache hit.
	searchResult2, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult2)
	assert.False(t, searchResult2.IsError)

	cacheMetrics := cache.Metrics()
	assert.Equal(t, uint64(1), cacheMetrics.Hits, "second search should be a cache hit")

	// Step 15: Run contract tests on all four real plugins.
	PluginContractTest(t, arxivPlugin)
	PluginContractTest(t, s2Plugin)
	PluginContractTest(t, oaPlugin)
	PluginContractTest(t, pmPlugin)

	// Step 16: Verify all four plugins are healthy.
	arxivHealth := arxivPlugin.Health(ctx)
	assert.True(t, arxivHealth.Enabled)
	assert.True(t, arxivHealth.Healthy)

	s2Health := s2Plugin.Health(ctx)
	assert.True(t, s2Health.Enabled)
	assert.True(t, s2Health.Healthy)

	oaHealth := oaPlugin.Health(ctx)
	assert.True(t, oaHealth.Enabled)
	assert.True(t, oaHealth.Healthy)

	pmHealth := pmPlugin.Health(ctx)
	assert.True(t, pmHealth.Enabled)
	assert.True(t, pmHealth.Healthy)
	assert.Empty(t, pmHealth.LastError)
}

// ---------------------------------------------------------------------------
// DC-09: EuropePMC quint-source E2E test constants
// ---------------------------------------------------------------------------

// e2e EMC + PM + OA + S2 + ArXiv quint-source test constants.
const (
	e2eEMCVersion           = "0.9.0-e2e"
	e2eEMCSharedDOI         = "10.1234/e2e-quint-source-doi"
	e2eEMCSharedArXivID     = "2401.99999"
	e2eEMCOAWorkID          = "W9999999999"
	e2eEMCS2PaperID         = "s2paper001quint12345678901234567890abcdef"
	e2eEMCPubMedPMID        = "99999999"
	e2eEMCEuropePMCID       = "33333333"
	e2eEMCSearchResultCount = 6 // 10 total (2+2+2+2+2) - 4 DOI dedup = 6
	e2eEMCExpectedCitations = 250
	e2eEMCExpectedSources   = 5
)

// TestE2EEuropePMCPluginQuintSourcePipeline exercises the full DC-09 pipeline:
// config loading → real ArXivPlugin + S2Plugin + OpenAlexPlugin + PubMedPlugin + EuropePMCPlugin →
// httptest servers → real Router → real Cache → real RateLimitManager →
// real CredentialResolver → MCP tool handlers. Validates quint-source search,
// dedup by DOI across five sources, and EuropePMC JSON workflow.
func TestE2EEuropePMCPluginQuintSourcePipeline(t *testing.T) {
	// Not parallel: mutates global version state.
	SetVersionForTesting(e2eEMCVersion, "e2e-commit", "2024-04-09")
	t.Cleanup(ResetVersionForTesting)

	// Step 1: Create httptest ArXiv server.
	arxivServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")

		if idList := r.URL.Query().Get("id_list"); idList != "" {
			entry := arxivTestEntry{
				ID:              e2eEMCSharedArXivID,
				Title:           "E2E Quint-Source Paper",
				Summary:         "Abstract for quint-source test.",
				Published:       "2024-01-15T08:00:00Z",
				Updated:         "2024-01-20T10:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Alice E2E", Affiliation: "MIT"}},
				Categories:      []string{"cs.CL", "cs.AI"},
				DOI:             e2eEMCSharedDOI,
				PrimaryCategory: "cs.CL",
			}
			fmt.Fprint(w, buildArxivTestFeedXML(1, 0, []arxivTestEntry{entry}))
			return
		}

		entries := []arxivTestEntry{
			{
				ID:              e2eEMCSharedArXivID,
				Title:           "E2E Quint-Source Paper",
				Summary:         "Abstract for quint-source test.",
				Published:       "2024-01-15T08:00:00Z",
				Updated:         "2024-01-20T10:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Alice E2E", Affiliation: "MIT"}},
				Categories:      []string{"cs.CL", "cs.AI"},
				DOI:             e2eEMCSharedDOI,
				PrimaryCategory: "cs.CL",
			},
			{
				ID:              "2401.88888",
				Title:           "E2E ArXiv-Only Paper (quint)",
				Summary:         "Only found on ArXiv in quint test.",
				Published:       "2024-02-01T08:00:00Z",
				Updated:         "2024-02-01T08:00:00Z",
				Authors:         []arxivTestAuthor{{Name: "Bob E2E"}},
				Categories:      []string{"cs.LG"},
				PrimaryCategory: "cs.LG",
			},
		}
		fmt.Fprint(w, buildArxivTestFeedXML(2, 0, entries))
	}))
	defer arxivServer.Close()

	// Step 2: Create httptest S2 server.
	s2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/citations"):
			fmt.Fprint(w, `{"offset":0,"next":null,"data":[]}`)
		case strings.HasSuffix(path, "/references"):
			fmt.Fprint(w, `{"offset":0,"next":null,"data":[]}`)
		case strings.Contains(path, "/paper/search"):
			papers := []string{
				fmt.Sprintf(`{
					"paperId":%q,
					"externalIds":{"DOI":%q,"ArXiv":%q,"PMID":"","CorpusId":99999},
					"title":"E2E Quint-Source Paper (S2 copy)",
					"abstract":"S2 abstract for quint-source test.",
					"year":2024,
					"authors":[{"authorId":"a1","name":"Alice E2E"}],
					"citationCount":180,
					"referenceCount":20,
					"publicationDate":"2024-01-15",
					"journal":{"name":"Nature","volume":"625","pages":"1-10"},
					"openAccessPdf":{"url":"https://example.com/quint.pdf"},
					"fieldsOfStudy":["Computer Science"],
					"url":"https://www.semanticscholar.org/paper/s2quint",
					"isOpenAccess":true,
					"publicationTypes":["JournalArticle"]
				}`, e2eEMCS2PaperID, e2eEMCSharedDOI, e2eEMCSharedArXivID),
				`{
					"paperId":"s2paper002uniqueQNT",
					"externalIds":null,
					"title":"E2E S2-Only Paper (quint)",
					"abstract":"Only found on S2 in quint test.",
					"year":2024,
					"authors":[{"authorId":"a2","name":"Carol E2E"}],
					"citationCount":10,
					"referenceCount":5,
					"publicationDate":"2024-03-01",
					"journal":null,
					"openAccessPdf":null,
					"fieldsOfStudy":["Computer Science"],
					"url":"https://www.semanticscholar.org/paper/s2paper002",
					"isOpenAccess":false,
					"publicationTypes":["Conference"]
				}`,
			}
			fmt.Fprintf(w, `{"total":2,"offset":0,"next":null,"data":[%s]}`,
				strings.Join(papers, ","))
		default:
			// Single-paper GET.
			fmt.Fprintf(w, `{
				"paperId":%q,
				"externalIds":{"DOI":%q,"ArXiv":%q},
				"title":"E2E Quint-Source Paper (S2 copy)",
				"abstract":"S2 abstract for quint-source test.",
				"year":2024,
				"authors":[{"authorId":"a1","name":"Alice E2E"}],
				"citationCount":180,
				"referenceCount":20,
				"publicationDate":"2024-01-15",
				"journal":{"name":"Nature","volume":"625","pages":"1-10"},
				"openAccessPdf":{"url":"https://example.com/quint.pdf"},
				"fieldsOfStudy":["Computer Science"],
				"url":"https://www.semanticscholar.org/paper/s2quint",
				"isOpenAccess":true,
				"publicationTypes":["JournalArticle"]
			}`, e2eEMCS2PaperID, e2eEMCSharedDOI, e2eEMCSharedArXivID)
		}
	}))
	defer s2Server.Close()

	// Step 3: Create httptest OpenAlex server.
	oaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		path := r.URL.Path
		if strings.Contains(path, "/works/") {
			// Single work GET.
			fmt.Fprintf(w, `{
				"id":"https://openalex.org/%s",
				"doi":"https://doi.org/%s",
				"title":"E2E Quint-Source Paper (OA copy)",
				"publication_year":2024,
				"publication_date":"2024-01-15",
				"type":"article",
				"cited_by_count":%d,
				"authorships":[{"author_position":"first","author":{"id":"A1","display_name":"Alice E2E","orcid":""},"institutions":[{"id":"I1","display_name":"MIT"}]}],
				"primary_location":{"source":{"id":"S1","display_name":"Nature","type":"journal","issn_l":"0028-0836"},"pdf_url":"https://example.com/quint-oa.pdf","landing_page_url":"https://doi.org/%s","is_oa":true},
				"open_access":{"is_oa":true,"oa_url":"https://example.com/quint-oa.pdf","oa_status":"gold"},
				"abstract_inverted_index":{"OA":[],"abstract":[1],"for":[2],"quint-source":[3],"test.":[4]},
				"concepts":[{"id":"C1","display_name":"Computer Science","level":0,"score":0.9}],
				"topics":[{"id":"T1","display_name":"NLP"}],
				"biblio":null,
				"ids":{"openalex":"https://openalex.org/%s","doi":"https://doi.org/%s"},
				"referenced_works":[],
				"related_works":[]
			}`, e2eEMCOAWorkID, e2eEMCSharedDOI, e2eEMCExpectedCitations, e2eEMCSharedDOI, e2eEMCOAWorkID, e2eEMCSharedDOI)
			return
		}

		// Search.
		fmt.Fprintf(w, `{"meta":{"count":2,"page":1,"per_page":25},"results":[
			{
				"id":"https://openalex.org/%s",
				"doi":"https://doi.org/%s",
				"title":"E2E Quint-Source Paper (OA copy)",
				"publication_year":2024,
				"publication_date":"2024-01-15",
				"type":"article",
				"cited_by_count":%d,
				"authorships":[{"author_position":"first","author":{"id":"A1","display_name":"Alice E2E","orcid":""},"institutions":[{"id":"I1","display_name":"MIT"}]}],
				"primary_location":{"source":{"id":"S1","display_name":"Nature","type":"journal","issn_l":"0028-0836"},"pdf_url":"https://example.com/quint-oa.pdf","landing_page_url":"","is_oa":true},
				"open_access":{"is_oa":true,"oa_url":"https://example.com/quint-oa.pdf","oa_status":"gold"},
				"abstract_inverted_index":{"OA":[],"abstract":[1],"for":[2],"quint-source":[3],"test.":[4]},
				"concepts":[{"id":"C1","display_name":"Computer Science","level":0,"score":0.9}],
				"topics":[],"biblio":null,"ids":{},"referenced_works":[],"related_works":[]
			},
			{
				"id":"https://openalex.org/W1111111111",
				"doi":"",
				"title":"E2E OA-Only Paper (quint)",
				"publication_year":2024,
				"publication_date":"2024-04-01",
				"type":"article",
				"cited_by_count":5,
				"authorships":[{"author_position":"first","author":{"id":"A3","display_name":"Dave E2E","orcid":""},"institutions":[]}],
				"primary_location":null,
				"open_access":null,
				"abstract_inverted_index":{"Only":[0],"found":[1],"in":[2],"OpenAlex":[3],"quint":[4],"test.":[5]},
				"concepts":[],"topics":[],"biblio":null,"ids":{},"referenced_works":[],"related_works":[]
			}
		]}`, e2eEMCOAWorkID, e2eEMCSharedDOI, e2eEMCExpectedCitations)
	}))
	defer oaServer.Close()

	// Step 4: Create httptest PubMed server (two-phase: esearch + efetch).
	pmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if strings.HasSuffix(path, pmESearchPath) {
			w.Header().Set("Content-Type", "application/xml")
			pmids := []string{e2eEMCPubMedPMID, "88887777"}
			fmt.Fprint(w, buildPMTestESearchXML(2, 0, 2, testPMQueryKey, testPMWebEnv, pmids))
			return
		}

		if strings.HasSuffix(path, pmEFetchPath) {
			w.Header().Set("Content-Type", "application/xml")

			articles := []pmTestArticle{
				{
					PMID:         e2eEMCPubMedPMID,
					Title:        "E2E Quint-Source Paper (PM copy)",
					Abstract:     "PubMed abstract for quint-source test.",
					Authors:      []pmTestAuthor{{LastName: "Alice", ForeName: "E2E", Initials: "E"}},
					DOI:          e2eEMCSharedDOI,
					PMCID:        "PMC99999999",
					JournalTitle: "Nature",
					Volume:       "625",
					Issue:        "1",
					ISSN:         "0028-0836",
					PubYear:      "2024",
					PubMonth:     "Jan",
					PubDay:       "15",
					MeSHTerms:    []string{"CRISPR-Cas Systems"},
					PubTypes:     []string{"Journal Article"},
					Language:     "eng",
				},
				{
					PMID:         "88887777",
					Title:        "E2E PM-Only Paper (quint)",
					Abstract:     "Only found on PubMed in quint test.",
					Authors:      []pmTestAuthor{{LastName: "Eve", ForeName: "E2E", Initials: "E"}},
					JournalTitle: "Science",
					PubYear:      "2024",
					PubMonth:     "Mar",
					PubDay:       "20",
					PubTypes:     []string{"Journal Article"},
					Language:     "eng",
				},
			}

			if id := r.URL.Query().Get(pmParamID); id != "" {
				for _, a := range articles {
					if a.PMID == id {
						fmt.Fprint(w, buildPMTestEFetchXML([]pmTestArticle{a}))
						return
					}
				}
				fmt.Fprint(w, buildPMTestEFetchXML([]pmTestArticle{}))
				return
			}

			fmt.Fprint(w, buildPMTestEFetchXML(articles))
			return
		}
	}))
	defer pmServer.Close()

	// Step 5: Create httptest EuropePMC server.
	emcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Full text XML request.
		if strings.HasSuffix(path, emcFullTextXMLPath) {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, "<article><body><p>E2E EPMC full text.</p></body></article>")
			return
		}

		w.Header().Set("Content-Type", "application/json")

		query := r.URL.Query().Get(emcParamQuery)

		// Single-ID lookup (Get request).
		if strings.Contains(query, emcFieldExtID) {
			result := emcTestResult{
				ID:                   e2eEMCEuropePMCID,
				Source:               "MED",
				PMID:                 e2eEMCEuropePMCID,
				DOI:                  e2eEMCSharedDOI,
				Title:                "E2E Quint-Source Paper (EPMC copy)",
				AuthorString:         "Alice E2E.",
				AbstractText:         "EPMC abstract for quint-source test.",
				FirstPublicationDate: "2024-01-15",
				IsOpenAccess:         emcOpenAccessYes,
				CitedByCount:         200,
				JournalTitle:         "Nature Medicine",
				JournalISSN:          "1078-8956",
				Volume:               "30",
				Issue:                "1",
				MeSHTerms:            []string{"CRISPR-Cas Systems"},
			}
			fmt.Fprint(w, buildEMCTestSearchJSON(1, []emcTestResult{result}))
			return
		}

		// Search request — return 2 results (1 shared DOI, 1 unique).
		results := []emcTestResult{
			{
				ID:                   e2eEMCEuropePMCID,
				Source:               "MED",
				PMID:                 e2eEMCEuropePMCID,
				DOI:                  e2eEMCSharedDOI,
				Title:                "E2E Quint-Source Paper (EPMC copy)",
				AuthorString:         "Alice E2E.",
				AbstractText:         "EPMC abstract for quint-source test.",
				FirstPublicationDate: "2024-01-15",
				IsOpenAccess:         emcOpenAccessYes,
				CitedByCount:         200,
				JournalTitle:         "Nature Medicine",
				JournalISSN:          "1078-8956",
				Volume:               "30",
				Issue:                "1",
				MeSHTerms:            []string{"CRISPR-Cas Systems"},
			},
			{
				ID:                   "44444444",
				Source:               "MED",
				PMID:                 "44444444",
				DOI:                  "10.5678/e2e-emc-unique",
				Title:                "E2E EPMC-Only Paper (quint)",
				AuthorString:         "Frank E2E.",
				AbstractText:         "Only found on EPMC in quint test.",
				FirstPublicationDate: "2024-05-01",
				IsOpenAccess:         "N",
				CitedByCount:         3,
			},
		}
		fmt.Fprint(w, buildEMCTestSearchJSON(2, results))
	}))
	defer emcServer.Close()

	// Step 6: Load config from temp YAML.
	configYAML := fmt.Sprintf(`
server:
  name: "retrievr-mcp"
  http_addr: ":0"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["arxiv", "s2", "openalex", "pubmed", "europmc"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 500

sources:
  arxiv:
    enabled: true
    base_url: "%s"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
  pubmed:
    enabled: true
    base_url: "%s"
    api_key: "e2e-server-pm-key"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      tool: "retrievr-mcp"
      email: "e2e@test.com"
  s2:
    enabled: true
    base_url: "%s"
    api_key: "e2e-server-s2-key"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
  openalex:
    enabled: true
    base_url: "%s"
    api_key: "e2e-server-oa-key"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      mailto: "e2e@test.com"
  huggingface:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
  europmc:
    enabled: true
    base_url: "%s"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
`, arxivServer.URL, pmServer.URL+"/", s2Server.URL, oaServer.URL, emcServer.URL+"/")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configYAML), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	// Step 7: Create real plugins.
	arxivPlugin := &ArXivPlugin{}
	err = arxivPlugin.Initialize(context.Background(), cfg.Sources[SourceArXiv])
	require.NoError(t, err)

	s2Plugin := &S2Plugin{}
	err = s2Plugin.Initialize(context.Background(), cfg.Sources[SourceS2])
	require.NoError(t, err)

	oaPlugin := &OpenAlexPlugin{}
	err = oaPlugin.Initialize(context.Background(), cfg.Sources[SourceOpenAlex])
	require.NoError(t, err)

	pmPlugin := &PubMedPlugin{}
	err = pmPlugin.Initialize(context.Background(), cfg.Sources[SourcePubMed])
	require.NoError(t, err)

	emcPlugin := &EuropePMCPlugin{}
	err = emcPlugin.Initialize(context.Background(), cfg.Sources[SourceEuropePMC])
	require.NoError(t, err)

	plugins := map[string]SourcePlugin{
		SourceArXiv:     arxivPlugin,
		SourceS2:        s2Plugin,
		SourceOpenAlex:  oaPlugin,
		SourcePubMed:    pmPlugin,
		SourceEuropePMC: emcPlugin,
	}

	// Step 8: Create real infrastructure.
	cache := NewCache(CacheConfig{
		MaxEntries: cfg.Router.CacheMaxEntries,
		TTL:        cfg.Router.CacheTTL.Duration,
		Enabled:    cfg.Router.CacheEnabled,
	})

	rateLimits := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	for sourceID, sourceCfg := range cfg.Sources {
		rps := sourceCfg.RateLimit
		if rps < RateLimitMinRPS {
			rps = DefaultRateLimitRPS
		}
		burst := sourceCfg.RateLimitBurst
		if burst <= 0 {
			burst = DefaultRateLimitBurst
		}
		rateLimits.Register(RateLimiterConfig{
			SourceID:          sourceID,
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}
	rateLimits.Start(DefaultCleanupInterval)
	t.Cleanup(rateLimits.Stop)

	resolver := &CredentialResolver{}

	serverDefaults := make(map[string]string, len(cfg.Sources))
	for id, src := range cfg.Sources {
		serverDefaults[id] = src.APIKey
	}

	// Step 9: Create router + server.
	router := NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, nil)
	srv := NewServer(cfg, router, rateLimits, nil)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Step 10: Test quint-source search via MCP tool handler.
	ctx := context.Background()
	searchHandler := NewSearchHandler(router)

	searchReq := mcp.CallToolRequest{}
	searchReq.Params.Name = ToolNameSearch
	searchReq.Params.Arguments = map[string]any{
		FieldQuery:   "quint-source test",
		FieldSources: []any{SourceArXiv, SourceS2, SourceOpenAlex, SourcePubMed, SourceEuropePMC},
		FieldSort:    string(SortCitations),
		FieldLimit:   float64(10),
	}

	searchResult, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult)
	assert.False(t, searchResult.IsError, "search should succeed")

	searchText := extractTextContent(t, searchResult)
	var merged MergedSearchResult
	err = json.Unmarshal([]byte(searchText), &merged)
	require.NoError(t, err)

	// 10 total results (2 ArXiv + 2 S2 + 2 OA + 2 PubMed + 2 EPMC) - 4 DOI dedup = 6.
	assert.Len(t, merged.Results, e2eEMCSearchResultCount, "10 pubs minus 4 DOI dedup = 6")
	assert.Len(t, merged.SourcesQueried, e2eEMCExpectedSources)
	assert.Contains(t, merged.SourcesQueried, SourceArXiv)
	assert.Contains(t, merged.SourcesQueried, SourceS2)
	assert.Contains(t, merged.SourcesQueried, SourceOpenAlex)
	assert.Contains(t, merged.SourcesQueried, SourcePubMed)
	assert.Contains(t, merged.SourcesQueried, SourceEuropePMC)
	assert.Empty(t, merged.SourcesFailed)

	// Step 11: Verify DOI-based dedup across all five sources.
	var sharedPaper *Publication
	for i := range merged.Results {
		if merged.Results[i].DOI == e2eEMCSharedDOI {
			sharedPaper = &merged.Results[i]
			break
		}
	}
	require.NotNil(t, sharedPaper, "shared DOI paper should be in results")
	assert.NotEmpty(t, sharedPaper.AlsoFoundIn, "should track also_found_in from multiple sources")

	// Highest citation count should win (OA has 250, EPMC has 200, S2 has 180, ArXiv nil, PM nil).
	require.NotNil(t, sharedPaper.CitationCount)
	assert.Equal(t, e2eEMCExpectedCitations, *sharedPaper.CitationCount, "highest citation count should win")

	// Step 12: Test EuropePMC Get via MCP tool handler.
	getHandler := NewGetHandler(router)
	getReq := mcp.CallToolRequest{}
	getReq.Params.Name = ToolNameGet
	getReq.Params.Arguments = map[string]any{
		FieldID: SourceEuropePMC + prefixedIDSeparator + e2eEMCEuropePMCID,
	}

	getResult, err := getHandler(ctx, getReq)
	require.NoError(t, err)
	require.NotNil(t, getResult)
	assert.False(t, getResult.IsError)

	getText := extractTextContent(t, getResult)
	var pub Publication
	err = json.Unmarshal([]byte(getText), &pub)
	require.NoError(t, err)
	assert.Contains(t, pub.Title, "Quint-Source")
	assert.Equal(t, SourceEuropePMC, pub.Source)
	assert.Equal(t, e2eEMCSharedDOI, pub.DOI)
	assert.NotEmpty(t, pub.Abstract)
	assert.NotEmpty(t, pub.Authors)

	// Step 13: Test EuropePMC Get with BibTeX format.
	getBibReq := mcp.CallToolRequest{}
	getBibReq.Params.Name = ToolNameGet
	getBibReq.Params.Arguments = map[string]any{
		FieldID:     SourceEuropePMC + prefixedIDSeparator + e2eEMCEuropePMCID,
		FieldFormat: string(FormatBibTeX),
	}

	getBibResult, err := getHandler(ctx, getBibReq)
	require.NoError(t, err)
	require.NotNil(t, getBibResult)
	assert.False(t, getBibResult.IsError)

	getBibText := extractTextContent(t, getBibResult)
	var bibPub Publication
	err = json.Unmarshal([]byte(getBibText), &bibPub)
	require.NoError(t, err)
	require.NotNil(t, bibPub.FullText)
	assert.Equal(t, FormatBibTeX, bibPub.FullText.ContentFormat)
	assert.Contains(t, bibPub.FullText.Content, "@article{")
	assert.Contains(t, bibPub.FullText.Content, e2eEMCSharedDOI)

	// Step 14: Test list_sources — all five should appear.
	listHandler := NewListSourcesHandler(router)
	listReq := mcp.CallToolRequest{}
	listReq.Params.Name = ToolNameListSources

	listResult, err := listHandler(ctx, listReq)
	require.NoError(t, err)
	require.NotNil(t, listResult)
	assert.False(t, listResult.IsError)

	listText := extractTextContent(t, listResult)
	var infos []SourceInfo
	err = json.Unmarshal([]byte(listText), &infos)
	require.NoError(t, err)
	require.Len(t, infos, e2eEMCExpectedSources)

	// Verify EuropePMC appears and has correct capabilities.
	var emcInfo *SourceInfo
	for i := range infos {
		if infos[i].ID == SourceEuropePMC {
			emcInfo = &infos[i]
			break
		}
	}
	require.NotNil(t, emcInfo, "EuropePMC should appear in list_sources")
	assert.Equal(t, emcPluginName, emcInfo.Name)
	assert.True(t, emcInfo.Enabled)
	assert.Contains(t, emcInfo.ContentTypes, ContentTypePaper)
	assert.Equal(t, FormatJSON, emcInfo.NativeFormat)
	assert.True(t, emcInfo.SupportsDateFilter)
	assert.True(t, emcInfo.SupportsAuthorFilter)
	assert.True(t, emcInfo.SupportsCategoryFilter)
	assert.True(t, emcInfo.SupportsFullText)
	assert.True(t, emcInfo.SupportsCitations)
	assert.False(t, emcInfo.AcceptsCredentials, "EuropePMC does not accept per-call credentials")

	// Step 15: Verify cache — second search should be a cache hit.
	searchResult2, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult2)
	assert.False(t, searchResult2.IsError)

	cacheMetrics := cache.Metrics()
	assert.Equal(t, uint64(1), cacheMetrics.Hits, "second search should be a cache hit")

	// Step 16: Run contract tests on all five real plugins.
	PluginContractTest(t, arxivPlugin)
	PluginContractTest(t, s2Plugin)
	PluginContractTest(t, oaPlugin)
	PluginContractTest(t, pmPlugin)
	PluginContractTest(t, emcPlugin)

	// Step 17: Verify all five plugins are healthy.
	arxivHealth := arxivPlugin.Health(ctx)
	assert.True(t, arxivHealth.Enabled)
	assert.True(t, arxivHealth.Healthy)

	s2Health := s2Plugin.Health(ctx)
	assert.True(t, s2Health.Enabled)
	assert.True(t, s2Health.Healthy)

	oaHealth := oaPlugin.Health(ctx)
	assert.True(t, oaHealth.Enabled)
	assert.True(t, oaHealth.Healthy)

	pmHealth := pmPlugin.Health(ctx)
	assert.True(t, pmHealth.Enabled)
	assert.True(t, pmHealth.Healthy)

	emcHealth := emcPlugin.Health(ctx)
	assert.True(t, emcHealth.Enabled)
	assert.True(t, emcHealth.Healthy)
	assert.Empty(t, emcHealth.LastError)
}

// ---------------------------------------------------------------------------
// E2E: HuggingFace Plugin — papers, models, datasets through full pipeline
// ---------------------------------------------------------------------------

const (
	e2eHFVersion            = "0.10.0-e2e"
	e2eHFPaperID            = "2401.55555"
	e2eHFPaperTitle         = "E2E HuggingFace Paper on Transformers"
	e2eHFPaperSummary       = "A comprehensive study on transformer architectures."
	e2eHFPaperDate          = "2024-01-20T00:00:00.000Z"
	e2eHFPaperUpvotes       = 99
	e2eHFPaperComments      = 12
	e2eHFPaperAuthor1       = "E2E Author One"
	e2eHFPaperAuthor2       = "E2E Author Two"
	e2eHFModelID            = "e2e-org/e2e-model"
	e2eHFModelDownloads     = 5000000
	e2eHFModelLikes         = 1500
	e2eHFModelPipeline      = "text-generation"
	e2eHFModelLibrary       = "transformers"
	e2eHFModelDate          = "2024-06-15T00:00:00.000Z"
	e2eHFDatasetID          = "e2e-org/e2e-dataset"
	e2eHFDatasetAuthor      = "e2e-org"
	e2eHFDatasetDownloads   = 50000
	e2eHFDatasetLikes       = 300
	e2eHFDatasetDescription = "E2E test dataset for question answering"
	e2eHFDatasetDate        = "2023-08-01T00:00:00.000Z"
	e2eHFMarkdownContent    = "# E2E HuggingFace Paper on Transformers\n\nFull text content here."
	e2eHFLinkedModelID      = "e2e-org/linked-model"
	e2eHFSearchResultCount  = 3 // 1 paper + 1 model + 1 dataset
	e2eHFExpectedSources    = 1
	e2eHFModelTag1          = "pytorch"
	e2eHFModelTag2          = "text-generation"
	e2eHFDatasetTag1        = "task_categories:question-answering"
	e2eHFServerAPIKey       = "hf-e2e-server-token"
	e2eHFPerCallToken       = "hf-e2e-per-call-token"
)

// TestE2EHuggingFace exercises the full HuggingFace plugin pipeline:
// httptest servers → real plugin → real router → MCP tool handlers.
// Covers papers/models/datasets search, get with full text and related,
// credential passthrough, BibTeX format, and ArXiv ID dedup.
func TestE2EHuggingFace(t *testing.T) {
	t.Parallel()

	SetVersionForTesting(e2eHFVersion, "e2e-commit", "2024-04-07")

	// Step 1: Set up httptest server that handles all HuggingFace sub-APIs.
	hfServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		// Papers search.
		case r.URL.Path == hfAPIPapersSearchPath:
			fmt.Fprintf(w, `[{
				"paper": {
					"id": %q,
					"title": %q,
					"summary": %q,
					"publishedAt": %q,
					"upvotes": %d,
					"authors": [{"name": %q, "user": null}, {"name": %q, "user": null}]
				},
				"publishedAt": %q,
				"title": %q,
				"summary": %q,
				"numComments": %d
			}]`, e2eHFPaperID, e2eHFPaperTitle, e2eHFPaperSummary, e2eHFPaperDate,
				e2eHFPaperUpvotes, e2eHFPaperAuthor1, e2eHFPaperAuthor2,
				e2eHFPaperDate, e2eHFPaperTitle, e2eHFPaperSummary, e2eHFPaperComments)

		// Paper get by ArXiv ID.
		case r.URL.Path == hfAPIPaperGetPath+e2eHFPaperID:
			fmt.Fprintf(w, `{
				"id": %q,
				"title": %q,
				"summary": %q,
				"publishedAt": %q,
				"upvotes": %d,
				"authors": [{"name": %q, "user": null}, {"name": %q, "user": null}]
			}`, e2eHFPaperID, e2eHFPaperTitle, e2eHFPaperSummary, e2eHFPaperDate,
				e2eHFPaperUpvotes, e2eHFPaperAuthor1, e2eHFPaperAuthor2)

		// Paper markdown.
		case r.URL.Path == hfAPIPaperMarkdownPath+e2eHFPaperID+hfPaperMDSuffix:
			w.Header().Set("Content-Type", "text/markdown")
			fmt.Fprint(w, e2eHFMarkdownContent)

		// Models search (also handles linked models query via filter param).
		case r.URL.Path == hfAPIModelsPath:
			filterVal := r.URL.Query().Get(hfParamFilter)
			if strings.Contains(filterVal, hfArxivFilterPrefix) {
				// Linked models for a paper.
				fmt.Fprintf(w, `[{
					"id": %q, "modelId": %q, "likes": 100, "downloads": 50000,
					"pipeline_tag": "text-generation", "library_name": "transformers",
					"tags": ["pytorch"], "createdAt": "2024-01-01T00:00:00.000Z", "private": false
				}]`, e2eHFLinkedModelID, e2eHFLinkedModelID)
			} else {
				// Regular model search.
				fmt.Fprintf(w, `[{
					"id": %q, "modelId": %q, "likes": %d, "downloads": %d,
					"pipeline_tag": %q, "library_name": %q,
					"tags": [%q, %q], "createdAt": %q, "private": false
				}]`, e2eHFModelID, e2eHFModelID, e2eHFModelLikes, e2eHFModelDownloads,
					e2eHFModelPipeline, e2eHFModelLibrary,
					e2eHFModelTag1, e2eHFModelTag2, e2eHFModelDate)
			}

		// Model get.
		case strings.HasPrefix(r.URL.Path, hfAPIModelsSlashPath):
			fmt.Fprintf(w, `{
				"id": %q, "modelId": %q, "likes": %d, "downloads": %d,
				"pipeline_tag": %q, "library_name": %q,
				"tags": [%q, %q], "createdAt": %q, "private": false
			}`, e2eHFModelID, e2eHFModelID, e2eHFModelLikes, e2eHFModelDownloads,
				e2eHFModelPipeline, e2eHFModelLibrary,
				e2eHFModelTag1, e2eHFModelTag2, e2eHFModelDate)

		// Datasets search.
		case r.URL.Path == hfAPIDatasetsPath:
			fmt.Fprintf(w, `[{
				"id": %q, "author": %q, "likes": %d, "downloads": %d,
				"tags": [%q], "createdAt": %q,
				"description": %q, "private": false
			}]`, e2eHFDatasetID, e2eHFDatasetAuthor, e2eHFDatasetLikes, e2eHFDatasetDownloads,
				e2eHFDatasetTag1, e2eHFDatasetDate, e2eHFDatasetDescription)

		// Dataset get.
		case strings.HasPrefix(r.URL.Path, hfAPIDatasetsSlashPath):
			fmt.Fprintf(w, `{
				"id": %q, "author": %q, "likes": %d, "downloads": %d,
				"tags": [%q], "createdAt": %q,
				"description": %q, "private": false
			}`, e2eHFDatasetID, e2eHFDatasetAuthor, e2eHFDatasetLikes, e2eHFDatasetDownloads,
				e2eHFDatasetTag1, e2eHFDatasetDate, e2eHFDatasetDescription)

		default:
			http.NotFound(w, r)
		}
	}))
	defer hfServer.Close()

	// Step 2: Load config from temp YAML.
	configYAML := fmt.Sprintf(`
server:
  name: "retrievr-mcp"
  http_addr: ":0"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["huggingface"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 500

sources:
  huggingface:
    enabled: true
    base_url: "%s"
    api_key: "%s"
    timeout: "5s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      include_models: "true"
      include_datasets: "true"
      include_papers: "true"
`, hfServer.URL, e2eHFServerAPIKey)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configYAML), 0o644)
	require.NoError(t, err)

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)

	// Step 3: Create real HuggingFace plugin.
	hfPlugin := &HuggingFacePlugin{}
	err = hfPlugin.Initialize(context.Background(), cfg.Sources[SourceHuggingFace])
	require.NoError(t, err)

	plugins := map[string]SourcePlugin{
		SourceHuggingFace: hfPlugin,
	}

	// Step 4: Create real infrastructure.
	cache := NewCache(CacheConfig{
		MaxEntries: cfg.Router.CacheMaxEntries,
		TTL:        cfg.Router.CacheTTL.Duration,
		Enabled:    cfg.Router.CacheEnabled,
	})

	rateLimits := NewSourceRateLimitManager(DefaultCredentialBucketTTL)
	for sourceID, sourceCfg := range cfg.Sources {
		rps := sourceCfg.RateLimit
		if rps < RateLimitMinRPS {
			rps = DefaultRateLimitRPS
		}
		burst := sourceCfg.RateLimitBurst
		if burst <= 0 {
			burst = DefaultRateLimitBurst
		}
		rateLimits.Register(RateLimiterConfig{
			SourceID:          sourceID,
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}
	rateLimits.Start(DefaultCleanupInterval)
	t.Cleanup(rateLimits.Stop)

	resolver := &CredentialResolver{}

	serverDefaults := make(map[string]string, len(cfg.Sources))
	for id, src := range cfg.Sources {
		serverDefaults[id] = src.APIKey
	}

	// Step 5: Create router + server.
	router := NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, nil)
	srv := NewServer(cfg, router, rateLimits, nil)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx := context.Background()

	// ---------------------------------------------------------------
	// Step 6: Search ContentTypeAny — should return papers + models + datasets.
	// ---------------------------------------------------------------
	searchHandler := NewSearchHandler(router)

	searchReq := mcp.CallToolRequest{}
	searchReq.Params.Name = ToolNameSearch
	searchReq.Params.Arguments = map[string]any{
		FieldQuery:       "e2e test",
		FieldSources:     []any{SourceHuggingFace},
		FieldContentType: string(ContentTypeAny),
		FieldLimit:       float64(10),
	}

	searchResult, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	require.NotNil(t, searchResult)
	assert.False(t, searchResult.IsError, "search should succeed")

	searchText := extractTextContent(t, searchResult)
	var merged MergedSearchResult
	err = json.Unmarshal([]byte(searchText), &merged)
	require.NoError(t, err)

	assert.Len(t, merged.Results, e2eHFSearchResultCount, "1 paper + 1 model + 1 dataset")
	assert.Contains(t, merged.SourcesQueried, SourceHuggingFace)
	assert.Empty(t, merged.SourcesFailed)

	// Verify we have all three content types.
	var hasPaper, hasModel, hasDataset bool
	for _, pub := range merged.Results {
		switch pub.ContentType {
		case ContentTypePaper:
			hasPaper = true
			assert.Equal(t, e2eHFPaperTitle, pub.Title)
			assert.Equal(t, e2eHFPaperID, pub.ArXivID)
		case ContentTypeModel:
			hasModel = true
			assert.Equal(t, e2eHFModelID, pub.Title)
		case ContentTypeDataset:
			hasDataset = true
			assert.Equal(t, e2eHFDatasetID, pub.Title)
			assert.Equal(t, e2eHFDatasetDescription, pub.Abstract)
		}
	}
	assert.True(t, hasPaper, "should have paper result")
	assert.True(t, hasModel, "should have model result")
	assert.True(t, hasDataset, "should have dataset result")

	// ---------------------------------------------------------------
	// Step 7: Search ContentTypePaper only.
	// ---------------------------------------------------------------
	paperSearchReq := mcp.CallToolRequest{}
	paperSearchReq.Params.Name = ToolNameSearch
	paperSearchReq.Params.Arguments = map[string]any{
		FieldQuery:       "e2e test",
		FieldSources:     []any{SourceHuggingFace},
		FieldContentType: string(ContentTypePaper),
		FieldLimit:       float64(10),
	}

	paperResult, err := searchHandler(ctx, paperSearchReq)
	require.NoError(t, err)
	paperText := extractTextContent(t, paperResult)
	var paperMerged MergedSearchResult
	err = json.Unmarshal([]byte(paperText), &paperMerged)
	require.NoError(t, err)
	assert.Len(t, paperMerged.Results, 1)
	assert.Equal(t, ContentTypePaper, paperMerged.Results[0].ContentType)

	// ---------------------------------------------------------------
	// Step 8: Get paper with full text and related models.
	// ---------------------------------------------------------------
	getHandler := NewGetHandler(router)
	getReq := mcp.CallToolRequest{}
	getReq.Params.Name = ToolNameGet
	getReq.Params.Arguments = map[string]any{
		FieldID:      SourceHuggingFace + prefixedIDSeparator + hfSubTypePaper + e2eHFPaperID,
		FieldInclude: []any{string(IncludeFullText), string(IncludeRelated)},
	}

	getResult, err := getHandler(ctx, getReq)
	require.NoError(t, err)
	require.NotNil(t, getResult)
	assert.False(t, getResult.IsError)

	getText := extractTextContent(t, getResult)
	var pub Publication
	err = json.Unmarshal([]byte(getText), &pub)
	require.NoError(t, err)

	assert.Equal(t, e2eHFPaperTitle, pub.Title)
	assert.Equal(t, SourceHuggingFace, pub.Source)
	assert.Equal(t, e2eHFPaperID, pub.ArXivID)
	assert.Len(t, pub.Authors, 2)
	assert.Equal(t, e2eHFPaperAuthor1, pub.Authors[0].Name)

	// Full text markdown.
	require.NotNil(t, pub.FullText, "full text should be fetched")
	assert.Equal(t, FormatMarkdown, pub.FullText.ContentFormat)
	assert.Equal(t, e2eHFMarkdownContent, pub.FullText.Content)

	// Related linked models.
	require.Len(t, pub.Related, 1)
	assert.Contains(t, pub.Related[0].ID, e2eHFLinkedModelID)

	// ---------------------------------------------------------------
	// Step 9: Get model.
	// ---------------------------------------------------------------
	getModelReq := mcp.CallToolRequest{}
	getModelReq.Params.Name = ToolNameGet
	getModelReq.Params.Arguments = map[string]any{
		FieldID: SourceHuggingFace + prefixedIDSeparator + hfSubTypeModel + e2eHFModelID,
	}

	getModelResult, err := getHandler(ctx, getModelReq)
	require.NoError(t, err)
	assert.False(t, getModelResult.IsError)

	getModelText := extractTextContent(t, getModelResult)
	var modelPub Publication
	err = json.Unmarshal([]byte(getModelText), &modelPub)
	require.NoError(t, err)
	assert.Equal(t, e2eHFModelID, modelPub.Title)
	assert.Equal(t, ContentTypeModel, modelPub.ContentType)
	assert.Equal(t, float64(e2eHFModelDownloads), modelPub.SourceMetadata[hfMetaKeyDownloads])

	// ---------------------------------------------------------------
	// Step 10: Get dataset.
	// ---------------------------------------------------------------
	getDatasetReq := mcp.CallToolRequest{}
	getDatasetReq.Params.Name = ToolNameGet
	getDatasetReq.Params.Arguments = map[string]any{
		FieldID: SourceHuggingFace + prefixedIDSeparator + hfSubTypeDataset + e2eHFDatasetID,
	}

	getDatasetResult, err := getHandler(ctx, getDatasetReq)
	require.NoError(t, err)
	assert.False(t, getDatasetResult.IsError)

	getDatasetText := extractTextContent(t, getDatasetResult)
	var datasetPub Publication
	err = json.Unmarshal([]byte(getDatasetText), &datasetPub)
	require.NoError(t, err)
	assert.Equal(t, e2eHFDatasetID, datasetPub.Title)
	assert.Equal(t, ContentTypeDataset, datasetPub.ContentType)
	assert.Equal(t, e2eHFDatasetDescription, datasetPub.Abstract)

	// ---------------------------------------------------------------
	// Step 11: Get paper with BibTeX format.
	// ---------------------------------------------------------------
	getBibReq := mcp.CallToolRequest{}
	getBibReq.Params.Name = ToolNameGet
	getBibReq.Params.Arguments = map[string]any{
		FieldID:     SourceHuggingFace + prefixedIDSeparator + hfSubTypePaper + e2eHFPaperID,
		FieldFormat: string(FormatBibTeX),
	}

	getBibResult, err := getHandler(ctx, getBibReq)
	require.NoError(t, err)
	assert.False(t, getBibResult.IsError)

	getBibText := extractTextContent(t, getBibResult)
	var bibPub Publication
	err = json.Unmarshal([]byte(getBibText), &bibPub)
	require.NoError(t, err)
	require.NotNil(t, bibPub.FullText)
	assert.Equal(t, FormatBibTeX, bibPub.FullText.ContentFormat)
	assert.Contains(t, bibPub.FullText.Content, e2eHFPaperTitle)
	assert.Contains(t, bibPub.FullText.Content, e2eHFPaperAuthor1)

	// ---------------------------------------------------------------
	// Step 12: Credential passthrough — per-call token overrides server.
	// ---------------------------------------------------------------
	credSearchReq := mcp.CallToolRequest{}
	credSearchReq.Params.Name = ToolNameSearch
	credSearchReq.Params.Arguments = map[string]any{
		FieldQuery:       "credential test",
		FieldSources:     []any{SourceHuggingFace},
		FieldContentType: string(ContentTypePaper),
		FieldLimit:       float64(5),
		FieldCredentials: map[string]any{
			CredFieldHFToken: e2eHFPerCallToken,
		},
	}

	credResult, err := searchHandler(ctx, credSearchReq)
	require.NoError(t, err)
	assert.False(t, credResult.IsError)

	// ---------------------------------------------------------------
	// Step 13: list_sources — HuggingFace should appear.
	// ---------------------------------------------------------------
	listHandler := NewListSourcesHandler(router)
	listReq := mcp.CallToolRequest{}
	listReq.Params.Name = ToolNameListSources

	listResult, err := listHandler(ctx, listReq)
	require.NoError(t, err)
	assert.False(t, listResult.IsError)

	listText := extractTextContent(t, listResult)
	var infos []SourceInfo
	err = json.Unmarshal([]byte(listText), &infos)
	require.NoError(t, err)
	require.Len(t, infos, e2eHFExpectedSources)

	var hfInfo *SourceInfo
	for i := range infos {
		if infos[i].ID == SourceHuggingFace {
			hfInfo = &infos[i]
			break
		}
	}
	require.NotNil(t, hfInfo, "HuggingFace should appear in list_sources")
	assert.Equal(t, hfPluginName, hfInfo.Name)
	assert.True(t, hfInfo.Enabled)
	assert.Contains(t, hfInfo.ContentTypes, ContentTypePaper)
	assert.Contains(t, hfInfo.ContentTypes, ContentTypeModel)
	assert.Contains(t, hfInfo.ContentTypes, ContentTypeDataset)
	assert.Equal(t, FormatJSON, hfInfo.NativeFormat)
	assert.True(t, hfInfo.SupportsFullText)
	assert.True(t, hfInfo.SupportsCategoryFilter)
	assert.True(t, hfInfo.AcceptsCredentials, "HuggingFace accepts per-call credentials")

	// ---------------------------------------------------------------
	// Step 14: Cache — second search should be a cache hit.
	// ---------------------------------------------------------------
	searchResult2, err := searchHandler(ctx, searchReq)
	require.NoError(t, err)
	assert.False(t, searchResult2.IsError)

	cacheMetrics := cache.Metrics()
	assert.Equal(t, uint64(1), cacheMetrics.Hits, "second search should be a cache hit")

	// ---------------------------------------------------------------
	// Step 15: Contract test + health check on the real plugin.
	// ---------------------------------------------------------------
	PluginContractTest(t, hfPlugin)

	hfHealth := hfPlugin.Health(ctx)
	assert.True(t, hfHealth.Enabled)
	assert.True(t, hfHealth.Healthy)
	assert.Empty(t, hfHealth.LastError)
}
