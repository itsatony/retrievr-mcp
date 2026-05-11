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
// v3 cycle 4 / v2.5.0: Wikimedia Commons image-search tests.
// ---------------------------------------------------------------------------

func newWikimediaTestPlugin(t *testing.T, baseURL string) *WikimediaPlugin {
	t.Helper()
	p := &WikimediaPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		RateLimit: 100,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildWikimediaTestResponse(pages map[string]wikimediaPage) string {
	body := wikimediaQueryResponse{Query: &wikimediaQueryBlock{Pages: pages}}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestWikimedia_Identity(t *testing.T) {
	t.Parallel()
	p := &WikimediaPlugin{}
	assert.Equal(t, "wikimedia", p.ID())
}

func TestWikimedia_Capabilities(t *testing.T) {
	t.Parallel()
	p := &WikimediaPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindImage)
}

func TestWikimedia_Residency_IsPublicResearch(t *testing.T) {
	t.Parallel()
	p := &WikimediaPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionPublicResearch, tag.Region)
	assert.True(t, tag.Region.IsPublicResearch(),
		"Wikimedia must tag as public-research-infrastructure for eu_strict opt-in admissibility")
}

func TestWikimedia_Search_HappyPath_LicenseFirstClass(t *testing.T) {
	t.Parallel()
	pages := map[string]wikimediaPage{
		"42": {
			PageID:    42,
			Namespace: 6,
			Title:     "File:Mona Lisa.jpg",
			Index:     1,
			ImageInfo: []wikimediaImageInfo{{
				URL:            "https://upload.wikimedia.org/wikipedia/commons/6/6a/Mona_Lisa.jpg",
				DescriptionURL: "https://commons.wikimedia.org/wiki/File:Mona_Lisa.jpg",
				MIME:           "image/jpeg",
				Size:           4500000,
				Width:          2000,
				Height:         3000,
				User:           "Wikipedian",
				ExtMetadata: map[string]wikimediaExtField{
					"LicenseShortName": {Value: "Public domain"},
					"LicenseUrl":       {Value: "https://creativecommons.org/publicdomain/mark/1.0/"},
					"Artist":           {Value: `<a href="//commons.wikimedia.org/wiki/Leonardo">Leonardo da Vinci</a>`},
				},
			}},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, wikimediaAPIPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "query", q.Get("action"))
		assert.Equal(t, "search", q.Get("generator"))
		assert.Equal(t, "6", q.Get("gsrnamespace"))
		assert.Equal(t, "mona lisa", q.Get("gsrsearch"))
		assert.Contains(t, q.Get("iiprop"), "extmetadata", "extmetadata must be requested for license info")
		assert.NotEmpty(t, r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildWikimediaTestResponse(pages))
	}))
	defer srv.Close()

	p := newWikimediaTestPlugin(t, srv.URL)
	got, err := p.Search(context.Background(), SearchParams{Query: "mona lisa", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)

	pub := got.Results[0]
	assert.Equal(t, ContentTypeImage, pub.ContentType)
	assert.Equal(t, "wikimedia:File:Mona_Lisa.jpg", pub.ID)
	assert.Equal(t, "File:Mona Lisa.jpg", pub.Title)
	assert.Equal(t, "https://upload.wikimedia.org/wikipedia/commons/6/6a/Mona_Lisa.jpg", pub.MediaURL)
	assert.Equal(t, "image/jpeg", pub.MediaMime)
	// License must be populated for downstream-safe reuse.
	assert.Equal(t, "Public domain", pub.License)
	assert.Equal(t, "File:Mona_Lisa.jpg", pub.SourceMetadata[MetaKeyWikimediaFile])
	assert.Equal(t, 2000, pub.SourceMetadata[smetaWidth])
	assert.Equal(t, 3000, pub.SourceMetadata[smetaHeight])
	// HTML tags stripped from Artist.
	require.NotEmpty(t, pub.Authors)
	assert.Equal(t, "Leonardo da Vinci", pub.Authors[0].Name)
	assert.Contains(t, pub.SourceMetadata[smetaLicenseURL], "creativecommons.org")
}

func TestWikimedia_Search_FiltersOutEntriesWithoutMediaURL(t *testing.T) {
	t.Parallel()
	pages := map[string]wikimediaPage{
		"1": {PageID: 1, Title: "File:Empty.jpg", ImageInfo: []wikimediaImageInfo{{URL: ""}}, Index: 1},
		"2": {PageID: 2, Title: "File:Valid.jpg", ImageInfo: []wikimediaImageInfo{{URL: "https://x/v.jpg"}}, Index: 2},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildWikimediaTestResponse(pages))
	}))
	defer srv.Close()
	p := newWikimediaTestPlugin(t, srv.URL)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got.Results, 1, "entries with empty URL must be filtered out")
	assert.Contains(t, got.Results[0].MediaURL, "/v.jpg")
}

func TestWikimedia_Search_StableOrderByIndex(t *testing.T) {
	t.Parallel()
	// Map iteration is non-deterministic; sortByIndex must impose the rank.
	pages := map[string]wikimediaPage{
		"3": {PageID: 3, Title: "File:Third.jpg", ImageInfo: []wikimediaImageInfo{{URL: "https://x/3.jpg"}}, Index: 3},
		"1": {PageID: 1, Title: "File:First.jpg", ImageInfo: []wikimediaImageInfo{{URL: "https://x/1.jpg"}}, Index: 1},
		"2": {PageID: 2, Title: "File:Second.jpg", ImageInfo: []wikimediaImageInfo{{URL: "https://x/2.jpg"}}, Index: 2},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildWikimediaTestResponse(pages))
	}))
	defer srv.Close()
	p := newWikimediaTestPlugin(t, srv.URL)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got.Results, 3)
	assert.Equal(t, "File:First.jpg", got.Results[0].Title)
	assert.Equal(t, "File:Second.jpg", got.Results[1].Title)
	assert.Equal(t, "File:Third.jpg", got.Results[2].Title)
}

func TestWikimedia_Search_429MapsToRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newWikimediaTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestWikimedia_Search_APIErrorPropagated(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":{"code":"badtoken","info":"Invalid CSRF token"}}`)
	}))
	defer srv.Close()
	p := newWikimediaTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "badtoken")
}

func TestWikimedia_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &WikimediaPlugin{}
	_, err := p.Get(context.Background(), "File:X.jpg", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestNormalizeWikimediaFile(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "File:Mona_Lisa.jpg", normalizeWikimediaFile("File:Mona Lisa.jpg"))
	assert.Equal(t, "File:Already_Underscored.jpg", normalizeWikimediaFile("File:Already_Underscored.jpg"))
}
