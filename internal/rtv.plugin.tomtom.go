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
// TomTom Search API place-search provider — v3 cycle 3 / v2.4.0.
//
// EU-resident commercial provider (Amsterdam, NL) with strong POI coverage.
// Free tier: 2,500 requests/day. Paid: ~$0.75/1k beyond.
//
// API: GET https://api.tomtom.com/search/2/search/<query>.json
//   Required: key=<API_KEY>
//   Optional: limit, countrySet (comma ISO 3166-1), lat+lon (bias),
//             topLeft+btmRight (bbox), language, idxSet, ofs (offset)
//   Response: { summary: {...},
//               results: [{ type, id, score,
//                           address: { freeformAddress, country, countryCode,
//                                      municipality, postalCode, street,
//                                      streetNumber, ... },
//                           position: { lat, lon },
//                           viewport: { topLeftPoint, btmRightPoint },
//                           poi: { name, categories, classifications } }] }
//
// Residency: EU (TomTom International BV, Amsterdam NL). Signed DPA on
// enterprise contracts; covered-by-SCC otherwise. Admissible under eu_strict.
// ---------------------------------------------------------------------------

const (
	tomtomPluginID          = SourceTomTom
	tomtomPluginName        = "TomTom Search"
	tomtomPluginDescription = "Commercial EU-resident place search by TomTom (Amsterdam, NL). Strong POI coverage, 2,500 free requests/day. EU-resident; admissible under eu_strict."

	tomtomDefaultBaseURL    = "https://api.tomtom.com"
	tomtomSearchPathPrefix  = "/search/2/search/"
	tomtomSearchPathSuffix  = ".json"
	tomtomQueryParamKey     = "key"
	tomtomQueryParamLimit   = "limit"
	tomtomQueryParamCountry = "countrySet"
	tomtomQueryParamLat     = "lat"
	tomtomQueryParamLon     = "lon"
	tomtomQueryParamLang    = "language"

	tomtomDefaultLimit = 10
	tomtomMaxLimitCap  = 100
	tomtomDefaultRPS   = 5.0
	tomtomAcceptHeader = "Accept"
	tomtomAcceptJSON   = "application/json"

	tomtomCategoriesHint = "places + POIs (TomTom commercial dataset, strong European coverage)"
)

// Extra-key constants.
const (
	tomtomExtraCountrySet = "country_set" // comma-separated ISO 3166-1 (e.g. "DE,FR,NL")
	tomtomExtraLanguage   = "language"    // IETF BCP 47 (e.g. "en-US", "de-DE")
	tomtomExtraBiasLat    = "bias_lat"
	tomtomExtraBiasLon    = "bias_lon"
)

// ---------------------------------------------------------------------------
// TomTom wire types
// ---------------------------------------------------------------------------

type tomtomSearchResponse struct {
	Summary tomtomSummary  `json:"summary"`
	Results []tomtomResult `json:"results"`
}

type tomtomSummary struct {
	Query        string `json:"query"`
	NumResults   int    `json:"numResults"`
	TotalResults int    `json:"totalResults"`
	Offset       int    `json:"offset"`
}

type tomtomResult struct {
	Type        string             `json:"type"` // "POI", "Street", "Geography", "Address", "Point Address"
	ID          string             `json:"id"`
	Score       float64            `json:"score"`
	Address     tomtomAddress      `json:"address"`
	Position    tomtomPosition     `json:"position"`
	POI         *tomtomPOI         `json:"poi,omitempty"`
	DataSources *tomtomDataSources `json:"dataSources,omitempty"`
}

type tomtomAddress struct {
	StreetNumber       string `json:"streetNumber,omitempty"`
	Street             string `json:"streetName,omitempty"`
	Municipality       string `json:"municipality,omitempty"` // city
	MunicipalitySubdiv string `json:"municipalitySubdivision,omitempty"`
	CountrySecondary   string `json:"countrySecondarySubdivision,omitempty"` // state-like
	CountrySubdivision string `json:"countrySubdivision,omitempty"`
	PostalCode         string `json:"postalCode,omitempty"`
	ExtendedPostalCode string `json:"extendedPostalCode,omitempty"`
	CountryCode        string `json:"countryCode,omitempty"`
	Country            string `json:"country,omitempty"`
	Freeform           string `json:"freeformAddress,omitempty"`
}

