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
// Photon (Komoot) place-search provider — v3 cycle 3 / v2.4.0.
//
// OpenStreetMap-based geocoder developed by Komoot (Berlin, DE). Apache 2.0
// licensed; either run against the public throttled endpoint at
// photon.komoot.io or self-host (planet DB ~95 GB, 64 GB RAM recommended).
//
// API: GET https://photon.komoot.io/api
//   Required: q=<query>
//   Optional: limit, lang, lat+lon (bias point), bbox, layer, osm_tag
//   Auth: none
//   Response: GeoJSON FeatureCollection
//     { type: "FeatureCollection",
//       features: [{ type: "Feature",
//                    geometry: { type: "Point", coordinates: [lon, lat] },
//                    properties: { osm_id, osm_type, osm_key, osm_value,
//                                  name, country, countrycode, state, city,
//                                  street, housenumber, postcode, type,
//                                  extent } }] }
//
// Residency: EU (Komoot, Berlin DE). Admissible under eu_strict.
// ---------------------------------------------------------------------------

const (
	photonPluginID          = SourcePhoton
	photonPluginName        = "Photon (Komoot)"
	photonPluginDescription = "OpenStreetMap-based geocoder by Komoot (Berlin, DE). Search-as-you-type tolerant, typo-correcting. Apache 2.0 OSS — public endpoint or self-host. EU-resident; admissible under eu_strict."

	photonDefaultBaseURL = "https://photon.komoot.io"
	photonSearchPath     = "/api"

	photonDefaultLimit  = 10
	photonMaxLimitCap   = 50
	photonDefaultRPS    = 2.0
	photonAcceptHeader  = "Accept"
	photonAcceptJSON    = "application/json"
	photonAcceptEncHdr  = "Accept-Encoding"
	photonAcceptEncIden = "identity"

	photonCategoriesHint = "places, streets, points-of-interest from OpenStreetMap (Photon focuses on autocomplete/typo-tolerance)"
)

// Extra-key constants.
const (
	photonExtraLang    = "lang"     // ISO 639-1 (e.g. "en", "de"). Default: not set
	photonExtraBiasLat = "bias_lat" // location bias point (string-encoded float)
	photonExtraBiasLon = "bias_lon"
)

// ---------------------------------------------------------------------------
// Photon wire types
// ---------------------------------------------------------------------------

type photonFeatureCollection struct {
	Type     string          `json:"type"`
	Features []photonFeature `json:"features"`
}

type photonFeature struct {
	Type       string           `json:"type"`
	Geometry   photonGeometry   `json:"geometry"`
	Properties photonProperties `json:"properties"`
}

type photonGeometry struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"` // [lon, lat]
}

type photonProperties struct {
	OSMID       int64     `json:"osm_id"`
	OSMType     string    `json:"osm_type"`
	OSMKey      string    `json:"osm_key"`
	OSMValue    string    `json:"osm_value"`
	Name        string    `json:"name"`
	Country     string    `json:"country,omitempty"`
	CountryCode string    `json:"countrycode,omitempty"`
	State       string    `json:"state,omitempty"`
	City        string    `json:"city,omitempty"`
	Street      string    `json:"street,omitempty"`
	HouseNumber string    `json:"housenumber,omitempty"`
	Postcode    string    `json:"postcode,omitempty"`
	Type        string    `json:"type,omitempty"`
	Extent      []float64 `json:"extent,omitempty"` // bbox: [minlon, maxlat, maxlon, minlat]
}

// ---------------------------------------------------------------------------
// PhotonPlugin
// ---------------------------------------------------------------------------

// PhotonPlugin implements SourcePlugin for the Photon geocoder.
// Thread-safe after Initialize.
type PhotonPlugin struct {
	baseURL     string
	defaultLang string
	biasLat     string
	biasLon     string
	httpClient  *http.Client
	enabled     bool
	rateLimit   float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "photon".
func (p *PhotonPlugin) ID() string { return photonPluginID }

// Name returns the human-readable label.
func (p *PhotonPlugin) Name() string { return photonPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *PhotonPlugin) Description() string { return photonPluginDescription }

// ContentTypes — Photon emits place.
func (p *PhotonPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePlace}
}

// NativeFormat — JSON.
func (p *PhotonPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *PhotonPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Photon's filter/sort surface.
func (p *PhotonPlugin) Capabilities() SourceCapabilities {
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
		MaxResultsPerQuery:       photonMaxLimitCap,
		CategoriesHint:           photonCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentReference},
		Kinds:                    []ResultKind{KindPlace},
	}
}

// Residency — EU (Komoot, Berlin DE). Self-hosted instances stay EU when
// run on EU infra; the residency tag here describes the default public
// endpoint operator and is the input to the eu_strict admission gate.
func (*PhotonPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionEU,
		DPAStatus:      DPANotApplicable, // OSS endpoint; no personal data processed beyond IP for rate limiting
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *PhotonPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = photonDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = photonDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.defaultLang = stringFromExtra(cfg.Extra, photonExtraLang, "")
	p.biasLat = stringFromExtra(cfg.Extra, photonExtraBiasLat, "")
	p.biasLon = stringFromExtra(cfg.Extra, photonExtraBiasLon, "")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *PhotonPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Photon geocoding query.
func (p *PhotonPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = photonDefaultLimit
	}
	if limit > photonMaxLimitCap {
		limit = photonMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params.Query, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Features))
	for _, f := range resp.Features {
		pubs = append(pubs, photonFeatureToPublication(f))
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false, // Photon doesn't paginate the autocomplete endpoint
	}, nil
}

