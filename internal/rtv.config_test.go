package internal

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validConfigYAML = `
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
  s2:
    enabled: true
    api_key: "test-key"
    timeout: "10s"
`

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(content), 0o644)
	require.NoError(t, err)
	return path
}

// ---------------------------------------------------------------------------
// LoadConfig tests
// ---------------------------------------------------------------------------

func TestLoadConfigValid(t *testing.T) {
	path := writeConfigFile(t, validConfigYAML)

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Server
	assert.Equal(t, "retrievr-mcp", cfg.Server.Name)
	assert.Equal(t, ":8099", cfg.Server.HTTPAddr)
	assert.Equal(t, "info", cfg.Server.LogLevel)
	assert.Equal(t, "json", cfg.Server.LogFormat)

	// Router
	assert.Equal(t, []string{"arxiv", "s2"}, cfg.Router.DefaultSources)
	assert.Equal(t, 10*time.Second, cfg.Router.PerSourceTimeout.Duration)
	assert.True(t, cfg.Router.DedupEnabled)
	assert.True(t, cfg.Router.CacheEnabled)
	assert.Equal(t, 5*time.Minute, cfg.Router.CacheTTL.Duration)
	assert.Equal(t, 500, cfg.Router.CacheMaxEntries)

	// Sources
	assert.Len(t, cfg.Sources, 2)
	assert.True(t, cfg.Sources["arxiv"].Enabled)
	assert.Equal(t, 15*time.Second, cfg.Sources["arxiv"].Timeout.Duration)
	assert.True(t, cfg.Sources["s2"].Enabled)
	assert.Equal(t, "test-key", cfg.Sources["s2"].APIKey)
}

func TestLoadConfigMissingFile(t *testing.T) {
	cfg, err := LoadConfig("/nonexistent/path/config.yaml")
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConfigLoad)
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	path := writeConfigFile(t, "{{not valid yaml")
	cfg, err := LoadConfig(path)
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConfigParse)
}

func TestLoadConfigValidationFailures(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		errMsg string
	}{
		{
			name: "missing_server_name",
			yaml: `
server:
  name: ""
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"
router:
  default_sources: ["arxiv"]
  per_source_timeout: "10s"
sources:
  arxiv:
    enabled: true
`,
			errMsg: "Name",
		},
		{
			name: "invalid_log_level",
			yaml: `
server:
  name: "test"
  http_addr: ":8099"
  log_level: "verbose"
  log_format: "json"
router:
  default_sources: ["arxiv"]
  per_source_timeout: "10s"
sources:
  arxiv:
    enabled: true
`,
			errMsg: "LogLevel",
		},
		{
			name: "invalid_log_format",
			yaml: `
server:
  name: "test"
  http_addr: ":8099"
  log_level: "info"
  log_format: "csv"
router:
  default_sources: ["arxiv"]
  per_source_timeout: "10s"
sources:
  arxiv:
    enabled: true
`,
			errMsg: "LogFormat",
		},
		{
			name: "empty_default_sources",
			yaml: `
server:
  name: "test"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"
router:
  default_sources: []
  per_source_timeout: "10s"
sources:
  arxiv:
    enabled: true
`,
			errMsg: "DefaultSources",
		},
		{
			name: "no_sources",
			yaml: `
server:
  name: "test"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"
router:
  default_sources: ["arxiv"]
  per_source_timeout: "10s"
sources: {}
`,
			errMsg: "Sources",
		},
		{
			name: "no_enabled_sources",
			yaml: `
server:
  name: "test"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"
router:
  default_sources: ["arxiv"]
  per_source_timeout: "10s"
sources:
  arxiv:
    enabled: false
`,
			errMsg: "at least one source must be enabled",
		},
		{
			name: "unknown_default_source",
			yaml: `
server:
  name: "test"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"
router:
  default_sources: ["arxiv", "nonexistent"]
  per_source_timeout: "10s"
sources:
  arxiv:
    enabled: true
`,
			errMsg: "unknown source in default_sources",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfigFile(t, tt.yaml)
			cfg, err := LoadConfig(path)
			assert.Nil(t, cfg)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrConfigValidation)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

// ---------------------------------------------------------------------------
// Duration parsing tests
// ---------------------------------------------------------------------------

func TestDurationYAMLParsing(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
	}{
		{"seconds", "10s", 10 * time.Second},
		{"minutes", "5m", 5 * time.Minute},
		{"hours_minutes", "1h30m", 90 * time.Minute},
		{"milliseconds", "500ms", 500 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yamlContent := `
server:
  name: "test"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"
router:
  default_sources: ["arxiv"]
  per_source_timeout: "` + tt.input + `"
sources:
  arxiv:
    enabled: true
`
			path := writeConfigFile(t, yamlContent)
			cfg, err := LoadConfig(path)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, cfg.Router.PerSourceTimeout.Duration)
		})
	}
}

func TestDurationInvalidString(t *testing.T) {
	yamlContent := `
server:
  name: "test"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"
router:
  default_sources: ["arxiv"]
  per_source_timeout: "not-a-duration"
sources:
  arxiv:
    enabled: true
`
	path := writeConfigFile(t, yamlContent)
	cfg, err := LoadConfig(path)
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConfigParse)
}

// ---------------------------------------------------------------------------
// All 6 sources parsed
// ---------------------------------------------------------------------------

