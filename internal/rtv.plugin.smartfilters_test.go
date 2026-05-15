package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Smart filters (v2.7.0) — cross-plugin filter wiring tests.
//
// Covers: include_domains / exclude_domains (brave, exa), channels
// (youtube, scrapingdog_youtube), subreddits (reddit), language (brave,
// youtube, scrapingdog_youtube, bluesky, europeana, mastodon post-filter),
// Brave freshness date wiring + 422-retry fallback, BCP-47 helpers,
// domain validation.
// ---------------------------------------------------------------------------

const (
	smartfiltersTestRefDate    = "2026-05-15"
	smartfiltersTestChannelID1 = "UCxQKljiqhbT3Cb7BFK5jdcQ"
	smartfiltersTestChannelID2 = "UCfM3zsQsOnfWNUppiycmBuw"
	smartfiltersTestChannelID3 = "UCsBjURrPoezykLs9EqgamOA"
)

// ---------------------------------------------------------------------------
// BCP-47 / domain helpers
// ---------------------------------------------------------------------------

func TestBCP47FirstSubtag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"en", "en"},
		{"EN", "en"},
		{"en-US", "en"},
		{"DE-DE", "de"},
		{"fr-CA", "fr"},
		{"  pt-BR  ", "pt"},
		{"zh-Hant-TW", "zh"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, BCP47FirstSubtag(c.in), "input %q", c.in)
	}
}

func TestMatchesLanguagePrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		record, filter string
		want           bool
	}{
		{"", "de", true},   // fail-open on missing record metadata
		{"de", "", true},   // empty filter = pass-through
		{"de", "de", true}, // exact
		{"de-DE", "de", true},
		{"DE-AT", "de", true},
		{"deu", "de", false}, // dash-or-equal rule prevents over-match
		{"den", "de", false},
		{"en", "de", false},
		{"en-US", "EN", true}, // case-insensitive
	}
	for _, c := range cases {
		assert.Equal(t, c.want, MatchesLanguagePrefix(c.record, c.filter),
			"record=%q filter=%q", c.record, c.filter)
	}
}

func TestValidateDomainList(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidateDomainList(nil))
	require.NoError(t, ValidateDomainList([]string{}))
	require.NoError(t, ValidateDomainList([]string{"example.com", "kubernetes.io"}))

	for _, bad := range []string{"", "https://example.com", "example.com/path", "exam ple.com", "with\ttab"} {
		err := ValidateDomainList([]string{bad})
		require.Error(t, err, "must reject %q", bad)
		assert.True(t, errors.Is(err, ErrInvalidDomainList), "must wrap ErrInvalidDomainList")
	}
}

// ---------------------------------------------------------------------------
// Brave freshness mapping (table per retrievr_v4.md §6.2)
// ---------------------------------------------------------------------------

func TestBraveFreshnessFromDate(t *testing.T) {
	t.Parallel()
	ref, err := time.Parse(time.DateOnly, smartfiltersTestRefDate)
	require.NoError(t, err)

	cases := []struct {
		name             string
		from, to         string
		wantFreshnessAny []string // any match acceptable
	}{
		{"empty", "", "", []string{""}},
		{"day", "2026-05-14", "", []string{braveFreshnessDay}},
		{"week", "2026-05-10", "", []string{braveFreshnessWeek}},
		{"month", "2026-04-20", "", []string{braveFreshnessMonth}},
		{"year", "2025-06-01", "", []string{braveFreshnessYear}},
		{"older-than-year-dropped", "2024-01-01", "", []string{""}},
		{"custom-range", "2026-01-01", "2026-03-31", []string{"2026-01-01to2026-03-31"}},
		{"invalid-date-omits", "not-a-date", "", []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := braveFreshnessFromDate(SearchFilters{DateFrom: c.from, DateTo: c.to}, ref)
			assert.Contains(t, c.wantFreshnessAny, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Brave wiring: include_domains, exclude_domains, freshness, search_lang
// ---------------------------------------------------------------------------

func TestBrave_Search_DomainAndLanguageFilters(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "kubernetes.io,docs.kubernetes.io", q.Get(braveParamIncludeDomains))
		assert.Equal(t, "reddit.com", q.Get(braveParamExcludeDomains))
		assert.Equal(t, "de", q.Get(braveParamSearchLang), "BCP-47 first subtag of de-DE")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildBraveTestResponse(nil, nil))
	}))
	defer srv.Close()

	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{
		Query: "k8s",
		Limit: 5,
		Filters: SearchFilters{
			IncludeDomains: []string{"kubernetes.io", "docs.kubernetes.io"},
			ExcludeDomains: []string{"reddit.com"},
			Language:       "de-DE",
		},
	})
	require.NoError(t, err)
}

