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
// Wikimedia Commons image-search provider — v3 cycle 4 / v2.5.0.
//
// Searches the MediaWiki API on commons.wikimedia.org. File namespace (6)
// only. Single-call shape using `generator=search` + `prop=imageinfo` to
// avoid a second roundtrip per result.
//
// API: GET https://commons.wikimedia.org/w/api.php
//   Params:
//     action=query, format=json,
//     generator=search, gsrnamespace=6, gsrsearch=<q>, gsrlimit=<n>,
//     prop=imageinfo, iiprop=url|size|mime|extmetadata|user
//   Response:
//     { query: { pages: { "<pageid>": {
//         pageid, ns, title,
//         imageinfo: [{ url, descriptionurl, mime, size, width, height,
//                       user,
//                       extmetadata: {
//                         LicenseShortName: { value, source, hidden },
//                         LicenseUrl:       { value, ... },
//                         Artist:           { value, ... }, ...
//                       } }] } } } }
//
// Auth: none. UA header recommended.
// Residency: US-hosted (Wikimedia Foundation), but the data is openly-
// licensed public infrastructure → tagged public-research-infrastructure
// per `RegionPublicResearch`. Admissible under eu_strict ONLY when the
// operator opts in to public-research-infrastructure inclusion.
// ---------------------------------------------------------------------------

const (
	wikimediaPluginID          = SourceWikimedia
	wikimediaPluginName        = "Wikimedia Commons"
	wikimediaPluginDescription = "100M+ openly-licensed images, audio, and video on Wikimedia Commons. License (e.g. CC BY-SA) is first-class — Publication.License and ImageData.License are always populated for downstream-safe reuse. Tagged public-research-infrastructure (US-hosted but openly-licensed public infrastructure)."

	wikimediaDefaultBaseURL = "https://commons.wikimedia.org"
	wikimediaAPIPath        = "/w/api.php"

	wikimediaDefaultLimit  = 10
	wikimediaMaxLimitCap   = 50
	wikimediaDefaultRPS    = 5.0
	wikimediaAcceptHeader  = "Accept"
	wikimediaAcceptJSON    = "application/json"
	wikimediaFileNamespace = "6"

	wikimediaCategoriesHint = "openly-licensed media files on Wikimedia Commons (images, audio, video; this plugin focuses on images)"
)

// Extra-key constants.
const (
	wikimediaExtraUserAgent = "user_agent"
	wikimediaDefaultUA      = "retrievr-mcp/2.5 (+https://github.com/itsatony/retrievr-mcp; please-override-user-agent@example.com)"
)

// ---------------------------------------------------------------------------
// Wikimedia wire types
// ---------------------------------------------------------------------------

type wikimediaQueryResponse struct {
	Query *wikimediaQueryBlock `json:"query,omitempty"`
	Error *wikimediaError      `json:"error,omitempty"`
}

type wikimediaQueryBlock struct {
	// Pages is a map keyed by pageid (as string). Use a json.RawMessage step
	// for the imageinfo array because some Mediawiki responses surface a
	// missing/empty pages object.
	Pages map[string]wikimediaPage `json:"pages"`
}

type wikimediaPage struct {
	PageID    int                  `json:"pageid"`
	Namespace int                  `json:"ns"`
	Title     string               `json:"title"`
	Index     int                  `json:"index,omitempty"` // search-rank position
	ImageInfo []wikimediaImageInfo `json:"imageinfo,omitempty"`
}

type wikimediaImageInfo struct {
	URL            string                       `json:"url,omitempty"`
	DescriptionURL string                       `json:"descriptionurl,omitempty"`
	MIME           string                       `json:"mime,omitempty"`
	Size           int                          `json:"size,omitempty"`
	Width          int                          `json:"width,omitempty"`
	Height         int                          `json:"height,omitempty"`
	User           string                       `json:"user,omitempty"`
	ExtMetadata    map[string]wikimediaExtField `json:"extmetadata,omitempty"`
}