type tomtomPosition struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type tomtomPOI struct {
	Name            string                 `json:"name,omitempty"`
	Phone           string                 `json:"phone,omitempty"`
	URL             string                 `json:"url,omitempty"`
	Categories      []string               `json:"categories,omitempty"`
	Classifications []tomtomClassification `json:"classifications,omitempty"`
}

type tomtomClassification struct {
	Code  string               `json:"code,omitempty"`
	Names []tomtomCategoryName `json:"names,omitempty"`
}

type tomtomCategoryName struct {
	NameLocale string `json:"nameLocale,omitempty"`
	Name       string `json:"name,omitempty"`
}

type tomtomDataSources struct {
	Geometry *tomtomGeometryRef `json:"geometry,omitempty"`
}

type tomtomGeometryRef struct {
	ID string `json:"id,omitempty"`
}

// ---------------------------------------------------------------------------
// TomTomPlugin
// ---------------------------------------------------------------------------

// TomTomPlugin implements SourcePlugin for TomTom Search.
// Thread-safe after Initialize.
type TomTomPlugin struct {
	baseURL    string
	apiKey     string
	countrySet string
	language   string
	biasLat    string
	biasLon    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "tomtom".
func (p *TomTomPlugin) ID() string { return tomtomPluginID }

// Name returns the human-readable label.
func (p *TomTomPlugin) Name() string { return tomtomPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *TomTomPlugin) Description() string { return tomtomPluginDescription }

// ContentTypes — TomTom emits place.
func (p *TomTomPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePlace}
}

// NativeFormat — JSON.
func (p *TomTomPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *TomTomPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports TomTom's filter/sort surface.
func (p *TomTomPlugin) Capabilities() SourceCapabilities {
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
		SupportsPagination:       true, // via ofs
		MaxResultsPerQuery:       tomtomMaxLimitCap,
		CategoriesHint:           tomtomCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentReference},
		Kinds:                    []ResultKind{KindPlace},
		RequiresCredential:       true,
	}
}

// Residency — EU (Amsterdam, NL).
func (*TomTomPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionEU,
		DPAStatus:      DPASigned,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *TomTomPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = tomtomDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = tomtomDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.countrySet = stringFromExtra(cfg.Extra, tomtomExtraCountrySet, "")
	p.language = stringFromExtra(cfg.Extra, tomtomExtraLanguage, "")
	p.biasLat = stringFromExtra(cfg.Extra, tomtomExtraBiasLat, "")
	p.biasLon = stringFromExtra(cfg.Extra, tomtomExtraBiasLon, "")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *TomTomPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a TomTom Search query.
func (p *TomTomPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, tomtomPluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: tomtom requires an API key", ErrCredentialRequired)
	}

	limit := params.Limit
	if limit <= 0 {
		limit = tomtomDefaultLimit
	}
	if limit > tomtomMaxLimitCap {
		limit = tomtomMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params.Query, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Results))
	for _, r := range resp.Results {
		pubs = append(pubs, tomtomResultToPublication(r))
	}
	hasMore := resp.Summary.TotalResults > resp.Summary.NumResults+resp.Summary.Offset
	return &SearchResult{
		Total:   resp.Summary.TotalResults,
		Results: pubs,
		HasMore: hasMore,
	}, nil
}

