package internal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v3 cycle 4 / v2.5.0: Brave image-search dispatch tests.
//
// Brave's plugin gained an image-search code path. This file covers the
// dispatch behavior: ContentType=Image hits /res/v1/images/search; anything
// else continues to /res/v1/web/search (covered in rtv.plugin.brave_test.go).
// ---------------------------------------------------------------------------

func buildBraveImageTestResponse(results []braveImageResult) string {
	body := braveImageSearchResponse{Type: "images", Results: results}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestBrave_ContentTypes_IncludesImage(t *testing.T) {
	t.Parallel()
	p := &BravePlugin{}
	cts := p.ContentTypes()
	assert.Contains(t, cts, ContentTypeImage,
		"v3 cycle 4 added image-search dispatch on Brave; ContentTypes must surface it")
	assert.Contains(t, cts, ContentTypeAny, "web/news path must still be the default")
}

func TestBrave_Capabilities_KindsIncludeImage(t *testing.T) {
	t.Parallel()
	p := &BravePlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindImage)
}

func TestBrave_Search_DispatchesToImagesEndpoint(t *testing.T) {
	t.Parallel()
	imageResults := []braveImageResult{
		{
			Type:   "image_result",
			Title:  "Brandenburg Gate at sunset",
			URL:    "https://example.com/gallery/page",
			Source: "example.com",
			Thumbnail: braveImageThumb{
				Src:      "https://example.com/thumb.jpg",
				Original: "https://example.com/full.jpg",
			},
			Properties: braveImageProperties{
				URL:    "https://example.com/full.jpg",
				Width:  1920,
				Height: 1080,
				Format: "jpg",
			},
		},
	}

	imageEndpointHit := false
	webEndpointHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case braveImageSearchPath:
			imageEndpointHit = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, buildBraveImageTestResponse(imageResults))
		case braveSearchPath:
			webEndpointHit = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, buildBraveTestResponse(nil, nil))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{
		Query:       "brandenburg gate",
		Limit:       5,
		ContentType: ContentTypeImage, // <-- the dispatch trigger
	})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.True(t, imageEndpointHit, "ContentTypeImage must dispatch to /res/v1/images/search")
	assert.False(t, webEndpointHit, "ContentTypeImage must NOT hit /res/v1/web/search")

	pub := got.Results[0]
	assert.Equal(t, ContentTypeImage, pub.ContentType)
	assert.Equal(t, "https://example.com/full.jpg", pub.MediaURL)
	assert.Equal(t, "https://example.com/thumb.jpg", pub.ThumbnailURL)
	assert.Equal(t, "image/jpeg", pub.MediaMime, "format=jpg must map to image/jpeg")
	assert.Equal(t, 1920, pub.SourceMetadata[smetaWidth])
	assert.Equal(t, 1080, pub.SourceMetadata[smetaHeight])
	assert.Equal(t, "https://example.com/gallery/page", pub.SourceMetadata[smetaSourcePage])
	// Brave images SERP doesn't carry license info — License stays empty as
	// the explicit "unverified" signal for downstream consumers.
	assert.Empty(t, pub.License, "Brave images SERP must NOT fabricate a license")
}

func TestBrave_Search_NonImageContentTypeStaysOnWebEndpoint(t *testing.T) {
	t.Parallel()
	imageEndpointHit := false
	webEndpointHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case braveImageSearchPath:
			imageEndpointHit = true
		case braveSearchPath:
			webEndpointHit = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildBraveTestResponse(nil, nil))
	}))
	defer srv.Close()

	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x", Limit: 1,
		ContentType: ContentTypeAny,
	})
	require.NoError(t, err)
	assert.False(t, imageEndpointHit, "non-image content type must NOT hit images endpoint")
	assert.True(t, webEndpointHit, "non-image content type must hit web endpoint")
}

func TestBrave_Image_FiltersOutResultsWithoutMediaURL(t *testing.T) {
	t.Parallel()
	results := []braveImageResult{
		{Title: "no-url"},
		{Title: "with-url", Properties: braveImageProperties{URL: "https://example.com/y.jpg", Format: "jpg"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildBraveImageTestResponse(results))
	}))
	defer srv.Close()
	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 10, ContentType: ContentTypeImage})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.Equal(t, "with-url", got.Results[0].Title)
}

func TestBraveImageFormatToMime(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"jpg":  "image/jpeg",
		"JPEG": "image/jpeg",
		"png":  "image/png",
		"webp": "image/webp",
		"svg":  "image/svg+xml",
		"":     "",
		"abc":  "",
	}
	for in, want := range cases {
		assert.Equal(t, want, braveImageFormatToMime(in), "input %q", in)
	}
}