func TestLoadConfigAllSources(t *testing.T) {
	allSourcesYAML := `
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
    api_key: "pm-key"
    timeout: "10s"
    extra:
      tool: "retrievr-mcp"
      email: "test@example.com"
  s2:
    enabled: true
    api_key: "s2-key"
    timeout: "10s"
  openalex:
    enabled: true
    api_key: ""
    timeout: "10s"
    extra:
      mailto: "test@example.com"
  huggingface:
    enabled: true
    api_key: "hf-token"
    timeout: "10s"
    extra:
      include_models: "true"
      include_datasets: "true"
      include_papers: "true"
  europmc:
    enabled: true
    timeout: "10s"
`
	path := writeConfigFile(t, allSourcesYAML)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	expectedSourceCount := 6
	assert.Len(t, cfg.Sources, expectedSourceCount)

	// Verify each source exists and has expected fields.
	assert.True(t, cfg.Sources["arxiv"].Enabled)
	assert.Equal(t, 15*time.Second, cfg.Sources["arxiv"].Timeout.Duration)

	assert.Equal(t, "pm-key", cfg.Sources["pubmed"].APIKey)
	assert.Equal(t, "retrievr-mcp", cfg.Sources["pubmed"].Extra["tool"])

	assert.Equal(t, "s2-key", cfg.Sources["s2"].APIKey)

	assert.Equal(t, "test@example.com", cfg.Sources["openalex"].Extra["mailto"])

	assert.Equal(t, "hf-token", cfg.Sources["huggingface"].APIKey)
	assert.Equal(t, "true", cfg.Sources["huggingface"].Extra["include_models"])

	assert.True(t, cfg.Sources["europmc"].Enabled)
}

// ---------------------------------------------------------------------------
// EnabledSourceIDs
// ---------------------------------------------------------------------------

func TestEnabledSourceIDs(t *testing.T) {
	cfg := &Config{
		Sources: map[string]PluginConfig{
			"arxiv":  {Enabled: true},
			"s2":     {Enabled: true},
			"pubmed": {Enabled: false},
		},
	}

	enabled := cfg.EnabledSourceIDs()
	assert.Len(t, enabled, 2)
	assert.Contains(t, enabled, "arxiv")
	assert.Contains(t, enabled, "s2")
	assert.NotContains(t, enabled, "pubmed")
}

// ---------------------------------------------------------------------------
// Optional fields default to zero values
// ---------------------------------------------------------------------------

func TestLoadConfigOptionalFieldsDefault(t *testing.T) {
	minimalYAML := `
server:
  name: "test"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"
router:
  default_sources: ["arxiv"]
  per_source_timeout: "10s"
sources:
  arxiv:
    enabled: true
`
	path := writeConfigFile(t, minimalYAML)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	// Optional router fields should be zero values.
	assert.False(t, cfg.Router.DedupEnabled)
	assert.False(t, cfg.Router.CacheEnabled)
	assert.Equal(t, time.Duration(0), cfg.Router.CacheTTL.Duration)
	assert.Equal(t, 0, cfg.Router.CacheMaxEntries)

	// Optional plugin fields should be zero values.
	assert.Empty(t, cfg.Sources["arxiv"].APIKey)
	assert.Empty(t, cfg.Sources["arxiv"].BaseURL)
	assert.Nil(t, cfg.Sources["arxiv"].Extra)
}

// ---------------------------------------------------------------------------
// Duration JSON marshal/unmarshal tests
// ---------------------------------------------------------------------------

func TestDurationJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		json     string
	}{
		{"seconds", 10 * time.Second, `"10s"`},
		{"minutes", 5 * time.Minute, `"5m0s"`},
		{"zero", 0, `"0s"`},
		{"milliseconds", 500 * time.Millisecond, `"500ms"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Duration{Duration: tt.duration}

			// Marshal
			data, err := d.MarshalJSON()
			require.NoError(t, err)
			assert.Equal(t, tt.json, string(data))

			// Unmarshal
			var parsed Duration
			err = parsed.UnmarshalJSON(data)
			require.NoError(t, err)
			assert.Equal(t, tt.duration, parsed.Duration)
		})
	}
}

func TestDurationUnmarshalJSONErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"single_char", "x"},
		{"not_quoted", "10s"},
		{"number", "10"},
		{"invalid_duration", `"notaduration"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			err := d.UnmarshalJSON([]byte(tt.input))
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrDurationParse)
		})
	}
}

func TestDurationMarshalYAML(t *testing.T) {
	d := Duration{Duration: 15 * time.Second}
	val, err := d.MarshalYAML()
	require.NoError(t, err)
	assert.Equal(t, "15s", val)
}

// ---------------------------------------------------------------------------
// Config validation: default_sources must exist in sources map
// ---------------------------------------------------------------------------

func TestLoadConfigDefaultSourceNotInSourcesMap(t *testing.T) {
	yaml := `
server:
  name: "test"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"
router:
  default_sources: ["arxiv", "s2"]
  per_source_timeout: "10s"
sources:
  arxiv:
    enabled: true
`
	path := writeConfigFile(t, yaml)
	cfg, err := LoadConfig(path)
	assert.Nil(t, cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConfigValidation)
	assert.Contains(t, err.Error(), "not configured in sources")
}
