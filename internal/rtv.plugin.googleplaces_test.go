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
// v6 cycle 1 / v2.14.0 — Google Places tests.
// ---------------------------------------------------------------------------

func newGooglePlacesTestPlugin(t *testing.T, baseURL, apiKey string) *GooglePlacesPlugin {
	t.Helper()
	p := &GooglePlacesPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestGooglePlaces_Identity(t *testing.T) {
	t.Parallel()
	p := &GooglePlacesPlugin{}
	assert.Equal(t, SourceGooglePlaces, p.ID())
}

func TestGooglePlaces_Capabilities(t *testing.T) {
	t.Parallel()
	p := &GooglePlacesPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPlace)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsCategoryFilter)
	assert.True(t, caps.SupportsLanguageFilter)
}

func TestGooglePlaces_Residency(t *testing.T) {
	t.Parallel()
	p := &GooglePlacesPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestGooglePlaces_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newGooglePlacesTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestGooglePlaces_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := googlePlacesSearchResponse{
		Places: []googlePlacesPlace{{
			ID:               "places/ChIJabc123",
			DisplayName:      googlePlacesLocalized{Text: "Berlin TV Tower", LanguageCode: "en"},
			FormattedAddress: "Panoramastraße 1A, 10178 Berlin, Germany",
			Location:         googlePlacesLatLng{Latitude: 52.5208, Longitude: 13.4094},
			Rating:           4.5,
			UserRatingCount:  120000,
			Types:            []string{"tourist_attraction", "point_of_interest"},
			WebsiteURI:       "https://tv-turm.de/",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, googlePlacesSearchPath, r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get(googlePlacesHeaderAPIKey))
		assert.Contains(t, r.Header.Get(googlePlacesHeaderFieldMask), "places.id")

		var body googlePlacesSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "Berlin TV Tower", body.TextQuery)
		assert.Equal(t, 10, body.MaxResultCount)

		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newGooglePlacesTestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "Berlin TV Tower", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "googleplaces:ChIJabc123", pub.ID)
	assert.Equal(t, ContentTypePlace, pub.ContentType)
	assert.Equal(t, "Berlin TV Tower", pub.Title)
	require.NotNil(t, pub.Lat)
	require.NotNil(t, pub.Lon)
	assert.InDelta(t, 52.5208, *pub.Lat, 0.0001)
	assert.InDelta(t, 13.4094, *pub.Lon, 0.0001)
	assert.Equal(t, "tourist_attraction", pub.SourceMetadata[smetaPlaceType])
	assert.Equal(t, 4.5, pub.SourceMetadata[googlePlacesMetaKeyRating])
	assert.Equal(t, "https://tv-turm.de/", pub.SourceMetadata[googlePlacesMetaKeyWebsite])
}

func TestGooglePlaces_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newGooglePlacesTestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestGooglePlaces_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newGooglePlacesTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestGooglePlaces_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &GooglePlacesPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

// keep io alive for future fixture expansions
var _ = io.Discard
