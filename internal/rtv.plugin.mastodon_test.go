package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v3 cycle 5 / v2.6.0: Mastodon public-statuses search tests.
// ---------------------------------------------------------------------------

func newMastodonTestPlugin(t *testing.T, baseURL, region string) *MastodonPlugin {
	t.Helper()
	p := &MastodonPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		RateLimit: 100,
		Extra: map[string]string{
			mastodonExtraRegion: region,
		},
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildMastodonTestResponse(statuses []mastodonStatus) string {
	b, _ := json.Marshal(mastodonSearchResponse{Statuses: statuses})
	return string(b)
}

func TestMastodon_Identity(t *testing.T) {
	t.Parallel()
	p := &MastodonPlugin{}
	assert.Equal(t, "mastodon", p.ID())
}

func TestMastodon_Capabilities(t *testing.T) {
	t.Parallel()
	p := &MastodonPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPost)
	assert.Equal(t, mastodonMaxLimitCap, caps.MaxResultsPerQuery)
}

// TestMastodon_Residency_DefaultIsEU confirms the unconfigured/default
// instance tags as EU (so the eu_strict gate admits without opt-in).
func TestMastodon_Residency_DefaultIsEU(t *testing.T) {
	t.Parallel()
	p := &MastodonPlugin{}
	require.NoError(t, p.Initialize(context.Background(), PluginConfig{Enabled: true}))
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
	assert.True(t, tag.Region.IsEU(), "default Mastodon must be admissible under eu_strict")
}

// TestMastodon_Residency_DynamicByConfig confirms operator-declared region
// drives residency. Pointing at a US instance MUST flip the tag so the
// eu_strict gate stays truthful.
func TestMastodon_Residency_DynamicByConfig(t *testing.T) {
	t.Parallel()
	p := newMastodonTestPlugin(t, "https://us-instance.example", string(RegionUS))
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.False(t, tag.Region.IsEU(),
		"US-declared instance must NOT pass IsEU() — eu_strict gate truthfulness depends on it")
}

func TestMastodon_Search_HappyPath(t *testing.T) {
	t.Parallel()
	statuses := []mastodonStatus{
		{
			ID:              "12345",
			URL:             "https://mastodon.social/@alice/12345",
			Content:         "<p>Hello <strong>fediverse</strong>!</p>",
			CreatedAt:       "2026-05-11T10:00:00Z",
			Language:        "en",
			FavouritesCount: 42,
			ReblogsCount:    7,
			RepliesCount:    3,
			Account: mastodonAccount{
				ID:          "1",
				Username:    "alice",
				Acct:        "alice", // local user
				DisplayName: "Alice",
				URL:         "https://mastodon.social/@alice",
				Verified:    true,
			},
			MediaAttachments: []mastodonMediaItem{
				{ID: "m1", Type: "image", URL: "https://mastodon.social/media/abc.jpg"},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, mastodonSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "fediverse", q.Get("q"))
		assert.Equal(t, "statuses", q.Get("type"))
		assert.Equal(t, "false", q.Get("resolve"), "resolve=false must be set for privacy-respecting default")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildMastodonTestResponse(statuses))
	}))
	defer srv.Close()

	p := newMastodonTestPlugin(t, srv.URL, string(RegionEU))
	got, err := p.Search(context.Background(), SearchParams{Query: "fediverse", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)

	pub := got.Results[0]
	assert.Equal(t, ContentTypePost, pub.ContentType)
	assert.Equal(t, "mastodon:12345", pub.ID)
	assert.Equal(t, "Hello fediverse!", pub.Abstract, "HTML tags must be stripped from content")
	require.NotNil(t, pub.EngagementScore)
	assert.Equal(t, 42+7+3, *pub.EngagementScore, "engagement = favourites + reblogs + replies")
	assert.Equal(t, 42, pub.SourceMetadata[smetaLikeCount])
	assert.Equal(t, 7, pub.SourceMetadata[smetaRepostCount])
	assert.Equal(t, 3, pub.SourceMetadata[smetaReplyCount])
	assert.Equal(t, "en", pub.Language)
	assert.Equal(t, true, pub.SourceMetadata[smetaVerified])
	assert.Equal(t, 1, pub.SourceMetadata[smetaMediaCount])
	assert.Equal(t, "https://mastodon.social/media/abc.jpg", pub.ThumbnailURL)
	// Handle composed for local user. url.Hostname() strips the port, so
	// the test plugin's "127.0.0.1:NNNN" base becomes "127.0.0.1".
	handle, _ := pub.SourceMetadata[smetaAuthorHandle].(string)
	assert.True(t, strings.HasPrefix(handle, "@alice@"), "handle %q must start with @alice@", handle)
}

func TestMastodon_Search_RemoteUserAcctPreserved(t *testing.T) {
	t.Parallel()
	statuses := []mastodonStatus{
		{
			ID:        "1",
			URL:       "https://mastodon.social/@bob",
			Content:   "<p>x</p>",
			CreatedAt: "2026-01-01T00:00:00Z",
			Account: mastodonAccount{
				Username: "bob",
				Acct:     "bob@some-remote.example", // remote user
				URL:      "https://some-remote.example/@bob",
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildMastodonTestResponse(statuses))
	}))
	defer srv.Close()
	p := newMastodonTestPlugin(t, srv.URL, string(RegionEU))
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, "@bob@some-remote.example", got.Results[0].SourceMetadata[smetaAuthorHandle])
}

func TestMastodon_Search_429MapsToRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newMastodonTestPlugin(t, srv.URL, string(RegionEU))
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestMastodon_Search_401MapsToCredentialRequired(t *testing.T) {
	t.Parallel()
	// Some instances require auth even for /search. Surface explicitly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newMastodonTestPlugin(t, srv.URL, string(RegionEU))
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestMastodon_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &MastodonPlugin{}
	_, err := p.Get(context.Background(), "1", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}
