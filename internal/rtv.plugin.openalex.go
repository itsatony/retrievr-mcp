package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// OpenAlex plugin identity constants
// ---------------------------------------------------------------------------

const (
	oaPluginID          = "openalex"
	oaPluginName        = "OpenAlex"
	oaPluginDescription = "Open catalog of the global research system with 250M+ works, authors, institutions, and concepts"
)

// ---------------------------------------------------------------------------
// OpenAlex API constants
// ---------------------------------------------------------------------------

const (
	oaDefaultBaseURL    = "https://api.openalex.org"
	oaWorksSearchPath   = "/works"
	oaWorksGetPrefix    = "/works/"
	oaMaxResultsPerPage = 200
	oaMaxResponseBytes  = 10 << 20 // 10 MB upper bound
)

// ---------------------------------------------------------------------------
// OpenAlex query parameter constants
// ---------------------------------------------------------------------------

const (
	oaParamSearch  = "search"
	oaParamFilter  = "filter"
	oaParamSort    = "sort"
	oaParamPage    = "page"
	oaParamPerPage = "per_page"
	oaParamMailto  = "mailto"
	oaParamAPIKey  = "api_key"
)

// ---------------------------------------------------------------------------
// OpenAlex filter syntax constants
// ---------------------------------------------------------------------------

const (
	oaFilterTitleSearch = "title.search:"
	oaFilterPubYear     = "publication_year:"
	oaFilterFromDate    = "from_publication_date:"
	oaFilterToDate      = "to_publication_date:"
	oaFilterCitedBy     = "cited_by_count:>"
	oaFilterOpenAccess  = "open_access.is_oa:"
	oaFilterSeparator   = ","
	oaFilterTrue        = "true"
)

// ---------------------------------------------------------------------------
// OpenAlex sort constants
// ---------------------------------------------------------------------------

const (
	oaSortCitedByCountDesc = "cited_by_count:desc"
	oaSortPubDateDesc      = "publication_date:desc"
	oaSortRelevanceDesc    = "relevance_score:desc"
)

// ---------------------------------------------------------------------------
// OpenAlex HTTP constants
// ---------------------------------------------------------------------------

const oaHTTPStatusErrFmt = "status %d"

// ---------------------------------------------------------------------------
// OpenAlex date format constants
// ---------------------------------------------------------------------------

const (
	oaYearOnlyLength  = 4
	oaYearEndPad      = "-12-31"
	oaFilterMaxParts  = 4 // typical max filter count for pre-allocation
)

// ---------------------------------------------------------------------------
// OpenAlex metadata key constants
// ---------------------------------------------------------------------------

const (
	oaMetaKeyType         = "oa_type"
	oaMetaKeyConcepts     = "oa_concepts"
	oaMetaKeyTopics       = "oa_topics"
	oaMetaKeyPrimaryTopic = "oa_primary_topic"
	oaMetaKeyIsOA         = "oa_is_open_access"
	oaMetaKeyOAStatus     = "oa_oa_status"
	oaMetaKeyVenue        = "oa_venue"
	oaMetaKeyOpenAlexID   = "oa_openalex_id"
)

// ---------------------------------------------------------------------------
// OpenAlex BibTeX constants
// ---------------------------------------------------------------------------

const oaBibTeXTemplate = `@article{%s,
  title  = {%s},
  author = {%s},
  year   = {%s},
  doi    = {%s},
  url    = {%s}
}`

const (
	oaBibTeXAuthorSeparator = " and "
	oaBibTeXKeyPrefix       = "OA-"
)

// ---------------------------------------------------------------------------
// OpenAlex categories hint
// ---------------------------------------------------------------------------

const oaCategoriesHint = "Computer Science, Medicine, Biology, Physics, Mathematics, Psychology, Chemistry, Engineering, Environmental Science, Economics, Sociology, Political Science"

// ---------------------------------------------------------------------------
// OpenAlex inverted abstract constants
// ---------------------------------------------------------------------------

const (
	oaAbstractWordSeparator  = " "
	oaMaxAbstractPositions   = 10000 // safety limit against memory exhaustion
	oaAbstractInitialCapHint = 256   // initial string builder capacity
)

