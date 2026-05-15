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
// v6 cycle 5 / v2.18.0 — Google Knowledge Graph tests.
// ---------------------------------------------------------------------------

func newKGAPITestPlugin(t *testing.T, baseURL, apiKey string) *KGAPIPlugin {
	t.Helper()
	p := &KGAPIPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestKGAPI_Identity(t *testing.T) {
	t.Parallel()
	p := &KGAPIPlugin{}
	assert.Equal(t, SourceKGAPI, p.ID())
}

func TestKGAPI_Capabilities(t *testing.T) {
	t.Parallel()
	p := &KGAPIPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindFact)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsCategoryFilter)
	assert.True(t, caps.SupportsLanguageFilter)
}

func TestKGAPI_Residency(t *testing.T) {
	t.Parallel()
	p := &KGAPIPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestKGAPI_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newKGAPITestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestKGAPI_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := kgapiSearchResponse{
		ItemListElement: []kgapiElement{{
			Type:        "EntitySearchResult",
			ResultScore: 1234.5,
			Result: kgapiResult{
				ID:          "kg:/m/0abc12",
				Name:        "Brandenburg Gate",
				Types:       []string{"Place", "Thing", "LandmarksOrHistoricalBuildings"},
				Description: "Monument in Berlin",
				Image:       &kgapiImage{ContentURL: "https://example.com/img.jpg"},
				DetailedDescription: &kgapiDetailedDesc{
					ArticleBody: "<p>The Brandenburg Gate is...</p>",
					URL:         "https://en.wikipedia.org/wiki/Brandenburg_Gate",
					License:     "https://creativecommons.org/licenses/by-sa/3.0/",
				},
				URL: "https://www.berlin.de/brandenburg-gate",
			},
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, kgapiSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "Brandenburg Gate", q.Get(kgapiParamQuery))
		assert.Equal(t, "test-key", q.Get(kgapiParamKey))
		assert.Equal(t, "10", q.Get(kgapiParamLimit))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newKGAPITestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "Brandenburg Gate"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "kgapi:/m/0abc12", pub.ID)
	assert.Equal(t, "Brandenburg Gate", pub.Title)
	assert.Contains(t, pub.Abstract, "Brandenburg Gate is")
	assert.Equal(t, "https://www.berlin.de/brandenburg-gate", pub.URL)
	assert.Equal(t, "https://example.com/img.jpg", pub.ThumbnailURL)
	assert.Contains(t, pub.License, "creativecommons")
	assert.Equal(t, "/m/0abc12", pub.SourceMetadata[kgapiMetaKeyKGID])
	assert.Equal(t, 1234.5, pub.SourceMetadata[kgapiMetaKeyScore])
	assert.Equal(t, "https://en.wikipedia.org/wiki/Brandenburg_Gate", pub.SourceMetadata[kgapiMetaKeyArticleURL])
}

func TestKGAPI_Search_TypesAndLanguageFilter(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "Place,Person", q.Get(kgapiParamTypes))
		assert.Equal(t, "de", q.Get(kgapiParamLanguages))
		_, _ = io.WriteString(w, `{"itemListElement":[]}`)
	}))
	defer srv.Close()
	p := newKGAPITestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "Berlin",
		Filters: SearchFilters{
			Categories: []string{"Place", "Person"},
			Language:   "de",
		},
	})
	require.NoError(t, err)
}

func TestKGAPI_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p := newKGAPITestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestKGAPI_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newKGAPITestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestKGAPI_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &KGAPIPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}
