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
// v5 cycle 4 / v2.11.0 — PyPI tests.
// ---------------------------------------------------------------------------

func newPyPITestPlugin(t *testing.T, baseURL string) *PyPIPlugin {
	t.Helper()
	p := &PyPIPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

// pypiTestSearchHTML returns a stub of the pypi.org/search page with two
// package snippets matching the regex shape.
const pypiTestSearchHTML = `<!doctype html><html><body>
<a class="package-snippet" href="/project/requests/">
  <h3>
    <span class="package-snippet__name">requests</span>
    <span class="package-snippet__version">2.31.0</span>
    <time datetime="2023-05-22T18:30:00+0000"></time>
  </h3>
  <p class="package-snippet__description">Python HTTP for Humans.</p>
</a>
<a class="package-snippet" href="/project/requests-toolbelt/">
  <h3>
    <span class="package-snippet__name">requests-toolbelt</span>
    <span class="package-snippet__version">1.0.0</span>
    <time datetime="2023-05-01T00:00:00+0000"></time>
  </h3>
  <p class="package-snippet__description">A utility belt for advanced users of python-requests</p>
</a>
</body></html>`

func TestPyPI_Identity(t *testing.T) {
	t.Parallel()
	p := &PyPIPlugin{}
	assert.Equal(t, SourcePyPI, p.ID())
}

func TestPyPI_Capabilities(t *testing.T) {
	t.Parallel()
	p := &PyPIPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindCode)
	assert.False(t, caps.SupportsCategoryFilter)
}

func TestPyPI_Residency(t *testing.T) {
	t.Parallel()
	p := &PyPIPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestPyPI_Search_ParsesHTML(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, pypiSearchPath, r.URL.Path)
		assert.Equal(t, "requests", r.URL.Query().Get(pypiParamQ))
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, pypiTestSearchHTML)
	}))
	defer srv.Close()

	p := newPyPITestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "requests", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 2)

	first := res.Results[0]
	assert.Equal(t, "pypi:requests", first.ID)
	assert.Equal(t, "requests", first.Title)
	assert.Equal(t, "https://pypi.org/project/requests/", first.URL)
	assert.Contains(t, first.Abstract, "Python HTTP")
	assert.Equal(t, "2023-05-22", first.Published)
	assert.Equal(t, "2.31.0", first.SourceMetadata[smetaPackageVersion])
}

func TestPyPI_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newPyPITestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestPyPI_Get_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/pypi/requests/json", r.URL.Path)
		b, _ := json.Marshal(pypiPackageEnvelope{
			Info: pypiInfo{
				Name:       "requests",
				Version:    "2.31.0",
				Summary:    "Python HTTP for Humans.",
				License:    "Apache-2.0",
				Author:     "Kenneth Reitz",
				HomePage:   "https://requests.readthedocs.io",
				PackageURL: "https://pypi.org/project/requests/",
				Keywords:   "http, https, web, api",
			},
		})
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newPyPITestPlugin(t, srv.URL)
	pub, err := p.Get(context.Background(), "requests", nil, FormatNative)
	require.NoError(t, err)
	assert.Equal(t, "pypi:requests", pub.ID)
	assert.Equal(t, "Apache-2.0", pub.License)
	assert.Contains(t, pub.Categories, "http")
	assert.Contains(t, pub.Categories, "api")
}

func TestPyPI_ParseSearchHTML_RespectsLimit(t *testing.T) {
	t.Parallel()
	hits := pypiParseSearchHTML(pypiTestSearchHTML, 1)
	require.Len(t, hits, 1)
	assert.Equal(t, "requests", hits[0].Title)
}
