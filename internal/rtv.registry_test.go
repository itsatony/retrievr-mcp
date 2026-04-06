package internal

import (
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

const (
	testRegistryExpectedFactoryCount = 6
)

// ---------------------------------------------------------------------------
// PluginFactories
// ---------------------------------------------------------------------------

func TestPluginFactories(t *testing.T) {
	t.Parallel()

	factories := PluginFactories()

	t.Run("has_all_sources", func(t *testing.T) {
		t.Parallel()
		assert.Len(t, factories, testRegistryExpectedFactoryCount)
		for _, id := range AllSourceIDs() {
			_, ok := factories[id]
			assert.True(t, ok, "factory missing for source %q", id)
		}
	})

	t.Run("factories_produce_non_nil_plugins", func(t *testing.T) {
		t.Parallel()
		for id, factory := range factories {
			plugin := factory()
			assert.NotNil(t, plugin, "factory for %q returned nil", id)
		}
	})

	t.Run("factory_plugins_return_correct_id", func(t *testing.T) {
		t.Parallel()
		for id, factory := range factories {
			plugin := factory()
			// Initialize with minimal config to populate identity fields.
			_ = plugin.Initialize(t.Context(), PluginConfig{Enabled: true})
			assert.Equal(t, id, plugin.ID(), "plugin ID mismatch for factory %q", id)
		}
	})
}

// ---------------------------------------------------------------------------
// InitializePlugins
// ---------------------------------------------------------------------------

func TestInitializePlugins(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	t.Run("single_enabled_source", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Sources: map[string]PluginConfig{
				SourceArXiv: {Enabled: true},
			},
		}
		plugins, err := InitializePlugins(cfg, logger)
		require.NoError(t, err)
		assert.Len(t, plugins, 1)
		assert.NotNil(t, plugins[SourceArXiv])
	})

	t.Run("all_sources_enabled", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Sources: map[string]PluginConfig{
				SourceArXiv:       {Enabled: true},
				SourceS2:          {Enabled: true},
				SourceOpenAlex:    {Enabled: true},
				SourcePubMed:      {Enabled: true},
				SourceEuropePMC:   {Enabled: true},
				SourceHuggingFace: {Enabled: true},
			},
		}
		plugins, err := InitializePlugins(cfg, logger)
		require.NoError(t, err)
		assert.Len(t, plugins, testRegistryExpectedFactoryCount)
	})

	t.Run("all_disabled_returns_empty", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Sources: map[string]PluginConfig{
				SourceArXiv:  {Enabled: false},
				SourceS2:     {Enabled: false},
				SourcePubMed: {Enabled: false},
			},
		}
		plugins, err := InitializePlugins(cfg, logger)
		require.NoError(t, err)
		assert.Empty(t, plugins)
	})

	t.Run("empty_config_returns_empty", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Sources: map[string]PluginConfig{},
		}
		plugins, err := InitializePlugins(cfg, logger)
		require.NoError(t, err)
		assert.Empty(t, plugins)
	})

	t.Run("unknown_source_id_skipped", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Sources: map[string]PluginConfig{
				"unknown_source": {Enabled: true},
				SourceArXiv:      {Enabled: true},
			},
		}
		plugins, err := InitializePlugins(cfg, logger)
		require.NoError(t, err)
		assert.Len(t, plugins, 1)
		assert.NotNil(t, plugins[SourceArXiv])
	})

	t.Run("mixed_enabled_disabled", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Sources: map[string]PluginConfig{
				SourceArXiv:  {Enabled: true},
				SourceS2:     {Enabled: false},
				SourcePubMed: {Enabled: true},
			},
		}
		plugins, err := InitializePlugins(cfg, logger)
		require.NoError(t, err)
		assert.Len(t, plugins, 2)
		assert.NotNil(t, plugins[SourceArXiv])
		assert.NotNil(t, plugins[SourcePubMed])
		assert.Nil(t, plugins[SourceS2])
	})
}
