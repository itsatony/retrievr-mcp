package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Reddit OAuth2 client-credentials post-search provider — v3 cycle 5 / v2.6.0.
//
// Two-stage flow:
//   1) POST https://www.reddit.com/api/v1/access_token
//        Basic auth: <client_id>:<client_secret>
//        Body: grant_type=client_credentials
//        Response: { access_token, token_type, expires_in, scope }
//   2) GET https://oauth.reddit.com/search?q=<q>&limit=<n>&type=link&raw_json=1
//        Auth: Bearer <access_token>
//        Response: { kind: "Listing", data: { children: [{ kind: "t3",
//                                              data: { id, name, title,
//                                                      author, subreddit,
//                                                      permalink, url,
//                                                      selftext, score,
//                                                      num_comments,
//                                                      ups, downs,
//                                                      created_utc, ... } }],
//                                              after } }
//
// Credential format: `<client_id>:<client_secret>` as a single string,
// passed via `X-Retrievr-Cred-reddit` per-call header or
// `RETRIEVR_REDDIT_API_KEY` env / YAML.
//
// Residency: US (Reddit Inc., San Francisco). Blocked under eu_strict.
//
// User-Agent: Reddit requires a meaningful UA identifying the application.
// Operators must override the placeholder before enabling.
// ---------------------------------------------------------------------------

const (
	redditPluginID          = SourceReddit
	redditPluginName        = "Reddit"
	redditPluginDescription = "Search Reddit posts (type=link) via OAuth2 client-credentials. Credential format is <client_id>:<client_secret> as a single value. ~100 QPM on the free tier. US-resident; blocked under eu_strict."

	redditTokenURL                  = "https://www.reddit.com/api/v1/access_token"
	redditDefaultBaseURL            = "https://oauth.reddit.com"
	redditSearchPath                = "/search"
	redditSubredditSearchPathFormat = "/r/%s/search"
	redditDefaultLimit              = 10
	redditMaxLimitCap               = 100
	redditMaxSubredditFanout        = 5
	redditDefaultRPS                = 1.5 // 100 QPM ≈ 1.67 req/s; leave headroom
	redditAcceptHeader              = "Accept"
	redditAcceptJSON                = "application/json"
	redditContentTypeHeader         = "Content-Type"
	redditContentTypeFormURLEncoded = "application/x-www-form-urlencoded"
	redditAuthHeader                = "Authorization"
	redditAuthBearerPrefix          = "Bearer "
	redditUAHeader                  = "User-Agent"
	redditDefaultUserAgent          = "retrievr-mcp/2.7 (+https://github.com/itsatony/retrievr-mcp; please-override-user-agent@example.com)"

	redditTokenGrantBody           = "grant_type=client_credentials"
	redditTokenDefaultExpiresInSec = 3600

	// Query-param name constants (extracted v2.7.0).
	redditQueryParamQ         = "q"
	redditQueryParamLimit     = "limit"
	redditQueryParamType      = "type"
	redditQueryParamTypeLink  = "link"
	redditQueryParamRawJSON   = "raw_json"
	redditQueryParamRawJSONOn = "1"
	redditQueryParamRestrict  = "restrict_sr"
	redditQueryParamRestrictY = "on"

	redditListingKindSubmission = "t3"

	// Reddit's documented subreddit-name charset; same pattern as the
	// /etc/account-creation validator. Lengths 2-21 per Reddit FAQ.
	redditSubredditNamePattern = "^[A-Za-z0-9_]{2,21}$"

	redditCategoriesHint = "Reddit submissions (posts), including title + selftext; restrict by community via filters.subreddits"

	// Token expiry safety margin: refresh 60s before the upstream expires.
	redditTokenRefreshSafetyMargin = 60 * time.Second
)

// Extra-key constants.
const (
	redditExtraUserAgent = "user_agent"
)

// ---------------------------------------------------------------------------
// Reddit wire types
// ---------------------------------------------------------------------------

type redditTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
	Error       string `json:"error,omitempty"`
}

type redditListing struct {
	Kind string            `json:"kind"`
	Data redditListingData `json:"data"`
}

type redditListingData struct {
	After    string               `json:"after,omitempty"`
	Children []redditListingChild `json:"children"`
}

type redditListingChild struct {
	Kind string           `json:"kind"`
	Data redditSubmission `json:"data"`
}

