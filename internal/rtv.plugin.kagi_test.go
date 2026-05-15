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
// v6 cycle 4 / v2.17.0 — Kagi tests.
// ---------------------------------------------------------------------------

func newKagiTestPlugin(t *testing.T, baseURL, apiKey string) *KagiPlugin {
	t.Helper()
	p := &KagiPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestKagi_Identity(t *testing.T) {
	t.Parallel()
	p := &KagiPlugin{}
	assert.Equal(t, SourceKagi, p.ID())
}

func TestKagi_Capabilities(t *testing.T) {
	t.Parallel()
	p := &KagiPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindWeb)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsDomainFilter)
}

func TestKagi_Residency(t *testing.T) {
	t.Parallel()
	p := &KagiPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestKagi_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newKagiTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestKagi_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := kagiSearchResponse{
		Data: []kagiDatum{
			{T: 0, Rank: 1, URL: "https://example.com/a", Title: "Result A", Snippet: "Snippet A.", Published: "2024-06-15T00:00:00Z"},
			{T: 1, Rank: 0}, // related-search entry, should be filtered
			{T: 0, Rank: 2, URL: "https://example.com/b", Title: "Result B", Snippet: "Snippet B."},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, kagiSearchPath, r.URL.Path)
		assert.Equal(t, "Bot test-key", r.Header.Get(kagiHeaderAuth))
		q := r.URL.Query()
		assert.Contains(t, q.Get(kagiParamQ), "neural")
		assert.Contains(t, q.Get(kagiParamQ), "site:example.com")
		assert.Contains(t, q.Get(kagiParamQ), "-site:spam.com")
		assert.Equal(t, "10", q.Get(kagiParamLimit))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newKagiTestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{
		Query: "neural",
		Limit: 10,
		Filters: SearchFilters{
			IncludeDomains: []string{"example.com"},
			ExcludeDomains: []string{"spam.com"},
		},
	})
	require.NoError(t, err)
	require.Len(t, res.Results, 2) // related-search filtered out

	first := res.Results[0]
	assert.Equal(t, ContentTypePaper, first.ContentType)
	assert.Equal(t, "Result A", first.Title)
	assert.Equal(t, "https://example.com/a", first.URL)
	assert.Equal(t, "2024-06-15", first.Published)
	assert.Equal(t, 1, first.SourceMetadata["kagi_rank"])
}

func TestKagi_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newKagiTestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestKagi_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newKagiTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestKagi_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &KagiPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

// keep io alive for fixture writer growth
var _ = io.Discard
