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
// v5 cycle 5 / v2.12.0 — EUR-Lex tests.
// ---------------------------------------------------------------------------

func newEURLexTestPlugin(t *testing.T, baseURL string) *EURLexPlugin {
	t.Helper()
	p := &EURLexPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

// eurLexTestSearchHTML reproduces the relevant CELEX-anchor shape of the
// EUR-Lex search page.
const eurLexTestSearchHTML = `<!doctype html><html><body>
<div class="SearchResult">
  <h2><a class="title" href="./legal-content/EN/TXT/?uri=CELEX:32016R0679">Regulation (EU) 2016/679 of the European Parliament and of the Council of 27 April 2016 on the protection of natural persons with regard to the processing of personal data</a></h2>
</div>
<div class="SearchResult">
  <h2><a class="title" href="./legal-content/EN/TXT/?uri=CELEX:62019CJ0311">JUDGMENT OF THE COURT in case C-311/19</a></h2>
</div>
</body></html>`

func TestEURLex_Identity(t *testing.T) {
	t.Parallel()
	p := &EURLexPlugin{}
	assert.Equal(t, SourceEURLex, p.ID())
}

func TestEURLex_Capabilities(t *testing.T) {
	t.Parallel()
	p := &EURLexPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindLaw)
	assert.True(t, caps.SupportsLanguageFilter)
}

func TestEURLex_Residency(t *testing.T) {
	t.Parallel()
	p := &EURLexPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
}

func TestEURLex_Search_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, eurLexSearchPath, r.URL.Path)
		assert.Equal(t, "GDPR", r.URL.Query().Get(eurLexParamText))
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, eurLexTestSearchHTML)
	}))
	defer srv.Close()

	p := newEURLexTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "GDPR", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 2)

	first := res.Results[0]
	assert.Equal(t, "eurlex:32016R0679", first.ID)
	assert.Equal(t, ContentTypePaper, first.ContentType)
	assert.Contains(t, first.Title, "Regulation (EU) 2016/679")
	assert.Equal(t, "32016R0679", first.SourceMetadata[MetaKeyCitationCode])
	assert.Equal(t, "EU", first.SourceMetadata[smetaLawJurisdiction])
	assert.Contains(t, first.URL, "CELEX:32016R0679")
}

func TestEURLex_Search_LanguageHint(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "de", r.URL.Query().Get(eurLexParamLang))
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, eurLexTestSearchHTML)
	}))
	defer srv.Close()
	p := newEURLexTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{
		Query:   "GDPR",
		Filters: SearchFilters{Language: "de"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Results)
	assert.Equal(t, "de", res.Results[0].Language)
	assert.Contains(t, res.Results[0].URL, "/DE/TXT/")
}

func TestEURLex_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newEURLexTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestEURLex_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &EURLexPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestEURLex_ParseSearchHTML_DedupsByCELEX(t *testing.T) {
	t.Parallel()
	html := eurLexTestSearchHTML + `
<a href="?uri=CELEX:32016R0679">link 2 to same doc</a>`
	hits := eurLexParseSearchHTML(html, 10, "EN")
	// Two distinct CELEX entries despite three anchor references.
	require.Len(t, hits, 2)
}
