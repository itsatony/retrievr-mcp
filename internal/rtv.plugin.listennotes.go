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
// Listen Notes podcast provider — v6 cycle 2 / v2.15.0.
//
// API: GET https://listen-api.listennotes.com/api/v2/search
//   Header:  X-ListenAPI-Key: <key>
//   Params:
//     q         free-text query
//     type      "episode" (default for retrieval), "podcast", "curated"
//     offset    0-indexed
//     language  full English language name (e.g. "English", "Spanish")
//     sort_by_date  0 (relevance) | 1 (newest first)
//     len_min / len_max  minimum / maximum episode duration in minutes
//
// Response (subset):
//   { "count","total","next_offset",
//     "results": [
//       { "id","title_original","description_original",
//         "audio","audio_length_sec","pub_date_ms",
//         "explicit_content","link","image","thumbnail",
//         "podcast": { "id","title_original","publisher_original","language" } } ] }
//
// Free 300 req/mo dev tier; paid above. Refuses to start without a key.
// Per-call credential: `listennotes`.
// Residency: US.
// ---------------------------------------------------------------------------

const (
	listennotesPluginID          = SourceListenNotes
	listennotesPluginName        = "Listen Notes"
	listennotesPluginDescription = "Search Listen Notes (listen-api.listennotes.com) for podcast episodes and shows. Free 300 calls/mo dev tier; paid above. Returns episode title, audio URL, duration, publisher, show metadata. Requires API key (per-call credentials.listennotes)."

	listennotesDefaultBaseURL = "https://listen-api.listennotes.com"
	listennotesSearchPath     = "/api/v2/search"
	listennotesMaxLimitCap    = 10 // Listen Notes returns 10/page by default; pagination via offset
	listennotesDefaultRPS     = 1.0
	listennotesDefaultTimeout = 15 * time.Second

	listennotesIDPrefix    = "listennotes:"
	listennotesEcosystemID = "listennotes"

	listennotesHeaderAPIKey = "X-ListenAPI-Key"

	listennotesParamQ          = "q"
	listennotesParamType       = "type"
	listennotesParamOffset     = "offset"
	listennotesParamLanguage   = "language"
	listennotesParamSortByDate = "sort_by_date"

	listennotesTypeEpisode = "episode"

	listennotesCategoriesHint = "Listen Notes language values use full English names (e.g. 'English', 'Spanish'); pass via filters.language. No category filter on the search endpoint."
)

// ---------------------------------------------------------------------------
// Listen Notes wire types
// ---------------------------------------------------------------------------

type listennotesSearchResponse struct {
	Count      int                  `json:"count,omitempty"`
	Total      int                  `json:"total,omitempty"`
	NextOffset int                  `json:"next_offset,omitempty"`
	Results    []listennotesEpisode `json:"results,omitempty"`
}

type listennotesEpisode struct {
	ID                  string          `json:"id,omitempty"`
	TitleOriginal       string          `json:"title_original,omitempty"`
	DescriptionOriginal string          `json:"description_original,omitempty"`
	Audio               string          `json:"audio,omitempty"`
	AudioLengthSec      int             `json:"audio_length_sec,omitempty"`
	PubDateMS           int64           `json:"pub_date_ms,omitempty"`
	ExplicitContent     bool            `json:"explicit_content,omitempty"`
	Link                string          `json:"link,omitempty"`
	Image               string          `json:"image,omitempty"`
	Thumbnail           string          `json:"thumbnail,omitempty"`
	Podcast             listennotesShow `json:"podcast,omitempty"`
}

type listennotesShow struct {
	ID                string `json:"id,omitempty"`
	TitleOriginal     string `json:"title_original,omitempty"`
	PublisherOriginal string `json:"publisher_original,omitempty"`
	Language          string `json:"language,omitempty"`
}

// ---------------------------------------------------------------------------
// ListenNotesPlugin
// ---------------------------------------------------------------------------

// ListenNotesPlugin implements SourcePlugin for the Listen Notes search
// API. Thread-safe after Initialize.
type ListenNotesPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "listennotes".
func (p *ListenNotesPlugin) ID() string { return listennotesPluginID }

// Name returns the human-readable label.
func (p *ListenNotesPlugin) Name() string { return listennotesPluginName }

// Description returns the LLM-facing one-liner.
func (p *ListenNotesPlugin) Description() string { return listennotesPluginDescription }

// ContentTypes — audio.
func (p *ListenNotesPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypeAudio} }

