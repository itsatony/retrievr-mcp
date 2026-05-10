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
// Cycle 2 Wave-1: Brave Search provider tests.
// ---------------------------------------------------------------------------

const (
	braveTestServerKey  = "brave-server-key"
	braveTestPerCallKey = "brave-per-call-key"
)

func newBraveTestPlugin(t *testing.T, baseURL, apiKey string) *BravePlugin {
	t.Helper()
	p := &BravePlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		RateLimit: 1,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildBraveTestResponse(web []braveResult, news []braveResult) string {
	body := braveSearchResponse{Type: "search"}
	if web != nil {
		body.Web = &braveWebSection{Results: web}
	}
	if news != nil {
		body.News = &braveNewsSection{Results: news}
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestBrave_Identity(t *testing.T) {
	t.Parallel()
	p := &BravePlugin{}
	assert.Equal(t, "brave", p.ID())
	assert.Equal(t, "Brave Search", p.Name())
	assert.NotEmpty(t, p.Description())
}

func TestBrave_Capabilities(t *testing.T) {
	t.Parallel()
	p := &BravePlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindWeb)
	assert.Contains(t, caps.Kinds, KindNews)
	assert.Contains(t, caps.QueryIntents, IntentQuickLookup)
	assert.Contains(t, caps.QueryIntents, IntentNews)
	assert.True(t, caps.SupportsPagination)
	assert.True(t, caps.SupportsDateFilter)
	assert.Equal(t, braveMaxCount, caps.MaxResultsPerQuery)
}

func TestBrave_Residency_IsUSBlocked(t *testing.T) {
	t.Parallel()
	p := &BravePlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.False(t, tag.Region.IsEU())
}

func TestBrave_Search_HappyPath_WebAndNewsMerged(t *testing.T) {
	t.Parallel()
	web := []braveResult{
		{
			Type:        "search_result",
			Title:       "Sparse Transformers Guide",
			URL:         "https://example.com/sparse",
			Description: "After running sparse-attention models in production...",
			Language:    "en",
			PageAge:     "2024-09-12T10:00:00Z",
			MetaURL:     &braveMetaURL{Hostname: "example.com"},
		},
	}
	news := []braveResult{
		{
			Type:        "news_result",
			Title:       "OpenAI announces sparse-attention library",
			URL:         "https://news.example/sparse-news",
			Description: "OpenAI released a sparse attention reference impl...",
			Language:    "en",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, braveSearchPath, r.URL.Path)
		assert.Equal(t, braveTestServerKey, r.Header.Get(braveAuthHeader))
		assert.Equal(t, "transformer attention", r.URL.Query().Get("q"))
		assert.Equal(t, "5", r.URL.Query().Get("count"))
		assert.Equal(t, "moderate", r.URL.Query().Get("safesearch"), "default safesearch must propagate")

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildBraveTestResponse(web, news))
	}))
	defer srv.Close()

	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "transformer attention", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 2, "must merge web + news sections")

	// First result: web kind. Second: news (with smetaKindOverride).
	assert.Equal(t, "Sparse Transformers Guide", got.Results[0].Title)
	assert.Contains(t, got.Results[0].SourceMetadata, smetaSnippet)
	assert.Contains(t, got.Results[0].SourceMetadata, smetaDomain)
	assert.Equal(t, "example.com", got.Results[0].SourceMetadata[smetaDomain])

	assert.Equal(t, "OpenAI announces sparse-attention library", got.Results[1].Title)
	assert.Equal(t, string(KindNews), got.Results[1].SourceMetadata[smetaKindOverride])
}

func TestBrave_Search_PerCallCredentialOverridesServerKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, braveTestPerCallKey, r.Header.Get(braveAuthHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildBraveTestResponse(nil, nil))
	}))
	defer srv.Close()

	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceBrave: braveTestPerCallKey,
	})
	_, err := p.Search(ctx, SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
}

func TestBrave_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newBraveTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestBrave_Search_401ReturnsErrCredentialInvalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
	assert.False(t, p.Health(context.Background()).Healthy)
}

func TestBrave_Search_429ReturnsErrRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestBrave_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &BravePlugin{}
	_, err := p.Get(context.Background(), "abc", nil, FormatNative)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestBrave_CountClampedToMax(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "20", r.URL.Query().Get("count"), "count must clamp to braveMaxCount")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildBraveTestResponse(nil, nil))
	}))
	defer srv.Close()

	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 9999})
	require.NoError(t, err)
}

func TestBrave_ExtraSnippetsAppendedToAbstract(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildBraveTestResponse([]braveResult{{
			Title:         "Title",
			URL:           "https://example.com",
			Description:   "Primary description",
			ExtraSnippets: []string{"Extra 1", "Extra 2"},
		}}, nil))
	}))
	defer srv.Close()

	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.Contains(t, got.Results[0].Abstract, "Primary description")
	assert.Contains(t, got.Results[0].Abstract, "Extra 1")
	assert.Contains(t, got.Results[0].Abstract, "Extra 2")
}

// ---------------------------------------------------------------------------
// Live test (gated on BRAVE_SEARCH env var).
// ---------------------------------------------------------------------------

func TestBrave_LiveSmoke(t *testing.T) {
	apiKey := os.Getenv("BRAVE_SEARCH")
	if apiKey == "" {
		t.Skip("BRAVE_SEARCH env var not set; skipping live Brave smoke test")
	}
	p := newBraveTestPlugin(t, "", apiKey)

	got, err := p.Search(context.Background(), SearchParams{Query: "transformer attention mechanism", Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, got.Results)
	for _, r := range got.Results {
		t.Logf("hit: id=%s title=%q url=%s", r.ID, r.Title, r.URL)
		assert.True(t, strings.HasPrefix(r.ID, "brave:"))
		assert.NotEmpty(t, r.Title)
	}
}
