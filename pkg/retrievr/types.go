package retrievr

import "github.com/itsatony/retrievr-mcp/internal"

// ---------------------------------------------------------------------------
// Type aliases — re-exported from internal/ for the v1.5.0 skeleton.
//
// These will retain their shapes through cycles 1 and 2. Cycle 2 introduces
// the new fat-struct Result (see result.go) alongside Publication; v1
// consumers continue using Publication via the MCP compat shim until v2.0.0.
// ---------------------------------------------------------------------------

// ContentType represents the type of content a source provides.
type ContentType = internal.ContentType

// ContentType constants.
const (
	ContentTypePaper   = internal.ContentTypePaper
	ContentTypeModel   = internal.ContentTypeModel
	ContentTypeDataset = internal.ContentTypeDataset
	ContentTypeAny     = internal.ContentTypeAny
)

// ContentFormat represents the format of content returned by a source.
type ContentFormat = internal.ContentFormat

// ContentFormat constants.
const (
	FormatNative   = internal.FormatNative
	FormatJSON     = internal.FormatJSON
	FormatXML      = internal.FormatXML
	FormatMarkdown = internal.FormatMarkdown
	FormatBibTeX   = internal.FormatBibTeX
)

// IncludeField specifies what additional data to include in a Get request.
type IncludeField = internal.IncludeField

// IncludeField constants.
const (
	IncludeAbstract   = internal.IncludeAbstract
	IncludeFullText   = internal.IncludeFullText
	IncludeReferences = internal.IncludeReferences
	IncludeCitations  = internal.IncludeCitations
	IncludeRelated    = internal.IncludeRelated
	IncludeMetadata   = internal.IncludeMetadata
)

// SortOrder specifies the ordering of search results.
type SortOrder = internal.SortOrder

// SortOrder constants.
const (
	SortRelevance = internal.SortRelevance
	SortDateDesc  = internal.SortDateDesc
	SortDateAsc   = internal.SortDateAsc
	SortCitations = internal.SortCitations
)

// Source ID constants.
const (
	SourceArXiv       = internal.SourceArXiv
	SourcePubMed      = internal.SourcePubMed
	SourceS2          = internal.SourceS2
	SourceOpenAlex    = internal.SourceOpenAlex
	SourceHuggingFace = internal.SourceHuggingFace
	SourceEuropePMC   = internal.SourceEuropePMC
	SourceCrossRef    = internal.SourceCrossRef
	SourceBioRxiv     = internal.SourceBioRxiv
	SourceDBLP        = internal.SourceDBLP
	SourceADS         = internal.SourceADS
)

// IsValidSourceID returns true if the given ID is a known source.
func IsValidSourceID(id string) bool { return internal.IsValidSourceID(id) }

// AllSourceIDs returns a fresh slice of all known source identifiers.
func AllSourceIDs() []string { return internal.AllSourceIDs() }

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Publication is the v1 unified result type. Retained through v1.6.0 alongside
// the new Result fat-struct for the MCP compat shim. Removed at v2.0.0.
type Publication = internal.Publication

// Author represents a publication author.
type Author = internal.Author

// Reference represents a cited or related publication.
type Reference = internal.Reference

// FullTextContent holds retrieved content in its native or requested format.
type FullTextContent = internal.FullTextContent

// ---------------------------------------------------------------------------
// Search input / output
// ---------------------------------------------------------------------------

// SearchParams contains all parameters for a search request.
//
// The struct gained Intent (v2.1.0), and SearchFilters gained
// IncludeDomains, ExcludeDomains, Channels, Subreddits, Language
// (v2.7.0). EUMode is handled server-side (config + per-request audit
// gate), not in SearchParams. License and MinStars are not implemented.
type SearchParams = internal.SearchParams

// SearchFilters contains optional filters to narrow search results.
type SearchFilters = internal.SearchFilters

// SearchResult is the per-plugin search return type.
type SearchResult = internal.SearchResult

// MergedSearchResult is the router's merged, deduplicated search response.
//
// Cycle-2 will extend with: Intent, Mode, ProvidersInvoked,
// ProvidersSkipped (with reason), FallbackWalked, RerankApplied, AuditRef.
type MergedSearchResult = internal.MergedSearchResult

// ---------------------------------------------------------------------------
// Plugin / source surface
// ---------------------------------------------------------------------------

// SourceCapabilities reports what filtering, sorting, and features a source supports.
type SourceCapabilities = internal.SourceCapabilities

// SourceHealth represents the current health and rate-limit status of a source.
type SourceHealth = internal.SourceHealth

// PluginConfig is the configuration for a single source plugin.
type PluginConfig = internal.PluginConfig

// Duration wraps time.Duration for YAML unmarshaling from string format.
type Duration = internal.Duration

// RateLimitInfo provides rate limit status for a source.
type RateLimitInfo = internal.RateLimitInfo

// SourceInfo is the response item from rtv_list_sources / Client.ListSources.
type SourceInfo = internal.SourceInfo
