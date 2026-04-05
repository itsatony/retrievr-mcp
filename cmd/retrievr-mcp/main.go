package main

import (
	"flag"
	"log/slog"
	"os"

	internal "github.com/itsatony/retrievr-mcp/internal"
)

const (
	flagNameConfig  = "config"
	flagDefaultConfig = "configs/retrievr-mcp.yaml"
	flagUsageConfig = "path to config file"

	defaultVersionPath = "versions.yaml"

	logMsgStartup      = "starting retrievr-mcp"
	logMsgConfigLoaded = "config loaded"
	logMsgNotImpl      = "server not yet implemented, exiting"
)

func main() {
	configPath := flag.String(flagNameConfig, flagDefaultConfig, flagUsageConfig)
	flag.Parse()

	// Load version from versions.yaml (or ldflags).
	if err := internal.LoadVersion(defaultVersionPath); err != nil {
		slog.Error("failed to load version", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Load and validate config.
	cfg, err := internal.LoadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", slog.String("error", err.Error()), slog.String(internal.LogKeyConfig, *configPath))
		os.Exit(1)
	}

	// Setup structured logger.
	logger := setupLogger(cfg)
	slog.SetDefault(logger)

	logger.Info(logMsgStartup,
		slog.String(internal.LogKeyConfig, *configPath),
		slog.String(internal.LogKeyAddr, cfg.Server.HTTPAddr),
	)

	logger.Info(logMsgConfigLoaded,
		slog.Any(internal.LogKeySources, cfg.EnabledSourceIDs()),
	)

	// Server wiring will be added in DC-04.
	logger.Info(logMsgNotImpl)
}

func setupLogger(cfg *internal.Config) *slog.Logger {
	var level slog.Level
	switch cfg.Server.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler

	switch cfg.Server.LogFormat {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(handler).With(
		slog.String(internal.LogKeyService, cfg.Server.Name),
		slog.String(internal.LogKeyVersion, internal.Version),
	)
}