func TestBrave_Search_DateAppliesFreshnessBucket(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get(braveParamFreshness)
		assert.Contains(t, []string{braveFreshnessDay, braveFreshnessWeek, braveFreshnessMonth, braveFreshnessYear},
			got, "DateFrom must map to one of the bucket tokens")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildBraveTestResponse(nil, nil))
	}))
	defer srv.Close()

	from := time.Now().AddDate(0, -2, 0).Format(time.DateOnly) // ~2 months ago → pm or py
	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "x",
		Limit:   1,
		Filters: SearchFilters{DateFrom: from},
	})
	require.NoError(t, err)
}

func TestBrave_Search_AbsentFiltersOmitParams(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.NotContains(t, q, braveParamIncludeDomains)
		assert.NotContains(t, q, braveParamExcludeDomains)
		assert.NotContains(t, q, braveParamFreshness)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildBraveTestResponse(nil, nil))
	}))
	defer srv.Close()

	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
}

func TestBrave_Search_InvalidDomainRejected(t *testing.T) {
	t.Parallel()
	p := newBraveTestPlugin(t, "http://unused", braveTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "x",
		Limit:   1,
		Filters: SearchFilters{IncludeDomains: []string{"https://example.com"}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidDomainList))
}

func TestBrave_Search_CustomRange422FallsBackToBucket(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		got := r.URL.Query().Get(braveParamFreshness)
		switch n {
		case 1:
			assert.Contains(t, got, "to", "first call uses custom range")
			http.Error(w, "bad range", http.StatusUnprocessableEntity)
		case 2:
			assert.NotContains(t, got, "to", "retry uses bucket token")
			assert.Contains(t, []string{braveFreshnessDay, braveFreshnessWeek, braveFreshnessMonth, braveFreshnessYear}, got)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, buildBraveTestResponse(nil, nil))
		default:
			t.Fatalf("unexpected third call")
		}
	}))
	defer srv.Close()

	from := time.Now().AddDate(0, -1, 0).Format(time.DateOnly)
	to := time.Now().Format(time.DateOnly)
	p := newBraveTestPlugin(t, srv.URL, braveTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "k8s",
		Limit:   1,
		Filters: SearchFilters{DateFrom: from, DateTo: to},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load(), "must retry exactly once with bucket")
}

// ---------------------------------------------------------------------------
// Exa wiring: includeDomains / excludeDomains in JSON body
// ---------------------------------------------------------------------------

func TestExa_Search_DomainFilters(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		var req exaSearchRequest
		require.NoError(t, json.Unmarshal(buf, &req))
		assert.Equal(t, []string{"kubernetes.io"}, req.IncludeDomains)
		assert.Equal(t, []string{"reddit.com", "twitter.com"}, req.ExcludeDomains)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"requestId":"x","results":[]}`)
	}))
	defer srv.Close()

	p := newExaTestPlugin(t, srv.URL, "exa-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "k8s",
		Limit: 5,
		Filters: SearchFilters{
			IncludeDomains: []string{"kubernetes.io"},
			ExcludeDomains: []string{"reddit.com", "twitter.com"},
		},
	})
	require.NoError(t, err)
}

func TestExa_Search_InvalidDomainRejected(t *testing.T) {
	t.Parallel()
	p := newExaTestPlugin(t, "http://unused", "exa-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "x",
		Limit:   1,
		Filters: SearchFilters{ExcludeDomains: []string{"bad path/here"}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidDomainList))
}

// ---------------------------------------------------------------------------
// YouTube wiring: channelId, multi-channel fan-out, relevanceLanguage
// ---------------------------------------------------------------------------

func TestYouTube_Search_ChannelAndLanguage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, smartfiltersTestChannelID1, q.Get(youtubeParamChannelID))
		assert.Equal(t, "de", q.Get(youtubeParamRelevanceLanguage))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildYouTubeSearchTestResponse(nil, "", 0))
	}))
	defer srv.Close()

	p := newYouTubeTestPlugin(t, srv.URL, "yt-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "kubernetes",
		Limit: 5,
		Filters: SearchFilters{
			Channels: []string{smartfiltersTestChannelID1},
			Language: "de-DE",
		},
	})
	require.NoError(t, err)
}

func TestYouTube_Search_MultiChannelFansOut(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	seenChannels := make(map[string]bool)
	var mu = make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		ch := r.URL.Query().Get(youtubeParamChannelID)
		require.NotEmpty(t, ch, "every fan-out call must carry a channelId")
		mu <- struct{}{}
		seenChannels[ch] = true
		<-mu
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildYouTubeSearchTestResponse(nil, "", 0))
	}))
	defer srv.Close()

	p := newYouTubeTestPlugin(t, srv.URL, "yt-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "k8s",
		Limit: 3,
		Filters: SearchFilters{
			Channels: []string{smartfiltersTestChannelID1, smartfiltersTestChannelID2, smartfiltersTestChannelID3},
		},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, calls.Load())
	assert.Len(t, seenChannels, 3)
}

func TestYouTube_Search_TooManyChannelsRejected(t *testing.T) {
	t.Parallel()
	p := newYouTubeTestPlugin(t, "http://unused", "yt-key")
	channels := make([]string, youtubeMaxChannelFanout+1)
	for i := range channels {
		channels[i] = "UC" + strings.Repeat("a", 22)
	}
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "x",
		Limit:   1,
		Filters: SearchFilters{Channels: channels},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTooManyChannels))
}

// ---------------------------------------------------------------------------
// Scrapingdog YouTube: channel: qualifier + language
// ---------------------------------------------------------------------------

func TestScrapingdogYouTube_Search_ChannelAndLanguage(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Contains(t, q.Get(scrapingdogYouTubeQueryParamQuery), scrapingdogYouTubeChannelQualifier+smartfiltersTestChannelID1)
		assert.Equal(t, "de", q.Get(scrapingdogYouTubeQueryParamLanguage))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"video_results":[]}`)
	}))
	defer srv.Close()

	p := newScrapingdogYouTubeTestPlugin(t, srv.URL, "sd-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "k8s",
		Limit: 5,
		Filters: SearchFilters{
			Channels: []string{smartfiltersTestChannelID1},
			Language: "de-DE",
		},
	})
	require.NoError(t, err)
}

