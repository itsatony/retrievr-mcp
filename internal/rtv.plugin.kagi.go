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
// Kagi Search — v6 cycle 4 / v2.17.0.
//
// API: GET https://kagi.com/api/v0/search?q=<q>&limit=10
//   Header: Authorization: Bot <token>
//
// Response:
//   { "meta": {"id":"...","node":"...","ms": int, "api_balance": float},
//     "data": [
//       { "t": 0,             // type: 0 = SearchResult
//         "rank": int,
//         "url": "...",
//         "title": "...",
//         "snippet": "...",
//         "published": "...",  // ISO 8601 when known
//         "thumbnail": {"url":"..."} },
//       { "t": 1, "list": [...] }  // related searches, skipped
//     ] }
//
// Paid; ~$0.025/search above the $25/mo plan floor. Per-call credential:
// `kagi`. Refuses to start without a key.
// Residency: US (Kagi LLC).
// ---------------------------------------------------------------------------

const (
	kagiPluginID          = SourceKagi
	kagiPluginName        = "Kagi Search"
	kagiPluginDescription = "Search Kagi (ad-free premium web search). Paid; $25/mo + $0.025/search. Returns ranked organic results with snippet, published date, thumbnail when available. Per-call credential: kagi. US-resident (Kagi LLC)."

	kagiDefaultBaseURL = "https://kagi.com"
	kagiSearchPath     = "/api/v0/search"
	kagiDefaultLimit   = 10
	kagiMaxLimitCap    = 25
	kagiDefaultRPS     = 1.0
	kagiDefaultTimeout = 20 * time.Second

	kagiIDPrefix         = "kagi:"
	kagiHeaderAuth       = "Authorization"
	kagiBotPrefix        = "Bot "
	kagiParamQ           = "q"
	kagiParamLimit       = "limit"
	kagiTypeSearchResult = 0

	kagiCategoriesHint = "Kagi has no native category filter on the API surface; pass refinement tokens (\"site:\", \"-site:\") inline in the query."
)

// ---------------------------------------------------------------------------
// Kagi wire types
// ---------------------------------------------------------------------------

type kagiSearchResponse struct {
	Meta kagiMeta    `json:"meta,omitempty"`
	Data []kagiDatum `json:"data,omitempty"`
}

type kagiMeta struct {
	ID         string  `json:"id,omitempty"`
	APIBalance float64 `json:"api_balance,omitempty"`
}

type kagiDatum struct {
	T         int            `json:"t,omitempty"`
	Rank      int            `json:"rank,omitempty"`
	URL       string         `json:"url,omitempty"`
	Title     string         `json:"title,omitempty"`
	Snippet   string         `json:"snippet,omitempty"`
	Published string         `json:"published,omitempty"`
	Thumbnail *kagiThumbnail `json:"thumbnail,omitempty"`
}

type kagiThumbnail struct {
	URL string `json:"url,omitempty"`
}

// ---------------------------------------------------------------------------
// KagiPlugin
// ---------------------------------------------------------------------------

// KagiPlugin implements SourcePlugin for the Kagi public-search API.
// Thread-safe after Initialize.
type KagiPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "kagi".
func (p *KagiPlugin) ID() string { return kagiPluginID }

// Name returns the human-readable label.
func (p *KagiPlugin) Name() string { return kagiPluginName }

// Description returns the LLM-facing one-liner.
func (p *KagiPlugin) Description() string { return kagiPluginDescription }

// ContentTypes — paper (web hits land on paper-family with KindWeb at
// the v2 layer, matching the existing brave/exa plugins).
func (p *KagiPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *KagiPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *KagiPlugin) AvailableFormats() []ContentFormat { return []ContentFormat{FormatJSON} }

// Capabilities reports Kagi's filter/sort surface.
func (p *KagiPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     true, // via inline site: / -site: tokens
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       false,
		MaxResultsPerQuery:       kagiMaxLimitCap,
		CategoriesHint:           kagiCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentDeepResearch, IntentNews},
		Kinds:                    []ResultKind{KindWeb},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *KagiPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = kagiDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = kagiDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = kagiDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *KagiPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Kagi /api/v0/search query.
func (p *KagiPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = kagiDefaultLimit
	}
	if limit > kagiMaxLimitCap {
		limit = kagiMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceKagi, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: kagi requires an API key", ErrCredentialRequired)
	}

	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Data))
	for i := range resp.Data {
		if resp.Data[i].T != kagiTypeSearchResult {
			continue
		}
		pubs = append(pubs, kagiDatumToPublication(&resp.Data[i]))
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 4.
func (p *KagiPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: kagi Get is not wired in cycle 4", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *KagiPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*kagiSearchResponse, error) {
	q := url.Values{}
	q.Set(kagiParamQ, kagiBuildQuery(params))
	q.Set(kagiParamLimit, strconv.Itoa(limit))

	reqURL := p.baseURL + kagiSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("kagi: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(kagiHeaderAuth, kagiBotPrefix+apiKey)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kagi: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: kagi", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: kagi", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("kagi: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp kagiSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("kagi: decode response: %w", err)
	}
	return &resp, nil
}

// kagiBuildQuery folds free-text + include/exclude-domains into Kagi's
// q parameter. Kagi accepts Google-style site: / -site: tokens.
func kagiBuildQuery(params SearchParams) string {
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

func kagiDatumToPublication(d *kagiDatum) Publication {
	thumb := ""
	if d.Thumbnail != nil {
		thumb = d.Thumbnail.URL
	}

	published := d.Published
	if len(published) >= 10 {
		published = published[:10]
	}

	return Publication{
		ID:           kagiIDPrefix + hashURL(d.URL),
		Source:       SourceKagi,
		ContentType:  ContentTypePaper,
		Title:        strings.TrimSpace(d.Title),
		Abstract:     stripXMLTags(d.Snippet),
		URL:          d.URL,
		Published:    published,
		ThumbnailURL: thumb,
		SourceMetadata: map[string]any{
			"kagi_rank": d.Rank,
		},
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *KagiPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *KagiPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = sanitizeHealthError(err)
	}
}
