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
// Mojeek Search — v6 cycle 4 / v2.17.0.
//
// API: GET https://api.mojeek.com/search
//   Params:
//     q         free-text query
//     api_key   required
//     fmt       "json"
//     t         results per page (1..50)
//     s         start index (0-indexed)
//     lang      ISO 639-1 language hint
//
// Response:
//   { "response": {
//       "head": {"query":"...","results_count":int,"search_time":float},
//       "results": [
//         { "title":"...","url":"...","desc":"...","pubdate":"YYYY-MM-DD" } ] } }
//
// Free dev tier (~1k/mo); paid above. Mojeek runs its own index (UK
// adequacy). Per-call credential: `mojeek`. Refuses to start without
// a key.
// Residency: UK adequacy (Mojeek Ltd, Crawley UK).
// ---------------------------------------------------------------------------

const (
	mojeekPluginID          = SourceMojeek
	mojeekPluginName        = "Mojeek"
	mojeekPluginDescription = "Search Mojeek (mojeek.com) — independent UK-based web index. Free dev tier (~1k/mo); paid above. UK-adequacy residency. Per-call credential: mojeek."

	mojeekDefaultBaseURL = "https://api.mojeek.com"
	mojeekSearchPath     = "/search"
	mojeekDefaultLimit   = 10
	mojeekMaxLimitCap    = 50
	mojeekDefaultRPS     = 2.0
	mojeekDefaultTimeout = 15 * time.Second

	mojeekIDPrefix = "mojeek:"

	mojeekParamQ      = "q"
	mojeekParamAPIKey = "api_key"
	mojeekParamFmt    = "fmt"
	mojeekParamT      = "t"
	mojeekParamS      = "s"
	mojeekParamLang   = "lang"
	mojeekFmtJSON     = "json"

	mojeekCategoriesHint = "Mojeek has no native category filter. Use filters.include_domains/exclude_domains (mapped to site:/-site: tokens in q)."
)

// ---------------------------------------------------------------------------
// Mojeek wire types
// ---------------------------------------------------------------------------

type mojeekSearchResponse struct {
	Response mojeekResponse `json:"response"`
}

type mojeekResponse struct {
	Head    mojeekHead     `json:"head,omitempty"`
	Results []mojeekResult `json:"results,omitempty"`
}

type mojeekHead struct {
	Query        string  `json:"query,omitempty"`
	ResultsCount int     `json:"results_count,omitempty"`
	SearchTime   float64 `json:"search_time,omitempty"`
}

type mojeekResult struct {
	Title   string `json:"title,omitempty"`
	URL     string `json:"url,omitempty"`
	Desc    string `json:"desc,omitempty"`
	PubDate string `json:"pubdate,omitempty"`
}

// ---------------------------------------------------------------------------
// MojeekPlugin
// ---------------------------------------------------------------------------

// MojeekPlugin implements SourcePlugin for the Mojeek search API.
// Thread-safe after Initialize.
type MojeekPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "mojeek".
func (p *MojeekPlugin) ID() string { return mojeekPluginID }

// Name returns the human-readable label.
func (p *MojeekPlugin) Name() string { return mojeekPluginName }

// Description returns the LLM-facing one-liner.
func (p *MojeekPlugin) Description() string { return mojeekPluginDescription }

// ContentTypes — paper (web).
func (p *MojeekPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *MojeekPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *MojeekPlugin) AvailableFormats() []ContentFormat { return []ContentFormat{FormatJSON} }

// Capabilities reports Mojeek's filter/sort surface.
func (p *MojeekPlugin) Capabilities() SourceCapabilities {
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
		SupportsDomainFilter:     true,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true,
		SupportsPagination:       true,
		MaxResultsPerQuery:       mojeekMaxLimitCap,
		CategoriesHint:           mojeekCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentDeepResearch},
		Kinds:                    []ResultKind{KindWeb},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *MojeekPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = mojeekDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = mojeekDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = mojeekDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *MojeekPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Mojeek /search query.
func (p *MojeekPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = mojeekDefaultLimit
	}
	if limit > mojeekMaxLimitCap {
		limit = mojeekMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceMojeek, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: mojeek requires an API key", ErrCredentialRequired)
	}

	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Response.Results))
	for i := range resp.Response.Results {
		pubs = append(pubs, mojeekResultToPublication(&resp.Response.Results[i]))
	}
	return &SearchResult{
		Total:   resp.Response.Head.ResultsCount,
		Results: pubs,
		HasMore: resp.Response.Head.ResultsCount > params.Offset+len(pubs),
	}, nil
}

// Get is not wired in cycle 4.
func (p *MojeekPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: mojeek Get is not wired in cycle 4", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *MojeekPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*mojeekSearchResponse, error) {
	q := url.Values{}
	q.Set(mojeekParamQ, mojeekBuildQuery(params))
	q.Set(mojeekParamAPIKey, apiKey)
	q.Set(mojeekParamFmt, mojeekFmtJSON)
	q.Set(mojeekParamT, strconv.Itoa(limit))
	if params.Offset > 0 {
		q.Set(mojeekParamS, strconv.Itoa(params.Offset))
	}
	if lang := strings.TrimSpace(params.Filters.Language); lang != "" {
		q.Set(mojeekParamLang, lang)
	}

	reqURL := p.baseURL + mojeekSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mojeek: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mojeek: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: mojeek", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: mojeek", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("mojeek: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp mojeekSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("mojeek: decode response: %w", err)
	}
	return &resp, nil
}

// mojeekBuildQuery folds the free-text query with include/exclude
// domain filters into the q= param using site:/-site: tokens.
func mojeekBuildQuery(params SearchParams) string {
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

func mojeekResultToPublication(r *mojeekResult) Publication {
	return Publication{
		ID:          mojeekIDPrefix + hashURL(r.URL),
		Source:      SourceMojeek,
		ContentType: ContentTypePaper,
		Title:       strings.TrimSpace(r.Title),
		Abstract:    stripXMLTags(r.Desc),
		URL:         r.URL,
		Published:   r.PubDate,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *MojeekPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *MojeekPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
