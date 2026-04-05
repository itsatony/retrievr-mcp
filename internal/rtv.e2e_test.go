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
  pubmed:
    enabled: true
    api_key: "test-pm-key"
    timeout: "10s"
    extra:
      tool: "retrievr-mcp"
      email: "test@example.com"
  s2:
    enabled: true
    api_key: "test-s2-key"
    timeout: "10s"
  openalex:
    enabled: true
    timeout: "10s"
    extra:
      mailto: "test@example.com"
  huggingface:
    enabled: true
    api_key: "test-hf-token"
    timeout: "10s"
    extra:
      include_models: "true"
      include_datasets: "true"
      include_papers: "true"
  europmc:
    enabled: true
    timeout: "10s"
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

	versionPath := filepath.Join(dir, "versions.yaml")
	err = os.WriteFile(versionPath, []byte("version: \"0.1.0\"\n"), 0o644)
	require.NoError(t, err)

	err = LoadVersion(versionPath)
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", GetVersion())

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
