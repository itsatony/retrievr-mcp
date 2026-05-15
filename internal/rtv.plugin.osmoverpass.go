package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// OSM Overpass — v6 cycle 1 / v2.14.0.
//
// API: POST https://overpass-api.de/api/interpreter
//   Body: raw Overpass QL (text/plain). The plugin synthesizes a
//   default name-regex query from the free-text input:
//
//     [out:json][timeout:25];
//     (
//       node["name"~"<q>",i];
//       way["name"~"<q>",i];
//       relation["name"~"<q>",i];
//     );
//     out center <limit>;
//
//   Power users can pass raw Overpass QL via SearchParams.Filters.Categories[0]
//   when it begins with "[out:" — the plugin treats it as a verbatim query.
//
// Response:
//   { "version":0.6, "generator":"Overpass API",
//     "elements": [
//       { "type":"node|way|relation", "id":int,
//         "lat":float, "lon":float,            (nodes)
//         "center": {"lat":float,"lon":float}, (ways/relations w/ out center)
//         "tags": {"name":"...","amenity":"..."} } ] }
//
// Free, no auth required. The default mirror has a fair-use cap (~10k
// queries/day per IP). Power users should self-host or configure an
// alternate mirror via `base_url`.
//
// Residency: EU (Heidelberg primary mirror; n/a DPA).
// ---------------------------------------------------------------------------

const (
	osmOverpassPluginID          = SourceOSMOverpass
	osmOverpassPluginName        = "OSM Overpass"
	osmOverpassPluginDescription = "Query OpenStreetMap via the Overpass API. Free, no auth required. Default query is a name regex over nodes/ways/relations; pass raw Overpass QL beginning with '[out:' via filters.categories[0] for full control. EU-hosted by default (overpass-api.de Heidelberg mirror)."

	osmOverpassDefaultBaseURL  = "https://overpass-api.de"
	osmOverpassInterpreterPath = "/api/interpreter"
	osmOverpassDefaultLimit    = 25
	osmOverpassMaxLimitCap     = 100
	osmOverpassDefaultRPS      = 1.0
	osmOverpassDefaultTimeout  = 30 * time.Second

	osmOverpassIDPrefix = "osmoverpass:"

	osmOverpassCategoriesHint = "OSM Overpass: pass a raw QL query as filters.categories[0] when it starts with '[out:' (it bypasses the default name-regex generation). Otherwise the plugin uses the free-text query as a case-insensitive name regex over node/way/relation tags."

	osmOverpassMetaKeyOSMType = "osm_type"
	osmOverpassMetaKeyTags    = "osm_tags"

	// osmOverpassQLPrefix marks user-supplied verbatim Overpass QL in
	// filters.categories[0]. Anything starting with this prefix is
	// considered raw QL. The raw-QL path is opt-in via
	// extra.allow_raw_ql=true (defaults off) per the security review:
	// unbounded QL is a resource-exhaustion + SSRF vector.
	osmOverpassQLPrefix = "[out:"

	// osmOverpassMaxRawQLBytes caps the length of operator-supplied raw
	// Overpass QL bodies. Generous enough for complex bbox queries,
	// small enough to keep DoS risk bounded.
	osmOverpassMaxRawQLBytes = 2048

	// osmOverpassRawQLTimeoutSeconds is the server-controlled
	// [timeout:N] value spliced into raw QL when the caller-supplied
	// value is missing or exceeds the cap. Matches the synthesized
	// default-query timeout so the two paths share a single ceiling.
	osmOverpassRawQLTimeoutSeconds = 25

	osmOverpassExtraAllowRawQL = "allow_raw_ql"
)

// osmOverpassTimeoutDirectiveRegex matches the `[timeout:N]` setting
// within a raw Overpass QL body. The plugin clamps N down to
// osmOverpassRawQLTimeoutSeconds when it exceeds the ceiling.
var osmOverpassTimeoutDirectiveRegex = regexp.MustCompile(`\[timeout:\s*(\d+)\s*\]`)

// ---------------------------------------------------------------------------
// Overpass wire types
// ---------------------------------------------------------------------------

