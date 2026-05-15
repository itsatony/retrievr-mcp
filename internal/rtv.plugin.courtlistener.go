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
// CourtListener provider — v5 cycle 5 / v2.12.0.
//
// API: GET https://www.courtlistener.com/api/rest/v4/search/?q=<q>&type=o
//   Params:
//     q         free-text query
//     type      "o" = opinions (default for legal-research use), "r" =
//               RECAP docs, "oa" = oral arguments, etc. We default to "o".
//     court     comma-joined court slugs (e.g. "scotus,ca9")
//     filed_after / filed_before  date filters in YYYY-MM-DD
//     page_size 1..50
//     cursor    keyset pagination token
//
// Headers (optional but recommended):
//   Authorization: Token <api-token>     — bumps the rate limit
//
// Response (v4 search):
//   { "count": int, "next": "...", "previous": "...",
//     "results": [ { "id","absolute_url","caseName","court",
//                     "court_citation_string","dateFiled","citation":[...],
//                     "docketNumber","snippet","status" } ] }
//
// Free, non-profit (Free Law Project). 8M+ US federal + state opinions.
//
// Emits ContentTypePaper with KindLaw — dedup on citation_code.
//
// Residency: US (Free Law Project, non-profit).
// ---------------------------------------------------------------------------

const (
	courtListenerPluginID          = SourceCourtListener
	courtListenerPluginName        = "CourtListener"
	courtListenerPluginDescription = "Search CourtListener (Free Law Project) for US federal and state court opinions. 8M+ records. Free; optional API token (per-call: courtlistener) lifts rate limits. Emits paper-typed results with Result.Kind = KindLaw; dedup keyed on citation code."

	courtListenerDefaultBaseURL = "https://www.courtlistener.com/api/rest/v4"
	courtListenerSearchPath     = "/search/"
	courtListenerDefaultLimit   = 20
	courtListenerMaxLimitCap    = 50
	courtListenerDefaultRPS     = 5.0
	courtListenerDefaultTimeout = 15 * time.Second

	courtListenerIDPrefix = "courtlistener:"

	courtListenerCategoriesHint = "CourtListener court slugs (lowercase): scotus, ca1..ca11, ca-dc, ca-fed, state slugs like ny, ca, tx. Pass via filters.categories — comma-joined as the court= param."

	courtListenerHeaderAuthorization = "Authorization"
	courtListenerTokenPrefix         = "Token "
	courtListenerTypeOpinion         = "o"
)

// ---------------------------------------------------------------------------
// CourtListener wire types
// ---------------------------------------------------------------------------

type courtListenerSearchResponse struct {
	Count   int                      `json:"count"`
	Next    string                   `json:"next,omitempty"`
	Results []courtListenerSearchHit `json:"results"`
}

type courtListenerSearchHit struct {
	ID                  json.RawMessage `json:"id,omitempty"` // can be int or string
	AbsoluteURL         string          `json:"absolute_url,omitempty"`
	CaseName            string          `json:"caseName,omitempty"`
	Court               string          `json:"court,omitempty"`
	CourtCitationString string          `json:"court_citation_string,omitempty"`
	DateFiled           string          `json:"dateFiled,omitempty"`
	Citation            []string        `json:"citation,omitempty"`
	DocketNumber        string          `json:"docketNumber,omitempty"`
	Snippet             string          `json:"snippet,omitempty"`
	Status              string          `json:"status,omitempty"`
}

// ---------------------------------------------------------------------------
// CourtListenerPlugin
// ---------------------------------------------------------------------------

// CourtListenerPlugin implements SourcePlugin for the CourtListener v4
// search API. Thread-safe after Initialize.
type CourtListenerPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "courtlistener".
func (p *CourtListenerPlugin) ID() string { return courtListenerPluginID }

// Name returns the human-readable label.
func (p *CourtListenerPlugin) Name() string { return courtListenerPluginName }

// Description returns the LLM-facing one-liner.
func (p *CourtListenerPlugin) Description() string { return courtListenerPluginDescription }

