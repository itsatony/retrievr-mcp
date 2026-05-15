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
	"time"
)

// ---------------------------------------------------------------------------
// Zenodo OpenScience provider — v5 cycle 2 / v2.9.0.
//
// API: GET https://zenodo.org/api/records
//   Params:
//     q          free-text query (Lucene syntax tolerated)
//     size       page size (default 25, max 10000 but practical cap 100)
//     page       1-indexed pagination
//     sort       "bestmatch" (default) | "mostrecent" | "publication-date"
//     type       resource_type filter — "publication" | "dataset" |
//                "software" | "image" | "video" | "poster" | "presentation"
//                | "lesson" | "physicalobject" | "other"
//     access_right  "open" | "embargoed" | "restricted" | "closed"
//     bounds     filtering by publication_date via the q param:
//                  q=publication_date:[YYYY-MM-DD TO YYYY-MM-DD]
//
// Response shape (relevant fields):
//   {
//     "hits": {
//       "total": int,
//       "hits": [
//         {
//           "id": int,
//           "conceptrecid": string,
//           "doi": string,
//           "links": { "self": url, "self_html": url, "files": url },
//           "metadata": {
//             "title": string,
//             "publication_date": "YYYY-MM-DD",
//             "description": string (HTML — strip tags),
//             "creators": [ { "name": "Family, Given", "affiliation": string,
//                             "orcid": string } ],
//             "resource_type": { "type": string, "title": string },
//             "license": { "id": string },
//             "keywords": [string],
//             "language": "eng" (ISO 639-3, optional)
//           }
//         }
//       ]
//     }
//   }
//
// Free, no auth, public CERN-hosted infrastructure. Polite-pool guidance
// is informal but honored: short user-agent, modest request rate.
//
// Residency: EU (CERN, Geneva — Swiss site honoring EU adequacy norms).
// ---------------------------------------------------------------------------

const (
	zenodoPluginID          = SourceZenodo
	zenodoPluginName        = "Zenodo"
	zenodoPluginDescription = "Search Zenodo (CERN-hosted, EU) for open-access papers, datasets, software, and other research outputs. Returns DOIs for cross-source dedup with arXiv/CrossRef/OpenAlex. Free, no auth required. Filters: resource_type (categories), publication_date range."

	zenodoDefaultBaseURL = "https://zenodo.org"
	zenodoSearchPath     = "/api/records"
	zenodoDefaultLimit   = 25
	zenodoMaxLimitCap    = 100
	zenodoDefaultRPS     = 2.0
	zenodoDefaultTimeout = 10 * time.Second

	zenodoIDPrefix = "zenodo:"

	zenodoParamQ      = "q"
	zenodoParamSize   = "size"
	zenodoParamPage   = "page"
	zenodoParamSort   = "sort"
	zenodoParamType   = "type"
	zenodoParamAccess = "access_right"

	zenodoSortBestMatch  = "bestmatch"
	zenodoSortMostRecent = "mostrecent"

	zenodoAccessOpen = "open"

	zenodoCategoriesHint = "Zenodo resource_type values: publication, dataset, software, image, video, poster, presentation, lesson, physicalobject, other. Map via filters.categories — first non-empty value wins (Zenodo accepts a single type filter per request)."

	zenodoMetaKeyResourceType = "zenodo_resource_type"
	zenodoMetaKeyConceptRecID = "zenodo_concept_recid"
	zenodoMetaKeyKeywords     = "zenodo_keywords"
	zenodoMetaKeyAccessRight  = "zenodo_access_right"
)

// ---------------------------------------------------------------------------
// Zenodo wire types
// ---------------------------------------------------------------------------

type zenodoSearchResponse struct {
	Hits zenodoHitsBlock `json:"hits"`
}

type zenodoHitsBlock struct {
	Total int         `json:"total"`
	Hits  []zenodoHit `json:"hits"`
}

type zenodoHit struct {
	ID           int64           `json:"id"`
	ConceptRecID string          `json:"conceptrecid,omitempty"`
	DOI          string          `json:"doi,omitempty"`
	Links        zenodoLinks     `json:"links,omitempty"`
	Metadata     zenodoMetadata  `json:"metadata,omitempty"`
	Files        json.RawMessage `json:"files,omitempty"`
}

