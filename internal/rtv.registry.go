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
