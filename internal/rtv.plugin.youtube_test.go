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
// v3 cycle 2 / v2.3.0: YouTube Data API v3 provider tests.
// ---------------------------------------------------------------------------

const (
	youtubeTestServerKey  = "youtube-server-key"
	youtubeTestPerCallKey = "youtube-per-call-key"
	youtubeTestVideoID    = "dQw4w9WgXcQ"
)

func newYouTubeTestPlugin(t *testing.T, baseURL, apiKey string) *YouTubePlugin {
	t.Helper()
	p := &YouTubePlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		RateLimit: 100, // high for tests
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildYouTubeSearchTestResponse(items []youtubeSearchItem, nextPage string, total int) string {
	body := youtubeSearchResponse{
		Kind:          "youtube#searchListResponse",
		NextPageToken: nextPage,
		PageInfo:      youtubePageInfo{TotalResults: total, ResultsPerPage: len(items)},
		Items:         items,
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func buildYouTubeVideosTestResponse(items []youtubeVideoItem) string {
	body := youtubeVideosResponse{
		Kind:  "youtube#videoListResponse",
		Items: items,
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestYouTube_Identity(t *testing.T) {
	t.Parallel()
	p := &YouTubePlugin{}
	assert.Equal(t, "youtube", p.ID())
	assert.Equal(t, "YouTube", p.Name())
	assert.NotEmpty(t, p.Description())
}

func TestYouTube_Capabilities(t *testing.T) {
	t.Parallel()
	p := &YouTubePlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindVideo)
	assert.Equal(t, youtubeMaxResultsCap, caps.MaxResultsPerQuery)
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsSortDate)
	assert.True(t, caps.SupportsPagination)
}

func TestYouTube_ContentTypes_Video(t *testing.T) {
	t.Parallel()
	p := &YouTubePlugin{}
	cts := p.ContentTypes()
	require.Len(t, cts, 1)
	assert.Equal(t, ContentTypeVideo, cts[0])
}

func TestYouTube_Residency_IsUS(t *testing.T) {
	t.Parallel()
	p := &YouTubePlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.False(t, tag.Region.IsEU())
}

func TestYouTube_Search_HappyPath(t *testing.T) {
	t.Parallel()
	item := youtubeSearchItem{
		Kind: "youtube#searchResult",
		ID:   youtubeID{Kind: "youtube#video", VideoID: youtubeTestVideoID},
		Snippet: youtubeSnippet{
			Title:        "Never Gonna Give You Up",
			Description:  "Rick Astley official video",
			ChannelID:    "UCuAXFkgsw1L7xaCfnd5JJOw",
			ChannelTitle: "Rick Astley",
			PublishedAt:  "2009-10-25T06:57:33Z",
			Thumbnails: map[string]youtubeThumb{
				"default": {URL: "https://i.ytimg.com/vi/dQw4w9WgXcQ/default.jpg"},
				"high":    {URL: "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg"},
			},
			LiveBroadcastContent: "none",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, youtubeSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "snippet", q.Get("part"))
		assert.Equal(t, "video", q.Get("type"))
		assert.Equal(t, "rick astley", q.Get("q"))
		assert.Equal(t, "5", q.Get("maxResults"))
		assert.Equal(t, youtubeTestServerKey, q.Get("key"))
		assert.Equal(t, "moderate", q.Get("safeSearch"), "default safeSearch must propagate")

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildYouTubeSearchTestResponse([]youtubeSearchItem{item}, "PAGE_2", 42))
	}))
	defer srv.Close()

	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "rick astley", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.True(t, got.HasMore, "nextPageToken => HasMore")
	assert.Equal(t, 42, got.Total)

	pub := got.Results[0]
	assert.Equal(t, ContentTypeVideo, pub.ContentType)
	assert.Equal(t, "youtube:"+youtubeTestVideoID, pub.ID)
	assert.Equal(t, "Never Gonna Give You Up", pub.Title)
	assert.Equal(t, "https://www.youtube.com/watch?v="+youtubeTestVideoID, pub.URL)
	assert.Equal(t, "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg", pub.ThumbnailURL)
	require.Len(t, pub.Authors, 1)
	assert.Equal(t, "Rick Astley", pub.Authors[0].Name)
	assert.Equal(t, "2009-10-25", pub.Published)
	assert.Equal(t, youtubeTestVideoID, pub.SourceMetadata[MetaKeyYouTubeID])
	assert.Equal(t, "UCuAXFkgsw1L7xaCfnd5JJOw", pub.SourceMetadata[smetaChannelID])
}

