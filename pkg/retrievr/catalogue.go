package retrievr

import "github.com/itsatony/retrievr-mcp/v2/internal"

// ---------------------------------------------------------------------------
// Source catalogue — v2.28.0
//
// Catalogue is the STABLE, machine-readable description of every built-in
// source, for governance surfaces that must render the full set of sources
// without constructing a Client (obol's per-org source allow-list is the
// first consumer). It needs no config file, no credentials and no network.
//
// Stability contract: an entry's ID never changes; fields are only ever
// ADDED; the closed sets (SourceCategory, Region, ResultKind, Intent) gain
// values only in a minor release. A breaking change to the JSON document
// bumps CatalogueSchemaVersion. docs/source-catalogue.json is this function's
// output, committed and drift-guarded.
//
// rtv_list_sources / Client.ListSources read every static field through the
// same producer, so the two never disagree about a source; ListSources adds
// what only a running instance knows (enabled, live rate-limit headroom).
// ---------------------------------------------------------------------------

// SourceCategory is the governance grouping of a source.
type SourceCategory = internal.SourceCategory

// SourceCategory values.
const (
	CategoryAcademic   = internal.CategoryAcademic
	CategoryWeb        = internal.CategoryWeb
	CategoryCode       = internal.CategoryCode
	CategoryPatentsLaw = internal.CategoryPatentsLaw
	CategoryPlaces     = internal.CategoryPlaces
	CategoryImages     = internal.CategoryImages
	CategorySocial     = internal.CategorySocial
	CategoryMedia      = internal.CategoryMedia
	CategoryOther      = internal.CategoryOther
)

// CatalogueEntry is the static description of one source.
type CatalogueEntry = internal.CatalogueEntry

// CatalogueDocument is the JSON document rendered by CatalogueJSON.
type CatalogueDocument = internal.CatalogueDocument

// CatalogueSchemaVersion is the version of the CatalogueJSON document shape.
const CatalogueSchemaVersion = internal.CatalogueSchemaVersion

// Catalogue returns every built-in source, sorted by ID. Pure: no client,
// no config file, no I/O.
func Catalogue() []CatalogueEntry { return internal.Catalogue() }

// CatalogueJSON renders the catalogue document (schema_version, categories,
// sources) as indented JSON — byte-identical to docs/source-catalogue.json.
func CatalogueJSON() ([]byte, error) { return internal.CatalogueJSON() }

// AllSourceCategories returns every SourceCategory in rendering order.
func AllSourceCategories() []SourceCategory { return internal.AllSourceCategories() }

// SourceCategoryFor returns the category of a source ID (CategoryOther for
// an ID the catalogue does not know).
func SourceCategoryFor(sourceID string) SourceCategory { return internal.SourceCategoryFor(sourceID) }
