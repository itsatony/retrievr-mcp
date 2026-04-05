package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	internal "github.com/itsatony/retrievr-mcp/internal"
)

const (
	flagNameConfig    = "config"
	flagDefaultConfig = "configs/retrievr-mcp.yaml"
	flagUsageConfig   = "path to config file"

	defaultVersionPath = "versions.yaml"

	logMsgStartup      = "starting retrievr-mcp"
	logMsgConfigLoaded = "config loaded"
	logMsgVersionFail  = "failed to load version"
	logMsgConfigFail   = "failed to load config"

	logMsgPluginsInit    = "plugins initialized"
	logMsgRateLimitsInit = "rate limits initialized"
	logMsgRouterCreated  = "router created"
	logMsgServerCreated  = "server created"
	logMsgShutdownSignal = "received shutdown signal"
	logMsgShutdownDone   = "shutdown complete"
	logMsgShutdownFail   = "shutdown failed"
	logMsgServerFail     = "server failed"

	instanceIDBytes = 8

	exitCodeSuccess = 0
	exitCodeStartup = 1

	signalChannelSize = 1
)

func main() {
	os.Exit(run())
}

func run() int {
	// Bootstrap a JSON logger for early error messages.
	// This ensures stdout is always valid JSON, even before config is loaded.
	bootstrapLogger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(bootstrapLogger)

	configPath := flag.String(flagNameConfig, flagDefaultConfig, flagUsageConfig)
	flag.Parse()

	// Load version from versions.yaml (or ldflags).
	if err := internal.LoadVersion(defaultVersionPath); err != nil {
		bootstrapLogger.Error(logMsgVersionFail, slog.String(internal.LogKeyError, err.Error()))
		return exitCodeStartup
	}

	// Load and validate config.
	cfg, err := internal.LoadConfig(*configPath)
	if err != nil {
		bootstrapLogger.Error(logMsgConfigFail,
			slog.String(internal.LogKeyError, err.Error()),
			slog.String(internal.LogKeyConfig, *configPath),
		)
		return exitCodeStartup
	}

	// Setup structured logger with full attributes.
	logger := setupLogger(cfg)
	slog.SetDefault(logger)

	logger.Info(logMsgStartup,
		slog.String(internal.LogKeyConfig, *configPath),
		slog.String(internal.LogKeyAddr, cfg.Server.HTTPAddr),
	)

	logger.Info(logMsgConfigLoaded,
		slog.Any(internal.LogKeySources, cfg.EnabledSourceIDs()),
	)

	// Initialize plugins (empty for now — real plugins added in future cycles).
	plugins := map[string]internal.SourcePlugin{}
	logger.Info(logMsgPluginsInit, slog.Int(internal.LogKeyResultCnt, len(plugins)))

	// Step 2: Create rate limit manager and register all enabled sources.
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
	logger.Info(logMsgRateLimitsInit)

	// Step 3: Create credential resolver.
	resolver := &internal.CredentialResolver{}

	// Step 4: Create cache (if enabled).
	var cache *internal.Cache
	if cfg.Router.CacheEnabled {
		cache = internal.NewCache(internal.CacheConfig{
			MaxEntries: cfg.Router.CacheMaxEntries,
			TTL:        cfg.Router.CacheTTL.Duration,
			Enabled:    cfg.Router.CacheEnabled,
		})
	}

	// Step 5: Build server defaults map (sourceID → API key from config).
	serverDefaults := make(map[string]string, len(cfg.Sources))
	for id, src := range cfg.Sources {
		serverDefaults[id] = src.APIKey
	}

	// Step 6: Create router.
	router := internal.NewRouter(cfg.Router, plugins, serverDefaults, cache, rateLimits, resolver, logger)
	logger.Info(logMsgRouterCreated)

	// Step 7: Create MCP server.
	srv := internal.NewServer(cfg, router, rateLimits, logger)
	logger.Info(logMsgServerCreated)

	// Step 8: Signal handling — graceful shutdown on SIGTERM/SIGINT.
	errCh := make(chan error, signalChannelSize)
	go func() {
		errCh <- srv.Start()
	}()

	sigCh := make(chan os.Signal, signalChannelSize)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		logger.Info(logMsgShutdownSignal, slog.String(internal.LogKeySignal, sig.String()))
		shutdownCtx, cancel := context.WithTimeout(context.Background(), internal.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error(logMsgShutdownFail, slog.String(internal.LogKeyError, err.Error()))
			return exitCodeStartup
		}
		logger.Info(logMsgShutdownDone)
		return exitCodeSuccess

	case err := <-errCh:
		if err != nil {
			logger.Error(logMsgServerFail, slog.String(internal.LogKeyError, err.Error()))
			return exitCodeStartup
		}
		return exitCodeSuccess
	}
}

func setupLogger(cfg *internal.Config) *slog.Logger {
	var level slog.Level
	switch cfg.Server.LogLevel {
	case internal.LogLevelDebug:
		level = slog.LevelDebug
	case internal.LogLevelWarn:
		level = slog.LevelWarn
	case internal.LogLevelError:
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler

	switch cfg.Server.LogFormat {
	case internal.LogFormatText:
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler).With(
		slog.String(internal.LogKeyService, cfg.Server.Name),
		slog.String(internal.LogKeyVersion, internal.GetVersion()),
		slog.String(internal.LogKeyInstanceID, generateInstanceID()),
	)
}

func generateInstanceID() string {
	b := make([]byte, instanceIDBytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("pid-%d", os.Getpid())
	}
	return hex.EncodeToString(b)
}
