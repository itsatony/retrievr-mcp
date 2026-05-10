package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Wikipedia plugin uses absolute URLs derived from `lang`, so we can't
// inject a httptest base URL. Tests instead exercise the wire-mapping
// helpers + identity / capabilities / residency surfaces. A live smoke
// test (no env-gating needed since Wikipedia is keyless) confirms the
// real wire path.

func TestWikipedia_IdentityAndCapabilities(t *testing.T) {
	t.Parallel()
	p := &WikipediaPlugin{}
	assert.Equal(t, "wikipedia", p.ID())
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindEncyclopedia)
	assert.Contains(t, caps.QueryIntents, IntentReference)
	assert.Contains(t, caps.QueryIntents, IntentQuickLookup)
	assert.True(t, caps.SupportsFullText)
}

func TestWikipedia_Residency_PublicResearch(t *testing.T) {
	t.Parallel()
	tag := (&WikipediaPlugin{}).Residency()
	assert.Equal(t, RegionPublicResearch, tag.Region)
	assert.True(t, tag.Region.IsPublicResearch())
	assert.False(t, tag.Region.IsEU())
}

func TestWikipedia_HitToPublication_StripsHTMLSnippets(t *testing.T) {
	t.Parallel()
	hit := wikipediaSearchHit{
		Title:     "Attention (machine learning)",
		Snippet:   `In <span class="searchmatch">attention</span> mechanisms, …`,
		Timestamp: "2024-10-01T12:34:56Z",
	}
	pub := wikipediaHitToPublication(hit, "en")
	assert.Equal(t, "wikipedia:Attention_(machine_learning)", pub.ID)
	assert.Equal(t, "Attention (machine learning)", pub.Title)
	assert.Contains(t, pub.URL, "en.wikipedia.org/wiki/Attention_")
	assert.NotContains(t, pub.Abstract, "<span", "HTML tags must be stripped from snippet")
	assert.Contains(t, pub.Abstract, "attention mechanisms")
	assert.Equal(t, "en", pub.SourceMetadata[smetaLanguage])
	assert.Equal(t, "2024-10-01", pub.Updated)
}

func TestStripHTMLTags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{`<span class="x">foo</span>`, "foo"},
		{`<b>bold</b> and <i>italic</i>`, "bold and italic"},
		{"plain text", "plain text"},
		{"", ""},
		{"<unclosed", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, stripHTMLTags(c.in))
	}
}

// fakeWikipediaServer returns a httptest server that responds to both the
// search and summary paths. The plugin builds absolute URLs against
// <lang>.wikipedia.org so we can't redirect those — instead we exercise
// the unmarshalling code via the wire helpers directly. This test
// validates the search-response decoder.
func TestWikipedia_SearchResponseDecode(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wikipediaSearchResponse{
			Query: &wikipediaQuery{
				Search: []wikipediaSearchHit{{Title: "Go (programming language)", Snippet: "Go is a <i>language</i>"}},
				SearchInfo: &wikipediaSearchInfo{TotalHits: 42},
			},
		})
	}))
	defer srv.Close()

	p := &WikipediaPlugin{lang: "en", userAgent: "test-ua", httpClient: NewEgressClient(0), enabled: true, healthy: true, rateLimit: 10}
	resp, err := p.fetchSearch(context.Background(), srv.URL+"/anything")
	require.NoError(t, err)
	require.NotNil(t, resp.Query)
	require.Len(t, resp.Query.Search, 1)
	assert.Equal(t, 42, resp.Query.SearchInfo.TotalHits)
}

func TestWikipedia_403MapsToErrCredentialInvalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	p := &WikipediaPlugin{lang: "en", userAgent: "", httpClient: NewEgressClient(0), enabled: true, rateLimit: 10}
	_, err := p.fetchSearch(context.Background(), srv.URL+"/x")
	assert.True(t, errors.Is(err, ErrCredentialInvalid),
		"403 must surface as ErrCredentialInvalid (likely missing polite UA)")
}

func TestWikipedia_LiveSmoke(t *testing.T) {
	// Wikipedia is keyless; gate only on a small env flag so default CI
	// doesn't exercise an external host without explicit consent.
	if testing.Short() {
		t.Skip("short mode; skipping live Wikipedia smoke")
	}
	p := &WikipediaPlugin{}
	require.NoError(t, p.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		RateLimit: 10,
		Extra:     map[string]string{wikipediaExtraUserAgent: "retrievr-test/dev (+https://github.com/itsatony/retrievr-mcp; tests@example.com)"},
	}))

	got, err := p.Search(context.Background(), SearchParams{Query: "transformer attention machine learning", Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, got.Results)
	for _, r := range got.Results {
		t.Logf("hit: id=%s title=%q", r.ID, r.Title)
		assert.True(t, strings.HasPrefix(r.ID, "wikipedia:"))
		assert.NotEmpty(t, r.Title)
	}
}
