package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
		if rps < rateLimitMinRPS {
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

// e2eMockPlugin creates a mock SourcePlugin for E2E testing.
// It returns known results and implements the full interface with real values.
type e2eMockPlugin struct {
	id      string
	results []Publication
}

func (p *e2eMockPlugin) ID() string          { return p.id }
func (p *e2eMockPlugin) Name() string         { return p.id + " (e2e)" }
func (p *e2eMockPlugin) Description() string  { return "E2E test plugin for " + p.id }
func (p *e2eMockPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }
func (p *e2eMockPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsSortRelevance: true,
		SupportsSortDate:      true,
		SupportsPagination:    true,
		MaxResultsPerQuery:    100,
		NativeFormat:          FormatJSON,
		AvailableFormats:      []ContentFormat{FormatJSON},
	}
}
func (p *e2eMockPlugin) NativeFormat() ContentFormat       { return FormatJSON }
func (p *e2eMockPlugin) AvailableFormats() []ContentFormat { return []ContentFormat{FormatJSON} }
func (p *e2eMockPlugin) Health(_ context.Context) SourceHealth {
	return SourceHealth{Enabled: true, Healthy: true, RateLimit: 10.0}
}
func (p *e2eMockPlugin) Initialize(_ context.Context, _ PluginConfig) error { return nil }
func (p *e2eMockPlugin) Search(_ context.Context, _ SearchParams, _ *CallCredentials) (*SearchResult, error) {
	return &SearchResult{Total: len(p.results), Results: p.results, HasMore: false}, nil
}
func (p *e2eMockPlugin) Get(_ context.Context, id string, _ []IncludeField, _ ContentFormat, _ *CallCredentials) (*Publication, error) {
	for _, pub := range p.results {
		_, rawID, err := ParsePrefixedID(pub.ID)
		if err == nil && rawID == id {
			return &pub, nil
		}
	}
	return nil, fmt.Errorf("%w: not found", ErrGetFailed)
}

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
		if rps < rateLimitMinRPS {
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
			ID:            "arxiv:2401.99999",
			Source:        SourceArXiv,
			ContentType:   ContentTypePaper,
			Title:         "E2E Test Paper Alpha",
			Authors:       []Author{{Name: "Alice Researcher"}},
			Published:     "2024-01-15",
			URL:           "https://arxiv.org/abs/2401.99999",
			DOI:           e2eDOI,
			ArXivID:       "2401.99999",
			CitationCount: &e2eCitations10,
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
			ID:            "s2:abc123",
			Source:        SourceS2,
			ContentType:   ContentTypePaper,
			Title:         "E2E Test Paper Alpha (S2 copy)",
			Authors:       []Author{{Name: "Alice Researcher", Affiliation: "MIT"}},
			Published:     "2024-01-15",
			URL:           "https://semanticscholar.org/paper/abc123",
			DOI:           e2eDOI,
			CitationCount: &e2eCitations50,
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
		SourceArXiv: &e2eMockPlugin{id: SourceArXiv, results: arxivPubs},
		SourceS2:    &e2eMockPlugin{id: SourceS2, results: s2Pubs},
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
