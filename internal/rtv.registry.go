package internal

import (
	"context"
	"fmt"
	"log/slog"
)

// ---------------------------------------------------------------------------
// Plugin registry constants
// ---------------------------------------------------------------------------

const (
	logMsgPluginInitialized = "plugin initialized"
	logMsgPluginSkipped     = "plugin skipped (disabled)"

	errDetailPluginInitSource = "%s"
)

// ---------------------------------------------------------------------------
// Plugin factory
// ---------------------------------------------------------------------------

// PluginFactory creates a zero-value SourcePlugin ready for Initialize().
type PluginFactory func() SourcePlugin

// PluginFactories returns the factory map for all known source plugins.
// The map is keyed by source ID constants (SourceArXiv, SourceS2, etc.).
func PluginFactories() map[string]PluginFactory {
	return map[string]PluginFactory{
		SourceArXiv:       func() SourcePlugin { return &ArXivPlugin{} },
		SourceS2:          func() SourcePlugin { return &S2Plugin{} },
		SourceOpenAlex:    func() SourcePlugin { return &OpenAlexPlugin{} },
		SourcePubMed:      func() SourcePlugin { return &PubMedPlugin{} },
		SourceEuropePMC:   func() SourcePlugin { return &EuropePMCPlugin{} },
		SourceHuggingFace: func() SourcePlugin { return &HuggingFacePlugin{} },
		SourceCrossRef:    func() SourcePlugin { return &CrossRefPlugin{} },
		SourceBioRxiv:     func() SourcePlugin { return &BioRxivPlugin{} },
		SourceDBLP:        func() SourcePlugin { return &DBLPPlugin{} },
		SourceADS:         func() SourcePlugin { return &ADSPlugin{} },

		// Cycle-2 Wave-1 providers.
		SourceExa:       func() SourcePlugin { return &ExaPlugin{} },
		SourceBrave:     func() SourcePlugin { return &BravePlugin{} },
		SourceLinkup:    func() SourcePlugin { return &LinkupPlugin{} },
		SourceFirecrawl: func() SourcePlugin { return &FirecrawlPlugin{} },
		SourceGitHub:    func() SourcePlugin { return &GitHubPlugin{} },
		SourceWikipedia: func() SourcePlugin { return &WikipediaPlugin{} },
		SourceUnpaywall: func() SourcePlugin { return &UnpaywallPlugin{} },

		// Cycle-3 Wave-2 providers.
		SourcePerplexity: func() SourcePlugin { return &PerplexityPlugin{} },

		// v3 cycle 2 / v2.3.0 — video.
		SourceYouTube:            func() SourcePlugin { return &YouTubePlugin{} },
		SourceScrapingdogYouTube: func() SourcePlugin { return &ScrapingdogYouTubePlugin{} },

		// v3 cycle 3 / v2.4.0 — place.
		SourcePhoton:    func() SourcePlugin { return &PhotonPlugin{} },
		SourceTomTom:    func() SourcePlugin { return &TomTomPlugin{} },
		SourceNominatim: func() SourcePlugin { return &NominatimPlugin{} },

		// v3 cycle 4 / v2.5.0 — image.
		SourceWikimedia: func() SourcePlugin { return &WikimediaPlugin{} },
		SourceEuropeana: func() SourcePlugin { return &EuropeanaPlugin{} },

		// v3 cycle 5 / v2.6.0 — social posts.
		SourceMastodon: func() SourcePlugin { return &MastodonPlugin{} },
		SourceBluesky:  func() SourcePlugin { return &BlueskyPlugin{} },
		SourceReddit:   func() SourcePlugin { return &RedditPlugin{} },

		// v5 cycle 1 / v2.8.0 — Q&A.
		SourceStackExchange: func() SourcePlugin { return &StackExchangePlugin{} },
		SourceHackerNews:    func() SourcePlugin { return &HackerNewsPlugin{} },

		// v5 cycle 2 / v2.9.0 — OpenScience aggregators.
		SourceZenodo:   func() SourcePlugin { return &ZenodoPlugin{} },
		SourceCORE:     func() SourcePlugin { return &COREPlugin{} },
		SourceOpenAIRE: func() SourcePlugin { return &OpenAIREPlugin{} },

		// v5 cycle 3 / v2.10.0 — Structured knowledge.
		SourceWikidata: func() SourcePlugin { return &WikidataPlugin{} },
		SourceDataCite: func() SourcePlugin { return &DataCitePlugin{} },
		SourceORCID:    func() SourcePlugin { return &ORCIDPlugin{} },

		// v5 cycle 4 / v2.11.0 — code-package registries.
		SourceNPM:      func() SourcePlugin { return &NPMPlugin{} },
		SourcePyPI:     func() SourcePlugin { return &PyPIPlugin{} },
		SourceCrates:   func() SourcePlugin { return &CratesPlugin{} },
		SourcePkgGoDev: func() SourcePlugin { return &PkgGoDevPlugin{} },

		// v5 cycle 5 / v2.12.0 — patents + law.
		SourceGooglePatents: func() SourcePlugin { return &GooglePatentsPlugin{} },
		SourceEPOOPS:        func() SourcePlugin { return &EPOOPSPlugin{} },
		SourceCourtListener: func() SourcePlugin { return &CourtListenerPlugin{} },
		SourceEURLex:        func() SourcePlugin { return &EURLexPlugin{} },
	}
}

// ---------------------------------------------------------------------------
// Plugin initialization
// ---------------------------------------------------------------------------

// InitializePlugins creates and initializes all enabled source plugins from
// the given config using the default plugin factories. It iterates cfg.Sources,
// looks up each source ID in the factory map, creates the plugin, and calls
// Initialize. Returns the resulting plugin map or an error on the first plugin
// that fails to initialize. Unknown source IDs in config are silently skipped.
func InitializePlugins(cfg *Config, logger *slog.Logger) (map[string]SourcePlugin, error) {
	return InitializePluginsWithFactories(cfg, PluginFactories(), logger)
}

// InitializePluginsWithFactories is like InitializePlugins but accepts a
// custom factory map. This allows tests to inject mock factories for error
// path coverage.
func InitializePluginsWithFactories(cfg *Config, factories map[string]PluginFactory, logger *slog.Logger) (map[string]SourcePlugin, error) {
	plugins := make(map[string]SourcePlugin, len(cfg.Sources))

	for sourceID, sourceCfg := range cfg.Sources {
		if !sourceCfg.Enabled {
			logger.Debug(logMsgPluginSkipped,
				slog.String(LogKeySource, sourceID),
			)
			continue
		}

		factory, ok := factories[sourceID]
		if !ok {
			continue
		}

		plugin := factory()
		if err := plugin.Initialize(context.Background(), sourceCfg); err != nil {
			return nil, fmt.Errorf("%w: "+errDetailPluginInitSource+": %w", ErrPluginInit, sourceID, err)
		}

		plugins[sourceID] = plugin
		logger.Info(logMsgPluginInitialized,
			slog.String(LogKeySource, sourceID),
		)
	}

	return plugins, nil
}
