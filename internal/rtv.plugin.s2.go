package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
)

// ---------------------------------------------------------------------------
// S2 plugin identity constants
// ---------------------------------------------------------------------------

const (
	s2PluginID          = "s2"
	s2PluginName        = "Semantic Scholar"
	s2PluginDescription = "AI-powered research tool for scientific literature with citation data and open-access links"
)

// ---------------------------------------------------------------------------
// S2 API constants
// ---------------------------------------------------------------------------

const (
	s2DefaultBaseURL    = "https://api.semanticscholar.org/graph/v1"
	s2PaperSearchPath   = "/paper/search"
	s2PaperGetPath      = "/paper/"
	s2CitationsPath     = "/citations"
	s2ReferencesPath    = "/references"
	s2MaxResultsPerPage = 100
	s2MaxResponseBytes  = 10 << 20 // 10 MB upper bound
)

// ---------------------------------------------------------------------------
// S2 field selection constants
// ---------------------------------------------------------------------------

const (
	s2SearchFields    = "paperId,externalIds,title,abstract,year,authors,citationCount,referenceCount,publicationDate,journal,openAccessPdf,fieldsOfStudy,url,isOpenAccess,publicationTypes"
	s2GetFields       = "paperId,externalIds,title,abstract,year,authors,citationCount,referenceCount,publicationDate,journal,openAccessPdf,fieldsOfStudy,url,isOpenAccess,publicationTypes"
	s2CitationFields  = "paperId,externalIds,title,year,authors,citationCount"
	s2ReferenceFields = "paperId,externalIds,title,year,authors,citationCount"
)

// ---------------------------------------------------------------------------
// S2 API parameter name constants
// ---------------------------------------------------------------------------

const (
	s2ParamQuery                 = "query"
	s2ParamOffset                = "offset"
	s2ParamLimit                 = "limit"
	s2ParamFields                = "fields"
	s2ParamPublicationDateOrYear = "publicationDateOrYear"
)

// ---------------------------------------------------------------------------
// S2 HTTP constants
// ---------------------------------------------------------------------------

const (
	s2APIKeyHeader     = "x-api-key"
	s2HTTPStatusErrFmt = "status %d"
)

// ---------------------------------------------------------------------------
// S2 date format constants
// ---------------------------------------------------------------------------

const (
	s2DateSeparator  = ":"
	s2YearOnlyLength = 4
	s2YearStartPad   = "-01-01"
	s2YearEndPad     = "-12-31"
)

// ---------------------------------------------------------------------------
// S2 metadata key constants
// ---------------------------------------------------------------------------

const (
	s2MetaKeyJournal          = "s2_journal"
	s2MetaKeyFieldsOfStudy    = "s2_fields_of_study"
	s2MetaKeyPublicationTypes = "s2_publication_types"
	s2MetaKeyCorpusID         = "s2_corpus_id"
	s2MetaKeyReferenceCount   = "s2_reference_count"
	s2MetaKeyIsOpenAccess     = "s2_is_open_access"
)

// ---------------------------------------------------------------------------
// S2 categories hint
// ---------------------------------------------------------------------------

const s2CategoriesHint = "Computer Science, Medicine, Biology, Physics, Mathematics, Psychology, Chemistry, Engineering, Environmental Science, Economics"

// ---------------------------------------------------------------------------
// S2 JSON response struct definitions
// ---------------------------------------------------------------------------

// s2SearchResponse represents the top-level search response from the S2 API.
type s2SearchResponse struct {
	Total  int       `json:"total"`
	Offset int       `json:"offset"`
	Next   *int      `json:"next"` // nil when no more pages
	Data   []s2Paper `json:"data"`
}

// s2Paper represents a single paper in the S2 API response.
type s2Paper struct {
	PaperID          string           `json:"paperId"`
	ExternalIDs      *s2ExternalIDs   `json:"externalIds"`
	Title            string           `json:"title"`
	Abstract         string           `json:"abstract"`
	Year             int              `json:"year"`
	Authors          []s2Author       `json:"authors"`
	CitationCount    int              `json:"citationCount"`
	ReferenceCount   int              `json:"referenceCount"`
	PublicationDate  string           `json:"publicationDate"`
	Journal          *s2Journal       `json:"journal"`
	OpenAccessPdf    *s2OpenAccessPdf `json:"openAccessPdf"`
	FieldsOfStudy    []string         `json:"fieldsOfStudy"`
	URL              string           `json:"url"`
	IsOpenAccess     bool             `json:"isOpenAccess"`
	PublicationTypes []string         `json:"publicationTypes"`
}

