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
// v5 cycle 2 / v2.9.0 — Zenodo tests.
// ---------------------------------------------------------------------------

func newZenodoTestPlugin(t *testing.T, baseURL string) *ZenodoPlugin {
	t.Helper()
	p := &ZenodoPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildZenodoSearchResponse(hits []zenodoHit, total int) string {
	b, _ := json.Marshal(zenodoSearchResponse{
		Hits: zenodoHitsBlock{Total: total, Hits: hits},
	})
	return string(b)
}

func TestZenodo_Identity(t *testing.T) {
	t.Parallel()
	p := &ZenodoPlugin{}
	assert.Equal(t, SourceZenodo, p.ID())
	assert.Equal(t, FormatJSON, p.NativeFormat())
}

func TestZenodo_Capabilities(t *testing.T) {
	t.Parallel()
	p := &ZenodoPlugin{}
	caps := p.Capabilities()
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsCategoryFilter)
	assert.True(t, caps.SupportsOpenAccessFilter)
	assert.Contains(t, caps.Kinds, KindPaper)
	assert.Contains(t, caps.Kinds, KindDataset)
}

func TestZenodo_Residency(t *testing.T) {
	t.Parallel()
	p := &ZenodoPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
}

func TestZenodo_Search_HappyPath(t *testing.T) {
	t.Parallel()
	hits := []zenodoHit{{
		ID:           123456,
		ConceptRecID: "123455",
		DOI:          "10.5281/zenodo.123456",
		Links:        zenodoLinks{SelfHTML: "https://zenodo.org/record/123456"},
		Metadata: zenodoMetadata{
			Title:           "Climate Model Output 2024",
			PublicationDate: "2024-06-15",
			Description:     "<p>Daily simulations from CMIP7.</p>",
			Creators: []zenodoCreator{
				{Name: "Doe, Jane", Affiliation: "ETH Zurich", ORCID: "0000-0001-2345-6789"},
				{Name: "Smith, Bob"},
			},
			ResourceType: zenodoResource{Type: "dataset"},
			License:      zenodoLicenseRef{ID: "cc-by-4.0"},
			Keywords:     []string{"climate", "cmip7"},
			Language:     "eng",
			AccessRight:  "open",
		},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, zenodoSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "climate model", q.Get(zenodoParamQ))
		assert.Equal(t, "10", q.Get(zenodoParamSize))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildZenodoSearchResponse(hits, 1))
	}))
	defer srv.Close()

	p := newZenodoTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "climate model", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "zenodo:123456", pub.ID)
	assert.Equal(t, ContentTypeDataset, pub.ContentType)
	assert.Equal(t, "Climate Model Output 2024", pub.Title)
	assert.Equal(t, "10.5281/zenodo.123456", pub.DOI)
	assert.Equal(t, "2024-06-15", pub.Published)
	assert.Equal(t, "https://zenodo.org/record/123456", pub.URL)
	assert.Contains(t, pub.Abstract, "Daily simulations")
	assert.Equal(t, "cc-by-4.0", pub.License)
	assert.Equal(t, "eng", pub.Language)
	require.Len(t, pub.Authors, 2)
	assert.Equal(t, "Doe, Jane", pub.Authors[0].Name)
	assert.Equal(t, "0000-0001-2345-6789", pub.Authors[0].ORCID)
	assert.Equal(t, "dataset", pub.SourceMetadata[zenodoMetaKeyResourceType])
	assert.Equal(t, "123455", pub.SourceMetadata[zenodoMetaKeyConceptRecID])
}

func TestZenodo_Search_DateRangeAndCategory(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Contains(t, q.Get(zenodoParamQ), "publication_date:[2024-01-01 TO 2024-12-31]")
		assert.Equal(t, "publication", q.Get(zenodoParamType))
		assert.Equal(t, zenodoAccessOpen, q.Get(zenodoParamAccess))
		assert.Equal(t, zenodoSortMostRecent, q.Get(zenodoParamSort))
		_, _ = io.WriteString(w, buildZenodoSearchResponse(nil, 0))
	}))
	defer srv.Close()

	p := newZenodoTestPlugin(t, srv.URL)
	openAccess := true
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Sort:  SortDateDesc,
		Filters: SearchFilters{
			DateFrom:   "2024",
			DateTo:     "2024",
			Categories: []string{"publication"},
			OpenAccess: &openAccess,
		},
	})
	require.NoError(t, err)
}

func TestZenodo_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newZenodoTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestZenodo_Get_HappyPath(t *testing.T) {
	t.Parallel()
	hit := zenodoHit{
		ID:    42,
		DOI:   "10.5281/zenodo.42",
		Links: zenodoLinks{SelfHTML: "https://zenodo.org/record/42"},
		Metadata: zenodoMetadata{
			Title:           "Test record",
			PublicationDate: "2024-01-15",
			ResourceType:    zenodoResource{Type: "publication"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, zenodoSearchPath+"/42", r.URL.Path)
		b, _ := json.Marshal(hit)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newZenodoTestPlugin(t, srv.URL)
	pub, err := p.Get(context.Background(), "42", nil, FormatNative)
	require.NoError(t, err)
	assert.Equal(t, "zenodo:42", pub.ID)
	assert.Equal(t, "10.5281/zenodo.42", pub.DOI)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
}

func TestZenodo_NormalizeDate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "2024-01-01", zenodoNormalizeDate("2024", true))
	assert.Equal(t, "2024-12-31", zenodoNormalizeDate("2024", false))
	assert.Equal(t, "2024-06-15", zenodoNormalizeDate("2024-06-15", true))
	assert.Equal(t, "", zenodoNormalizeDate("", true))
}

func TestZenodo_BuildQuery_NoDates(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "climate", zenodoBuildQuery(SearchParams{Query: "climate"}))
}

func TestZenodo_BuildQuery_DateOnly(t *testing.T) {
	t.Parallel()
	got := zenodoBuildQuery(SearchParams{
		Filters: SearchFilters{DateFrom: "2020", DateTo: "2024"},
	})
	assert.Equal(t, "publication_date:[2020-01-01 TO 2024-12-31]", got)
}
