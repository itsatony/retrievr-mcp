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
// v5 cycle 4 / v2.11.0 — crates.io tests.
// ---------------------------------------------------------------------------

func newCratesTestPlugin(t *testing.T, baseURL string) *CratesPlugin {
	t.Helper()
	p := &CratesPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestCrates_Identity(t *testing.T) {
	t.Parallel()
	p := &CratesPlugin{}
	assert.Equal(t, SourceCrates, p.ID())
}

func TestCrates_Capabilities(t *testing.T) {
	t.Parallel()
	p := &CratesPlugin{}
	caps := p.Capabilities()
	assert.True(t, caps.SupportsCategoryFilter)
	assert.True(t, caps.SupportsSortDate)
	assert.Contains(t, caps.Kinds, KindCode)
}

func TestCrates_Residency(t *testing.T) {
	t.Parallel()
	p := &CratesPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestCrates_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := cratesSearchResponse{
		Crates: []cratesCrate{{
			Name:          "tokio",
			MaxVersion:    "1.36.0",
			Description:   "An event-driven, non-blocking I/O platform.",
			Repository:    "https://github.com/tokio-rs/tokio",
			Documentation: "https://docs.rs/tokio",
			Downloads:     200000000,
			Categories:    []string{"asynchronous"},
			Keywords:      []string{"async", "io"},
			UpdatedAt:     "2024-02-25T12:00:00Z",
		}},
		Meta: cratesMeta{Total: 1},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, cratesSearchPath, r.URL.Path)
		assert.Contains(t, r.Header.Get("User-Agent"), "retrievr-mcp")
		q := r.URL.Query()
		assert.Equal(t, "tokio", q.Get(cratesParamQ))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newCratesTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "tokio", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "crates:tokio", pub.ID)
	assert.Equal(t, ContentTypePackage, pub.ContentType)
	assert.Equal(t, "tokio", pub.Title)
	assert.Equal(t, "https://crates.io/crates/tokio", pub.URL)
	assert.Equal(t, "2024-02-25", pub.Published)
	assert.Equal(t, "crates:tokio", pub.SourceMetadata[MetaKeyPackageID])
	assert.Equal(t, "1.36.0", pub.SourceMetadata[smetaPackageVersion])
	assert.Equal(t, int64(200000000), pub.SourceMetadata[smetaPackageDownloads])
	assert.Contains(t, pub.Categories, "asynchronous")
}

func TestCrates_Search_CategoryAndSort(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "asynchronous", q.Get(cratesParamCategory))
		assert.Equal(t, cratesSortRecentUpdates, q.Get(cratesParamSort))
		_, _ = io.WriteString(w, `{"crates":[],"meta":{"total":0}}`)
	}))
	defer srv.Close()
	p := newCratesTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "x",
		Sort:    SortDateDesc,
		Filters: SearchFilters{Categories: []string{"Asynchronous"}},
	})
	require.NoError(t, err)
}

func TestCrates_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newCratesTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestCrates_Get_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, cratesSearchPath+"/tokio", r.URL.Path)
		b, _ := json.Marshal(struct {
			Crate cratesCrate `json:"crate"`
		}{Crate: cratesCrate{Name: "tokio", MaxVersion: "1.36.0"}})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	p := newCratesTestPlugin(t, srv.URL)
	pub, err := p.Get(context.Background(), "tokio", nil, FormatNative)
	require.NoError(t, err)
	assert.Equal(t, "crates:tokio", pub.ID)
}
