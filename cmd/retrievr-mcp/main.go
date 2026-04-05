package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"

	internal "github.com/itsatony/retrievr-mcp/internal"
)

const (
	flagNameConfig    = "config"
	flagDefaultConfig = "configs/retrievr-mcp.yaml"
	flagUsageConfig   = "path to config file"

	defaultVersionPath = "versions.yaml"

	logMsgStartup      = "starting retrievr-mcp"
	logMsgConfigLoaded = "config loaded"
	logMsgNotImpl      = "server not yet implemented, exiting"
	logMsgVersionFail  = "failed to load version"
	logMsgConfigFail   = "failed to load config"

	instanceIDBytes = 8
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
		return 1
	}

	// Load and validate config.
	cfg, err := internal.LoadConfig(*configPath)
	if err != nil {
		bootstrapLogger.Error(logMsgConfigFail,
			slog.String(internal.LogKeyError, err.Error()),
			slog.String(internal.LogKeyConfig, *configPath),
		)
		return 1
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

	// Server wiring will be added in DC-04.
	logger.Info(logMsgNotImpl)
	return 0
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
