package internal

import "time"

// ---------------------------------------------------------------------------
// Content type constants
// ---------------------------------------------------------------------------

// ContentType represents the type of content a source provides.
type ContentType string

const (
	ContentTypePaper   ContentType = "paper"
	ContentTypeModel   ContentType = "model"
	ContentTypeDataset ContentType = "dataset"
	ContentTypeAny     ContentType = "any"
)

// ---------------------------------------------------------------------------
// Content format constants
// ---------------------------------------------------------------------------

// ContentFormat represents the format of content returned by a source.
type ContentFormat string

const (
	FormatNative   ContentFormat = "native"
	FormatJSON     ContentFormat = "json"
	FormatXML      ContentFormat = "xml"
	FormatMarkdown ContentFormat = "markdown"
	FormatBibTeX   ContentFormat = "bibtex"
)

// ---------------------------------------------------------------------------
// Include field constants
// ---------------------------------------------------------------------------

// IncludeField specifies what additional data to include in a get request.
type IncludeField string

const (
	IncludeAbstract   IncludeField = "abstract"
	IncludeFullText   IncludeField = "full_text"
	IncludeReferences IncludeField = "references"
	IncludeCitations  IncludeField = "citations"
	IncludeRelated    IncludeField = "related"
	IncludeMetadata   IncludeField = "metadata"
)

// ---------------------------------------------------------------------------
// Sort order constants
// ---------------------------------------------------------------------------

// SortOrder specifies the ordering of search results.
type SortOrder string

const (
	SortRelevance SortOrder = "relevance"
	SortDateDesc  SortOrder = "date_desc"
	SortDateAsc   SortOrder = "date_asc"
	SortCitations SortOrder = "citations"
)

// ---------------------------------------------------------------------------
// Source ID constants
// ---------------------------------------------------------------------------

const (
	SourceArXiv       = "arxiv"
	SourcePubMed      = "pubmed"
	SourceS2          = "s2"
	SourceOpenAlex    = "openalex"
	SourceHuggingFace = "huggingface"
	SourceEuropePMC   = "europmc"
)

// validSourceIDs is the internal immutable lookup set.
// Access via IsValidSourceID().
var validSourceIDs = map[string]bool{
	SourceArXiv:       true,
	SourcePubMed:      true,
	SourceS2:          true,
	SourceOpenAlex:    true,
	SourceHuggingFace: true,
	SourceEuropePMC:   true,
}

// IsValidSourceID returns true if the given ID is a known source.
func IsValidSourceID(id string) bool {
	return validSourceIDs[id]
}

// AllSourceIDs returns a fresh slice of all known source identifiers.
func AllSourceIDs() []string {
	ids := make([]string, 0, len(validSourceIDs))
	for id := range validSourceIDs {
		ids = append(ids, id)
	}
	return ids
}

// SourceCount is the number of known source plugins.
const SourceCount = 6

// ---------------------------------------------------------------------------
// Domain structs
// ---------------------------------------------------------------------------

// Publication is the unified result type across all sources.
type Publication struct {
	ID             string           `json:"id"`                        // Prefixed: "arxiv:2401.12345"
	Source         string           `json:"source"`                    // Primary source
	AlsoFoundIn    []string         `json:"also_found_in,omitempty"`   // Cross-source dedup tracking
	ContentType    ContentType      `json:"content_type"`              //nolint:tagliatelle
	Title          string           `json:"title"`                     //
	Authors        []Author         `json:"authors"`                   //
	Published      string           `json:"published"`                 // YYYY-MM-DD
	Updated        string           `json:"updated,omitempty"`         //
	Abstract       string           `json:"abstract,omitempty"`        //
	URL            string           `json:"url"`                       //
	PDFURL         string           `json:"pdf_url,omitempty"`         //
	DOI            string           `json:"doi,omitempty"`             //
	ArXivID        string           `json:"arxiv_id,omitempty"`        // For cross-source dedup
	Categories     []string         `json:"categories,omitempty"`      //
	CitationCount  *int             `json:"citation_count,omitempty"`  // Pointer: nil when unknown
	FullText       *FullTextContent `json:"full_text,omitempty"`       //
	References     []Reference      `json:"references,omitempty"`      //
	Citations      []Reference      `json:"citations,omitempty"`       //
	Related        []Reference      `json:"related,omitempty"`         //
	License        string           `json:"license,omitempty"`         //
	SourceMetadata map[string]any   `json:"source_metadata,omitempty"` //
}

