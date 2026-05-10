// retrievr-cli is a thin command-line wrapper around the public
// pkg/retrievr.Client package. It exists for two reasons:
//
//  1. Validates the importable surface end-to-end — if the CLI builds and
//     runs, then liz / nexus / any other Go consumer can use the same
//     pkg/retrievr import path with the same code.
//  2. Lets developers query retrievr without having to spin up the MCP
//     server.
//
// Subcommands:
//
//	retrievr-cli search   [--intent=...] [--sources=a,b] [--limit=N] [--format=table|json] <query>
//	retrievr-cli get      [--include=abstract,citations] [--format=native|bibtex] <prefixed-id>
//	retrievr-cli sources  [--format=table|json]
//
// Per-call credentials are read from environment variables of the form
// RETRIEVR_<SOURCEID>_API_KEY (e.g. RETRIEVR_S2_API_KEY, RETRIEVR_PUBMED_API_KEY).
// They are attached to the request context via retrievr.WithCredentials.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	internal "github.com/itsatony/retrievr-mcp/internal"
	"github.com/itsatony/retrievr-mcp/pkg/retrievr"
)

const (
	flagNameConfig    = "config"
	flagDefaultConfig = "configs/retrievr-mcp.yaml"
	flagUsageConfig   = "path to retrievr-mcp YAML config file"

	flagNameVerbose = "v"
	flagUsageVerbose = "verbose: emit structured logs to stderr"

	defaultVersionPath = "versions.yaml"

	exitCodeSuccess = 0
	exitCodeUsage   = 2
	exitCodeError   = 1

	envPrefix = "RETRIEVR_"
	envSuffix = "_API_KEY"

	subcommandSearch  = "search"
	subcommandGet     = "get"
	subcommandSources = "sources"
)

// usage prints a top-level usage summary to stderr.
func usage() {
	fmt.Fprintln(os.Stderr, `retrievr-cli — query the retrievr retrieval library directly.

Usage:
  retrievr-cli [global flags] <subcommand> [subcommand flags] [args]

Subcommands:
  search    Search across one or more sources.
  get       Retrieve a single publication by prefixed ID.
  sources   List available source plugins and their capabilities.

Global flags:
  --config <path>   Path to retrievr-mcp config (default: configs/retrievr-mcp.yaml).
  -v                Verbose: emit structured logs to stderr.

Run 'retrievr-cli <subcommand> -h' for subcommand-specific help.`)
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitCodeUsage
	}

	// Hand-rolled global flag parsing: we need to extract --config and -v
	// before dispatching to a subcommand, but stdlib flag's positional
	// handling forces the subcommand name to come first, which is awkward
	// for users. Strategy: allow global flags either before or after the
	// subcommand by scanning twice — first for globals, then dispatch.
	configPath := flagDefaultConfig
	verbose := false
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--"+flagNameConfig || a == "-"+flagNameConfig:
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "missing value for --config")
				return exitCodeUsage
			}
			configPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--"+flagNameConfig+"="):
			configPath = strings.TrimPrefix(a, "--"+flagNameConfig+"=")
		case a == "-"+flagNameVerbose, a == "--"+flagNameVerbose:
			verbose = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		usage()
		return exitCodeUsage
	}

	subcmd, subArgs := rest[0], rest[1:]

	// Subcommand help short-circuits before we try to load config + plugins,
	// so users can read --help without a config on disk.
	if isHelpFlag(subArgs) {
		switch subcmd {
		case subcommandSearch:
			fmt.Fprint(os.Stderr, usageSearch)
		case subcommandGet:
			fmt.Fprint(os.Stderr, usageGet)
		case subcommandSources:
			fmt.Fprint(os.Stderr, usageSources)
		default:
			usage()
		}
		return exitCodeSuccess
	}

	logger := buildLogger(verbose)

	client, cleanup, err := buildClient(configPath, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "retrievr-cli: %v\n", err)
		return exitCodeError
	}
	defer cleanup()

	ctx := context.Background()
	ctx = retrievr.WithCredentials(ctx, credentialsFromEnv())

	switch subcmd {
	case subcommandSearch:
		return runSearch(ctx, client, subArgs)
	case subcommandGet:
		return runGet(ctx, client, subArgs)
	case subcommandSources:
		return runSources(ctx, client, subArgs)
	case "-h", "--help", "help":
		usage()
		return exitCodeSuccess
	default:
		fmt.Fprintf(os.Stderr, "retrievr-cli: unknown subcommand %q\n", subcmd)
		usage()
		return exitCodeUsage
	}
}

// isHelpFlag returns true when args contains -h, --help, or "help".
func isHelpFlag(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			return true
		}
	}
	return false
}

// buildLogger returns a stderr-bound slog logger. Verbose mode lowers level
// to debug; otherwise we discard everything (the CLI prints results to
// stdout, so logs would interleave noise into machine-readable output).
func buildLogger(verbose bool) *slog.Logger {
	if !verbose {
		return slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// credentialsFromEnv collects RETRIEVR_<SOURCEID>_API_KEY env vars into a
// per-call credentials map. Only known source IDs are admitted; unknown
// envs are ignored.
func credentialsFromEnv() map[string]string {
	out := map[string]string{}
	for _, sourceID := range internal.AllSourceIDs() {
		key := envPrefix + strings.ToUpper(sourceID) + envSuffix
		if v := os.Getenv(key); v != "" {
			out[sourceID] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildClient mirrors the bootstrap from cmd/retrievr-mcp/main.go, minus the
// HTTP server. Returns a *retrievr.Client wrapping a fully-wired Router plus
// a cleanup func the caller should defer.
func buildClient(configPath string, logger *slog.Logger) (*retrievr.Client, func(), error) {
	if err := internal.LoadVersion(defaultVersionPath); err != nil {
		// Non-fatal: cli works without a baked-in version stamp.
		logger.Debug("version load failed", slog.String(internal.LogKeyError, err.Error()))
	}

	cfg, err := internal.LoadConfig(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load config %q: %w", configPath, err)
	}

	plugins, err := internal.InitializePlugins(cfg, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize plugins: %w", err)
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

	cleanup := func() {
		rateLimits.Stop()
	}
	return retrievr.NewClientFromRouter(router, retrievr.WithLogger(logger)), cleanup, nil
}
