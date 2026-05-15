package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// CORE OpenScience aggregator — v5 cycle 2 / v2.9.0.
//
// API: POST https://api.core.ac.uk/v3/search/works
//   Headers: Authorization: Bearer <api_key>
//   Body (JSON): { "q": "<query>", "limit": int, "offset": int,
//                  "scroll": false, "stats": false }
//   Optional fields: "sort": [{ "field": "publishedDate", "order": "desc" }]
//
// Response (relevant subset):
//   {
//     "totalHits": int,
//     "limit": int, "offset": int,
//     "results": [
//       {
//         "id": int,
//         "doi": string,
//         "title": string,
//         "abstract": string,
//         "authors": [ { "name": "Doe, Jane" } ],
//         "publishedDate": "2024-01-15T00:00:00",
//         "yearPublished": int,
//         "language": { "code": "en", "name": "English" },
//         "downloadUrl": string,
//         "sourceFulltextUrls": [string],
//         "documentType": string,
//         "publisher": string,
//         "links": [ { "type": "display"|"download"|"reader", "url": string } ]
//       }
//     ]
//   }
//
// Free with registration (https://core.ac.uk/services/api). 350M+ OA works.
// Hosted by Open University (UK) — UK adequacy.
//
// Residency: UK (Open University). Admissible eu_preferred under UK
// adequacy decision; blocked under eu_strict unless IncludePublicResearch.
// ---------------------------------------------------------------------------

const (
	corePluginID          = SourceCORE
	corePluginName        = "CORE"
	corePluginDescription = "Search CORE (core.ac.uk) — 350M+ open-access research papers aggregated from repositories worldwide. Returns DOIs for cross-source dedup. Free with API-key registration; pass key via PluginConfig.APIKey or per-call credentials.core."

	coreDefaultBaseURL = "https://api.core.ac.uk/v3"
	coreSearchPath     = "/search/works"
	coreGetPathPrefix  = "/works/"
	coreDefaultLimit   = 25
	coreMaxLimitCap    = 100
	coreDefaultRPS     = 5.0
	coreDefaultTimeout = 15 * time.Second

	coreIDPrefix = "core:"

	coreSortFieldPublished = "publishedDate"
	coreSortOrderDesc      = "desc"
	coreSortOrderAsc       = "asc"

	coreCategoriesHint = "CORE indexes journal articles, theses, dissertations, conference papers, and other open-access scholarly works. No per-category filter at search time; documentType lands in SourceMetadata."

	coreMetaKeyDocumentType = "core_document_type"
	coreMetaKeyPublisher    = "core_publisher"
	coreMetaKeyYear         = "core_year_published"
)

// ---------------------------------------------------------------------------
// CORE wire types
// ---------------------------------------------------------------------------

type coreSearchRequest struct {
	Q      string           `json:"q"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset,omitempty"`
	Sort   []coreSortClause `json:"sort,omitempty"`
}

type coreSortClause struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

type coreSearchResponse struct {
	TotalHits int          `json:"totalHits"`
	Limit     int          `json:"limit"`
	Offset    int          `json:"offset"`
	Results   []coreResult `json:"results"`
}

type coreResult struct {
	ID                 int64        `json:"id"`
	DOI                string       `json:"doi,omitempty"`
	Title              string       `json:"title,omitempty"`
	Abstract           string       `json:"abstract,omitempty"`
	Authors            []coreAuthor `json:"authors,omitempty"`
	PublishedDate      string       `json:"publishedDate,omitempty"`
	YearPublished      int          `json:"yearPublished,omitempty"`
	Language           coreLang     `json:"language,omitempty"`
	DownloadURL        string       `json:"downloadUrl,omitempty"`
	SourceFulltextURLs []string     `json:"sourceFulltextUrls,omitempty"`
	DocumentType       string       `json:"documentType,omitempty"`
	Publisher          string       `json:"publisher,omitempty"`
	Links              []coreLink   `json:"links,omitempty"`
}

type coreAuthor struct {
	Name string `json:"name,omitempty"`
}

type coreLang struct {
	Code string `json:"code,omitempty"`
	Name string `json:"name,omitempty"`
}

type coreLink struct {
	Type string `json:"type,omitempty"`
	URL  string `json:"url,omitempty"`
}

// ---------------------------------------------------------------------------
// COREPlugin
// ---------------------------------------------------------------------------

// COREPlugin implements SourcePlugin for core.ac.uk. Thread-safe after
// Initialize.
type COREPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "core".
func (p *COREPlugin) ID() string { return corePluginID }

// Name returns the human-readable label.
func (p *COREPlugin) Name() string { return corePluginName }

// Description returns the LLM-facing one-liner.
func (p *COREPlugin) Description() string { return corePluginDescription }