type overpassResponse struct {
	Elements []overpassElement `json:"elements,omitempty"`
}

type overpassElement struct {
	Type   string            `json:"type,omitempty"`
	ID     int64             `json:"id,omitempty"`
	Lat    float64           `json:"lat,omitempty"`
	Lon    float64           `json:"lon,omitempty"`
	Center *overpassLatLon   `json:"center,omitempty"`
	Tags   map[string]string `json:"tags,omitempty"`
}

type overpassLatLon struct {
	Lat float64 `json:"lat,omitempty"`
	Lon float64 `json:"lon,omitempty"`
}

// ---------------------------------------------------------------------------
// OSMOverpassPlugin
// ---------------------------------------------------------------------------

// OSMOverpassPlugin implements SourcePlugin for the OSM Overpass API.
// Thread-safe after Initialize.
type OSMOverpassPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	// allowRawQL gates the verbatim-QL escape hatch. Operators must
	// explicitly opt in via extra.allow_raw_ql=true; the default-off
	// posture keeps DoS / SSRF risk bounded.
	allowRawQL bool

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "osmoverpass".
func (p *OSMOverpassPlugin) ID() string { return osmOverpassPluginID }

// Name returns the human-readable label.
func (p *OSMOverpassPlugin) Name() string { return osmOverpassPluginName }

// Description returns the LLM-facing one-liner.
func (p *OSMOverpassPlugin) Description() string { return osmOverpassPluginDescription }

// ContentTypes — place.
func (p *OSMOverpassPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePlace} }