type redditSubmission struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"` // fullname e.g. "t3_<id>"
	Title       string  `json:"title"`
	SelfText    string  `json:"selftext,omitempty"`
	Author      string  `json:"author,omitempty"`
	Subreddit   string  `json:"subreddit,omitempty"`
	Permalink   string  `json:"permalink,omitempty"` // path-only, prepend reddit.com
	URL         string  `json:"url,omitempty"`       // crosspost URL when present, else permalink
	Score       int     `json:"score,omitempty"`
	NumComments int     `json:"num_comments,omitempty"`
	CreatedUTC  float64 `json:"created_utc,omitempty"`
	Thumbnail   string  `json:"thumbnail,omitempty"`
	Over18      bool    `json:"over_18,omitempty"`
}

// ---------------------------------------------------------------------------
// RedditPlugin
// ---------------------------------------------------------------------------

// RedditPlugin implements SourcePlugin for Reddit's OAuth search endpoint.
// Token cached + auto-refreshed via clientCredentialsToken. Thread-safe.
type RedditPlugin struct {
	baseURL    string
	tokenURL   string
	apiKey     string // "<client_id>:<client_secret>"
	userAgent  string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	// Per-credential token cache. Each tenant's <client_id:secret> pair
	// gets its own bearer-token entry so cross-tenant sessions never
	// reuse one another's identity / billing / rate-limit attribution.
	tokens *tokenCache

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "reddit".
func (p *RedditPlugin) ID() string { return redditPluginID }

// Name returns the human-readable label.
func (p *RedditPlugin) Name() string { return redditPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *RedditPlugin) Description() string { return redditPluginDescription }

// ContentTypes — Reddit emits post.
func (p *RedditPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePost}
}

// NativeFormat — JSON.
func (p *RedditPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *RedditPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Reddit's filter/sort surface.
func (p *RedditPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false, // t= filter not wired in v2.7.0; see retrievr_v4.md deferrals
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    true, // subreddit scoping = "channel" semantics
		SupportsLanguageFilter:   false,
		SupportsPagination:       true, // after= cursor; not wired in cycle 5
		MaxResultsPerQuery:       redditMaxLimitCap,
		CategoriesHint:           redditCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentNews},
		Kinds:                    []ResultKind{KindPost},
		RequiresCredential:       true,

		SupportsPublishedAfterFilter: PublishedAfterCoarsePostFilter,
	}
}

