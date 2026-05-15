package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Google Places (Places API v1) — v6 cycle 1 / v2.14.0.
//
// API: POST https://places.googleapis.com/v1/places:searchText
//   Headers:
//     X-Goog-Api-Key: <key>           — required
//     X-Goog-FieldMask: <comma-list>  — required, names the fields to return
//     Content-Type: application/json
//   Body: { "textQuery": "<q>", "maxResultCount": int,
//           "languageCode": "en", "regionCode": "US" }
//
// Response (subset of fields requested via FieldMask):
//   { "places": [ { "id": "places/ChIJ...",
//                    "displayName": {"text":"...","languageCode":"en"},
//                    "formattedAddress": "...",
//                    "location": {"latitude": float, "longitude": float},
//                    "rating": float, "userRatingCount": int,
//                    "types": ["..."],
//                    "websiteUri": "..." } ] }
//
// Free tier: $200/mo Google Maps Platform credit. Per-call credential:
// `googleplaces`. Refuses to start without a key.
//
// Residency: US (Google LLC, Mountain View; covered-by-SCC).
// ---------------------------------------------------------------------------

const (
	googlePlacesPluginID          = SourceGooglePlaces
	googlePlacesPluginName        = "Google Places"
	googlePlacesPluginDescription = "Search Google Places (Places API v1) for POIs and addresses. Free $200/mo credit on Google Maps Platform; paid above. Requires API key (PluginConfig.APIKey or per-call credentials.googleplaces). Returns place_id, formatted address, lat/lon, rating, types, website URI."

	googlePlacesDefaultBaseURL = "https://places.googleapis.com"
	googlePlacesSearchPath     = "/v1/places:searchText"
	googlePlacesDefaultLimit   = 10
	googlePlacesMaxLimitCap    = 20
	googlePlacesDefaultRPS     = 5.0
	googlePlacesDefaultTimeout = 15 * time.Second

	googlePlacesIDPrefix = "googleplaces:"

	googlePlacesHeaderAPIKey    = "X-Goog-Api-Key"
	googlePlacesHeaderFieldMask = "X-Goog-FieldMask"

	// Default field mask. Each field listed adds to the response (and to
	// the billing tier — these are all in the Basic SKU).
	googlePlacesDefaultFieldMask = "places.id,places.displayName,places.formattedAddress,places.location,places.rating,places.userRatingCount,places.types,places.websiteUri"

	googlePlacesCategoriesHint = "Google Places type values (lowercase, underscored): restaurant, cafe, museum, park, university, hospital, etc. Pass via filters.categories[*] — joined into the textQuery prompt."

	googlePlacesMetaKeyTypes       = "googleplaces_types"
	googlePlacesMetaKeyRating      = "googleplaces_rating"
	googlePlacesMetaKeyRatingCount = "googleplaces_rating_count"
	googlePlacesMetaKeyWebsite     = "googleplaces_website"
)

// ---------------------------------------------------------------------------
// Google Places wire types
// ---------------------------------------------------------------------------

type googlePlacesSearchRequest struct {
	TextQuery      string `json:"textQuery"`
	MaxResultCount int    `json:"maxResultCount,omitempty"`
	LanguageCode   string `json:"languageCode,omitempty"`
	RegionCode     string `json:"regionCode,omitempty"`
}

type googlePlacesSearchResponse struct {
	Places []googlePlacesPlace `json:"places,omitempty"`
}

type googlePlacesPlace struct {
	ID               string                `json:"id,omitempty"`
	DisplayName      googlePlacesLocalized `json:"displayName,omitempty"`
	FormattedAddress string                `json:"formattedAddress,omitempty"`
	Location         googlePlacesLatLng    `json:"location,omitempty"`
	Rating           float64               `json:"rating,omitempty"`
	UserRatingCount  int                   `json:"userRatingCount,omitempty"`
	Types            []string              `json:"types,omitempty"`
	WebsiteURI       string                `json:"websiteUri,omitempty"`
}

type googlePlacesLocalized struct {
	Text         string `json:"text,omitempty"`
	LanguageCode string `json:"languageCode,omitempty"`
}

type googlePlacesLatLng struct {
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
}

// ---------------------------------------------------------------------------
// GooglePlacesPlugin
// ---------------------------------------------------------------------------

