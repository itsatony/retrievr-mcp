package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// YouTube Data API v3 provider — v3 cycle 2 / v2.3.0.
//
// API:
//   - Search: GET https://www.googleapis.com/youtube/v3/search
//       Required params: part=snippet, q=<query>, type=video, key=<API_KEY>
//       Optional: maxResults (1-50, default 5), order, regionCode,
//                 relevanceLanguage, safeSearch, publishedAfter,
//                 publishedBefore, pageToken
//       Response: { items: [{ id: {videoId}, snippet: {...} }], nextPageToken }
//
//   - Videos (Get): GET https://www.googleapis.com/youtube/v3/videos
//       Required: part=snippet,contentDetails,statistics, id=<videoId>,
//                 key=<API_KEY>
//       Response: items include duration (ISO 8601 "PT1H2M3S"), view count,
//                 like count, live status.
//
// Auth: API key via X-Retrievr-Cred-youtube per-call header or
//       sources.youtube.api_key in YAML / RETRIEVR_YOUTUBE_API_KEY env.
// Quota: search.list = 100 units, videos.list = 1 unit. Default 10000
//        units/day = ~100 searches/key/day. 403 with reason "quotaExceeded"
//        maps to ErrRateLimitExceeded.
// Residency: US (Google Cloud). Blocked under eu_strict.
// ---------------------------------------------------------------------------

// Identity / config constants.
const (
	youtubePluginID          = SourceYouTube
	youtubePluginName        = "YouTube"
	youtubePluginDescription = "Search YouTube videos via the official Data API v3. Returns metadata (title, channel, thumbnail, published_at) on search; rtv_get adds duration, view count, and like count. US-resident; blocked under eu_strict. Default quota: 10,000 units/day per API key = ~100 searches/day."

	youtubeDefaultBaseURL = "https://www.googleapis.com"
	youtubeSearchPath     = "/youtube/v3/search"
	youtubeVideosPath     = "/youtube/v3/videos"

	// Default page size. YouTube allows 1-50; retrievr default is 10.
	youtubeDefaultMaxResults = 10
	youtubeMaxResultsCap     = 50

	// Soft rate limit. The real cap is the daily quota (100 searches/key/day
	// at default quota); pacing at ~1 req/s protects bursts from tripping
	// per-second short-window limits.
	youtubeDefaultRPS = 1.0

	youtubeAcceptHeader  = "Accept"
	youtubeAcceptJSON    = "application/json"
	youtubeQueryParamKey = "key"

	// Query-param name constants (extracted v2.7.0 — no magic strings).
	youtubeParamPart              = "part"
	youtubeParamType              = "type"
	youtubeParamQ                 = "q"
	youtubeParamMaxResults        = "maxResults"
	youtubeParamOrder             = "order"
	youtubeParamRegionCode        = "regionCode"
	youtubeParamRelevanceLanguage = "relevanceLanguage"
	youtubeParamSafeSearch        = "safeSearch"
	youtubeParamPublishedAfter    = "publishedAfter"
	youtubeParamPublishedBefore   = "publishedBefore"
	youtubeParamChannelID         = "channelId"
	youtubeParamID                = "id"

	youtubePartSearch = "snippet"
	youtubePartVideos = "snippet,contentDetails,statistics,liveStreamingDetails"
	youtubeTypeVideo  = "video"

	// Fan-out cap for multi-channel queries. YouTube's /search endpoint
	// accepts only one channelId per call; we issue one HTTP request per
	// channel and merge. Capped to protect quota — each call costs 100
	// quota units against the daily 10k budget (≈100 searches/day/key).
	youtubeMaxChannelFanout = 5

	youtubeCategoriesHint = "video content; restrict by channel via filters.channels (channelId values)"
)

// Extra-key constants (PluginConfig.Extra). All optional.
const (
	youtubeExtraRegionCode        = "region_code"        // ISO 3166-1 alpha-2 (e.g. "DE")
	youtubeExtraRelevanceLanguage = "relevance_language" // ISO 639-1 (e.g. "en")
	youtubeExtraSafeSearch        = "safe_search"        // none | moderate | strict (default: moderate)
)

