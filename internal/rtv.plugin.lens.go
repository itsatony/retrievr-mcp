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
// Lens.org scholarly provider — v6 cycle 3 / v2.16.0.
//
// API: POST https://api.lens.org/scholarly/search
//   Header:  Authorization: Bearer <token>
//   Body (JSON):
//     {
//       "query": { "match": { "full_text": "<q>" } },
//       "size": int,
//       "from": int,
//       "sort": [{"<field>":"asc|desc"}],
//       "include": [
//         "lens_id","title","abstract","year_published","external_ids",
//         "authors","source","publication_type","scholarly_citations_count"
//       ]
//     }
//
// Response:
//   { "total": int,
//     "data": [
//       { "lens_id":"...",
//         "title":"...",
//         "abstract":"...",
//         "year_published": int,
//         "external_ids":[{"type":"doi","value":"10.x/y"},
//                          {"type":"openalex","value":"W..."}],
//         "authors":[{"first_name","last_name","display_name","orcid":"..."}],
//         "source":{"title":"Nature"},
//         "publication_type":"journal article",
//         "scholarly_citations_count": int } ] }
//
// Paid; Lens.org subscription required. Per-call credential: `lens`.
// Refuses to start without a key.
// Residency: AU (Cambia, Australia). Covered-by-SCC.
// ---------------------------------------------------------------------------

const (
	lensPluginID          = SourceLens
	lensPluginName        = "Lens.org"
	lensPluginDescription = "Search Lens.org scholarly database via the REST API. Paid subscription required. Returns publications with DOI, authors, citation count, source journal, publication type. Complements Dimensions and CrossRef for citation-graph and patent-scholarly overlap analysis."

	lensDefaultBaseURL = "https://api.lens.org"
	lensSearchPath     = "/scholarly/search"
	lensDefaultLimit   = 25
	lensMaxLimitCap    = 100
	lensDefaultRPS     = 2.0
	lensDefaultTimeout = 30 * time.Second

	lensIDPrefix = "lens:"

	lensHeaderAuth   = "Authorization"
	lensBearerPrefix = "Bearer "

	lensCategoriesHint = "Lens.org subject classifications (MeSH / Fields of Study): pass via filters.categories[*]. Folded into the bool query as 'subjects.subject_name.keyword' terms."

	lensMetaKeySource          = "lens_source"
	lensMetaKeyPublicationType = "lens_publication_type"
	lensMetaKeyScholarlyCit    = "lens_scholarly_citations_count"
)

// ---------------------------------------------------------------------------
// Lens.org wire types
// ---------------------------------------------------------------------------

type lensSearchRequest struct {
	Query   map[string]any      `json:"query"`
	Size    int                 `json:"size,omitempty"`
	From    int                 `json:"from,omitempty"`
	Sort    []map[string]string `json:"sort,omitempty"`
	Include []string            `json:"include,omitempty"`
}

type lensSearchResponse struct {
	Total int        `json:"total,omitempty"`
	Data  []lensWork `json:"data,omitempty"`
}

type lensWork struct {
	LensID                  string       `json:"lens_id,omitempty"`
	Title                   string       `json:"title,omitempty"`
	Abstract                string       `json:"abstract,omitempty"`
	YearPublished           int          `json:"year_published,omitempty"`
	DatePublished           string       `json:"date_published,omitempty"`
	ExternalIDs             []lensExtID  `json:"external_ids,omitempty"`
	Authors                 []lensAuthor `json:"authors,omitempty"`
	Source                  *lensSource  `json:"source,omitempty"`
	PublicationType         string       `json:"publication_type,omitempty"`
	ScholarlyCitationsCount int          `json:"scholarly_citations_count,omitempty"`
}

type lensExtID struct {
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
}

type lensAuthor struct {
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	ORCID       string `json:"orcid,omitempty"`
}

type lensSource struct {
	Title string `json:"title,omitempty"`
}

// ---------------------------------------------------------------------------
// LensPlugin
// ---------------------------------------------------------------------------

// LensPlugin implements SourcePlugin for the Lens.org /scholarly/search
// API. Thread-safe after Initialize.
type LensPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "lens".
func (p *LensPlugin) ID() string { return lensPluginID }

// Name returns the human-readable label.
func (p *LensPlugin) Name() string { return lensPluginName }

// Description returns the LLM-facing one-liner.
func (p *LensPlugin) Description() string { return lensPluginDescription }

// ContentTypes — paper.
func (p *LensPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *LensPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON + BibTeX (assembled centrally).
func (p *LensPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// Capabilities reports Lens.org's filter/sort surface.
func (p *LensPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        true,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    true,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       lensMaxLimitCap,
		CategoriesHint:           lensCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource},
		Kinds:                    []ResultKind{KindPaper},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *LensPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = lensDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = lensDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = lensDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *LensPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Lens.org /scholarly/search query.
func (p *LensPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = lensDefaultLimit
	}
	if limit > lensMaxLimitCap {
		limit = lensMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceLens, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: lens requires an API key", ErrCredentialRequired)
	}

	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Data))
	for i := range resp.Data {
		pubs = append(pubs, lensWorkToPublication(&resp.Data[i]))
	}
	return &SearchResult{
		Total:   resp.Total,
		Results: pubs,
		HasMore: resp.Total > params.Offset+len(pubs),
	}, nil
}

