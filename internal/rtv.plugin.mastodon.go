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
// Mastodon Search API post provider — v3 cycle 5 / v2.6.0.
//
// Searches a configurable Mastodon instance via /api/v2/search?type=statuses.
// Since Mastodon v4.0+ the public-statuses search endpoint requires NO
// auth token, so retrievr can run anonymously against any open instance.
//
// API: GET {instance}/api/v2/search?q=<q>&type=statuses&limit=<n>&resolve=false
//   Response: { accounts: [], statuses: [{ id, content (HTML),
//                  url, account: { id, username, acct, display_name, url,
//                                  bot, locked, verified },
//                  created_at, favourites_count, reblogs_count,
//                  replies_count, language, media_attachments: [...] }],
//                hashtags: [] }
//
// Auth: none (public statuses on v4+ instances). The plugin does not
// surface a per-call instance override in cycle 5 — operators run one
// retrievr-mcp instance per Mastodon instance pair if they want multi-
// tenancy; the config Extra.default_instance + optional .additional_instances
// will be wired in a later cycle if there's demand.
//
// Residency: operator-declared via PluginConfig.Extra["region"]. Default
// is RegionEU because the spec recommends an EU instance default
// (mastodon.social = Germany; mastodon.online = France). Operators
// pointing at non-EU instances MUST set extra.region=US (or other) to
// keep the EU-mode gate truthful.
// ---------------------------------------------------------------------------

const (
	mastodonPluginID          = SourceMastodon
	mastodonPluginName        = "Mastodon"
	mastodonPluginDescription = "Search public statuses (posts) on a configured Mastodon instance. Mastodon v4+ requires no auth for public-statuses search. Residency follows the configured instance — defaults to EU; operators using a non-EU instance must set extra.region accordingly."

	mastodonDefaultInstance = "https://mastodon.social" // Germany-based default
	mastodonSearchPath      = "/api/v2/search"

	mastodonDefaultLimit = 10
	mastodonMaxLimitCap  = 40 // Mastodon hard cap
	mastodonDefaultRPS   = 5.0
	mastodonAcceptHeader = "Accept"
	mastodonAcceptJSON   = "application/json"

	// Query-param name constants (extracted v2.7.0).
	mastodonQueryParamQ        = "q"
	mastodonQueryParamType     = "type"
	mastodonQueryParamLimit    = "limit"
	mastodonQueryParamResolve  = "resolve"
	mastodonQueryParamTypeStat = "statuses"
	mastodonQueryParamResolveN = "false"

	mastodonCategoriesHint = "public statuses (posts) on a Mastodon instance — content is the post text (HTML-stripped); filters.language post-filters on Status.language with fail-open on missing metadata"
)

// Extra-key constants.
const (
	mastodonExtraRegion = "region" // EU | US | UK-adequacy | global | public-research-infrastructure | unknown
)

// ---------------------------------------------------------------------------
// Mastodon wire types
// ---------------------------------------------------------------------------

type mastodonSearchResponse struct {
	Accounts []json.RawMessage `json:"accounts,omitempty"`
	Statuses []mastodonStatus  `json:"statuses,omitempty"`
	Hashtags []json.RawMessage `json:"hashtags,omitempty"`
}

type mastodonStatus struct {
	ID               string              `json:"id"`
	URL              string              `json:"url"`
	Content          string              `json:"content"` // HTML
	CreatedAt        string              `json:"created_at"`
	Account          mastodonAccount     `json:"account"`
	FavouritesCount  int                 `json:"favourites_count"`
	ReblogsCount     int                 `json:"reblogs_count"`
	RepliesCount     int                 `json:"replies_count"`
	Language         string              `json:"language,omitempty"`
	MediaAttachments []mastodonMediaItem `json:"media_attachments,omitempty"`
	SpoilerText      string              `json:"spoiler_text,omitempty"`
	Sensitive        bool                `json:"sensitive,omitempty"`
}

type mastodonAccount struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Acct        string `json:"acct"`
	DisplayName string `json:"display_name,omitempty"`
	URL         string `json:"url,omitempty"`
	Bot         bool   `json:"bot,omitempty"`
	Locked      bool   `json:"locked,omitempty"`
	Verified    bool   `json:"verified,omitempty"`
}

type mastodonMediaItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url,omitempty"`
}

// ---------------------------------------------------------------------------
// MastodonPlugin
// ---------------------------------------------------------------------------

// MastodonPlugin implements SourcePlugin for Mastodon's public-statuses
// search endpoint. Thread-safe after Initialize.
type MastodonPlugin struct {
	baseURL    string
	region     Region
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "mastodon".
func (p *MastodonPlugin) ID() string { return mastodonPluginID }

// Name returns the human-readable label.
func (p *MastodonPlugin) Name() string { return mastodonPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *MastodonPlugin) Description() string { return mastodonPluginDescription }

// ContentTypes — Mastodon emits post.
func (p *MastodonPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePost}
}

// NativeFormat — JSON.
func (p *MastodonPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *MastodonPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Mastodon's filter/sort surface.
func (p *MastodonPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false, // cursor-based; deferred per retrievr_v4.md
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		// Mastodon has no server-side language filter on /api/v2/search; we
		// post-filter on Status.language with fail-open on missing metadata.
		// This is the single sanctioned client-side filter in v2.7.0 — see
		// docs/filter-reference.md and project_plan/retrievr_v4.md §4.
		SupportsLanguageFilter: true,
		SupportsPagination:     false, // offset+limit on /api/v2/search not wired in cycle 5
		MaxResultsPerQuery:     mastodonMaxLimitCap,
		CategoriesHint:         mastodonCategoriesHint,
		NativeFormat:           FormatJSON,
		AvailableFormats:       []ContentFormat{FormatJSON},
		QueryIntents:           []Intent{IntentQuickLookup, IntentNews},
		Kinds:                  []ResultKind{KindPost},
	}
}