func TestScrapingdogYouTube_TooManyChannelsRejected(t *testing.T) {
	t.Parallel()
	p := newScrapingdogYouTubeTestPlugin(t, "http://unused", "sd-key")
	channels := make([]string, scrapingdogYouTubeMaxChannelFanout+1)
	for i := range channels {
		channels[i] = smartfiltersTestChannelID1
	}
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1, Filters: SearchFilters{Channels: channels}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTooManyChannels))
}

// ---------------------------------------------------------------------------
// Reddit: subreddit routing + fan-out
// ---------------------------------------------------------------------------

func TestReddit_Search_SubredditScoped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/access_token":
			writeRedditTokenResponse(w, "tok", 3600)
		case "/r/golang/search":
			assert.Equal(t, redditQueryParamRestrictY, r.URL.Query().Get(redditQueryParamRestrict))
			writeRedditSearchResponse(w, makeRedditListing(nil, ""))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := newRedditTestPlugin(t, srv.URL, "client:secret")
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "channels",
		Limit:   5,
		Filters: SearchFilters{Subreddits: []string{"golang"}},
	})
	require.NoError(t, err)
}

func TestReddit_Search_MultiSubredditFanOut(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	subsSeen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/access_token":
			writeRedditTokenResponse(w, "tok", 3600)
		case strings.HasPrefix(r.URL.Path, "/r/") && strings.HasSuffix(r.URL.Path, "/search"):
			calls.Add(1)
			sub := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/r/"), "/search")
			subsSeen[sub] = true
			writeRedditSearchResponse(w, makeRedditListing(nil, ""))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := newRedditTestPlugin(t, srv.URL, "client:secret")
	_, err := p.Search(context.Background(), SearchParams{
		Query:   "x",
		Limit:   3,
		Filters: SearchFilters{Subreddits: []string{"golang", "kubernetes"}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load())
	assert.Equal(t, map[string]bool{"golang": true, "kubernetes": true}, subsSeen)
}

func TestReddit_TooManySubredditsRejected(t *testing.T) {
	t.Parallel()
	p := newRedditTestPlugin(t, "http://unused", "client:secret")
	subs := make([]string, redditMaxSubredditFanout+1)
	for i := range subs {
		subs[i] = "x"
	}
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1, Filters: SearchFilters{Subreddits: subs}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTooManySubreddits))
}

func TestReddit_Search_UnscopedHitsAllRoute(t *testing.T) {
	t.Parallel()
	hitSearch := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/access_token":
			writeRedditTokenResponse(w, "tok", 3600)
		case redditSearchPath:
			hitSearch = true
			assert.Empty(t, r.URL.Query().Get(redditQueryParamRestrict), "no restrict_sr when unscoped")
			writeRedditSearchResponse(w, makeRedditListing(nil, ""))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := newRedditTestPlugin(t, srv.URL, "client:secret")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
	assert.True(t, hitSearch)
}

// ---------------------------------------------------------------------------
// Mastodon: client-side language post-filter (fail-open on empty)
// ---------------------------------------------------------------------------