type wikimediaExtField struct {
	Value string `json:"value"`
	// Source/Hidden fields exist but we don't need them for the mapping.
}

type wikimediaError struct {
	Code string `json:"code"`
	Info string `json:"info"`
}

// ---------------------------------------------------------------------------
// WikimediaPlugin
// ---------------------------------------------------------------------------

// WikimediaPlugin implements SourcePlugin for Wikimedia Commons image search.
// Thread-safe after Initialize.
type WikimediaPlugin struct {
	baseURL    string
	userAgent  string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "wikimedia".
func (p *WikimediaPlugin) ID() string { return wikimediaPluginID }

// Name returns the human-readable label.
func (p *WikimediaPlugin) Name() string { return wikimediaPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *WikimediaPlugin) Description() string { return wikimediaPluginDescription }

// ContentTypes — Wikimedia (this plugin scope) emits image.
func (p *WikimediaPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeImage}
}

// NativeFormat — JSON.
func (p *WikimediaPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *WikimediaPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Wikimedia's filter/sort surface.
func (p *WikimediaPlugin) Capabilities() SourceCapabilities {
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
		SupportsPagination:       false,
		MaxResultsPerQuery:       wikimediaMaxLimitCap,
		CategoriesHint:           wikimediaCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentReference, IntentQuickLookup},
		Kinds:                    []ResultKind{KindImage},
	}
}

// Residency — public-research-infrastructure. Same admissibility tier as
// ArXiv / OpenAlex — eu_strict + include_public_research opt-in.
func (*WikimediaPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionPublicResearch,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *WikimediaPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = wikimediaDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = wikimediaDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.userAgent = stringFromExtra(cfg.Extra, wikimediaExtraUserAgent, wikimediaDefaultUA)

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *WikimediaPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Wikimedia Commons image search.
func (p *WikimediaPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = wikimediaDefaultLimit
	}
	if limit > wikimediaMaxLimitCap {
		limit = wikimediaMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params.Query, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	// Filter out non-image results, drop entries with no resolvable URL,
	// and surface in stable per-rank order.
	pubs := wikimediaPagesToPublications(resp)
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false, // single-page MediaWiki API; explicit pagination not wired
	}, nil
}