// Residency — US-resident (Reddit Inc., San Francisco).
func (*RedditPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPACoveredBySCC,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *RedditPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = redditDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = redditDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	// Tests override the token URL by pointing it at the same test server
	// as the base. When BaseURL is overridden, also route tokens there.
	if cfg.BaseURL == "" {
		p.tokenURL = redditTokenURL
	} else {
		p.tokenURL = strings.TrimRight(cfg.BaseURL, "/") + "/api/v1/access_token"
	}

	p.userAgent = stringFromExtra(cfg.Extra, redditExtraUserAgent, redditDefaultUserAgent)

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.tokens = newTokenCache(SourceReddit)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *RedditPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Reddit OAuth search.
func (p *RedditPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	credential := CredentialFor(ctx, redditPluginID, p.apiKey)
	if credential == "" {
		return nil, fmt.Errorf("%w: reddit requires <client_id>:<client_secret>", ErrCredentialRequired)
	}
	clientID, clientSecret, err := parseRedditCredential(credential)
	if err != nil {
		return nil, err
	}

	// Validate the fan-out cap BEFORE touching the network so callers get a
	// fast typed error instead of waiting on token exchange to fail later.
	subreddits := params.Filters.Subreddits
	if len(subreddits) > redditMaxSubredditFanout {
		return nil, fmt.Errorf("%w: reddit accepts at most %d subreddits per call, got %d",
			ErrTooManySubreddits, redditMaxSubredditFanout, len(subreddits))
	}
	for _, sub := range subreddits {
		if !isValidSubredditName(sub) {
			return nil, fmt.Errorf("%w: reddit subreddit name %q is invalid (allowed: %s)",
				ErrInvalidInput, sub, redditSubredditNamePattern)
		}
	}

	token, err := p.ensureToken(ctx, clientID, clientSecret)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	credKey := redditCredentialKey(clientID, clientSecret)

	limit := params.Limit
	if limit <= 0 {
		limit = redditDefaultLimit
	}
	if limit > redditMaxLimitCap {
		limit = redditMaxLimitCap
	}

	if len(subreddits) == 0 {
		listing, err := p.doSearch(ctx, params, "", limit, token, credKey)
		if err != nil {
			p.recordError(err)
			return nil, err
		}
		p.recordSuccess()
		pubs := redditPublicationsFromListing(listing)
		return &SearchResult{
			Total:   len(pubs),
			Results: pubs,
			HasMore: listing.Data.After != "",
		}, nil
	}

	// Subreddit fan-out: one request per subreddit, results merged with
	// dedup keyed on the prefixed submission ID (Reddit's t3_<id> via
	// pub.ID). The same submission crossposted to multiple subreddits has
	// distinct permalinks but a single submission name, so URL-based dedup
	// would let crossposts through. Fallback to URL when ID is empty.
	merged := make([]Publication, 0, len(subreddits)*limit)
	seen := make(map[string]struct{}, len(subreddits)*limit)
	hasMore := false
	for _, sub := range subreddits {
		listing, err := p.doSearch(ctx, params, sub, limit, token, credKey)
		if err != nil {
			p.recordError(err)
			return nil, err
		}
		if listing.Data.After != "" {
			hasMore = true
		}
		for _, pub := range redditPublicationsFromListing(listing) {
			key := pub.ID
			if key == "" {
				key = pub.URL
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, pub)
		}
	}
	p.recordSuccess()
	return &SearchResult{
		Total:   len(merged),
		Results: merged,
		HasMore: hasMore,
	}, nil
}

// redditSubredditNameRE caches the compiled subreddit-name validator.
var redditSubredditNameRE = regexp.MustCompile(redditSubredditNamePattern)

// isValidSubredditName reports whether the given name fits Reddit's charset.
func isValidSubredditName(name string) bool {
	return redditSubredditNameRE.MatchString(name)
}

func redditPublicationsFromListing(listing *redditListing) []Publication {
	pubs := make([]Publication, 0, len(listing.Data.Children))
	for _, c := range listing.Data.Children {
		if c.Kind != redditListingKindSubmission {
			continue
		}
		pubs = append(pubs, redditSubmissionToPublication(c.Data))
	}
	return pubs
}

// Get is not wired in cycle 5. Reddit's /by_id/<fullname>.json covers it.
func (p *RedditPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: reddit Get is not wired in cycle 5", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

// credentialKey returns the cache key under which a (clientID,
// clientSecret) pair's bearer token lives. Used by ensureToken and the
// 401-invalidation path so cache lookups and writes agree.
func redditCredentialKey(clientID, clientSecret string) string {
	return clientID + ":" + clientSecret
}

// ensureToken returns a cached access token if still valid (with safety
// margin), otherwise exchanges client_id:client_secret for a fresh one.
// The token cache is keyed per credential pair so multi-tenant callers
// never reuse one another's bearer token.
func (p *RedditPlugin) ensureToken(ctx context.Context, clientID, clientSecret string) (string, error) {
	credKey := redditCredentialKey(clientID, clientSecret)
	if tok, ok := p.tokens.Get(credKey); ok {
		return tok, nil
	}

	body := strings.NewReader(redditTokenGrantBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, body)
	if err != nil {
		return "", fmt.Errorf("reddit: build token request: %w", err)
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set(redditContentTypeHeader, redditContentTypeFormURLEncoded)
	req.Header.Set(redditAcceptHeader, redditAcceptJSON)
	req.Header.Set(redditUAHeader, p.userAgent)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("reddit: token http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("%w: reddit client_id/client_secret rejected", ErrCredentialInvalid)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return "", fmt.Errorf("reddit: token status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var tok redditTokenResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("reddit: decode token response: %w", err)
	}
	if tok.Error != "" {
		return "", fmt.Errorf("reddit: token error %s", tok.Error)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("reddit: empty access_token")
	}

	expiresIn := tok.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = redditTokenDefaultExpiresInSec
	}
	lifetime := time.Duration(expiresIn)*time.Second - redditTokenRefreshSafetyMargin
	if lifetime < time.Minute {
		lifetime = time.Minute
	}
	p.tokens.Set(credKey, tok.AccessToken, lifetime)
	return tok.AccessToken, nil
}

// doSearch issues one search request. If subreddit is non-empty, the path
// becomes /r/<sub>/search with restrict_sr=on so Reddit limits results to
// that community; otherwise /search runs against /r/all by default.
func (p *RedditPlugin) doSearch(ctx context.Context, params SearchParams, subreddit string, limit int, token, credKey string) (*redditListing, error) {
	q := url.Values{}
	q.Set(redditQueryParamQ, params.Query)
	q.Set(redditQueryParamLimit, strconv.Itoa(limit))
	q.Set(redditQueryParamType, redditQueryParamTypeLink) // submissions only — exclude comments
	q.Set(redditQueryParamRawJSON, redditQueryParamRawJSONOn)

	path := redditSearchPath
	if subreddit != "" {
		path = fmt.Sprintf(redditSubredditSearchPathFormat, subreddit)
		q.Set(redditQueryParamRestrict, redditQueryParamRestrictY)
	}

	reqURL := p.baseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("reddit: build search request: %w", err)
	}
	req.Header.Set(redditAuthHeader, redditAuthBearerPrefix+token)
	req.Header.Set(redditAcceptHeader, redditAcceptJSON)
	req.Header.Set(redditUAHeader, p.userAgent)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reddit: search http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusUnauthorized,
		httpResp.StatusCode == http.StatusForbidden:
		// Invalidate THIS tenant's cached token only; other tenants
		// keep their entries. Reddit returns 401 for expired tokens
		// and 403 for revoked/suspended apps — both require re-auth
		// on the next call, so the cache miss must propagate.
		p.tokens.Invalidate(credKey)
		return nil, fmt.Errorf("%w: reddit search returned %d — token rejected or app revoked", ErrCredentialInvalid, httpResp.StatusCode)
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: reddit", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("reddit: search status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var listing redditListing
	if err := json.NewDecoder(httpResp.Body).Decode(&listing); err != nil {
		return nil, fmt.Errorf("reddit: decode search response: %w", err)
	}
	return &listing, nil
}

// parseRedditCredential splits "<client_id>:<client_secret>" on the FIRST
// colon. Returns ErrCredentialInvalid when the format is wrong.
func parseRedditCredential(credential string) (clientID, clientSecret string, err error) {
	idx := strings.Index(credential, ":")
	if idx < 0 || idx == 0 || idx == len(credential)-1 {
		return "", "", fmt.Errorf("%w: reddit credential must be <client_id>:<client_secret>", ErrCredentialInvalid)
	}
	return credential[:idx], credential[idx+1:], nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func redditSubmissionToPublication(s redditSubmission) Publication {
	permalink := "https://www.reddit.com" + s.Permalink
	platformURL := permalink
	externalURL := s.URL
	if externalURL == permalink {
		externalURL = ""
	}

	body := s.SelfText
	title := s.Title

	createdAt := time.Unix(int64(s.CreatedUTC), 0).UTC().Format(time.RFC3339)
	engagement := s.Score + s.NumComments

	thumb := s.Thumbnail
	if !redditValidThumbnail(thumb) {
		thumb = ""
	}

	pub := Publication{
		ID:           fmt.Sprintf("%s:%s", SourceReddit, s.Name),
		Source:       SourceReddit,
		ContentType:  ContentTypePost,
		Title:        title,
		Abstract:     body,
		URL:          platformURL,
		Published:    createdAt[:10],
		ThumbnailURL: thumb,
		Authors: []Author{{
			Name: "u/" + s.Author,
		}},
		EngagementScore: &engagement,
		SourceMetadata: map[string]any{
			smetaAuthorHandle: "u/" + s.Author,
			smetaAuthorURL:    "https://www.reddit.com/user/" + s.Author,
			smetaPlatformURL:  platformURL,
			smetaPublishedAt:  createdAt,
			smetaSubreddit:    s.Subreddit,
			smetaLikeCount:    s.Score, // Reddit conflates likes/upvotes
			smetaReplyCount:   s.NumComments,
		},
	}
	if externalURL != "" {
		pub.SourceMetadata[smetaExternalURL] = externalURL
	}
	return pub
}

// redditValidThumbnail returns false for the placeholder values Reddit uses
// when no thumbnail is available (self, default, nsfw, etc.).
func redditValidThumbnail(s string) bool {
	switch s {
	case "", "self", "default", "nsfw", "spoiler", "image":
		return false
	}
	return strings.HasPrefix(s, "http")
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *RedditPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *RedditPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
