package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Brave Search web/news provider — Cycle 2 Wave-1.
//
// API: https://api.search.brave.com/res/v1/web/search
//   Method: GET
//   Headers: X-Subscription-Token: <key>, Accept: application/json
//   Query params: q, count, country, search_lang, ui_lang, freshness, ...
//   Response: { type, web: { results: [...] }, news: { results: [...] }, ... }
//
// Residency: US (Brave Software is US-based; index is global). Blocked under
// eu_strict; admissible in eu_preferred and off.
//
// Free tier soft cap: 1 req/s. Operators with a Pro subscription bump
// sources.brave.rate_limit.
// ---------------------------------------------------------------------------

// Identity / config constants.
const (
	bravePluginID          = SourceBrave
	bravePluginName        = "Brave Search"
	bravePluginDescription = "Independent 35B+ page web index from Brave Software. Strong privacy posture, fast (median <1s). US-resident; blocked under eu_strict."

	braveDefaultBaseURL  = "https://api.search.brave.com"
	braveSearchPath      = "/res/v1/web/search"
	braveAuthHeader      = "X-Subscription-Token"
	braveAcceptHeader    = "Accept"
	braveAcceptJSON      = "application/json"
	braveAcceptEncHeader = "Accept-Encoding"
	braveAcceptEncIdent  = "identity" // disable gzip — Go's http.Client decodes opaquely otherwise

	braveDefaultCount = 10
	braveMaxCount     = 20 // Brave caps web search at 20 per request
	braveDefaultRPS   = 1.0

	braveCategoriesHint = "general web, news, blogs (domain include/exclude via filters.include_domains)"
)

// Extra-key constants (PluginConfig.Extra).
const (
	braveExtraCountry    = "country"     // ISO 3166 alpha-2; default ALL
	braveExtraSearchLang = "search_lang" // ISO 639-1; default not set
	braveExtraSafesearch = "safesearch"  // off | moderate | strict
)

// ---------------------------------------------------------------------------
// Brave wire types
// ---------------------------------------------------------------------------

type braveSearchResponse struct {
	Type string             `json:"type"`
	Web  *braveWebSection   `json:"web,omitempty"`
	News *braveNewsSection  `json:"news,omitempty"`
}

type braveWebSection struct {
	Results []braveResult `json:"results"`
}

type braveNewsSection struct {
	Results []braveResult `json:"results"`
}

type braveResult struct {
	Type           string         `json:"type"`
	Subtype        string         `json:"subtype,omitempty"`
	Title          string         `json:"title"`
	URL            string         `json:"url"`
	Description    string         `json:"description,omitempty"`
	PageAge        string         `json:"page_age,omitempty"`
	Language       string         `json:"language,omitempty"`
	ExtraSnippets  []string       `json:"extra_snippets,omitempty"`
	MetaURL        *braveMetaURL  `json:"meta_url,omitempty"`
	Profile        *braveProfile  `json:"profile,omitempty"`
}

type braveMetaURL struct {
	Hostname string `json:"hostname,omitempty"`
	Favicon  string `json:"favicon,omitempty"`
	Path     string `json:"path,omitempty"`
}

type braveProfile struct {
	Name     string `json:"name,omitempty"`
	LongName string `json:"long_name,omitempty"`
	URL      string `json:"url,omitempty"`
}

// ---------------------------------------------------------------------------
// BravePlugin
// ---------------------------------------------------------------------------

// BravePlugin implements SourcePlugin for Brave Search.
// Thread-safe for concurrent use after Initialize.
type BravePlugin struct {
	baseURL        string
	apiKey         string
	defaultCountry string
	searchLang     string
	safesearch     string
	httpClient     *http.Client
	enabled        bool
	rateLimit      float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "brave".
func (p *BravePlugin) ID() string { return bravePluginID }

// Name returns the human-readable label.
func (p *BravePlugin) Name() string { return bravePluginName }

// Description returns a one-liner for LLM tool listing.
func (p *BravePlugin) Description() string { return bravePluginDescription }

// ContentTypes — Brave covers general web; mapped to ContentTypeAny.
func (p *BravePlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeAny}
}

// NativeFormat — JSON.
func (p *BravePlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *BravePlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatMarkdown}
}

// Capabilities reports Brave-specific filtering + sorting support.
func (p *BravePlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true, // via freshness (pd, pw, pm, py)
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true, // via offset+count
		MaxResultsPerQuery:       braveMaxCount,
		CategoriesHint:           braveCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatMarkdown},
		QueryIntents:             []Intent{IntentQuickLookup, IntentNews},
		Kinds:                    []ResultKind{KindWeb, KindNews},
	}
}