// ---------------------------------------------------------------------------
// OpenAlex DOI / ID normalization constants
// ---------------------------------------------------------------------------

const (
	oaDOIURLPrefix = "https://doi.org/"
	oaIDURLPrefix  = "https://openalex.org/"
)

// ---------------------------------------------------------------------------
// OpenAlex config extra key constants
// ---------------------------------------------------------------------------

const oaExtraKeyMailto = "mailto"

// ---------------------------------------------------------------------------
// OpenAlex pagination constants
// ---------------------------------------------------------------------------

const (
	oaFirstPage      = 1
	oaDefaultPerPage = 25
)

// ---------------------------------------------------------------------------
// OpenAlex JSON response struct definitions
// ---------------------------------------------------------------------------

// oaSearchResponse represents the top-level search response from the OpenAlex API.
type oaSearchResponse struct {
	Meta    oaMeta   `json:"meta"`
	Results []oaWork `json:"results"`
}

// oaMeta represents pagination metadata in an OpenAlex response.
type oaMeta struct {
	Count   int `json:"count"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
}

// oaWork represents a single work in the OpenAlex API response.
type oaWork struct {
	ID                    string           `json:"id"`
	DOI                   string           `json:"doi"`
	Title                 string           `json:"title"`
	DisplayName           string           `json:"display_name"`
	PublicationYear       int              `json:"publication_year"`
	PublicationDate       string           `json:"publication_date"`
	Type                  string           `json:"type"`
	CitedByCount          int              `json:"cited_by_count"`
	Authorships           []oaAuthorship   `json:"authorships"`
	PrimaryLocation       *oaLocation      `json:"primary_location"`
	OpenAccess            *oaOpenAccess    `json:"open_access"`
	AbstractInvertedIndex map[string][]int `json:"abstract_inverted_index"`
	Concepts              []oaConcept      `json:"concepts"`
	Topics                []oaTopic        `json:"topics"`
	PrimaryTopic          *oaTopic         `json:"primary_topic"`
	Biblio                *oaBiblio        `json:"biblio"`
	IDs                   *oaIDs           `json:"ids"`
	ReferencedWorks       []string         `json:"referenced_works"`
	RelatedWorks          []string         `json:"related_works"`
	License               string           `json:"license"`
}

// oaAuthorship represents an author-institution link.
type oaAuthorship struct {
	AuthorPosition string   `json:"author_position"`
	Author         oaAuthor `json:"author"`
	Institutions   []oaInst `json:"institutions"`
}

// oaAuthor represents an author in the OpenAlex response.
type oaAuthor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	ORCID       string `json:"orcid"`
}

// oaInst represents an institution.
type oaInst struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// oaLocation represents a publication location (journal, repository, etc.).
type oaLocation struct {
	Source         *oaSource `json:"source"`
	PDFURL         string    `json:"pdf_url"`
	LandingPageURL string    `json:"landing_page_url"`
	IsOA           bool      `json:"is_oa"`
}

// oaSource represents the venue/journal source.
type oaSource struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	ISSN        string `json:"issn_l"`
}

// oaOpenAccess represents open access status.
type oaOpenAccess struct {
	IsOA     bool   `json:"is_oa"`
	OAURL    string `json:"oa_url"`
	OAStatus string `json:"oa_status"`
}

// oaConcept represents a concept/topic tag.
type oaConcept struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"display_name"`
	Level       int     `json:"level"`
	Score       float64 `json:"score"`
}

// oaTopic represents a research topic.
type oaTopic struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// oaBiblio represents bibliographic metadata.
type oaBiblio struct {
	Volume    string `json:"volume"`
	Issue     string `json:"issue"`
	FirstPage string `json:"first_page"`
	LastPage  string `json:"last_page"`
}

// oaIDs represents external identifier mappings.
type oaIDs struct {
	OpenAlex string `json:"openalex"`
	DOI      string `json:"doi"`
	PMID     string `json:"pmid"`
}

// ---------------------------------------------------------------------------
// OpenAlexPlugin struct
// ---------------------------------------------------------------------------

