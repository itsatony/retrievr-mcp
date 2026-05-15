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
// v6 cycle 2 / v2.15.0 — Listen Notes tests.
// ---------------------------------------------------------------------------

func newListenNotesTestPlugin(t *testing.T, baseURL, apiKey string) *ListenNotesPlugin {
	t.Helper()
	p := &ListenNotesPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestListenNotes_Identity(t *testing.T) {
	t.Parallel()
	p := &ListenNotesPlugin{}
	assert.Equal(t, SourceListenNotes, p.ID())
}

func TestListenNotes_Capabilities(t *testing.T) {
	t.Parallel()
	p := &ListenNotesPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindAudio)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsLanguageFilter)
	assert.True(t, caps.SupportsSortDate)
}

func TestListenNotes_Residency(t *testing.T) {
	t.Parallel()
	p := &ListenNotesPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestListenNotes_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newListenNotesTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestListenNotes_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := listennotesSearchResponse{
		Total: 1,
		Count: 1,
		Results: []listennotesEpisode{{
			ID:                  "abc123",
			TitleOriginal:       "Episode 42: Machine Learning",
			DescriptionOriginal: "<p>An interview about ML.</p>",
			Audio:               "https://cdn.listennotes.com/audio/abc123.mp3",
			AudioLengthSec:      3600,
			PubDateMS:           1701432000000,
			ExplicitContent:     false,
			Link:                "https://www.listennotes.com/podcasts/show/abc123/",
			Image:               "https://cdn.listennotes.com/img/abc123.jpg",
			Thumbnail:           "https://cdn.listennotes.com/thumb/abc123.jpg",
			Podcast: listennotesShow{
				ID:                "show1",
				TitleOriginal:     "ML Pod",
				PublisherOriginal: "Acme Media",
				Language:          "English",
			},
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, listennotesSearchPath, r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get(listennotesHeaderAPIKey))
		q := r.URL.Query()
		assert.Equal(t, "machine learning", q.Get(listennotesParamQ))
		assert.Equal(t, listennotesTypeEpisode, q.Get(listennotesParamType))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newListenNotesTestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "machine learning"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "listennotes:abc123", pub.ID)
	assert.Equal(t, ContentTypeAudio, pub.ContentType)
	assert.Equal(t, "Episode 42: Machine Learning", pub.Title)
	assert.Equal(t, "listennotes:abc123", pub.SourceMetadata[MetaKeyAudioID])
	assert.Equal(t, "ML Pod", pub.SourceMetadata[smetaAudioShowTitle])
	assert.Equal(t, "Acme Media", pub.SourceMetadata[smetaAudioPublisher])
	assert.Equal(t, 3600, pub.SourceMetadata[smetaAudioDurationSeconds])
	require.NotNil(t, pub.DurationSeconds)
	assert.Equal(t, 3600, *pub.DurationSeconds)
	assert.Equal(t, "audio/mpeg", pub.MediaMime)
	assert.Equal(t, "https://cdn.listennotes.com/audio/abc123.mp3", pub.MediaURL)
	assert.Equal(t, "2023-12-01", pub.Published)
}

func TestListenNotes_Search_LanguageMapping(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "German", r.URL.Query().Get(listennotesParamLanguage))
		assert.Equal(t, "1", r.URL.Query().Get(listennotesParamSortByDate))
		_, _ = io.WriteString(w, `{"results":[],"total":0}`)
	}))
	defer srv.Close()
	p := newListenNotesTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "podcast",
		Sort:    SortDateDesc,
		Filters: SearchFilters{Language: "de"},
	})
	require.NoError(t, err)
}

func TestListenNotes_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newListenNotesTestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestListenNotes_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newListenNotesTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestListenNotes_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &ListenNotesPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestListenNotes_LanguageName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "English", listennotesLanguageName("en"))
	assert.Equal(t, "Spanish", listennotesLanguageName("es-MX"))
	assert.Equal(t, "Klingon", listennotesLanguageName("Klingon"))
}
