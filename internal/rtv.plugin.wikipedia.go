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
// Wikipedia encyclopedia provider — Cycle 2 Wave-1 (keyless / free).
//
// Search API:  https://<lang>.wikipedia.org/w/api.php?action=query&list=search&...
// Get API:     https://<lang>.wikipedia.org/api/rest_v1/page/summary/<title>
//
// No auth required, but Wikimedia REQUIRES a polite User-Agent identifying
// the application + a contact email. We override the egress UA with the
// configured value (sources.wikipedia.user_agent in YAML) — failing to do
// so triggers HTTP 403 from the Wikimedia rate limiter.
//
// Residency: public-research-infrastructure (WMF, US — but the data is a
// public-good encyclopedia). Admitted under eu_strict only when the
// IncludePublicResearch flag is set.
// ---------------------------------------------------------------------------

// Identity / config constants.
const (
	wikipediaPluginID          = SourceWikipedia
	wikipediaPluginName        = "Wikipedia"
	wikipediaPluginDescription = "Encyclopedia article search via the public MediaWiki API. No auth required (polite User-Agent recommended)."

	wikipediaSearchPathFmt    = "https://%s.wikipedia.org/w/api.php"
	wikipediaSummaryPathFmt   = "https://%s.wikipedia.org/api/rest_v1/page/summary/%s"
	wikipediaUserAgentDefault = "retrievr/dev (+https://github.com/itsatony/retrievr-mcp)"

	wikipediaDefaultLang  = "en"
	wikipediaDefaultLimit = 10
	wikipediaMaxLimit     = 50
	wikipediaDefaultRPS   = 10.0

	wikipediaCategoriesHint = "encyclopedia articles; per-language via extra.lang (default en)"
)

// Extra-key constants.
const (
	wikipediaExtraLang      = "lang"       // ISO 639-1; default "en"
	wikipediaExtraUserAgent = "user_agent" // RFC 9110 polite UA with contact info
)

// ---------------------------------------------------------------------------
// Wikipedia wire types
// ---------------------------------------------------------------------------

type wikipediaSearchResponse struct {
	Query *wikipediaQuery `json:"query,omitempty"`
}

type wikipediaQuery struct {
	Search     []wikipediaSearchHit `json:"search"`
	SearchInfo *wikipediaSearchInfo `json:"searchinfo,omitempty"`
}

type wikipediaSearchInfo struct {
	TotalHits int `json:"totalhits,omitempty"`
}

type wikipediaSearchHit struct {
	NS        int    `json:"ns"`
	Title     string `json:"title"`
	PageID    int    `json:"pageid"`
	Snippet   string `json:"snippet"` // HTML; we strip tags
	Size      int    `json:"size"`
	WordCount int    `json:"wordcount"`
	Timestamp string `json:"timestamp"`
}

type wikipediaSummaryResponse struct {
	Type         string                `json:"type"`
	Title        string                `json:"title"`
	DisplayTitle string                `json:"displaytitle,omitempty"`
	Description  string                `json:"description,omitempty"`
	Extract      string                `json:"extract,omitempty"`
	Lang         string                `json:"lang,omitempty"`
	ContentURLs  *wikipediaContentURLs `json:"content_urls,omitempty"`
	Revision     string                `json:"revision,omitempty"`
}

type wikipediaContentURLs struct {
	Desktop *wikipediaURLPair `json:"desktop,omitempty"`
}

type wikipediaURLPair struct {
	Page string `json:"page,omitempty"`
}

// ---------------------------------------------------------------------------
// WikipediaPlugin
// ---------------------------------------------------------------------------

// WikipediaPlugin implements SourcePlugin for Wikipedia article search.
type WikipediaPlugin struct {
	lang       string
	userAgent  string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID / Name / Description.
func (p *WikipediaPlugin) ID() string                  { return wikipediaPluginID }
func (p *WikipediaPlugin) Name() string                { return wikipediaPluginName }
func (p *WikipediaPlugin) Description() string         { return wikipediaPluginDescription }
func (p *WikipediaPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypeAny} }
func (p *WikipediaPlugin) NativeFormat() ContentFormat { return FormatJSON }
func (p *WikipediaPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatMarkdown}
}

// Capabilities.
func (p *WikipediaPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         true, // via summary.extract
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       wikipediaMaxLimit,
		CategoriesHint:           wikipediaCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatMarkdown},
		QueryIntents:             []Intent{IntentReference, IntentQuickLookup},
		Kinds:                    []ResultKind{KindEncyclopedia},
	}
}

