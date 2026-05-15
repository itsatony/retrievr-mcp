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
// HERE Geocoding & Search — v6 cycle 1 / v2.14.0.
//
// API: GET https://geocode.search.hereapi.com/v1/geocode
//   Params:
//     q          free-text query (address or POI)
//     apiKey     required
//     limit      1..100 (default 20)
//     lang       BCP-47 language tag
//     in         restriction expression (e.g. "countryCode:DEU")
//
// Response (subset):
//   { "items": [ { "id":"here:cm:...",
//                   "title":"...",
//                   "resultType":"place|locality|address|...",
//                   "address":{ "label","countryCode","city","street",
//                                "houseNumber","postalCode" },
//                   "position":{"lat":float,"lng":float},
//                   "categories":[{"id":"...","name":"..."}] } ] }
//
// Free tier: 1000 transactions/day; paid above.
// Per-call credential: `here`. EU-resident (Berlin).
// ---------------------------------------------------------------------------

const (
	herePluginID          = SourceHERE
	herePluginName        = "HERE Geocoding"
	herePluginDescription = "Search HERE Geocoding & Search (geocode.search.hereapi.com) for addresses, POIs, and administrative areas. Free 1000 transactions/day tier; paid above. EU-resident (Berlin). Requires API key (PluginConfig.APIKey or per-call credentials.here)."

	hereDefaultBaseURL = "https://geocode.search.hereapi.com/v1"
	hereGeocodePath    = "/geocode"
	hereDefaultLimit   = 20
	hereMaxLimitCap    = 100
	hereDefaultRPS     = 5.0
	hereDefaultTimeout = 15 * time.Second

	hereIDPrefix = "here:"

	hereParamQ      = "q"
	hereParamAPIKey = "apiKey"
	hereParamLimit  = "limit"
	hereParamLang   = "lang"
	hereParamIn     = "in"

	hereCategoriesHint = "HERE 'in' restriction expression: pass values like 'countryCode:DEU' or 'circle:52.52,13.40;r=5000' via filters.categories[0] for filtered geocoding."

	hereMetaKeyResultType  = "here_result_type"
	hereMetaKeyCountryCode = "here_country_code"
	hereMetaKeyCategories  = "here_categories"
)

// ---------------------------------------------------------------------------
// HERE wire types
// ---------------------------------------------------------------------------

type hereGeocodeResponse struct {
	Items []hereItem `json:"items,omitempty"`
}

type hereItem struct {
	ID         string         `json:"id,omitempty"`
	Title      string         `json:"title,omitempty"`
	ResultType string         `json:"resultType,omitempty"`
	Address    hereAddress    `json:"address,omitempty"`
	Position   hereLatLng     `json:"position,omitempty"`
	Categories []hereCategory `json:"categories,omitempty"`
}

type hereAddress struct {
	Label       string `json:"label,omitempty"`
	CountryCode string `json:"countryCode,omitempty"`
	City        string `json:"city,omitempty"`
	Street      string `json:"street,omitempty"`
	HouseNumber string `json:"houseNumber,omitempty"`
	PostalCode  string `json:"postalCode,omitempty"`
}

type hereLatLng struct {
	Lat float64 `json:"lat,omitempty"`
	Lng float64 `json:"lng,omitempty"`
}

type hereCategory struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// ---------------------------------------------------------------------------
// HEREPlugin
// ---------------------------------------------------------------------------

// HEREPlugin implements SourcePlugin for the HERE Geocoding & Search v1
// API. Thread-safe after Initialize.
type HEREPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "here".
func (p *HEREPlugin) ID() string { return herePluginID }

// Name returns the human-readable label.
func (p *HEREPlugin) Name() string { return herePluginName }

// Description returns the LLM-facing one-liner.
func (p *HEREPlugin) Description() string { return herePluginDescription }

// ContentTypes — place.
func (p *HEREPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePlace} }

// NativeFormat — JSON.
func (p *HEREPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *HEREPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports HERE's filter/sort surface.
func (p *HEREPlugin) Capabilities() SourceCapabilities {
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
		MaxResultsPerQuery:       hereMaxLimitCap,
		CategoriesHint:           hereCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentReference, IntentQuickLookup},
		Kinds:                    []ResultKind{KindPlace},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *HEREPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = hereDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = hereDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = hereDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *HEREPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a HERE Geocoding query.
func (p *HEREPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = hereDefaultLimit
	}
	if limit > hereMaxLimitCap {
		limit = hereMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceHERE, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: here requires an API key", ErrCredentialRequired)
	}

	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Items))
	for i := range resp.Items {
		pubs = append(pubs, hereItemToPublication(&resp.Items[i]))
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 1.
func (p *HEREPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: here Get is not wired in cycle 1", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *HEREPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*hereGeocodeResponse, error) {
	q := url.Values{}
	q.Set(hereParamQ, params.Query)
	q.Set(hereParamAPIKey, apiKey)
	q.Set(hereParamLimit, strconv.Itoa(limit))

	if lang := strings.TrimSpace(params.Filters.Language); lang != "" {
		q.Set(hereParamLang, lang)
	}
	if len(params.Filters.Categories) > 0 {
		if in := strings.TrimSpace(params.Filters.Categories[0]); in != "" {
			q.Set(hereParamIn, in)
		}
	}

	reqURL := p.baseURL + hereGeocodePath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("here: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("here: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: here", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: here", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("here: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp hereGeocodeResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("here: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func hereItemToPublication(it *hereItem) Publication {
	rawID := it.ID

	lat := it.Position.Lat
	lon := it.Position.Lng
	var latPtr, lonPtr *float64
	if lat != 0 || lon != 0 {
		latPtr = &lat
		lonPtr = &lon
	}

	title := it.Title
	if title == "" {
		title = it.Address.Label
	}

	categories := make([]string, 0, len(it.Categories))
	for _, c := range it.Categories {
		if c.Name != "" {
			categories = append(categories, c.Name)
		}
	}

	meta := map[string]any{}
	if it.ResultType != "" {
		meta[hereMetaKeyResultType] = it.ResultType
		meta[smetaPlaceType] = it.ResultType
	}
	if it.Address.CountryCode != "" {
		meta[hereMetaKeyCountryCode] = it.Address.CountryCode
	}
	if len(categories) > 0 {
		meta[hereMetaKeyCategories] = categories
	}

	return Publication{
		ID:             hereIDPrefix + rawID,
		Source:         SourceHERE,
		ContentType:    ContentTypePlace,
		Title:          title,
		Address:        it.Address.Label,
		Lat:            latPtr,
		Lon:            lonPtr,
		Categories:     categories,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *HEREPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *HEREPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = sanitizeHealthError(err)
	}
}