// Residency — US-resident.
func (*BravePlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig. Uses NewEgressClient
// (Hook #4) for outbound HTTP hygiene.
func (p *BravePlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = braveDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = braveDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.defaultCountry = stringFromExtra(cfg.Extra, braveExtraCountry, "")
	p.searchLang = stringFromExtra(cfg.Extra, braveExtraSearchLang, "")
	p.safesearch = stringFromExtra(cfg.Extra, braveExtraSafesearch, "moderate")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *BravePlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Brave web search. Credentials read from ctx.
func (p *BravePlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, bravePluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: brave requires an API key", ErrCredentialRequired)
	}

	count := params.Limit
	if count <= 0 {
		count = braveDefaultCount
	}
	if count > braveMaxCount {
		count = braveMaxCount
	}

	resp, err := p.doSearch(ctx, params.Query, count, params.Offset, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := p.mergeBraveSections(resp)
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: len(pubs) >= count, // Brave doesn't surface a total; assume more on full page
	}, nil
}

// Get is not supported — Brave Search has no per-result-ID retrieval API.
func (p *BravePlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: brave has no per-result Get API", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *BravePlugin) doSearch(ctx context.Context, query string, count, offset int, apiKey string) (*braveSearchResponse, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("count", fmt.Sprintf("%d", count))
	if offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", offset))
	}
	if p.defaultCountry != "" {
		q.Set("country", p.defaultCountry)
	}
	if p.searchLang != "" {
		q.Set("search_lang", p.searchLang)
	}
	if p.safesearch != "" {
		q.Set("safesearch", p.safesearch)
	}

	reqURL := p.baseURL + braveSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("brave: build request: %w", err)
	}
	req.Header.Set(braveAuthHeader, apiKey)
	req.Header.Set(braveAcceptHeader, braveAcceptJSON)
	req.Header.Set(braveAcceptEncHeader, braveAcceptEncIdent)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: brave returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: brave", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("brave: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp braveSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("brave: decode response: %w", err)
	}
	return &resp, nil
}

// mergeBraveSections collects web + news results into a single Publication
// slice, tagging news entries with smetaKindOverride so the converter
// produces Result.Kind=KindNews while web entries default to KindWeb (the
// first entry of Capabilities().Kinds).
func (p *BravePlugin) mergeBraveSections(resp *braveSearchResponse) []Publication {
	if resp == nil {
		return nil
	}
	var out []Publication
	if resp.Web != nil {
		for _, r := range resp.Web.Results {
			out = append(out, braveResultToPublication(r, KindWeb))
		}
	}
	if resp.News != nil {
		for _, r := range resp.News.Results {
			out = append(out, braveResultToPublication(r, KindNews))
		}
	}
	return out
}

// braveResultToPublication maps one Brave result into a Publication.
// Snippet, domain, language, page_age, favicon are stuffed into
// SourceMetadata for the converter to unpack into Result.Web / Result.News.
func braveResultToPublication(r braveResult, kind ResultKind) Publication {
	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceBrave, hashURL(r.URL)),
		Source:      SourceBrave,
		ContentType: ContentTypeAny,
		Title:       r.Title,
		Abstract:    r.Description,
		URL:         r.URL,
	}

	meta := map[string]any{}
	if string(kind) != string(KindWeb) {
		meta[smetaKindOverride] = string(kind)
	}
	if r.Description != "" {
		meta[smetaSnippet] = truncateSnippet(r.Description)
	}
	if r.Language != "" {
		meta[smetaLanguage] = r.Language
	}
	if r.PageAge != "" {
		meta[smetaPublishedAt] = r.PageAge
	}
	if r.MetaURL != nil {
		if r.MetaURL.Hostname != "" {
			meta[smetaDomain] = r.MetaURL.Hostname
			meta[smetaSiteName] = r.MetaURL.Hostname
		}
	}
	// extra_snippets concatenated — gives the LLM more context per result.
	if len(r.ExtraSnippets) > 0 {
		extras := strings.Join(r.ExtraSnippets, " · ")
		if pub.Abstract == "" {
			pub.Abstract = extras
		} else {
			pub.Abstract = pub.Abstract + "\n\n" + extras
		}
	}
	if len(meta) > 0 {
		pub.SourceMetadata = meta
	}
	return pub
}

// hashURL returns a short stable identifier for a URL — used as the per-
// result ID since Brave doesn't surface stable IDs of its own. We re-use
// the audit hashing helper (sha256:16) for consistency.
func hashURL(u string) string {
	if u == "" {
		return "noid"
	}
	return hashQuery(u)
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *BravePlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *BravePlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