// Residency — public-research-infrastructure tier.
func (*WikipediaPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionPublicResearch,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize.
func (p *WikipediaPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = wikipediaDefaultRPS
	}
	p.lang = stringFromExtra(cfg.Extra, wikipediaExtraLang, wikipediaDefaultLang)
	p.userAgent = stringFromExtra(cfg.Extra, wikipediaExtraUserAgent, wikipediaUserAgentDefault)

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health.
func (p *WikipediaPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{Enabled: p.enabled, Healthy: p.healthy, RateLimit: p.rateLimit, LastError: p.lastError}
}

// Search executes a MediaWiki action=query&list=search call.
func (p *WikipediaPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = wikipediaDefaultLimit
	}
	if limit > wikipediaMaxLimit {
		limit = wikipediaMaxLimit
	}

	q := url.Values{}
	q.Set("action", "query")
	q.Set("list", "search")
	q.Set("srsearch", params.Query)
	q.Set("srlimit", fmt.Sprintf("%d", limit))
	q.Set("format", "json")
	q.Set("formatversion", "2")
	if params.Offset > 0 {
		q.Set("sroffset", fmt.Sprintf("%d", params.Offset))
	}
	reqURL := fmt.Sprintf(wikipediaSearchPathFmt, p.lang) + "?" + q.Encode()

	resp, err := p.fetchSearch(ctx, reqURL)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Query.Search))
	for _, hit := range resp.Query.Search {
		pubs = append(pubs, wikipediaHitToPublication(hit, p.lang))
	}

	total := len(pubs)
	if resp.Query.SearchInfo != nil {
		total = resp.Query.SearchInfo.TotalHits
	}
	return &SearchResult{Total: total, Results: pubs, HasMore: total > len(pubs)}, nil
}

// Get fetches the per-article summary endpoint.
func (p *WikipediaPlugin) Get(ctx context.Context, title string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	if title == "" {
		return nil, fmt.Errorf("%w: wikipedia Get requires a page title", ErrInvalidID)
	}
	encoded := url.PathEscape(strings.ReplaceAll(title, " ", "_"))
	reqURL := fmt.Sprintf(wikipediaSummaryPathFmt, p.lang, encoded)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("wikipedia: build summary request: %w", err)
	}
	p.applyHeaders(req)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wikipedia: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: wikipedia article %q", ErrSourceNotFound, title)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: wikipedia", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("wikipedia: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var summary wikipediaSummaryResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&summary); err != nil {
		return nil, fmt.Errorf("wikipedia: decode summary: %w", err)
	}
	pub := wikipediaSummaryToPublication(summary, p.lang)
	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (p *WikipediaPlugin) fetchSearch(ctx context.Context, reqURL string) (*wikipediaSearchResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("wikipedia: build request: %w", err)
	}
	p.applyHeaders(req)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wikipedia: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: wikipedia", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode == http.StatusForbidden {
		// Wikimedia returns 403 when a polite UA is missing — surface as
		// invalid credentials so operators notice and configure user_agent.
		return nil, fmt.Errorf("%w: wikipedia 403 (likely missing polite User-Agent)", ErrCredentialInvalid)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("wikipedia: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp wikipediaSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("wikipedia: decode response: %w", err)
	}
	if resp.Query == nil {
		return nil, fmt.Errorf("wikipedia: empty query block")
	}
	return &resp, nil
}

func (p *WikipediaPlugin) applyHeaders(req *http.Request) {
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}
	req.Header.Set("Accept", "application/json")
}

// ---------------------------------------------------------------------------
// Result mapping
// ---------------------------------------------------------------------------

func wikipediaHitToPublication(hit wikipediaSearchHit, lang string) Publication {
	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceWikipedia, strings.ReplaceAll(hit.Title, " ", "_")),
		Source:      SourceWikipedia,
		ContentType: ContentTypeAny,
		Title:       hit.Title,
		URL:         fmt.Sprintf("https://%s.wikipedia.org/wiki/%s", lang, url.PathEscape(strings.ReplaceAll(hit.Title, " ", "_"))),
		Updated:     hit.Timestamp[:cap10(hit.Timestamp)],
		Abstract:    stripHTMLTags(hit.Snippet),
	}
	meta := map[string]any{
		smetaSnippet:  truncateSnippet(stripHTMLTags(hit.Snippet)),
		smetaLanguage: lang,
		smetaDomain:   fmt.Sprintf("%s.wikipedia.org", lang),
	}
	pub.SourceMetadata = meta
	return pub
}

func wikipediaSummaryToPublication(s wikipediaSummaryResponse, lang string) Publication {
	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceWikipedia, strings.ReplaceAll(s.Title, " ", "_")),
		Source:      SourceWikipedia,
		ContentType: ContentTypeAny,
		Title:       s.Title,
		Abstract:    s.Extract,
	}
	if s.ContentURLs != nil && s.ContentURLs.Desktop != nil {
		pub.URL = s.ContentURLs.Desktop.Page
	}
	meta := map[string]any{
		smetaLanguage: lang,
		smetaArticle:  s.Extract,
		smetaRevision: s.Revision,
	}
	if s.Description != "" {
		meta[smetaSnippet] = s.Description
	} else if s.Extract != "" {
		meta[smetaSnippet] = truncateSnippet(s.Extract)
	}
	pub.SourceMetadata = meta
	return pub
}

// stripHTMLTags is a tiny tag stripper for Wikipedia search snippets which
// arrive wrapped in <span class="searchmatch">…</span> markup. We don't
// need a full HTML parser for this — angle-bracket pruning is enough.
func stripHTMLTags(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cap10 returns min(len(s), 10) — used to clip ISO-8601 timestamps to date.
func cap10(s string) int {
	if len(s) > 10 {
		return 10
	}
	return len(s)
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *WikipediaPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *WikipediaPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
