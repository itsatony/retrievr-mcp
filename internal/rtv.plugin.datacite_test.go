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
// v5 cycle 3 / v2.10.0 — DataCite tests.
// ---------------------------------------------------------------------------

func newDataCiteTestPlugin(t *testing.T, baseURL string) *DataCitePlugin {
	t.Helper()
	p := &DataCitePlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildDataCiteSearchResponse(records []dataciteRecord, total int) string {
	b, _ := json.Marshal(dataciteSearchResponse{
		Data: records,
		Meta: dataciteMeta{Total: total},
	})
	return string(b)
}

func TestDataCite_Identity(t *testing.T) {
	t.Parallel()
	p := &DataCitePlugin{}
	assert.Equal(t, SourceDataCite, p.ID())
}

func TestDataCite_Capabilities(t *testing.T) {
	t.Parallel()
	p := &DataCitePlugin{}
	caps := p.Capabilities()
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsCategoryFilter)
	assert.Contains(t, caps.Kinds, KindDataset)
}

func TestDataCite_Residency(t *testing.T) {
	t.Parallel()
	p := &DataCitePlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
}

func TestDataCite_Search_HappyPath(t *testing.T) {
	t.Parallel()
	records := []dataciteRecord{{
		ID:   "10.5061/dryad.abc123",
		Type: "dois",
		Attributes: dataciteAttributes{
			DOI:    "10.5061/dryad.abc123",
			Titles: []dataciteTitle{{Title: "Human Genome Reference"}},
			Creators: []dataciteCreator{{
				Name:        "Doe, Jane",
				Affiliation: []dataciteAffiliation{{Name: "Broad Institute"}},
				NameIdentifiers: []dataciteNameIdentifier{
					{NameIdentifier: "0000-0001-2345-6789", NameIdentifierScheme: "ORCID"},
				},
			}},
			Publisher:       "Dryad",
			PublicationYear: 2023,
			Types:           dataciteTypes{ResourceType: "Genome", ResourceTypeGeneral: "Dataset"},
			Descriptions: []dataciteDescription{
				{Description: "<p>Annotated human genome data.</p>", DescriptionType: "Abstract"},
			},
			Registered: "2023-04-10T00:00:00Z",
			URL:        "https://datadryad.org/abc",
		},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, dataciteSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "human genome", q.Get(dataciteParamQuery))
		assert.Equal(t, "10", q.Get(dataciteParamPageSize))
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, _ = io.WriteString(w, buildDataCiteSearchResponse(records, 1))
	}))
	defer srv.Close()

	p := newDataCiteTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "human genome", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "datacite:10.5061/dryad.abc123", pub.ID)
	assert.Equal(t, ContentTypeDataset, pub.ContentType)
	assert.Equal(t, "10.5061/dryad.abc123", pub.DOI)
	assert.Equal(t, "Human Genome Reference", pub.Title)
	assert.Equal(t, "2023", pub.Published)
	assert.Equal(t, "https://datadryad.org/abc", pub.URL)
	assert.Contains(t, pub.Abstract, "Annotated human genome")
	require.Len(t, pub.Authors, 1)
	assert.Equal(t, "Broad Institute", pub.Authors[0].Affiliation)
	assert.Equal(t, "0000-0001-2345-6789", pub.Authors[0].ORCID)
	assert.Equal(t, "Dataset", pub.SourceMetadata[dataciteMetaKeyResourceTypeGeneral])
}

func TestDataCite_Search_DateRangeAndCategory(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "dataset", q.Get(dataciteParamResourceTID))
		assert.Equal(t, "2020-01-01,2024-12-31", q.Get(dataciteParamRegistered))
		assert.Equal(t, dataciteSortCreatedDesc, q.Get(dataciteParamSort))
		_, _ = io.WriteString(w, buildDataCiteSearchResponse(nil, 0))
	}))
	defer srv.Close()

	p := newDataCiteTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Sort:  SortDateDesc,
		Filters: SearchFilters{
			DateFrom:   "2020",
			DateTo:     "2024",
			Categories: []string{"Dataset"},
		},
	})
	require.NoError(t, err)
}

func TestDataCite_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newDataCiteTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestDataCite_Get_HappyPath(t *testing.T) {
	t.Parallel()
	record := dataciteRecord{
		ID:   "10.x/y",
		Type: "dois",
		Attributes: dataciteAttributes{
			DOI:             "10.x/y",
			Titles:          []dataciteTitle{{Title: "Single record"}},
			PublicationYear: 2022,
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/dois/")
		b, _ := json.Marshal(struct {
			Data dataciteRecord `json:"data"`
		}{Data: record})
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newDataCiteTestPlugin(t, srv.URL)
	pub, err := p.Get(context.Background(), "10.x/y", nil, FormatNative)
	require.NoError(t, err)
	assert.Equal(t, "datacite:10.x/y", pub.ID)
	assert.Equal(t, "Single record", pub.Title)
}
