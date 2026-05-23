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
// GDELT 2.0 DOC provider — v5 cycle 6 / v2.13.0.
//
// API: GET https://api.gdeltproject.org/api/v2/doc/doc
//   Params:
//     query              free-text query; tokens like sourcelang:eng,
//                        sourcecountry:US, theme:CLIMATE may be appended
//     mode               "ArtList" returns article-level records
//     format             "json"
//     maxrecords         1..250 (default 75)
//     sort               "HybridRel" (default) | "DateDesc" | "DateAsc"
//                        | "ToneDesc" | "ToneAsc"
//     timespan           shorthand like "1d", "1w", "1m"
//     startdatetime /
//     enddatetime        YYYYMMDDHHMMSS bounds
//
// Response (ArtList):
//   { "articles": [ { "url","url_mobile","title","seendate":"YYYYMMDDTHHMMSSZ",
//                      "socialimage","domain","language","sourcecountry" } ] }
//
// Free, no auth. Real-time event stream with ~15-minute update cycle.
// 60+ languages, global coverage.
//
// Residency: US (Georgetown / Google partnership; data hosted in US).
// ---------------------------------------------------------------------------

const (
	gdeltPluginID          = SourceGDELT
	gdeltPluginName        = "GDELT 2.0"
	gdeltPluginDescription = "Search the GDELT 2.0 Global Database of Events, Language & Tone — real-time global news with 15-minute update cycle, 60+ languages, structured theme/country tags. Free, no auth required. ArtList mode (article-level records)."

	gdeltDefaultBaseURL = "https://api.gdeltproject.org"
	gdeltSearchPath     = "/api/v2/doc/doc"
	gdeltDefaultLimit   = 25
	gdeltMaxLimitCap    = 250
	gdeltDefaultRPS     = 1.0
	gdeltDefaultTimeout = 20 * time.Second

	gdeltIDPrefix = "gdelt:"

	gdeltParamQuery       = "query"
	gdeltParamMode        = "mode"
	gdeltParamFormat      = "format"
	gdeltParamMaxRecords  = "maxrecords"
	gdeltParamSort        = "sort"
	gdeltParamStartDateTm = "startdatetime"
	gdeltParamEndDateTm   = "enddatetime"

	gdeltModeArtList   = "ArtList"
	gdeltFormatJSON    = "json"
	gdeltSortHybridRel = "HybridRel"
	gdeltSortDateDesc  = "DateDesc"
	gdeltSortDateAsc   = "DateAsc"

	gdeltCategoriesHint = "GDELT themes (CRISIS-related: 'PROTEST', 'CRIME_TERROR'; sectoral: 'CLIMATE', 'WB_2024_ECONOMIC_POLICY'). Pass as filters.categories[*] — each is appended as 'theme:<value>' to the query."

	gdeltMetaKeyDomain        = "gdelt_domain"
	gdeltMetaKeySourceCountry = "gdelt_source_country"
	gdeltMetaKeySeenDate      = "gdelt_seen_date"
	gdeltMetaKeyImageURL      = "gdelt_social_image"
)

// ---------------------------------------------------------------------------
// GDELT wire types
// ---------------------------------------------------------------------------

type gdeltSearchResponse struct {
	Articles []gdeltArticle `json:"articles,omitempty"`
}

type gdeltArticle struct {
	URL           string `json:"url,omitempty"`
	URLMobile     string `json:"url_mobile,omitempty"`
	Title         string `json:"title,omitempty"`
	SeenDate      string `json:"seendate,omitempty"`
	SocialImage   string `json:"socialimage,omitempty"`
	Domain        string `json:"domain,omitempty"`
	Language      string `json:"language,omitempty"`
	SourceCountry string `json:"sourcecountry,omitempty"`
}

// ---------------------------------------------------------------------------
// GDELTPlugin
// ---------------------------------------------------------------------------

// GDELTPlugin implements SourcePlugin for the GDELT 2.0 DOC API.
// Thread-safe after Initialize.
type GDELTPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "gdelt".
func (p *GDELTPlugin) ID() string { return gdeltPluginID }

// Name returns the human-readable label.
func (p *GDELTPlugin) Name() string { return gdeltPluginName }

// Description returns the LLM-facing one-liner.
func (p *GDELTPlugin) Description() string { return gdeltPluginDescription }

