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
// v6 cycle 4 / v2.17.0 — SerpAPI (Google) tests.
// ---------------------------------------------------------------------------

func newSerpAPITestPlugin(t *testing.T, baseURL, apiKey string) *SerpAPIPlugin {
	t.Helper()
	p := &SerpAPIPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestSerpAPI_Identity(t *testing.T) {
	t.Parallel()
	p := &SerpAPIPlugin{}
	assert.Equal(t, SourceSerpAPI, p.ID())
}

func TestSerpAPI_Capabilities(t *testing.T) {
	t.Parallel()
	p := &SerpAPIPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindWeb)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsDomainFilter)
	assert.True(t, caps.SupportsLanguageFilter)
}

func TestSerpAPI_Residency(t *testing.T) {
	t.Parallel()
	p := &SerpAPIPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestSerpAPI_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newSerpAPITestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestSerpAPI_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := serpapiSearchResponse{
		SearchInformation: serpapiSearchInfo{TotalResults: 12345},
		OrganicResults: []serpapiOrganicResult{{
			Position:      1,
			Title:         "Attention is All You Need",
			Link:          "https://arxiv.org/abs/1706.03762",
			DisplayedLink: "arxiv.org",
			Snippet:       "The dominant sequence transduction models...",
			Date:          "2017",
			Source:        "arXiv",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, serpapiSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, serpapiEngineGoogle, q.Get(serpapiParamEngine))
		assert.Equal(t, "test-key", q.Get(serpapiParamAPIKey))
		assert.Contains(t, q.Get(serpapiParamQ), "attention")
		assert.Equal(t, "10", q.Get(serpapiParamNum))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newSerpAPITestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "attention", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, "Attention is All You Need", pub.Title)
	assert.Equal(t, "https://arxiv.org/abs/1706.03762", pub.URL)
	assert.Equal(t, "2017", pub.Published)
	assert.Equal(t, 1, pub.SourceMetadata["serpapi_position"])
	assert.Equal(t, "arxiv.org", pub.SourceMetadata["serpapi_displayed_link"])
	assert.Equal(t, 12345, res.Total)
}

func TestSerpAPI_Search_LangAndCountryAndDomainFilters(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "de", q.Get(serpapiParamHL))
		assert.Equal(t, "de", q.Get(serpapiParamGL))
		assert.Contains(t, q.Get(serpapiParamQ), "site:wikipedia.org")
		_, _ = io.WriteString(w, `{"organic_results":[]}`)
	}))
	defer srv.Close()
	p := newSerpAPITestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Filters: SearchFilters{
			Language:       "de",
			Categories:     []string{"DE"},
			IncludeDomains: []string{"wikipedia.org"},
		},
	})
	require.NoError(t, err)
}

func TestSerpAPI_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newSerpAPITestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestSerpAPI_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newSerpAPITestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestSerpAPI_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &SerpAPIPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}
