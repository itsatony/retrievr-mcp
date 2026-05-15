package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v6 cycle 6 / v2.19.0 — SerpAPI Google News tests.
// ---------------------------------------------------------------------------

func newSerpAPINewsTestPlugin(t *testing.T, baseURL, apiKey string) *SerpAPINewsPlugin {
	t.Helper()
	p := &SerpAPINewsPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestSerpAPINews_Identity(t *testing.T) {
	t.Parallel()
	p := &SerpAPINewsPlugin{}
	assert.Equal(t, SourceSerpAPINews, p.ID())
}

func TestSerpAPINews_Capabilities(t *testing.T) {
	t.Parallel()
	p := &SerpAPINewsPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindNews)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsDomainFilter)
	assert.True(t, caps.SupportsLanguageFilter)
}

func TestSerpAPINews_Residency(t *testing.T) {
	t.Parallel()
	p := &SerpAPINewsPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestSerpAPINews_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newSerpAPINewsTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestSerpAPINews_Search_UsesGoogleNewsEngine(t *testing.T) {
	t.Parallel()
	resp := serpapiSearchResponse{
		SearchInformation: serpapiSearchInfo{TotalResults: 1234},
		OrganicResults: []serpapiOrganicResult{{
			Position:      1,
			Title:         "Climate Summit Outcomes",
			Link:          "https://bbc.com/news/climate",
			DisplayedLink: "bbc.com",
			Snippet:       "Negotiators reached consensus on...",
			Date:          "6 hours ago",
			Source:        "BBC News",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, serpapiSearchPath, r.URL.Path)
		q := r.URL.Query()
		// Critical: the engine MUST be google_news, not the cycle-4 web engine.
		assert.Equal(t, serpapinewsEngine, q.Get(serpapiParamEngine))
		assert.Equal(t, "test-key", q.Get(serpapiParamAPIKey))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newSerpAPINewsTestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "climate"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	// ID prefix must be "serpapinews:" not "serpapi:" so dedup keeps news + web separate.
	assert.Contains(t, pub.ID, "serpapinews:")
	assert.Equal(t, SourceSerpAPINews, pub.Source)
	assert.Equal(t, "Climate Summit Outcomes", pub.Title)
	assert.Equal(t, "https://bbc.com/news/climate", pub.URL)
}

func TestSerpAPINews_Search_LanguageAndCountry(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "de", q.Get(serpapiParamHL))
		assert.Equal(t, "de", q.Get(serpapiParamGL))
		_, _ = w.Write([]byte(`{"organic_results":[]}`))
	}))
	defer srv.Close()
	p := newSerpAPINewsTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Filters: SearchFilters{
			Language:   "de",
			Categories: []string{"DE"},
		},
	})
	require.NoError(t, err)
}

func TestSerpAPINews_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newSerpAPINewsTestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestSerpAPINews_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newSerpAPINewsTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestSerpAPINews_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &SerpAPINewsPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestSerpAPINews_CredentialSharedWithWebPlugin(t *testing.T) {
	t.Parallel()
	// Verify that the SerpAPI key passed via PluginConfig.APIKey is
	// resolved through the same SourceSerpAPI credential key (not a
	// distinct "serpapinews" key) — SerpAPI keys are account-scoped.
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceSerpAPI: "shared-key",
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "shared-key", r.URL.Query().Get(serpapiParamAPIKey))
		_, _ = w.Write([]byte(`{"organic_results":[]}`))
	}))
	defer srv.Close()

	// Note: PluginConfig.APIKey deliberately empty so the only key
	// source is the per-call credential.
	p := newSerpAPINewsTestPlugin(t, srv.URL, "")
	_, err := p.Search(ctx, SearchParams{Query: "x"})
	require.NoError(t, err)
}