func TestYouTube_Search_SkipsItemsWithoutVideoID(t *testing.T) {
	t.Parallel()
	// Items with empty VideoID (e.g., channel/playlist match leaking through)
	// must be filtered.
	items := []youtubeSearchItem{
		{ID: youtubeID{Kind: "youtube#channel", VideoID: ""}, Snippet: youtubeSnippet{Title: "channel"}},
		{ID: youtubeID{Kind: "youtube#video", VideoID: "abc123"}, Snippet: youtubeSnippet{Title: "video"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildYouTubeSearchTestResponse(items, "", 2))
	}))
	defer srv.Close()

	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.Equal(t, "youtube:abc123", got.Results[0].ID)
}

func TestYouTube_Search_PerCallCredentialOverridesServerKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, youtubeTestPerCallKey, r.URL.Query().Get("key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildYouTubeSearchTestResponse(nil, "", 0))
	}))
	defer srv.Close()

	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceYouTube: youtubeTestPerCallKey,
	})
	_, err := p.Search(ctx, SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
}

func TestYouTube_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newYouTubeTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestYouTube_Search_QuotaExceededMapsToRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":403,"message":"quotaExceeded","errors":[{"reason":"quotaExceeded","message":"The request cannot be completed because you have exceeded your quota."}]}}`)
	}))
	defer srv.Close()
	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded), "quotaExceeded must map to ErrRateLimitExceeded; got %v", err)

	// Critical: quota-exhausted does NOT make the plugin unhealthy — it's a
	// throttling signal, not a failure. Health stays true so retry middleware
	// keeps the plugin in rotation for new requests.
	assert.True(t, p.Health(context.Background()).Healthy, "quota-exhausted must NOT flip healthy=false")
}

func TestYouTube_Search_KeyInvalidMapsToCredentialInvalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":400,"errors":[{"reason":"keyInvalid","message":"Bad Request"}]}}`)
	}))
	defer srv.Close()
	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestYouTube_Search_401ReturnsErrCredentialInvalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestYouTube_Get_FullDetail(t *testing.T) {
	t.Parallel()
	item := youtubeVideoItem{
		Kind: "youtube#video",
		ID:   youtubeTestVideoID,
		Snippet: youtubeSnippet{
			Title:        "Never Gonna Give You Up",
			ChannelTitle: "Rick Astley",
			ChannelID:    "UCuAXFkgsw1L7xaCfnd5JJOw",
			PublishedAt:  "2009-10-25T06:57:33Z",
			Thumbnails: map[string]youtubeThumb{
				"default": {URL: "https://i.ytimg.com/vi/dQw4w9WgXcQ/default.jpg"},
			},
		},
		ContentDetails: &youtubeContentDetails{Duration: "PT3M33S"},
		Statistics:     &youtubeStatistics{ViewCount: "1500000000", LikeCount: "18000000"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, youtubeVideosPath, r.URL.Path)
		assert.Equal(t, youtubeTestVideoID, r.URL.Query().Get("id"))
		assert.Contains(t, r.URL.Query().Get("part"), "contentDetails")
		assert.Contains(t, r.URL.Query().Get("part"), "statistics")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildYouTubeVideosTestResponse([]youtubeVideoItem{item}))
	}))
	defer srv.Close()

	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	pub, err := p.Get(context.Background(), youtubeTestVideoID, nil, FormatNative)
	require.NoError(t, err)
	require.NotNil(t, pub.DurationSeconds)
	assert.Equal(t, 3*60+33, *pub.DurationSeconds)
	assert.Equal(t, 18000000, pub.SourceMetadata[smetaLikeCount])
	assert.Equal(t, 1500000000, pub.SourceMetadata[smetaViewCount])
	require.NotNil(t, pub.EngagementScore)
	assert.Equal(t, 18000000, *pub.EngagementScore)
}

