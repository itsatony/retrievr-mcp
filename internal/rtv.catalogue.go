package internal

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// ---------------------------------------------------------------------------
// Source catalogue — v2.28.0
//
// The catalogue is the STATIC description of every built-in source: what it
// is, which governance category it belongs to, where its provider resides,
// whether it needs a key, whether rtv_get can address it. It exists so a
// governance surface (obol's per-org source allow-list is the first) can
// render the full set of sources without constructing a client, loading a
// config or hardcoding a list that drifts.
//
// ONE PRODUCER. rtv_list_sources (Router.ListSources) and Catalogue() both
// read every static field through catalogueEntryFor, which asks the plugin
// itself (ID / Name / Description / ContentTypes / Capabilities / Residency).
// Nothing here restates a plugin fact. The only facts this file OWNS are the
// governance categories in sourceCategories, because no plugin declares one.
//
// The catalogue is built from DEFAULT-configured plugins: each factory's
// plugin is Initialize'd with an empty, enabled PluginConfig (no key, default
// endpoint). Initialize is pure config parsing for every plugin — no I/O. A
// zero-value plugin is NOT enough: huggingface derives its ContentTypes from
// Initialize (it reports none before it), measured when this file was
// written. TestCatalogueIsIndependentOfInitialization pins that the entry
// does not depend on the key or endpoint a deployment configures. The one
// config-dependent field is huggingface's content types, which an operator
// may NARROW via sources.huggingface.extra; the catalogue reports the
// source's capability, rtv_list_sources the running instance.
// ---------------------------------------------------------------------------

// SourceCategory is the governance grouping of a source. It is coarser than
// ResultKind on purpose: a per-org allow-list is rendered by category, and a
// category must be stable across releases even when a plugin starts emitting
// an extra kind.
type SourceCategory string

// SourceCategory values. The set is closed; adding one is a minor release and
// a schema note in docs/source-catalogue.md.
const (
	CategoryAcademic   SourceCategory = "academic"
	CategoryWeb        SourceCategory = "web"
	CategoryCode       SourceCategory = "code"
	CategoryPatentsLaw SourceCategory = "patents_law"
	CategoryPlaces     SourceCategory = "places"
	CategoryImages     SourceCategory = "images"
	CategorySocial     SourceCategory = "social"
	CategoryMedia      SourceCategory = "media"
	CategoryOther      SourceCategory = "other"
)

// AllSourceCategories returns every SourceCategory in rendering order.
func AllSourceCategories() []SourceCategory {
	return []SourceCategory{
		CategoryAcademic, CategoryWeb, CategoryCode, CategoryPatentsLaw,
		CategoryPlaces, CategoryImages, CategorySocial, CategoryMedia,
		CategoryOther,
	}
}

// CatalogueSchemaVersion is the version of the JSON document CatalogueJSON
// renders. Bump it only on a breaking change to the document shape (a removed
// or renamed field, a changed type); added fields and added sources do not
// bump it.
const CatalogueSchemaVersion = "1"

// catalogueDateLayout renders ResidencyTag.LastVerifiedAt as a calendar date.
const catalogueDateLayout = "2006-01-02"

// catalogueJSONIndent is the indent of the rendered catalogue document.
const catalogueJSONIndent = "  "

