package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ghTestServerKey  = "gh-server-key"
	ghTestPerCallKey = "gh-per-call-key"
)

func newGitHubTestPlugin(t *testing.T, baseURL, apiKey string) *GitHubPlugin {
	t.Helper()
	p := &GitHubPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 0.5}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestGitHub_IdentityAndCapabilities(t *testing.T) {
	t.Parallel()
	p := &GitHubPlugin{}
	assert.Equal(t, "github", p.ID())
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindCode)
	assert.Contains(t, caps.QueryIntents, IntentCodeProvenance)
	assert.True(t, caps.SupportsAuthorFilter)
	assert.True(t, caps.SupportsCategoryFilter)
}

func TestGitHub_Residency_USBlocked(t *testing.T) {
	t.Parallel()
	tag := (&GitHubPlugin{}).Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.False(t, tag.Region.IsEU())
}

func TestGitHub_Search_HappyPath(t *testing.T) {
	t.Parallel()
	items := []githubRepoItem{
		{
			ID:              1,
			FullName:        "openai/sparse-attention",
			HTMLURL:         "https://github.com/openai/sparse-attention",
			Description:     "Sparse attention reference impl.",
			Language:        "Python",
			StargazersCount: 1240,
			ForksCount:      88,
			Topics:          []string{"transformer", "attention"},
			License:         &githubLicense{SPDXID: "Apache-2.0"},
			PushedAt:        "2024-08-30T12:34:56Z",
			Owner:           &githubOwner{Login: "openai", Type: "Organization"},
		},
	}
	resp := githubReposResponse{TotalCount: 1, Items: items}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, githubReposSearchPath, r.URL.Path)
		assert.Equal(t, githubAuthScheme+ghTestServerKey, r.Header.Get(githubAuthHeader))
		assert.Equal(t, githubAcceptValue, r.Header.Get(githubAcceptHeader))
		assert.Equal(t, "transformer attention", r.URL.Query().Get("q"))
		assert.Equal(t, "stars", r.URL.Query().Get("sort"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newGitHubTestPlugin(t, srv.URL, ghTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "transformer attention", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	r := got.Results[0]
	assert.Equal(t, "github:openai/sparse-attention", r.ID)
	assert.Equal(t, "openai/sparse-attention", r.Title)
	assert.Equal(t, "Apache-2.0", r.License)
	assert.Equal(t, "2024-08-30", r.Updated)
	assert.Equal(t, string(KindCode), r.SourceMetadata[smetaKindOverride])
	assert.Equal(t, 1240, r.SourceMetadata[smetaStars])
	assert.Equal(t, "Python", r.SourceMetadata[smetaCodeLang])
}

func TestGitHub_Search_PerCallCredentialOverride(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, githubAuthScheme+ghTestPerCallKey, r.Header.Get(githubAuthHeader))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(githubReposResponse{})
	}))
	defer srv.Close()
	p := newGitHubTestPlugin(t, srv.URL, ghTestServerKey)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{SourceGitHub: ghTestPerCallKey})
	_, err := p.Search(ctx, SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
}

func TestGitHub_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newGitHubTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestGitHub_Search_AuthErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		code int
		want error
	}{
		{"401", http.StatusUnauthorized, ErrCredentialInvalid},
		{"403", http.StatusForbidden, ErrCredentialInvalid},
		{"429", http.StatusTooManyRequests, ErrRateLimitExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()
			p := newGitHubTestPlugin(t, srv.URL, ghTestServerKey)
			_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
			assert.True(t, errors.Is(err, tc.want), "want %v got %v", tc.want, err)
		})
	}
}

func TestGitHub_Get_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/openai/sparse-attention", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(githubRepoItem{
			FullName: "openai/sparse-attention",
			HTMLURL:  "https://github.com/openai/sparse-attention",
		})
	}))
	defer srv.Close()
	p := newGitHubTestPlugin(t, srv.URL, ghTestServerKey)
	pub, err := p.Get(context.Background(), "openai/sparse-attention", nil, FormatNative)
	require.NoError(t, err)
	assert.Equal(t, "github:openai/sparse-attention", pub.ID)
}

func TestGitHub_Get_InvalidIDFormat(t *testing.T) {
	t.Parallel()
	p := newGitHubTestPlugin(t, "http://unused", ghTestServerKey)
	_, err := p.Get(context.Background(), "noslash", nil, FormatNative)
	assert.True(t, errors.Is(err, ErrInvalidID))
}

func TestGitHub_LiveSmoke(t *testing.T) {
	apiKey := os.Getenv("GITHUB_PUBLICREPOS_PAT")
	if apiKey == "" {
		t.Skip("GITHUB_PUBLICREPOS_PAT not set; skipping live smoke")
	}
	p := newGitHubTestPlugin(t, "", apiKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "language:go context cancellation", Limit: 3})
	require.NoError(t, err)
	for _, r := range got.Results {
		t.Logf("hit: id=%s stars=%v lang=%v", r.ID, r.SourceMetadata[smetaStars], r.SourceMetadata[smetaCodeLang])
		assert.True(t, strings.HasPrefix(r.ID, "github:"))
	}
}