// Get is not wired in cycle 3.
func (p *LensPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: lens Get is not wired in cycle 3", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *LensPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*lensSearchResponse, error) {
	reqBody := lensBuildRequest(params, limit)
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("lens: encode body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+lensSearchPath, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("lens: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(lensHeaderAuth, lensBearerPrefix+apiKey)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lens: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: lens", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: lens", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("lens: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp lensSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("lens: decode response: %w", err)
	}
	return &resp, nil
}

// lensBuildRequest constructs the JSON body for /scholarly/search. The
// query is a bool with a must-match on full_text plus optional year
// range + subject filters.
func lensBuildRequest(params SearchParams, limit int) lensSearchRequest {
	must := []map[string]any{
		{"match": map[string]any{"full_text": strings.TrimSpace(params.Query)}},
	}

	fromY := yearFromDateString(params.Filters.DateFrom)
	toY := yearFromDateString(params.Filters.DateTo)
	if fromY != 0 || toY != 0 {
		rangeBody := map[string]any{}
		if fromY != 0 {
			rangeBody["gte"] = fromY
		}
		if toY != 0 {
			rangeBody["lte"] = toY
		}
		must = append(must, map[string]any{"range": map[string]any{"year_published": rangeBody}})
	}

	for _, c := range params.Filters.Categories {
		if v := strings.TrimSpace(c); v != "" {
			must = append(must, map[string]any{
				"term": map[string]any{"subjects.subject_name.keyword": v},
			})
		}
	}

	body := lensSearchRequest{
		Query: map[string]any{"bool": map[string]any{"must": must}},
		Size:  limit,
		From:  params.Offset,
		Include: []string{
			"lens_id", "title", "abstract", "year_published", "date_published",
			"external_ids", "authors", "source", "publication_type",
			"scholarly_citations_count",
		},
	}

	switch params.Sort {
	case SortDateDesc:
		body.Sort = []map[string]string{{"date_published": "desc"}}
	case SortDateAsc:
		body.Sort = []map[string]string{{"date_published": "asc"}}
	case SortCitations:
		body.Sort = []map[string]string{{"scholarly_citations_count": "desc"}}
	}
	return body
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func lensWorkToPublication(w *lensWork) Publication {
	authors := make([]Author, 0, len(w.Authors))
	for _, a := range w.Authors {
		name := strings.TrimSpace(a.DisplayName)
		if name == "" {
			parts := []string{}
			if a.LastName != "" {
				parts = append(parts, a.LastName)
			}
			if a.FirstName != "" {
				parts = append(parts, a.FirstName)
			}
			name = strings.Join(parts, ", ")
		}
		if name == "" {
			continue
		}
		authors = append(authors, Author{Name: name, ORCID: a.ORCID})
	}

	// External IDs: DOI + ArXiv from the typed array.
	doi := ""
	arxiv := ""
	for _, e := range w.ExternalIDs {
		switch strings.ToLower(e.Type) {
		case "doi":
			doi = e.Value
		case "arxiv":
			arxiv = e.Value
		}
	}

	published := strings.TrimSpace(w.DatePublished)
	if len(published) >= 10 {
		published = published[:10]
	} else if w.YearPublished > 0 {
		published = strconv.Itoa(w.YearPublished)
	}

	citCount := w.ScholarlyCitationsCount
	var citPtr *int
	if citCount != 0 {
		citPtr = &citCount
	}

	displayURL := ""
	if doi != "" {
		displayURL = "https://doi.org/" + doi
	} else if w.LensID != "" {
		displayURL = "https://www.lens.org/lens/scholar/article/" + w.LensID
	}

	meta := map[string]any{}
	if w.Source != nil && w.Source.Title != "" {
		meta[lensMetaKeySource] = w.Source.Title
	}
	if w.PublicationType != "" {
		meta[lensMetaKeyPublicationType] = w.PublicationType
	}
	if w.ScholarlyCitationsCount != 0 {
		meta[lensMetaKeyScholarlyCit] = w.ScholarlyCitationsCount
	}

	return Publication{
		ID:             lensIDPrefix + w.LensID,
		Source:         SourceLens,
		ContentType:    ContentTypePaper,
		Title:          strings.TrimSpace(w.Title),
		Abstract:       stripXMLTags(w.Abstract),
		URL:            displayURL,
		DOI:            doi,
		ArXivID:        arxiv,
		Published:      published,
		Authors:        authors,
		CitationCount:  citPtr,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *LensPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *LensPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