// Author represents a publication author.
type Author struct {
	Name        string `json:"name"`
	Affiliation string `json:"affiliation,omitempty"`
	ORCID       string `json:"orcid,omitempty"`
}

// Reference represents a cited or related publication.
type Reference struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title"`
	Year  int    `json:"year,omitempty"`
}

// FullTextContent holds retrieved content in its native or requested format.
type FullTextContent struct {
	Content       string        `json:"content"`
	ContentFormat ContentFormat `json:"content_format"`
	ContentLength int           `json:"content_length"`
	Truncated     bool          `json:"truncated"`
}

// ---------------------------------------------------------------------------
// Search types
// ---------------------------------------------------------------------------

// SearchParams contains all parameters for a search request.
type SearchParams struct {
	Query       string        `json:"query"`
	ContentType ContentType   `json:"content_type"`
	Filters     SearchFilters `json:"filters"`
	Sort        SortOrder     `json:"sort"`
	Limit       int           `json:"limit"`
	Offset      int           `json:"offset"`
}

// SearchFilters contains optional filters to narrow search results.
type SearchFilters struct {
	Title        string   `json:"title,omitempty"`
	Authors      []string `json:"authors,omitempty"`
	DateFrom     string   `json:"date_from,omitempty"` // YYYY-MM-DD or YYYY
	DateTo       string   `json:"date_to,omitempty"`   // YYYY-MM-DD or YYYY
	Categories   []string `json:"categories,omitempty"`
	OpenAccess   *bool    `json:"open_access,omitempty"`   // Pointer: nil = not set
	MinCitations *int     `json:"min_citations,omitempty"` // Pointer: nil = not set
}

// SearchResult is the per-plugin search return type.
type SearchResult struct {
	Total   int           `json:"total"`
	Results []Publication `json:"results"`
	HasMore bool          `json:"has_more"`
}

// MergedSearchResult is the router's merged, deduplicated search response.
type MergedSearchResult struct {
	TotalResults   int           `json:"total_results"`
	Results        []Publication `json:"results"`
	SourcesQueried []string      `json:"sources_queried"`
	SourcesFailed  []string      `json:"sources_failed"`
	HasMore        bool          `json:"has_more"`
}

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

// CallCredentials carries per-call auth that overrides server config.
// Enables multi-tenant / multi-user operation.
type CallCredentials struct {
	PubMedAPIKey   string `json:"pubmed_api_key,omitempty"`
	S2APIKey       string `json:"s2_api_key,omitempty"`
	OpenAlexAPIKey string `json:"openalex_api_key,omitempty"`
	HFToken        string `json:"hf_token,omitempty"`
}

// ResolveForSource returns the credential relevant to a given source ID.
// Falls back to serverDefault if the per-call value is empty.
func (c *CallCredentials) ResolveForSource(sourceID string, serverDefault string) string {
	if c == nil {
		return serverDefault
	}

	var perCall string
	switch sourceID {
	case SourcePubMed:
		perCall = c.PubMedAPIKey
	case SourceS2:
		perCall = c.S2APIKey
	case SourceOpenAlex:
		perCall = c.OpenAlexAPIKey
	case SourceHuggingFace:
		perCall = c.HFToken
	}

	if perCall != "" {
		return perCall
	}
	return serverDefault
}

// ---------------------------------------------------------------------------
// Source capabilities and health
// ---------------------------------------------------------------------------