// s2Author represents an author in the S2 API response.
type s2Author struct {
	AuthorID string `json:"authorId"`
	Name     string `json:"name"`
}

// s2ExternalIDs represents external identifier mappings.
type s2ExternalIDs struct {
	DOI      string `json:"DOI"`
	ArXiv    string `json:"ArXiv"`
	PMID     string `json:"PMID"`
	CorpusID int    `json:"CorpusId"`
}

// s2OpenAccessPdf represents an open-access PDF link.
type s2OpenAccessPdf struct {
	URL string `json:"url"`
}

// s2Journal represents journal information.
type s2Journal struct {
	Name   string `json:"name"`
	Volume string `json:"volume"`
	Pages  string `json:"pages"`
}

// s2CitationResponse wraps citations/references sub-resource responses.
type s2CitationResponse struct {
	Offset int               `json:"offset"`
	Next   *int              `json:"next"`
	Data   []s2CitationEntry `json:"data"`
}

// s2CitationEntry wraps a citing or cited paper.
type s2CitationEntry struct {
	CitingPaper *s2Paper `json:"citingPaper,omitempty"`
	CitedPaper  *s2Paper `json:"citedPaper,omitempty"`
}

// ---------------------------------------------------------------------------
// S2Plugin struct
// ---------------------------------------------------------------------------

