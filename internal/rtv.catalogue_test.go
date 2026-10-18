package internal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// catalogueFloor is the non-vacuity floor: v2.29.0 ships 62 sources. A
// catalogue that shrank below this would make every coverage assertion below
// pass over a smaller set — so the floor is a number, not len(something).
const catalogueFloor = 62

func TestCatalogue_CoversEveryRegisteredSourceExactlyOnce(t *testing.T) {
	t.Parallel()
	cat := Catalogue()
	require.GreaterOrEqual(t, len(cat), catalogueFloor, "non-vacuity floor")

	counts := map[string]int{}
	for _, e := range cat {
		counts[e.ID]++
	}
	for id, n := range counts {
		assert.Equal(t, 1, n, "source %q appears %d times", id, n)
	}

	factories := PluginFactories()
	// registry → catalogue
	for id := range factories {
		assert.Contains(t, counts, id, "registered source %q missing from the catalogue", id)
	}
	// catalogue → registry
	for id := range counts {
		assert.Contains(t, factories, id, "catalogue lists %q, which no factory registers", id)
	}
	// The second id list (validSourceIDs) agrees too, both directions.
	assert.ElementsMatch(t, AllSourceIDs(), keysOf(counts))
	assert.Equal(t, SourceCount, len(cat))
}

func TestCatalogueCategoriesCoverEveryFactory(t *testing.T) {
	t.Parallel()
	factories := PluginFactories()
	require.GreaterOrEqual(t, len(sourceCategories), catalogueFloor)
	for id := range factories {
		_, ok := sourceCategories[id]
		assert.True(t, ok, "source %q has no governance category", id)
	}
	for id := range sourceCategories {
		assert.Contains(t, factories, id, "category declared for unregistered source %q", id)
	}
	valid := map[SourceCategory]bool{}
	for _, c := range AllSourceCategories() {
		valid[c] = true
	}
	for id, c := range sourceCategories {
		assert.True(t, valid[c], "source %q has undeclared category %q", id, c)
	}
	assert.Equal(t, CategoryOther, SourceCategoryFor("not-a-source"))
}

// kindCategory is the category a plugin's first declared ResultKind implies.
// The test uses it to catch a sourceCategories entry contradicting what the
// plugin says it emits.
var kindCategory = map[ResultKind]SourceCategory{
	KindPaper: CategoryAcademic, KindDataset: CategoryAcademic, KindModel: CategoryAcademic,
	KindWeb: CategoryWeb, KindNews: CategoryWeb,
	KindCode:   CategoryCode,
	KindPatent: CategoryPatentsLaw, KindLaw: CategoryPatentsLaw,
	KindPlace: CategoryPlaces, KindImage: CategoryImages,
	KindPost: CategorySocial, KindQA: CategorySocial,
	KindVideo: CategoryMedia, KindAudio: CategoryMedia,
	KindFact: CategoryOther, KindEncyclopedia: CategoryOther,
}

// categoryKindExceptions are deliberate disagreements, each with its reason.
var categoryKindExceptions = map[string]string{
	SourceORCID: "emits KindFact, but the facts are researcher records — academic",
}

func TestCatalogueCategoryAgreesWithDeclaredKinds(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, e := range Catalogue() {
		if len(e.Kinds) == 0 {
			continue
		}
		want, ok := kindCategory[e.Kinds[0]]
		require.True(t, ok, "kind %q has no category mapping", e.Kinds[0])
		if _, exc := categoryKindExceptions[e.ID]; exc {
			assert.NotEqual(t, want, e.Category, "exception for %q is stale — remove it", e.ID)
			continue
		}
		assert.Equal(t, want, e.Category, "source %q: first kind %q implies %q", e.ID, e.Kinds[0], want)
		checked++
	}
	assert.Greater(t, checked, 40, "non-vacuity: most sources declare kinds")
}

