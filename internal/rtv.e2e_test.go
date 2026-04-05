package internal

import (
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
