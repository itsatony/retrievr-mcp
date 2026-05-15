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
)

// ---------------------------------------------------------------------------
// Bluesky (AT Protocol) post-search provider — v3 cycle 5 / v2.6.0.
//
// Calls the public AppView endpoint app.bsky.feed.searchPosts which
// requires NO authentication for public-search access.
//
// API: GET https://api.bsky.app/xrpc/app.bsky.feed.searchPosts
//   Params: q=<query>, limit=<n> (1..100), cursor=<page>
//   Response: { cursor, posts: [{ uri: "at://did:plc:.../app.bsky.feed.post/<rkey>",
//                                cid, author: { did, handle, displayName,
//                                  viewer: { ... }, labels: [...] },
//                                record: { '$type', text, langs, createdAt, ... },
//                                indexedAt, likeCount, repostCount,
//                                replyCount, quoteCount }] }
//
// Residency: public-research-infrastructure. Bluesky is US-hosted but the
// AT Protocol is an open standard with the public data accessible without
// auth — same admissibility tier as ArXiv/OpenAlex (eu_strict + the
// include-public-research opt-in).
// ---------------------------------------------------------------------------

const (
	blueskyPluginID          = SourceBluesky
	blueskyPluginName        = "Bluesky"
	blueskyPluginDescription = "Search public posts on Bluesky via the AT Protocol AppView. No auth required for public search. AtprotoURI is the canonical dedup key (matches MetaKeyAtprotoURI). Tagged public-research-infrastructure for eu_strict opt-in admissibility."

	blueskyDefaultBaseURL = "https://api.bsky.app"
	blueskySearchPath     = "/xrpc/app.bsky.feed.searchPosts"

	blueskyDefaultLimit = 25
	blueskyMaxLimitCap  = 100
	blueskyDefaultRPS   = 5.0
	blueskyAcceptHeader = "Accept"
	blueskyAcceptJSON   = "application/json"

	// Query-param name constants (extracted v2.7.0).
	blueskyQueryParamQ     = "q"
	blueskyQueryParamLimit = "limit"
	blueskyQueryParamLang  = "lang"

	blueskyCategoriesHint = "public posts on Bluesky (atproto skeets); filters.language wired via the lang query param"
)

// ---------------------------------------------------------------------------
// Bluesky wire types
// ---------------------------------------------------------------------------

type blueskySearchResponse struct {
	Cursor string        `json:"cursor,omitempty"`
	Posts  []blueskyPost `json:"posts,omitempty"`
}

type blueskyPost struct {
	URI         string        `json:"uri"` // canonical atproto URI: at://did:plc:.../app.bsky.feed.post/<rkey>
	CID         string        `json:"cid"`
	Author      blueskyAuthor `json:"author"`
	Record      blueskyRecord `json:"record"`
	IndexedAt   string        `json:"indexedAt,omitempty"`
	LikeCount   int           `json:"likeCount,omitempty"`
	RepostCount int           `json:"repostCount,omitempty"`
	ReplyCount  int           `json:"replyCount,omitempty"`
	QuoteCount  int           `json:"quoteCount,omitempty"`
}

type blueskyAuthor struct {
	DID         string `json:"did"`
	Handle      string `json:"handle"` // alice.bsky.social
	DisplayName string `json:"displayName,omitempty"`
	Avatar      string `json:"avatar,omitempty"`
}

type blueskyRecord struct {
	Type      string   `json:"$type"`
	Text      string   `json:"text"`
	Langs     []string `json:"langs,omitempty"`
	CreatedAt string   `json:"createdAt"`
}

// ---------------------------------------------------------------------------
// BlueskyPlugin
// ---------------------------------------------------------------------------

// BlueskyPlugin implements SourcePlugin for Bluesky's public-search endpoint.
// Thread-safe after Initialize.
type BlueskyPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "bluesky".
func (p *BlueskyPlugin) ID() string { return blueskyPluginID }

// Name returns the human-readable label.
func (p *BlueskyPlugin) Name() string { return blueskyPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *BlueskyPlugin) Description() string { return blueskyPluginDescription }

// ContentTypes — Bluesky emits post.
func (p *BlueskyPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePost}
}

