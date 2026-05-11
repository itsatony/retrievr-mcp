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
// v3 cycle 4 / v2.5.0: Europeana image-search tests.
// ---------------------------------------------------------------------------

const (
	europeanaTestServerKey  = "europeana-server-key"
	europeanaTestPerCallKey = "europeana-per-call-key"
)

func newEuropeanaTestPlugin(t *testing.T, baseURL, apiKey string) *EuropeanaPlugin {
	t.Helper()
	p := &EuropeanaPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		RateLimit: 100,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildEuropeanaTestResponse(items []europeanaItem, total int) string {
	body := europeanaSearchResponse{Success: true, TotalResults: total, ItemsCount: len(items), Items: items}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestEuropeana_Identity(t *testing.T) {
	t.Parallel()
	p := &EuropeanaPlugin{}
	assert.Equal(t, "europeana", p.ID())
}

func TestEuropeana_Capabilities(t *testing.T) {
	t.Parallel()
	p := &EuropeanaPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindImage)
}

func TestEuropeana_Residency_IsEU_DPASigned(t *testing.T) {
	t.Parallel()
	p := &EuropeanaPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
	assert.True(t, tag.Region.IsEU(), "Europeana must be admissible under eu_strict")
	assert.Equal(t, DPASigned, tag.DPAStatus)
}

func TestEuropeana_Search_HappyPath(t *testing.T) {
	t.Parallel()
	items := []europeanaItem{
		{
			ID:            "/91634/AAEC1A2D_VERMEER_LACE",
			Type:          "IMAGE",
			Title:         []string{"The Lacemaker"},
			Country:       []string{"France"},
			DataProvider:  []string{"Musée du Louvre"},
			EDMIsShownBy:  []string{"https://example.org/vermeer-lacemaker.jpg"},
			EDMPreview:    []string{"https://example.org/vermeer-lacemaker_thumb.jpg"},
			GUID:          "https://www.europeana.eu/en/item/91634/AAEC1A2D_VERMEER_LACE",
			Rights:        []string{"http://creativecommons.org/publicdomain/mark/1.0/"},
			DCCreator:     []string{"Vermeer, Johannes"},
			DCDescription: []string{"Oil on canvas, ~24 × 21 cm"},
			Year:          []string{"1669"},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, europeanaSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, europeanaTestServerKey, q.Get("wskey"))
		assert.Equal(t, "vermeer", q.Get("query"))
		assert.Equal(t, "5", q.Get("rows"))
		assert.Equal(t, europeanaTypeImageFilter, q.Get("qf"))
		assert.Equal(t, "true", q.Get("media"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildEuropeanaTestResponse(items, 1))
	}))
	defer srv.Close()

	p := newEuropeanaTestPlugin(t, srv.URL, europeanaTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "vermeer", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)

	pub := got.Results[0]
	assert.Equal(t, ContentTypeImage, pub.ContentType)
	assert.Equal(t, "europeana:91634/AAEC1A2D_VERMEER_LACE", pub.ID)
	assert.Equal(t, "The Lacemaker", pub.Title)
	assert.Equal(t, "https://example.org/vermeer-lacemaker.jpg", pub.MediaURL)
	assert.Equal(t, "image/jpeg", pub.MediaMime)
	// License URL exposed in Publication.License (per Europeana convention).
	assert.Contains(t, pub.License, "publicdomain")
	assert.Equal(t, "1669", pub.Published)
	require.NotEmpty(t, pub.Authors)
	assert.Equal(t, "Vermeer, Johannes", pub.Authors[0].Name)
	assert.Equal(t, "Musée du Louvre", pub.SourceMetadata["data_provider"])
	assert.Equal(t, "France", pub.SourceMetadata[smetaCountry])
}

func TestEuropeana_Search_PerCallCredentialWins(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, europeanaTestPerCallKey, r.URL.Query().Get("wskey"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildEuropeanaTestResponse(nil, 0))
	}))
	defer srv.Close()
	p := newEuropeanaTestPlugin(t, srv.URL, europeanaTestServerKey)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceEuropeana: europeanaTestPerCallKey,
	})
	_, err := p.Search(ctx, SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
}

func TestEuropeana_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newEuropeanaTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestEuropeana_Search_401MapsToCredentialInvalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newEuropeanaTestPlugin(t, srv.URL, europeanaTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestEuropeana_Search_FiltersOutItemsWithoutMediaURL(t *testing.T) {
	t.Parallel()
	items := []europeanaItem{
		{ID: "/x/empty", Title: []string{"empty"}}, // no EDMIsShownBy
		{ID: "/x/valid", Title: []string{"valid"}, EDMIsShownBy: []string{"https://example.org/v.jpg"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildEuropeanaTestResponse(items, 2))
	}))
	defer srv.Close()
	p := newEuropeanaTestPlugin(t, srv.URL, europeanaTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.Equal(t, "valid", got.Results[0].Title)
}

func TestEuropeana_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &EuropeanaPlugin{}
	_, err := p.Get(context.Background(), "/x/y", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestSanitizeEuropeanaID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "91634/AAEC1A2D_VERMEER_LACE", sanitizeEuropeanaID("/91634/AAEC1A2D_VERMEER_LACE"))
	assert.Equal(t, "already-clean", sanitizeEuropeanaID("already-clean"))
}

func TestInferMimeFromURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://x/a.jpg":        "image/jpeg",
		"https://x/A.JPEG":       "image/jpeg",
		"https://x/a.png":        "image/png",
		"https://x/a.svg":        "image/svg+xml",
		"https://x/a.tiff":       "image/tiff",
		"https://x/path/to.webp": "image/webp",
		"https://x/no-extension": "",
		"":                       "",
	}
	for in, want := range cases {
		assert.Equal(t, want, inferMimeFromURL(in), "input %q", in)
	}
}

func TestFirstSliceValue(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "a", firstSliceValue([]string{"a", "b"}))
	assert.Equal(t, "b", firstSliceValue([]string{"", "  ", "b"}))
	assert.Equal(t, "", firstSliceValue(nil))
	assert.Equal(t, "", firstSliceValue([]string{strings.Repeat(" ", 4)}))
}
