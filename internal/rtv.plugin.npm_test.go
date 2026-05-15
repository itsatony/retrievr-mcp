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
// v5 cycle 4 / v2.11.0 — npm tests.
// ---------------------------------------------------------------------------

func newNPMTestPlugin(t *testing.T, baseURL string) *NPMPlugin {
	t.Helper()
	p := &NPMPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestNPM_Identity(t *testing.T) {
	t.Parallel()
	p := &NPMPlugin{}
	assert.Equal(t, SourceNPM, p.ID())
}

func TestNPM_Capabilities(t *testing.T) {
	t.Parallel()
	p := &NPMPlugin{}
	caps := p.Capabilities()
	assert.True(t, caps.SupportsCategoryFilter)
	assert.False(t, caps.SupportsDateFilter)
	assert.Contains(t, caps.Kinds, KindCode)
}

func TestNPM_Residency(t *testing.T) {
	t.Parallel()
	p := &NPMPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestNPM_Search_HappyPath(t *testing.T) {
	t.Parallel()
	objs := []npmSearchObject{{
		Package: npmPackage{
			Name:        "express",
			Version:     "4.18.2",
			Description: "Fast, unopinionated, minimalist web framework",
			Keywords:    []string{"web", "framework", "http"},
			Date:        "2022-10-08T14:00:00.000Z",
			Links: npmLinks{
				NPM:        "https://www.npmjs.com/package/express",
				Homepage:   "http://expressjs.com/",
				Repository: "https://github.com/expressjs/express",
			},
			Author: npmAuthor{Name: "TJ Holowaychuk"},
		},
		Score: npmScore{Final: 0.88},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, npmSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "express", q.Get(npmParamText))
		assert.Equal(t, "10", q.Get(npmParamSize))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(npmSearchResponse{Objects: objs, Total: 1})
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newNPMTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "express", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "npm:express", pub.ID)
	assert.Equal(t, ContentTypePackage, pub.ContentType)
	assert.Equal(t, "express", pub.Title)
	assert.Equal(t, "2022-10-08", pub.Published)
	assert.Equal(t, "https://www.npmjs.com/package/express", pub.URL)
	assert.Equal(t, "npm:express", pub.SourceMetadata[MetaKeyPackageID])
	assert.Equal(t, "4.18.2", pub.SourceMetadata[smetaPackageVersion])
	assert.Equal(t, "https://github.com/expressjs/express", pub.SourceMetadata[smetaPackageRepoURL])
}

func TestNPM_Search_CategoriesAddKeywordsQualifier(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get(npmParamText)
		assert.Equal(t, "router keywords:web", got)
		_, _ = io.WriteString(w, `{"objects":[],"total":0}`)
	}))
	defer srv.Close()
	p := newNPMTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "router",
		Filters: SearchFilters{Categories: []string{"web"}},
	})
	require.NoError(t, err)
}

func TestNPM_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newNPMTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestNPM_Get_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/express/latest", r.URL.Path)
		b, _ := json.Marshal(npmPackage{Name: "express", Version: "4.18.2"})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	p := newNPMTestPlugin(t, srv.URL)
	pub, err := p.Get(context.Background(), "express", nil, FormatNative)
	require.NoError(t, err)
	assert.Equal(t, "npm:express", pub.ID)
	assert.Equal(t, "4.18.2", pub.SourceMetadata[smetaPackageVersion])
}
