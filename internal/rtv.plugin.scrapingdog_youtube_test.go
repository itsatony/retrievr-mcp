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
// v3 cycle 2 / v2.3.0: Scrapingdog YouTube provider tests.
// ---------------------------------------------------------------------------

const (
	scrapingdogYTTestServerKey  = "sd-server-key"
	scrapingdogYTTestPerCallKey = "sd-per-call-key"
)

func newScrapingdogYouTubeTestPlugin(t *testing.T, baseURL, apiKey string) *ScrapingdogYouTubePlugin {
	t.Helper()
	p := &ScrapingdogYouTubePlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		RateLimit: 100,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildScrapingdogYouTubeTestResponse(items []scrapingdogVideoResult) string {
	body := scrapingdogYouTubeResponse{VideoResults: items}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestScrapingdogYouTube_Identity(t *testing.T) {
	t.Parallel()
	p := &ScrapingdogYouTubePlugin{}
	assert.Equal(t, "scrapingdog_youtube", p.ID())
	assert.NotEmpty(t, p.Name())
	assert.NotEmpty(t, p.Description())
}

func TestScrapingdogYouTube_Capabilities(t *testing.T) {
	t.Parallel()
	p := &ScrapingdogYouTubePlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindVideo)
	assert.False(t, caps.SupportsDateFilter, "scrapingdog YouTube date-filter is intentionally not surfaced in cycle 2")
}

func TestScrapingdogYouTube_Residency_IsUS(t *testing.T) {
	t.Parallel()
	p := &ScrapingdogYouTubePlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.False(t, tag.Region.IsEU())
}

func TestScrapingdogYouTube_Search_HappyPath(t *testing.T) {
	t.Parallel()
	items := []scrapingdogVideoResult{
		{
			Title:         "Never Gonna Give You Up",
			Link:          "https://www.youtube.com/watch?v=" + youtubeTestVideoID,
			Length:        "3:33",
			Views:         "1,500,000,000 views",
			PublishedDate: "15 years ago",
			Description:   "Rick Astley official video",
			Thumbnail:     scrapingdogThumbnail{Static: "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg"},
			Channel:       scrapingdogChannelInfo{Name: "Rick Astley", Link: "https://www.youtube.com/@RickAstleyYT", Verified: true},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, scrapingdogYouTubeSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, scrapingdogYTTestServerKey, q.Get(scrapingdogYouTubeQueryParamKey))
		assert.Equal(t, "rick astley", q.Get(scrapingdogYouTubeQueryParamQuery))
		assert.Equal(t, "us", q.Get("country"), "default country must propagate")
		assert.Equal(t, "en", q.Get("language"), "default language must propagate")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildScrapingdogYouTubeTestResponse(items))
	}))
	defer srv.Close()

	p := newScrapingdogYouTubeTestPlugin(t, srv.URL, scrapingdogYTTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "rick astley", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)

	pub := got.Results[0]
	assert.Equal(t, ContentTypeVideo, pub.ContentType)
	assert.Equal(t, "scrapingdog_youtube:"+youtubeTestVideoID, pub.ID)
	assert.Equal(t, youtubeTestVideoID, pub.SourceMetadata[MetaKeyYouTubeID])
	require.NotNil(t, pub.DurationSeconds)
	assert.Equal(t, 3*60+33, *pub.DurationSeconds)
	assert.Equal(t, 1500000000, pub.SourceMetadata[smetaViewCount])
}

func TestScrapingdogYouTube_Search_SkipsItemsWithoutVideoID(t *testing.T) {
	t.Parallel()
	items := []scrapingdogVideoResult{
		{Title: "channel-page", Link: "https://www.youtube.com/@SomeChannel"},
		{Title: "video", Link: "https://www.youtube.com/watch?v=abc123"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildScrapingdogYouTubeTestResponse(items))
	}))
	defer srv.Close()
	p := newScrapingdogYouTubeTestPlugin(t, srv.URL, scrapingdogYTTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.Equal(t, "scrapingdog_youtube:abc123", got.Results[0].ID)
}

func TestScrapingdogYouTube_Search_PerCallCredentialWins(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, scrapingdogYTTestPerCallKey, r.URL.Query().Get(scrapingdogYouTubeQueryParamKey))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildScrapingdogYouTubeTestResponse(nil))
	}))
	defer srv.Close()
	p := newScrapingdogYouTubeTestPlugin(t, srv.URL, scrapingdogYTTestServerKey)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceScrapingdogYouTube: scrapingdogYTTestPerCallKey,
	})
	_, err := p.Search(ctx, SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
}

func TestScrapingdogYouTube_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newScrapingdogYouTubeTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestScrapingdogYouTube_Search_PaymentRequiredMapsToCredentialInvalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer srv.Close()
	p := newScrapingdogYouTubeTestPlugin(t, srv.URL, scrapingdogYTTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestScrapingdogYouTube_Search_429MapsToRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newScrapingdogYouTubeTestPlugin(t, srv.URL, scrapingdogYTTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestScrapingdogYouTube_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &ScrapingdogYouTubePlugin{}
	_, err := p.Get(context.Background(), "abc", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestScrapingdogYouTube_LimitClampsResults(t *testing.T) {
	t.Parallel()
	items := make([]scrapingdogVideoResult, 5)
	for i := range items {
		items[i] = scrapingdogVideoResult{Title: "v", Link: "https://www.youtube.com/watch?v=id" + string(rune('a'+i))}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildScrapingdogYouTubeTestResponse(items))
	}))
	defer srv.Close()
	p := newScrapingdogYouTubeTestPlugin(t, srv.URL, scrapingdogYTTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 2})
	require.NoError(t, err)
	assert.Len(t, got.Results, 2, "limit must cap the result slice")
}

// ---------------------------------------------------------------------------
// Pure-helper tests
// ---------------------------------------------------------------------------

func TestExtractYouTubeVideoID(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://www.youtube.com/watch?v=" + youtubeTestVideoID:                         youtubeTestVideoID,
		"https://m.youtube.com/watch?v=" + youtubeTestVideoID:                           youtubeTestVideoID,
		"https://youtu.be/" + youtubeTestVideoID:                                        youtubeTestVideoID,
		"https://www.youtube.com/watch?v=" + youtubeTestVideoID + "&list=PLxxx&index=3": youtubeTestVideoID,
		"https://www.youtube.com/@SomeChannel":                                          "",
		"https://example.com/something":                                                 "",
		"":                                                                              "",
	}
	for in, want := range cases {
		assert.Equal(t, want, extractYouTubeVideoID(in), "input %q", in)
	}
}

func TestParseClockDurationSeconds(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"0:00":    0,
		"0:33":    33,
		"3:33":    3*60 + 33,
		"1:02:03": 3600 + 120 + 3,
		"":        0,
		"garbage": 0,
		"abc:def": 0,
	}
	for in, want := range cases {
		assert.Equal(t, want, parseClockDurationSeconds(in), "input %q", in)
	}
}

func TestParseViewCount(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"1,234 views":   1234,
		"1,500,000,000": 1500000000,
		"1.2K views":    1200,
		"1.2M":          1200000,
		"3.5B views":    3500000000,
		"42":            42,
		"":              0,
		"NaN views":     0,
	}
	for in, want := range cases {
		assert.Equal(t, want, parseViewCount(in), "input %q", in)
	}
}
