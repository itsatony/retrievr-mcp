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
// v3 cycle 5 / v2.6.0: Bluesky (AT Protocol) search tests.
// ---------------------------------------------------------------------------

const (
	blueskyTestURI    = "at://did:plc:abc123/app.bsky.feed.post/3kdq5xyz"
	blueskyTestRKey   = "3kdq5xyz"
	blueskyTestHandle = "alice.bsky.social"
)

func newBlueskyTestPlugin(t *testing.T, baseURL string) *BlueskyPlugin {
	t.Helper()
	p := &BlueskyPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		RateLimit: 100,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildBlueskyTestResponse(posts []blueskyPost, cursor string) string {
	b, _ := json.Marshal(blueskySearchResponse{Posts: posts, Cursor: cursor})
	return string(b)
}

func TestBluesky_Identity(t *testing.T) {
	t.Parallel()
	p := &BlueskyPlugin{}
	assert.Equal(t, "bluesky", p.ID())
}

func TestBluesky_Capabilities(t *testing.T) {
	t.Parallel()
	p := &BlueskyPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPost)
	assert.Equal(t, blueskyMaxLimitCap, caps.MaxResultsPerQuery)
}

func TestBluesky_Residency_IsPublicResearch(t *testing.T) {
	t.Parallel()
	p := &BlueskyPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionPublicResearch, tag.Region)
	assert.True(t, tag.Region.IsPublicResearch(),
		"Bluesky must tag as public-research-infrastructure for eu_strict opt-in admissibility")
}

func TestBluesky_Search_HappyPath(t *testing.T) {
	t.Parallel()
	posts := []blueskyPost{
		{
			URI: blueskyTestURI,
			CID: "cid_xyz",
			Author: blueskyAuthor{
				DID:         "did:plc:abc123",
				Handle:      blueskyTestHandle,
				DisplayName: "Alice",
				Avatar:      "https://av.example.com/a.jpg",
			},
			Record: blueskyRecord{
				Type:      "app.bsky.feed.post",
				Text:      "Bluesky post about retrievr v3",
				Langs:     []string{"en"},
				CreatedAt: "2026-05-11T10:00:00Z",
			},
			IndexedAt:   "2026-05-11T10:00:01Z",
			LikeCount:   100,
			RepostCount: 20,
			ReplyCount:  5,
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, blueskySearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "retrievr", q.Get("q"))
		assert.Equal(t, "25", q.Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildBlueskyTestResponse(posts, "next_cursor"))
	}))
	defer srv.Close()

	p := newBlueskyTestPlugin(t, srv.URL)
	got, err := p.Search(context.Background(), SearchParams{Query: "retrievr"})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.True(t, got.HasMore, "non-empty cursor => HasMore")

	pub := got.Results[0]
	assert.Equal(t, ContentTypePost, pub.ContentType)
	assert.Equal(t, "bluesky:"+blueskyTestRKey, pub.ID)
	// atproto_uri populated as the dedup key.
	assert.Equal(t, blueskyTestURI, pub.SourceMetadata[MetaKeyAtprotoURI])
	// Platform URL derived from handle + rkey.
	assert.Equal(t, "https://bsky.app/profile/"+blueskyTestHandle+"/post/"+blueskyTestRKey, pub.URL)
	assert.Equal(t, "Bluesky post about retrievr v3", pub.Abstract)
	assert.Equal(t, "en", pub.Language)
	assert.Equal(t, "https://av.example.com/a.jpg", pub.ThumbnailURL)
	require.NotNil(t, pub.EngagementScore)
	assert.Equal(t, 100+20+5, *pub.EngagementScore)
	assert.Equal(t, blueskyTestHandle, pub.SourceMetadata[smetaAuthorHandle])
	assert.Equal(t, 100, pub.SourceMetadata[smetaLikeCount])
}

func TestBluesky_Search_429MapsToRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newBlueskyTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestBluesky_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &BlueskyPlugin{}
	_, err := p.Get(context.Background(), "at://...", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestBlueskyRKey(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"at://did:plc:abc/app.bsky.feed.post/3kdq5xyz": "3kdq5xyz",
		"at://did:plc:abc/app.bsky.feed.post/":         "at://did:plc:abc/app.bsky.feed.post/",
		"":                                             "unknown",
		"3kdq5xyz":                                     "3kdq5xyz",
	}
	for in, want := range cases {
		assert.Equal(t, want, blueskyRKey(in), "input %q", in)
	}
}

func TestBlueskyPlatformURL(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "https://bsky.app/profile/alice.bsky.social/post/3kdq5xyz",
		blueskyPlatformURL(blueskyTestURI, "alice.bsky.social"))
	assert.Equal(t, "", blueskyPlatformURL("", "alice.bsky.social"))
	assert.Equal(t, "", blueskyPlatformURL(blueskyTestURI, ""))
}
