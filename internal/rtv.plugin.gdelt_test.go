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
// v5 cycle 6 / v2.13.0 — GDELT tests.
// ---------------------------------------------------------------------------

func newGDELTTestPlugin(t *testing.T, baseURL string) *GDELTPlugin {
	t.Helper()
	p := &GDELTPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestGDELT_Identity(t *testing.T) {
	t.Parallel()
	p := &GDELTPlugin{}
	assert.Equal(t, SourceGDELT, p.ID())
}

func TestGDELT_Capabilities(t *testing.T) {
	t.Parallel()
	p := &GDELTPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindNews)
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsLanguageFilter)
	assert.True(t, caps.SupportsCategoryFilter)
	assert.True(t, caps.SupportsDomainFilter)
}

func TestGDELT_Residency(t *testing.T) {
	t.Parallel()
	p := &GDELTPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestGDELT_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := gdeltSearchResponse{
		Articles: []gdeltArticle{{
			URL:           "https://example.com/article",
			Title:         "Climate Summit Concludes",
			SeenDate:      "20240615T120000Z",
			SocialImage:   "https://example.com/img.jpg",
			Domain:        "example.com",
			Language:      "English",
			SourceCountry: "United States",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, gdeltSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "climate summit", q.Get(gdeltParamQuery))
		assert.Equal(t, gdeltModeArtList, q.Get(gdeltParamMode))
		assert.Equal(t, gdeltFormatJSON, q.Get(gdeltParamFormat))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newGDELTTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "climate summit", Limit: 25})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "gdelt:https://example.com/article", pub.ID)
	assert.Equal(t, "Climate Summit Concludes", pub.Title)
	assert.Equal(t, "2024-06-15", pub.Published)
	assert.Equal(t, "English", pub.Language)
	assert.Equal(t, "example.com", pub.SourceMetadata[gdeltMetaKeyDomain])
	assert.Equal(t, "https://example.com/img.jpg", pub.ThumbnailURL)
}

func TestGDELT_Search_BuildQuery_LangCategoryDomain(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get(gdeltParamQuery)
		assert.Contains(t, got, "climate")
		assert.Contains(t, got, "sourcelang:en")
		assert.Contains(t, got, "theme:CLIMATE")
		assert.Contains(t, got, "domain:bbc.co.uk")
		assert.Equal(t, gdeltSortDateDesc, r.URL.Query().Get(gdeltParamSort))
		assert.Equal(t, "20240101000000", r.URL.Query().Get(gdeltParamStartDateTm))
		assert.Equal(t, "20241231235959", r.URL.Query().Get(gdeltParamEndDateTm))
		_, _ = io.WriteString(w, `{"articles":[]}`)
	}))
	defer srv.Close()
	p := newGDELTTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query: "climate",
		Sort:  SortDateDesc,
		Filters: SearchFilters{
			Language:       "en",
			Categories:     []string{"CLIMATE"},
			IncludeDomains: []string{"bbc.co.uk"},
			DateFrom:       "2024",
			DateTo:         "2024",
		},
	})
	require.NoError(t, err)
}

func TestGDELT_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newGDELTTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

// v2.22.1 — GDELT's free-tier limiter returns an HTML "exceeded" page
// with HTTP 200 OK instead of a 429. The JSON decoder MUST NOT see it;
// the plugin maps it to ErrRateLimitExceeded so the standard
// retry/backoff path applies uniformly with a real 429.
func TestGDELT_Search_HTML200MapsToRateLimit(t *testing.T) {
	t.Parallel()
	const throttleHTML = `<html><head><title>GDELT Service</title></head><body>` +
		`<h1>You have exceeded the rate limit. Please try again later.</h1></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, throttleHTML)
	}))
	defer srv.Close()
	p := newGDELTTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded),
		"HTML body with 200 OK must map to ErrRateLimitExceeded (got %v)", err)
}

// v2.22.1 — empty body on 200 OK is GDELT's no-results shape; treat as
// an empty result envelope, not a decode failure.
func TestGDELT_Search_EmptyBodyTreatedAsNoResults(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// No body written → empty.
	}))
	defer srv.Close()
	p := newGDELTTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.NoError(t, err)
	assert.Empty(t, res.Results)
	assert.Equal(t, 0, res.Total)
}

// v2.22.1 — whitespace-only body is the same shape as the empty-body
// case; harden against subtle upstream variations.
func TestGDELT_Search_WhitespaceBodyTreatedAsNoResults(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "\n\n  \t\n")
	}))
	defer srv.Close()
	p := newGDELTTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.NoError(t, err)
	assert.Empty(t, res.Results)
}

// v2.22.1 — actual malformed JSON should surface as a decode error with
// the body excerpt embedded for diagnostics (and must not be silently
// reclassified as a rate-limit).
func TestGDELT_Search_MalformedJSONReportsDecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"articles":[`)
	}))
	defer srv.Close()
	p := newGDELTTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrRateLimitExceeded),
		"malformed JSON must not be misclassified as a rate-limit")
	assert.Contains(t, err.Error(), "decode response")
}

// v2.22.1 — default rate-limit floor lowered to 0.2 RPS (1 req / 5 s)
// to align with the observed free-tier ceiling. Operators with paid
// arrangements override via PluginConfig.RateLimit.
func TestGDELT_DefaultRateLimitMatchesFreeTierCeiling(t *testing.T) {
	t.Parallel()
	p := &GDELTPlugin{}
	require.NoError(t, p.Initialize(context.Background(),
		PluginConfig{Enabled: true, BaseURL: "http://unused"}))
	h := p.Health(context.Background())
	assert.InDelta(t, 0.2, h.RateLimit, 1e-9,
		"GDELT free-tier default should be 0.2 RPS, not the legacy 1.0")
}

func TestGDELT_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &GDELTPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestGDELT_DatetimeBound(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "20240101000000", gdeltDatetimeBound("2024", true))
	assert.Equal(t, "20241231235959", gdeltDatetimeBound("2024", false))
	assert.Equal(t, "20240615000000", gdeltDatetimeBound("2024-06-15", true))
	assert.Equal(t, "20240615235959", gdeltDatetimeBound("2024-06-15", false))
}

// v2.22.0 — PublishedAfter / PublishedBefore push-through: GDELT
// STARTDATETIME/ENDDATETIME accept 14-digit sub-day precision, so the
// precise RFC3339 timestamp lands in the URL verbatim (in YYYYMMDDHHMMSS
// form), not a day-floor expansion.
func TestGDELT_Search_PublishedAfterPushDown(t *testing.T) {
	t.Parallel()
	resp := gdeltSearchResponse{Articles: []gdeltArticle{}}
	body, _ := json.Marshal(resp)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "20260523080000", q.Get(gdeltParamStartDateTm))
		assert.Equal(t, "20260523180000", q.Get(gdeltParamEndDateTm))
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	p := newGDELTTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Filters: SearchFilters{
			PublishedAfter:  "2026-05-23T08:00:00Z",
			PublishedBefore: "2026-05-23T18:00:00Z",
		},
	})
	require.NoError(t, err)
}

func TestGDELT_Capabilities_PublishedAfterIsNative(t *testing.T) {
	t.Parallel()
	p := &GDELTPlugin{}
	assert.Equal(t, PublishedAfterNative, p.Capabilities().SupportsPublishedAfterFilter)
}
