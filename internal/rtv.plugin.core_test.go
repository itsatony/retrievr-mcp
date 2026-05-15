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
// v5 cycle 2 / v2.9.0 — CORE tests.
// ---------------------------------------------------------------------------

func newCORETestPlugin(t *testing.T, baseURL, apiKey string) *COREPlugin {
	t.Helper()
	p := &COREPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildCORESearchResponse(results []coreResult, total int) string {
	b, _ := json.Marshal(coreSearchResponse{
		TotalHits: total,
		Results:   results,
	})
	return string(b)
}

func TestCORE_Identity(t *testing.T) {
	t.Parallel()
	p := &COREPlugin{}
	assert.Equal(t, SourceCORE, p.ID())
}

func TestCORE_Capabilities(t *testing.T) {
	t.Parallel()
	p := &COREPlugin{}
	caps := p.Capabilities()
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsOpenAccessFilter)
	assert.Contains(t, caps.Kinds, KindPaper)
}

func TestCORE_Residency(t *testing.T) {
	t.Parallel()
	p := &COREPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUKAdequacy, tag.Region)
}

func TestCORE_Search_HappyPath(t *testing.T) {
	t.Parallel()
	results := []coreResult{{
		ID:            7777,
		DOI:           "10.1234/core.7777",
		Title:         "Attention is All You Need",
		Abstract:      "<p>Transformer architecture.</p>",
		Authors:       []coreAuthor{{Name: "Vaswani, Ashish"}, {Name: "Shazeer, Noam"}},
		PublishedDate: "2017-06-12T00:00:00",
		YearPublished: 2017,
		Language:      coreLang{Code: "en"},
		DownloadURL:   "https://core.ac.uk/download/pdf/7777.pdf",
		DocumentType:  "research",
		Publisher:     "arXiv",
		Links:         []coreLink{{Type: "display", URL: "https://core.ac.uk/works/7777"}},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, coreSearchPath, r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		var body coreSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "attention mechanism", body.Q)
		assert.Equal(t, 25, body.Limit)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildCORESearchResponse(results, 1))
	}))
	defer srv.Close()

	p := newCORETestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "attention mechanism", Limit: 25})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "core:7777", pub.ID)
	assert.Equal(t, "10.1234/core.7777", pub.DOI)
	assert.Equal(t, "Attention is All You Need", pub.Title)
	assert.Equal(t, "2017-06-12", pub.Published)
	assert.Equal(t, "en", pub.Language)
	assert.Equal(t, "https://core.ac.uk/download/pdf/7777.pdf", pub.PDFURL)
	assert.Equal(t, "https://core.ac.uk/works/7777", pub.URL)
	assert.Contains(t, pub.Abstract, "Transformer")
	require.Len(t, pub.Authors, 2)
	assert.Equal(t, "research", pub.SourceMetadata[coreMetaKeyDocumentType])
}

func TestCORE_Search_DateSort(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body coreSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Len(t, body.Sort, 1)
		assert.Equal(t, coreSortFieldPublished, body.Sort[0].Field)
		assert.Equal(t, coreSortOrderDesc, body.Sort[0].Order)
		assert.Contains(t, body.Q, "yearPublished:[2020 TO 2024]")
		_, _ = io.WriteString(w, buildCORESearchResponse(nil, 0))
	}))
	defer srv.Close()

	p := newCORETestPlugin(t, srv.URL, "")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Sort:  SortDateDesc,
		Filters: SearchFilters{
			DateFrom: "2020",
			DateTo:   "2024-12-31",
		},
	})
	require.NoError(t, err)
}

func TestCORE_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newCORETestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestCORE_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newCORETestPlugin(t, srv.URL, "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestCORE_YearFromDate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 2024, coreYearFromDate("2024"))
	assert.Equal(t, 2024, coreYearFromDate("2024-06-15"))
	assert.Equal(t, 0, coreYearFromDate(""))
	assert.Equal(t, 0, coreYearFromDate("abc"))
}