// NativeFormat — JSON.
func (p *OSMOverpassPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *OSMOverpassPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Overpass's filter/sort surface.
func (p *OSMOverpassPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    false,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       false,
		MaxResultsPerQuery:       osmOverpassMaxLimitCap,
		CategoriesHint:           osmOverpassCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentReference, IntentQuickLookup},
		Kinds:                    []ResultKind{KindPlace},
		RequiresCredential:       false,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *OSMOverpassPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = osmOverpassDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = osmOverpassDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	if cfg.Extra != nil {
		p.allowRawQL = strings.EqualFold(cfg.Extra[osmOverpassExtraAllowRawQL], "true")
	}

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = osmOverpassDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *OSMOverpassPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search runs an Overpass query and maps elements to Publications.
func (p *OSMOverpassPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = osmOverpassDefaultLimit
	}
	if limit > osmOverpassMaxLimitCap {
		limit = osmOverpassMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Elements))
	for i := range resp.Elements {
		if pub, ok := overpassElementToPublication(&resp.Elements[i]); ok {
			pubs = append(pubs, pub)
		}
		if len(pubs) >= limit {
			break
		}
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 1 — Overpass id lookups via
// `node(<id>);out;` could be wired in a future cycle, but
// most callers already use osmoverpass: results' lat/lon for downstream
// lookups.
func (p *OSMOverpassPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: osmoverpass Get is not wired in cycle 1", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *OSMOverpassPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*overpassResponse, error) {
	ql, err := p.buildQL(params, limit)
	if err != nil {
		return nil, err
	}

	reqURL := p.baseURL + osmOverpassInterpreterPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(ql))
	if err != nil {
		return nil, fmt.Errorf("osmoverpass: build request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("osmoverpass: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: osmoverpass", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("osmoverpass: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp overpassResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("osmoverpass: decode response: %w", err)
	}
	return &resp, nil
}

// buildQL synthesizes a default case-insensitive name-regex query, or
// returns a verbatim user-supplied query when filters.categories[0]
// starts with the Overpass QL prelude "[out:". Raw QL is only honored
// when extra.allow_raw_ql=true was configured; otherwise it's rejected
// with ErrInvalidInput. Raw QL is also length-capped and has its
// [timeout:N] directive clamped to osmOverpassRawQLTimeoutSeconds so a
// caller cannot extend the upstream's compute budget arbitrarily.
func (p *OSMOverpassPlugin) buildQL(params SearchParams, limit int) (string, error) {
	if len(params.Filters.Categories) > 0 {
		first := strings.TrimSpace(params.Filters.Categories[0])
		if strings.HasPrefix(first, osmOverpassQLPrefix) {
			if !p.allowRawQL {
				return "", fmt.Errorf("%w: osmoverpass raw Overpass QL is disabled (set extra.allow_raw_ql=true to opt in)", ErrInvalidInput)
			}
			if len(first) > osmOverpassMaxRawQLBytes {
				return "", fmt.Errorf("%w: osmoverpass raw QL exceeds %d-byte cap", ErrInvalidInput, osmOverpassMaxRawQLBytes)
			}
			return clampOverpassTimeout(first), nil
		}
	}
	return overpassDefaultQL(params, limit), nil
}

// clampOverpassTimeout rewrites the `[timeout:N]` directive in a raw
// Overpass QL body so N never exceeds the server-controlled ceiling.
// Bodies that omit the directive are returned unchanged (Overpass'
// own default applies upstream, which is already short).
func clampOverpassTimeout(ql string) string {
	return osmOverpassTimeoutDirectiveRegex.ReplaceAllStringFunc(ql, func(m string) string {
		sub := osmOverpassTimeoutDirectiveRegex.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		n, err := strconv.Atoi(sub[1])
		if err != nil || n > osmOverpassRawQLTimeoutSeconds {
			return "[timeout:" + strconv.Itoa(osmOverpassRawQLTimeoutSeconds) + "]"
		}
		return m
	})
}

// overpassDefaultQL synthesizes the safe default name-regex QL — the
// 95% search-by-keyword path. Power users opt into raw QL via the
// allow_raw_ql flag (handled in buildQL above, not here).
func overpassDefaultQL(params SearchParams, limit int) string {
	// Escape double-quotes in the query to keep the Overpass regex
	// well-formed; backslashes round-trip as-is.
	q := strings.ReplaceAll(strings.TrimSpace(params.Query), `"`, `\"`)
	if q == "" {
		q = ".*"
	}

	return strings.Join([]string{
		"[out:json][timeout:25];",
		"(",
		"  node[\"name\"~\"" + q + "\",i];",
		"  way[\"name\"~\"" + q + "\",i];",
		"  relation[\"name\"~\"" + q + "\",i];",
		");",
		"out center " + strconv.Itoa(limit) + ";",
	}, "\n")
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func overpassElementToPublication(el *overpassElement) (Publication, bool) {
	// Resolve coordinates: nodes carry lat/lon; ways/relations carry
	// center when the query used `out center`.
	var lat, lon float64
	switch {
	case el.Lat != 0 || el.Lon != 0:
		lat, lon = el.Lat, el.Lon
	case el.Center != nil:
		lat, lon = el.Center.Lat, el.Center.Lon
	default:
		return Publication{}, false
	}

	title := el.Tags["name"]
	if title == "" {
		title = el.Tags["name:en"]
	}
	if title == "" {
		// Skip elements without a human-readable name — they're typically
		// raw boundary or coastline geometry and not useful in retrieval.
		return Publication{}, false
	}

	osmIDComposite := el.Type + "/" + strconv.FormatInt(el.ID, 10)

	meta := map[string]any{
		MetaKeyOSMID:              osmIDComposite,
		osmOverpassMetaKeyOSMType: el.Type,
	}
	if len(el.Tags) > 0 {
		meta[osmOverpassMetaKeyTags] = el.Tags
		// Inherit smetaPlaceType when a recognizable tag is present.
		for _, k := range []string{"amenity", "shop", "tourism", "natural", "place"} {
			if v, ok := el.Tags[k]; ok && v != "" {
				meta[smetaPlaceType] = v
				break
			}
		}
	}

	latPtr := lat
	lonPtr := lon

	return Publication{
		ID:             osmOverpassIDPrefix + osmIDComposite,
		Source:         SourceOSMOverpass,
		ContentType:    ContentTypePlace,
		Title:          title,
		Address:        el.Tags["addr:full"],
		Lat:            &latPtr,
		Lon:            &lonPtr,
		SourceMetadata: meta,
	}, true
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *OSMOverpassPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *OSMOverpassPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
