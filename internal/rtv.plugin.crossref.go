package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// CrossRef plugin identity constants
// ---------------------------------------------------------------------------

const (
	crossrefPluginID          = "crossref"
	crossrefPluginName        = "CrossRef"
	crossrefPluginDescription = "Comprehensive DOI registration agency with metadata for 150M+ scholarly works across all disciplines"
)

// ---------------------------------------------------------------------------
// CrossRef API constants
// ---------------------------------------------------------------------------

const (
	crossrefDefaultBaseURL    = "https://api.crossref.org/v1"
	crossrefWorksSearchPath   = "/works"
	crossrefWorksGetPrefix    = "/works/"
	crossrefMaxResultsPerPage = 100
	crossrefMaxResponseBytes  = 10 << 20 // 10 MB upper bound
)

// ---------------------------------------------------------------------------
// CrossRef query parameter constants
// ---------------------------------------------------------------------------

const (
	crossrefParamQuery  = "query"
	crossrefParamRows   = "rows"
	crossrefParamOffset = "offset"
	crossrefParamMailto = "mailto"
	crossrefParamSort   = "sort"
	crossrefParamOrder  = "order"
	crossrefParamFilter = "filter"
)

// ---------------------------------------------------------------------------
// CrossRef sort constants
// ---------------------------------------------------------------------------

const (
	crossrefSortRelevance = "relevance"
	crossrefSortPublished = "published"
	crossrefSortCitations = "is-referenced-by-count"
	crossrefOrderDesc     = "desc"
	crossrefOrderAsc      = "asc"
)

// ---------------------------------------------------------------------------
// CrossRef filter constants
// ---------------------------------------------------------------------------

const (
	crossrefFilterFromDate  = "from-pub-date:"
	crossrefFilterToDate    = "until-pub-date:"
	crossrefFilterSeparator = ","
)

// ---------------------------------------------------------------------------
// CrossRef HTTP constants
// ---------------------------------------------------------------------------

const crossrefHTTPStatusErrFmt = "status %d"

// ---------------------------------------------------------------------------
// CrossRef metadata key constants
// ---------------------------------------------------------------------------

const (
	crossrefMetaKeyJournal = "crossref_journal"
	crossrefMetaKeyType    = "crossref_type"
	crossrefMetaKeyISSN    = "crossref_issn"
	crossrefMetaKeyVolume  = "crossref_volume"
	crossrefMetaKeyIssue   = "crossref_issue"
	crossrefMetaKeyPage    = "crossref_page"
)

// ---------------------------------------------------------------------------
// CrossRef config extra key constants
// ---------------------------------------------------------------------------

const crossrefExtraKeyMailto = "mailto"

// ---------------------------------------------------------------------------
// CrossRef ORCID constants
// ---------------------------------------------------------------------------

const crossrefOrcidURLPrefix = "https://orcid.org/"

// ---------------------------------------------------------------------------
// CrossRef categories hint
// ---------------------------------------------------------------------------

const crossrefCategoriesHint = "Journal Articles, Conference Papers, Books, Preprints, Reports, Dissertations"

// ---------------------------------------------------------------------------
// CrossRef XML tag stripping (for JATS abstracts)
// ---------------------------------------------------------------------------

var crossrefXMLTagRegex = regexp.MustCompile("<[^>]*>")

// stripXMLTags removes XML/JATS tags, collapses whitespace, and trims.
func stripXMLTags(s string) string {
	stripped := crossrefXMLTagRegex.ReplaceAllString(s, " ")
	// Collapse multiple whitespace characters into a single space.
	fields := strings.Fields(stripped)
	return strings.Join(fields, " ")
}

// ---------------------------------------------------------------------------
// CrossRef date-parts types and helpers
// ---------------------------------------------------------------------------

// crossrefDateParts represents the nested date-parts format: [[2024, 1, 15]].
type crossrefDateParts struct {
	DateParts [][]int `json:"date-parts"`
}