// Sort-order to YouTube `order` param mapping.
const (
	youtubeOrderRelevance = "relevance"
	youtubeOrderDate      = "date"
	youtubeOrderViewCount = "viewCount"
)

// YouTube API error reasons we explicitly map.
const (
	youtubeReasonQuotaExceeded      = "quotaExceeded"
	youtubeReasonDailyLimitExceeded = "dailyLimitExceeded"
	youtubeReasonRateLimitExceeded  = "rateLimitExceeded"
	youtubeReasonKeyInvalid         = "keyInvalid"
)

// ---------------------------------------------------------------------------
// YouTube wire types
// ---------------------------------------------------------------------------

type youtubeSearchResponse struct {
	Kind          string                   `json:"kind"`
	NextPageToken string                   `json:"nextPageToken,omitempty"`
	PageInfo      youtubePageInfo          `json:"pageInfo"`
	Items         []youtubeSearchItem      `json:"items"`
	Error         *youtubeAPIErrorEnvelope `json:"error,omitempty"`
}

type youtubeVideosResponse struct {
	Kind  string                   `json:"kind"`
	Items []youtubeVideoItem       `json:"items"`
	Error *youtubeAPIErrorEnvelope `json:"error,omitempty"`
}

type youtubePageInfo struct {
	TotalResults   int `json:"totalResults"`
	ResultsPerPage int `json:"resultsPerPage"`
}

type youtubeSearchItem struct {
	Kind    string         `json:"kind"`
	ID      youtubeID      `json:"id"`
	Snippet youtubeSnippet `json:"snippet"`
}

type youtubeVideoItem struct {
	Kind                 string                       `json:"kind"`
	ID                   string                       `json:"id"` // bare videoId on /videos
	Snippet              youtubeSnippet               `json:"snippet"`
	ContentDetails       *youtubeContentDetails       `json:"contentDetails,omitempty"`
	Statistics           *youtubeStatistics           `json:"statistics,omitempty"`
	LiveStreamingDetails *youtubeLiveStreamingDetails `json:"liveStreamingDetails,omitempty"`
}

type youtubeID struct {
	Kind    string `json:"kind"`
	VideoID string `json:"videoId,omitempty"`
}

type youtubeSnippet struct {
	PublishedAt          string                  `json:"publishedAt"`
	ChannelID            string                  `json:"channelId"`
	Title                string                  `json:"title"`
	Description          string                  `json:"description"`
	Thumbnails           map[string]youtubeThumb `json:"thumbnails"`
	ChannelTitle         string                  `json:"channelTitle"`
	LiveBroadcastContent string                  `json:"liveBroadcastContent,omitempty"`
	DefaultLanguage      string                  `json:"defaultLanguage,omitempty"`
	DefaultAudioLanguage string                  `json:"defaultAudioLanguage,omitempty"`
}