// sourceCategories is the governance category of every built-in source — the
// one fact the catalogue owns. A source missing here renders as
// CategoryOther, and TestCatalogueCategoriesCoverEveryFactory fails, so the
// default is never silently relied on.
var sourceCategories = map[string]SourceCategory{
	// Scholarly metadata, preprints, research data, research identity.
	SourceADS:         CategoryAcademic,
	SourceArXiv:       CategoryAcademic,
	SourceBioRxiv:     CategoryAcademic,
	SourceCORE:        CategoryAcademic,
	SourceCrossRef:    CategoryAcademic,
	SourceDataCite:    CategoryAcademic,
	SourceDBLP:        CategoryAcademic,
	SourceDimensions:  CategoryAcademic,
	SourceEuropePMC:   CategoryAcademic,
	SourceHuggingFace: CategoryAcademic, // ML models + datasets: research artefacts
	SourceIAScholar:   CategoryAcademic,
	SourceLens:        CategoryAcademic,
	SourceOpenAIRE:    CategoryAcademic,
	SourceOpenAlex:    CategoryAcademic,
	SourceORCID:       CategoryAcademic, // emits KindFact, but the facts are researcher records
	SourcePubMed:      CategoryAcademic,
	SourceS2:          CategoryAcademic,
	SourceUnpaywall:   CategoryAcademic,
	SourceZenodo:      CategoryAcademic,

	// Web search, answer engines, news, web archive.
	SourceBrave:       CategoryWeb,
	SourceExa:         CategoryWeb,
	SourceFirecrawl:   CategoryWeb,
	SourceGDELT:       CategoryWeb,
	SourceKagi:        CategoryWeb,
	SourceLinkup:      CategoryWeb,
	SourceMojeek:      CategoryWeb,
	SourceNewsAPI:     CategoryWeb,
	SourcePerplexity:  CategoryWeb,
	SourceSerpAPI:     CategoryWeb,
	SourceSerpAPINews: CategoryWeb,
	SourceStaan:       CategoryWeb,
	SourceWayback:     CategoryWeb,

	// Code hosting and package registries.
	SourceCrates:   CategoryCode,
	SourceGitHub:   CategoryCode,
	SourceNPM:      CategoryCode,
	SourcePkgGoDev: CategoryCode,
	SourcePyPI:     CategoryCode,

	// Patents and law.
	SourceCourtListener: CategoryPatentsLaw,
	SourceEPOOPS:        CategoryPatentsLaw,
	SourceEURLex:        CategoryPatentsLaw,
	SourceGooglePatents: CategoryPatentsLaw,

	// Places / geocoding / POI.
	SourceGooglePlaces: CategoryPlaces,
	SourceHERE:         CategoryPlaces,
	SourceNominatim:    CategoryPlaces,
	SourceOSMOverpass:  CategoryPlaces,
	SourcePhoton:       CategoryPlaces,
	SourceTomTom:       CategoryPlaces,

	// Images.
	SourceEuropeana: CategoryImages,
	SourceWikimedia: CategoryImages,

	// User-generated community content: social posts and Q&A.
	SourceBluesky:       CategorySocial,
	SourceHackerNews:    CategorySocial,
	SourceMastodon:      CategorySocial,
	SourceReddit:        CategorySocial,
	SourceStackExchange: CategorySocial,

	// Video and audio.
	SourceITunes:             CategoryMedia,
	SourceListenNotes:        CategoryMedia,
	SourceScrapingdogYouTube: CategoryMedia,
	SourceYouTube:            CategoryMedia,

	// Encyclopedic and computed knowledge.
	SourceKGAPI:        CategoryOther,
	SourceWikidata:     CategoryOther,
	SourceWikipedia:    CategoryOther,
	SourceWolframAlpha: CategoryOther,
}

// SourceCategoryFor returns the governance category of a source ID, or
// CategoryOther for an ID the catalogue does not know (e.g. a plugin an
// embedding consumer registered itself).
func SourceCategoryFor(sourceID string) SourceCategory {
	if c, ok := sourceCategories[sourceID]; ok {
		return c
	}
	return CategoryOther
}

// CatalogueEntry is the static, configuration-independent description of one
// source. Every field is a property of the plugin type, never of a running
// instance — so it is identical on every fleet and needs no client to read.
type CatalogueEntry struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Category    SourceCategory `json:"category"`

	// ContentTypes is what the source serves (paper, video, place, ...).
	// Kinds is the v2 ResultKind set it emits; empty for the cycle-1
	// scholarly plugins, which emit papers through the v1 conversion.
	ContentTypes []ContentType `json:"content_types"`
	Kinds        []ResultKind  `json:"kinds"`

	// Residency is the provider's region in the existing ResidencyTag
	// vocabulary (EU / UK-adequacy / US / global /
	// public-research-infrastructure / unknown) — reported, never mapped.
	Region              Region    `json:"region"`
	DPAStatus           DPAStatus `json:"dpa_status,omitempty"`
	SubprocessorURL     string    `json:"subprocessor_url,omitempty"`
	ResidencyVerifiedAt string    `json:"residency_verified_at,omitempty"`

	// RequiresAPIKey is true when the source refuses to operate without a
	// key. AcceptsCredentials is true when a per-call key is honored at all
	// (every key-requiring source, plus free sources with an optional key).
	RequiresAPIKey     bool `json:"requires_api_key"`
	AcceptsCredentials bool `json:"accepts_credentials"`

	// SupportsGet is false when rtv_get can never succeed for this source.
	SupportsGet bool `json:"supports_get"`

	// Intents are the query intents the source declares itself fit for.
	Intents []Intent `json:"intents"`
}

