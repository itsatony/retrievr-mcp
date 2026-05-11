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
// Nominatim place-search provider — v3 cycle 3 / v2.4.0.
//
// OpenStreetMap's reference geocoder, hosted by the OSM Foundation (UK).
// **Strict usage policy**: 1 request/second hard cap, custom User-Agent
// required identifying the application + contact email. Self-hosting is
// recommended for any meaningful traffic.
//   https://operations.osmfoundation.org/policies/nominatim/
//
// API: GET https://nominatim.openstreetmap.org/search
//   Required: q=<query>, format=json
//   Optional: limit, accept-language, countrycodes (comma ISO), addressdetails,
//             extratags, namedetails, viewbox, bounded
//   Auth: none, but User-Agent header REQUIRED
//   Response: JSON array of:
//     [{ place_id, licence, osm_type, osm_id, boundingbox, lat, lon,
//        display_name, class, type, importance, address: { ... } }]
//
// Residency: UK (OSMF, UK-adequacy under EU GDPR). Admissible under eu_strict.
// ---------------------------------------------------------------------------

const (
	nominatimPluginID          = SourceNominatim
	nominatimPluginName        = "Nominatim (OSM)"
	nominatimPluginDescription = "OpenStreetMap reference geocoder. Comprehensive global coverage. STRICT policy: 1 req/s hard cap on the public endpoint; self-host for higher volume. UK-adequacy residency; admissible under eu_strict."

	nominatimDefaultBaseURL = "https://nominatim.openstreetmap.org"
	nominatimSearchPath     = "/search"

	// Nominatim Usage Policy: 1 request/second HARD. We enforce this in
	// Initialize regardless of cfg.RateLimit — operators cannot opt out
	// of OSM's policy. Self-hosters override via BaseURL.
	nominatimHardRPS = 1.0

	nominatimDefaultLimit = 10
	nominatimMaxLimitCap  = 50

	nominatimAcceptHeader = "Accept"
	nominatimAcceptJSON   = "application/json"

	// User-Agent header is REQUIRED by OSMF policy. Operators must override
	// this via PluginConfig.Extra["user_agent"] with their app+contact.
	nominatimDefaultUserAgent = "retrievr-mcp/2.4 (+https://github.com/itsatony/retrievr-mcp; please-override-user-agent@example.com)"

	nominatimCategoriesHint = "comprehensive OSM geocoding (places, streets, POIs); slower + strict-policy public endpoint"
)

// Extra-key constants.
const (
	nominatimExtraUserAgent      = "user_agent"
	nominatimExtraAcceptLanguage = "accept_language" // IETF BCP 47 (e.g. "en", "de", "de-DE,en;q=0.7")
	nominatimExtraCountryCodes   = "country_codes"   // comma ISO 3166-1 (e.g. "de,fr")
)

// ---------------------------------------------------------------------------
// Nominatim wire types
// ---------------------------------------------------------------------------

type nominatimResult struct {
	PlaceID     int64            `json:"place_id"`
	Licence     string           `json:"licence,omitempty"`
	OSMType     string           `json:"osm_type"`
	OSMID       int64            `json:"osm_id"`
	BoundingBox []string         `json:"boundingbox,omitempty"` // strings (Nominatim quirk)
	Lat         string           `json:"lat"`                   // strings (Nominatim quirk)
	Lon         string           `json:"lon"`
	DisplayName string           `json:"display_name"`
	Class       string           `json:"class,omitempty"`
	Type        string           `json:"type,omitempty"`
	Importance  float64          `json:"importance,omitempty"`
	Address     nominatimAddress `json:"address,omitempty"`
}

