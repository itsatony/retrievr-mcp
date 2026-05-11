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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v3 cycle 5 / v2.6.0: Reddit OAuth client-credentials tests.
// ---------------------------------------------------------------------------

const (
	redditTestClientID     = "cid"
	redditTestClientSecret = "csecret"
	redditTestCredential   = redditTestClientID + ":" + redditTestClientSecret
	redditTestAccessToken  = "test-access-token"
	redditTestSubName      = "kubernetes"
	redditTestPostID       = "abc123"
)

func newRedditTestPlugin(t *testing.T, baseURL, apiKey string) *RedditPlugin {
	t.Helper()
	p := &RedditPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		RateLimit: 100,
		Extra: map[string]string{
			redditExtraUserAgent: "retrievr-test/1.0 (test@example.com)",
		},
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func writeRedditTokenResponse(w http.ResponseWriter, token string, expiresIn int) {
	body := redditTokenResponse{AccessToken: token, TokenType: "bearer", ExpiresIn: expiresIn}
	b, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func writeRedditSearchResponse(w http.ResponseWriter, listing redditListing) {
	b, _ := json.Marshal(listing)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func makeRedditListing(subs []redditSubmission, after string) redditListing {
	children := make([]redditListingChild, 0, len(subs))
	for _, s := range subs {
		children = append(children, redditListingChild{Kind: "t3", Data: s})
	}
	return redditListing{Kind: "Listing", Data: redditListingData{After: after, Children: children}}
}

// redditTestServer mounts both /api/v1/access_token and /search on one
// httptest server so Initialize wires both correctly.
func redditTestServer(t *testing.T, search func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/access_token":
			// Basic auth must carry the test credentials.
			u, p, ok := r.BasicAuth()
			require.True(t, ok, "Basic auth header must be set on token request")
			assert.Equal(t, redditTestClientID, u)
			assert.Equal(t, redditTestClientSecret, p)
			body, _ := io.ReadAll(r.Body)
			assert.Contains(t, string(body), "grant_type=client_credentials")
			writeRedditTokenResponse(w, redditTestAccessToken, 3600)
		case redditSearchPath:
			search(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestReddit_Identity(t *testing.T) {
	t.Parallel()
	p := &RedditPlugin{}
	assert.Equal(t, "reddit", p.ID())
}

func TestReddit_Capabilities(t *testing.T) {
	t.Parallel()
	p := &RedditPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPost)
}

func TestReddit_Residency_IsUSCoveredBySCC(t *testing.T) {
	t.Parallel()
	p := &RedditPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.False(t, tag.Region.IsEU(), "Reddit must NOT be admissible under eu_strict")
	assert.Equal(t, DPACoveredBySCC, tag.DPAStatus)
}

func TestReddit_Search_HappyPath(t *testing.T) {
	t.Parallel()
	sub := redditSubmission{
		ID:          redditTestPostID,
		Name:        "t3_" + redditTestPostID,
		Title:       "Kubernetes 1.30 release notes",
		SelfText:    "Highlights include …",
		Author:      "alice",
		Subreddit:   redditTestSubName,
		Permalink:   "/r/kubernetes/comments/" + redditTestPostID + "/k8s_130/",
		URL:         "https://kubernetes.io/blog/k8s-1-30/",
		Score:       1234,
		NumComments: 56,
		CreatedUTC:  float64(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC).Unix()),
		Thumbnail:   "https://b.thumbs.redditmedia.com/x.jpg",
	}

	srv := redditTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Validate bearer token + query params.
		assert.Equal(t, "Bearer "+redditTestAccessToken, r.Header.Get("Authorization"))
		assert.NotEmpty(t, r.Header.Get("User-Agent"))
		q := r.URL.Query()
		assert.Equal(t, "kubernetes", q.Get("q"))
		assert.Equal(t, "10", q.Get("limit"))
		assert.Equal(t, "link", q.Get("type"))
		assert.Equal(t, "1", q.Get("raw_json"))
		writeRedditSearchResponse(w, makeRedditListing([]redditSubmission{sub}, "after_cursor"))
	})
	defer srv.Close()

	p := newRedditTestPlugin(t, srv.URL, redditTestCredential)
	got, err := p.Search(context.Background(), SearchParams{Query: "kubernetes", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.True(t, got.HasMore, "after_cursor non-empty => HasMore")

	pub := got.Results[0]
	assert.Equal(t, ContentTypePost, pub.ContentType)
	assert.Equal(t, "reddit:t3_"+redditTestPostID, pub.ID)
	assert.Equal(t, "Kubernetes 1.30 release notes", pub.Title)
	assert.Equal(t, "https://www.reddit.com"+sub.Permalink, pub.URL)
	assert.Equal(t, "https://b.thumbs.redditmedia.com/x.jpg", pub.ThumbnailURL)
	require.NotNil(t, pub.EngagementScore)
	assert.Equal(t, sub.Score+sub.NumComments, *pub.EngagementScore)
	assert.Equal(t, "u/alice", pub.SourceMetadata[smetaAuthorHandle])
	assert.Equal(t, redditTestSubName, pub.SourceMetadata[smetaSubreddit])
	assert.Equal(t, sub.Score, pub.SourceMetadata[smetaLikeCount])
	assert.Equal(t, sub.NumComments, pub.SourceMetadata[smetaReplyCount])
	assert.Equal(t, sub.URL, pub.SourceMetadata["external_url"], "non-permalink URL surfaces as external_url")
}

func TestReddit_Search_TokenCachedBetweenCalls(t *testing.T) {
	t.Parallel()
	tokenCalls := 0
	searchCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/access_token":
			tokenCalls++
			writeRedditTokenResponse(w, redditTestAccessToken, 3600)
		case redditSearchPath:
			searchCalls++
			writeRedditSearchResponse(w, makeRedditListing(nil, ""))
		}
	}))
	defer srv.Close()
	p := newRedditTestPlugin(t, srv.URL, redditTestCredential)

	for i := 0; i < 3; i++ {
		_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
		require.NoError(t, err)
	}
	assert.Equal(t, 1, tokenCalls, "token must be cached after first exchange")
	assert.Equal(t, 3, searchCalls)
}