// Get is not supported — Photon has no per-OSM-ID lookup endpoint; the
// search endpoint covers the same surface for any known place name.
// Callers wanting OSM-ID resolution should consult Nominatim's /lookup.
func (p *PhotonPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: photon has no Get; use Search with the place name", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *PhotonPlugin) doSearch(ctx context.Context, query string, limit int) (*photonFeatureCollection, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("limit", strconv.Itoa(limit))
	if p.defaultLang != "" {
		q.Set("lang", p.defaultLang)
	}
	if p.biasLat != "" && p.biasLon != "" {
		q.Set("lat", p.biasLat)
		q.Set("lon", p.biasLon)
	}

	reqURL := p.baseURL + photonSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("photon: build request: %w", err)
	}
	req.Header.Set(photonAcceptHeader, photonAcceptJSON)
	req.Header.Set(photonAcceptEncHdr, photonAcceptEncIden)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("photon: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: photon", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("photon: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var fc photonFeatureCollection
	if err := json.NewDecoder(httpResp.Body).Decode(&fc); err != nil {
		return nil, fmt.Errorf("photon: decode response: %w", err)
	}
	return &fc, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

// photonFeatureToPublication maps a single GeoJSON feature to a place
// Publication. Coordinates are [lon, lat] in GeoJSON — note the order.
func photonFeatureToPublication(f photonFeature) Publication {
	var lat, lon *float64
	if len(f.Geometry.Coordinates) >= 2 {
		lonV := f.Geometry.Coordinates[0]
		latV := f.Geometry.Coordinates[1]
		lat = &latV
		lon = &lonV
	}

	title := f.Properties.Name
	if title == "" {
		title = composePhotonName(f.Properties)
	}
	address := composePhotonAddress(f.Properties)

	// osm_id composite key: "<osm_type>:<osm_id>". Matches Nominatim's
	// canonical form so the dedup index merges cross-provider duplicates.
	osmIDComposite := ""
	if f.Properties.OSMType != "" && f.Properties.OSMID != 0 {
		osmIDComposite = fmt.Sprintf("%s:%d", f.Properties.OSMType, f.Properties.OSMID)
	}

	meta := map[string]any{}
	if osmIDComposite != "" {
		meta[MetaKeyOSMID] = osmIDComposite
	}
	if f.Properties.OSMType != "" {
		meta[smetaOSMType] = f.Properties.OSMType
	}
	if f.Properties.Country != "" {
		meta[smetaCountry] = f.Properties.Country
	}
	if f.Properties.CountryCode != "" {
		meta[smetaCountryCode] = strings.ToUpper(f.Properties.CountryCode)
	}
	if f.Properties.City != "" {
		meta[smetaCity] = f.Properties.City
	}
	if f.Properties.State != "" {
		meta[smetaState] = f.Properties.State
	}
	if f.Properties.Postcode != "" {
		meta[smetaPostcode] = f.Properties.Postcode
	}
	if f.Properties.Street != "" {
		meta[smetaStreet] = f.Properties.Street
	}
	if f.Properties.HouseNumber != "" {
		meta[smetaHouseNumber] = f.Properties.HouseNumber
	}
	if pt := derivePhotonPlaceType(f.Properties); pt != "" {
		meta[smetaPlaceType] = pt
	}

	// Deterministic per-result ID: osm_type:osm_id when present, else hash
	// the title+coordinates so repeated runs are stable.
	id := osmIDComposite
	if id == "" {
		id = fmt.Sprintf("photon:%s", hashURL(fmt.Sprintf("%s|%v|%v", title, lat, lon)))
	} else {
		id = fmt.Sprintf("%s:%s", SourcePhoton, id)
	}

	return Publication{
		ID:             id,
		Source:         SourcePhoton,
		ContentType:    ContentTypePlace,
		Title:          title,
		Address:        address,
		Lat:            lat,
		Lon:            lon,
		SourceMetadata: meta,
	}
}

// composePhotonName builds a fallback display name when Properties.Name is
// empty (typical for pure-address results without a POI name).
func composePhotonName(pr photonProperties) string {
	var parts []string
	if pr.HouseNumber != "" && pr.Street != "" {
		parts = append(parts, pr.Street+" "+pr.HouseNumber)
	} else if pr.Street != "" {
		parts = append(parts, pr.Street)
	}
	if pr.City != "" {
		parts = append(parts, pr.City)
	}
	if pr.Country != "" {
		parts = append(parts, pr.Country)
	}
	if len(parts) == 0 {
		return pr.OSMKey + "=" + pr.OSMValue
	}
	return strings.Join(parts, ", ")
}

// composePhotonAddress assembles a single-line formatted address from the
// available administrative parts.
func composePhotonAddress(pr photonProperties) string {
	var parts []string
	street := pr.Street
	if pr.HouseNumber != "" && street != "" {
		street = street + " " + pr.HouseNumber
	}
	if street != "" {
		parts = append(parts, street)
	}
	if pr.Postcode != "" && pr.City != "" {
		parts = append(parts, pr.Postcode+" "+pr.City)
	} else if pr.City != "" {
		parts = append(parts, pr.City)
	}
	if pr.State != "" {
		parts = append(parts, pr.State)
	}
	if pr.Country != "" {
		parts = append(parts, pr.Country)
	}
	return strings.Join(parts, ", ")
}

// derivePhotonPlaceType reduces the osm_key / osm_value pair to a coarse
// place_type for downstream consumers.
func derivePhotonPlaceType(pr photonProperties) string {
	switch pr.OSMKey {
	case "place":
		return pr.OSMValue // city, town, village, hamlet, ...
	case "highway":
		return "street"
	case "building":
		return "building"
	case "amenity", "shop", "tourism", "leisure", "office":
		return "poi"
	}
	if pr.Type != "" {
		return pr.Type
	}
	return ""
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *PhotonPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *PhotonPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
