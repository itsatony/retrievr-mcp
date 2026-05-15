package internal

import (
	"context"
	"errors"
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
	testRegistryExpectedFactoryCount = 54 // 10 cycle-1 + 7 Wave-1 + Perplexity (cycle-3) + 2 v3-cycle-2 video (v2.3.0) + 3 v3-cycle-3 place (v2.4.0) + 2 v3-cycle-4 image (v2.5.0) + 3 v3-cycle-5 social (v2.6.0) + 2 v5-cycle-1 Q&A (v2.8.0) + 3 v5-cycle-2 OpenScience (v2.9.0) + 3 v5-cycle-3 Structured (v2.10.0) + 4 v5-cycle-4 Packages (v2.11.0) + 4 v5-cycle-5 PatentsAndLaw (v2.12.0) + 3 v5-cycle-6 TemporalArchives (v2.13.0) + 3 v6-cycle-1 GeoExpansion (v2.14.0) + 2 v6-cycle-2 AudioPodcast (v2.15.0) + 2 v6-cycle-3 PaidScholarly (v2.16.0)
	testRegistryUnknownSourceID      = "unknown_source"
	testRegistryFailingSourceID      = "failing_source"
	testRegistryFailingErrMsg        = "intentional init failure"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// failingPlugin is a minimal SourcePlugin whose Initialize always returns an error.
type failingPlugin struct{}

func (f *failingPlugin) ID() string                        { return testRegistryFailingSourceID }
func (f *failingPlugin) Name() string                      { return testRegistryFailingSourceID }
func (f *failingPlugin) Description() string               { return testRegistryFailingSourceID }
func (f *failingPlugin) ContentTypes() []ContentType       { return nil }
func (f *failingPlugin) Capabilities() SourceCapabilities  { return SourceCapabilities{} }
func (f *failingPlugin) NativeFormat() ContentFormat       { return FormatJSON }
func (f *failingPlugin) AvailableFormats() []ContentFormat { return nil }
func (f *failingPlugin) Search(_ context.Context, _ SearchParams) (*SearchResult, error) {
	return nil, nil
}
func (f *failingPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, nil
}
func (f *failingPlugin) Initialize(_ context.Context, _ PluginConfig) error {
	return errors.New(testRegistryFailingErrMsg)
}
func (f *failingPlugin) Health(_ context.Context) SourceHealth { return SourceHealth{} }
func (f *failingPlugin) Residency() ResidencyTag {
	return ResidencyTag{Region: RegionUnknown, DPAStatus: DPAUnknown}
}

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
				SourceArXiv:              {Enabled: true},
				SourceS2:                 {Enabled: true},
				SourceOpenAlex:           {Enabled: true},
				SourcePubMed:             {Enabled: true},
				SourceEuropePMC:          {Enabled: true},
				SourceHuggingFace:        {Enabled: true},
				SourceCrossRef:           {Enabled: true},
				SourceBioRxiv:            {Enabled: true},
				SourceDBLP:               {Enabled: true},
				SourceADS:                {Enabled: true},
				SourceExa:                {Enabled: true}, // Cycle 2 Wave-1
				SourceBrave:              {Enabled: true}, // Cycle 2 Wave-1
				SourceLinkup:             {Enabled: true}, // Cycle 2 Wave-1 (EU-resident)
				SourceFirecrawl:          {Enabled: true}, // Cycle 2 Wave-1
				SourceGitHub:             {Enabled: true}, // Cycle 2 Wave-1
				SourceWikipedia:          {Enabled: true}, // Cycle 2 Wave-1
				SourceUnpaywall:          {Enabled: true}, // Cycle 2 Wave-1 (enrichment)
				SourcePerplexity:         {Enabled: true}, // Cycle 3 Wave-2
				SourceYouTube:            {Enabled: true}, // v3 cycle 2 / v2.3.0
				SourceScrapingdogYouTube: {Enabled: true}, // v3 cycle 2 / v2.3.0
				SourcePhoton:             {Enabled: true}, // v3 cycle 3 / v2.4.0
				SourceTomTom:             {Enabled: true}, // v3 cycle 3 / v2.4.0
				SourceNominatim:          {Enabled: true}, // v3 cycle 3 / v2.4.0
				SourceWikimedia:          {Enabled: true}, // v3 cycle 4 / v2.5.0
				SourceEuropeana:          {Enabled: true}, // v3 cycle 4 / v2.5.0
				SourceMastodon:           {Enabled: true}, // v3 cycle 5 / v2.6.0
				SourceBluesky:            {Enabled: true}, // v3 cycle 5 / v2.6.0
				SourceReddit:             {Enabled: true}, // v3 cycle 5 / v2.6.0
				SourceStackExchange:      {Enabled: true}, // v5 cycle 1 / v2.8.0
				SourceHackerNews:         {Enabled: true}, // v5 cycle 1 / v2.8.0
				SourceZenodo:             {Enabled: true}, // v5 cycle 2 / v2.9.0
				SourceCORE:               {Enabled: true}, // v5 cycle 2 / v2.9.0
				SourceOpenAIRE:           {Enabled: true}, // v5 cycle 2 / v2.9.0
				SourceWikidata:           {Enabled: true}, // v5 cycle 3 / v2.10.0
				SourceDataCite:           {Enabled: true}, // v5 cycle 3 / v2.10.0
				SourceORCID:              {Enabled: true}, // v5 cycle 3 / v2.10.0
				SourceNPM:                {Enabled: true}, // v5 cycle 4 / v2.11.0
				SourcePyPI:               {Enabled: true}, // v5 cycle 4 / v2.11.0
				SourceCrates:             {Enabled: true}, // v5 cycle 4 / v2.11.0
				SourcePkgGoDev:           {Enabled: true}, // v5 cycle 4 / v2.11.0
				SourceGooglePatents:      {Enabled: true}, // v5 cycle 5 / v2.12.0
				SourceEPOOPS:             {Enabled: true}, // v5 cycle 5 / v2.12.0
				SourceCourtListener:      {Enabled: true}, // v5 cycle 5 / v2.12.0
				SourceEURLex:             {Enabled: true}, // v5 cycle 5 / v2.12.0
				SourceGDELT:              {Enabled: true}, // v5 cycle 6 / v2.13.0
				SourceIAScholar:          {Enabled: true}, // v5 cycle 6 / v2.13.0
				SourceWayback:            {Enabled: true}, // v5 cycle 6 / v2.13.0
				SourceGooglePlaces:       {Enabled: true}, // v6 cycle 1 / v2.14.0
				SourceOSMOverpass:        {Enabled: true}, // v6 cycle 1 / v2.14.0
				SourceHERE:               {Enabled: true}, // v6 cycle 1 / v2.14.0
				SourceListenNotes:        {Enabled: true}, // v6 cycle 2 / v2.15.0
				SourceITunes:             {Enabled: true}, // v6 cycle 2 / v2.15.0
				SourceDimensions:         {Enabled: true}, // v6 cycle 3 / v2.16.0
				SourceLens:               {Enabled: true}, // v6 cycle 3 / v2.16.0
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
				testRegistryUnknownSourceID: {Enabled: true},
				SourceArXiv:                 {Enabled: true},
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

	t.Run("init_error_returns_sentinel", func(t *testing.T) {
		t.Parallel()
		factories := map[string]PluginFactory{
			testRegistryFailingSourceID: func() SourcePlugin { return &failingPlugin{} },
		}
		cfg := &Config{
			Sources: map[string]PluginConfig{
				testRegistryFailingSourceID: {Enabled: true},
			},
		}
		plugins, err := InitializePluginsWithFactories(cfg, factories, logger)
		require.Error(t, err)
		assert.Nil(t, plugins)
		assert.True(t, errors.Is(err, ErrPluginInit),
			"expected ErrPluginInit sentinel, got: %v", err)
		assert.Contains(t, err.Error(), testRegistryFailingSourceID)
		assert.Contains(t, err.Error(), testRegistryFailingErrMsg)
	})
}