type zenodoLinks struct {
	Self     string `json:"self,omitempty"`
	SelfHTML string `json:"self_html,omitempty"`
	HTML     string `json:"html,omitempty"`
	Files    string `json:"files,omitempty"`
}

type zenodoMetadata struct {
	Title           string           `json:"title,omitempty"`
	PublicationDate string           `json:"publication_date,omitempty"`
	Description     string           `json:"description,omitempty"`
	Creators        []zenodoCreator  `json:"creators,omitempty"`
	ResourceType    zenodoResource   `json:"resource_type,omitempty"`
	License         zenodoLicenseRef `json:"license,omitempty"`
	Keywords        []string         `json:"keywords,omitempty"`
	Language        string           `json:"language,omitempty"`
	AccessRight     string           `json:"access_right,omitempty"`
}

type zenodoCreator struct {
	Name        string `json:"name,omitempty"`
	Affiliation string `json:"affiliation,omitempty"`
	ORCID       string `json:"orcid,omitempty"`
}

type zenodoResource struct {
	Type    string `json:"type,omitempty"`
	Subtype string `json:"subtype,omitempty"`
	Title   string `json:"title,omitempty"`
}

type zenodoLicenseRef struct {
	ID string `json:"id,omitempty"`
}

// ---------------------------------------------------------------------------
// ZenodoPlugin
// ---------------------------------------------------------------------------

// ZenodoPlugin implements SourcePlugin for the Zenodo open-research
// repository. Thread-safe after Initialize.
type ZenodoPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "zenodo".
func (p *ZenodoPlugin) ID() string { return zenodoPluginID }

// Name returns the human-readable label.
func (p *ZenodoPlugin) Name() string { return zenodoPluginName }

// Description returns the LLM-facing one-liner.
func (p *ZenodoPlugin) Description() string { return zenodoPluginDescription }

// ContentTypes — Zenodo holds papers, datasets, and software; we surface
// it as paper for the academic chain. Dataset / software discovery routes
// through the resource_type filter and is captured via SourceMetadata.
func (p *ZenodoPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper, ContentTypeDataset}
}

// NativeFormat — JSON.
func (p *ZenodoPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only (BibTeX assembled centrally from metadata).
func (p *ZenodoPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// Capabilities reports Zenodo's filter/sort surface.
func (p *ZenodoPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: true,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       zenodoMaxLimitCap,
		CategoriesHint:           zenodoCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource, IntentReference},
		Kinds:                    []ResultKind{KindPaper, KindDataset},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *ZenodoPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = zenodoDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = zenodoDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = zenodoDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *ZenodoPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Zenodo /api/records query.
func (p *ZenodoPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = zenodoDefaultLimit
	}
	if limit > zenodoMaxLimitCap {
		limit = zenodoMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Hits.Hits))
	for i := range resp.Hits.Hits {
		pubs = append(pubs, zenodoHitToPublication(&resp.Hits.Hits[i]))
	}
	return &SearchResult{
		Total:   resp.Hits.Total,
		Results: pubs,
		HasMore: resp.Hits.Total > params.Offset+len(pubs),
	}, nil
}