// Residency returns the operator-configured region. Mastodon's residency
// is instance-dependent — there is no canonical Mastodon HQ — so the
// plugin honors the operator's declaration.
func (p *MastodonPlugin) Residency() ResidencyTag {
	region := p.region
	if region == "" {
		region = RegionEU
	}
	return ResidencyTag{
		Region:         region,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
//
// `BaseURL` selects the Mastodon instance (default `mastodon.social`).
// `Extra.region` overrides the EU default — set it to "US" when running
// against a non-EU instance so the EU-mode gate stays truthful.
func (p *MastodonPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = mastodonDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = mastodonDefaultInstance
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	regionStr := stringFromExtra(cfg.Extra, mastodonExtraRegion, string(RegionEU))
	p.region = Region(regionStr)

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *MastodonPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Mastodon public-statuses search.
func (p *MastodonPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	if err := ValidateLanguageTag(params.Filters.Language); err != nil {
		return nil, fmt.Errorf("mastodon: language: %w", err)
	}

	limit := params.Limit
	if limit <= 0 {
		limit = mastodonDefaultLimit
	}
	if limit > mastodonMaxLimitCap {
		limit = mastodonMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Statuses))
	for _, s := range resp.Statuses {
		if !MatchesLanguagePrefix(s.Language, params.Filters.Language) {
			continue
		}
		pubs = append(pubs, mastodonStatusToPublication(s, p.instanceHostname()))
	}
	// HasMore reflects whether the upstream had more matches — compare
	// against the pre-filter response size, not the post-filter slice.
	// Otherwise a language filter that drops most results would falsely
	// report no further pages.
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: len(resp.Statuses) >= limit,
	}, nil
}

// Get is not wired in cycle 5. Mastodon's /api/v1/statuses/:id covers it
// but the ID-routing path is out of scope for v2.6.0.
func (p *MastodonPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: mastodon Get is not wired in cycle 5", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *MastodonPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*mastodonSearchResponse, error) {
	q := url.Values{}
	q.Set(mastodonQueryParamQ, params.Query)
	q.Set(mastodonQueryParamType, mastodonQueryParamTypeStat)
	q.Set(mastodonQueryParamLimit, strconv.Itoa(limit))
	q.Set(mastodonQueryParamResolve, mastodonQueryParamResolveN) // do not eagerly fetch remote results — privacy-respecting default

	reqURL := p.baseURL + mastodonSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("mastodon: build request: %w", err)
	}
	req.Header.Set(mastodonAcceptHeader, mastodonAcceptJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mastodon: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden:
		// Some instances require auth even for /search; surface the distinction.
		return nil, fmt.Errorf("%w: mastodon returned %d — instance may require auth or v3 statuses search", ErrCredentialRequired, httpResp.StatusCode)
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: mastodon", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("mastodon: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp mastodonSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("mastodon: decode response: %w", err)
	}
	return &resp, nil
}

// instanceHostname returns the hostname portion of BaseURL ("mastodon.social"
// from "https://mastodon.social"), or "" on parse failure.
func (p *MastodonPlugin) instanceHostname() string {
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func mastodonStatusToPublication(s mastodonStatus, instance string) Publication {
	plain := stripHTMLTags(s.Content)
	title := truncateSnippet(plain)
	if title == "" {
		title = "(empty status)"
	}

	// Engagement = favourites + reblogs + replies.
	engagement := s.FavouritesCount + s.ReblogsCount + s.RepliesCount

	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceMastodon, s.ID),
		Source:      SourceMastodon,
		ContentType: ContentTypePost,
		Title:       title,
		Abstract:    plain,
		URL:         s.URL,
		Published:   shortDate(s.CreatedAt),
		Language:    s.Language,
		Authors: []Author{{
			Name: firstNonEmpty(s.Account.DisplayName, s.Account.Username),
		}},
		EngagementScore: &engagement,
		SourceMetadata: map[string]any{
			smetaAuthorHandle: mastodonHandle(s.Account, instance),
			smetaAuthorURL:    s.Account.URL,
			smetaPlatformURL:  s.URL,
			smetaPublishedAt:  s.CreatedAt,
			smetaLikeCount:    s.FavouritesCount,
			smetaRepostCount:  s.ReblogsCount,
			smetaReplyCount:   s.RepliesCount,
			smetaInstance:     instance,
			smetaVerified:     s.Account.Verified,
		},
	}
	if len(s.MediaAttachments) > 0 {
		pub.SourceMetadata[smetaMediaCount] = len(s.MediaAttachments)
		// Use the first attachment as a thumbnail preview when available.
		if first := s.MediaAttachments[0]; first.URL != "" {
			pub.ThumbnailURL = first.URL
		}
	}
	return pub
}

// mastodonHandle composes "@username@instance" — the canonical fediverse
// handle. account.acct already contains "@user@instance" for remote users,
// but is bare "username" for local users; normalize.
func mastodonHandle(acct mastodonAccount, instance string) string {
	if strings.Contains(acct.Acct, "@") {
		return "@" + acct.Acct
	}
	if instance == "" {
		return "@" + acct.Username
	}
	return "@" + acct.Username + "@" + instance
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *MastodonPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *MastodonPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