// NativeFormat — JSON.
func (p *BlueskyPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *BlueskyPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Bluesky's filter/sort surface.
func (p *BlueskyPlugin) Capabilities() SourceCapabilities {
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
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true, // lang query param (first BCP-47 subtag)
		SupportsPagination:       true, // via cursor; not wired in cycle 5
		MaxResultsPerQuery:       blueskyMaxLimitCap,
		CategoriesHint:           blueskyCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentNews},
		Kinds:                    []ResultKind{KindPost},
	}
}

// Residency — public-research-infrastructure. Admissible under eu_strict
// only when the operator opts in to public-research-infrastructure.
func (*BlueskyPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionPublicResearch,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *BlueskyPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = blueskyDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = blueskyDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *BlueskyPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Bluesky public-search request.
func (p *BlueskyPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = blueskyDefaultLimit
	}
	if limit > blueskyMaxLimitCap {
		limit = blueskyMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Posts))
	for _, post := range resp.Posts {
		pubs = append(pubs, blueskyPostToPublication(post))
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: resp.Cursor != "",
	}, nil
}

// Get is not wired in cycle 5. Bluesky's app.bsky.feed.getPosts endpoint
// accepts URIs but the ID-routing path is out of scope for v2.6.0.
func (p *BlueskyPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: bluesky Get is not wired in cycle 5", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *BlueskyPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*blueskySearchResponse, error) {
	q := url.Values{}
	q.Set(blueskyQueryParamQ, params.Query)
	q.Set(blueskyQueryParamLimit, strconv.Itoa(limit))
	if lang := BCP47FirstSubtag(params.Filters.Language); lang != "" {
		q.Set(blueskyQueryParamLang, lang)
	}

	reqURL := p.baseURL + blueskySearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("bluesky: build request: %w", err)
	}
	req.Header.Set(blueskyAcceptHeader, blueskyAcceptJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bluesky: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: bluesky", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("bluesky: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp blueskySearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("bluesky: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func blueskyPostToPublication(post blueskyPost) Publication {
	text := post.Record.Text
	title := truncateSnippet(text)
	if title == "" {
		title = "(empty post)"
	}

	engagement := post.LikeCount + post.RepostCount + post.ReplyCount
	platformURL := blueskyPlatformURL(post.URI, post.Author.Handle)
	lang := ""
	if len(post.Record.Langs) > 0 {
		lang = post.Record.Langs[0]
	}

	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceBluesky, blueskyRKey(post.URI)),
		Source:      SourceBluesky,
		ContentType: ContentTypePost,
		Title:       title,
		Abstract:    text,
		URL:         platformURL,
		Published:   shortDate(post.Record.CreatedAt),
		Language:    lang,
		Authors: []Author{{
			Name: firstNonEmpty(post.Author.DisplayName, post.Author.Handle),
		}},
		EngagementScore: &engagement,
		ThumbnailURL:    post.Author.Avatar,
		SourceMetadata: map[string]any{
			MetaKeyAtprotoURI: post.URI,
			smetaAuthorHandle: post.Author.Handle,
			smetaAuthorURL:    "https://bsky.app/profile/" + post.Author.Handle,
			smetaPlatformURL:  platformURL,
			smetaPublishedAt:  post.Record.CreatedAt,
			smetaLikeCount:    post.LikeCount,
			smetaRepostCount:  post.RepostCount,
			smetaReplyCount:   post.ReplyCount,
		},
	}
	return pub
}

// blueskyRKey extracts the record key (last path segment) from an atproto
// URI like "at://did:plc:xyz/app.bsky.feed.post/3kdq5..."
func blueskyRKey(atprotoURI string) string {
	if atprotoURI == "" {
		return "unknown"
	}
	idx := strings.LastIndex(atprotoURI, "/")
	if idx < 0 || idx == len(atprotoURI)-1 {
		return atprotoURI
	}
	return atprotoURI[idx+1:]
}

// blueskyPlatformURL constructs a human-readable bsky.app URL from the
// atproto URI's record key and the author handle.
//
//	at://did:plc:xyz/app.bsky.feed.post/3kdq5...
//	→ https://bsky.app/profile/alice.bsky.social/post/3kdq5...
func blueskyPlatformURL(atprotoURI, handle string) string {
	if atprotoURI == "" || handle == "" {
		return ""
	}
	rkey := blueskyRKey(atprotoURI)
	if rkey == "" {
		return ""
	}
	return "https://bsky.app/profile/" + handle + "/post/" + rkey
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *BlueskyPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *BlueskyPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