// Get retrieves a single record by Zenodo numeric ID.
func (p *ZenodoPlugin) Get(ctx context.Context, id string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	reqURL := p.baseURL + zenodoSearchPath + "/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("zenodo: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("zenodo: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: zenodo record %s", ErrGetFailed, id)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("zenodo: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var hit zenodoHit
	if err := json.NewDecoder(httpResp.Body).Decode(&hit); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("zenodo: decode response: %w", err)
	}
	p.recordSuccess()
	pub := zenodoHitToPublication(&hit)
	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *ZenodoPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*zenodoSearchResponse, error) {
	q := url.Values{}
	q.Set(zenodoParamQ, zenodoBuildQuery(params))
	q.Set(zenodoParamSize, strconv.Itoa(limit))
	if params.Offset > 0 {
		// Zenodo paginates 1-indexed by page; translate offset to page when
		// it aligns; otherwise leave page unset and rely on size capping.
		if params.Offset%limit == 0 {
			q.Set(zenodoParamPage, strconv.Itoa(params.Offset/limit+1))
		}
	}

	switch params.Sort {
	case SortDateDesc, SortDateAsc:
		q.Set(zenodoParamSort, zenodoSortMostRecent)
	default:
		q.Set(zenodoParamSort, zenodoSortBestMatch)
	}

	if len(params.Filters.Categories) > 0 {
		// Zenodo accepts a single resource_type per request — pick the first.
		if rt := strings.TrimSpace(params.Filters.Categories[0]); rt != "" {
			q.Set(zenodoParamType, rt)
		}
	}
	if params.Filters.OpenAccess != nil && *params.Filters.OpenAccess {
		q.Set(zenodoParamAccess, zenodoAccessOpen)
	}

	reqURL := p.baseURL + zenodoSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("zenodo: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zenodo: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: zenodo", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("zenodo: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp zenodoSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("zenodo: decode response: %w", err)
	}
	return &resp, nil
}

// zenodoBuildQuery folds the free-text query plus any structured date
// range into a single Zenodo q= expression. Zenodo's search uses an
// ElasticSearch-style syntax — date ranges are predicates on
// publication_date with bracket ranges.
func zenodoBuildQuery(params SearchParams) string {
	parts := []string{}
	q := strings.TrimSpace(params.Query)
	if q != "" {
		parts = append(parts, q)
	}
	from := zenodoNormalizeDate(params.Filters.DateFrom, true)
	to := zenodoNormalizeDate(params.Filters.DateTo, false)
	if from != "" || to != "" {
		if from == "" {
			from = "*"
		}
		if to == "" {
			to = "*"
		}
		parts = append(parts, "publication_date:["+from+" TO "+to+"]")
	}
	return strings.Join(parts, " AND ")
}

// zenodoNormalizeDate accepts YYYY or YYYY-MM-DD and returns a YYYY-MM-DD
// date suitable for the Zenodo publication_date range syntax. lowerBound
// expands a bare year to Jan 1; upperBound expands it to Dec 31.
func zenodoNormalizeDate(in string, lowerBound bool) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return ""
	}
	switch len(in) {
	case 4:
		if lowerBound {
			return in + "-01-01"
		}
		return in + "-12-31"
	case 10:
		return in
	}
	return in
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func zenodoHitToPublication(hit *zenodoHit) Publication {
	rawID := strconv.FormatInt(hit.ID, 10)
	meta := hit.Metadata

	authors := make([]Author, 0, len(meta.Creators))
	for _, c := range meta.Creators {
		if c.Name == "" {
			continue
		}
		authors = append(authors, Author(c))
	}

	htmlURL := hit.Links.SelfHTML
	if htmlURL == "" {
		htmlURL = hit.Links.HTML
	}
	if htmlURL == "" {
		htmlURL = "https://zenodo.org/record/" + rawID
	}

	ct := ContentTypePaper
	switch strings.ToLower(meta.ResourceType.Type) {
	case "dataset":
		ct = ContentTypeDataset
	case "software":
		// Software lands as paper — no dedicated ContentType yet; keep
		// the resource_type in SourceMetadata for callers that care.
		ct = ContentTypePaper
	}

	sourceMeta := map[string]any{}
	if meta.ResourceType.Type != "" {
		sourceMeta[zenodoMetaKeyResourceType] = meta.ResourceType.Type
	}
	if hit.ConceptRecID != "" {
		sourceMeta[zenodoMetaKeyConceptRecID] = hit.ConceptRecID
	}
	if len(meta.Keywords) > 0 {
		sourceMeta[zenodoMetaKeyKeywords] = meta.Keywords
	}
	if meta.AccessRight != "" {
		sourceMeta[zenodoMetaKeyAccessRight] = meta.AccessRight
	}

	return Publication{
		ID:             zenodoIDPrefix + rawID,
		Source:         SourceZenodo,
		ContentType:    ct,
		Title:          strings.TrimSpace(meta.Title),
		Abstract:       stripXMLTags(meta.Description),
		URL:            htmlURL,
		DOI:            strings.TrimSpace(hit.DOI),
		Published:      meta.PublicationDate,
		Authors:        authors,
		Categories:     meta.Keywords,
		License:        meta.License.ID,
		Language:       meta.Language,
		SourceMetadata: sourceMeta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *ZenodoPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *ZenodoPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
