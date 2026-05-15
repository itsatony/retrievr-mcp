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
// v6 cycle 1 / v2.14.0 — OSM Overpass tests.
// ---------------------------------------------------------------------------

func newOSMOverpassTestPlugin(t *testing.T, baseURL string) *OSMOverpassPlugin {
	t.Helper()
	p := &OSMOverpassPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestOSMOverpass_Identity(t *testing.T) {
	t.Parallel()
	p := &OSMOverpassPlugin{}
	assert.Equal(t, SourceOSMOverpass, p.ID())
}

func TestOSMOverpass_Capabilities(t *testing.T) {
	t.Parallel()
	p := &OSMOverpassPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPlace)
	assert.False(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsCategoryFilter)
}

func TestOSMOverpass_Residency(t *testing.T) {
	t.Parallel()
	p := &OSMOverpassPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
}

func TestOSMOverpass_Search_DefaultQLNodes(t *testing.T) {
	t.Parallel()
	resp := overpassResponse{
		Elements: []overpassElement{{
			Type: "node",
			ID:   240109189,
			Lat:  52.5162746,
			Lon:  13.3777041,
			Tags: map[string]string{
				"name":    "Brandenburg Gate",
				"tourism": "attraction",
			},
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, osmOverpassInterpreterPath, r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(body), `node["name"~"Brandenburg Gate",i]`)
		assert.Contains(t, string(body), "out center 25;")
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newOSMOverpassTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "Brandenburg Gate"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "osmoverpass:node/240109189", pub.ID)
	assert.Equal(t, "Brandenburg Gate", pub.Title)
	require.NotNil(t, pub.Lat)
	assert.InDelta(t, 52.5163, *pub.Lat, 0.0005)
	assert.Equal(t, "node/240109189", pub.SourceMetadata[MetaKeyOSMID])
	assert.Equal(t, "attraction", pub.SourceMetadata[smetaPlaceType])
}

func TestOSMOverpass_Search_VerbatimQL(t *testing.T) {
	t.Parallel()
	customQL := `[out:json][timeout:10];node["amenity"="cafe"](around:1000,52.52,13.40);out;`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The plugin should pass the custom QL through unchanged.
		assert.Equal(t, customQL, string(body))
		_, _ = io.WriteString(w, `{"elements":[]}`)
	}))
	defer srv.Close()

	p := newOSMOverpassTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "ignored when custom QL provided",
		Filters: SearchFilters{Categories: []string{customQL}},
	})
	require.NoError(t, err)
}

func TestOSMOverpass_Search_WayWithCenter(t *testing.T) {
	t.Parallel()
	resp := overpassResponse{
		Elements: []overpassElement{{
			Type:   "way",
			ID:     12345,
			Center: &overpassLatLon{Lat: 48.8584, Lon: 2.2945},
			Tags:   map[string]string{"name": "Eiffel Tower", "tourism": "attraction"},
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newOSMOverpassTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "Eiffel"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	require.NotNil(t, res.Results[0].Lat)
	assert.InDelta(t, 48.8584, *res.Results[0].Lat, 0.0001)
	assert.Equal(t, "osmoverpass:way/12345", res.Results[0].ID)
}

func TestOSMOverpass_Search_SkipsUnnamedElements(t *testing.T) {
	t.Parallel()
	resp := overpassResponse{
		Elements: []overpassElement{
			{Type: "node", ID: 1, Lat: 1, Lon: 1, Tags: map[string]string{"name": "Named"}},
			{Type: "node", ID: 2, Lat: 2, Lon: 2, Tags: map[string]string{"highway": "primary"}}, // no name
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	p := newOSMOverpassTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	assert.Equal(t, "Named", res.Results[0].Title)
}

func TestOSMOverpass_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newOSMOverpassTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestOSMOverpass_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &OSMOverpassPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestOSMOverpass_BuildQL_QuoteEscape(t *testing.T) {
	t.Parallel()
	got := overpassBuildQL(SearchParams{Query: `"weird"`}, 10)
	assert.Contains(t, got, `node["name"~"\"weird\"",i]`)
	assert.True(t, strings.Contains(got, "out center 10;"))
}
