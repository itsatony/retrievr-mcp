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
// v6 cycle 2 / v2.15.0 — iTunes tests.
// ---------------------------------------------------------------------------

func newITunesTestPlugin(t *testing.T, baseURL string) *ITunesPlugin {
	t.Helper()
	p := &ITunesPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestITunes_Identity(t *testing.T) {
	t.Parallel()
	p := &ITunesPlugin{}
	assert.Equal(t, SourceITunes, p.ID())
}

func TestITunes_Capabilities(t *testing.T) {
	t.Parallel()
	p := &ITunesPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindAudio)
	assert.False(t, caps.RequiresCredential)
}

func TestITunes_Residency(t *testing.T) {
	t.Parallel()
	p := &ITunesPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestITunes_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := itunesSearchResponse{
		ResultCount: 1,
		Results: []itunesEpisode{{
			Kind:            "podcast-episode",
			TrackID:         1234567890,
			CollectionID:    9876543210,
			TrackName:       "Intelligent Machines #42",
			CollectionName:  "Intelligent Machines",
			ArtistName:      "Acme Media",
			PreviewURL:      "https://traffic.megaphone.fm/abc.mp3",
			TrackViewURL:    "https://podcasts.apple.com/us/podcast/intelligent-machines/id1234567890?i=1234",
			ReleaseDate:     "2024-06-15T12:00:00Z",
			TrackTimeMillis: 3600000,
			ArtworkURL600:   "https://is1-ssl.mzstatic.com/image/.../600x600bb.jpg",
			Description:     "<p>Episode notes.</p>",
			Country:         "USA",
			Language:        "EN",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, itunesSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "intelligent machines", q.Get(itunesParamTerm))
		assert.Equal(t, itunesMediaPodcast, q.Get(itunesParamMedia))
		assert.Equal(t, itunesEntityEpisode, q.Get(itunesParamEntity))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newITunesTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "intelligent machines", Limit: 25})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "itunes:1234567890", pub.ID)
	assert.Equal(t, ContentTypeAudio, pub.ContentType)
	assert.Equal(t, "Intelligent Machines #42", pub.Title)
	assert.Equal(t, "2024-06-15", pub.Published)
	require.NotNil(t, pub.DurationSeconds)
	assert.Equal(t, 3600, *pub.DurationSeconds)
	assert.Equal(t, "itunes:1234567890", pub.SourceMetadata[MetaKeyAudioID])
	assert.Equal(t, "Intelligent Machines", pub.SourceMetadata[smetaAudioShowTitle])
	assert.Equal(t, "Acme Media", pub.SourceMetadata[smetaAudioPublisher])
}

func TestITunes_Search_CountryFilter(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "de", r.URL.Query().Get(itunesParamCountry))
		_, _ = io.WriteString(w, `{"resultCount":0,"results":[]}`)
	}))
	defer srv.Close()
	p := newITunesTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "podcast",
		Filters: SearchFilters{Categories: []string{"DE"}},
	})
	require.NoError(t, err)
}

func TestITunes_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newITunesTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestITunes_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &ITunesPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}