type youtubeThumb struct {
	URL    string `json:"url"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

type youtubeContentDetails struct {
	Duration string `json:"duration"` // ISO 8601, e.g. "PT1H2M3S"
}

type youtubeStatistics struct {
	ViewCount    string `json:"viewCount,omitempty"`
	LikeCount    string `json:"likeCount,omitempty"`
	CommentCount string `json:"commentCount,omitempty"`
}

type youtubeLiveStreamingDetails struct {
	ActualStartTime    string `json:"actualStartTime,omitempty"`
	ScheduledStartTime string `json:"scheduledStartTime,omitempty"`
}

type youtubeAPIErrorEnvelope struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Errors  []youtubeAPIError `json:"errors,omitempty"`
}

type youtubeAPIError struct {
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

// ---------------------------------------------------------------------------
// YouTubePlugin
// ---------------------------------------------------------------------------

// YouTubePlugin implements SourcePlugin for the YouTube Data API v3.
// Thread-safe for concurrent use after Initialize.
type YouTubePlugin struct {
	baseURL           string
	apiKey            string
	regionCode        string
	relevanceLanguage string
	safeSearch        string
	httpClient        *http.Client
	enabled           bool
	rateLimit         float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "youtube".
func (p *YouTubePlugin) ID() string { return youtubePluginID }

// Name returns the human-readable label.
func (p *YouTubePlugin) Name() string { return youtubePluginName }

// Description returns a one-liner for LLM tool listing.
func (p *YouTubePlugin) Description() string { return youtubePluginDescription }

// ContentTypes — YouTube emits video.
func (p *YouTubePlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeVideo}
}

// NativeFormat — JSON.
func (p *YouTubePlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *YouTubePlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports YouTube-specific filtering + sorting support.
func (p *YouTubePlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true, // publishedAfter / publishedBefore
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    true,
		SupportsLanguageFilter:   true,
		SupportsPagination:       true,
		MaxResultsPerQuery:       youtubeMaxResultsCap,
		CategoriesHint:           youtubeCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentReference},
		Kinds:                    []ResultKind{KindVideo},
		RequiresCredential:       true,
	}
}

// Residency — US-resident (Google Cloud).
func (*YouTubePlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPACoveredBySCC,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *YouTubePlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = youtubeDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = youtubeDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.regionCode = stringFromExtra(cfg.Extra, youtubeExtraRegionCode, "")
	p.relevanceLanguage = stringFromExtra(cfg.Extra, youtubeExtraRelevanceLanguage, "")
	p.safeSearch = stringFromExtra(cfg.Extra, youtubeExtraSafeSearch, "moderate")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *YouTubePlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a YouTube search via /youtube/v3/search.
// Credentials read from ctx via CredentialFor.
func (p *YouTubePlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, youtubePluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: youtube requires an API key", ErrCredentialRequired)
	}

	maxResults := params.Limit
	if maxResults <= 0 {
		maxResults = youtubeDefaultMaxResults
	}
	if maxResults > youtubeMaxResultsCap {
		maxResults = youtubeMaxResultsCap
	}

	channels := params.Filters.Channels
	if len(channels) > youtubeMaxChannelFanout {
		return nil, fmt.Errorf("%w: youtube accepts at most %d channels per call, got %d",
			ErrTooManyChannels, youtubeMaxChannelFanout, len(channels))
	}
	if err := ValidateLanguageTag(params.Filters.Language); err != nil {
		return nil, fmt.Errorf("youtube: language: %w", err)
	}

	// Single-channel (or unscoped) path: one upstream request.
	if len(channels) <= 1 {
		channelID := ""
		if len(channels) == 1 {
			channelID = channels[0]
		}
		resp, err := p.doSearch(ctx, params, channelID, maxResults, apiKey)
		if err != nil {
			p.recordError(err)
			return nil, err
		}
		p.recordSuccess()
		pubs := publicationsFromYouTubeResponse(resp)
		return &SearchResult{
			Total:   resp.PageInfo.TotalResults,
			Results: pubs,
			HasMore: resp.NextPageToken != "",
		}, nil
	}

	// Multi-channel fan-out: one request per channel, sequential to respect
	// the per-key quota budget. Results merged with router-style dedup by
	// videoId. HasMore is true if any per-channel response had a nextPageToken.
	merged := make([]Publication, 0, len(channels)*maxResults)
	seen := make(map[string]struct{}, len(channels)*maxResults)
	hasMore := false
	totalSeen := 0
	for _, channelID := range channels {
		resp, err := p.doSearch(ctx, params, channelID, maxResults, apiKey)
		if err != nil {
			p.recordError(err)
			return nil, err
		}
		totalSeen += resp.PageInfo.TotalResults
		if resp.NextPageToken != "" {
			hasMore = true
		}
		for _, pub := range publicationsFromYouTubeResponse(resp) {
			if _, dup := seen[pub.ID]; dup {
				continue
			}
			seen[pub.ID] = struct{}{}
			merged = append(merged, pub)
		}
	}
	p.recordSuccess()

	return &SearchResult{
		Total:   totalSeen,
		Results: merged,
		HasMore: hasMore,
	}, nil
}

// publicationsFromYouTubeResponse maps a search response into Publications,
// skipping items missing a videoId. Shared by single-channel and fan-out paths.
func publicationsFromYouTubeResponse(resp *youtubeSearchResponse) []Publication {
	pubs := make([]Publication, 0, len(resp.Items))
	for _, item := range resp.Items {
		if item.ID.VideoID == "" {
			continue
		}
		pubs = append(pubs, youtubeSearchItemToPublication(item))
	}
	return pubs
}

// Get retrieves a single video's full metadata via /youtube/v3/videos.
// id is the bare videoId (e.g. "dQw4w9WgXcQ").
func (p *YouTubePlugin) Get(ctx context.Context, id string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	apiKey := CredentialFor(ctx, youtubePluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: youtube requires an API key", ErrCredentialRequired)
	}
	if id == "" {
		return nil, fmt.Errorf("%w: empty videoId", ErrInvalidID)
	}

	resp, err := p.doVideos(ctx, id, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	if len(resp.Items) == 0 {
		return nil, fmt.Errorf("%w: youtube videoId %q", ErrSourceNotFound, id)
	}
	pub := youtubeVideoItemToPublication(resp.Items[0])
	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *YouTubePlugin) doSearch(ctx context.Context, params SearchParams, channelID string, maxResults int, apiKey string) (*youtubeSearchResponse, error) {
	q := url.Values{}
	q.Set(youtubeParamPart, youtubePartSearch)
	q.Set(youtubeParamType, youtubeTypeVideo)
	q.Set(youtubeParamQ, params.Query)
	q.Set(youtubeParamMaxResults, strconv.Itoa(maxResults))
	q.Set(youtubeQueryParamKey, apiKey)

	if order := youtubeOrderFromSort(params.Sort); order != "" {
		q.Set(youtubeParamOrder, order)
	}
	if p.regionCode != "" {
		q.Set(youtubeParamRegionCode, p.regionCode)
	}
	if lang := p.resolveRelevanceLanguage(params.Filters.Language); lang != "" {
		q.Set(youtubeParamRelevanceLanguage, lang)
	}
	if p.safeSearch != "" {
		q.Set(youtubeParamSafeSearch, p.safeSearch)
	}
	if channelID != "" {
		q.Set(youtubeParamChannelID, channelID)
	}
	if params.Filters.DateFrom != "" {
		if rfc := dateToRFC3339Start(params.Filters.DateFrom); rfc != "" {
			q.Set(youtubeParamPublishedAfter, rfc)
		}
	}
	if params.Filters.DateTo != "" {
		if rfc := dateToRFC3339End(params.Filters.DateTo); rfc != "" {
			q.Set(youtubeParamPublishedBefore, rfc)
		}
	}

	reqURL := p.baseURL + youtubeSearchPath + "?" + q.Encode()
	var resp youtubeSearchResponse
	if err := p.doJSON(ctx, reqURL, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// resolveRelevanceLanguage returns the per-call language (first BCP-47
// subtag) when set, otherwise the operator-configured default. Empty means
// "do not send the relevanceLanguage param".
func (p *YouTubePlugin) resolveRelevanceLanguage(filterLang string) string {
	if filterLang != "" {
		return BCP47FirstSubtag(filterLang)
	}
	return p.relevanceLanguage
}

func (p *YouTubePlugin) doVideos(ctx context.Context, id, apiKey string) (*youtubeVideosResponse, error) {
	q := url.Values{}
	q.Set(youtubeParamPart, youtubePartVideos)
	q.Set(youtubeParamID, id)
	q.Set(youtubeQueryParamKey, apiKey)

	reqURL := p.baseURL + youtubeVideosPath + "?" + q.Encode()
	var resp youtubeVideosResponse
	if err := p.doJSON(ctx, reqURL, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// doJSON issues a GET, classifies common errors, and decodes the body.
// Generic enough for both /search and /videos.
func (p *YouTubePlugin) doJSON(ctx context.Context, reqURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("youtube: build request: %w", err)
	}
	req.Header.Set(youtubeAcceptHeader, youtubeAcceptJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("youtube: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode >= 400 {
		body, _ := io.ReadAll(httpResp.Body)
		return classifyYouTubeHTTPError(httpResp.StatusCode, body)
	}

	if err := json.NewDecoder(httpResp.Body).Decode(out); err != nil {
		return fmt.Errorf("youtube: decode response: %w", err)
	}
	return nil
}

// classifyYouTubeHTTPError maps a non-2xx response to a typed retrievr
// sentinel error. Falls back to a generic error with the truncated body.
func classifyYouTubeHTTPError(status int, body []byte) error {
	// Parse the structured envelope to read reason codes.
	var env struct {
		Error youtubeAPIErrorEnvelope `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	for _, e := range env.Error.Errors {
		switch e.Reason {
		case youtubeReasonQuotaExceeded, youtubeReasonDailyLimitExceeded, youtubeReasonRateLimitExceeded:
			return fmt.Errorf("%w: youtube reason=%s", ErrRateLimitExceeded, e.Reason)
		case youtubeReasonKeyInvalid:
			return fmt.Errorf("%w: youtube reason=%s", ErrCredentialInvalid, e.Reason)
		}
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: youtube returned %d", ErrCredentialInvalid, status)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: youtube", ErrRateLimitExceeded)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: youtube", ErrSourceNotFound)
	}
	return fmt.Errorf("youtube: status=%d body=%s", status, truncateForError(string(body)))
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