func TestReddit_Search_401InvalidatesTokenCache(t *testing.T) {
	t.Parallel()
	tokenCalls := 0
	first := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/access_token":
			tokenCalls++
			writeRedditTokenResponse(w, redditTestAccessToken, 3600)
		case redditSearchPath:
			if first {
				first = false
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeRedditSearchResponse(w, makeRedditListing(nil, ""))
		}
	}))
	defer srv.Close()
	p := newRedditTestPlugin(t, srv.URL, redditTestCredential)

	// First call: 401 → token invalidated → ErrCredentialInvalid.
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))

	// Second call: token cache was cleared, so a fresh exchange happens.
	_, err = p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, 2, tokenCalls, "token cache must be re-exchanged after 401 from /search")
}

func TestReddit_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newRedditTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestReddit_Search_MalformedCredentialReturnsCredentialInvalid(t *testing.T) {
	t.Parallel()
	p := newRedditTestPlugin(t, "http://unused", "missing-colon")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestReddit_Token_RejectedOnUnauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/access_token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}))
	defer srv.Close()
	p := newRedditTestPlugin(t, srv.URL, redditTestCredential)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestReddit_Search_429MapsToRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := redditTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer srv.Close()
	p := newRedditTestPlugin(t, srv.URL, redditTestCredential)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestReddit_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &RedditPlugin{}
	_, err := p.Get(context.Background(), "t3_x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestParseRedditCredential(t *testing.T) {
	t.Parallel()
	cid, cs, err := parseRedditCredential("client:secret")
	require.NoError(t, err)
	assert.Equal(t, "client", cid)
	assert.Equal(t, "secret", cs)

	// Colon-in-secret: only first colon splits.
	cid, cs, err = parseRedditCredential("client:secret:with:colons")
	require.NoError(t, err)
	assert.Equal(t, "client", cid)
	assert.Equal(t, "secret:with:colons", cs)

	// Bad forms.
	for _, bad := range []string{"", "no-colon", ":no-id", "no-secret:"} {
		_, _, err := parseRedditCredential(bad)
		require.Error(t, err, "input %q must error", bad)
		assert.True(t, errors.Is(err, ErrCredentialInvalid))
	}
}

func TestRedditValidThumbnail(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "self", "default", "nsfw", "spoiler", "image"} {
		assert.False(t, redditValidThumbnail(bad), "input %q must be invalid", bad)
	}
	assert.True(t, redditValidThumbnail("https://b.thumbs.redditmedia.com/x.jpg"))
	assert.False(t, redditValidThumbnail("not-a-url"))
	// Self-check: must not panic on near-empty input.
	assert.False(t, redditValidThumbnail(strings.Repeat(" ", 1)))
}
