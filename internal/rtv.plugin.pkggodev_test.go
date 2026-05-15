package internal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v5 cycle 4 / v2.11.0 — pkg.go.dev tests.
// ---------------------------------------------------------------------------

func newPkgGoDevTestPlugin(t *testing.T, baseURL string) *PkgGoDevPlugin {
	t.Helper()
	p := &PkgGoDevPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

// pkggoTestSearchHTML reproduces the snippet shape of the pkg.go.dev
// search page closely enough for the parser to extract a result.
const pkggoTestSearchHTML = `<!doctype html><html><body>
<div class="SearchSnippet" data-test-id="snippet-card-0">
  <h2 class="SearchSnippet-headerContainer">
    <a data-test-id="snippet-title" href="/github.com/go-chi/chi/v5">
      <span data-test-id="snippet-title-name">chi</span>
      <span class="SearchSnippet-header-path">(github.com/go-chi/chi/v5)</span>
    </a>
  </h2>
  <p data-test-id="snippet-synopsis">Package chi is a small, idiomatic and composable router.</p>
  <div class="SearchSnippet-infoLabel">
    <span data-test-id="snippet-importedby">Imported by 12,345</span>
    <span data-test-id="snippet-published">v5.0.10 published on Mar 5, 2024</span>
    <span data-test-id="snippet-license">MIT</span>
  </div>
</div>
</body></html>`

func TestPkgGoDev_Identity(t *testing.T) {
	t.Parallel()
	p := &PkgGoDevPlugin{}
	assert.Equal(t, SourcePkgGoDev, p.ID())
}

func TestPkgGoDev_Capabilities(t *testing.T) {
	t.Parallel()
	p := &PkgGoDevPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindCode)
	assert.False(t, caps.SupportsDateFilter)
}

func TestPkgGoDev_Residency(t *testing.T) {
	t.Parallel()
	p := &PkgGoDevPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestPkgGoDev_Search_ParsesHTML(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, pkggoSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "go-chi/chi", q.Get(pkggoParamQ))
		assert.Equal(t, pkggoModePkg, q.Get(pkggoParamMode))
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, pkggoTestSearchHTML)
	}))
	defer srv.Close()

	p := newPkgGoDevTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "go-chi/chi", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "pkggodev:github.com/go-chi/chi/v5", pub.ID)
	assert.Equal(t, "github.com/go-chi/chi/v5", pub.Title)
	assert.Equal(t, "https://pkg.go.dev/github.com/go-chi/chi/v5", pub.URL)
	assert.Contains(t, pub.Abstract, "composable router")
	assert.Equal(t, "MIT", pub.License)
	assert.Equal(t, "github.com/go-chi/chi/v5", pub.SourceMetadata[pkggoMetaKeyImportPath])
	assert.Equal(t, "12345", pub.SourceMetadata[pkggoMetaKeyImportedBy])
}

func TestPkgGoDev_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newPkgGoDevTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestPkgGoDev_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &PkgGoDevPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}
