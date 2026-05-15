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
// Hacker News (Algolia mirror) provider — v5 cycle 1 / v2.8.0.
//
// API: GET https://hn.algolia.com/api/v1/search          (relevance-ranked)
//      GET https://hn.algolia.com/api/v1/search_by_date  (date-ranked)
//   Params:
//     query           free-text query
//     tags            HN object-type filter: "story" | "comment" | "poll"
//                     | "show_hn" | "ask_hn" | "front_page". This plugin
//                     defaults to "story" — comments are followups, not
//                     standalone Q&A.
//     numericFilters  comma-joined predicates over numeric fields, e.g.
//                     "created_at_i>1700000000,points>10". We use this
//                     for SearchFilters.DateFrom / DateTo.
//     hitsPerPage     1..1000 (the Algolia hard cap)
//     page            0-indexed pagination (unused in cycle 1)
//
// Response shape:
//   { "hits": [ { "objectID", "title", "url", "author", "points",
//                 "num_comments", "story_text", "comment_text",
//                 "created_at", "created_at_i", "_tags": [...],
//                 "story_id": int } ],
//     "nbHits": int, "page": int, "hitsPerPage": int }
//
// Free, anonymous, no auth, no key. Algolia front-end is the same backend
// the news.ycombinator.com search box uses, so coverage matches the
// official site. Honor 429 (rare) the same way other providers do.
//
// Residency: US-hosted (Y Combinator + Algolia). Blocked under eu_strict;
// admissible under eu_preferred or default deployments.
// ---------------------------------------------------------------------------

const (
	hackerNewsPluginID          = SourceHackerNews
	hackerNewsPluginName        = "Hacker News"
	hackerNewsPluginDescription = "Search Hacker News via the public Algolia-hosted mirror (hn.algolia.com). Free, no auth required, covers the full HN history. Returns stories by default; filters.date_from/date_to honoured via numericFilters created_at_i. Date-sort is supported."

	hackerNewsDefaultBaseURL    = "https://hn.algolia.com"
	hackerNewsSearchPath        = "/api/v1/search"
	hackerNewsSearchByDatePath  = "/api/v1/search_by_date"
	hackerNewsDefaultLimit      = 25
	hackerNewsMaxLimitCap       = 1000 // Algolia hard cap; we don't go this high in practice
	hackerNewsDefaultRPS        = 5.0  // Algolia is generous; 5 rps is safe
	hackerNewsDefaultTimeout    = 10 * time.Second
	hackerNewsStoryTagDefault   = "story"
	hackerNewsSiteForDedup      = "hackernews"
	hackerNewsIDPrefix          = "hackernews:"
	hackerNewsItemURLTemplate   = "https://news.ycombinator.com/item?id=%d"
	hackerNewsUserURLTemplate   = "https://news.ycombinator.com/user?id=%s"
	hackerNewsAcceptHeader      = "Accept"
	hackerNewsAcceptJSON        = "application/json"
	hackerNewsAuthorTagPrefix   = "author_"
	hackerNewsCategoriesHint    = "Hacker News stories via Algolia mirror; filters.date_from/date_to map to numericFilters=created_at_i predicates. No category/language filter — HN is English-only by community convention."
	hackerNewsNumericFilterJoin = ","

	hackerNewsQueryParamQuery          = "query"
	hackerNewsQueryParamTags           = "tags"
	hackerNewsQueryParamHitsPerPage    = "hitsPerPage"
	hackerNewsQueryParamNumericFilters = "numericFilters"
)

// ---------------------------------------------------------------------------
// Hacker News wire types
// ---------------------------------------------------------------------------

type hackerNewsSearchResponse struct {
	Hits        []hackerNewsHit `json:"hits,omitempty"`
	NbHits      int             `json:"nbHits,omitempty"`
	Page        int             `json:"page,omitempty"`
	HitsPerPage int             `json:"hitsPerPage,omitempty"`
}