func TestYouTube_Get_NotFoundReturnsSourceNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildYouTubeVideosTestResponse(nil))
	}))
	defer srv.Close()
	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	_, err := p.Get(context.Background(), "missing", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceNotFound))
}

func TestYouTube_Get_EmptyIDInvalid(t *testing.T) {
	t.Parallel()
	p := newYouTubeTestPlugin(t, "http://unused", youtubeTestServerKey)
	_, err := p.Get(context.Background(), "", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidID))
}

func TestYouTube_LimitClampedToMax(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "50", r.URL.Query().Get("maxResults"), "limit must clamp to youtubeMaxResultsCap")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildYouTubeSearchTestResponse(nil, "", 0))
	}))
	defer srv.Close()
	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 9999})
	require.NoError(t, err)
}

func TestYouTube_DateFilterMapsToPublishedAfterBefore(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "2024-01-01T00:00:00Z", r.URL.Query().Get("publishedAfter"))
		assert.Equal(t, "2024-12-31T23:59:59Z", r.URL.Query().Get("publishedBefore"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildYouTubeSearchTestResponse(nil, "", 0))
	}))
	defer srv.Close()
	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x", Limit: 1,
		Filters: SearchFilters{DateFrom: "2024-01-01", DateTo: "2024-12-31"},
	})
	require.NoError(t, err)
}

func TestYouTube_SortDateMapsToOrderDate(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "date", r.URL.Query().Get("order"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildYouTubeSearchTestResponse(nil, "", 0))
	}))
	defer srv.Close()
	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1, Sort: SortDateDesc})
	require.NoError(t, err)
}

func TestParseISO8601DurationSeconds(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"PT0S":     0,
		"PT33S":    33,
		"PT3M33S":  3*60 + 33,
		"PT1H2M3S": 3600 + 120 + 3,
		"PT1H":     3600,
		"PT2M":     120,
		"":         0,
		"garbage":  0,
		"P1D":      0, // we don't support day-level (videos shouldn't have it)
	}
	for in, want := range cases {
		got := parseISO8601DurationSeconds(in)
		assert.Equal(t, want, got, "input %q", in)
	}
}

func TestPickBestThumbnail(t *testing.T) {
	t.Parallel()
	thumbs := map[string]youtubeThumb{
		"default": {URL: "low"},
		"high":    {URL: "high"},
	}
	assert.Equal(t, "high", pickBestThumbnail(thumbs))

	thumbs["maxres"] = youtubeThumb{URL: "max"}
	assert.Equal(t, "max", pickBestThumbnail(thumbs))

	assert.Equal(t, "", pickBestThumbnail(nil))
}

// ---------------------------------------------------------------------------
// Live test (gated on YOUTUBE_API_KEY env var).
// ---------------------------------------------------------------------------

func TestYouTube_LiveSmoke(t *testing.T) {
	apiKey := os.Getenv("YOUTUBE_API_KEY")
	if apiKey == "" {
		t.Skip("YOUTUBE_API_KEY env var not set; skipping live YouTube smoke test")
	}
	p := newYouTubeTestPlugin(t, "", apiKey)

	got, err := p.Search(context.Background(), SearchParams{Query: "kubernetes tutorial", Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, got.Results)
	for _, pub := range got.Results {
		assert.Equal(t, ContentTypeVideo, pub.ContentType)
		assert.NotEmpty(t, pub.Title)
		assert.True(t, strings.Contains(pub.URL, "youtube.com"))
		assert.NotEmpty(t, pub.SourceMetadata[MetaKeyYouTubeID])
	}
}
