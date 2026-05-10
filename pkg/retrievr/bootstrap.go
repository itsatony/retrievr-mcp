package retrievr

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/itsatony/retrievr-mcp/internal"
)

// NewClientFromConfig is the cycle-1 public bootstrap helper: load a
// retrievr-mcp YAML config from disk, initialize all enabled plugins, and
// return a Client wrapping the resulting Router.
//
// This exists so external callers (liz, nexus, integration tests) can wire
// a Client without re-implementing the bootstrap or reaching into
// retrievr-mcp's internal package — Go's internal-visibility rule would
// reject that.
//
// Returns the Client and a Close function the caller must defer to release
// background goroutines (rate-limit cleanup tickers).
//
// Status: cycle-1 escape hatch. Cycle 2 replaces this with a richer
// NewClient(opts ...ClientOption) that takes the config struct directly
// and exposes middleware / EU-mode / reranker / fallback hooks via options.
func NewClientFromConfig(configPath string, logger *slog.Logger) (*Client, func(), error) {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}

	if err := internal.LoadVersion(bootstrapVersionPath); err != nil {
		// Non-fatal: callers can run without a baked-in version.
		logger.Debug(bootstrapLogVersionFail, slog.String(internal.LogKeyError, err.Error()))
	}

	cfg, err := internal.LoadConfig(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("retrievr: load config %q: %w", configPath, err)
	}

	plugins, err := internal.InitializePlugins(cfg, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("retrievr: initialize plugins: %w", err)
	}

	rateLimits := internal.NewSourceRateLimitManager(internal.DefaultCredentialBucketTTL)
	for sourceID, sourceCfg := range cfg.Sources {
		if !sourceCfg.Enabled {
			continue
		}
		rps := sourceCfg.RateLimit
		if rps < internal.RateLimitMinRPS {
			rps = internal.DefaultRateLimitRPS
		}
		burst := sourceCfg.RateLimitBurst
		if burst <= 0 {
			burst = internal.DefaultRateLimitBurst
		}
		rateLimits.Register(internal.RateLimiterConfig{
			SourceID:          sourceID,
			RequestsPerSecond: rps,
			Burst:             burst,
		})
	}
	rateLimits.Start(internal.DefaultCleanupInterval)

	resolver := &internal.CredentialResolver{}
	metrics := internal.NewMetrics()

	var cache *internal.Cache
	if cfg.Router.CacheEnabled {
		cache = internal.NewCache(internal.CacheConfig{
			MaxEntries: cfg.Router.CacheMaxEntries,
			TTL:        cfg.Router.CacheTTL.Duration,
			Enabled:    cfg.Router.CacheEnabled,
		}, metrics)
	}

	serverDefaults := make(map[string]string, len(cfg.Sources))
	for id, src := range cfg.Sources {
		serverDefaults[id] = src.APIKey
	}

	router := internal.NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, metrics, logger)
	client := NewClientFromRouter(router, WithLogger(logger))

	return client, rateLimits.Stop, nil
}

const (
	bootstrapVersionPath    = "versions.yaml"
	bootstrapLogVersionFail = "retrievr: version load failed"
)