// NativeFormat — JSON.
func (p *ListenNotesPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *ListenNotesPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Listen Notes' filter/sort surface.
func (p *ListenNotesPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true,
		SupportsPagination:       true,
		MaxResultsPerQuery:       listennotesMaxLimitCap,
		CategoriesHint:           listennotesCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentNews, IntentReference},
		Kinds:                    []ResultKind{KindAudio},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *ListenNotesPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = listennotesDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = listennotesDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = listennotesDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *ListenNotesPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Listen Notes /search query.
func (p *ListenNotesPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, SourceListenNotes, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: listennotes requires an API key", ErrCredentialRequired)
	}

	resp, err := p.doSearch(ctx, params, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Results))
	for i := range resp.Results {
		pubs = append(pubs, listennotesEpisodeToPublication(&resp.Results[i]))
	}
	return &SearchResult{
		Total:   resp.Total,
		Results: pubs,
		HasMore: resp.NextOffset > 0 && resp.NextOffset < resp.Total,
	}, nil
}

// Get is not wired in cycle 2 — single-episode lookup uses
// /api/v2/episodes/<id> and isn't on the cycle's critical path.
func (p *ListenNotesPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: listennotes Get is not wired in cycle 2", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *ListenNotesPlugin) doSearch(ctx context.Context, params SearchParams, apiKey string) (*listennotesSearchResponse, error) {
	q := url.Values{}
	q.Set(listennotesParamQ, params.Query)
	q.Set(listennotesParamType, listennotesTypeEpisode)
	if params.Offset > 0 {
		q.Set(listennotesParamOffset, strconv.Itoa(params.Offset))
	}
	if lang := strings.TrimSpace(params.Filters.Language); lang != "" {
		// Listen Notes wants the full English language name; callers
		// pass BCP-47 codes by convention, so a tiny mapping covers the
		// 95% case. Unknown codes are forwarded as-is — Listen Notes
		// will ignore unrecognised values rather than error.
		q.Set(listennotesParamLanguage, listennotesLanguageName(lang))
	}
	if params.Sort == SortDateDesc {
		q.Set(listennotesParamSortByDate, "1")
	}

	reqURL := p.baseURL + listennotesSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("listennotes: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(listennotesHeaderAPIKey, apiKey)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listennotes: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: listennotes", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: listennotes", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("listennotes: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp listennotesSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("listennotes: decode response: %w", err)
	}
	return &resp, nil
}

// listennotesLanguageName maps the BCP-47 short codes most callers pass
// to the full English language names Listen Notes expects. Unmapped
// inputs round-trip unchanged so power users can pass the API's native
// values directly.
func listennotesLanguageName(in string) string {
	switch strings.ToLower(in) {
	case "en", "en-us", "en-gb":
		return "English"
	case "es", "es-es", "es-mx":
		return "Spanish"
	case "de", "de-de":
		return "German"
	case "fr", "fr-fr":
		return "French"
	case "pt", "pt-br":
		return "Portuguese"
	case "it":
		return "Italian"
	case "ja":
		return "Japanese"
	case "zh":
		return "Chinese"
	}
	return in
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func listennotesEpisodeToPublication(ep *listennotesEpisode) Publication {
	rawID := ep.ID
	title := stripXMLTags(strings.TrimSpace(ep.TitleOriginal))
	if title == "" {
		title = ep.Podcast.TitleOriginal
	}

	displayURL := ep.Link
	if displayURL == "" {
		displayURL = "https://www.listennotes.com/podcasts/" + rawID + "/"
	}

	published := ""
	if ep.PubDateMS > 0 {
		published = time.UnixMilli(ep.PubDateMS).UTC().Format("2006-01-02")
	}

	duration := ep.AudioLengthSec

	meta := map[string]any{
		MetaKeyAudioID:            listennotesEcosystemID + ":" + rawID,
		smetaAudioShowTitle:       ep.Podcast.TitleOriginal,
		smetaAudioDurationSeconds: duration,
		smetaAudioPublisher:       ep.Podcast.PublisherOriginal,
		smetaAudioExplicit:        ep.ExplicitContent,
	}
	if ep.Audio != "" {
		meta[smetaAudioAudioURL] = ep.Audio
	}
	if ep.Image != "" {
		meta[smetaAudioImageURL] = ep.Image
	}

	var durPtr *int
	if duration > 0 {
		durPtr = &duration
	}

	return Publication{
		ID:              listennotesIDPrefix + rawID,
		Source:          SourceListenNotes,
		ContentType:     ContentTypeAudio,
		Title:           title,
		Abstract:        stripXMLTags(ep.DescriptionOriginal),
		URL:             displayURL,
		Published:       published,
		Language:        ep.Podcast.Language,
		MediaURL:        ep.Audio,
		MediaMime:       "audio/mpeg",
		DurationSeconds: durPtr,
		ThumbnailURL:    ep.Thumbnail,
		SourceMetadata:  meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *ListenNotesPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *ListenNotesPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
