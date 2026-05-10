package internal

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Config default constants
// ---------------------------------------------------------------------------

const (
	DefaultServerName       = "retrievr-mcp"
	DefaultHTTPAddr         = ":8099"
	DefaultPerSourceTimeout = 10 * time.Second
	DefaultCacheTTL         = 5 * time.Minute
	DefaultCacheMaxEntries  = 1000
	DefaultPluginTimeout    = 10 * time.Second

	DefaultRateLimitRPS        = 1.0
	DefaultRateLimitBurst      = 3
	DefaultCredentialBucketTTL = 15 * time.Minute
	DefaultCleanupInterval     = DefaultCredentialBucketTTL / 2
)

// Log level constants — used in config validation and logger setup.
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// Log format constants — used in config validation and logger setup.
const (
	LogFormatJSON = "json"
	LogFormatText = "text"
)

// Log field key constants used across the application.
const (
	LogKeyService    = "service"
	LogKeyInstanceID = "instance_id"
	LogKeyRequestID  = "request_id"
	LogKeyTool       = "tool"
	LogKeySources    = "sources"
	LogKeyDuration   = "duration"
	LogKeyResultCnt  = "result_count"
	LogKeyAddr       = "addr"
	LogKeyConfig     = "config"
	LogKeyError      = "error"
	LogKeySource     = "source"
	LogKeyCredHash   = "cred_hash"
	LogKeyRateRPS    = "rate_rps"
	LogKeyRateBurst  = "rate_burst"
	LogKeyRateRemain = "rate_remaining"
	LogKeyCacheHit   = "cache_hit"
	LogKeyCacheKey   = "cache_key"
	LogKeyCacheSize  = "cache_size"
	LogKeyQuery      = "query"
	LogKeyLimit      = "limit"
	LogKeyPubID      = "pub_id"
	LogKeySignal     = "signal"
	LogKeyStatus     = "status"
	LogKeyEndpoint   = "endpoint"
)

// ---------------------------------------------------------------------------
// Config struct tree
// ---------------------------------------------------------------------------

// Config is the top-level configuration.
type Config struct {
	Server     ServerConfig            `yaml:"server" validate:"required"`
	Router     RouterConfig            `yaml:"router" validate:"required"`
	Sources    map[string]PluginConfig `yaml:"sources" validate:"required,min=1"`
	EUMode     EUModeConfig            `yaml:"eu_mode"`
	Audit      AuditConfig             `yaml:"audit"`
	Snapshot   SnapshotConfig          `yaml:"snapshot"`
	Enrichment EnrichmentConfig        `yaml:"enrichment"`
}

// EUModeConfig governs jurisdictional gating across all providers (Cycle 2).
// See plan §3.7 for the full design and the six audit-hook contract.
type EUModeConfig struct {
	// Mode is one of "off" | "eu_preferred" | "eu_strict".
	// Empty defaults to "off" (cycle-2 default; flips to "eu_preferred" in v2.0.0).
	Mode string `yaml:"mode" validate:"omitempty,oneof=off eu_preferred eu_strict"`

	// IncludePublicResearch widens eu_strict to admit public-research-
	// infrastructure providers (ArXiv, OpenAlex, CrossRef, Semantic Scholar,
	// PubMed, Wikipedia, Unpaywall). Ignored when Mode != eu_strict.
	IncludePublicResearch bool `yaml:"include_public_research"`
}

// AuditConfig governs the outbound-query audit log (EU-mode hook #3).
type AuditConfig struct {
	// Enabled toggles AuditEvent emission. Default: true (always emit when
	// EUMode != "off"; configurable for noise control).
	Enabled bool `yaml:"enabled"`

	// LogQueryPlaintext, when true, includes the raw query string in
	// AuditEvent.QueryPlaintext. Default: false — query is sha256-hashed
	// to the first 16 hex chars (DSGVO Art. 5(1)(c) data minimization).
	LogQueryPlaintext bool `yaml:"log_query_plaintext"`

	// Sink selects the audit destination: "slog" (default), "file", "nats".
	// "file" and "nats" are deferred to v2.1.0; "slog" routes events to
	// the Router's logger at Info level.
	Sink string `yaml:"sink" validate:"omitempty,oneof=slog file nats"`
}

