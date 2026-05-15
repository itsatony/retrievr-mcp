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
// v6 cycle 6 / v2.19.0 — NewsAPI.org tests.
// ---------------------------------------------------------------------------

func newNewsAPITestPlugin(t *testing.T, baseURL, apiKey string) *NewsAPIPlugin {
	t.Helper()
	p := &NewsAPIPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestNewsAPI_Identity(t *testing.T) {
	t.Parallel()
	p := &NewsAPIPlugin{}
	assert.Equal(t, SourceNewsAPI, p.ID())
}

func TestNewsAPI_Capabilities(t *testing.T) {
	t.Parallel()
	p := &NewsAPIPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindNews)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsLanguageFilter)
	assert.True(t, caps.SupportsDomainFilter)
}

func TestNewsAPI_Residency(t *testing.T) {
	t.Parallel()
	p := &NewsAPIPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestNewsAPI_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newNewsAPITestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestNewsAPI_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := newsapiSearchResponse{
		Status:       "ok",
		TotalResults: 1,
		Articles: []newsapiArticle{{
			Source:      newsapiSource{ID: "bbc-news", Name: "BBC News"},
			Author:      "Jane Doe",
			Title:       "Climate summit concludes",
			Description: "A summary of the climate summit outcomes.",
			URL:         "https://bbc.com/news/climate-summit",
			URLToImage:  "https://bbc.com/img.jpg",
			PublishedAt: "2024-06-15T12:00:00Z",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, newsapiSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "climate", q.Get(newsapiParamQ))
		assert.Equal(t, "test-key", q.Get(newsapiParamAPIKey))
		assert.Equal(t, "20", q.Get(newsapiParamPageSize))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newNewsAPITestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "climate"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, "Climate summit concludes", pub.Title)
	assert.Equal(t, "https://bbc.com/news/climate-summit", pub.URL)
	assert.Equal(t, "2024-06-15", pub.Published)
	require.Len(t, pub.Authors, 1)
	assert.Equal(t, "Jane Doe", pub.Authors[0].Name)
	assert.Equal(t, "bbc-news", pub.SourceMetadata[newsapiMetaKeySourceID])
	assert.Equal(t, "BBC News", pub.SourceMetadata[newsapiMetaKeySourceName])
}

func TestNewsAPI_Search_FiltersAndSort(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "de", q.Get(newsapiParamLanguage))
		assert.Equal(t, "2024-01-01", q.Get(newsapiParamFrom))
		assert.Equal(t, "2024-12-31", q.Get(newsapiParamTo))
		assert.Equal(t, "bbc.com,nytimes.com", q.Get(newsapiParamDomains))
		assert.Equal(t, "tabloid.example", q.Get(newsapiParamExcludeDomains))
		assert.Equal(t, newsapiSortPublishedAt, q.Get(newsapiParamSortBy))
		_, _ = io.WriteString(w, `{"status":"ok","totalResults":0,"articles":[]}`)
	}))
	defer srv.Close()
	p := newNewsAPITestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Sort:  SortDateDesc,
		Filters: SearchFilters{
			Language:       "de-DE", // BCP-47 → stripped to "de"
			DateFrom:       "2024",
			DateTo:         "2024",
			IncludeDomains: []string{"bbc.com", "nytimes.com"},
			ExcludeDomains: []string{"tabloid.example"},
		},
	})
	require.NoError(t, err)
}

func TestNewsAPI_Search_APIErrorEnvelope(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 200 OK + status:error in body — NewsAPI's pattern for malformed queries.
		b, _ := json.Marshal(newsapiSearchResponse{
			Status:  "error",
			Code:    "parameterInvalid",
			Message: "The 'q' parameter is missing.",
		})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	p := newNewsAPITestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parameterInvalid")
}

func TestNewsAPI_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newNewsAPITestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestNewsAPI_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newNewsAPITestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestNewsAPI_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &NewsAPIPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}