// Get is not wired in cycle 4. Wikimedia's per-file detail endpoint
// (action=query&pageids=...) covers the same surface, but pre-fixed-ID
// resolution would require a separate codepath; out of scope for v2.5.0.
func (p *WikimediaPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: wikimedia Get is not wired in cycle 4 (use Search)", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *WikimediaPlugin) doSearch(ctx context.Context, query string, limit int) (*wikimediaQueryResponse, error) {
	q := url.Values{}
	q.Set("action", "query")
	q.Set("format", "json")
	q.Set("generator", "search")
	q.Set("gsrnamespace", wikimediaFileNamespace)
	q.Set("gsrsearch", query)
	q.Set("gsrlimit", strconv.Itoa(limit))
	q.Set("prop", "imageinfo")
	q.Set("iiprop", "url|size|mime|extmetadata|user")
	q.Set("formatversion", "2") // newer formatversion has cleaner pages array; but to keep backward-compat with our pages-as-map parsing we DON'T use it
	q.Del("formatversion")      // sticking with default (pages-as-map keyed by pageid)

	reqURL := p.baseURL + wikimediaAPIPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("wikimedia: build request: %w", err)
	}
	req.Header.Set(wikimediaAcceptHeader, wikimediaAcceptJSON)
	req.Header.Set("User-Agent", p.userAgent)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wikimedia: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: wikimedia", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("wikimedia: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp wikimediaQueryResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("wikimedia: decode response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("wikimedia: api error code=%s info=%s", resp.Error.Code, resp.Error.Info)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

// wikimediaPagesToPublications converts the MediaWiki response into a
// Publication slice sorted by the search-rank `index` field.
func wikimediaPagesToPublications(resp *wikimediaQueryResponse) []Publication {
	if resp == nil || resp.Query == nil || len(resp.Query.Pages) == 0 {
		return nil
	}
	pubs := make([]Publication, 0, len(resp.Query.Pages))
	for _, page := range resp.Query.Pages {
		if len(page.ImageInfo) == 0 {
			continue
		}
		pub := wikimediaPageToPublication(page)
		if pub.MediaURL == "" {
			continue
		}
		pubs = append(pubs, pub)
	}
	// MediaWiki returns pages as a map → stable iteration not guaranteed.
	// Sort by Index (search rank) so order is deterministic.
	sortByIndex(pubs)
	return pubs
}

func wikimediaPageToPublication(page wikimediaPage) Publication {
	info := page.ImageInfo[0]
	license, licenseURL, artist := extractWikimediaLicense(info.ExtMetadata)

	pub := Publication{
		ID:           fmt.Sprintf("%s:%s", SourceWikimedia, normalizeWikimediaFile(page.Title)),
		Source:       SourceWikimedia,
		ContentType:  ContentTypeImage,
		Title:        page.Title,
		URL:          info.DescriptionURL,
		ThumbnailURL: info.URL, // MediaWiki returns the full URL; usable as-is for preview
		MediaURL:     info.URL,
		MediaMime:    info.MIME,
		License:      license,
		SourceMetadata: map[string]any{
			MetaKeyWikimediaFile: normalizeWikimediaFile(page.Title),
			smetaWidth:           info.Width,
			smetaHeight:          info.Height,
			smetaSourcePage:      info.DescriptionURL,
			// "index" surfaced separately for sortByIndex; see below.
			"_search_index": page.Index,
		},
	}
	if licenseURL != "" {
		pub.SourceMetadata[smetaLicenseURL] = licenseURL
	}
	if artist != "" {
		pub.SourceMetadata[smetaArtist] = stripHTMLTags(artist)
		pub.Authors = []Author{{Name: stripHTMLTags(artist)}}
	}
	return pub
}

// normalizeWikimediaFile turns "File:Mona Lisa.jpg" → "File:Mona_Lisa.jpg"
// (the canonical wiki form, which is also Commons' filesystem identity).
// Used as both the dedup key (MetaKeyWikimediaFile) and the per-result ID
// suffix so two providers returning the same Commons file converge.
func normalizeWikimediaFile(title string) string {
	return strings.ReplaceAll(title, " ", "_")
}

// extractWikimediaLicense reads LicenseShortName / LicenseUrl / Artist from
// extmetadata. Returns ("", "", "") when none are present.
func extractWikimediaLicense(meta map[string]wikimediaExtField) (license, licenseURL, artist string) {
	if v, ok := meta["LicenseShortName"]; ok {
		license = v.Value
	}
	if v, ok := meta["LicenseUrl"]; ok {
		licenseURL = v.Value
	}
	if v, ok := meta["Artist"]; ok {
		artist = v.Value
	}
	return
}

// (stripHTMLTags is shared with rtv.plugin.wikipedia.go.)

// sortByIndex sorts Publications by their MediaWiki search-rank index
// (stored in SourceMetadata["_search_index"]) ascending. Stable; preserves
// insertion order for entries lacking an index.
func sortByIndex(pubs []Publication) {
	for i := 1; i < len(pubs); i++ {
		ii := indexOfPublication(pubs[i])
		j := i
		for j > 0 && indexOfPublication(pubs[j-1]) > ii {
			pubs[j], pubs[j-1] = pubs[j-1], pubs[j]
			j--
		}
	}
}

func indexOfPublication(p Publication) int {
	if v, ok := p.SourceMetadata["_search_index"].(int); ok {
		return v
	}
	return 1 << 30
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *WikimediaPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *WikimediaPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
