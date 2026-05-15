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
// v5 cycle 2 / v2.9.0 — OpenAIRE tests.
// ---------------------------------------------------------------------------

func newOpenAIRETestPlugin(t *testing.T, baseURL, apiKey string) *OpenAIREPlugin {
	t.Helper()
	p := &OpenAIREPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildOpenAIRESearchResponse(results []openaireResult, total int) string {
	b, _ := json.Marshal(openaireSearchResponse{
		Header:  openaireHeader{NumFound: total},
		Results: results,
	})
	return string(b)
}

func TestOpenAIRE_Identity(t *testing.T) {
	t.Parallel()
	p := &OpenAIREPlugin{}
	assert.Equal(t, SourceOpenAIRE, p.ID())
}

func TestOpenAIRE_Capabilities(t *testing.T) {
	t.Parallel()
	p := &OpenAIREPlugin{}
	caps := p.Capabilities()
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsPagination)
	assert.Contains(t, caps.Kinds, KindPaper)
}

func TestOpenAIRE_Residency(t *testing.T) {
	t.Parallel()
	p := &OpenAIREPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
}

func TestOpenAIRE_Search_HappyPath(t *testing.T) {
	t.Parallel()
	results := []openaireResult{{
		ID:        "openaire____::abc123",
		MainTitle: "Horizon Europe AI Project Output",
		Descriptions: []string{
			"<p>Outputs from a Horizon Europe-funded AI project.</p>",
		},
		Authors: []openaireAuthor{{
			FullName: "Müller, Anna",
			PID: &openaireAuthorPID{
				ID: openairePID{Scheme: "orcid", Value: "0000-0001-1111-2222"},
			},
		}},
		PIDs: []openairePID{
			{Scheme: "doi", Value: "10.5072/horizon.123"},
		},
		PublicationDate: "2024-09-01",
		Language:        openaireLabeled{Code: "eng", Label: "English"},
		BestAccessRight: openaireLabeled{Code: "c_abf2", Label: "Open Access"},
		Type:            "publication",
		Publisher:       "EU Publications Office",
		Subjects:        []openaireSubject{{Value: "artificial intelligence"}, {Value: "horizon europe"}},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, openaire_SearchPath, r.URL.Path)
		assert.Equal(t, "Bearer pub-token", r.Header.Get("Authorization"))
		q := r.URL.Query()
		assert.Equal(t, "horizon europe AI", q.Get(openaire_ParamSearch))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildOpenAIRESearchResponse(results, 1))
	}))
	defer srv.Close()

	p := newOpenAIRETestPlugin(t, srv.URL, "pub-token")
	res, err := p.Search(context.Background(), SearchParams{Query: "horizon europe AI", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "openaire:openaire____::abc123", pub.ID)
	assert.Equal(t, "10.5072/horizon.123", pub.DOI)
	assert.Equal(t, "https://doi.org/10.5072/horizon.123", pub.URL)
	assert.Equal(t, "Horizon Europe AI Project Output", pub.Title)
	assert.Contains(t, pub.Abstract, "Horizon Europe-funded AI")
	assert.Equal(t, "2024-09-01", pub.Published)
	assert.Equal(t, "eng", pub.Language)
	require.Len(t, pub.Authors, 1)
	assert.Equal(t, "Müller, Anna", pub.Authors[0].Name)
	assert.Equal(t, "0000-0001-1111-2222", pub.Authors[0].ORCID)
	assert.Equal(t, "Open Access", pub.SourceMetadata[openaire_MetaKeyAccessLabel])
	assert.Equal(t, "publication", pub.SourceMetadata[openaire_MetaKeyType])
}

func TestOpenAIRE_Search_DateFiltersAndSort(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "2024-01-01", q.Get(openaire_ParamFromDate))
		assert.Equal(t, "2024-12-31", q.Get(openaire_ParamToDate))
		assert.Equal(t, openaire_SortPublishedDesc, q.Get(openaire_ParamSortBy))
		_, _ = io.WriteString(w, buildOpenAIRESearchResponse(nil, 0))
	}))
	defer srv.Close()

	p := newOpenAIRETestPlugin(t, srv.URL, "")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Sort:  SortDateDesc,
		Filters: SearchFilters{
			DateFrom: "2024",
			DateTo:   "2024",
		},
	})
	require.NoError(t, err)
}

func TestOpenAIRE_Search_DatasetType(t *testing.T) {
	t.Parallel()
	results := []openaireResult{{
		ID:        "openaire____::dataset1",
		MainTitle: "Genome dataset",
		Type:      "dataset",
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, buildOpenAIRESearchResponse(results, 1))
	}))
	defer srv.Close()

	p := newOpenAIRETestPlugin(t, srv.URL, "")
	res, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	assert.Equal(t, ContentTypeDataset, res.Results[0].ContentType)
}

func TestOpenAIRE_Search_AuthorFullNameFallback(t *testing.T) {
	t.Parallel()
	results := []openaireResult{{
		ID:        "x",
		MainTitle: "t",
		Authors: []openaireAuthor{
			{Surname: "Doe", Name: "Jane"},
		},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, buildOpenAIRESearchResponse(results, 1))
	}))
	defer srv.Close()
	p := newOpenAIRETestPlugin(t, srv.URL, "")
	res, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.NoError(t, err)
	require.Len(t, res.Results[0].Authors, 1)
	assert.Equal(t, "Doe, Jane", res.Results[0].Authors[0].Name)
}

func TestOpenAIRE_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newOpenAIRETestPlugin(t, srv.URL, "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestOpenAIRE_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &OpenAIREPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestOpenAIRE_NormalizeDate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "2024-01-01", openaireNormalizeDate("2024", true))
	assert.Equal(t, "2024-12-31", openaireNormalizeDate("2024", false))
	assert.Equal(t, "2024-06-15", openaireNormalizeDate("2024-06-15", true))
}
