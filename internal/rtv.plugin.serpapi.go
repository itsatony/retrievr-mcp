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
// SerpAPI (Google web engine) — v6 cycle 4 / v2.17.0.
//
// API: GET https://serpapi.com/search.json
//   Params:
//     engine    "google" (this plugin) — the same SerpAPI key powers
//               additional engines exercised by the cycle-6 news plugin.
//     q         free-text query
//     api_key   required
//     num       1..100 (results per page)
//     hl        language (BCP-47)
//     gl        geo / country
//
// Response (subset):
//   { "organic_results": [
//       { "position": int,
//         "title": "...",
//         "link": "...",
//         "displayed_link": "...",
//         "snippet": "...",
//         "date": "..." } ],
//     "search_information": {"total_results": int} }
//
// Paid; $50/mo dev = 5k searches. Per-call credential: `serpapi`.
// Refuses to start without a key.
// Residency: US (SerpAPI Inc.).
// ---------------------------------------------------------------------------

const (
	serpapiPluginID          = SourceSerpAPI
	serpapiPluginName        = "SerpAPI (Google)"
	serpapiPluginDescription = "Search Google via SerpAPI (serpapi.com). Paid; same per-call credential covers Google + Google News engines (cycle 6 reuses this key). Returns organic_results with position, title, link, snippet, displayed_link, date."

	serpapiDefaultBaseURL = "https://serpapi.com"
	serpapiSearchPath     = "/search.json"
	serpapiDefaultLimit   = 10
	serpapiMaxLimitCap    = 100
	serpapiDefaultRPS     = 2.0
	serpapiDefaultTimeout = 20 * time.Second

	serpapiIDPrefix = "serpapi:"

	serpapiParamEngine = "engine"
	serpapiParamQ      = "q"
	serpapiParamAPIKey = "api_key"
	serpapiParamNum    = "num"
	serpapiParamHL     = "hl"
	serpapiParamGL     = "gl"

	serpapiEngineGoogle = "google"

	serpapiCategoriesHint = "SerpAPI Google has no per-category filter; filters.include_domains/exclude_domains map to site:/-site: tokens in q. Pass `gl` (country) via filters.categories[0] when needed."
)

// ---------------------------------------------------------------------------
// SerpAPI wire types
// ---------------------------------------------------------------------------

type serpapiSearchResponse struct {
	OrganicResults    []serpapiOrganicResult `json:"organic_results,omitempty"`
	SearchInformation serpapiSearchInfo      `json:"search_information,omitempty"`
}

type serpapiOrganicResult struct {
	Position      int    `json:"position,omitempty"`
	Title         string `json:"title,omitempty"`
	Link          string `json:"link,omitempty"`
	DisplayedLink string `json:"displayed_link,omitempty"`
	Snippet       string `json:"snippet,omitempty"`
	Date          string `json:"date,omitempty"`
	Source        string `json:"source,omitempty"`
}

type serpapiSearchInfo struct {
	TotalResults int64 `json:"total_results,omitempty"`
}

// ---------------------------------------------------------------------------
// SerpAPIPlugin
// ---------------------------------------------------------------------------

// SerpAPIPlugin implements SourcePlugin for the SerpAPI Google web
// engine. Thread-safe after Initialize.
type SerpAPIPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "serpapi".
func (p *SerpAPIPlugin) ID() string { return serpapiPluginID }

// Name returns the human-readable label.
func (p *SerpAPIPlugin) Name() string { return serpapiPluginName }

// Description returns the LLM-facing one-liner.
func (p *SerpAPIPlugin) Description() string { return serpapiPluginDescription }

// ContentTypes — paper (web).
func (p *SerpAPIPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *SerpAPIPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *SerpAPIPlugin) AvailableFormats() []ContentFormat { return []ContentFormat{FormatJSON} }

// Capabilities reports SerpAPI Google's filter/sort surface.
func (p *SerpAPIPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true, // mapped to gl (country)
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     true,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true,
		SupportsPagination:       false,
		MaxResultsPerQuery:       serpapiMaxLimitCap,
		CategoriesHint:           serpapiCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentDeepResearch, IntentNews},
		Kinds:                    []ResultKind{KindWeb},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *SerpAPIPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = serpapiDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = serpapiDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = serpapiDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *SerpAPIPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a SerpAPI Google search.
func (p *SerpAPIPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = serpapiDefaultLimit
	}
	if limit > serpapiMaxLimitCap {
		limit = serpapiMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceSerpAPI, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: serpapi requires an API key", ErrCredentialRequired)
	}

	resp, err := p.doSearch(ctx, params, limit, apiKey, serpapiEngineGoogle)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.OrganicResults))
	for i := range resp.OrganicResults {
		pubs = append(pubs, serpapiOrganicToPublication(&resp.OrganicResults[i], SourceSerpAPI, serpapiIDPrefix))
	}
	return &SearchResult{
		Total:   int(resp.SearchInformation.TotalResults),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 4.
func (p *SerpAPIPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: serpapi Get is not wired in cycle 4", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

// doSearch is shared between the Google web engine (cycle 4) and the
// Google News engine (cycle 6) — both ride the same /search.json
// endpoint with different `engine=` values.
func (p *SerpAPIPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey, engine string) (*serpapiSearchResponse, error) {
	q := url.Values{}
	q.Set(serpapiParamEngine, engine)
	q.Set(serpapiParamQ, serpapiBuildQuery(params))
	q.Set(serpapiParamAPIKey, apiKey)
	q.Set(serpapiParamNum, strconv.Itoa(limit))

	if lang := strings.TrimSpace(params.Filters.Language); lang != "" {
		q.Set(serpapiParamHL, lang)
	}
	if len(params.Filters.Categories) > 0 {
		if gl := strings.TrimSpace(params.Filters.Categories[0]); gl != "" {
			q.Set(serpapiParamGL, strings.ToLower(gl))
		}
	}

	reqURL := p.baseURL + serpapiSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("serpapi: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("serpapi: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: serpapi", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: serpapi", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("serpapi: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp serpapiSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("serpapi: decode response: %w", err)
	}
	return &resp, nil
}

// serpapiBuildQuery folds free-text + include/exclude-domains into the
// q parameter using Google's site:/-site: operators.
func serpapiBuildQuery(params SearchParams) string {
	parts := []string{}
	if q := strings.TrimSpace(params.Query); q != "" {
		parts = append(parts, q)
	}
	for _, d := range params.Filters.IncludeDomains {
		if v := strings.TrimSpace(d); v != "" {
			parts = append(parts, "site:"+v)
		}
	}
	for _, d := range params.Filters.ExcludeDomains {
		if v := strings.TrimSpace(d); v != "" {
			parts = append(parts, "-site:"+v)
		}
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

// serpapiOrganicToPublication is shared with the Google News engine
// plugin in cycle 6 — both engines emit `organic_results` with the
// same shape. Source + prefix are parameterized so the same wire-type
// stays useful across engines.
func serpapiOrganicToPublication(r *serpapiOrganicResult, source, idPrefix string) Publication {
	return Publication{
		ID:          idPrefix + hashURL(r.Link),
		Source:      source,
		ContentType: ContentTypePaper,
		Title:       strings.TrimSpace(r.Title),
		Abstract:    stripXMLTags(r.Snippet),
		URL:         r.Link,
		Published:   r.Date,
		SourceMetadata: map[string]any{
			"serpapi_position":       r.Position,
			"serpapi_displayed_link": r.DisplayedLink,
			"serpapi_source":         r.Source,
		},
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *SerpAPIPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *SerpAPIPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
