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
// Europeana Search API image provider — v3 cycle 4 / v2.5.0.
//
// 50M+ EU cultural-heritage items (museums, libraries, archives). Image
// subset surfaced via the `qf=TYPE:IMAGE` filter.
//
// API: GET https://api.europeana.eu/record/v2/search.json
//   Required: wskey=<API_KEY>, query=<q>
//   Optional: rows, qf, media, profile
//   Response: { success, requestNumber, itemsCount, totalResults,
//                items: [{ id, type, title: [..], country: [..],
//                          dataProvider: [..], provider: [..],
//                          edmIsShownBy: [..], edmPreview: [..],
//                          guid, link, rights: [..],
//                          dcCreator: [..], year: [..],
//                          edmDatasetName: [..] }] }
//
// Auth: free API key required (Europeana developer portal). Pass via
//       X-Retrievr-Cred-europeana per-call or sources.europeana.api_key.
// Residency: EU (Europeana Foundation, The Hague NL). DPA signed.
// Admissible under eu_strict.
// ---------------------------------------------------------------------------

const (
	europeanaPluginID          = SourceEuropeana
	europeanaPluginName        = "Europeana"
	europeanaPluginDescription = "50M+ EU cultural-heritage items from museums, libraries, and archives. Image subset surfaced via TYPE=IMAGE. License first-class (Europeana's Rights statements). EU-resident (The Hague, NL); admissible under eu_strict."

	europeanaDefaultBaseURL = "https://api.europeana.eu"
	europeanaSearchPath     = "/record/v2/search.json"

	europeanaDefaultRows = 10
	europeanaMaxRowsCap  = 100
	europeanaDefaultRPS  = 5.0
	europeanaAcceptHdr   = "Accept"
	europeanaAcceptJSON  = "application/json"

	// Query-param name constants (extracted v2.7.0).
	europeanaQueryParamKey     = "wskey"
	europeanaQueryParamQuery   = "query"
	europeanaQueryParamRows    = "rows"
	europeanaQueryParamQF      = "qf"
	europeanaQueryParamMedia   = "media"
	europeanaQueryParamMediaY  = "true"
	europeanaQueryParamProfile = "profile"
	europeanaQueryParamLang    = "lang"

	europeanaCategoriesHint = "EU cultural-heritage images (paintings, photographs, manuscripts, museum objects); filters.language wired via the lang param"
)

// Extra-key constants.
const (
	europeanaExtraProfile = "profile" // standard | minimal | rich | facets — default standard
)

// Europeana TYPE filter values.
const europeanaTypeImageFilter = "TYPE:IMAGE"

// ---------------------------------------------------------------------------
// Europeana wire types
// ---------------------------------------------------------------------------

type europeanaSearchResponse struct {
	Success       bool            `json:"success"`
	RequestNumber int             `json:"requestNumber,omitempty"`
	ItemsCount    int             `json:"itemsCount,omitempty"`
	TotalResults  int             `json:"totalResults,omitempty"`
	Items         []europeanaItem `json:"items,omitempty"`
	Error         string          `json:"error,omitempty"`
}

type europeanaItem struct {
	ID             string   `json:"id"`
	Type           string   `json:"type,omitempty"`
	Title          []string `json:"title,omitempty"`
	Country        []string `json:"country,omitempty"`
	DataProvider   []string `json:"dataProvider,omitempty"`
	Provider       []string `json:"provider,omitempty"`
	EDMIsShownBy   []string `json:"edmIsShownBy,omitempty"`
	EDMPreview     []string `json:"edmPreview,omitempty"`
	GUID           string   `json:"guid,omitempty"`
	Link           string   `json:"link,omitempty"`
	Rights         []string `json:"rights,omitempty"`
	DCCreator      []string `json:"dcCreator,omitempty"`
	DCDescription  []string `json:"dcDescription,omitempty"`
	Year           []string `json:"year,omitempty"`
	EDMDatasetName []string `json:"edmDatasetName,omitempty"`
}

// ---------------------------------------------------------------------------
// EuropeanaPlugin
// ---------------------------------------------------------------------------

// EuropeanaPlugin implements SourcePlugin for the Europeana Search API.
// Thread-safe after Initialize.
type EuropeanaPlugin struct {
	baseURL    string
	apiKey     string
	profile    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "europeana".
func (p *EuropeanaPlugin) ID() string { return europeanaPluginID }

// Name returns the human-readable label.
func (p *EuropeanaPlugin) Name() string { return europeanaPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *EuropeanaPlugin) Description() string { return europeanaPluginDescription }

// ContentTypes — Europeana (this plugin scope) emits image.
func (p *EuropeanaPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeImage}
}

// NativeFormat — JSON.
func (p *EuropeanaPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *EuropeanaPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Europeana's filter/sort surface.
func (p *EuropeanaPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false, // could be wired via qf=YEAR:[...] in a future cycle
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true, // lang query param (first BCP-47 subtag)
		SupportsPagination:       true, // via start+rows; not wired in cycle 4
		MaxResultsPerQuery:       europeanaMaxRowsCap,
		CategoriesHint:           europeanaCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentReference},
		Kinds:                    []ResultKind{KindImage},
		RequiresCredential:       true,
	}
}

