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
// NewsAPI.org — v6 cycle 6 / v2.19.0.
//
// API: GET https://newsapi.org/v2/everything
//   Params:
//     q          free-text query
//     apiKey     required
//     pageSize   1..100
//     page       1-indexed
//     sortBy     "publishedAt" | "popularity" | "relevancy" (default)
//     language   ISO-639-1 short code (en, de, es, fr, ...)
//     from/to    YYYY-MM-DD bounds
//     domains    comma-joined hostname include list
//     excludeDomains  comma-joined hostname exclude list
//
// Response (subset):
//   { "status": "ok",
//     "totalResults": int,
//     "articles": [
//       { "source": {"id":"bbc-news","name":"BBC News"},
//         "author": "...",
//         "title": "...",
//         "description": "...",
//         "url": "...",
//         "urlToImage": "...",
//         "publishedAt": "2024-06-15T12:00:00Z",
//         "content": "..." } ] }
//
// Free dev tier (24h delay + dev domain only); paid plans unlock
// real-time + all domains. Per-call credential: `newsapi`. Refuses to
// start without a key.
// Residency: US (Newsapi.org, UK-incorporated but US-hosted CDN).
// ---------------------------------------------------------------------------

const (
	newsapiPluginID          = SourceNewsAPI
	newsapiPluginName        = "NewsAPI.org"
	newsapiPluginDescription = "Search NewsAPI.org for news articles across 80k+ sources. Free dev tier (24-hour delay + dev-domain-only); paid plans real-time. Returns title, source, author, snippet, URL, published date. Per-call credential: newsapi. Maps filters.language (ISO-639-1), date_from/date_to, include_domains/exclude_domains."

	newsapiDefaultBaseURL = "https://newsapi.org"
	newsapiSearchPath     = "/v2/everything"
	newsapiDefaultLimit   = 20
	newsapiMaxLimitCap    = 100
	newsapiDefaultRPS     = 1.0
	newsapiDefaultTimeout = 20 * time.Second

	newsapiIDPrefix = "newsapi:"

	newsapiParamQ              = "q"
	newsapiParamAPIKey         = "apiKey"
	newsapiParamPageSize       = "pageSize"
	newsapiParamPage           = "page"
	newsapiParamSortBy         = "sortBy"
	newsapiParamLanguage       = "language"
	newsapiParamFrom           = "from"
	newsapiParamTo             = "to"
	newsapiParamDomains        = "domains"
	newsapiParamExcludeDomains = "excludeDomains"

	newsapiSortPublishedAt = "publishedAt"
	newsapiSortRelevancy   = "relevancy"

	newsapiCategoriesHint = "NewsAPI.org has no per-category filter on /everything; use the /top-headlines endpoint instead (deferred). Pass include_domains/exclude_domains for source filtering."

	newsapiMetaKeySourceID   = "newsapi_source_id"
	newsapiMetaKeySourceName = "newsapi_source_name"
)

// ---------------------------------------------------------------------------
// NewsAPI wire types
// ---------------------------------------------------------------------------

type newsapiSearchResponse struct {
	Status       string           `json:"status,omitempty"`
	TotalResults int              `json:"totalResults,omitempty"`
	Code         string           `json:"code,omitempty"`
	Message      string           `json:"message,omitempty"`
	Articles     []newsapiArticle `json:"articles,omitempty"`
}

type newsapiArticle struct {
	Source      newsapiSource `json:"source,omitempty"`
	Author      string        `json:"author,omitempty"`
	Title       string        `json:"title,omitempty"`
	Description string        `json:"description,omitempty"`
	URL         string        `json:"url,omitempty"`
	URLToImage  string        `json:"urlToImage,omitempty"`
	PublishedAt string        `json:"publishedAt,omitempty"`
	Content     string        `json:"content,omitempty"`
}

type newsapiSource struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// ---------------------------------------------------------------------------
// NewsAPIPlugin
// ---------------------------------------------------------------------------

// NewsAPIPlugin implements SourcePlugin for the NewsAPI.org /everything
// endpoint. Thread-safe after Initialize.
type NewsAPIPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "newsapi".
func (p *NewsAPIPlugin) ID() string { return newsapiPluginID }

// Name returns the human-readable label.
func (p *NewsAPIPlugin) Name() string { return newsapiPluginName }

// Description returns the LLM-facing one-liner.
func (p *NewsAPIPlugin) Description() string { return newsapiPluginDescription }