type hackerNewsHit struct {
	ObjectID    string   `json:"objectID"`
	Title       string   `json:"title,omitempty"`
	URL         string   `json:"url,omitempty"`
	Author      string   `json:"author,omitempty"`
	Points      *int     `json:"points,omitempty"`
	NumComments *int     `json:"num_comments,omitempty"`
	StoryText   string   `json:"story_text,omitempty"`
	CommentText string   `json:"comment_text,omitempty"`
	CreatedAtI  int64    `json:"created_at_i,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	Tags        []string `json:"_tags,omitempty"`
	StoryID     *int64   `json:"story_id,omitempty"`
}

// ---------------------------------------------------------------------------
// HackerNewsPlugin
// ---------------------------------------------------------------------------

// HackerNewsPlugin implements SourcePlugin for the Hacker News Algolia
// mirror. Thread-safe after Initialize.
type HackerNewsPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "hackernews".
func (p *HackerNewsPlugin) ID() string { return hackerNewsPluginID }

// Name returns the human-readable label.
func (p *HackerNewsPlugin) Name() string { return hackerNewsPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *HackerNewsPlugin) Description() string { return hackerNewsPluginDescription }

// ContentTypes — HN emits paper (with KindQA discriminator).
func (p *HackerNewsPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat — JSON.
func (p *HackerNewsPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *HackerNewsPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports HN's filter/sort surface.
func (p *HackerNewsPlugin) Capabilities() SourceCapabilities {
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
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       false,
		MaxResultsPerQuery:       hackerNewsMaxLimitCap,
		CategoriesHint:           hackerNewsCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentNews, IntentCodeProvenance},
		Kinds:                    []ResultKind{KindQA},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *HackerNewsPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = hackerNewsDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = hackerNewsDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = hackerNewsDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *HackerNewsPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Hacker News search.
func (p *HackerNewsPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = hackerNewsDefaultLimit
	}
	if limit > hackerNewsMaxLimitCap {
		limit = hackerNewsMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		pubs = append(pubs, hackerNewsHitToPublication(hit))
	}
	return &SearchResult{
		Total:   resp.NbHits,
		Results: pubs,
		HasMore: resp.NbHits > len(pubs),
	}, nil
}

// Get is not wired in cycle 1.
func (p *HackerNewsPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: hackernews Get is not wired in cycle 1", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *HackerNewsPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*hackerNewsSearchResponse, error) {
	q := url.Values{}
	q.Set(hackerNewsQueryParamQuery, params.Query)
	q.Set(hackerNewsQueryParamTags, hackerNewsStoryTagDefault)
	q.Set(hackerNewsQueryParamHitsPerPage, strconv.Itoa(limit))

	var filters []string
	if from, ok := stackExchangeUnixFromDate(params.Filters.DateFrom); ok {
		filters = append(filters, "created_at_i>="+strconv.FormatInt(from, 10))
	}
	if to, ok := stackExchangeUnixFromDate(params.Filters.DateTo); ok {
		filters = append(filters, "created_at_i<="+strconv.FormatInt(to, 10))
	}
	if len(filters) > 0 {
		q.Set(hackerNewsQueryParamNumericFilters, strings.Join(filters, hackerNewsNumericFilterJoin))
	}

	path := hackerNewsSearchPath
	switch params.Sort {
	case SortDateDesc, SortDateAsc:
		// Algolia's date endpoint is a single descending-sort variant; HN
		// has no ascending-date endpoint. We use search_by_date for both
		// directions and rely on the upstream desc order — SortDateAsc
		// would require client-side reversal, which is out of scope here.
		path = hackerNewsSearchByDatePath
	}

	reqURL := p.baseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("hackernews: build request: %w", err)
	}
	req.Header.Set(hackerNewsAcceptHeader, hackerNewsAcceptJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hackernews: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: hackernews", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("hackernews: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp hackerNewsSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("hackernews: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func hackerNewsHitToPublication(hit hackerNewsHit) Publication {
	rawID := hit.ObjectID
	dedupKey := hackerNewsSiteForDedup + ":" + rawID

	title := hit.Title
	if title == "" {
		// Comments/polls/etc — synthesize a title from the body text.
		body := firstNonEmpty(hit.StoryText, hit.CommentText)
		title = truncateSnippet(body)
	}
	abstract := stackExchangeStripHTML(firstNonEmpty(hit.StoryText, hit.CommentText))

	// External URL (the story's outbound link) lives on hit.URL when
	// present; otherwise the canonical HN item page is the URL.
	itemID, _ := strconv.ParseInt(rawID, 10, 64)
	platformURL := fmt.Sprintf(hackerNewsItemURLTemplate, itemID)
	displayURL := hit.URL
	if displayURL == "" {
		displayURL = platformURL
	}

	authors := []Author{}
	if hit.Author != "" {
		authors = append(authors, Author{Name: hit.Author})
	}

	tags := filterAuthorTags(hit.Tags)

	score := 0
	if hit.Points != nil {
		score = *hit.Points
	}
	answerCount := 0
	if hit.NumComments != nil {
		answerCount = *hit.NumComments
	}

	meta := map[string]any{
		MetaKeyQAQuestionID:  dedupKey,
		smetaQASite:          hackerNewsSiteForDedup,
		smetaQARawQuestionID: rawID,
		smetaQATags:          tags,
		smetaQAAnswerCount:   answerCount,
		smetaQAScore:         score,
		smetaAuthorHandle:    hit.Author,
		smetaPlatformURL:     platformURL,
	}
	if hit.URL != "" {
		meta[smetaExternalURL] = hit.URL
	}
	if hit.Author != "" {
		meta[smetaAuthorURL] = fmt.Sprintf(hackerNewsUserURLTemplate, hit.Author)
	}
	if hit.CreatedAt != "" {
		meta[smetaPublishedAt] = hit.CreatedAt
	}

	return Publication{
		ID:             hackerNewsIDPrefix + rawID,
		Source:         SourceHackerNews,
		ContentType:    ContentTypePaper,
		Title:          title,
		Abstract:       abstract,
		URL:            displayURL,
		Published:      hackerNewsShortDate(hit.CreatedAtI, hit.CreatedAt),
		Authors:        authors,
		Categories:     tags,
		SourceMetadata: meta,
	}
}

// filterAuthorTags strips Algolia's bookkeeping tags. The _tags array
// includes the object type ("story", "comment", ...) plus an
// "author_<handle>" entry per hit. The object type stays in tags so
// consumers can filter (e.g. show_hn vs story); the author tag is noise.
func filterAuthorTags(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		if strings.HasPrefix(t, hackerNewsAuthorTagPrefix) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// hackerNewsShortDate prefers the unix epoch when present, falling back
// to parsing the RFC3339 created_at string.
func hackerNewsShortDate(unix int64, rfc string) string {
	if unix != 0 {
		return time.Unix(unix, 0).UTC().Format("2006-01-02")
	}
	if rfc == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, rfc); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return ""
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *HackerNewsPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *HackerNewsPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