// GooglePlacesPlugin implements SourcePlugin for the Google Places API
// v1 Text Search endpoint. Thread-safe after Initialize.
type GooglePlacesPlugin struct {
	baseURL    string
	apiKey     string
	fieldMask  string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "googleplaces".
func (p *GooglePlacesPlugin) ID() string { return googlePlacesPluginID }

// Name returns the human-readable label.
func (p *GooglePlacesPlugin) Name() string { return googlePlacesPluginName }

// Description returns the LLM-facing one-liner.
func (p *GooglePlacesPlugin) Description() string { return googlePlacesPluginDescription }

// ContentTypes — place.
func (p *GooglePlacesPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePlace} }

// NativeFormat — JSON.
func (p *GooglePlacesPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *GooglePlacesPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Google Places' filter/sort surface.
func (p *GooglePlacesPlugin) Capabilities() SourceCapabilities {
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
		SupportsLanguageFilter:   true,
		SupportsPagination:       false,
		MaxResultsPerQuery:       googlePlacesMaxLimitCap,
		CategoriesHint:           googlePlacesCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentReference, IntentQuickLookup},
		Kinds:                    []ResultKind{KindPlace},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *GooglePlacesPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = googlePlacesDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = googlePlacesDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.fieldMask = googlePlacesDefaultFieldMask
	if cfg.Extra != nil {
		if fm := strings.TrimSpace(cfg.Extra["field_mask"]); fm != "" {
			p.fieldMask = fm
		}
	}

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = googlePlacesDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *GooglePlacesPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Google Places Text Search POST.
func (p *GooglePlacesPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = googlePlacesDefaultLimit
	}
	if limit > googlePlacesMaxLimitCap {
		limit = googlePlacesMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceGooglePlaces, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: googleplaces requires an API key", ErrCredentialRequired)
	}

	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Places))
	for i := range resp.Places {
		pubs = append(pubs, googlePlacesPlaceToPublication(&resp.Places[i]))
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 1 — Place Details lives at
// /v1/places/<place_id> and would add another billing tier. Future
// cycle can wire it behind an opt-in flag.
func (p *GooglePlacesPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: googleplaces Get is not wired in cycle 1", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *GooglePlacesPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*googlePlacesSearchResponse, error) {
	body := googlePlacesSearchRequest{
		TextQuery:      googlePlacesBuildQuery(params),
		MaxResultCount: limit,
	}
	if lang := strings.TrimSpace(params.Filters.Language); lang != "" {
		body.LanguageCode = lang
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("googleplaces: encode body: %w", err)
	}

	reqURL := p.baseURL + googlePlacesSearchPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("googleplaces: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(googlePlacesHeaderAPIKey, apiKey)
	req.Header.Set(googlePlacesHeaderFieldMask, p.fieldMask)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("googleplaces: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: googleplaces", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: googleplaces", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("googleplaces: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp googlePlacesSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("googleplaces: decode response: %w", err)
	}
	return &resp, nil
}

// googlePlacesBuildQuery folds the free-text query plus any category
// hints into the textQuery body field. Google Places' textQuery is a
// natural-language prompt so we space-join the parts.
func googlePlacesBuildQuery(params SearchParams) string {
	parts := []string{}
	if q := strings.TrimSpace(params.Query); q != "" {
		parts = append(parts, q)
	}
	for _, c := range params.Filters.Categories {
		if v := strings.TrimSpace(c); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func googlePlacesPlaceToPublication(pl *googlePlacesPlace) Publication {
	rawID := strings.TrimPrefix(pl.ID, "places/")
	title := pl.DisplayName.Text
	if title == "" {
		title = pl.FormattedAddress
	}

	lat := pl.Location.Latitude
	lon := pl.Location.Longitude
	var latPtr, lonPtr *float64
	if lat != 0 || lon != 0 {
		latPtr = &lat
		lonPtr = &lon
	}

	meta := map[string]any{}
	if len(pl.Types) > 0 {
		meta[googlePlacesMetaKeyTypes] = pl.Types
		meta[smetaPlaceType] = pl.Types[0]
	}
	if pl.Rating != 0 {
		meta[googlePlacesMetaKeyRating] = pl.Rating
	}
	if pl.UserRatingCount != 0 {
		meta[googlePlacesMetaKeyRatingCount] = pl.UserRatingCount
	}
	if pl.WebsiteURI != "" {
		meta[googlePlacesMetaKeyWebsite] = pl.WebsiteURI
	}

	return Publication{
		ID:             googlePlacesIDPrefix + rawID,
		Source:         SourceGooglePlaces,
		ContentType:    ContentTypePlace,
		Title:          title,
		Address:        pl.FormattedAddress,
		URL:            pl.WebsiteURI,
		Lat:            latPtr,
		Lon:            lonPtr,
		Language:       pl.DisplayName.LanguageCode,
		Categories:     pl.Types,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *GooglePlacesPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *GooglePlacesPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
