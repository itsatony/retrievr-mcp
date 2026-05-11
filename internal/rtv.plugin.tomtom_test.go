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
// v3 cycle 3 / v2.4.0: TomTom Search tests.
// ---------------------------------------------------------------------------

const (
	tomtomTestServerKey  = "tt-server-key"
	tomtomTestPerCallKey = "tt-per-call-key"
)

func newTomTomTestPlugin(t *testing.T, baseURL, apiKey string) *TomTomPlugin {
	t.Helper()
	p := &TomTomPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		RateLimit: 100,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildTomTomTestResponse(results []tomtomResult, total int) string {
	body := tomtomSearchResponse{
		Summary: tomtomSummary{NumResults: len(results), TotalResults: total, Offset: 0},
		Results: results,
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestTomTom_Identity(t *testing.T) {
	t.Parallel()
	p := &TomTomPlugin{}
	assert.Equal(t, "tomtom", p.ID())
	assert.NotEmpty(t, p.Name())
}

func TestTomTom_Capabilities(t *testing.T) {
	t.Parallel()
	p := &TomTomPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPlace)
	assert.True(t, caps.SupportsPagination)
}

func TestTomTom_Residency_IsEU_DPASigned(t *testing.T) {
	t.Parallel()
	p := &TomTomPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
	assert.True(t, tag.Region.IsEU(), "TomTom must be admissible under eu_strict")
	assert.Equal(t, DPASigned, tag.DPAStatus)
}

func TestTomTom_Search_HappyPath(t *testing.T) {
	t.Parallel()
	results := []tomtomResult{
		{
			Type:  "POI",
			ID:    "g-tomtom-poi-1",
			Score: 9.5,
			Address: tomtomAddress{
				StreetNumber:       "1",
				Street:             "Pariser Platz",
				Municipality:       "Berlin",
				CountrySubdivision: "Berlin",
				PostalCode:         "10117",
				CountryCode:        "DE",
				Country:            "Germany",
				Freeform:           "Pariser Platz 1, 10117 Berlin",
			},
			Position: tomtomPosition{Lat: 52.51629, Lon: 13.37770},
			POI: &tomtomPOI{
				Name:       "Brandenburger Tor",
				Categories: []string{"monument", "landmark"},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.True(t, strings.HasPrefix(r.URL.Path, tomtomSearchPathPrefix), "path must include search prefix")
		assert.True(t, strings.HasSuffix(r.URL.Path, tomtomSearchPathSuffix), "path must end .json")
		assert.Equal(t, tomtomTestServerKey, r.URL.Query().Get(tomtomQueryParamKey))
		assert.Equal(t, "5", r.URL.Query().Get(tomtomQueryParamLimit))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildTomTomTestResponse(results, 1))
	}))
	defer srv.Close()

	p := newTomTomTestPlugin(t, srv.URL, tomtomTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "Brandenburger Tor", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)

	pub := got.Results[0]
	assert.Equal(t, ContentTypePlace, pub.ContentType)
	assert.Equal(t, "tomtom:g-tomtom-poi-1", pub.ID)
	assert.Equal(t, "Brandenburger Tor", pub.Title)
	assert.Equal(t, "Pariser Platz 1, 10117 Berlin", pub.Address)
	require.NotNil(t, pub.Lat)
	assert.InDelta(t, 52.51629, *pub.Lat, 1e-5)
	assert.Equal(t, "DE", pub.SourceMetadata[smetaCountryCode])
	assert.Equal(t, "Berlin", pub.SourceMetadata[smetaCity])
	assert.Equal(t, "poi", pub.SourceMetadata[smetaPlaceType])
	cats, _ := pub.SourceMetadata[smetaCategories].([]string)
	assert.Contains(t, cats, "monument")
	// Importance derived from score=9.5 → 1 - 1/(1+9.5) = ~0.9048
	imp, ok := pub.SourceMetadata[smetaImportance].(float64)
	require.True(t, ok)
	assert.InDelta(t, 0.9048, imp, 1e-3)
}

func TestTomTom_Search_PerCallCredentialWins(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, tomtomTestPerCallKey, r.URL.Query().Get(tomtomQueryParamKey))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildTomTomTestResponse(nil, 0))
	}))
	defer srv.Close()
	p := newTomTomTestPlugin(t, srv.URL, tomtomTestServerKey)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceTomTom: tomtomTestPerCallKey,
	})
	_, err := p.Search(ctx, SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
}

func TestTomTom_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newTomTomTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestTomTom_Search_401ReturnsCredentialInvalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newTomTomTestPlugin(t, srv.URL, tomtomTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestTomTom_Search_429ReturnsRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newTomTomTestPlugin(t, srv.URL, tomtomTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestTomTom_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &TomTomPlugin{}
	_, err := p.Get(context.Background(), "anything", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestTomTom_HasMore_DerivedFromTotalNumOffset(t *testing.T) {
	t.Parallel()
	results := []tomtomResult{{Type: "POI", ID: "a", Address: tomtomAddress{Freeform: "A"}, Position: tomtomPosition{Lat: 0, Lon: 0}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildTomTomTestResponse(results, 50))
	}))
	defer srv.Close()
	p := newTomTomTestPlugin(t, srv.URL, tomtomTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
	assert.True(t, got.HasMore, "total=50 with 1 returned must HasMore=true")
}
