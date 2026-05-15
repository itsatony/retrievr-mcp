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
// v5 cycle 5 / v2.12.0 — CourtListener tests.
// ---------------------------------------------------------------------------

func newCourtListenerTestPlugin(t *testing.T, baseURL, apiKey string) *CourtListenerPlugin {
	t.Helper()
	p := &CourtListenerPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestCourtListener_Identity(t *testing.T) {
	t.Parallel()
	p := &CourtListenerPlugin{}
	assert.Equal(t, SourceCourtListener, p.ID())
}

func TestCourtListener_Capabilities(t *testing.T) {
	t.Parallel()
	p := &CourtListenerPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindLaw)
	assert.True(t, caps.SupportsCategoryFilter)
	assert.True(t, caps.SupportsDateFilter)
}

func TestCourtListener_Residency(t *testing.T) {
	t.Parallel()
	p := &CourtListenerPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestCourtListener_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := courtListenerSearchResponse{
		Count: 1,
		Results: []courtListenerSearchHit{{
			ID:                  json.RawMessage(`108713`),
			AbsoluteURL:         "/opinion/108713/miranda-v-arizona/",
			CaseName:            "Miranda v. Arizona",
			Court:               "scotus",
			CourtCitationString: "384 U.S. 436",
			DateFiled:           "1966-06-13",
			DocketNumber:        "759",
			Snippet:             "The prosecution may not use statements ...",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, courtListenerSearchPath, r.URL.Path)
		assert.Equal(t, "Token test-token", r.Header.Get(courtListenerHeaderAuthorization))
		q := r.URL.Query()
		assert.Equal(t, "miranda", q.Get("q"))
		assert.Equal(t, courtListenerTypeOpinion, q.Get("type"))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newCourtListenerTestPlugin(t, srv.URL, "test-token")
	res, err := p.Search(context.Background(), SearchParams{Query: "miranda", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "courtlistener:108713", pub.ID)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, "Miranda v. Arizona", pub.Title)
	assert.Equal(t, "https://www.courtlistener.com/opinion/108713/miranda-v-arizona/", pub.URL)
	assert.Equal(t, "1966-06-13", pub.Published)
	assert.Equal(t, "384 U.S. 436", pub.SourceMetadata[MetaKeyCitationCode])
	assert.Equal(t, "scotus", pub.SourceMetadata[smetaLawCourt])
	assert.Equal(t, "US", pub.SourceMetadata[smetaLawJurisdiction])
}

func TestCourtListener_Search_CategoriesAndDate(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "scotus,ca9", q.Get("court"))
		assert.Equal(t, "1960-01-01", q.Get("filed_after"))
		assert.Equal(t, "1970-12-31", q.Get("filed_before"))
		assert.Equal(t, "dateFiled desc", q.Get("order_by"))
		_, _ = io.WriteString(w, `{"count":0,"results":[]}`)
	}))
	defer srv.Close()
	p := newCourtListenerTestPlugin(t, srv.URL, "")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Sort:  SortDateDesc,
		Filters: SearchFilters{
			Categories: []string{"scotus", "ca9"},
			DateFrom:   "1960",
			DateTo:     "1970",
		},
	})
	require.NoError(t, err)
}

func TestCourtListener_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newCourtListenerTestPlugin(t, srv.URL, "bad-token")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestCourtListener_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newCourtListenerTestPlugin(t, srv.URL, "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestCourtListener_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &CourtListenerPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}
