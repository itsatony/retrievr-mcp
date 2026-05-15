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
// v5 cycle 3 / v2.10.0 — Wikidata tests.
// ---------------------------------------------------------------------------

func newWikidataTestPlugin(t *testing.T, baseURL string) *WikidataPlugin {
	t.Helper()
	p := &WikidataPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestWikidata_Identity(t *testing.T) {
	t.Parallel()
	p := &WikidataPlugin{}
	assert.Equal(t, SourceWikidata, p.ID())
}

func TestWikidata_Capabilities(t *testing.T) {
	t.Parallel()
	p := &WikidataPlugin{}
	caps := p.Capabilities()
	assert.True(t, caps.SupportsLanguageFilter)
	assert.Contains(t, caps.Kinds, KindFact)
	assert.False(t, caps.SupportsDateFilter)
}

func TestWikidata_Residency(t *testing.T) {
	t.Parallel()
	p := &WikidataPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionPublicResearch, tag.Region)
}

func TestWikidata_Search_HappyPath(t *testing.T) {
	t.Parallel()
	hits := []wikidataSearchHit{{
		ID:          "Q82425",
		PageID:      168312,
		ConceptURI:  "http://www.wikidata.org/entity/Q82425",
		URL:         "//www.wikidata.org/wiki/Q82425",
		Label:       "Brandenburg Gate",
		Description: "neoclassical triumphal arch in Berlin, Germany",
		Aliases:     []string{"Brandenburger Tor"},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, wikidataAPIPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, wikidataActionSearch, q.Get(wikidataParamAction))
		assert.Equal(t, "Brandenburg Gate", q.Get(wikidataParamSearch))
		assert.Equal(t, "en", q.Get(wikidataParamLanguage))
		assert.Equal(t, "10", q.Get(wikidataParamLimit))
		b, _ := json.Marshal(wikidataSearchResponse{Search: hits, Success: 1})
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newWikidataTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "Brandenburg Gate", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "wikidata:Q82425", pub.ID)
	assert.Equal(t, "Brandenburg Gate", pub.Title)
	assert.Equal(t, "https://www.wikidata.org/wiki/Q82425", pub.URL)
	assert.Contains(t, pub.Abstract, "neoclassical")
	assert.Equal(t, "Q82425", pub.SourceMetadata[wikidataMetaKeyQID])
	assert.Equal(t, []string{"Brandenburger Tor"}, pub.SourceMetadata[wikidataMetaKeyAliases])
}

func TestWikidata_Search_LanguageOverride(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "de", r.URL.Query().Get(wikidataParamLanguage))
		b, _ := json.Marshal(wikidataSearchResponse{Search: nil, Success: 1})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	p := newWikidataTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "Brandenburg Tor",
		Filters: SearchFilters{Language: "de"},
	})
	require.NoError(t, err)
}

func TestWikidata_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newWikidataTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestWikidata_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &WikidataPlugin{}
	_, err := p.Get(context.Background(), "Q1", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestWikidata_HitToPublication_RelativeURL(t *testing.T) {
	t.Parallel()
	pub := wikidataHitToPublication(&wikidataSearchHit{
		ID:    "Q42",
		URL:   "//www.wikidata.org/wiki/Q42",
		Label: "Douglas Adams",
	})
	assert.Equal(t, "https://www.wikidata.org/wiki/Q42", pub.URL)
}

// Pre-empt body usage so io is reachable when fixture writers grow.
var _ = io.Discard