// S2Plugin implements SourcePlugin for Semantic Scholar.
// Thread-safe for concurrent use after Initialize.
type S2Plugin struct {
	baseURL    string
	apiKey     string // server-level default from config
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
func (p *S2Plugin) ID() string { return s2PluginID }

// Name returns a human-readable name.
func (p *S2Plugin) Name() string { return s2PluginName }

// Description returns a short description for LLM context.
func (p *S2Plugin) Description() string { return s2PluginDescription }

// ContentTypes returns the types of content this source provides.
func (p *S2Plugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat returns the default content format.
func (p *S2Plugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats returns all formats this source can provide.
func (p *S2Plugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Capabilities
// ---------------------------------------------------------------------------

// Capabilities reports what filtering, sorting, and features S2 supports.
func (p *S2Plugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        true,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       s2MaxResultsPerPage,
		CategoriesHint:           s2CategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Initialize
// ---------------------------------------------------------------------------

// Initialize sets up the S2 plugin with the given configuration.
// Called once at startup.
func (p *S2Plugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = s2DefaultBaseURL
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
func (p *S2Plugin) Health(_ context.Context) SourceHealth {
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

// Search executes a search query against the Semantic Scholar API.
func (p *S2Plugin) Search(ctx context.Context, params SearchParams, creds *CallCredentials) (*SearchResult, error) {
	if params.Query == "" {
		return nil, ErrS2EmptyQuery
	}

	reqURL := buildS2SearchURL(p.baseURL, params)
	apiKey := resolveS2APIKey(creds, p.apiKey)

	var response s2SearchResponse
	if err := p.doRequest(ctx, reqURL, apiKey, &response); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}

	p.recordSuccess()

	pubs := make([]Publication, 0, len(response.Data))
	for i := range response.Data {
		pubs = append(pubs, mapS2PaperToPublication(&response.Data[i]))
	}

	hasMore := response.Next != nil

	return &SearchResult{
		Total:   response.Total,
		Results: pubs,
		HasMore: hasMore,
	}, nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Get
// ---------------------------------------------------------------------------

// Get retrieves a single publication by its Semantic Scholar paper ID.
func (p *S2Plugin) Get(ctx context.Context, id string, include []IncludeField, format ContentFormat, creds *CallCredentials) (*Publication, error) {
	apiKey := resolveS2APIKey(creds, p.apiKey)
	reqURL := buildS2GetURL(p.baseURL, id)

	var paper s2Paper
	if err := p.doRequest(ctx, reqURL, apiKey, &paper); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	if paper.PaperID == "" {
		return nil, fmt.Errorf("%w: %s", ErrS2NotFound, id)
	}

	p.recordSuccess()

	pub := mapS2PaperToPublication(&paper)

	// Fetch citations if requested (non-fatal on failure).
	if slices.Contains(include, IncludeCitations) {
		citations, err := p.fetchCitations(ctx, id, apiKey)
		if err == nil {
			pub.Citations = citations
		}
	}

	// Fetch references if requested (non-fatal on failure).
	if slices.Contains(include, IncludeReferences) {
		refs, err := p.fetchReferences(ctx, id, apiKey)
		if err == nil {
			pub.References = refs
		}
	}

	// Apply format conversion if not native JSON.
	if format != FormatNative && format != FormatJSON {
		if err := convertS2Format(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// ---------------------------------------------------------------------------
// Citations and references fetching
// ---------------------------------------------------------------------------

// fetchCitations retrieves papers that cite the given paper.
func (p *S2Plugin) fetchCitations(ctx context.Context, paperID, apiKey string) ([]Reference, error) {
	reqURL := buildS2CitationsURL(p.baseURL, paperID)

	var resp s2CitationResponse
	if err := p.doRequest(ctx, reqURL, apiKey, &resp); err != nil {
		return nil, err
	}

	refs := make([]Reference, 0, len(resp.Data))
	for _, entry := range resp.Data {
		if entry.CitingPaper != nil {
			refs = append(refs, mapS2PaperToReference(entry.CitingPaper))
		}
	}

	return refs, nil
}

// fetchReferences retrieves papers referenced by the given paper.
func (p *S2Plugin) fetchReferences(ctx context.Context, paperID, apiKey string) ([]Reference, error) {
	reqURL := buildS2ReferencesURL(p.baseURL, paperID)

	var resp s2CitationResponse
	if err := p.doRequest(ctx, reqURL, apiKey, &resp); err != nil {
		return nil, err
	}

	refs := make([]Reference, 0, len(resp.Data))
	for _, entry := range resp.Data {
		if entry.CitedPaper != nil {
			refs = append(refs, mapS2PaperToReference(entry.CitedPaper))
		}
	}

	return refs, nil
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

// doRequest executes an HTTP GET with optional API key and decodes the JSON
// response into the target struct.
func (p *S2Plugin) doRequest(ctx context.Context, reqURL, apiKey string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrS2HTTPRequest, err)
	}

	if apiKey != "" {
		req.Header.Set(s2APIKeyHeader, apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return fmt.Errorf("%w: %w", ErrS2HTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrS2NotFound
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%w: "+s2HTTPStatusErrFmt, ErrS2HTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(s2MaxResponseBytes))
	if err := json.NewDecoder(limitedBody).Decode(target); err != nil {
		return fmt.Errorf("%w: %w", ErrS2JSONParse, err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Health state helpers
// ---------------------------------------------------------------------------

func (p *S2Plugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *S2Plugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	p.lastError = err.Error()
}

// ---------------------------------------------------------------------------
// Credential resolution helper
// ---------------------------------------------------------------------------

// resolveS2APIKey extracts the effective API key from per-call credentials
// and server default, following the three-level resolution chain.
func resolveS2APIKey(creds *CallCredentials, serverDefault string) string {
	if creds != nil {
		return creds.ResolveForSource(SourceS2, serverDefault)
	}
	return serverDefault
}

// ---------------------------------------------------------------------------
// URL / query building
// ---------------------------------------------------------------------------

// buildS2SearchURL assembles the full search URL with query parameters.
func buildS2SearchURL(baseURL string, params SearchParams) string {
	qParams := url.Values{}
	qParams.Set(s2ParamQuery, params.Query)
	qParams.Set(s2ParamOffset, strconv.Itoa(params.Offset))
	qParams.Set(s2ParamLimit, strconv.Itoa(params.Limit))
	qParams.Set(s2ParamFields, s2SearchFields)

	dateFilter := buildS2DateFilter(params.Filters.DateFrom, params.Filters.DateTo)
	if dateFilter != "" {
		qParams.Set(s2ParamPublicationDateOrYear, dateFilter)
	}

	return baseURL + s2PaperSearchPath + "?" + qParams.Encode()
}

// buildS2GetURL assembles the URL for fetching a single paper by ID.
func buildS2GetURL(baseURL, paperID string) string {
	qParams := url.Values{}
	qParams.Set(s2ParamFields, s2GetFields)
	return baseURL + s2PaperGetPath + url.PathEscape(paperID) + "?" + qParams.Encode()
}

// buildS2CitationsURL assembles the URL for fetching a paper's citations.
func buildS2CitationsURL(baseURL, paperID string) string {
	qParams := url.Values{}
	qParams.Set(s2ParamFields, s2CitationFields)
	return baseURL + s2PaperGetPath + url.PathEscape(paperID) + s2CitationsPath + "?" + qParams.Encode()
}

// buildS2ReferencesURL assembles the URL for fetching a paper's references.
func buildS2ReferencesURL(baseURL, paperID string) string {
	qParams := url.Values{}
	qParams.Set(s2ParamFields, s2ReferenceFields)
	return baseURL + s2PaperGetPath + url.PathEscape(paperID) + s2ReferencesPath + "?" + qParams.Encode()
}

// buildS2DateFilter constructs the publicationDateOrYear filter value.
// S2 format: "YYYY-MM-DD:YYYY-MM-DD", either side may be empty.
func buildS2DateFilter(dateFrom, dateTo string) string {
	if dateFrom == "" && dateTo == "" {
		return ""
	}

	from := ""
	to := ""

	if dateFrom != "" {
		from = normalizeS2Date(dateFrom, false)
	}
	if dateTo != "" {
		to = normalizeS2Date(dateTo, true)
	}

	return from + s2DateSeparator + to
}

// normalizeS2Date pads year-only dates to full YYYY-MM-DD format.
// Full dates are returned as-is.
func normalizeS2Date(date string, isEndDate bool) string {
	if len(date) == s2YearOnlyLength {
		if isEndDate {
			return date + s2YearEndPad
		}
		return date + s2YearStartPad
	}
	return date
}

// ---------------------------------------------------------------------------
// Response mapping
// ---------------------------------------------------------------------------

// mapS2PaperToPublication converts an S2 paper to the unified Publication type.
func mapS2PaperToPublication(paper *s2Paper) Publication {
	pub := Publication{
		ID:          SourceS2 + prefixedIDSeparator + paper.PaperID,
		Source:      SourceS2,
		ContentType: ContentTypePaper,
		Title:       paper.Title,
		Abstract:    paper.Abstract,
		URL:         paper.URL,
		Authors:     mapS2Authors(paper.Authors),
		Published:   mapS2Date(paper.PublicationDate, paper.Year),
	}

	citationCount := paper.CitationCount
	pub.CitationCount = &citationCount

	// External IDs (may be nil for some papers).
	if paper.ExternalIDs != nil {
		pub.DOI = paper.ExternalIDs.DOI
		pub.ArXivID = paper.ExternalIDs.ArXiv
	}

	// Open access PDF link.
	if paper.OpenAccessPdf != nil {
		pub.PDFURL = paper.OpenAccessPdf.URL
	}

	// Categories from fields of study.
	pub.Categories = paper.FieldsOfStudy

	// Source metadata.
	metadata := make(map[string]any)
	if paper.Journal != nil && paper.Journal.Name != "" {
		metadata[s2MetaKeyJournal] = paper.Journal.Name
	}
	if len(paper.FieldsOfStudy) > 0 {
		metadata[s2MetaKeyFieldsOfStudy] = paper.FieldsOfStudy
	}
	if len(paper.PublicationTypes) > 0 {
		metadata[s2MetaKeyPublicationTypes] = paper.PublicationTypes
	}
	if paper.ExternalIDs != nil && paper.ExternalIDs.CorpusID > 0 {
		metadata[s2MetaKeyCorpusID] = paper.ExternalIDs.CorpusID
	}
	if paper.ReferenceCount > 0 {
		metadata[s2MetaKeyReferenceCount] = paper.ReferenceCount
	}
	metadata[s2MetaKeyIsOpenAccess] = paper.IsOpenAccess
	if len(metadata) > 0 {
		pub.SourceMetadata = metadata
	}

	return pub
}

// mapS2Authors converts S2 authors to the unified Author type.
func mapS2Authors(authors []s2Author) []Author {
	result := make([]Author, len(authors))
	for i, a := range authors {
		result[i] = Author{Name: a.Name}
	}
	return result
}

// mapS2Date returns the best available date string.
// Prefers publicationDate (YYYY-MM-DD) over year-only.
func mapS2Date(publicationDate string, year int) string {
	if publicationDate != "" {
		return publicationDate
	}
	if year > 0 {
		return strconv.Itoa(year)
	}
	return ""
}

// mapS2PaperToReference converts an S2 paper to the Reference type (for citations/references).
func mapS2PaperToReference(paper *s2Paper) Reference {
	id := ""
	if paper.PaperID != "" {
		id = SourceS2 + prefixedIDSeparator + paper.PaperID
	}
	return Reference{
		ID:    id,
		Title: paper.Title,
		Year:  paper.Year,
	}
}

// ---------------------------------------------------------------------------
// Format conversion
// ---------------------------------------------------------------------------

// convertS2Format applies format conversion on a Publication.
func convertS2Format(_ *Publication, format ContentFormat) error {
	switch format {
	case FormatJSON:
		return nil // Publication is natively JSON-serializable
	default:
		return fmt.Errorf("%w: %s", ErrFormatUnsupported, format)
	}
}
