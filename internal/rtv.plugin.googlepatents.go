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
// Google Patents provider — v5 cycle 5 / v2.12.0.
//
// Google publishes patent data via two paths:
//   1. BigQuery Public Datasets (paid, GCP creds required) — out of scope
//      for this open-source plugin.
//   2. The internal xhr/query endpoint that powers patents.google.com:
//        GET https://patents.google.com/xhr/query?url=<urlencoded q-string>
//      Returns a JSON body prefixed with `)]}'\n` (Google's anti-JSON-hijack
//      sentinel). The shape is undocumented but stable enough to ride.
//
// This plugin uses path 2 with the fragility explicitly called out — risk
// register entry in retrievr_v5.md §12 covers TOS shifts. If Google ever
// closes the xhr endpoint, the plugin disables itself rather than scraping
// HTML.
//
// Free, no auth required.
// Residency: US (Google LLC, Mountain View).
// ---------------------------------------------------------------------------

const (
	googlePatentsPluginID          = SourceGooglePatents
	googlePatentsPluginName        = "Google Patents"
	googlePatentsPluginDescription = "Search Google Patents via the public xhr/query endpoint. Free, no auth. Returns publication number, title, snippet, inventors, assignee, dates, CPC classifications. NOTE: rides Google's undocumented xhr endpoint — see retrievr_v5.md §12 for fragility caveats."

	googlePatentsDefaultBaseURL = "https://patents.google.com"
	googlePatentsSearchPath     = "/xhr/query"
	googlePatentsDefaultLimit   = 10
	googlePatentsMaxLimitCap    = 100
	googlePatentsDefaultRPS     = 1.0
	googlePatentsDefaultTimeout = 15 * time.Second

	googlePatentsIDPrefix = "googlepatents:"

	// xhrAntiHijackPrefix is the literal four-byte sentinel Google emits
	// before every JSON body to defeat JSON-hijacking attacks. We strip
	// it before decoding.
	googlePatentsAntiHijackPrefix = ")]}'"

	googlePatentsParamURL = "url"
	googlePatentsParamExp = "exp"
	googlePatentsParamDLD = "download"

	googlePatentsCategoriesHint = "Patent classifications: pass CPC codes (e.g. \"G06N3/08\") in filters.categories. Routed inline in the search q-string."

	googlePatentsPagePathFmt = "https://patents.google.com/patent/%s"
)

// ---------------------------------------------------------------------------
// xhr wire types
// ---------------------------------------------------------------------------

type googlePatentsResponse struct {
	Results         googlePatentsResults `json:"results"`
	TotalNumResults int                  `json:"total_num_results"`
}

type googlePatentsResults struct {
	Cluster []googlePatentsCluster `json:"cluster,omitempty"`
}

type googlePatentsCluster struct {
	Result []googlePatentsHit `json:"result,omitempty"`
}

type googlePatentsHit struct {
	Patent googlePatentsRecord `json:"patent,omitempty"`
}

type googlePatentsRecord struct {
	PublicationNumber string   `json:"publication_number,omitempty"`
	Title             string   `json:"title,omitempty"`
	Snippet           string   `json:"snippet,omitempty"`
	PriorityDate      string   `json:"priority_date,omitempty"`
	FilingDate        string   `json:"filing_date,omitempty"`
	GrantDate         string   `json:"grant_date,omitempty"`
	PublicationDate   string   `json:"publication_date,omitempty"`
	Inventor          string   `json:"inventor,omitempty"`
	Assignee          string   `json:"assignee,omitempty"`
	CPC               []string `json:"cpc,omitempty"`
	CountryCode       string   `json:"country_code,omitempty"`
	KindCode          string   `json:"kind_code,omitempty"`
}

// ---------------------------------------------------------------------------
// GooglePatentsPlugin
// ---------------------------------------------------------------------------

// GooglePatentsPlugin implements SourcePlugin for the patents.google.com
// xhr/query endpoint. Thread-safe after Initialize.
type GooglePatentsPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "googlepatents".
func (p *GooglePatentsPlugin) ID() string { return googlePatentsPluginID }

// Name returns the human-readable label.
func (p *GooglePatentsPlugin) Name() string { return googlePatentsPluginName }

// Description returns the LLM-facing one-liner.
func (p *GooglePatentsPlugin) Description() string { return googlePatentsPluginDescription }

// ContentTypes — patent.
func (p *GooglePatentsPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePatent} }

