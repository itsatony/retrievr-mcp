package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v6 cycle 1 / v2.14.0 — HERE Geocoding tests.
// ---------------------------------------------------------------------------

func newHERETestPlugin(t *testing.T, baseURL, apiKey string) *HEREPlugin {
	t.Helper()
	p := &HEREPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestHERE_Identity(t *testing.T) {
	t.Parallel()
	p := &HEREPlugin{}
	assert.Equal(t, SourceHERE, p.ID())
}

func TestHERE_Capabilities(t *testing.T) {
	t.Parallel()
	p := &HEREPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPlace)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsLanguageFilter)
}

func TestHERE_Residency(t *testing.T) {
	t.Parallel()
	p := &HEREPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
}

func TestHERE_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newHERETestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestHERE_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := hereGeocodeResponse{
		Items: []hereItem{{
			ID:         "here:cm:namedplace:12345",
			Title:      "Eiffel Tower, Paris, France",
			ResultType: "place",
			Address: hereAddress{
				Label:       "Eiffel Tower, Paris, France",
				CountryCode: "FRA",
				City:        "Paris",
				Street:      "Avenue Anatole France",
				HouseNumber: "5",
			},
			Position:   hereLatLng{Lat: 48.8584, Lng: 2.2945},
			Categories: []hereCategory{{ID: "300-3000", Name: "Landmark-Attraction"}},
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, hereGeocodePath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "Eiffel Tower", q.Get(hereParamQ))
		assert.Equal(t, "test-key", q.Get(hereParamAPIKey))
		assert.Equal(t, "20", q.Get(hereParamLimit))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newHERETestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "Eiffel Tower", Limit: 20})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "here:here:cm:namedplace:12345", pub.ID)
	assert.Equal(t, ContentTypePlace, pub.ContentType)
	assert.Equal(t, "Eiffel Tower, Paris, France", pub.Title)
	require.NotNil(t, pub.Lat)
	require.NotNil(t, pub.Lon)
	assert.InDelta(t, 48.8584, *pub.Lat, 0.0001)
	assert.Equal(t, "place", pub.SourceMetadata[smetaPlaceType])
	assert.Equal(t, "FRA", pub.SourceMetadata[hereMetaKeyCountryCode])
}

func TestHERE_Search_LanguageAndCategoryFilter(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "de", q.Get(hereParamLang))
		assert.Equal(t, "countryCode:DEU", q.Get(hereParamIn))
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	defer srv.Close()
	p := newHERETestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "Berlin",
		Filters: SearchFilters{
			Language:   "de",
			Categories: []string{"countryCode:DEU"},
		},
	})
	require.NoError(t, err)
}

func TestHERE_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newHERETestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestHERE_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newHERETestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestHERE_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &HEREPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}