// SnapshotConfig governs the providers.yaml drift guard (EU-mode hook #6).
type SnapshotConfig struct {
	// ProvidersFile is the path to a checked-in providers.yaml that
	// declares the residency-tag-bearing provider set. Cycle 2 reads this
	// at NewClient time and computes a SHA256.
	ProvidersFile string `yaml:"providers_file"`

	// SignatureFile is the path to a checked-in plain-text file holding
	// the SHA256 of ProvidersFile. Mismatch at boot emits a drift warning
	// audit event.
	SignatureFile string `yaml:"signature_file"`

	// Strict, when true, promotes drift-mismatch from warn to fatal. Used
	// in production deployments where unverified provider changes must
	// not boot.
	Strict bool `yaml:"strict"`
}

// EnrichmentConfig groups post-merge enrichment hooks. Cycle 2 ships
// Unpaywall + Firecrawl scrape; cycle 3 may add more (e.g., Cohere/Mixedbread
// rerank, currently config-less).
type EnrichmentConfig struct {
	// Unpaywall fills in PaperData.PDFURL / OpenAccess on results that
	// have a DOI but no upstream PDF link.
	Unpaywall UnpaywallEnrichmentConfig `yaml:"unpaywall"`

	// Firecrawl per-URL scrape for kind=web results with thin snippets.
	// Off by default; opt-in to avoid burning the limited Firecrawl tier.
	Firecrawl FirecrawlEnrichmentConfig `yaml:"firecrawl"`
}

// UnpaywallEnrichmentConfig is the Unpaywall-specific knob set.
type UnpaywallEnrichmentConfig struct {
	Enabled bool   `yaml:"enabled"`
	Email   string `yaml:"email" validate:"omitempty,email"`
}

// FirecrawlEnrichmentConfig is the Firecrawl-specific knob set.
type FirecrawlEnrichmentConfig struct {
	Enabled bool `yaml:"enabled"`
}

// ServerConfig holds HTTP server and logging settings.
type ServerConfig struct {
	Name      string `yaml:"name" validate:"required"`
	HTTPAddr  string `yaml:"http_addr" validate:"required"`
	LogLevel  string `yaml:"log_level" validate:"required,oneof=debug info warn error"`
	LogFormat string `yaml:"log_format" validate:"required,oneof=json text"`
}

// RouterConfig holds source routing, dedup, cache, and resilience settings.
type RouterConfig struct {
	DefaultSources   []string `yaml:"default_sources" validate:"required,min=1"`
	PerSourceTimeout Duration `yaml:"per_source_timeout" validate:"required"`
	DedupEnabled     bool     `yaml:"dedup_enabled"`
	CacheEnabled     bool     `yaml:"cache_enabled"`
	CacheTTL         Duration `yaml:"cache_ttl"`
	CacheMaxEntries  int      `yaml:"cache_max_entries" validate:"min=0"`

	// Retry governs the cycle-1 plugin-invocation retry middleware.
	// Zero values trigger DefaultRetryConfig().
	Retry RouterRetryConfig `yaml:"retry"`

	// Fallback declares per-intent primary + fallback source chains.
	// Zero/empty falls back to default chains via DefaultFallbackConfig().
	Fallback RouterFallbackConfig `yaml:"fallback"`
}

// RouterRetryConfig is the YAML-friendly mirror of RetryConfig.
// Zero values fall back to DefaultRetryConfig() at NewRouter time.
type RouterRetryConfig struct {
	MaxAttempts    int      `yaml:"max_attempts"     validate:"min=0"`
	BaseDelay      Duration `yaml:"base_delay"`
	MaxDelay       Duration `yaml:"max_delay"`
	JitterFraction float64  `yaml:"jitter_fraction"  validate:"min=0,max=1"`
}

// RouterFallbackConfig declares per-intent primary source sets + fallback
// chains. When SearchParams.Intent is non-empty AND the caller did not pass
// an explicit Sources allowlist, Router resolves the chain via this config.
//
// Cycle-1: only the academic chain is meaningfully populated. Wave-1 (cycle 2)
// adds web/code/news/reference chains when the new providers land.
type RouterFallbackConfig struct {
	// Chains maps a chain name (e.g. "academic", "web") to its primary +
	// fallback source ID lists.
	Chains map[string]FallbackChain `yaml:"chains"`

	// IntentToChain maps an Intent value to a chain name in Chains.
	IntentToChain map[string]string `yaml:"intent_to_chain"`
}

