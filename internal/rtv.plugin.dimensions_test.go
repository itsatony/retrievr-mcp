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
// v6 cycle 3 / v2.16.0 — Dimensions.ai tests.
// ---------------------------------------------------------------------------

func newDimensionsTestPlugin(t *testing.T, baseURL, apiKey string) *DimensionsPlugin {
	t.Helper()
	p := &DimensionsPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

// dimensionsTestServer handles both /api/auth and /api/dsl. token=
// "test-token" returned on auth and verified on dsl.
func dimensionsTestServer(t *testing.T, dsl func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, dimensionsAuthPath):
			var body dimensionsAuthRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.NotEmpty(t, body.Key)
			b, _ := json.Marshal(dimensionsAuthResponse{Token: "test-token"})
			_, _ = w.Write(b)
		case strings.HasSuffix(r.URL.Path, dimensionsDSLPath):
			assert.Equal(t, "test-token", r.Header.Get(dimensionsHeaderAuth))
			dsl(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestDimensions_Identity(t *testing.T) {
	t.Parallel()
	p := &DimensionsPlugin{}
	assert.Equal(t, SourceDimensions, p.ID())
}

func TestDimensions_Capabilities(t *testing.T) {
	t.Parallel()
	p := &DimensionsPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPaper)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsCitations)
	assert.True(t, caps.SupportsSortCitations)
}

func TestDimensions_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newDimensionsTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestDimensions_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := dimensionsSearchResponse{
		Stats: dimensionsStats{TotalCount: 1},
		Publications: []dimensionsPublication{{
			ID:    "pub.1234567",
			Title: "Attention is All You Need",
			DOI:   "10.5555/3295222.3295349",
			Authors: []dimensionsAuthor{
				{FirstName: "Ashish", LastName: "Vaswani", ORCID: "0000-0001-2345-6789"},
				{FirstName: "Noam", LastName: "Shazeer"},
			},
			Year:       2017,
			Abstract:   "The dominant sequence transduction models...",
			TimesCited: 50000,
			Journal:    &dimensionsJournal{Title: "NeurIPS"},
			Publisher:  "Curran Associates",
		}},
	}

	srv := dimensionsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(body), `search publications for "attention"`)
		assert.Contains(t, string(body), "limit 25")
		assert.Contains(t, string(body), "sort by times_cited desc")
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})
	defer srv.Close()

	p := newDimensionsTestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{
		Query: "attention",
		Limit: 25,
		Sort:  SortCitations,
	})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "dimensions:pub.1234567", pub.ID)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, "Attention is All You Need", pub.Title)
	assert.Equal(t, "10.5555/3295222.3295349", pub.DOI)
	assert.Equal(t, "2017", pub.Published)
	require.NotNil(t, pub.CitationCount)
	assert.Equal(t, 50000, *pub.CitationCount)
	require.Len(t, pub.Authors, 2)
	assert.Equal(t, "Vaswani, Ashish", pub.Authors[0].Name)
	assert.Equal(t, "0000-0001-2345-6789", pub.Authors[0].ORCID)
	assert.Equal(t, "NeurIPS", pub.SourceMetadata[dimensionsMetaKeyJournal])
}

func TestDimensions_Search_DateAndCategoryFilters(t *testing.T) {
	t.Parallel()
	srv := dimensionsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		assert.Contains(t, s, "year>=2020")
		assert.Contains(t, s, "year<=2024")
		assert.Contains(t, s, `category_for.name in ["Computer Science"]`)
		_, _ = io.WriteString(w, `{"_stats":{"total_count":0},"publications":[]}`)
	})
	defer srv.Close()

	p := newDimensionsTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Filters: SearchFilters{
			DateFrom:   "2020",
			DateTo:     "2024",
			Categories: []string{"Computer Science"},
		},
	})
	require.NoError(t, err)
}

func TestDimensions_Search_BadAuthFailsAuth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newDimensionsTestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestDimensions_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := dimensionsTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer srv.Close()
	p := newDimensionsTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestDimensions_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &DimensionsPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestDimensions_BuildDSL_EscapesQuotes(t *testing.T) {
	t.Parallel()
	got := dimensionsBuildDSL(SearchParams{Query: `"weird"`}, 10)
	assert.Contains(t, got, `search publications for "\"weird\""`)
	assert.Contains(t, got, "limit 10")
}
