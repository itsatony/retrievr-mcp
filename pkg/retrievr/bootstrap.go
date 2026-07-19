package retrievr

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/itsatony/retrievr-mcp/v2/internal"
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
	logger = bootstrapLogger(logger)

	cfg, err := internal.LoadConfig(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("retrievr: load config %q: %w", configPath, err)
	}
	return newClientFromConfig(cfg, logger)
}

// NewClientFromConfigBytes is the in-memory counterpart to NewClientFromConfig:
// it parses a retrievr-mcp YAML config from a byte slice (rather than a file on
// disk), then initializes plugins and returns a Client + Close function exactly
// like NewClientFromConfig. This lets embedding consumers (e.g. an nx2 service
// that //go:embeds its retrievr config) bootstrap a Client without shipping a
// config file alongside the binary.
func NewClientFromConfigBytes(data []byte, logger *slog.Logger) (*Client, func(), error) {
	logger = bootstrapLogger(logger)

	cfg, err := internal.LoadConfigFromBytes(data)
	if err != nil {
		return nil, nil, fmt.Errorf("retrievr: load config bytes: %w", err)
	}
	return newClientFromConfig(cfg, logger)
}

// bootstrapLogger returns a non-nil logger, defaulting to a discard handler, and
// best-effort loads the baked-in version (non-fatal).
func bootstrapLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	if err := internal.LoadVersion(bootstrapVersionPath); err != nil {
		// Non-fatal: callers can run without a baked-in version.
		logger.Debug(bootstrapLogVersionFail, slog.String(internal.LogKeyError, err.Error()))
	}
	return logger
}

// newClientFromConfig performs the shared plugin-init → rate-limiter → router →
// Client assembly for both NewClientFromConfig and NewClientFromConfigBytes.
func newClientFromConfig(cfg *internal.Config, logger *slog.Logger) (*Client, func(), error) {
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

	// Pre-flight providers.yaml drift check (EU-mode Hook #6). Fatal-on-strict
	// per cfg.Snapshot.Strict; warn-only otherwise.
	if err := internal.VerifyProvidersSnapshot(cfg.Snapshot, logger); err != nil {
		return nil, nil, fmt.Errorf("retrievr: %w", err)
	}

	// Wire EU-mode + audit + Unpaywall enrichment from the YAML config.
	routerOpts := []internal.RouterOption{
		internal.WithEUMode(cfg.EUMode.Mode, cfg.EUMode.IncludePublicResearch),
		internal.WithAuditSink(internal.ResolveAuditSink(cfg.Audit, logger)),
		internal.WithAuditLogQueryPlaintext(cfg.Audit.LogQueryPlaintext),
	}
	if cfg.Enrichment.Unpaywall.Enabled {
		if up, ok := plugins[internal.SourceUnpaywall].(*internal.UnpaywallPlugin); ok {
			routerOpts = append(routerOpts, internal.WithUnpaywallEnrichment(up))
		}
	}

	router := internal.NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, metrics, logger, routerOpts...)
	client := NewClientFromRouter(router, WithLogger(logger))

	return client, rateLimits.Stop, nil
}

const (
	bootstrapVersionPath    = "versions.yaml"
	bootstrapLogVersionFail = "retrievr: version load failed"
)
