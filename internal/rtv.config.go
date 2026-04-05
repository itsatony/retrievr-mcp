package internal

import (
	"fmt"
	"os"
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
)

// ---------------------------------------------------------------------------
// Config struct tree
// ---------------------------------------------------------------------------

// Config is the top-level configuration.
type Config struct {
	Server  ServerConfig            `yaml:"server" validate:"required"`
	Router  RouterConfig            `yaml:"router" validate:"required"`
	Sources map[string]PluginConfig `yaml:"sources" validate:"required,min=1"`
}

// ServerConfig holds HTTP server and logging settings.
type ServerConfig struct {
	Name      string `yaml:"name" validate:"required"`
	HTTPAddr  string `yaml:"http_addr" validate:"required"`
	LogLevel  string `yaml:"log_level" validate:"required,oneof=debug info warn error"`
	LogFormat string `yaml:"log_format" validate:"required,oneof=json text"`
}

// RouterConfig holds source routing, dedup, and cache settings.
type RouterConfig struct {
	DefaultSources   []string `yaml:"default_sources" validate:"required,min=1"`
	PerSourceTimeout Duration `yaml:"per_source_timeout" validate:"required"`
	DedupEnabled     bool     `yaml:"dedup_enabled"`
	CacheEnabled     bool     `yaml:"cache_enabled"`
	CacheTTL         Duration `yaml:"cache_ttl"`
	CacheMaxEntries  int      `yaml:"cache_max_entries" validate:"min=0"`
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
// Config loading
// ---------------------------------------------------------------------------

// configValidator is a singleton — safe for concurrent use.
var configValidator = validator.New()

// LoadConfig reads and validates a YAML config file.
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

	return &cfg, nil
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