// FallbackChain describes the primary fan-out set and the ordered fallback
// list walked when the primary set yields zero results.
type FallbackChain struct {
	// Primary source IDs are fanned out concurrently, like the legacy
	// defaultSources path.
	Primary []string `yaml:"primary"`

	// Fallback source IDs are walked sequentially after the primary set
	// returned zero merged results. The first fallback that yields any hit
	// short-circuits the walk.
	Fallback []string `yaml:"fallback"`
}

// ---------------------------------------------------------------------------
// Duration YAML/JSON marshaling
// ---------------------------------------------------------------------------

// UnmarshalYAML parses a duration string (e.g. "10s", "5m") from YAML.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("%w: %w", ErrDurationParse, err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrDurationParse, s, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalYAML serializes a Duration as a string (e.g. "10s").
func (d Duration) MarshalYAML() (any, error) {
	return d.Duration.String(), nil
}

// MarshalJSON serializes a Duration as a JSON string (e.g. "10s").
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Duration.String() + `"`), nil
}

// UnmarshalJSON parses a JSON duration string (e.g. "10s").
func (d *Duration) UnmarshalJSON(b []byte) error {
	const minQuotedLen = 2 // minimum for quoted string: `""`
	if len(b) < minQuotedLen {
		return fmt.Errorf("%w: empty value", ErrDurationParse)
	}
	if b[0] != '"' || b[len(b)-1] != '"' {
		return fmt.Errorf("%w: expected quoted string, got %s", ErrDurationParse, string(b))
	}
	s := string(b[1 : len(b)-1])
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrDurationParse, s, err)
	}
	d.Duration = parsed
	return nil
}

// ---------------------------------------------------------------------------
// Environment variable override constants
// ---------------------------------------------------------------------------

const (
	// envVarPrefix is the prefix for environment variable API key overrides.
	// Convention: RETRIEVR_{UPPER_SOURCE_ID}_API_KEY (e.g., RETRIEVR_S2_API_KEY).
	envVarPrefix = "RETRIEVR_"
	envVarSuffix = "_API_KEY"
)

// ---------------------------------------------------------------------------
// Config loading
// ---------------------------------------------------------------------------

// configValidator is a singleton — safe for concurrent use.
var configValidator = validator.New()

// LoadConfig reads and validates a YAML config file.
// After validation, environment variable overrides are applied for API keys.
// Convention: RETRIEVR_{UPPER_SOURCE_ID}_API_KEY overrides sources.{id}.api_key.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfigLoad, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfigParse, err)
	}

	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}

	applyEnvOverrides(&cfg)

	return &cfg, nil
}

// applyEnvOverrides checks for RETRIEVR_{SOURCE_ID}_API_KEY environment
// variables and overwrites the corresponding source API key in config.
// This supports K8s secret injection without modifying the config file.
func applyEnvOverrides(cfg *Config) {
	for sourceID, sourceCfg := range cfg.Sources {
		envVar := envVarPrefix + strings.ToUpper(sourceID) + envVarSuffix
		if val, ok := os.LookupEnv(envVar); ok && val != "" {
			sourceCfg.APIKey = val
			cfg.Sources[sourceID] = sourceCfg
		}
	}
}

// validateConfig runs struct validation and custom business rules.
func validateConfig(cfg *Config) error {
	if err := configValidator.Struct(cfg); err != nil {
		return fmt.Errorf("%w: %w", ErrConfigValidation, err)
	}

	// Custom: all default_sources must be valid source IDs.
	for _, src := range cfg.Router.DefaultSources {
		if !IsValidSourceID(src) {
			return fmt.Errorf("%w: unknown source in default_sources: %q", ErrConfigValidation, src)
		}
	}

	// Custom: all default_sources must exist in the sources config map.
	for _, src := range cfg.Router.DefaultSources {
		if _, exists := cfg.Sources[src]; !exists {
			return fmt.Errorf("%w: default source %q not configured in sources", ErrConfigValidation, src)
		}
	}

	// Custom: at least one source must be enabled.
	hasEnabled := false
	for _, pluginCfg := range cfg.Sources {
		if pluginCfg.Enabled {
			hasEnabled = true
			break
		}
	}
	if !hasEnabled {
		return fmt.Errorf("%w: at least one source must be enabled", ErrConfigValidation)
	}

	return nil
}

// EnabledSourceIDs returns a slice of source IDs that are enabled in config.
func (c *Config) EnabledSourceIDs() []string {
	result := make([]string, 0, len(c.Sources))
	for id, src := range c.Sources {
		if src.Enabled {
			result = append(result, id)
		}
	}
	return result
}
