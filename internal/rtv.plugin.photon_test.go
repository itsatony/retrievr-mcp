package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v3 cycle 3 / v2.4.0: Photon (Komoot) place-search tests.
// ---------------------------------------------------------------------------

func newPhotonTestPlugin(t *testing.T, baseURL string) *PhotonPlugin {
	t.Helper()
	p := &PhotonPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		RateLimit: 100,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildPhotonTestResponse(features []photonFeature) string {
	b, _ := json.Marshal(photonFeatureCollection{Type: "FeatureCollection", Features: features})
	return string(b)
}

func TestPhoton_Identity(t *testing.T) {
	t.Parallel()
	p := &PhotonPlugin{}
	assert.Equal(t, "photon", p.ID())
	assert.NotEmpty(t, p.Name())
	assert.NotEmpty(t, p.Description())
}

func TestPhoton_Capabilities(t *testing.T) {
	t.Parallel()
	p := &PhotonPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPlace)
	assert.Equal(t, photonMaxLimitCap, caps.MaxResultsPerQuery)
}

func TestPhoton_Residency_IsEU_AdmissibleUnderEUStrict(t *testing.T) {
	t.Parallel()
	p := &PhotonPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
	assert.True(t, tag.Region.IsEU(), "Photon must be admissible under eu_strict")
}

func TestPhoton_Search_HappyPath(t *testing.T) {
	t.Parallel()
	features := []photonFeature{
		{
			Type:     "Feature",
			Geometry: photonGeometry{Type: "Point", Coordinates: []float64{13.37770, 52.51629}}, // lon, lat
			Properties: photonProperties{
				OSMID:       240109189,
				OSMType:     "node",
				OSMKey:      "tourism",
				OSMValue:    "attraction",
				Name:        "Brandenburger Tor",
				Country:     "Germany",
				CountryCode: "de",
				State:       "Berlin",
				City:        "Berlin",
				Postcode:    "10117",
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, photonSearchPath, r.URL.Path)
		assert.Equal(t, "brandenburger tor", r.URL.Query().Get("q"))
		assert.Equal(t, "5", r.URL.Query().Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildPhotonTestResponse(features))
	}))
	defer srv.Close()

	p := newPhotonTestPlugin(t, srv.URL)
	got, err := p.Search(context.Background(), SearchParams{Query: "brandenburger tor", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)

	pub := got.Results[0]
	assert.Equal(t, ContentTypePlace, pub.ContentType)
	assert.Equal(t, "photon:node:240109189", pub.ID)
	assert.Equal(t, "Brandenburger Tor", pub.Title)
	require.NotNil(t, pub.Lat)
	require.NotNil(t, pub.Lon)
	assert.InDelta(t, 52.51629, *pub.Lat, 1e-5)
	assert.InDelta(t, 13.37770, *pub.Lon, 1e-5)
	assert.Equal(t, "node:240109189", pub.SourceMetadata[MetaKeyOSMID])
	assert.Equal(t, "node", pub.SourceMetadata[smetaOSMType])
	assert.Equal(t, "DE", pub.SourceMetadata[smetaCountryCode])
	assert.Equal(t, "poi", pub.SourceMetadata[smetaPlaceType])
	assert.Contains(t, pub.Address, "Berlin")
	assert.Contains(t, pub.Address, "Germany")
}

func TestPhoton_Search_AddressOnlyComposesName(t *testing.T) {
	t.Parallel()
	// Properties.Name empty — street+housenumber+city must compose a name.
	features := []photonFeature{
		{
			Geometry: photonGeometry{Type: "Point", Coordinates: []float64{2.29448, 48.85837}},
			Properties: photonProperties{
				OSMID:       1,
				OSMType:     "way",
				OSMKey:      "place",
				OSMValue:    "house",
				Street:      "Champ de Mars",
				HouseNumber: "5",
				City:        "Paris",
				Country:     "France",
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildPhotonTestResponse(features))
	}))
	defer srv.Close()

	p := newPhotonTestPlugin(t, srv.URL)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.Contains(t, got.Results[0].Title, "Champ de Mars 5")
	assert.Contains(t, got.Results[0].Title, "Paris")
}

func TestPhoton_Search_429MapsToRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newPhotonTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestPhoton_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &PhotonPlugin{}
	_, err := p.Get(context.Background(), "node:1", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestPhoton_LimitClampedToMax(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "50", r.URL.Query().Get("limit"), "limit must clamp to photonMaxLimitCap")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildPhotonTestResponse(nil))
	}))
	defer srv.Close()
	p := newPhotonTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 999})
	require.NoError(t, err)
}

func TestPhoton_LiveSmoke(t *testing.T) {
	if os.Getenv("RETRIEVR_LIVE_PHOTON") == "" {
		t.Skip("RETRIEVR_LIVE_PHOTON not set; skipping live Photon smoke test")
	}
	p := newPhotonTestPlugin(t, "")
	got, err := p.Search(context.Background(), SearchParams{Query: "Brandenburger Tor Berlin", Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, got.Results)
}