// SourceCapabilities reports what filtering, sorting, and features a source supports.
type SourceCapabilities struct {
	SupportsFullText         bool            `json:"supports_full_text"`
	SupportsCitations        bool            `json:"supports_citations"`
	SupportsDateFilter       bool            `json:"supports_date_filter"`
	SupportsAuthorFilter     bool            `json:"supports_author_filter"`
	SupportsCategoryFilter   bool            `json:"supports_category_filter"`
	SupportsSortRelevance    bool            `json:"supports_sort_relevance"`
	SupportsSortDate         bool            `json:"supports_sort_date"`
	SupportsSortCitations    bool            `json:"supports_sort_citations"`
	SupportsOpenAccessFilter bool            `json:"supports_open_access_filter"`
	SupportsPagination       bool            `json:"supports_pagination"`
	MaxResultsPerQuery       int             `json:"max_results_per_query"`
	CategoriesHint           string          `json:"categories_hint,omitempty"`
	NativeFormat             ContentFormat   `json:"native_format"`
	AvailableFormats         []ContentFormat `json:"available_formats"`
}

// SourceHealth represents the current health and rate-limit status of a source.
type SourceHealth struct {
	Enabled            bool    `json:"enabled"`
	Healthy            bool    `json:"healthy"`
	RateLimit          float64 `json:"requests_per_second"`
	RateLimitRemaining float64 `json:"remaining,omitempty"`
	LastError          string  `json:"last_error,omitempty"`
}

// ---------------------------------------------------------------------------
// Plugin config
// ---------------------------------------------------------------------------

// PluginConfig is the configuration for a single source plugin.
// Used both for YAML config deserialization and as the Initialize parameter.
type PluginConfig struct {
	Enabled        bool              `yaml:"enabled"                    json:"enabled"`
	APIKey         string            `yaml:"api_key,omitempty"          json:"api_key,omitempty"`
	BaseURL        string            `yaml:"base_url,omitempty"         json:"base_url,omitempty"`
	Timeout        Duration          `yaml:"timeout,omitempty"          json:"timeout,omitzero"`
	RateLimit      float64           `yaml:"rate_limit,omitempty"       json:"rate_limit,omitempty"`
	RateLimitBurst int               `yaml:"rate_limit_burst,omitempty" json:"rate_limit_burst,omitempty"`
	Extra          map[string]string `yaml:"extra,omitempty"            json:"extra,omitempty"`
}

// Duration wraps time.Duration for YAML unmarshaling from string format (e.g. "10s").
type Duration struct {
	time.Duration
}

// ---------------------------------------------------------------------------
// Source info (for rtv_list_sources response)
// ---------------------------------------------------------------------------

// RateLimitInfo provides rate limit status for a source.
type RateLimitInfo struct {
	RequestsPerSecond float64 `json:"requests_per_second"`
	Remaining         float64 `json:"remaining"`
}

// SourceInfo is the response item from the list_sources tool.
type SourceInfo struct {
	ID                     string          `json:"id"`
	Name                   string          `json:"name"`
	Description            string          `json:"description"`
	Enabled                bool            `json:"enabled"`
	ContentTypes           []ContentType   `json:"content_types"`
	NativeFormat           ContentFormat   `json:"native_format"`
	AvailableFormats       []ContentFormat `json:"available_formats"`
	SupportsFullText       bool            `json:"supports_full_text"`
	SupportsCitations      bool            `json:"supports_citations"`
	SupportsDateFilter     bool            `json:"supports_date_filter"`
	SupportsAuthorFilter   bool            `json:"supports_author_filter"`
	SupportsCategoryFilter bool            `json:"supports_category_filter"`
	RateLimit              RateLimitInfo   `json:"rate_limit"`
	CategoriesHint         string          `json:"categories_hint,omitempty"`
	AcceptsCredentials     bool            `json:"accepts_credentials"`
}