// youtubeSearchItemToPublication builds a lightweight Publication from a
// search result (no duration / view count — those require a /videos call).
func youtubeSearchItemToPublication(item youtubeSearchItem) Publication {
	videoID := item.ID.VideoID
	pub := Publication{
		ID:           fmt.Sprintf("%s:%s", SourceYouTube, videoID),
		Source:       SourceYouTube,
		ContentType:  ContentTypeVideo,
		Title:        item.Snippet.Title,
		Abstract:     item.Snippet.Description,
		URL:          "https://www.youtube.com/watch?v=" + videoID,
		ThumbnailURL: pickBestThumbnail(item.Snippet.Thumbnails),
		Authors: []Author{{
			Name: item.Snippet.ChannelTitle,
		}},
		Published: shortDate(item.Snippet.PublishedAt),
		Language:  firstNonEmpty(item.Snippet.DefaultLanguage, item.Snippet.DefaultAudioLanguage),
		SourceMetadata: map[string]any{
			MetaKeyYouTubeID:   videoID,
			smetaChannelID:     item.Snippet.ChannelID,
			smetaChannelName:   item.Snippet.ChannelTitle,
			smetaPublishedAt:   item.Snippet.PublishedAt,
			smetaLiveBroadcast: item.Snippet.LiveBroadcastContent,
		},
	}
	return pub
}