// ContentTypes — paper (news lands here with KindNews at the v2 layer).
func (p *GDELTPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *GDELTPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *GDELTPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports GDELT's filter/sort surface.
func (p *GDELTPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     true,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true,
		SupportsPagination:       false,
		MaxResultsPerQuery:       gdeltMaxLimitCap,
		CategoriesHint:           gdeltCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentNews, IntentQuickLookup},
		Kinds:                    []ResultKind{KindNews},

		SupportsPublishedAfterFilter: PublishedAfterNative,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *GDELTPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = gdeltDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = gdeltDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = gdeltDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *GDELTPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a GDELT /api/v2/doc/doc ArtList query.
func (p *GDELTPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = gdeltDefaultLimit
	}
	if limit > gdeltMaxLimitCap {
		limit = gdeltMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Articles))
	for i := range resp.Articles {
		pubs = append(pubs, gdeltArticleToPublication(&resp.Articles[i]))
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 6 — GDELT articles point to upstream news
// URLs; full-text retrieval is the consumer's responsibility (use the
// firecrawl enrichment hook or fetch the URL directly).
func (p *GDELTPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: gdelt Get is not wired in cycle 6", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *GDELTPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*gdeltSearchResponse, error) {
	q := url.Values{}
	q.Set(gdeltParamQuery, gdeltBuildQuery(params))
	q.Set(gdeltParamMode, gdeltModeArtList)
	q.Set(gdeltParamFormat, gdeltFormatJSON)
	q.Set(gdeltParamMaxRecords, strconv.Itoa(limit))

	switch params.Sort {
	case SortDateDesc:
		q.Set(gdeltParamSort, gdeltSortDateDesc)
	case SortDateAsc:
		q.Set(gdeltParamSort, gdeltSortDateAsc)
	default:
		q.Set(gdeltParamSort, gdeltSortHybridRel)
	}

	// v2.22.0 — GDELT's STARTDATETIME/ENDDATETIME are 14-digit
	// YYYYMMDDHHMMSS in UTC; the format natively supports sub-day
	// precision, so prefer PublishedAfter / PublishedBefore over the day
	// bound when set.
	if t, ok, _ := parsePublishedAt(params.Filters.PublishedAfter); ok {
		q.Set(gdeltParamStartDateTm, t.Format("20060102150405"))
	} else if from := strings.TrimSpace(params.Filters.DateFrom); from != "" {
		q.Set(gdeltParamStartDateTm, gdeltDatetimeBound(from, true))
	}
	if t, ok, _ := parsePublishedAt(params.Filters.PublishedBefore); ok {
		q.Set(gdeltParamEndDateTm, t.Format("20060102150405"))
	} else if to := strings.TrimSpace(params.Filters.DateTo); to != "" {
		q.Set(gdeltParamEndDateTm, gdeltDatetimeBound(to, false))
	}

	reqURL := p.baseURL + gdeltSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("gdelt: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gdelt: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: gdelt", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("gdelt: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp gdeltSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("gdelt: decode response: %w", err)
	}
	return &resp, nil
}

// gdeltBuildQuery folds the free-text query plus language/category/domain
// filters into GDELT's CQL-style q syntax.
func gdeltBuildQuery(params SearchParams) string {
	parts := []string{}
	if q := strings.TrimSpace(params.Query); q != "" {
		parts = append(parts, q)
	}
	if lang := strings.TrimSpace(params.Filters.Language); lang != "" {
		parts = append(parts, "sourcelang:"+strings.ToLower(lang))
	}
	for _, c := range params.Filters.Categories {
		if v := strings.TrimSpace(c); v != "" {
			parts = append(parts, "theme:"+v)
		}
	}
	for _, d := range params.Filters.IncludeDomains {
		if v := strings.TrimSpace(d); v != "" {
			parts = append(parts, "domain:"+v)
		}
	}
	return strings.Join(parts, " ")
}

// gdeltDatetimeBound expands YYYY-MM-DD (or YYYY) to GDELT's 14-digit
// YYYYMMDDHHMMSS timestamps. Lower bound → 00:00:00, upper → 23:59:59.
func gdeltDatetimeBound(in string, lower bool) string {
	switch len(in) {
	case 4:
		if lower {
			return in + "0101000000"
		}
		return in + "1231235959"
	case 10:
		stripped := strings.ReplaceAll(in, "-", "")
		if lower {
			return stripped + "000000"
		}
		return stripped + "235959"
	}
	return strings.ReplaceAll(in, "-", "")
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func gdeltArticleToPublication(a *gdeltArticle) Publication {
	rawID := a.URL
	published := ""
	if len(a.SeenDate) >= 8 {
		// Format: YYYYMMDDTHHMMSSZ → YYYY-MM-DD.
		published = a.SeenDate[:4] + "-" + a.SeenDate[4:6] + "-" + a.SeenDate[6:8]
	}

	meta := map[string]any{
		gdeltMetaKeyDomain:        a.Domain,
		gdeltMetaKeySourceCountry: a.SourceCountry,
		gdeltMetaKeySeenDate:      a.SeenDate,
	}
	if a.SocialImage != "" {
		meta[gdeltMetaKeyImageURL] = a.SocialImage
	}

	return Publication{
		ID:             gdeltIDPrefix + rawID,
		Source:         SourceGDELT,
		ContentType:    ContentTypePaper,
		Title:          strings.TrimSpace(a.Title),
		URL:            a.URL,
		Published:      published,
		Language:       a.Language,
		ThumbnailURL:   a.SocialImage,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *GDELTPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *GDELTPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