func TestMastodon_Search_LanguagePostFilter(t *testing.T) {
	t.Parallel()
	statuses := []mastodonStatus{
		{ID: "1", URL: "https://example.social/1", Content: "<p>de1</p>", Language: "de"},
		{ID: "2", URL: "https://example.social/2", Content: "<p>de2</p>", Language: "de-DE"},
		{ID: "3", URL: "https://example.social/3", Content: "<p>en1</p>", Language: "en"},
		{ID: "4", URL: "https://example.social/4", Content: "<p>no-lang</p>", Language: ""},
	}
	body := mastodonSearchResponse{Statuses: statuses}
	raw, _ := json.Marshal(body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	cases := []struct {
		filter  string
		wantIDs []string
	}{
		{"", []string{"1", "2", "3", "4"}},
		{"de", []string{"1", "2", "4"}}, // fail-open on empty record
		{"en", []string{"3", "4"}},
		{"fr", []string{"4"}}, // only fail-open ones
	}
	// Subtests are serial: they share the parent's httptest server which
	// would close on return if any subtest paralleled past the parent.
	for _, c := range cases {
		t.Run("lang="+c.filter, func(t *testing.T) {
			p := newMastodonTestPlugin(t, srv.URL, string(RegionEU))
			got, err := p.Search(context.Background(), SearchParams{
				Query: "x", Limit: 10, Filters: SearchFilters{Language: c.filter},
			})
			require.NoError(t, err)
			ids := make([]string, 0, len(got.Results))
			for _, r := range got.Results {
				// Status ID is encoded as "mastodon:<id>" → unprefix
				ids = append(ids, strings.TrimPrefix(r.ID, "mastodon:"))
			}
			assert.ElementsMatch(t, c.wantIDs, ids)
		})
	}
}

// ---------------------------------------------------------------------------
// Bluesky + Europeana: lang param wiring
// ---------------------------------------------------------------------------

func TestBluesky_Search_LanguageWire(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "de", r.URL.Query().Get(blueskyQueryParamLang))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"posts":[]}`)
	}))
	defer srv.Close()

	p := newBlueskyTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x", Limit: 5, Filters: SearchFilters{Language: "de-DE"},
	})
	require.NoError(t, err)
}

func TestEuropeana_Search_LanguageWire(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "fr", r.URL.Query().Get(europeanaQueryParamLang))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"items":[]}`)
	}))
	defer srv.Close()

	p := newEuropeanaTestPlugin(t, srv.URL, "euro-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "vermeer", Limit: 5, Filters: SearchFilters{Language: "fr-CA"},
	})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Capability matrix — assertion that v2.7.0 flags are wired on the right
// plugins. Acts as a snapshot/regression guard for rtv_list_sources output.
// ---------------------------------------------------------------------------

func TestSourceCapabilities_SmartFiltersMatrix(t *testing.T) {
	t.Parallel()
	type sourceCaps struct {
		Domain   bool
		Channel  bool
		Language bool
	}
	want := map[string]sourceCaps{
		SourceBrave:              {Domain: true, Channel: false, Language: true},
		SourceExa:                {Domain: true, Channel: false, Language: false},
		SourceYouTube:            {Domain: false, Channel: true, Language: true},
		SourceScrapingdogYouTube: {Domain: false, Channel: true, Language: true},
		SourceReddit:             {Domain: false, Channel: true, Language: false},
		SourceMastodon:           {Domain: false, Channel: false, Language: true},
		SourceBluesky:            {Domain: false, Channel: false, Language: true},
		SourceEuropeana:          {Domain: false, Channel: false, Language: true},
	}
	matrix := map[string]sourceCaps{
		SourceBrave:              capsFor(t, &BravePlugin{}),
		SourceExa:                capsFor(t, &ExaPlugin{}),
		SourceYouTube:            capsFor(t, &YouTubePlugin{}),
		SourceScrapingdogYouTube: capsFor(t, &ScrapingdogYouTubePlugin{}),
		SourceReddit:             capsFor(t, &RedditPlugin{}),
		SourceMastodon:           capsFor(t, &MastodonPlugin{}),
		SourceBluesky:            capsFor(t, &BlueskyPlugin{}),
		SourceEuropeana:          capsFor(t, &EuropeanaPlugin{}),
	}
	for source, exp := range want {
		assert.Equal(t, exp, matrix[source], "capability flags for %s", source)
	}
}

func capsFor(t *testing.T, p interface{ Capabilities() SourceCapabilities }) struct {
	Domain, Channel, Language bool
} {
	t.Helper()
	c := p.Capabilities()
	return struct{ Domain, Channel, Language bool }{c.SupportsDomainFilter, c.SupportsChannelFilter, c.SupportsLanguageFilter}
}