// OpenAlexPlugin implements SourcePlugin for OpenAlex.
// Thread-safe for concurrent use after Initialize.
type OpenAlexPlugin struct {
	baseURL    string
	apiKey     string // server-level default from config
	mailto     string // polite pool email from config extra
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex // protects health state below
	healthy   bool
	lastError string
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: identity methods
// ---------------------------------------------------------------------------

// ID returns the unique source identifier.
func (p *OpenAlexPlugin) ID() string { return oaPluginID }

// Name returns a human-readable name.
func (p *OpenAlexPlugin) Name() string { return oaPluginName }

// Description returns a short description for LLM context.
func (p *OpenAlexPlugin) Description() string { return oaPluginDescription }

// ContentTypes returns the types of content this source provides.
func (p *OpenAlexPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat returns the default content format.
func (p *OpenAlexPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats returns all formats this source can provide.
func (p *OpenAlexPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Capabilities
// ---------------------------------------------------------------------------

// Capabilities reports what filtering, sorting, and features OpenAlex supports.
func (p *OpenAlexPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    true,
		SupportsOpenAccessFilter: true,
		SupportsPagination:       true,
		MaxResultsPerQuery:       oaMaxResultsPerPage,
		CategoriesHint:           oaCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Initialize
// ---------------------------------------------------------------------------

// Initialize sets up the OpenAlex plugin with the given configuration.
// Called once at startup.
func (p *OpenAlexPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = oaDefaultBaseURL
	}

	if cfg.Extra != nil {
		p.mailto = cfg.Extra[oaExtraKeyMailto]
	}

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}

	p.httpClient = &http.Client{Timeout: timeout}
	p.healthy = true

	return nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Health
// ---------------------------------------------------------------------------

// Health returns current health and rate-limit status.
func (p *OpenAlexPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Search
// ---------------------------------------------------------------------------

// Search executes a search query against the OpenAlex works API.
func (p *OpenAlexPlugin) Search(ctx context.Context, params SearchParams, creds *CallCredentials) (*SearchResult, error) {
	if params.Query == "" {
		return nil, ErrOAEmptyQuery
	}

	apiKey := resolveOAAPIKey(creds, p.apiKey)
	reqURL := buildOASearchURL(p.baseURL, params, p.mailto, apiKey)

	var response oaSearchResponse
	if err := p.doRequest(ctx, reqURL, &response); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}

	p.recordSuccess()

	pubs := make([]Publication, 0, len(response.Results))
	for i := range response.Results {
		pubs = append(pubs, mapOAWorkToPublication(&response.Results[i]))
	}

	hasMore := response.Meta.Count > response.Meta.Page*response.Meta.PerPage

	return &SearchResult{
		Total:   response.Meta.Count,
		Results: pubs,
		HasMore: hasMore,
	}, nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Get
// ---------------------------------------------------------------------------

// Get retrieves a single work by its OpenAlex ID.
func (p *OpenAlexPlugin) Get(ctx context.Context, id string, _ []IncludeField, format ContentFormat, creds *CallCredentials) (*Publication, error) {
	apiKey := resolveOAAPIKey(creds, p.apiKey)
	reqURL := buildOAGetURL(p.baseURL, id, p.mailto, apiKey)

	var work oaWork
	if err := p.doRequest(ctx, reqURL, &work); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	if work.ID == "" {
		return nil, fmt.Errorf("%w: %s", ErrOANotFound, id)
	}

	p.recordSuccess()

	pub := mapOAWorkToPublication(&work)

	// Apply format conversion if not native JSON.
	if format != FormatNative && format != FormatJSON {
		if err := convertOAFormat(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

// doRequest executes an HTTP GET and decodes the JSON response into the target.
// Auth and mailto are already embedded in the URL as query parameters.
func (p *OpenAlexPlugin) doRequest(ctx context.Context, reqURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOAHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return fmt.Errorf("%w: %w", ErrOAHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrOANotFound
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%w: "+oaHTTPStatusErrFmt, ErrOAHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, oaMaxResponseBytes)
	if err := json.NewDecoder(limitedBody).Decode(target); err != nil {
		return fmt.Errorf("%w: %w", ErrOAJSONParse, err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Health state helpers
// ---------------------------------------------------------------------------

func (p *OpenAlexPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *OpenAlexPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	p.lastError = err.Error()
}

// ---------------------------------------------------------------------------
// Credential resolution helper
// ---------------------------------------------------------------------------

// resolveOAAPIKey extracts the effective API key from per-call credentials
// and server default, following the three-level resolution chain.
func resolveOAAPIKey(creds *CallCredentials, serverDefault string) string {
	if creds != nil {
		return creds.ResolveForSource(SourceOpenAlex, serverDefault)
	}
	return serverDefault
}

// ---------------------------------------------------------------------------
// URL / query building
// ---------------------------------------------------------------------------

// buildOASearchURL assembles the full search URL with query parameters.
func buildOASearchURL(baseURL string, params SearchParams, mailto, apiKey string) string {
	qParams := url.Values{}
	qParams.Set(oaParamSearch, params.Query)

	filterStr := buildOAFilterString(params.Filters)
	if filterStr != "" {
		qParams.Set(oaParamFilter, filterStr)
	}

	sortStr := mapOASortOrder(params.Sort)
	if sortStr != "" {
		qParams.Set(oaParamSort, sortStr)
	}

	page, perPage := mapOAPagination(params.Offset, params.Limit)
	qParams.Set(oaParamPage, strconv.Itoa(page))
	qParams.Set(oaParamPerPage, strconv.Itoa(perPage))

	if mailto != "" {
		qParams.Set(oaParamMailto, mailto)
	}
	if apiKey != "" {
		qParams.Set(oaParamAPIKey, apiKey)
	}

	return baseURL + oaWorksSearchPath + "?" + qParams.Encode()
}

// buildOAGetURL assembles the URL for fetching a single work by ID.
func buildOAGetURL(baseURL, workID, mailto, apiKey string) string {
	qParams := url.Values{}
	if mailto != "" {
		qParams.Set(oaParamMailto, mailto)
	}
	if apiKey != "" {
		qParams.Set(oaParamAPIKey, apiKey)
	}

	encoded := qParams.Encode()
	if encoded != "" {
		return baseURL + oaWorksGetPrefix + url.PathEscape(workID) + "?" + encoded
	}
	return baseURL + oaWorksGetPrefix + url.PathEscape(workID)
}

// buildOAFilterString constructs the comma-separated filter string from SearchFilters.
func buildOAFilterString(filters SearchFilters) string {
	parts := make([]string, 0, oaFilterMaxParts)

	if filters.Title != "" {
		parts = append(parts, oaFilterTitleSearch+filters.Title)
	}

	if filters.DateFrom != "" {
		if len(filters.DateFrom) == oaYearOnlyLength {
			parts = append(parts, oaFilterPubYear+filters.DateFrom)
		} else {
			parts = append(parts, oaFilterFromDate+filters.DateFrom)
		}
	}

	if filters.DateTo != "" {
		if len(filters.DateTo) == oaYearOnlyLength {
			// For year-only end dates, combine with from date if also year-only.
			// If from date already set a publication_year filter, we need a range.
			// OpenAlex supports publication_year:YYYY or from/to_publication_date.
			parts = append(parts, oaFilterToDate+filters.DateTo+oaYearEndPad)
		} else {
			parts = append(parts, oaFilterToDate+filters.DateTo)
		}
	}

	if filters.MinCitations != nil {
		parts = append(parts, oaFilterCitedBy+strconv.Itoa(*filters.MinCitations))
	}

	if filters.OpenAccess != nil && *filters.OpenAccess {
		parts = append(parts, oaFilterOpenAccess+oaFilterTrue)
	}

	return strings.Join(parts, oaFilterSeparator)
}

// mapOASortOrder converts a SortOrder to an OpenAlex sort parameter value.
func mapOASortOrder(sort SortOrder) string {
	switch sort {
	case SortRelevance:
		return oaSortRelevanceDesc
	case SortDateDesc:
		return oaSortPubDateDesc
	case SortCitations:
		return oaSortCitedByCountDesc
	default:
		return ""
	}
}

// mapOAPagination converts offset/limit to page-based pagination.
func mapOAPagination(offset, limit int) (page, perPage int) {
	perPage = limit
	if perPage <= 0 {
		perPage = oaDefaultPerPage
	}
	if perPage > oaMaxResultsPerPage {
		perPage = oaMaxResultsPerPage
	}

	page = oaFirstPage
	if offset > 0 && perPage > 0 {
		page = (offset / perPage) + oaFirstPage
	}

	return page, perPage
}

// ---------------------------------------------------------------------------
// Inverted abstract reconstruction
// ---------------------------------------------------------------------------

// reconstructAbstract converts an OpenAlex inverted abstract index to plaintext.
//
// The inverted index maps words to their position indices in the abstract.
// Example: {"machine": [0, 4], "learning": [1], "is": [2], "great": [3]}
// produces: "machine learning is great machine"
//
// Returns empty string for nil or empty maps.
// Positions exceeding oaMaxAbstractPositions are silently dropped.
func reconstructAbstract(invertedIndex map[string][]int) string {
	if len(invertedIndex) == 0 {
		return ""
	}

	// Find max position, capped at safety limit.
	maxPos := 0
	for _, positions := range invertedIndex {
		for _, pos := range positions {
			if pos > maxPos {
				maxPos = pos
			}
		}
	}

	if maxPos > oaMaxAbstractPositions {
		maxPos = oaMaxAbstractPositions
	}

	// Build position → word slice.
	words := make([]string, maxPos+1)
	for word, positions := range invertedIndex {
		for _, pos := range positions {
			if pos >= 0 && pos <= maxPos {
				words[pos] = word
			}
		}
	}

	// Join non-empty slots.
	var b strings.Builder
	b.Grow(oaAbstractInitialCapHint)
	first := true
	for _, w := range words {
		if w == "" {
			continue
		}
		if !first {
			b.WriteString(oaAbstractWordSeparator)
		}
		b.WriteString(w)
		first = false
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// Response mapping
// ---------------------------------------------------------------------------

// mapOAWorkToPublication converts an OpenAlex work to the unified Publication type.
func mapOAWorkToPublication(work *oaWork) Publication {
	pub := Publication{
		ID:          SourceOpenAlex + prefixedIDSeparator + extractOAWorkID(work.ID),
		Source:      SourceOpenAlex,
		ContentType: ContentTypePaper,
		Title:       work.Title,
		Abstract:    reconstructAbstract(work.AbstractInvertedIndex),
		URL:         work.ID,
		Authors:     mapOAAuthors(work.Authorships),
		Published:   mapOADate(work.PublicationDate, work.PublicationYear),
		DOI:         normalizeOADOI(work.DOI),
		License:     work.License,
	}

	citationCount := work.CitedByCount
	pub.CitationCount = &citationCount

	// PDF URL: prefer primary location, then open access URL.
	if work.PrimaryLocation != nil && work.PrimaryLocation.PDFURL != "" {
		pub.PDFURL = work.PrimaryLocation.PDFURL
	} else if work.OpenAccess != nil && work.OpenAccess.OAURL != "" {
		pub.PDFURL = work.OpenAccess.OAURL
	}

	// Categories from top-level concepts (level 0).
	pub.Categories = mapOAConcepts(work.Concepts)

	// Source metadata.
	metadata := make(map[string]any)

	if work.Type != "" {
		metadata[oaMetaKeyType] = work.Type
	}

	if len(work.Concepts) > 0 {
		conceptNames := make([]string, 0, len(work.Concepts))
		for _, c := range work.Concepts {
			conceptNames = append(conceptNames, c.DisplayName)
		}
		metadata[oaMetaKeyConcepts] = conceptNames
	}

	if len(work.Topics) > 0 {
		topicNames := make([]string, 0, len(work.Topics))
		for _, t := range work.Topics {
			topicNames = append(topicNames, t.DisplayName)
		}
		metadata[oaMetaKeyTopics] = topicNames
	}

	if work.PrimaryTopic != nil {
		metadata[oaMetaKeyPrimaryTopic] = work.PrimaryTopic.DisplayName
	}

	if work.OpenAccess != nil {
		metadata[oaMetaKeyIsOA] = work.OpenAccess.IsOA
		if work.OpenAccess.OAStatus != "" {
			metadata[oaMetaKeyOAStatus] = work.OpenAccess.OAStatus
		}
	}

	if work.PrimaryLocation != nil && work.PrimaryLocation.Source != nil {
		metadata[oaMetaKeyVenue] = work.PrimaryLocation.Source.DisplayName
	}

	metadata[oaMetaKeyOpenAlexID] = extractOAWorkID(work.ID)

	if len(metadata) > 0 {
		pub.SourceMetadata = metadata
	}

	return pub
}

// mapOAAuthors converts OpenAlex authorships to the unified Author type.
func mapOAAuthors(authorships []oaAuthorship) []Author {
	result := make([]Author, len(authorships))
	for i, a := range authorships {
		author := Author{Name: a.Author.DisplayName}

		// Extract ORCID: strip the URL prefix if present.
		if a.Author.ORCID != "" {
			author.ORCID = normalizeOAORCID(a.Author.ORCID)
		}

		// Use first institution as affiliation.
		if len(a.Institutions) > 0 {
			author.Affiliation = a.Institutions[0].DisplayName
		}

		result[i] = author
	}
	return result
}

// oaORCIDURLPrefix is the URL prefix for ORCID identifiers.
const oaORCIDURLPrefix = "https://orcid.org/"

// normalizeOAORCID strips the URL prefix from an ORCID identifier.
func normalizeOAORCID(orcid string) string {
	return strings.TrimPrefix(orcid, oaORCIDURLPrefix)
}

// mapOADate returns the best available date string.
// Prefers publication_date (YYYY-MM-DD) over year-only.
func mapOADate(publicationDate string, year int) string {
	if publicationDate != "" {
		return publicationDate
	}
	if year > 0 {
		return strconv.Itoa(year)
	}
	return ""
}

// mapOAConcepts extracts concept display names for categories.
// Only includes top-level concepts (level 0) for cleaner categorization.
func mapOAConcepts(concepts []oaConcept) []string {
	if len(concepts) == 0 {
		return nil
	}

	result := make([]string, 0, len(concepts))
	for _, c := range concepts {
		if c.Level == 0 {
			result = append(result, c.DisplayName)
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// extractOAWorkID strips the OpenAlex URL prefix to get the bare work ID.
func extractOAWorkID(fullURL string) string {
	return strings.TrimPrefix(fullURL, oaIDURLPrefix)
}

// normalizeOADOI strips the DOI URL prefix to get the bare DOI identifier.
func normalizeOADOI(rawDOI string) string {
	return strings.TrimPrefix(rawDOI, oaDOIURLPrefix)
}

// ---------------------------------------------------------------------------
// Format conversion
// ---------------------------------------------------------------------------

// convertOAFormat applies format conversion on a Publication.
func convertOAFormat(pub *Publication, format ContentFormat) error {
	switch format {
	case FormatJSON:
		return nil
	case FormatBibTeX:
		bibtex := assembleOABibTeX(pub)
		pub.FullText = &FullTextContent{
			Content:       bibtex,
			ContentFormat: FormatBibTeX,
			ContentLength: len(bibtex),
			Truncated:     false,
		}
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrFormatUnsupported, format)
	}
}

// assembleOABibTeX creates a BibTeX entry from Publication metadata.
func assembleOABibTeX(pub *Publication) string {
	authorNames := make([]string, len(pub.Authors))
	for i, a := range pub.Authors {
		authorNames[i] = a.Name
	}

	year := ""
	if len(pub.Published) >= oaYearOnlyLength {
		year = pub.Published[:oaYearOnlyLength]
	}

	citeKey := oaBibTeXKeyPrefix + pub.ID

	return fmt.Sprintf(oaBibTeXTemplate,
		citeKey,
		pub.Title,
		strings.Join(authorNames, oaBibTeXAuthorSeparator),
		year,
		pub.DOI,
		pub.URL,
	)
}
