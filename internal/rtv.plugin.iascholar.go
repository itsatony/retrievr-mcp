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
// Internet Archive Scholar provider — v5 cycle 6 / v2.13.0.
//
// API: GET https://scholar.archive.org/search?q=<q>&format=json
//   Params:
//     q              free-text query
//     format         "json"
//     filter_year    "YYYY-YYYY" range (alternate: filter_time)
//     offset         0-indexed
//     limit          1..100
//
// Response (simplified):
//   { "count_found": int,
//     "results": [ { "ident","title","release_year","release_type",
//                     "abstracts":[{"body":"..."}],
//                     "contribs":[{"raw_name":"..."}],
//                     "ext_ids":{"doi":"...","arxiv_id":"..."},
//                     "publisher":"...","cover_url":"..." } ] }
//
// IA Scholar aggregates open-access scholarly papers + Wayback fallbacks
// for paywalled originals. Free, no auth.
//
// Residency: US (Internet Archive non-profit, San Francisco).
// ---------------------------------------------------------------------------

const (
	iascholarPluginID          = SourceIAScholar
	iascholarPluginName        = "IA Scholar"
	iascholarPluginDescription = "Search Internet Archive Scholar (scholar.archive.org) — aggregator of open-access scholarly papers with Wayback fallbacks for paywalled originals. Free, no auth required. Returns DOI + ArXiv ID for cross-source dedup. US non-profit residency."

	iascholarDefaultBaseURL = "https://scholar.archive.org"
	iascholarSearchPath     = "/search"
	iascholarDefaultLimit   = 20
	iascholarMaxLimitCap    = 100
	iascholarDefaultRPS     = 3.0
	iascholarDefaultTimeout = 20 * time.Second

	iascholarIDPrefix = "iascholar:"

	iascholarParamQ          = "q"
	iascholarParamFormat     = "format"
	iascholarParamLimit      = "limit"
	iascholarParamOffset     = "offset"
	iascholarParamFilterYear = "filter_year"

	iascholarFormatJSON = "json"

	iascholarCategoriesHint = "IA Scholar release-types: article-journal, paper-conference, thesis, report, book, chapter, dataset. Pass as filters.categories[0]; mapped to release_type query token."

	iascholarMetaKeyReleaseType = "iascholar_release_type"
	iascholarMetaKeyPublisher   = "iascholar_publisher"
	iascholarMetaKeyCoverURL    = "iascholar_cover_url"
	iascholarMetaKeyIdent       = "iascholar_ident"
)

// ---------------------------------------------------------------------------
// IA Scholar wire types
// ---------------------------------------------------------------------------

type iascholarSearchResponse struct {
	CountFound int                `json:"count_found"`
	Results    []iascholarRelease `json:"results"`
}

type iascholarRelease struct {
	Ident           string               `json:"ident,omitempty"`
	Title           string               `json:"title,omitempty"`
	ReleaseYear     int                  `json:"release_year,omitempty"`
	ReleaseType     string               `json:"release_type,omitempty"`
	Abstracts       []iascholarAbstract  `json:"abstracts,omitempty"`
	Contribs        []iascholarContrib   `json:"contribs,omitempty"`
	ExtIDs          iascholarExtIDs      `json:"ext_ids,omitempty"`
	Publisher       string               `json:"publisher,omitempty"`
	CoverURL        string               `json:"cover_url,omitempty"`
	Language        string               `json:"language,omitempty"`
	WaybackFallback []iascholarWaybackFB `json:"wayback,omitempty"`
}

type iascholarAbstract struct {
	Body string `json:"body,omitempty"`
}

type iascholarContrib struct {
	RawName string `json:"raw_name,omitempty"`
	Index   int    `json:"index,omitempty"`
}

type iascholarExtIDs struct {
	DOI     string `json:"doi,omitempty"`
	ArXivID string `json:"arxiv_id,omitempty"`
	PMID    string `json:"pmid,omitempty"`
	PMCID   string `json:"pmcid,omitempty"`
}

