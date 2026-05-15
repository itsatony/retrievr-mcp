package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Scrapingdog YouTube Search provider — v3 cycle 2 / v2.3.0.
//
// Paid fallback to the official YouTube Data API when daily quota is
// exhausted. Per-request cost: 5 Scrapingdog credits (~$1/1k searches at
// entry plan; cheaper at scale). Intended as a secondary node in the
// video fallback chain — never primary.
//
// API: GET https://api.scrapingdog.com/youtube/search/
//   Required: api_key=<KEY>, query=<q>
//   Optional: country (default "us"), language (default "en"), sp
//   Response shape (subset): {
//     video_results: [
//       { title, link, length, views, published_date, description,
//         thumbnail: { static, rich },
//         channel: { name, link } }
//     ],
//     pagination: { ... }
//   }
//
// Residency: US (Scrapingdog ToS-bound to YouTube). Blocked under eu_strict.
// ---------------------------------------------------------------------------

const (
	scrapingdogYouTubePluginID          = SourceScrapingdogYouTube
	scrapingdogYouTubePluginName        = "Scrapingdog YouTube"
	scrapingdogYouTubePluginDescription = "Paid Scrapingdog fallback for YouTube search when the official YouTube Data API daily quota is exhausted. ~5 credits per query (~$0.005). Same youtube_id dedup as the official source — merged transparently in cross-source results."

	scrapingdogYouTubeDefaultBaseURL = "https://api.scrapingdog.com"
	scrapingdogYouTubeSearchPath     = "/youtube/search/"

	scrapingdogYouTubeQueryParamKey      = "api_key"
	scrapingdogYouTubeQueryParamQuery    = "query"
	scrapingdogYouTubeQueryParamCountry  = "country"
	scrapingdogYouTubeQueryParamLanguage = "language"

	// channel:<id> is YouTube's documented SERP qualifier for channel
	// scoping. Scrapingdog passes the query through to YouTube's search,
	// so the qualifier honours channelId or @handle equivalently.
	scrapingdogYouTubeChannelQualifier = "channel:"

	scrapingdogYouTubeMaxChannelFanout = 5

	scrapingdogYouTubeDefaultRPS        = 2.0
	scrapingdogYouTubeDefaultMaxResults = 10
	scrapingdogYouTubeMaxResultsHardCap = 50
	scrapingdogYouTubeAcceptHeader      = "Accept"
	scrapingdogYouTubeAcceptJSON        = "application/json"
	scrapingdogYouTubeCategoriesHint    = "youtube videos via SERP scraping; restrict by channel via filters.channels"
)

// Extra-key constants (PluginConfig.Extra).
const (
	scrapingdogYouTubeExtraCountry  = "country"  // ISO 3166-1 alpha-2; default us
	scrapingdogYouTubeExtraLanguage = "language" // ISO 639-1; default en
)

// ---------------------------------------------------------------------------
// Scrapingdog wire types
// ---------------------------------------------------------------------------

type scrapingdogYouTubeResponse struct {
	VideoResults []scrapingdogVideoResult `json:"video_results"`
}

type scrapingdogVideoResult struct {
	Title         string                 `json:"title"`
	Link          string                 `json:"link"`
	Length        string                 `json:"length"`
	Views         string                 `json:"views"`
	PublishedDate string                 `json:"published_date"`
	Description   string                 `json:"description"`
	Thumbnail     scrapingdogThumbnail   `json:"thumbnail"`
	Channel       scrapingdogChannelInfo `json:"channel"`
	Extensions    []string               `json:"extensions,omitempty"`
}

type scrapingdogThumbnail struct {
	Static string `json:"static"`
	Rich   string `json:"rich,omitempty"`
}

type scrapingdogChannelInfo struct {
	Name     string `json:"name"`
	Link     string `json:"link"`
	Verified bool   `json:"verified,omitempty"`
}

// ---------------------------------------------------------------------------
// ScrapingdogYouTubePlugin
// ---------------------------------------------------------------------------

// ScrapingdogYouTubePlugin implements SourcePlugin for the Scrapingdog
// YouTube Search API. Thread-safe for concurrent use after Initialize.
type ScrapingdogYouTubePlugin struct {
	baseURL    string
	apiKey     string
	country    string
	language   string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "scrapingdog_youtube".
func (p *ScrapingdogYouTubePlugin) ID() string { return scrapingdogYouTubePluginID }

// Name returns the human-readable label.
func (p *ScrapingdogYouTubePlugin) Name() string { return scrapingdogYouTubePluginName }

// Description returns a one-liner for LLM tool listing.
func (p *ScrapingdogYouTubePlugin) Description() string {
	return scrapingdogYouTubePluginDescription
}

// ContentTypes — Scrapingdog YouTube emits video.
func (p *ScrapingdogYouTubePlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeVideo}
}