// ContentTypes — paper (law results carry the KindLaw discriminator at
// the v2 layer).
func (p *CourtListenerPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *CourtListenerPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *CourtListenerPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports CourtListener's filter/sort surface.
func (p *CourtListenerPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        true,
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
		SupportsPagination:       true,
		MaxResultsPerQuery:       courtListenerMaxLimitCap,
		CategoriesHint:           courtListenerCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource, IntentReference},
		Kinds:                    []ResultKind{KindLaw},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *CourtListenerPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = courtListenerDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = courtListenerDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = courtListenerDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *CourtListenerPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a CourtListener /search/ query.
func (p *CourtListenerPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = courtListenerDefaultLimit
	}
	if limit > courtListenerMaxLimitCap {
		limit = courtListenerMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceCourtListener, p.apiKey)
	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Results))
	for i := range resp.Results {
		pubs = append(pubs, courtListenerHitToPublication(&resp.Results[i]))
	}
	return &SearchResult{
		Total:   resp.Count,
		Results: pubs,
		HasMore: resp.Next != "",
	}, nil
}

// Get is not wired in cycle 5 — individual opinion fetch via /opinions/{id}/
// is reasonable but not on the cycle's critical path.
func (p *CourtListenerPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: courtlistener Get is not wired in cycle 5", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *CourtListenerPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*courtListenerSearchResponse, error) {
	q := url.Values{}
	q.Set("q", params.Query)
	q.Set("type", courtListenerTypeOpinion)
	q.Set("page_size", strconv.Itoa(limit))

	if len(params.Filters.Categories) > 0 {
		joined := strings.Join(params.Filters.Categories, ",")
		q.Set("court", strings.ToLower(joined))
	}
	if d := strings.TrimSpace(params.Filters.DateFrom); d != "" {
		q.Set("filed_after", normalizeDateYYYYMMDDHyphen(d, true))
	}
	if d := strings.TrimSpace(params.Filters.DateTo); d != "" {
		q.Set("filed_before", normalizeDateYYYYMMDDHyphen(d, false))
	}
	switch params.Sort {
	case SortDateDesc:
		q.Set("order_by", "dateFiled desc")
	case SortDateAsc:
		q.Set("order_by", "dateFiled asc")
	}

	reqURL := p.baseURL + courtListenerSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("courtlistener: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set(courtListenerHeaderAuthorization, courtListenerTokenPrefix+apiKey)
	}

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("courtlistener: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: courtlistener", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: courtlistener", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("courtlistener: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp courtListenerSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("courtlistener: decode response: %w", err)
	}
	return &resp, nil
}

// normalizeDateYYYYMMDDHyphen expands a bare year to Jan 1 / Dec 31 and
// passes YYYY-MM-DD through unchanged. Output keeps the hyphenated form
// CourtListener's filters expect.
func normalizeDateYYYYMMDDHyphen(in string, lower bool) string {
	switch len(in) {
	case 4:
		if lower {
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

func courtListenerHitToPublication(hit *courtListenerSearchHit) Publication {
	// Pick the primary citation: prefer the court_citation_string (e.g.
	// "384 U.S. 436") when present, otherwise the first citation.
	citationCode := strings.TrimSpace(hit.CourtCitationString)
	if citationCode == "" && len(hit.Citation) > 0 {
		citationCode = strings.TrimSpace(hit.Citation[0])
	}

	rawID := strings.Trim(string(hit.ID), "\"")

	displayURL := hit.AbsoluteURL
	if displayURL != "" && strings.HasPrefix(displayURL, "/") {
		displayURL = "https://www.courtlistener.com" + displayURL
	}
	if displayURL == "" {
		displayURL = "https://www.courtlistener.com/opinion/" + rawID + "/"
	}

	meta := map[string]any{
		smetaLawCourt:        hit.Court,
		smetaLawDecisionDate: hit.DateFiled,
		smetaLawDocketNumber: hit.DocketNumber,
		smetaLawJurisdiction: "US",
	}
	if citationCode != "" {
		meta[MetaKeyCitationCode] = citationCode
		meta[smetaLawCitationCode] = citationCode
	}

	return Publication{
		ID:             courtListenerIDPrefix + rawID,
		Source:         SourceCourtListener,
		ContentType:    ContentTypePaper,
		Title:          strings.TrimSpace(hit.CaseName),
		Abstract:       stripXMLTags(hit.Snippet),
		URL:            displayURL,
		Published:      hit.DateFiled,
		Categories:     []string{hit.Court},
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *CourtListenerPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *CourtListenerPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