// youtubeVideoItemToPublication builds a full Publication from a /videos
// response item (includes duration + view/like counts).
func youtubeVideoItemToPublication(item youtubeVideoItem) Publication {
	pub := Publication{
		ID:           fmt.Sprintf("%s:%s", SourceYouTube, item.ID),
		Source:       SourceYouTube,
		ContentType:  ContentTypeVideo,
		Title:        item.Snippet.Title,
		Abstract:     item.Snippet.Description,
		URL:          "https://www.youtube.com/watch?v=" + item.ID,
		ThumbnailURL: pickBestThumbnail(item.Snippet.Thumbnails),
		Authors: []Author{{
			Name: item.Snippet.ChannelTitle,
		}},
		Published: shortDate(item.Snippet.PublishedAt),
		Language:  firstNonEmpty(item.Snippet.DefaultLanguage, item.Snippet.DefaultAudioLanguage),
	}

	meta := map[string]any{
		MetaKeyYouTubeID:   item.ID,
		smetaChannelID:     item.Snippet.ChannelID,
		smetaChannelName:   item.Snippet.ChannelTitle,
		smetaPublishedAt:   item.Snippet.PublishedAt,
		smetaLiveBroadcast: item.Snippet.LiveBroadcastContent,
	}

	if item.ContentDetails != nil {
		if d := parseISO8601DurationSeconds(item.ContentDetails.Duration); d > 0 {
			pub.DurationSeconds = &d
		}
	}
	if item.Statistics != nil {
		if v, err := strconv.Atoi(item.Statistics.ViewCount); err == nil {
			meta[smetaViewCount] = v
		}
		if v, err := strconv.Atoi(item.Statistics.LikeCount); err == nil {
			meta[smetaLikeCount] = v
			pub.EngagementScore = &v
		}
	}
	pub.SourceMetadata = meta
	return pub
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// pickBestThumbnail picks the highest-resolution thumbnail URL. YouTube
// returns named buckets: default, medium, high, standard, maxres (asc).
func pickBestThumbnail(thumbs map[string]youtubeThumb) string {
	for _, k := range []string{"maxres", "standard", "high", "medium", "default"} {
		if t, ok := thumbs[k]; ok && t.URL != "" {
			return t.URL
		}
	}
	return ""
}

// firstNonEmpty returns the first non-empty argument, or "".
func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// shortDate normalizes RFC3339 ("2024-09-12T10:00:00Z") to YYYY-MM-DD.
// Returns the input unchanged if shorter than 10 chars.
func shortDate(s string) string {
	if len(s) < 10 {
		return s
	}
	return s[:10]
}

// dateToRFC3339Start formats YYYY-MM-DD or YYYY as a midnight-UTC RFC3339.
// Used for publishedAfter (inclusive lower bound).
func dateToRFC3339Start(d string) string {
	switch len(d) {
	case 4: // YYYY
		return d + "-01-01T00:00:00Z"
	case 10: // YYYY-MM-DD
		return d + "T00:00:00Z"
	}
	return ""
}

// dateToRFC3339End formats YYYY-MM-DD or YYYY as an end-of-day RFC3339.
func dateToRFC3339End(d string) string {
	switch len(d) {
	case 4: // YYYY
		return d + "-12-31T23:59:59Z"
	case 10: // YYYY-MM-DD
		return d + "T23:59:59Z"
	}
	return ""
}

// youtubeOrderFromSort maps retrievr SortOrder to YouTube `order`. Empty
// string is returned to let YouTube default to "relevance".
func youtubeOrderFromSort(s SortOrder) string {
	switch s {
	case SortRelevance:
		return youtubeOrderRelevance
	case SortDateDesc, SortDateAsc:
		return youtubeOrderDate
	}
	return ""
}

// iso8601DurationRE captures ISO 8601 duration components (hours, minutes,
// seconds). YouTube uses uppercase "PT" prefix with optional H, M, S parts.
var iso8601DurationRE = regexp.MustCompile(`^PT(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?$`)

// parseISO8601DurationSeconds parses YouTube's "PT1H2M3S" into total seconds.
// Returns 0 on parse failure.
func parseISO8601DurationSeconds(s string) int {
	if s == "" {
		return 0
	}
	m := iso8601DurationRE.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	parseInt := func(x string) int {
		if x == "" {
			return 0
		}
		v, err := strconv.Atoi(x)
		if err != nil {
			return 0
		}
		return v
	}
	return parseInt(m[1])*3600 + parseInt(m[2])*60 + parseInt(m[3])
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *YouTubePlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *YouTubePlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Intentional divergence from the majority recordError pattern: quota /
	// rate-limit errors are throttling signals, not health failures, so we
	// keep the plugin "healthy" for retry-middleware rotation. Every other
	// error class flips healthy=false. Pinned by
	// TestYouTube_Search_QuotaExceededMapsToRateLimitExceeded.
	p.healthy = errors.Is(err, ErrRateLimitExceeded)
	if err != nil {
		p.lastError = sanitizeHealthError(err)
	}
}