// Measured regression: a zero-value huggingface reports NO content types, so
// a catalogue built from zero-value plugins rendered it as serving nothing.
func TestCatalogue_HuggingFaceReportsItsContentTypes(t *testing.T) {
	t.Parallel()
	for _, e := range Catalogue() {
		if e.ID == SourceHuggingFace {
			assert.ElementsMatch(t, []ContentType{ContentTypePaper, ContentTypeModel, ContentTypeDataset}, e.ContentTypes)
			return
		}
	}
	t.Fatal("huggingface missing from the catalogue")
}

func TestCatalogue_EverySourceServesSomething(t *testing.T) {
	t.Parallel()
	for _, e := range Catalogue() {
		assert.NotEmpty(t, e.ContentTypes, "source %q declares no content types", e.ID)
		assert.NotEmpty(t, e.Name, e.ID)
		assert.NotEmpty(t, e.Region, e.ID)
	}
}

// The catalogue is read from default-configured plugins (empty config, no
// key). That is only sound if (a) Initialize accepts an empty config for
// every plugin and (b) no catalogue field depends on the key or endpoint a
// deployment configures — both pinned here.
func TestCatalogueIsIndependentOfInitialization(t *testing.T) {
	t.Parallel()
	n := 0
	for id, factory := range PluginFactories() {
		require.NoError(t, factory().Initialize(context.Background(), PluginConfig{Enabled: true}),
			"source %q must initialize from an empty config", id)

		p := factory()
		require.NoError(t, p.Initialize(context.Background(), PluginConfig{
			Enabled: true, APIKey: "test-key", BaseURL: "http://127.0.0.1:1",
		}), id)
		assert.Equal(t, catalogueEntryFor(defaultConfiguredPlugin(factory)), catalogueEntryFor(p),
			"source %q: static metadata depends on the configured key/endpoint", id)
		n++
	}
	assert.GreaterOrEqual(t, n, catalogueFloor)
}

// rtv_list_sources and Catalogue share one producer — so they agree on every
// static field for every registered source.
func TestListSourcesAgreesWithCatalogue(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{}
	for id, factory := range PluginFactories() {
		p := factory()
		require.NoError(t, p.Initialize(context.Background(), PluginConfig{
			Enabled: true, APIKey: "test-key", BaseURL: "http://127.0.0.1:1",
		}), id)
		plugins[id] = p
	}
	r := newTestRouterWithPlugins(t, plugins)
	infos := r.ListSources(context.Background())
	cat := Catalogue()
	require.Len(t, infos, len(cat))
	for i, e := range cat {
		info := infos[i]
		assert.Equal(t, e.ID, info.ID)
		assert.Equal(t, e.Name, info.Name)
		assert.Equal(t, e.Category, info.Category)
		assert.Equal(t, e.Region, info.Region)
		assert.Equal(t, e.DPAStatus, info.DPAStatus)
		assert.Equal(t, e.RequiresAPIKey, info.RequiresKey)
		assert.Equal(t, e.AcceptsCredentials, info.AcceptsCredentials)
		assert.Equal(t, e.SupportsGet, info.SupportsGet)
		assert.ElementsMatch(t, e.ContentTypes, info.ContentTypes)
		assert.ElementsMatch(t, e.Intents, info.QueryIntents)
	}
}

func TestCatalogueJSON_ShapeAndStability(t *testing.T) {
	t.Parallel()
	a, err := CatalogueJSON()
	require.NoError(t, err)
	b, err := CatalogueJSON()
	require.NoError(t, err)
	assert.Equal(t, a, b, "rendering must be deterministic")
	assert.Equal(t, byte('\n'), a[len(a)-1])

	var doc map[string]any
	require.NoError(t, json.Unmarshal(a, &doc))
	assert.Equal(t, CatalogueSchemaVersion, doc["schema_version"])
	sources, ok := doc["sources"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(sources), catalogueFloor)
	first, ok := sources[0].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"id", "name", "category", "content_types", "kinds", "region",
		"requires_api_key", "accepts_credentials", "supports_get", "intents"} {
		assert.Contains(t, first, key)
	}
	// Empty lists render as [], never null.
	for _, s := range sources {
		m, _ := s.(map[string]any)
		for _, key := range []string{"content_types", "kinds", "intents"} {
			assert.NotNil(t, m[key], "%v.%s rendered null", m["id"], key)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
