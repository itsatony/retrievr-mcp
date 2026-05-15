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
// v6 cycle 4 / v2.17.0 — Mojeek tests.
// ---------------------------------------------------------------------------

func newMojeekTestPlugin(t *testing.T, baseURL, apiKey string) *MojeekPlugin {
	t.Helper()
	p := &MojeekPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestMojeek_Identity(t *testing.T) {
	t.Parallel()
	p := &MojeekPlugin{}
	assert.Equal(t, SourceMojeek, p.ID())
}

func TestMojeek_Capabilities(t *testing.T) {
	t.Parallel()
	p := &MojeekPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindWeb)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsLanguageFilter)
	assert.True(t, caps.SupportsDomainFilter)
}

func TestMojeek_Residency(t *testing.T) {
	t.Parallel()
	p := &MojeekPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUKAdequacy, tag.Region)
}

func TestMojeek_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newMojeekTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestMojeek_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := mojeekSearchResponse{
		Response: mojeekResponse{
			Head: mojeekHead{Query: "neural", ResultsCount: 1},
			Results: []mojeekResult{{
				Title:   "Neural networks intro",
				URL:     "https://example.com/neural",
				Desc:    "A primer on neural networks.",
				PubDate: "2024-06-15",
			}},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, mojeekSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Contains(t, q.Get(mojeekParamQ), "neural")
		assert.Equal(t, "test-key", q.Get(mojeekParamAPIKey))
		assert.Equal(t, mojeekFmtJSON, q.Get(mojeekParamFmt))
		assert.Equal(t, "de", q.Get(mojeekParamLang))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newMojeekTestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{
		Query:   "neural",
		Filters: SearchFilters{Language: "de"},
	})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, "Neural networks intro", pub.Title)
	assert.Equal(t, "https://example.com/neural", pub.URL)
	assert.Equal(t, "2024-06-15", pub.Published)
	assert.Contains(t, pub.Abstract, "primer")
}

func TestMojeek_Search_DomainFilters(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get(mojeekParamQ)
		assert.Contains(t, q, "site:example.com")
		assert.Contains(t, q, "-site:spam.com")
		_, _ = io.WriteString(w, `{"response":{"head":{},"results":[]}}`)
	}))
	defer srv.Close()
	p := newMojeekTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Filters: SearchFilters{
			IncludeDomains: []string{"example.com"},
			ExcludeDomains: []string{"spam.com"},
		},
	})
	require.NoError(t, err)
}

func TestMojeek_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newMojeekTestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestMojeek_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newMojeekTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestMojeek_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &MojeekPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}