type nominatimAddress struct {
	HouseNumber string `json:"house_number,omitempty"`
	Road        string `json:"road,omitempty"`
	Suburb      string `json:"suburb,omitempty"`
	Village     string `json:"village,omitempty"`
	Town        string `json:"town,omitempty"`
	City        string `json:"city,omitempty"`
	County      string `json:"county,omitempty"`
	State       string `json:"state,omitempty"`
	Postcode    string `json:"postcode,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
}

// ---------------------------------------------------------------------------
// NominatimPlugin
// ---------------------------------------------------------------------------

// NominatimPlugin implements SourcePlugin for the OSM Nominatim geocoder.
// Thread-safe after Initialize.
//
// Compliance note: the 1 req/s policy is enforced at the plugin level (in
// Initialize) regardless of operator config. Self-hosters who bypass the
// public endpoint via BaseURL override the policy at their own discretion;
// retrievr surfaces the rate-limit value but does not police self-hosted
// installations.
type NominatimPlugin struct {
	baseURL        string
	userAgent      string
	acceptLanguage string
	countryCodes   string
	httpClient     *http.Client
	enabled        bool
	rateLimit      float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "nominatim".
func (p *NominatimPlugin) ID() string { return nominatimPluginID }

// Name returns the human-readable label.
func (p *NominatimPlugin) Name() string { return nominatimPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *NominatimPlugin) Description() string { return nominatimPluginDescription }

// ContentTypes — Nominatim emits place.
func (p *NominatimPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePlace}
}

// NativeFormat — JSON.
func (p *NominatimPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *NominatimPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Nominatim's filter/sort surface.
func (p *NominatimPlugin) Capabilities() SourceCapabilities {
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
		MaxResultsPerQuery:       nominatimMaxLimitCap,
		CategoriesHint:           nominatimCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentReference},
		Kinds:                    []ResultKind{KindPlace},
	}
}

// Residency — UK (OSMF), UK-adequacy. Admissible under eu_strict.
func (*NominatimPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUKAdequacy,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
//
// Enforces OSMF policy:
//   - User-Agent must be non-empty (default placeholder included, but
//     operators MUST override with their own app+contact).
//   - Rate limit clamped to nominatimHardRPS (1 req/s) UNLESS BaseURL is
//     overridden (self-host indicator).
func (p *NominatimPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled

	p.baseURL = cfg.BaseURL
	selfHosted := p.baseURL != "" && p.baseURL != nominatimDefaultBaseURL
	if p.baseURL == "" {
		p.baseURL = nominatimDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	// Rate limit: hard cap on the public endpoint, configurable when self-hosted.
	p.rateLimit = cfg.RateLimit
	if !selfHosted {
		// OSMF policy is non-negotiable on the public endpoint.
		p.rateLimit = nominatimHardRPS
	} else if p.rateLimit <= 0 {
		p.rateLimit = nominatimHardRPS
	}

	p.userAgent = stringFromExtra(cfg.Extra, nominatimExtraUserAgent, nominatimDefaultUserAgent)
	p.acceptLanguage = stringFromExtra(cfg.Extra, nominatimExtraAcceptLanguage, "")
	p.countryCodes = stringFromExtra(cfg.Extra, nominatimExtraCountryCodes, "")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *NominatimPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Nominatim search.
func (p *NominatimPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = nominatimDefaultLimit
	}
	if limit > nominatimMaxLimitCap {
		limit = nominatimMaxLimitCap
	}

	results, err := p.doSearch(ctx, params.Query, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(results))
	for _, r := range results {
		pubs = append(pubs, nominatimResultToPublication(r))
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 3. Nominatim's /lookup endpoint accepts an
// osm_type+osm_id pair, but the plumbing (pre-fixed ID parsing for
// composite "type:id" rawIDs) is out of scope for v2.4.0.
func (p *NominatimPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: nominatim Get is not wired in cycle 3 (use Search)", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *NominatimPlugin) doSearch(ctx context.Context, query string, limit int) ([]nominatimResult, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("limit", strconv.Itoa(limit))
	q.Set("addressdetails", "1")
	if p.acceptLanguage != "" {
		q.Set("accept-language", p.acceptLanguage)
	}
	if p.countryCodes != "" {
		q.Set("countrycodes", p.countryCodes)
	}

	reqURL := p.baseURL + nominatimSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("nominatim: build request: %w", err)
	}
	req.Header.Set(nominatimAcceptHeader, nominatimAcceptJSON)
	req.Header.Set("User-Agent", p.userAgent)
	if p.acceptLanguage != "" {
		req.Header.Set("Accept-Language", p.acceptLanguage)
	}

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nominatim: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		// Nominatim returns 429 when policy is violated; surface explicitly.
		return nil, fmt.Errorf("%w: nominatim", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusForbidden:
		// OSMF bans abusive User-Agents with 403.
		return nil, fmt.Errorf("%w: nominatim returned 403 — verify User-Agent compliance with OSMF policy", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("nominatim: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var results []nominatimResult
	if err := json.NewDecoder(httpResp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("nominatim: decode response: %w", err)
	}
	return results, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func nominatimResultToPublication(r nominatimResult) Publication {
	lat, _ := strconv.ParseFloat(r.Lat, 64)
	lon, _ := strconv.ParseFloat(r.Lon, 64)

	// osm_id composite key matches Photon's form: "<osm_type>:<osm_id>".
	osmIDComposite := ""
	if r.OSMType != "" && r.OSMID != 0 {
		osmIDComposite = fmt.Sprintf("%s:%d", r.OSMType, r.OSMID)
	}

	meta := map[string]any{}
	if osmIDComposite != "" {
		meta[MetaKeyOSMID] = osmIDComposite
	}
	if r.OSMType != "" {
		meta[smetaOSMType] = r.OSMType
	}
	if r.Address.Country != "" {
		meta[smetaCountry] = r.Address.Country
	}
	if r.Address.CountryCode != "" {
		meta[smetaCountryCode] = strings.ToUpper(r.Address.CountryCode)
	}
	city := firstNonEmpty(r.Address.City, r.Address.Town, r.Address.Village)
	if city != "" {
		meta[smetaCity] = city
	}
	if r.Address.State != "" {
		meta[smetaState] = r.Address.State
	}
	if r.Address.Postcode != "" {
		meta[smetaPostcode] = r.Address.Postcode
	}
	if r.Address.Road != "" {
		meta[smetaStreet] = r.Address.Road
	}
	if r.Address.HouseNumber != "" {
		meta[smetaHouseNumber] = r.Address.HouseNumber
	}
	if pt := deriveNominatimPlaceType(r); pt != "" {
		meta[smetaPlaceType] = pt
	}
	if r.Importance > 0 {
		meta[smetaImportance] = r.Importance
	}

	latPtr := lat
	lonPtr := lon
	id := osmIDComposite
	if id == "" {
		id = fmt.Sprintf("nominatim:place_%d", r.PlaceID)
	} else {
		id = fmt.Sprintf("%s:%s", SourceNominatim, id)
	}

	return Publication{
		ID:             id,
		Source:         SourceNominatim,
		ContentType:    ContentTypePlace,
		Title:          r.DisplayName,
		Address:        r.DisplayName,
		Lat:            &latPtr,
		Lon:            &lonPtr,
		License:        r.Licence,
		SourceMetadata: meta,
	}
}

// deriveNominatimPlaceType maps Nominatim's class/type to retrievr's coarse
// place_type vocabulary.
func deriveNominatimPlaceType(r nominatimResult) string {
	switch r.Class {
	case "place":
		return r.Type // city / town / village / suburb / ...
	case "highway":
		return "street"
	case "building":
		return "building"
	case "amenity", "shop", "tourism", "leisure", "office":
		return "poi"
	case "boundary":
		return "boundary"
	}
	if r.Type != "" {
		return r.Type
	}
	return ""
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *NominatimPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *NominatimPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