// NativeFormat — JSON.
func (p *ScrapingdogYouTubePlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *ScrapingdogYouTubePlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Scrapingdog YouTube filtering + sorting support.
// Scrapingdog scrapes the public YouTube SERP — date filtering is honored
// upstream by YouTube's URL parameters (via `sp`), but exposing it cleanly
// is out of scope for cycle 2. Date-filter is reported false to keep
// SearchParams.Filters semantics honest.
func (p *ScrapingdogYouTubePlugin) Capabilities() SourceCapabilities {
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
		SupportsChannelFilter:    true,
		SupportsLanguageFilter:   true,
		SupportsPagination:       false,
		MaxResultsPerQuery:       scrapingdogYouTubeMaxResultsHardCap,
		CategoriesHint:           scrapingdogYouTubeCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup},
		Kinds:                    []ResultKind{KindVideo},
		RequiresCredential:       true,
	}
}

// Residency — US-resident (Scrapingdog AWS US infra).
func (*ScrapingdogYouTubePlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *ScrapingdogYouTubePlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = scrapingdogYouTubeDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = scrapingdogYouTubeDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.country = stringFromExtra(cfg.Extra, scrapingdogYouTubeExtraCountry, "us")
	p.language = stringFromExtra(cfg.Extra, scrapingdogYouTubeExtraLanguage, "en")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *ScrapingdogYouTubePlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Scrapingdog YouTube search.
func (p *ScrapingdogYouTubePlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, scrapingdogYouTubePluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: scrapingdog_youtube requires an API key", ErrCredentialRequired)
	}

	limit := params.Limit
	if limit <= 0 {
		limit = scrapingdogYouTubeDefaultMaxResults
	}
	if limit > scrapingdogYouTubeMaxResultsHardCap {
		limit = scrapingdogYouTubeMaxResultsHardCap
	}

	channels := params.Filters.Channels
	if len(channels) > scrapingdogYouTubeMaxChannelFanout {
		return nil, fmt.Errorf("%w: scrapingdog_youtube accepts at most %d channels per call, got %d",
			ErrTooManyChannels, scrapingdogYouTubeMaxChannelFanout, len(channels))
	}
	if err := ValidateLanguageTag(params.Filters.Language); err != nil {
		return nil, fmt.Errorf("scrapingdog_youtube: language: %w", err)
	}

	// Single (or unscoped) path.
	if len(channels) <= 1 {
		query := params.Query
		if len(channels) == 1 {
			query = scrapingdogYouTubeChannelQualifier + channels[0] + " " + params.Query
		}
		resp, err := p.doSearch(ctx, params, query, apiKey)
		if err != nil {
			p.recordError(err)
			return nil, err
		}
		p.recordSuccess()
		pubs := scrapingdogPublicationsFromResponse(resp, limit)
		return &SearchResult{
			Total:   len(pubs),
			Results: pubs,
			HasMore: len(resp.VideoResults) > limit,
		}, nil
	}

	// Fan-out per channel (Scrapingdog passes channel: through to YouTube
	// SERP which has no native multi-channel OR syntax). Merged by videoId.
	merged := make([]Publication, 0, len(channels)*limit)
	seen := make(map[string]struct{}, len(channels)*limit)
	hasMore := false
	for _, channelID := range channels {
		query := scrapingdogYouTubeChannelQualifier + channelID + " " + params.Query
		resp, err := p.doSearch(ctx, params, query, apiKey)
		if err != nil {
			p.recordError(err)
			return nil, err
		}
		if len(resp.VideoResults) > limit {
			hasMore = true
		}
		for _, pub := range scrapingdogPublicationsFromResponse(resp, limit) {
			if _, dup := seen[pub.ID]; dup {
				continue
			}
			seen[pub.ID] = struct{}{}
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

func scrapingdogPublicationsFromResponse(resp *scrapingdogYouTubeResponse, limit int) []Publication {
	pubs := make([]Publication, 0, len(resp.VideoResults))
	for _, item := range resp.VideoResults {
		videoID := extractYouTubeVideoID(item.Link)
		if videoID == "" {
			continue
		}
		pubs = append(pubs, scrapingdogVideoResultToPublication(item, videoID))
		if len(pubs) >= limit {
			break
		}
	}
	return pubs
}

// Get is not supported — Scrapingdog's per-video endpoint is a separate paid
// product (different cost profile) and is not wired in cycle 2. Callers
// should resolve detail via the youtube primary plugin once a `youtube_id`
// is in hand.
func (p *ScrapingdogYouTubePlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: scrapingdog_youtube has no Get; resolve via the youtube plugin", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *ScrapingdogYouTubePlugin) doSearch(ctx context.Context, params SearchParams, query, apiKey string) (*scrapingdogYouTubeResponse, error) {
	q := url.Values{}
	q.Set(scrapingdogYouTubeQueryParamKey, apiKey)
	q.Set(scrapingdogYouTubeQueryParamQuery, query)
	if p.country != "" {
		q.Set(scrapingdogYouTubeQueryParamCountry, p.country)
	}
	if lang := p.resolveLanguage(params.Filters.Language); lang != "" {
		q.Set(scrapingdogYouTubeQueryParamLanguage, lang)
	}

	reqURL := p.baseURL + scrapingdogYouTubeSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("scrapingdog_youtube: build request: %w", err)
	}
	req.Header.Set(scrapingdogYouTubeAcceptHeader, scrapingdogYouTubeAcceptJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrapingdog_youtube: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusUnauthorized,
		httpResp.StatusCode == http.StatusForbidden,
		httpResp.StatusCode == http.StatusPaymentRequired:
		return nil, fmt.Errorf("%w: scrapingdog_youtube returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: scrapingdog_youtube", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("scrapingdog_youtube: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp scrapingdogYouTubeResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("scrapingdog_youtube: decode response: %w", err)
	}
	return &resp, nil
}

// resolveLanguage returns the per-call language (first BCP-47 subtag) when
// set, otherwise the operator-configured default. Empty omits the param.
func (p *ScrapingdogYouTubePlugin) resolveLanguage(filterLang string) string {
	if filterLang != "" {
		return BCP47FirstSubtag(filterLang)
	}
	return p.language
}

// ---------------------------------------------------------------------------
// Mapping
// ---------------------------------------------------------------------------

func scrapingdogVideoResultToPublication(r scrapingdogVideoResult, videoID string) Publication {
	pub := Publication{
		// Use the same ID prefix as the official YouTube plugin so cross-
		// source dedup on youtube_id additionally collapses the prefixed-ID
		// identity (defensive; the dedup family path already covers it).
		ID:           fmt.Sprintf("%s:%s", SourceScrapingdogYouTube, videoID),
		Source:       SourceScrapingdogYouTube,
		ContentType:  ContentTypeVideo,
		Title:        r.Title,
		Abstract:     r.Description,
		URL:          r.Link,
		ThumbnailURL: firstNonEmpty(r.Thumbnail.Static, r.Thumbnail.Rich),
		Authors: []Author{{
			Name: r.Channel.Name,
		}},
		SourceMetadata: map[string]any{
			MetaKeyYouTubeID: videoID,
			smetaChannelName: r.Channel.Name,
			smetaPublishedAt: r.PublishedDate,
		},
	}
	if secs := parseClockDurationSeconds(r.Length); secs > 0 {
		pub.DurationSeconds = &secs
	}
	if v := parseViewCount(r.Views); v > 0 {
		pub.SourceMetadata[smetaViewCount] = v
		eng := v
		pub.EngagementScore = &eng
	}
	return pub
}

// extractYouTubeVideoID parses a YouTube watch URL and returns the videoId,
// or "" if absent. Accepts the standard "watch?v=ID" shape and the short
// "youtu.be/ID" shape.
func extractYouTubeVideoID(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if u.Host == "youtu.be" {
		return strings.TrimPrefix(u.Path, "/")
	}
	if strings.HasSuffix(u.Host, "youtube.com") {
		return u.Query().Get("v")
	}
	return ""
}

// parseClockDurationSeconds parses a "H:MM:SS" or "M:SS" wall-clock duration
// (the format Scrapingdog scrapes from YouTube's UI) into total seconds.
// Returns 0 on parse failure.
func parseClockDurationSeconds(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	parts := strings.Split(s, ":")
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}

// parseViewCount parses "1,234 views" / "1.2M views" / bare numeric strings
// into an int. Returns 0 on parse failure (caller treats 0 as "unknown").
func parseViewCount(s string) int {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, " views")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Handle "1.2M" / "1.2K" / "1.2B" suffixes.
	mult := 1
	last := s[len(s)-1]
	switch last {
	case 'K', 'k':
		mult = 1000
		s = s[:len(s)-1]
	case 'M', 'm':
		mult = 1000000
		s = s[:len(s)-1]
	case 'B', 'b':
		mult = 1000000000
		s = s[:len(s)-1]
	}
	s = strings.ReplaceAll(s, ",", "")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	// Guard against ParseFloat accepting "NaN" / "Inf" — converting either
	// to int produces platform-defined trap values (e.g. math.MinInt64).
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return int(f * float64(mult))
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *ScrapingdogYouTubePlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *ScrapingdogYouTubePlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = sanitizeHealthError(err)
	}
}
