package internal

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Plugin contract test constants
// ---------------------------------------------------------------------------

const (
	contractTestMinMaxResults = 1 // minimum sane value for MaxResultsPerQuery
)

// validContentTypes is the set of allowed ContentType values for contract validation.
var validContentTypes = map[ContentType]bool{
	ContentTypePaper:   true,
	ContentTypeModel:   true,
	ContentTypeDataset: true,
	ContentTypeAny:     true,
}

// ---------------------------------------------------------------------------
// PluginContractTest
// ---------------------------------------------------------------------------

// PluginContractTest runs the generic contract tests that every SourcePlugin
// implementation must satisfy. Call this from your plugin's test file:
//
//	func TestArXivPluginContract(t *testing.T) {
//	    plugin := &ArXivPlugin{}
//	    _ = plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
//	    PluginContractTest(t, plugin)
//	}
func PluginContractTest(t *testing.T, plugin SourcePlugin) {
	t.Helper()

	t.Run("ID_NonEmpty", func(t *testing.T) {
		assert.NotEmpty(t, plugin.ID(), "plugin ID must not be empty")
	})

	t.Run("ID_IsValidSourceID", func(t *testing.T) {
		assert.True(t, IsValidSourceID(plugin.ID()),
			"plugin ID %q must be a registered source constant", plugin.ID())
	})

	t.Run("Name_NonEmpty", func(t *testing.T) {
		assert.NotEmpty(t, plugin.Name(), "plugin Name must not be empty")
	})

	t.Run("Description_NonEmpty", func(t *testing.T) {
		assert.NotEmpty(t, plugin.Description(), "plugin Description must not be empty")
	})

	t.Run("ContentTypes_NonEmpty", func(t *testing.T) {
		assert.NotEmpty(t, plugin.ContentTypes(), "plugin must report at least one ContentType")
	})

	t.Run("ContentTypes_ValidValues", func(t *testing.T) {
		for _, ct := range plugin.ContentTypes() {
			assert.True(t, validContentTypes[ct],
				"ContentType %q is not a valid constant", ct)
		}
	})

	t.Run("Capabilities_MaxResultsPositive", func(t *testing.T) {
		caps := plugin.Capabilities()
		if caps.SupportsPagination {
			assert.GreaterOrEqual(t, caps.MaxResultsPerQuery, contractTestMinMaxResults,
				"MaxResultsPerQuery must be >= %d when SupportsPagination is true", contractTestMinMaxResults)
		}
	})

	t.Run("NativeFormat_NonEmpty", func(t *testing.T) {
		assert.NotEmpty(t, plugin.NativeFormat(), "NativeFormat must not be empty")
	})

	t.Run("NativeFormat_InAvailableFormats", func(t *testing.T) {
		nf := plugin.NativeFormat()
		formats := plugin.AvailableFormats()
		assert.True(t, slices.Contains(formats, nf),
			"NativeFormat %q must appear in AvailableFormats %v", nf, formats)
	})

	t.Run("AvailableFormats_NonEmpty", func(t *testing.T) {
		assert.NotEmpty(t, plugin.AvailableFormats(), "AvailableFormats must not be empty")
	})

	t.Run("Health_ReturnsEnabled", func(t *testing.T) {
		health := plugin.Health(context.Background())
		assert.True(t, health.Enabled, "initialized plugin Health must report Enabled=true")
	})
}
