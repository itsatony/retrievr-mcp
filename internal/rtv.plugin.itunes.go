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
// iTunes / Apple Podcasts search — v6 cycle 2 / v2.15.0.
//
// API: GET https://itunes.apple.com/search
//   Params:
//     term      free-text query
//     media     "podcast"
//     entity    "podcastEpisode" (default for retrieval) | "podcast"
//     limit     1..200
//     country   ISO-3166 country code (e.g. "us", "de")
//     lang      "en_us" etc — passive language tag, not strict filter
//
// Response (subset):
//   { "resultCount": int,
//     "results": [ { "trackId","collectionId","trackName","collectionName",
//                     "artistName","previewUrl","episodeUrl","releaseDate",
//                     "trackTimeMillis","artworkUrl600","description",
//                     "feedUrl","country","language" } ] }
//
// Free, no auth required.
// Residency: US (Apple Inc., Cupertino).
// ---------------------------------------------------------------------------

const (
	itunesPluginID          = SourceITunes
	itunesPluginName        = "iTunes / Apple Podcasts"
	itunesPluginDescription = "Search the iTunes Store / Apple Podcasts catalog (itunes.apple.com). Free, no auth required. Returns podcast episode metadata: title, show name, publisher, audio preview URL, duration, artwork. Cross-provider audio dedup keyed on '<ecosystem>:<trackId>'."

	itunesDefaultBaseURL = "https://itunes.apple.com"
	itunesSearchPath     = "/search"
	itunesDefaultLimit   = 25
	itunesMaxLimitCap    = 200
	itunesDefaultRPS     = 5.0
	itunesDefaultTimeout = 15 * time.Second

	itunesIDPrefix    = "itunes:"
	itunesEcosystemID = "itunes"

	itunesParamTerm    = "term"
	itunesParamMedia   = "media"
	itunesParamEntity  = "entity"
	itunesParamLimit   = "limit"
	itunesParamCountry = "country"

	itunesMediaPodcast  = "podcast"
	itunesEntityEpisode = "podcastEpisode"

	itunesCategoriesHint = "iTunes country filter (ISO-3166 lowercase) via filters.categories[0] — e.g. 'us', 'de', 'gb'. No category-of-content filter beyond entity type."
)

// ---------------------------------------------------------------------------
// iTunes wire types
// ---------------------------------------------------------------------------

type itunesSearchResponse struct {
	ResultCount int             `json:"resultCount,omitempty"`
	Results     []itunesEpisode `json:"results,omitempty"`
}

type itunesEpisode struct {
	WrapperType     string `json:"wrapperType,omitempty"`
	Kind            string `json:"kind,omitempty"`
	TrackID         int64  `json:"trackId,omitempty"`
	CollectionID    int64  `json:"collectionId,omitempty"`
	TrackName       string `json:"trackName,omitempty"`
	CollectionName  string `json:"collectionName,omitempty"`
	ArtistName      string `json:"artistName,omitempty"`
	PreviewURL      string `json:"previewUrl,omitempty"`
	EpisodeURL      string `json:"episodeUrl,omitempty"`
	TrackViewURL    string `json:"trackViewUrl,omitempty"`
	ReleaseDate     string `json:"releaseDate,omitempty"`
	TrackTimeMillis int64  `json:"trackTimeMillis,omitempty"`
	ArtworkURL600   string `json:"artworkUrl600,omitempty"`
	ArtworkURL100   string `json:"artworkUrl100,omitempty"`
	Description     string `json:"description,omitempty"`
	FeedURL         string `json:"feedUrl,omitempty"`
	Country         string `json:"country,omitempty"`
	Language        string `json:"language,omitempty"`
}

// ---------------------------------------------------------------------------
// ITunesPlugin
// ---------------------------------------------------------------------------

// ITunesPlugin implements SourcePlugin for the iTunes Search API (Apple
// Podcasts catalog). Thread-safe after Initialize.
type ITunesPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "itunes".
func (p *ITunesPlugin) ID() string { return itunesPluginID }

// Name returns the human-readable label.
func (p *ITunesPlugin) Name() string { return itunesPluginName }

// Description returns the LLM-facing one-liner.
func (p *ITunesPlugin) Description() string { return itunesPluginDescription }

// ContentTypes — audio.
func (p *ITunesPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypeAudio} }

// NativeFormat — JSON.
func (p *ITunesPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *ITunesPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports iTunes' filter/sort surface.
func (p *ITunesPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       false,
		MaxResultsPerQuery:       itunesMaxLimitCap,
		CategoriesHint:           itunesCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentReference},
		Kinds:                    []ResultKind{KindAudio},
		RequiresCredential:       false,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *ITunesPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = itunesDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = itunesDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = itunesDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *ITunesPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes an iTunes /search query for podcast episodes.
func (p *ITunesPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = itunesDefaultLimit
	}
	if limit > itunesMaxLimitCap {
		limit = itunesMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Results))
	for i := range resp.Results {
		pubs = append(pubs, itunesEpisodeToPublication(&resp.Results[i]))
	}
	return &SearchResult{
		Total:   resp.ResultCount,
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 2.
func (p *ITunesPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: itunes Get is not wired in cycle 2", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *ITunesPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*itunesSearchResponse, error) {
	q := url.Values{}
	q.Set(itunesParamTerm, params.Query)
	q.Set(itunesParamMedia, itunesMediaPodcast)
	q.Set(itunesParamEntity, itunesEntityEpisode)
	q.Set(itunesParamLimit, strconv.Itoa(limit))
	if len(params.Filters.Categories) > 0 {
		if c := strings.TrimSpace(params.Filters.Categories[0]); c != "" {
			q.Set(itunesParamCountry, strings.ToLower(c))
		}
	}

	reqURL := p.baseURL + itunesSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("itunes: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("itunes: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: itunes", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("itunes: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp itunesSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("itunes: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func itunesEpisodeToPublication(ep *itunesEpisode) Publication {
	rawID := strconv.FormatInt(ep.TrackID, 10)

	title := strings.TrimSpace(ep.TrackName)
	if title == "" {
		title = ep.CollectionName
	}

	displayURL := ep.TrackViewURL
	if displayURL == "" {
		displayURL = ep.EpisodeURL
	}

	published := ep.ReleaseDate
	if len(published) >= 10 {
		published = published[:10]
	}

	duration := int(ep.TrackTimeMillis / 1000)
	var durPtr *int
	if duration > 0 {
		durPtr = &duration
	}

	artwork := ep.ArtworkURL600
	if artwork == "" {
		artwork = ep.ArtworkURL100
	}

	meta := map[string]any{
		MetaKeyAudioID:      itunesEcosystemID + ":" + rawID,
		smetaAudioShowTitle: ep.CollectionName,
		smetaAudioPublisher: ep.ArtistName,
	}
	if duration > 0 {
		meta[smetaAudioDurationSeconds] = duration
	}
	if ep.PreviewURL != "" {
		meta[smetaAudioAudioURL] = ep.PreviewURL
	}
	if artwork != "" {
		meta[smetaAudioImageURL] = artwork
	}

	return Publication{
		ID:              itunesIDPrefix + rawID,
		Source:          SourceITunes,
		ContentType:     ContentTypeAudio,
		Title:           title,
		Abstract:        stripXMLTags(ep.Description),
		URL:             displayURL,
		Published:       published,
		Language:        ep.Language,
		MediaURL:        ep.PreviewURL,
		MediaMime:       "audio/mpeg",
		DurationSeconds: durPtr,
		ThumbnailURL:    artwork,
		SourceMetadata:  meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *ITunesPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *ITunesPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