// ContentTypes — CORE is paper-only.
func (p *COREPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *COREPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only; BibTeX assembled centrally.
func (p *COREPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// Capabilities reports CORE's filter/sort surface.
func (p *COREPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         true,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: true, // CORE only indexes OA — flag for completeness
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       coreMaxLimitCap,
		CategoriesHint:           coreCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource},
		Kinds:                    []ResultKind{KindPaper},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *COREPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = coreDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = coreDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = coreDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *COREPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a CORE /search/works POST query.
func (p *COREPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = coreDefaultLimit
	}
	if limit > coreMaxLimitCap {
		limit = coreMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceCORE, p.apiKey)
	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Results))
	for i := range resp.Results {
		pubs = append(pubs, coreResultToPublication(&resp.Results[i]))
	}
	return &SearchResult{
		Total:   resp.TotalHits,
		Results: pubs,
		HasMore: resp.TotalHits > params.Offset+len(pubs),
	}, nil
}

// Get retrieves a single work by CORE numeric ID.
func (p *COREPlugin) Get(ctx context.Context, id string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	reqURL := p.baseURL + coreGetPathPrefix + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	apiKey := CredentialFor(ctx, SourceCORE, p.apiKey)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("core: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: core work %s", ErrGetFailed, id)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("core: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var r coreResult
	if err := json.NewDecoder(httpResp.Body).Decode(&r); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("core: decode response: %w", err)
	}
	p.recordSuccess()
	pub := coreResultToPublication(&r)
	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *COREPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*coreSearchResponse, error) {
	body := coreSearchRequest{
		Q:      coreBuildQuery(params),
		Limit:  limit,
		Offset: params.Offset,
	}
	switch params.Sort {
	case SortDateDesc:
		body.Sort = []coreSortClause{{Field: coreSortFieldPublished, Order: coreSortOrderDesc}}
	case SortDateAsc:
		body.Sort = []coreSortClause{{Field: coreSortFieldPublished, Order: coreSortOrderAsc}}
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("core: encode body: %w", err)
	}

	reqURL := p.baseURL + coreSearchPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("core: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("core: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: core", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: core", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf2, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("core: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf2)))
	}

	var resp coreSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("core: decode response: %w", err)
	}
	return &resp, nil
}

// coreBuildQuery folds the free-text query plus date filters into CORE's
// Elastic-style q expression. Date ranges use the yearPublished field.
func coreBuildQuery(params SearchParams) string {
	parts := []string{}
	q := strings.TrimSpace(params.Query)
	if q != "" {
		parts = append(parts, q)
	}
	from := coreYearFromDate(params.Filters.DateFrom)
	to := coreYearFromDate(params.Filters.DateTo)
	if from != 0 || to != 0 {
		fromStr := "*"
		toStr := "*"
		if from != 0 {
			fromStr = strconv.Itoa(from)
		}
		if to != 0 {
			toStr = strconv.Itoa(to)
		}
		parts = append(parts, "yearPublished:["+fromStr+" TO "+toStr+"]")
	}
	return strings.Join(parts, " AND ")
}

// coreYearFromDate extracts the year from a YYYY or YYYY-MM-DD value.
func coreYearFromDate(s string) int {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return 0
	}
	y, err := strconv.Atoi(s[:4])
	if err != nil || y < 1000 {
		return 0
	}
	return y
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func coreResultToPublication(r *coreResult) Publication {
	rawID := strconv.FormatInt(r.ID, 10)

	authors := make([]Author, 0, len(r.Authors))
	for _, a := range r.Authors {
		if a.Name == "" {
			continue
		}
		authors = append(authors, Author{Name: a.Name})
	}

	pdfURL := r.DownloadURL
	if pdfURL == "" && len(r.SourceFulltextURLs) > 0 {
		pdfURL = r.SourceFulltextURLs[0]
	}

	displayURL := ""
	for _, l := range r.Links {
		if l.Type == "display" && l.URL != "" {
			displayURL = l.URL
			break
		}
	}
	if displayURL == "" {
		displayURL = "https://core.ac.uk/works/" + rawID
	}

	published := r.PublishedDate
	if len(published) >= 10 {
		published = published[:10]
	} else if r.YearPublished != 0 {
		published = strconv.Itoa(r.YearPublished)
	}

	meta := map[string]any{}
	if r.DocumentType != "" {
		meta[coreMetaKeyDocumentType] = r.DocumentType
	}
	if r.Publisher != "" {
		meta[coreMetaKeyPublisher] = r.Publisher
	}
	if r.YearPublished != 0 {
		meta[coreMetaKeyYear] = r.YearPublished
	}

	return Publication{
		ID:             coreIDPrefix + rawID,
		Source:         SourceCORE,
		ContentType:    ContentTypePaper,
		Title:          strings.TrimSpace(r.Title),
		Abstract:       stripXMLTags(r.Abstract),
		URL:            displayURL,
		PDFURL:         pdfURL,
		DOI:            strings.TrimSpace(r.DOI),
		Published:      published,
		Authors:        authors,
		Language:       r.Language.Code,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *COREPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *COREPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
