package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Cycle 2 Wave-1: Exa.ai provider tests.
// ---------------------------------------------------------------------------

const (
	exaTestServerKey  = "exa-server-key"
	exaTestPerCallKey = "exa-per-call-key"
)

func newExaTestPlugin(t *testing.T, baseURL, apiKey string) *ExaPlugin {
	t.Helper()
	p := &ExaPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		RateLimit: 10,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildExaTestResponse(results []exaResult) string {
	body := exaSearchResponse{
		RequestID: "test-req-id",
		Results:   results,
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestExa_Identity(t *testing.T) {
	t.Parallel()
	p := &ExaPlugin{}
	assert.Equal(t, "exa", p.ID())
	assert.Equal(t, "Exa.ai", p.Name())
	assert.NotEmpty(t, p.Description())
}

func TestExa_Capabilities(t *testing.T) {
	t.Parallel()
	p := &ExaPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindWeb)
	assert.Contains(t, caps.Kinds, KindNews)
	assert.Contains(t, caps.QueryIntents, IntentQuickLookup)
	assert.Contains(t, caps.QueryIntents, IntentDeepResearch)
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsCategoryFilter)
	assert.False(t, caps.SupportsCitations)
}

func TestExa_Residency_IsUSBlocked(t *testing.T) {
	t.Parallel()
	p := &ExaPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.False(t, tag.Region.IsEU(), "Exa must not be EU-resident")
	assert.False(t, tag.Region.IsPublicResearch())
}

func TestExa_Search_HappyPath(t *testing.T) {
	t.Parallel()

	results := []exaResult{
		{
			ID:            "abc123",
			Title:         "Sparse Transformers",
			URL:           "https://example.com/sparse",
			PublishedDate: "2024-09-12T10:00:00Z",
			Author:        "Alice Engineer",
			Score:         0.87,
			Text:          "After running sparse-attention models in production...",
		},
		{
			ID:    "def456",
			Title: "Attention Visualizations",
			URL:   "https://huggingface.co/blog/attention",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, exaSearchPath, r.URL.Path)
		assert.Equal(t, exaTestServerKey, r.Header.Get(exaAuthHeader), "x-api-key must carry the server-default key")

		var body exaSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "transformer attention", body.Query)
		assert.Equal(t, 5, body.NumResults)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildExaTestResponse(results))
	}))
	defer srv.Close()

	p := newExaTestPlugin(t, srv.URL, exaTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "transformer attention", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 2)
	assert.Equal(t, "exa:abc123", got.Results[0].ID)
	assert.Equal(t, "Sparse Transformers", got.Results[0].Title)
	assert.Equal(t, "2024-09-12", got.Results[0].Published, "ISO-8601 timestamp must clamp to YYYY-MM-DD")
	require.NotEmpty(t, got.Results[0].Authors)
	assert.Equal(t, "Alice Engineer", got.Results[0].Authors[0].Name)
	assert.Contains(t, got.Results[0].SourceMetadata, smetaSnippet)
	assert.Contains(t, got.Results[0].SourceMetadata, smetaPublishedAt)

	health := p.Health(context.Background())
	assert.True(t, health.Healthy)
}

func TestExa_Search_PerCallCredentialOverridesServerKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, exaTestPerCallKey, r.Header.Get(exaAuthHeader),
			"per-call credential must override server-default")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildExaTestResponse(nil))
	}))
	defer srv.Close()

	p := newExaTestPlugin(t, srv.URL, exaTestServerKey)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceExa: exaTestPerCallKey,
	})
	_, err := p.Search(ctx, SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
}

func TestExa_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newExaTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestExa_Search_401ReturnsErrCredentialInvalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()

	p := newExaTestPlugin(t, srv.URL, exaTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid), "401 must surface as ErrCredentialInvalid; got %v", err)

	health := p.Health(context.Background())
	assert.False(t, health.Healthy)
	assert.NotEmpty(t, health.LastError)
}

func TestExa_Search_429ReturnsErrRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := newExaTestPlugin(t, srv.URL, exaTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	assert.True(t, errors.Is(err, ErrRateLimitExceeded), "429 must map to ErrRateLimitExceeded; got %v", err)
}

func TestExa_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &ExaPlugin{}
	_, err := p.Get(context.Background(), "abc", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestExa_NumResultsClampedToMax(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body exaSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.LessOrEqual(t, body.NumResults, exaMaxNumResults)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildExaTestResponse(nil))
	}))
	defer srv.Close()

	p := newExaTestPlugin(t, srv.URL, exaTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 9999})
	require.NoError(t, err)
}

func TestNormalizeExaDate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"2024-09-12T10:00:00Z", "2024-09-12"},
		{"2024-09-12", "2024-09-12"},
		{"", ""},
		{"garbage", "garbage"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, normalizeExaDate(c.in))
	}
}

// ---------------------------------------------------------------------------
// Live test (gated on EXAAI env var) — exercises the real Exa API end-to-end.
// ---------------------------------------------------------------------------

func TestExa_LiveSmoke(t *testing.T) {
	apiKey := os.Getenv("EXAAI")
	if apiKey == "" {
		t.Skip("EXAAI env var not set; skipping live Exa smoke test")
	}
	p := newExaTestPlugin(t, "", apiKey) // empty baseURL falls back to default

	got, err := p.Search(context.Background(), SearchParams{Query: "transformer attention mechanism", Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, got.Results, "live Exa search must return at least one result")
	for _, r := range got.Results {
		t.Logf("hit: id=%s title=%q url=%s", r.ID, r.Title, r.URL)
		assert.True(t, strings.HasPrefix(r.ID, "exa:"))
		assert.NotEmpty(t, r.Title)
	}
}