// ContentTypes — paper (news lands on paper-family with KindNews).
func (p *NewsAPIPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *NewsAPIPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *NewsAPIPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports NewsAPI's filter/sort surface.
func (p *NewsAPIPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     true,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true,
		SupportsPagination:       true,
		MaxResultsPerQuery:       newsapiMaxLimitCap,
		CategoriesHint:           newsapiCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentNews, IntentQuickLookup},
		Kinds:                    []ResultKind{KindNews},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *NewsAPIPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = newsapiDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = newsapiDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = newsapiDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *NewsAPIPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a NewsAPI.org /v2/everything query.
func (p *NewsAPIPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = newsapiDefaultLimit
	}
	if limit > newsapiMaxLimitCap {
		limit = newsapiMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceNewsAPI, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: newsapi requires an API key", ErrCredentialRequired)
	}

	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Articles))
	for i := range resp.Articles {
		pubs = append(pubs, newsapiArticleToPublication(&resp.Articles[i]))
	}
	return &SearchResult{
		Total:   resp.TotalResults,
		Results: pubs,
		HasMore: resp.TotalResults > params.Offset+len(pubs),
	}, nil
}

// Get is not wired in cycle 6.
func (p *NewsAPIPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: newsapi Get is not wired in cycle 6", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *NewsAPIPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*newsapiSearchResponse, error) {
	q := url.Values{}
	q.Set(newsapiParamQ, params.Query)
	q.Set(newsapiParamAPIKey, apiKey)
	q.Set(newsapiParamPageSize, strconv.Itoa(limit))

	if params.Offset > 0 && limit > 0 && params.Offset%limit == 0 {
		q.Set(newsapiParamPage, strconv.Itoa(params.Offset/limit+1))
	}

	switch params.Sort {
	case SortDateDesc, SortDateAsc:
		q.Set(newsapiParamSortBy, newsapiSortPublishedAt)
	default:
		q.Set(newsapiParamSortBy, newsapiSortRelevancy)
	}

	if lang := strings.TrimSpace(params.Filters.Language); lang != "" {
		// NewsAPI wants the bare ISO-639-1 short code (en, de, ...).
		// Strip a region tag if the caller passed BCP-47 (en-US → en).
		if idx := strings.Index(lang, "-"); idx > 0 {
			lang = lang[:idx]
		}
		q.Set(newsapiParamLanguage, strings.ToLower(lang))
	}
	if d := strings.TrimSpace(params.Filters.DateFrom); d != "" {
		q.Set(newsapiParamFrom, normalizeDateYYYYMMDDHyphen(d, true))
	}
	if d := strings.TrimSpace(params.Filters.DateTo); d != "" {
		q.Set(newsapiParamTo, normalizeDateYYYYMMDDHyphen(d, false))
	}
	if len(params.Filters.IncludeDomains) > 0 {
		q.Set(newsapiParamDomains, strings.Join(params.Filters.IncludeDomains, ","))
	}
	if len(params.Filters.ExcludeDomains) > 0 {
		q.Set(newsapiParamExcludeDomains, strings.Join(params.Filters.ExcludeDomains, ","))
	}

	reqURL := p.baseURL + newsapiSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("newsapi: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("newsapi: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: newsapi", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: newsapi", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("newsapi: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp newsapiSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("newsapi: decode response: %w", err)
	}
	// NewsAPI returns 200 OK with a body-level error envelope when the
	// query is malformed — surface it as a structured error.
	if resp.Status == "error" {
		return nil, fmt.Errorf("newsapi: api error code=%s msg=%s", resp.Code, resp.Message)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func newsapiArticleToPublication(a *newsapiArticle) Publication {
	published := a.PublishedAt
	if len(published) >= 10 {
		published = published[:10]
	}

	authors := []Author{}
	if a.Author != "" {
		authors = append(authors, Author{Name: a.Author})
	}

	abstract := strings.TrimSpace(a.Description)
	if abstract == "" {
		abstract = strings.TrimSpace(a.Content)
	}

	meta := map[string]any{}
	if a.Source.ID != "" {
		meta[newsapiMetaKeySourceID] = a.Source.ID
	}
	if a.Source.Name != "" {
		meta[newsapiMetaKeySourceName] = a.Source.Name
	}

	return Publication{
		ID:             newsapiIDPrefix + hashURL(a.URL),
		Source:         SourceNewsAPI,
		ContentType:    ContentTypePaper,
		Title:          strings.TrimSpace(a.Title),
		Abstract:       abstract,
		URL:            a.URL,
		Published:      published,
		Authors:        authors,
		ThumbnailURL:   a.URLToImage,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *NewsAPIPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *NewsAPIPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
