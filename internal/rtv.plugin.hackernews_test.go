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
// v5 cycle 1 / v2.8.0 — Hacker News (Algolia mirror) tests.
// ---------------------------------------------------------------------------

const (
	hackerNewsTestObjectID = "39000123"
	hackerNewsTestTitle    = "Show HN: A new way to search papers"
	hackerNewsTestStoryURL = "https://example.com/cool-story"
)

func newHackerNewsTestPlugin(t *testing.T, baseURL string) *HackerNewsPlugin {
	t.Helper()
	p := &HackerNewsPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		RateLimit: 100,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildHackerNewsTestResponse(hits []hackerNewsHit, nbHits int) string {
	b, _ := json.Marshal(hackerNewsSearchResponse{Hits: hits, NbHits: nbHits, HitsPerPage: len(hits)})
	return string(b)
}

func hnIntPtr(v int) *int { return &v }

func TestHackerNews_Identity(t *testing.T) {
	t.Parallel()
	p := &HackerNewsPlugin{}
	assert.Equal(t, SourceHackerNews, p.ID())
}

func TestHackerNews_Capabilities(t *testing.T) {
	t.Parallel()
	p := &HackerNewsPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindQA)
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsSortDate)
	assert.False(t, caps.SupportsLanguageFilter)
	assert.False(t, caps.SupportsCategoryFilter)
	assert.Equal(t, hackerNewsMaxLimitCap, caps.MaxResultsPerQuery)
}

func TestHackerNews_Residency(t *testing.T) {
	t.Parallel()
	p := &HackerNewsPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.Equal(t, DPAUnknown, tag.DPAStatus)
}

func TestHackerNews_Search_HappyPath(t *testing.T) {
	t.Parallel()
	hits := []hackerNewsHit{
		{
			ObjectID:    hackerNewsTestObjectID,
			Title:       hackerNewsTestTitle,
			URL:         hackerNewsTestStoryURL,
			Author:      "alice",
			Points:      hnIntPtr(120),
			NumComments: hnIntPtr(45),
			CreatedAtI:  1700000000,
			CreatedAt:   "2023-11-14T22:13:20.000Z",
			Tags:        []string{"story", "show_hn", "author_alice"},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, hackerNewsSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "rust async", q.Get(hackerNewsQueryParamQuery))
		assert.Equal(t, hackerNewsStoryTagDefault, q.Get(hackerNewsQueryParamTags))
		assert.Equal(t, "25", q.Get(hackerNewsQueryParamHitsPerPage))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildHackerNewsTestResponse(hits, 1))
	}))
	defer srv.Close()

	p := newHackerNewsTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{
		Query: "rust async",
		Limit: 25,
	})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, "hackernews:39000123", pub.ID)
	assert.Equal(t, SourceHackerNews, pub.Source)
	assert.Equal(t, hackerNewsTestTitle, pub.Title)
	assert.Equal(t, hackerNewsTestStoryURL, pub.URL)
	assert.Equal(t, "2023-11-14", pub.Published)
	// author_alice tag should be filtered; story + show_hn retained.
	assert.ElementsMatch(t, []string{"story", "show_hn"}, pub.Categories)
	assert.Equal(t, "hackernews:39000123", pub.SourceMetadata[MetaKeyQAQuestionID])
	assert.Equal(t, hackerNewsSiteForDedup, pub.SourceMetadata[smetaQASite])
	assert.Equal(t, 120, pub.SourceMetadata[smetaQAScore])
	assert.Equal(t, 45, pub.SourceMetadata[smetaQAAnswerCount])
	assert.Equal(t, hackerNewsTestStoryURL, pub.SourceMetadata[smetaExternalURL])
	assert.Equal(t, "https://news.ycombinator.com/item?id=39000123", pub.SourceMetadata[smetaPlatformURL])
	assert.Equal(t, "https://news.ycombinator.com/user?id=alice", pub.SourceMetadata[smetaAuthorURL])
}

func TestHackerNews_Search_HonoursDateAndSort(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, hackerNewsSearchByDatePath, r.URL.Path)
		q := r.URL.Query()
		nf := q.Get(hackerNewsQueryParamNumericFilters)
		assert.Contains(t, nf, "created_at_i>=1704067200")
		assert.Contains(t, nf, "created_at_i<=1735603200")
		_, _ = io.WriteString(w, buildHackerNewsTestResponse(nil, 0))
	}))
	defer srv.Close()

	p := newHackerNewsTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Sort:  SortDateDesc,
		Filters: SearchFilters{
			DateFrom: "2024-01-01",
			DateTo:   "2024-12-31",
		},
	})
	require.NoError(t, err)
}

func TestHackerNews_Search_MissingTitleFallsBackToBody(t *testing.T) {
	t.Parallel()
	hits := []hackerNewsHit{
		{
			ObjectID:   "1",
			StoryText:  "<p>Long story body that becomes the title when no title field present.</p>",
			CreatedAtI: 1700000000,
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, buildHackerNewsTestResponse(hits, 1))
	}))
	defer srv.Close()

	p := newHackerNewsTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	assert.Contains(t, res.Results[0].Title, "Long story body")
	// No outbound URL → URL falls back to the item page.
	assert.Equal(t, "https://news.ycombinator.com/item?id=1", res.Results[0].URL)
}

func TestHackerNews_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := newHackerNewsTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestHackerNews_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &HackerNewsPlugin{}
	_, err := p.Get(context.Background(), "hackernews:1", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestHackerNews_FilterAuthorTags(t *testing.T) {
	t.Parallel()
	out := filterAuthorTags([]string{"story", "ask_hn", "author_pg", "front_page"})
	assert.ElementsMatch(t, []string{"story", "ask_hn", "front_page"}, out)
}

func TestHackerNews_ShortDate_PrefersUnix(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "2023-11-14", hackerNewsShortDate(1700000000, "2099-01-01T00:00:00Z"))
	assert.Equal(t, "2024-06-01", hackerNewsShortDate(0, "2024-06-01T12:00:00Z"))
	assert.Equal(t, "", hackerNewsShortDate(0, ""))
	assert.Equal(t, "", hackerNewsShortDate(0, "garbage"))
}

// TestHackerNews_SortDateAscFallsThroughToDesc pins the documented
// limitation that Algolia's search_by_date endpoint is descending-only
// — SortDateAsc still routes there and returns desc order.
func TestHackerNews_SortDateAscFallsThroughToDesc(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, hackerNewsSearchByDatePath, r.URL.Path)
		_, _ = io.WriteString(w, buildHackerNewsTestResponse(nil, 0))
	}))
	defer srv.Close()

	p := newHackerNewsTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Sort: SortDateAsc})
	require.NoError(t, err)
}

func TestHackerNews_DedupAcrossSitesIsImpossible(t *testing.T) {
	t.Parallel()
	// Stack Overflow #1 and HN #1 share a raw question ID but the
	// namespaced dedup key MUST differ — no cross-site merge.
	se := stackExchangeQuestionToPublication(stackExchangeQuestion{
		QuestionID: 1, Title: "se", Owner: stackExchangeOwner{DisplayName: "x"},
	}, stackExchangeTestSite)
	hn := hackerNewsHitToPublication(hackerNewsHit{
		ObjectID: "1", Title: "hn",
	})
	merged := dedup([]Publication{se, hn})
	require.Len(t, merged, 2, "two distinct sites with same numeric ID must not dedup")
}