// CatalogueDocument is the JSON document CatalogueJSON renders and
// docs/source-catalogue.json holds.
type CatalogueDocument struct {
	SchemaVersion string           `json:"schema_version"`
	Categories    []SourceCategory `json:"categories"`
	Sources       []CatalogueEntry `json:"sources"`
}

// catalogueEntryFor reads the static description of one plugin. It is the
// single producer of every static field rtv_list_sources and Catalogue
// report.
func catalogueEntryFor(plugin SourcePlugin) CatalogueEntry {
	id := plugin.ID()
	caps := plugin.Capabilities()
	residency := plugin.Residency()

	verifiedAt := ""
	if !residency.LastVerifiedAt.IsZero() {
		verifiedAt = residency.LastVerifiedAt.UTC().Format(catalogueDateLayout)
	}
	region := residency.Region
	if region == "" {
		region = RegionUnknown
	}

	return CatalogueEntry{
		ID:                  id,
		Name:                plugin.Name(),
		Description:         plugin.Description(),
		Category:            SourceCategoryFor(id),
		ContentTypes:        nonNilSlice(plugin.ContentTypes()),
		Kinds:               nonNilSlice(caps.Kinds),
		Region:              region,
		DPAStatus:           residency.DPAStatus,
		SubprocessorURL:     residency.SubprocessorURL,
		ResidencyVerifiedAt: verifiedAt,
		RequiresAPIKey:      caps.RequiresCredential,
		AcceptsCredentials:  sourceAcceptsCredentialsFromCaps(id, caps),
		SupportsGet:         caps.SupportsGet,
		Intents:             nonNilSlice(caps.QueryIntents),
	}
}

// nonNilSlice returns a copy of s that is never nil, so a JSON rendering
// says [] rather than null — "declares none" and "absent" must not differ.
func nonNilSlice[T any](s []T) []T {
	out := make([]T, len(s))
	copy(out, s)
	return out
}

// defaultConfiguredPlugin builds a plugin as a deployment with an empty,
// enabled config would. Initialize cannot fail for an empty config
// (TestCatalogueIsIndependentOfInitialization); if a future plugin made it
// fail, the zero-value plugin is used rather than dropping the source — the
// coverage test then still sees it, and the independence test goes red.
func defaultConfiguredPlugin(factory PluginFactory) SourcePlugin {
	p := factory()
	if err := p.Initialize(context.Background(), PluginConfig{Enabled: true}); err != nil {
		return factory()
	}
	return p
}

// Catalogue returns the static description of every built-in source, sorted
// by ID. It constructs no client, reads no config file and performs no I/O.
func Catalogue() []CatalogueEntry {
	factories := PluginFactories()
	entries := make([]CatalogueEntry, 0, len(factories))
	for _, factory := range factories {
		entries = append(entries, catalogueEntryFor(defaultConfiguredPlugin(factory)))
	}
	slices.SortFunc(entries, func(a, b CatalogueEntry) int {
		return strings.Compare(a.ID, b.ID)
	})
	return entries
}

// CatalogueJSON renders the catalogue document as indented JSON with a
// trailing newline — byte-identical to docs/source-catalogue.json.
func CatalogueJSON() ([]byte, error) {
	doc := CatalogueDocument{
		SchemaVersion: CatalogueSchemaVersion,
		Categories:    AllSourceCategories(),
		Sources:       Catalogue(),
	}
	b, err := json.MarshalIndent(doc, "", catalogueJSONIndent)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
