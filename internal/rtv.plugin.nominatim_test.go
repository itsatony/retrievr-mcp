package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v3 cycle 3 / v2.4.0: Nominatim (OSM) tests.
//
// Special invariants beyond the other place plugins:
//   - User-Agent header MUST always be sent (OSMF policy).
//   - Rate limit on the PUBLIC endpoint MUST be 1 RPS regardless of cfg.
//   - Self-hosted (overridden BaseURL) is allowed to raise the rate limit.
// ---------------------------------------------------------------------------

const (
	nominatimTestUserAgent = "retrievr-test/1.0 (test@example.com)"
)

func newNominatimTestPlugin(t *testing.T, baseURL string, rate float64) *NominatimPlugin {
	t.Helper()
	p := &NominatimPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		RateLimit: rate,
		Extra: map[string]string{
			nominatimExtraUserAgent:      nominatimTestUserAgent,
			nominatimExtraAcceptLanguage: "en",
		},
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildNominatimTestResponse(results []nominatimResult) string {
	b, _ := json.Marshal(results)
	return string(b)
}

func TestNominatim_Identity(t *testing.T) {
	t.Parallel()
	p := &NominatimPlugin{}
	assert.Equal(t, "nominatim", p.ID())
}

func TestNominatim_Capabilities(t *testing.T) {
	t.Parallel()
	p := &NominatimPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPlace)
}

func TestNominatim_Residency_IsUKAdequacy_AdmissibleUnderEUStrict(t *testing.T) {
	t.Parallel()
	p := &NominatimPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUKAdequacy, tag.Region)
	assert.True(t, tag.Region.IsEU(), "UK-adequacy must satisfy IsEU() under eu_strict")
}

// TestNominatim_PublicEndpointForces1RPS proves the 1 req/s OSMF policy
// is non-negotiable when the operator uses the default public endpoint,
// even if config tries to raise it.
func TestNominatim_PublicEndpointForces1RPS(t *testing.T) {
	t.Parallel()
	p := &NominatimPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   "",  // empty → defaults to public endpoint
		RateLimit: 100, // operator tries to raise — must be clamped
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	assert.Equal(t, nominatimHardRPS, p.Health(context.Background()).RateLimit,
		"OSMF policy: public endpoint must be clamped to 1 RPS regardless of cfg")
}

// TestNominatim_SelfHostedAllowsHigherRate confirms operators running
// their own instance can raise the rate limit by overriding BaseURL.
func TestNominatim_SelfHostedAllowsHigherRate(t *testing.T) {
	t.Parallel()
	p := &NominatimPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   "https://nominatim.internal.example.com",
		RateLimit: 50,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	assert.Equal(t, 50.0, p.Health(context.Background()).RateLimit,
		"self-hosted: cfg RateLimit must be honored")
}

func TestNominatim_Search_HappyPath(t *testing.T) {
	t.Parallel()
	results := []nominatimResult{
		{
			PlaceID:     12345,
			OSMID:       240109189,
			OSMType:     "node",
			Lat:         "52.5162895",
			Lon:         "13.3777018",
			DisplayName: "Brandenburger Tor, Pariser Platz, Mitte, Berlin, 10117, Germany",
			Class:       "tourism",
			Type:        "attraction",
			Importance:  0.85,
			Licence:     "Data © OpenStreetMap contributors, ODbL 1.0.",
			Address: nominatimAddress{
				Road:        "Pariser Platz",
				City:        "Berlin",
				State:       "Berlin",
				Postcode:    "10117",
				Country:     "Germany",
				CountryCode: "de",
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, nominatimSearchPath, r.URL.Path)
		assert.Equal(t, nominatimTestUserAgent, r.Header.Get("User-Agent"),
			"User-Agent header is REQUIRED by OSMF policy and must propagate")
		assert.Equal(t, "brandenburger tor", r.URL.Query().Get("q"))
		assert.Equal(t, "json", r.URL.Query().Get("format"))
		assert.Equal(t, "1", r.URL.Query().Get("addressdetails"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildNominatimTestResponse(results))
	}))
	defer srv.Close()

	p := newNominatimTestPlugin(t, srv.URL, 50)
	got, err := p.Search(context.Background(), SearchParams{Query: "brandenburger tor", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)

	pub := got.Results[0]
	assert.Equal(t, ContentTypePlace, pub.ContentType)
	assert.Equal(t, "nominatim:node:240109189", pub.ID)
	assert.Equal(t, "node:240109189", pub.SourceMetadata[MetaKeyOSMID])
	require.NotNil(t, pub.Lat)
	require.NotNil(t, pub.Lon)
	assert.InDelta(t, 52.5162895, *pub.Lat, 1e-5)
	assert.InDelta(t, 13.3777018, *pub.Lon, 1e-5)
	assert.Equal(t, "DE", pub.SourceMetadata[smetaCountryCode])
	assert.Equal(t, "Berlin", pub.SourceMetadata[smetaCity])
	// class="tourism" → "poi" per deriveNominatimPlaceType vocabulary
	// (inner type="attraction" is informational; coarser category wins).
	assert.Equal(t, "poi", pub.SourceMetadata[smetaPlaceType])
	imp, _ := pub.SourceMetadata[smetaImportance].(float64)
	assert.InDelta(t, 0.85, imp, 1e-6)
	assert.Contains(t, pub.License, "OpenStreetMap")
}

func TestNominatim_UserAgentRequired(t *testing.T) {
	t.Parallel()
	// Default plugin (no Extra override) still sends the placeholder UA.
	p := &NominatimPlugin{}
	require.NoError(t, p.Initialize(context.Background(), PluginConfig{Enabled: true, BaseURL: "http://example.invalid"}))
	assert.NotEmpty(t, p.userAgent, "default user_agent placeholder must be set")
	assert.True(t, strings.HasPrefix(p.userAgent, "retrievr-mcp/"),
		"default UA must identify retrievr per OSMF policy")
}

func TestNominatim_Search_429MapsToRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newNominatimTestPlugin(t, srv.URL, 100)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestNominatim_Search_403MapsToCredentialInvalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p := newNominatimTestPlugin(t, srv.URL, 100)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid),
		"403 from OSMF means UA non-compliance — surface as ErrCredentialInvalid for clarity")
}

func TestNominatim_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &NominatimPlugin{}
	_, err := p.Get(context.Background(), "node:1", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestNominatim_CityFallbackToTownToVillage(t *testing.T) {
	t.Parallel()
	// City missing, but village set — should still populate smetaCity.
	results := []nominatimResult{
		{
			OSMType:     "node",
			OSMID:       1,
			Lat:         "0",
			Lon:         "0",
			DisplayName: "Test",
			Address:     nominatimAddress{Village: "Smalltown"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildNominatimTestResponse(results))
	}))
	defer srv.Close()
	p := newNominatimTestPlugin(t, srv.URL, 100)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.Equal(t, "Smalltown", got.Results[0].SourceMetadata[smetaCity])
}