// NativeFormat — JSON.
func (p *GooglePatentsPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *GooglePatentsPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Google Patents' filter/sort surface.
func (p *GooglePatentsPlugin) Capabilities() SourceCapabilities {
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
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       false,
		MaxResultsPerQuery:       googlePatentsMaxLimitCap,
		CategoriesHint:           googlePatentsCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource},
		Kinds:                    []ResultKind{KindPatent},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *GooglePatentsPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = googlePatentsDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = googlePatentsDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = googlePatentsDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *GooglePatentsPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Google Patents xhr/query call and parses the result
// clusters.
func (p *GooglePatentsPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = googlePatentsDefaultLimit
	}
	if limit > googlePatentsMaxLimitCap {
		limit = googlePatentsMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := []Publication{}
	for _, cluster := range resp.Results.Cluster {
		for i := range cluster.Result {
			pubs = append(pubs, googlePatentsHitToPublication(&cluster.Result[i].Patent))
			if len(pubs) >= limit {
				break
			}
		}
		if len(pubs) >= limit {
			break
		}
	}
	return &SearchResult{
		Total:   resp.TotalNumResults,
		Results: pubs,
		HasMore: resp.TotalNumResults > len(pubs),
	}, nil
}

// Get is not wired in cycle 5 — patent detail pages are HTML and don't
// add structured value beyond the search snippet. A future cycle could
// route Get through the USPTO PEDS or PatentsView APIs.
func (p *GooglePatentsPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: googlepatents Get is not wired in cycle 5", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *GooglePatentsPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*googlePatentsResponse, error) {
	innerQ := url.Values{}
	innerQ.Set("q", googlePatentsBuildInnerQuery(params))
	innerQ.Set("num", strconv.Itoa(limit))
	if params.Sort == SortDateDesc || params.Sort == SortDateAsc {
		innerQ.Set("oq", "publication_date")
	}

	outer := url.Values{}
	outer.Set(googlePatentsParamURL, innerQ.Encode())
	outer.Set(googlePatentsParamExp, "")
	outer.Set(googlePatentsParamDLD, "false")

	reqURL := p.baseURL + googlePatentsSearchPath + "?" + outer.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("googlepatents: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("googlepatents: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: googlepatents", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("googlepatents: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("googlepatents: read body: %w", err)
	}
	// Strip the anti-hijack sentinel if present. A missing prefix is
	// tolerated for test fixtures.
	cleaned := strings.TrimPrefix(strings.TrimSpace(string(body)), googlePatentsAntiHijackPrefix)
	cleaned = strings.TrimLeft(cleaned, "\n\r\t ")

	var resp googlePatentsResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return nil, fmt.Errorf("googlepatents: decode response: %w", err)
	}
	return &resp, nil
}

// googlePatentsBuildInnerQuery folds the free-text query plus
// date/category filters into Google Patents' search syntax.
func googlePatentsBuildInnerQuery(params SearchParams) string {
	parts := []string{}
	if q := strings.TrimSpace(params.Query); q != "" {
		parts = append(parts, q)
	}
	for _, c := range params.Filters.Categories {
		if v := strings.TrimSpace(c); v != "" {
			parts = append(parts, "(CPC="+v+")")
		}
	}
	if d := strings.TrimSpace(params.Filters.DateFrom); d != "" {
		parts = append(parts, "after:filing:"+normalizeDateYYYYMMDD(d, true))
	}
	if d := strings.TrimSpace(params.Filters.DateTo); d != "" {
		parts = append(parts, "before:filing:"+normalizeDateYYYYMMDD(d, false))
	}
	return strings.Join(parts, " ")
}

// normalizeDateYYYYMMDD expands YYYY → YYYY-01-01 / YYYY-12-31 and passes
// YYYY-MM-DD through. Hyphens swapped for slashes per Google's preferred
// date-operator syntax.
func normalizeDateYYYYMMDD(in string, lower bool) string {
	switch len(in) {
	case 4:
		if lower {
			return in + "0101"
		}
		return in + "1231"
	case 10:
		return strings.ReplaceAll(in, "-", "")
	}
	return strings.ReplaceAll(in, "-", "")
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func googlePatentsHitToPublication(rec *googlePatentsRecord) Publication {
	pubNum := strings.TrimSpace(rec.PublicationNumber)

	inventors := []Author{}
	for _, n := range splitMultiAuthorName(rec.Inventor) {
		inventors = append(inventors, Author{Name: n})
	}

	jurisdiction := rec.CountryCode
	if jurisdiction == "" && len(pubNum) >= 2 {
		jurisdiction = pubNum[:2]
	}

	published := rec.PublicationDate
	if published == "" {
		published = rec.GrantDate
	}
	if published == "" {
		published = rec.FilingDate
	}
	if len(published) >= 10 {
		published = published[:10]
	} else if len(published) == 8 {
		published = published[:4] + "-" + published[4:6] + "-" + published[6:8]
	}

	meta := map[string]any{
		MetaKeyPatentNumber: pubNum,
	}
	if rec.Assignee != "" {
		meta[smetaPatentAssignee] = rec.Assignee
	}
	if rec.Inventor != "" {
		meta[smetaPatentInventors] = rec.Inventor
	}
	if len(rec.CPC) > 0 {
		meta[smetaPatentCPC] = rec.CPC
	}
	if jurisdiction != "" {
		meta[smetaPatentJurisdiction] = jurisdiction
	}
	if rec.KindCode != "" {
		meta[smetaPatentKindCode] = rec.KindCode
	}
	if rec.FilingDate != "" {
		meta[smetaPatentFilingDate] = rec.FilingDate
	}

	return Publication{
		ID:             googlePatentsIDPrefix + pubNum,
		Source:         SourceGooglePatents,
		ContentType:    ContentTypePatent,
		Title:          strings.TrimSpace(rec.Title),
		Abstract:       stripXMLTags(rec.Snippet),
		URL:            fmt.Sprintf(googlePatentsPagePathFmt, pubNum),
		Published:      published,
		Authors:        inventors,
		Categories:     rec.CPC,
		SourceMetadata: meta,
	}
}

// splitMultiAuthorName splits a `;`- or `,`-joined author string. Most
// patent providers emit either single-author strings ("Doe, Jane") or
// semicolon-joined lists ("Doe, Jane; Smith, Bob"). When the only
// separator is `,` the result is left as a single entry — patent
// inventors almost always include a comma inside their own name.
func splitMultiAuthorName(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.Contains(s, ";") {
		out := make([]string, 0)
		for _, p := range strings.Split(s, ";") {
			if v := strings.TrimSpace(p); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return []string{s}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *GooglePatentsPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *GooglePatentsPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