// Get is not supported — TomTom's per-place detail endpoint is a separate
// product (Place Details). Out of scope for cycle 3.
func (p *TomTomPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: tomtom Place Details is not wired in cycle 3", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *TomTomPlugin) doSearch(ctx context.Context, query string, limit int, apiKey string) (*tomtomSearchResponse, error) {
	// TomTom embeds the query in the path; URL-encode it.
	encodedQ := url.PathEscape(query)
	q := url.Values{}
	q.Set(tomtomQueryParamKey, apiKey)
	q.Set(tomtomQueryParamLimit, strconv.Itoa(limit))
	if p.countrySet != "" {
		q.Set(tomtomQueryParamCountry, p.countrySet)
	}
	if p.language != "" {
		q.Set(tomtomQueryParamLang, p.language)
	}
	if p.biasLat != "" && p.biasLon != "" {
		q.Set(tomtomQueryParamLat, p.biasLat)
		q.Set(tomtomQueryParamLon, p.biasLon)
	}

	reqURL := p.baseURL + tomtomSearchPathPrefix + encodedQ + tomtomSearchPathSuffix + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("tomtom: build request: %w", err)
	}
	req.Header.Set(tomtomAcceptHeader, tomtomAcceptJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tomtom: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: tomtom returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: tomtom", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("tomtom: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp tomtomSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("tomtom: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

// tomtomResultToPublication maps a single TomTom result to a Publication.
func tomtomResultToPublication(r tomtomResult) Publication {
	lat := r.Position.Lat
	lon := r.Position.Lon

	title := ""
	if r.POI != nil && r.POI.Name != "" {
		title = r.POI.Name
	}
	if title == "" {
		title = r.Address.Freeform
	}
	if title == "" {
		title = strings.TrimSpace(r.Address.Street + " " + r.Address.StreetNumber)
	}

	address := r.Address.Freeform
	if address == "" {
		address = composeTomTomAddress(r.Address)
	}

	meta := map[string]any{}
	if r.Address.Country != "" {
		meta[smetaCountry] = r.Address.Country
	}
	if r.Address.CountryCode != "" {
		meta[smetaCountryCode] = strings.ToUpper(r.Address.CountryCode)
	}
	if r.Address.Municipality != "" {
		meta[smetaCity] = r.Address.Municipality
	}
	if r.Address.CountrySubdivision != "" {
		meta[smetaState] = r.Address.CountrySubdivision
	}
	if r.Address.PostalCode != "" {
		meta[smetaPostcode] = r.Address.PostalCode
	}
	if r.Address.Street != "" {
		meta[smetaStreet] = r.Address.Street
	}
	if r.Address.StreetNumber != "" {
		meta[smetaHouseNumber] = r.Address.StreetNumber
	}
	if pt := deriveTomTomPlaceType(r); pt != "" {
		meta[smetaPlaceType] = pt
	}
	if r.POI != nil && len(r.POI.Categories) > 0 {
		meta[smetaCategories] = append([]string(nil), r.POI.Categories...)
	}
	// TomTom score is 0–unbounded — treat as relevance signal. Normalize to
	// [0,1] importance estimate via 1 - 1/(1+score) so callers have a uniform
	// signal between Photon/Nominatim/TomTom.
	if r.Score > 0 {
		imp := 1.0 - 1.0/(1.0+r.Score)
		meta[smetaImportance] = imp
	}

	latPtr := lat
	lonPtr := lon
	return Publication{
		ID:             fmt.Sprintf("%s:%s", SourceTomTom, r.ID),
		Source:         SourceTomTom,
		ContentType:    ContentTypePlace,
		Title:          title,
		Address:        address,
		Lat:            &latPtr,
		Lon:            &lonPtr,
		SourceMetadata: meta,
	}
}

// composeTomTomAddress assembles a single-line formatted address.
func composeTomTomAddress(a tomtomAddress) string {
	var parts []string
	street := a.Street
	if a.StreetNumber != "" && street != "" {
		street = street + " " + a.StreetNumber
	}
	if street != "" {
		parts = append(parts, street)
	}
	if a.PostalCode != "" && a.Municipality != "" {
		parts = append(parts, a.PostalCode+" "+a.Municipality)
	} else if a.Municipality != "" {
		parts = append(parts, a.Municipality)
	}
	if a.CountrySubdivision != "" {
		parts = append(parts, a.CountrySubdivision)
	}
	if a.Country != "" {
		parts = append(parts, a.Country)
	}
	return strings.Join(parts, ", ")
}

// deriveTomTomPlaceType maps TomTom's `type` field to retrievr's coarse
// place_type vocabulary. Unknown / empty falls through to "".
func deriveTomTomPlaceType(r tomtomResult) string {
	switch r.Type {
	case "POI":
		return "poi"
	case "Street":
		return "street"
	case "Geography":
		return "geography"
	case "Address", "Point Address":
		return "address"
	}
	return ""
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *TomTomPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *TomTomPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = sanitizeHealthError(err)
	}
}