// mapCrossRefDateParts converts CrossRef date-parts to a "YYYY-MM-DD" string.
// Handles missing month/day gracefully.
func mapCrossRefDateParts(dp crossrefDateParts) string {
	if len(dp.DateParts) == 0 || len(dp.DateParts[0]) == 0 {
		return ""
	}

	parts := dp.DateParts[0]
	year := parts[0]
	if year == 0 {
		return ""
	}

	if len(parts) < 2 || parts[1] == 0 {
		return strconv.Itoa(year)
	}

	if len(parts) < 3 || parts[2] == 0 {
		return fmt.Sprintf("%d-%02d", year, parts[1])
	}

	return fmt.Sprintf("%d-%02d-%02d", year, parts[1], parts[2])
}

// ---------------------------------------------------------------------------
// CrossRef JSON response struct definitions
// ---------------------------------------------------------------------------

// crossrefEnvelope represents the top-level API response envelope.
// Message is json.RawMessage because the /works endpoint returns items
// in a search wrapper, while /works/{DOI} returns the work directly.
type crossrefEnvelope struct {
	Status  string          `json:"status"`
	Message json.RawMessage `json:"message"`
}

// crossrefSearchMessage represents the message body for search responses.
type crossrefSearchMessage struct {
	TotalResults int            `json:"total-results"`
	Items        []crossrefWork `json:"items"`
}

// crossrefWork represents a single work in the CrossRef API response.
type crossrefWork struct {
	DOI                 string            `json:"DOI"`
	Title               []string          `json:"title"`
	Author              []crossrefAuthor  `json:"author"`
	Abstract            string            `json:"abstract"`
	IsReferencedByCount int               `json:"is-referenced-by-count"`
	URL                 string            `json:"URL"`
	Type                string            `json:"type"`
	ContainerTitle      []string          `json:"container-title"`
	PublishedPrint      crossrefDateParts `json:"published-print"`
	PublishedOnline     crossrefDateParts `json:"published-online"`
	ISSN                []string          `json:"ISSN"`
	Volume              string            `json:"volume"`
	Issue               string            `json:"issue"`
	Page                string            `json:"page"`
}

// crossrefAuthor represents an author in the CrossRef response.
type crossrefAuthor struct {
	Given       string                `json:"given"`
	Family      string                `json:"family"`
	ORCID       string                `json:"ORCID"`
	Affiliation []crossrefAffiliation `json:"affiliation"`
}

