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
// v5 cycle 6 / v2.13.0 — IA Scholar tests.
// ---------------------------------------------------------------------------

func newIAScholarTestPlugin(t *testing.T, baseURL string) *IAScholarPlugin {
	t.Helper()
	p := &IAScholarPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestIAScholar_Identity(t *testing.T) {
	t.Parallel()
	p := &IAScholarPlugin{}
	assert.Equal(t, SourceIAScholar, p.ID())
}

func TestIAScholar_Capabilities(t *testing.T) {
	t.Parallel()
	p := &IAScholarPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPaper)
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsCategoryFilter)
}

func TestIAScholar_Residency(t *testing.T) {
	t.Parallel()
	p := &IAScholarPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestIAScholar_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := iascholarSearchResponse{
		CountFound: 1,
		Results: []iascholarRelease{{
			Ident:           "abc123",
			Title:           "Attention is All You Need",
			ReleaseYear:     2017,
			ReleaseType:     "article-journal",
			Abstracts:       []iascholarAbstract{{Body: "<p>Transformer architecture.</p>"}},
			Contribs:        []iascholarContrib{{RawName: "Vaswani, Ashish"}},
			ExtIDs:          iascholarExtIDs{DOI: "10.1234/transformer", ArXivID: "1706.03762"},
			Publisher:       "NeurIPS",
			Language:        "en",
			WaybackFallback: []iascholarWaybackFB{{URL: "https://web.archive.org/web/20200101/...", Timestamp: "20200101"}},
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, iascholarSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "transformer", q.Get(iascholarParamQ))
		assert.Equal(t, iascholarFormatJSON, q.Get(iascholarParamFormat))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newIAScholarTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "transformer", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "iascholar:abc123", pub.ID)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, "10.1234/transformer", pub.DOI)
	assert.Equal(t, "1706.03762", pub.ArXivID)
	assert.Equal(t, "2017", pub.Published)
	assert.Equal(t, "https://doi.org/10.1234/transformer", pub.URL)
	assert.Contains(t, pub.PDFURL, "web.archive.org")
	assert.Contains(t, pub.Abstract, "Transformer")
	require.Len(t, pub.Authors, 1)
	assert.Equal(t, "Vaswani, Ashish", pub.Authors[0].Name)
}

func TestIAScholar_Search_YearRangeFilter(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "2020-2024", r.URL.Query().Get(iascholarParamFilterYear))
		_, _ = io.WriteString(w, `{"count_found":0,"results":[]}`)
	}))
	defer srv.Close()
	p := newIAScholarTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "x",
		Filters: SearchFilters{DateFrom: "2020", DateTo: "2024-06-15"},
	})
	require.NoError(t, err)
}

func TestIAScholar_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newIAScholarTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestIAScholar_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &IAScholarPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestIAScholar_YearRange(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", iascholarYearRange("", ""))
	assert.Equal(t, "2020-*", iascholarYearRange("2020", ""))
	assert.Equal(t, "*-2024", iascholarYearRange("", "2024"))
	assert.Equal(t, "2020-2024", iascholarYearRange("2020-01-01", "2024-12-31"))
}
