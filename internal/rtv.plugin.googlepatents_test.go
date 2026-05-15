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
// v5 cycle 5 / v2.12.0 — Google Patents tests.
// ---------------------------------------------------------------------------

func newGooglePatentsTestPlugin(t *testing.T, baseURL string) *GooglePatentsPlugin {
	t.Helper()
	p := &GooglePatentsPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestGooglePatents_Identity(t *testing.T) {
	t.Parallel()
	p := &GooglePatentsPlugin{}
	assert.Equal(t, SourceGooglePatents, p.ID())
}

func TestGooglePatents_Capabilities(t *testing.T) {
	t.Parallel()
	p := &GooglePatentsPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPatent)
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsCategoryFilter)
}

func TestGooglePatents_Residency(t *testing.T) {
	t.Parallel()
	p := &GooglePatentsPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestGooglePatents_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := googlePatentsResponse{
		TotalNumResults: 1,
		Results: googlePatentsResults{
			Cluster: []googlePatentsCluster{{
				Result: []googlePatentsHit{{
					Patent: googlePatentsRecord{
						PublicationNumber: "US10001234B2",
						Title:             "Method and apparatus for neural network inference",
						Snippet:           "An apparatus comprising a neural network ...",
						FilingDate:        "20200115",
						PublicationDate:   "20210601",
						Inventor:          "Doe, Jane; Smith, Bob",
						Assignee:          "ACME Corp",
						CPC:               []string{"G06N3/08"},
						CountryCode:       "US",
						KindCode:          "B2",
					},
				}},
			}},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, googlePatentsSearchPath, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		// Emit with the anti-hijack sentinel to exercise stripping.
		_, _ = io.WriteString(w, googlePatentsAntiHijackPrefix+"\n")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newGooglePatentsTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "neural network", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "googlepatents:US10001234B2", pub.ID)
	assert.Equal(t, ContentTypePatent, pub.ContentType)
	assert.Equal(t, "Method and apparatus for neural network inference", pub.Title)
	assert.Equal(t, "2021-06-01", pub.Published)
	assert.Equal(t, "US10001234B2", pub.SourceMetadata[MetaKeyPatentNumber])
	assert.Equal(t, "US", pub.SourceMetadata[smetaPatentJurisdiction])
	assert.Equal(t, "ACME Corp", pub.SourceMetadata[smetaPatentAssignee])
	require.Len(t, pub.Authors, 2)
	assert.Equal(t, "Doe, Jane", pub.Authors[0].Name)
}

func TestGooglePatents_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newGooglePatentsTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestGooglePatents_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &GooglePatentsPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestGooglePatents_BuildInnerQuery(t *testing.T) {
	t.Parallel()
	got := googlePatentsBuildInnerQuery(SearchParams{
		Query:   "neural",
		Filters: SearchFilters{Categories: []string{"G06N3/08"}, DateFrom: "2020", DateTo: "2024-06-15"},
	})
	assert.Contains(t, got, "neural")
	assert.Contains(t, got, "(CPC=G06N3/08)")
	assert.Contains(t, got, "after:filing:20200101")
	assert.Contains(t, got, "before:filing:20240615")
}
