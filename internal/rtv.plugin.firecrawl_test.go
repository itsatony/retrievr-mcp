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

const (
	fcTestServerKey  = "fc-server-key"
	fcTestPerCallKey = "fc-per-call-key"
)

func newFirecrawlTestPlugin(t *testing.T, baseURL, apiKey string) *FirecrawlPlugin {
	t.Helper()
	p := &FirecrawlPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 2}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildFirecrawlResponse(success bool, data []firecrawlResult, errMsg string) string {
	body := firecrawlSearchResponse{Success: success, Data: data, Error: errMsg}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestFirecrawl_IdentityAndCapabilities(t *testing.T) {
	t.Parallel()
	p := &FirecrawlPlugin{}
	assert.Equal(t, "firecrawl", p.ID())
	assert.NotEmpty(t, p.Description())
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindWeb)
	assert.True(t, caps.SupportsFullText)
	assert.Contains(t, caps.QueryIntents, IntentDeepResearch)
}

func TestFirecrawl_Residency_USBlocked(t *testing.T) {
	t.Parallel()
	p := &FirecrawlPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.False(t, tag.Region.IsEU())
}

func TestFirecrawl_Search_HappyPath(t *testing.T) {
	t.Parallel()
	results := []firecrawlResult{
		{
			URL:         "https://example.com/page",
			Title:       "Sparse Transformers",
			Description: "After running sparse-attention models in production...",
			Metadata:    map[string]any{"language": "en", "publishedTime": "2024-09-12"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, firecrawlSearchPath, r.URL.Path)
		assert.Equal(t, firecrawlAuthScheme+fcTestServerKey, r.Header.Get(firecrawlAuthHeader))

		var body firecrawlSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "test query", body.Query)
		assert.Equal(t, 5, body.Limit)
		assert.Nil(t, body.ScrapeOptions, "include_markdown=false must omit scrapeOptions")

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildFirecrawlResponse(true, results, ""))
	}))
	defer srv.Close()

	p := newFirecrawlTestPlugin(t, srv.URL, fcTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "test query", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.Equal(t, "Sparse Transformers", got.Results[0].Title)
	assert.Equal(t, "en", got.Results[0].SourceMetadata[smetaLanguage])
	assert.Equal(t, "2024-09-12", got.Results[0].SourceMetadata[smetaPublishedAt])
	assert.True(t, strings.HasPrefix(got.Results[0].ID, "firecrawl:"))
}

func TestFirecrawl_Search_PerCallCredentialOverride(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, firecrawlAuthScheme+fcTestPerCallKey, r.Header.Get(firecrawlAuthHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildFirecrawlResponse(true, nil, ""))
	}))
	defer srv.Close()
	p := newFirecrawlTestPlugin(t, srv.URL, fcTestServerKey)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{SourceFirecrawl: fcTestPerCallKey})
	_, err := p.Search(ctx, SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
}

func TestFirecrawl_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newFirecrawlTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestFirecrawl_Search_401And429(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		statusCode int
		wantSent   error
	}{
		{"401_invalid", http.StatusUnauthorized, ErrCredentialInvalid},
		{"429_ratelimit", http.StatusTooManyRequests, ErrRateLimitExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
			}))
			defer srv.Close()
			p := newFirecrawlTestPlugin(t, srv.URL, fcTestServerKey)
			_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
			assert.True(t, errors.Is(err, tc.wantSent), "got %v", err)
		})
	}
}

func TestFirecrawl_Search_SuccessFalseSurfacesError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildFirecrawlResponse(false, nil, "internal error"))
	}))
	defer srv.Close()
	p := newFirecrawlTestPlugin(t, srv.URL, fcTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "internal error")
}

func TestFirecrawl_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &FirecrawlPlugin{}
	_, err := p.Get(context.Background(), "abc", nil, FormatNative)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestFirecrawl_LiveSmoke(t *testing.T) {
	apiKey := os.Getenv("FIRECRAWL_APIKEY")
	if apiKey == "" {
		t.Skip("FIRECRAWL_APIKEY not set; skipping live smoke")
	}
	p := newFirecrawlTestPlugin(t, "", apiKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "go programming language", Limit: 3})
	require.NoError(t, err)
	for _, r := range got.Results {
		t.Logf("hit: id=%s title=%q url=%s", r.ID, r.Title, r.URL)
	}
}