// crossrefAffiliation represents an author affiliation.
type crossrefAffiliation struct {
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// CrossRefPlugin struct
// ---------------------------------------------------------------------------

// CrossRefPlugin implements SourcePlugin for CrossRef.
// Thread-safe for concurrent use after Initialize.
type CrossRefPlugin struct {
	baseURL    string
	mailto     string
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
func (p *CrossRefPlugin) ID() string { return crossrefPluginID }

// Name returns a human-readable name.
func (p *CrossRefPlugin) Name() string { return crossrefPluginName }

// Description returns a short description for LLM context.
func (p *CrossRefPlugin) Description() string { return crossrefPluginDescription }

// ContentTypes returns the types of content this source provides.
func (p *CrossRefPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat returns the default content format.
func (p *CrossRefPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats returns all formats this source can provide.
func (p *CrossRefPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Capabilities
// ---------------------------------------------------------------------------

// Capabilities reports what filtering, sorting, and features CrossRef supports.
func (p *CrossRefPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    true,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       crossrefMaxResultsPerPage,
		CategoriesHint:           crossrefCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Initialize
// ---------------------------------------------------------------------------

// Initialize sets up the CrossRef plugin with the given configuration.
// Called once at startup.
func (p *CrossRefPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = crossrefDefaultBaseURL
	}

	if cfg.Extra != nil {
		p.mailto = cfg.Extra[crossrefExtraKeyMailto]
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
func (p *CrossRefPlugin) Health(_ context.Context) SourceHealth {
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

// Search executes a search query against the CrossRef works API.
func (p *CrossRefPlugin) Search(ctx context.Context, params SearchParams, _ *CallCredentials) (*SearchResult, error) {
	if params.Query == "" {
		return nil, ErrCrossRefEmptyQuery
	}

	reqURL := buildCrossRefSearchURL(p.baseURL, params, p.mailto)

	var envelope crossrefEnvelope
	if err := p.doRequest(ctx, reqURL, &envelope); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}

	var msg crossrefSearchMessage
	if err := json.Unmarshal(envelope.Message, &msg); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, fmt.Errorf("%w: %w", ErrCrossRefJSONParse, err))
	}

	p.recordSuccess()

	pubs := make([]Publication, 0, len(msg.Items))
	for i := range msg.Items {
		pubs = append(pubs, mapCrossRefWorkToPublication(&msg.Items[i]))
	}

	hasMore := (params.Offset + len(pubs)) < msg.TotalResults

	return &SearchResult{
		Total:   msg.TotalResults,
		Results: pubs,
		HasMore: hasMore,
	}, nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Get
// ---------------------------------------------------------------------------

// Get retrieves a single work by its DOI.
// The id parameter is the raw DOI (prefix already stripped by the router).
func (p *CrossRefPlugin) Get(ctx context.Context, id string, _ []IncludeField, format ContentFormat, _ *CallCredentials) (*Publication, error) {
	reqURL := buildCrossRefGetURL(p.baseURL, id, p.mailto)

	var envelope crossrefEnvelope
	if err := p.doRequest(ctx, reqURL, &envelope); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	// For single-work endpoints, message IS the work object directly.
	var work crossrefWork
	if err := json.Unmarshal(envelope.Message, &work); err != nil {
		unmarshalErr := fmt.Errorf("%w: %w", ErrCrossRefJSONParse, err)
		p.recordError(unmarshalErr)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, unmarshalErr)
	}

	if work.DOI == "" {
		return nil, fmt.Errorf("%w: %s", ErrCrossRefNotFound, id)
	}

	p.recordSuccess()

	pub := mapCrossRefWorkToPublication(&work)

	// Apply format conversion if not native JSON.
	if format != FormatNative && format != FormatJSON {
		if err := convertCrossRefFormat(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

// doRequest executes an HTTP GET and decodes the JSON response into the target.
func (p *CrossRefPlugin) doRequest(ctx context.Context, reqURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCrossRefHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return fmt.Errorf("%w: %w", ErrCrossRefHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrCrossRefNotFound
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%w: "+crossrefHTTPStatusErrFmt, ErrCrossRefHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(crossrefMaxResponseBytes))
	if err := json.NewDecoder(limitedBody).Decode(target); err != nil {
		return fmt.Errorf("%w: %w", ErrCrossRefJSONParse, err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Health state helpers
// ---------------------------------------------------------------------------

func (p *CrossRefPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *CrossRefPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	p.lastError = err.Error()
}

// ---------------------------------------------------------------------------
// URL / query building
// ---------------------------------------------------------------------------

// buildCrossRefSearchURL assembles the full search URL with query parameters.
func buildCrossRefSearchURL(baseURL string, params SearchParams, mailto string) string {
	qParams := url.Values{}
	qParams.Set(crossrefParamQuery, params.Query)

	rows := params.Limit
	if rows <= 0 {
		rows = crossrefMaxResultsPerPage
	}
	if rows > crossrefMaxResultsPerPage {
		rows = crossrefMaxResultsPerPage
	}
	qParams.Set(crossrefParamRows, strconv.Itoa(rows))

	if params.Offset > 0 {
		qParams.Set(crossrefParamOffset, strconv.Itoa(params.Offset))
	}

	if mailto != "" {
		qParams.Set(crossrefParamMailto, mailto)
	}

	sortVal, orderVal := mapCrossRefSortOrder(params.Sort)
	if sortVal != "" {
		qParams.Set(crossrefParamSort, sortVal)
		qParams.Set(crossrefParamOrder, orderVal)
	}

	filterStr := buildCrossRefFilterString(params.Filters)
	if filterStr != "" {
		qParams.Set(crossrefParamFilter, filterStr)
	}

	return baseURL + crossrefWorksSearchPath + "?" + qParams.Encode()
}

// buildCrossRefGetURL assembles the URL for fetching a single work by DOI.
func buildCrossRefGetURL(baseURL, doi, mailto string) string {
	qParams := url.Values{}
	if mailto != "" {
		qParams.Set(crossrefParamMailto, mailto)
	}

	encoded := qParams.Encode()
	if encoded != "" {
		return baseURL + crossrefWorksGetPrefix + url.PathEscape(doi) + "?" + encoded
	}
	return baseURL + crossrefWorksGetPrefix + url.PathEscape(doi)
}

// buildCrossRefFilterString constructs the comma-separated filter string from SearchFilters.
func buildCrossRefFilterString(filters SearchFilters) string {
	var parts []string

	if filters.DateFrom != "" {
		parts = append(parts, crossrefFilterFromDate+filters.DateFrom)
	}

	if filters.DateTo != "" {
		parts = append(parts, crossrefFilterToDate+filters.DateTo)
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, crossrefFilterSeparator)
}

// mapCrossRefSortOrder converts a SortOrder to CrossRef sort and order parameter values.
func mapCrossRefSortOrder(sort SortOrder) (string, string) {
	switch sort {
	case SortRelevance:
		return crossrefSortRelevance, crossrefOrderDesc
	case SortDateDesc:
		return crossrefSortPublished, crossrefOrderDesc
	case SortDateAsc:
		return crossrefSortPublished, crossrefOrderAsc
	case SortCitations:
		return crossrefSortCitations, crossrefOrderDesc
	default:
		return "", ""
	}
}

// ---------------------------------------------------------------------------
// Response mapping
// ---------------------------------------------------------------------------

// mapCrossRefWorkToPublication converts a CrossRef work to the unified Publication type.
func mapCrossRefWorkToPublication(work *crossrefWork) Publication {
	pub := Publication{
		ID:          SourceCrossRef + prefixedIDSeparator + work.DOI,
		Source:      SourceCrossRef,
		ContentType: ContentTypePaper,
		DOI:         work.DOI,
		URL:         work.URL,
	}

	// Title: take first element of array.
	if len(work.Title) > 0 {
		pub.Title = work.Title[0]
	}

	// Abstract: strip JATS XML tags.
	if work.Abstract != "" {
		pub.Abstract = stripXMLTags(work.Abstract)
	}

	// Authors.
	pub.Authors = mapCrossRefAuthors(work.Author)

	// Date: prefer published-print, fall back to published-online.
	pub.Published = mapCrossRefDateParts(work.PublishedPrint)
	if pub.Published == "" {
		pub.Published = mapCrossRefDateParts(work.PublishedOnline)
	}

	// Citation count.
	citationCount := work.IsReferencedByCount
	pub.CitationCount = &citationCount

	// Source metadata.
	metadata := make(map[string]any)

	if len(work.ContainerTitle) > 0 && work.ContainerTitle[0] != "" {
		metadata[crossrefMetaKeyJournal] = work.ContainerTitle[0]
	}

	if work.Type != "" {
		metadata[crossrefMetaKeyType] = work.Type
	}

	if len(work.ISSN) > 0 && work.ISSN[0] != "" {
		metadata[crossrefMetaKeyISSN] = work.ISSN[0]
	}

	if work.Volume != "" {
		metadata[crossrefMetaKeyVolume] = work.Volume
	}

	if work.Issue != "" {
		metadata[crossrefMetaKeyIssue] = work.Issue
	}

	if work.Page != "" {
		metadata[crossrefMetaKeyPage] = work.Page
	}

	if len(metadata) > 0 {
		pub.SourceMetadata = metadata
	}

	return pub
}

// mapCrossRefAuthors converts CrossRef authors to the unified Author type.
func mapCrossRefAuthors(authors []crossrefAuthor) []Author {
	result := make([]Author, len(authors))
	for i, a := range authors {
		name := strings.TrimSpace(a.Given + " " + a.Family)
		author := Author{Name: name}

		// Strip ORCID URL prefix if present.
		if a.ORCID != "" {
			author.ORCID = strings.TrimPrefix(a.ORCID, crossrefOrcidURLPrefix)
		}

		// Use first affiliation.
		if len(a.Affiliation) > 0 && a.Affiliation[0].Name != "" {
			author.Affiliation = a.Affiliation[0].Name
		}

		result[i] = author
	}
	return result
}

// ---------------------------------------------------------------------------
// Format conversion
// ---------------------------------------------------------------------------

// convertCrossRefFormat applies format conversion on a Publication.
func convertCrossRefFormat(_ *Publication, format ContentFormat) error {
	switch format {
	case FormatJSON:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrFormatUnsupported, format)
	}
}