// Residency — EU (The Hague, NL).
func (*EuropeanaPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionEU,
		DPAStatus:      DPASigned,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *EuropeanaPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = europeanaDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = europeanaDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.profile = stringFromExtra(cfg.Extra, europeanaExtraProfile, "standard")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *EuropeanaPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Europeana image search.
func (p *EuropeanaPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, europeanaPluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: europeana requires an API key (wskey)", ErrCredentialRequired)
	}
	if err := ValidateLanguageTag(params.Filters.Language); err != nil {
		return nil, fmt.Errorf("europeana: language: %w", err)
	}

	rows := params.Limit
	if rows <= 0 {
		rows = europeanaDefaultRows
	}
	if rows > europeanaMaxRowsCap {
		rows = europeanaMaxRowsCap
	}

	resp, err := p.doSearch(ctx, params, rows, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Items))
	for _, item := range resp.Items {
		pub := europeanaItemToPublication(item)
		if pub.MediaURL == "" {
			continue
		}
		pubs = append(pubs, pub)
	}
	return &SearchResult{
		Total:   resp.TotalResults,
		Results: pubs,
		HasMore: resp.TotalResults > rows,
	}, nil
}

// Get is not wired in cycle 4. Europeana's per-record endpoint
// (/record/v2/<id>.json) covers the same surface but the ID-routing path
// is out of scope for v2.5.0.
func (p *EuropeanaPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: europeana Get is not wired in cycle 4", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *EuropeanaPlugin) doSearch(ctx context.Context, params SearchParams, rows int, apiKey string) (*europeanaSearchResponse, error) {
	q := url.Values{}
	q.Set(europeanaQueryParamKey, apiKey)
	q.Set(europeanaQueryParamQuery, params.Query)
	q.Set(europeanaQueryParamRows, strconv.Itoa(rows))
	q.Set(europeanaQueryParamQF, europeanaTypeImageFilter)
	q.Set(europeanaQueryParamMedia, europeanaQueryParamMediaY) // only items that have downloadable media
	if p.profile != "" {
		q.Set(europeanaQueryParamProfile, p.profile)
	}
	if lang := BCP47FirstSubtag(params.Filters.Language); lang != "" {
		q.Set(europeanaQueryParamLang, lang)
	}

	reqURL := p.baseURL + europeanaSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("europeana: build request: %w", err)
	}
	req.Header.Set(europeanaAcceptHdr, europeanaAcceptJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("europeana: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: europeana returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: europeana", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("europeana: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp europeanaSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("europeana: decode response: %w", err)
	}
	if !resp.Success && resp.Error != "" {
		return nil, fmt.Errorf("europeana: api error %s", resp.Error)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func europeanaItemToPublication(item europeanaItem) Publication {
	title := firstSliceValue(item.Title)
	if title == "" {
		title = item.ID
	}
	mediaURL := firstSliceValue(item.EDMIsShownBy)
	thumb := firstSliceValue(item.EDMPreview)
	if thumb == "" {
		thumb = mediaURL
	}
	rights := firstSliceValue(item.Rights)
	creator := firstSliceValue(item.DCCreator)
	year := firstSliceValue(item.Year)
	country := firstSliceValue(item.Country)
	dataProvider := firstSliceValue(item.DataProvider)

	// Per-result page on Europeana's site is the GUID, not the item ID.
	sourcePage := item.GUID
	if sourcePage == "" {
		sourcePage = item.Link
	}

	pub := Publication{
		ID:           fmt.Sprintf("%s:%s", SourceEuropeana, sanitizeEuropeanaID(item.ID)),
		Source:       SourceEuropeana,
		ContentType:  ContentTypeImage,
		Title:        title,
		Abstract:     firstSliceValue(item.DCDescription),
		URL:          sourcePage,
		ThumbnailURL: thumb,
		MediaURL:     mediaURL,
		MediaMime:    inferMimeFromURL(mediaURL),
		License:      rights,
		Published:    year,
		SourceMetadata: map[string]any{
			smetaSourcePage: sourcePage,
		},
	}
	if rights != "" {
		pub.SourceMetadata[smetaLicenseURL] = rights // Europeana's rights field IS a URL
	}
	if creator != "" {
		pub.SourceMetadata[smetaArtist] = creator
		pub.Authors = []Author{{Name: creator}}
	}
	if country != "" {
		pub.SourceMetadata[smetaCountry] = country
	}
	if dataProvider != "" {
		pub.SourceMetadata[smetaDataProvider] = dataProvider
	}
	return pub
}

// firstSliceValue returns the first non-empty string in s, or "".
func firstSliceValue(s []string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// sanitizeEuropeanaID makes the record ID safe for use as a prefixed ID
// suffix. Europeana IDs are pathy like "/91634/AAEC1A2D...". Drop the
// leading slash so the prefix-parsing on Router.Get works cleanly.
func sanitizeEuropeanaID(id string) string {
	return strings.TrimPrefix(id, "/")
}

// inferMimeFromURL guesses a MIME type from the file extension when the
// upstream doesn't surface it (Europeana doesn't).
func inferMimeFromURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	p := strings.ToLower(u.Path)
	switch {
	case strings.HasSuffix(p, ".jpg"), strings.HasSuffix(p, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(p, ".png"):
		return "image/png"
	case strings.HasSuffix(p, ".gif"):
		return "image/gif"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(p, ".webp"):
		return "image/webp"
	case strings.HasSuffix(p, ".tif"), strings.HasSuffix(p, ".tiff"):
		return "image/tiff"
	}
	return ""
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *EuropeanaPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *EuropeanaPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = sanitizeHealthError(err)
	}
}
