package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