type iascholarWaybackFB struct {
	URL       string `json:"url,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// ---------------------------------------------------------------------------
// IAScholarPlugin
// ---------------------------------------------------------------------------

// IAScholarPlugin implements SourcePlugin for the scholar.archive.org
// JSON search endpoint. Thread-safe after Initialize.
type IAScholarPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "iascholar".
func (p *IAScholarPlugin) ID() string { return iascholarPluginID }

// Name returns the human-readable label.
func (p *IAScholarPlugin) Name() string { return iascholarPluginName }

// Description returns the LLM-facing one-liner.
func (p *IAScholarPlugin) Description() string { return iascholarPluginDescription }

// ContentTypes — paper.
func (p *IAScholarPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *IAScholarPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON + BibTeX (assembled centrally).
func (p *IAScholarPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// Capabilities reports IA Scholar's filter/sort surface.
func (p *IAScholarPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false, // IA Scholar is OA-only by mission
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       iascholarMaxLimitCap,
		CategoriesHint:           iascholarCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource},
		Kinds:                    []ResultKind{KindPaper},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *IAScholarPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = iascholarDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = iascholarDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = iascholarDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *IAScholarPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes an IA Scholar /search?format=json query.
func (p *IAScholarPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = iascholarDefaultLimit
	}
	if limit > iascholarMaxLimitCap {
		limit = iascholarMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Results))
	for i := range resp.Results {
		pubs = append(pubs, iascholarReleaseToPublication(&resp.Results[i]))
	}
	return &SearchResult{
		Total:   resp.CountFound,
		Results: pubs,
		HasMore: resp.CountFound > params.Offset+len(pubs),
	}, nil
}

// Get is not wired in cycle 6 — IA Scholar's per-release page is HTML.
// Future cycle can route through the Fatcat REST API
// (https://api.fatcat.wiki/v0/release/<ident>) for full JSON metadata.
func (p *IAScholarPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: iascholar Get is not wired in cycle 6", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *IAScholarPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*iascholarSearchResponse, error) {
	q := url.Values{}
	q.Set(iascholarParamQ, iascholarBuildQuery(params))
	q.Set(iascholarParamFormat, iascholarFormatJSON)
	q.Set(iascholarParamLimit, strconv.Itoa(limit))
	if params.Offset > 0 {
		q.Set(iascholarParamOffset, strconv.Itoa(params.Offset))
	}

	yearRange := iascholarYearRange(params.Filters.DateFrom, params.Filters.DateTo)
	if yearRange != "" {
		q.Set(iascholarParamFilterYear, yearRange)
	}

	reqURL := p.baseURL + iascholarSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("iascholar: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iascholar: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: iascholar", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("iascholar: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp iascholarSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("iascholar: decode response: %w", err)
	}
	return &resp, nil
}

// iascholarBuildQuery folds free-text + release_type filter into the q
// parameter.
func iascholarBuildQuery(params SearchParams) string {
	parts := []string{}
	if q := strings.TrimSpace(params.Query); q != "" {
		parts = append(parts, q)
	}
	if len(params.Filters.Categories) > 0 {
		if rt := strings.TrimSpace(params.Filters.Categories[0]); rt != "" {
			parts = append(parts, "release_type:"+rt)
		}
	}
	return strings.Join(parts, " ")
}

// iascholarYearRange builds "YYYY-YYYY" from DateFrom/DateTo (each can
// be bare YYYY or YYYY-MM-DD). Returns "" if both are empty.
func iascholarYearRange(from, to string) string {
	yearOf := func(s string) string {
		s = strings.TrimSpace(s)
		if len(s) >= 4 {
			return s[:4]
		}
		return ""
	}
	f := yearOf(from)
	t := yearOf(to)
	if f == "" && t == "" {
		return ""
	}
	if f == "" {
		f = "*"
	}
	if t == "" {
		t = "*"
	}
	return f + "-" + t
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func iascholarReleaseToPublication(r *iascholarRelease) Publication {
	abstract := ""
	if len(r.Abstracts) > 0 {
		abstract = stripXMLTags(r.Abstracts[0].Body)
	}

	authors := make([]Author, 0, len(r.Contribs))
	for _, c := range r.Contribs {
		if n := strings.TrimSpace(c.RawName); n != "" {
			authors = append(authors, Author{Name: n})
		}
	}

	published := ""
	if r.ReleaseYear > 0 {
		published = strconv.Itoa(r.ReleaseYear)
	}

	id := r.Ident
	if id == "" {
		id = r.ExtIDs.DOI
	}

	displayURL := ""
	if r.ExtIDs.DOI != "" {
		displayURL = "https://doi.org/" + r.ExtIDs.DOI
	} else if r.Ident != "" {
		displayURL = "https://scholar.archive.org/work/" + r.Ident
	}

	// Wayback fallback PDF/URL when present.
	pdfURL := ""
	if len(r.WaybackFallback) > 0 {
		pdfURL = r.WaybackFallback[0].URL
	}

	meta := map[string]any{
		iascholarMetaKeyIdent: r.Ident,
	}
	if r.ReleaseType != "" {
		meta[iascholarMetaKeyReleaseType] = r.ReleaseType
	}
	if r.Publisher != "" {
		meta[iascholarMetaKeyPublisher] = r.Publisher
	}
	if r.CoverURL != "" {
		meta[iascholarMetaKeyCoverURL] = r.CoverURL
	}

	return Publication{
		ID:             iascholarIDPrefix + id,
		Source:         SourceIAScholar,
		ContentType:    ContentTypePaper,
		Title:          strings.TrimSpace(r.Title),
		Abstract:       abstract,
		URL:            displayURL,
		PDFURL:         pdfURL,
		DOI:            r.ExtIDs.DOI,
		ArXivID:        r.ExtIDs.ArXivID,
		Published:      published,
		Authors:        authors,
		Language:       r.Language,
		ThumbnailURL:   r.CoverURL,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *IAScholarPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *IAScholarPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
