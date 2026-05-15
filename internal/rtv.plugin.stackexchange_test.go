package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v5 cycle 1 / v2.8.0 — Stack Exchange Q&A tests.
// ---------------------------------------------------------------------------

const (
	stackExchangeTestSite  = "stackoverflow"
	stackExchangeTestQID   = int64(78901234)
	stackExchangeTestTitle = "How do I scope a kubernetes ingress to a namespace?"
	stackExchangeTestQURL  = "https://stackoverflow.com/questions/78901234/how-do-i-scope-a-kubernetes-ingress-to-a-namespace"
)

func newStackExchangeTestPlugin(t *testing.T, baseURL string) *StackExchangePlugin {
	t.Helper()
	p := &StackExchangePlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		RateLimit: 100,
		Extra:     map[string]string{stackExchangeExtraDefaultSite: stackExchangeTestSite},
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildStackExchangeTestResponse(items []stackExchangeQuestion, hasMore bool) string {
	b, _ := json.Marshal(stackExchangeSearchResponse{Items: items, HasMore: hasMore})
	return string(b)
}

func TestStackExchange_Identity(t *testing.T) {
	t.Parallel()
	p := &StackExchangePlugin{}
	assert.Equal(t, SourceStackExchange, p.ID())
	assert.Equal(t, stackExchangePluginName, p.Name())
}

func TestStackExchange_Capabilities(t *testing.T) {
	t.Parallel()
	p := &StackExchangePlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindQA)
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsCategoryFilter)
	assert.True(t, caps.SupportsSortDate)
	assert.Equal(t, stackExchangeMaxLimitCap, caps.MaxResultsPerQuery)
	assert.False(t, caps.SupportsLanguageFilter)
}

func TestStackExchange_Residency(t *testing.T) {
	t.Parallel()
	p := &StackExchangePlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.Equal(t, DPACoveredBySCC, tag.DPAStatus)
}

func TestStackExchange_Search_HappyPath(t *testing.T) {
	t.Parallel()
	items := []stackExchangeQuestion{
		{
			QuestionID:       stackExchangeTestQID,
			Title:            stackExchangeTestTitle,
			Body:             "<p>I want my ingress to only route within ns=foo.</p>",
			Link:             stackExchangeTestQURL,
			Tags:             []string{"kubernetes", "ingress"},
			Score:            42,
			AnswerCount:      3,
			IsAnswered:       true,
			AcceptedAnswerID: 78901235,
			CreationDate:     1700000000,
			LastActivityDate: 1700000500,
			Owner:            stackExchangeOwner{UserID: 1, DisplayName: "k8s-fan", Link: "https://stackoverflow.com/users/1/k8s-fan"},
			ContentLicense:   "CC BY-SA 4.0",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, stackExchangeSearchPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "kubernetes ingress", q.Get(stackExchangeQueryParamQ))
		assert.Equal(t, stackExchangeTestSite, q.Get(stackExchangeQueryParamSite))
		assert.Equal(t, "25", q.Get(stackExchangeQueryParamPageSize))
		assert.Equal(t, stackExchangeFilterWithBody, q.Get(stackExchangeQueryParamFilter))
		assert.Equal(t, "kubernetes;ingress", q.Get(stackExchangeQueryParamTagged))
		assert.Equal(t, stackExchangeSortRelevance, q.Get(stackExchangeQueryParamSort))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildStackExchangeTestResponse(items, false))
	}))
	defer srv.Close()

	p := newStackExchangeTestPlugin(t, srv.URL)
	res, err := p.Search(context.Background(), SearchParams{
		Query: "kubernetes ingress",
		Limit: 25,
		Filters: SearchFilters{
			Categories: []string{"kubernetes", "ingress"},
		},
	})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, "stackexchange:stackoverflow:78901234", pub.ID)
	assert.Equal(t, SourceStackExchange, pub.Source)
	assert.Equal(t, stackExchangeTestTitle, pub.Title)
	assert.Equal(t, stackExchangeTestQURL, pub.URL)
	assert.Equal(t, "2023-11-14", pub.Published) // unix 1700000000 UTC
	assert.Equal(t, "CC BY-SA 4.0", pub.License)
	assert.Contains(t, pub.Categories, "kubernetes")
	// Abstract is HTML-stripped.
	assert.Contains(t, pub.Abstract, "ingress to only route")
	assert.NotContains(t, pub.Abstract, "<p>")
	// Dedup key namespaced by site.
	assert.Equal(t, "stackoverflow:78901234", pub.SourceMetadata[MetaKeyQAQuestionID])
	assert.Equal(t, stackExchangeTestSite, pub.SourceMetadata[smetaQASite])
	assert.Equal(t, 42, pub.SourceMetadata[smetaQAScore])
	assert.Equal(t, 3, pub.SourceMetadata[smetaQAAnswerCount])
	assert.Equal(t, true, pub.SourceMetadata[smetaQAIsAnswered])
	assert.Equal(t, "78901235", pub.SourceMetadata[smetaQAAcceptedAnswerID])
}

func TestStackExchange_Search_HonoursDateAndSort(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// 2024-01-01 → 1704067200, 2024-12-31 → 1735603200
		assert.Equal(t, "1704067200", q.Get(stackExchangeQueryParamFromDate))
		assert.Equal(t, "1735603200", q.Get(stackExchangeQueryParamToDate))
		assert.Equal(t, stackExchangeSortCreation, q.Get(stackExchangeQueryParamSort))
		assert.Equal(t, stackExchangeOrderDesc, q.Get(stackExchangeQueryParamOrder))
		_, _ = io.WriteString(w, buildStackExchangeTestResponse(nil, false))
	}))
	defer srv.Close()

	p := newStackExchangeTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{
		Query: "foo",
		Sort:  SortDateDesc,
		Filters: SearchFilters{
			DateFrom: "2024-01-01",
			DateTo:   "2024-12-31",
		},
	})
	require.NoError(t, err)
}

func TestStackExchange_Search_ThrottleEnvelopeMapsToRateLimit(t *testing.T) {
	t.Parallel()
	envelope := stackExchangeSearchResponse{
		ErrorID:      502,
		ErrorName:    "throttle_violation",
		ErrorMessage: "too many requests",
		Backoff:      30,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(envelope)
	}))
	defer srv.Close()

	p := newStackExchangeTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestStackExchange_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := newStackExchangeTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestStackExchange_Search_HTTPErrorWrapsBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "bad input")
	}))
	defer srv.Close()

	p := newStackExchangeTestPlugin(t, srv.URL)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestStackExchange_Search_APIKeyFromContextOverridesConfig(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "per-call-key", r.URL.Query().Get(stackExchangeQueryParamKey))
		_, _ = io.WriteString(w, buildStackExchangeTestResponse(nil, false))
	}))
	defer srv.Close()

	p := &StackExchangePlugin{}
	require.NoError(t, p.Initialize(context.Background(), PluginConfig{
		Enabled: true, BaseURL: srv.URL, RateLimit: 100,
		APIKey: "config-key",
		Extra:  map[string]string{stackExchangeExtraDefaultSite: stackExchangeTestSite},
	}))
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceStackExchange: "per-call-key",
	})
	_, err := p.Search(ctx, SearchParams{Query: "x"})
	require.NoError(t, err)
}

func TestStackExchange_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &StackExchangePlugin{}
	_, err := p.Get(context.Background(), "stackexchange:stackoverflow:1", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestStackExchange_StripHTML_CollapsesWhitespace(t *testing.T) {
	t.Parallel()
	out := stackExchangeStripHTML("<p>foo</p>\n\n<p>bar  baz</p>")
	assert.Equal(t, "foo bar baz", out)
	assert.Equal(t, "", stackExchangeStripHTML(""))
}

func TestStackExchange_UnixFromDate(t *testing.T) {
	t.Parallel()
	v, ok := parseFilterDateUnix("2024-01-01")
	assert.True(t, ok)
	assert.Equal(t, int64(1704067200), v)

	v, ok = parseFilterDateUnix("2024")
	assert.True(t, ok)
	assert.Equal(t, int64(1704067200), v)

	_, ok = parseFilterDateUnix("")
	assert.False(t, ok)
	_, ok = parseFilterDateUnix("not-a-date")
	assert.False(t, ok)
}

func TestStackExchange_NormalizeLicense(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "CC BY-SA 4.0", stackExchangeNormalizeLicense("cc by-sa 4.0"))
	assert.Equal(t, "CC BY-SA 3.0", stackExchangeNormalizeLicense("CC_BY_SA_3_0"))
	assert.Equal(t, "", stackExchangeNormalizeLicense(""))
	// Unknown values pass through.
	assert.Equal(t, "weird", stackExchangeNormalizeLicense("weird"))
}

func TestStackExchange_QAResultConversion(t *testing.T) {
	t.Parallel()
	// End-to-end: Publication → Result must surface KindQA + QA block.
	q := stackExchangeQuestion{
		QuestionID:   1,
		Title:        "title",
		Tags:         []string{"a", "b"},
		Score:        10,
		AnswerCount:  2,
		IsAnswered:   true,
		CreationDate: 1700000000,
		Owner:        stackExchangeOwner{DisplayName: "u"},
	}
	pub := stackExchangeQuestionToPublication(q, stackExchangeTestSite)

	r := newRouterForTest(t)
	r.plugins[SourceStackExchange] = &StackExchangePlugin{}
	res := r.toResult(pub, 0)
	assert.Equal(t, KindQA, res.Kind)
	require.NotNil(t, res.QA)
	assert.Equal(t, stackExchangeTestSite, res.QA.Site)
	assert.Equal(t, []string{"a", "b"}, res.QA.Tags)
	require.NotNil(t, res.QA.Score)
	assert.Equal(t, 10, *res.QA.Score)
	require.NotNil(t, res.QA.AnswerCount)
	assert.Equal(t, 2, *res.QA.AnswerCount)
	require.NotNil(t, res.QA.IsAnswered)
	assert.True(t, *res.QA.IsAnswered)
	assert.Equal(t, "u", res.QA.AuthorHandle)
}

// newRouterForTest returns a minimal Router with a plugins map ready for
// per-source lookups (Residency, Capabilities) in unit tests.
func newRouterForTest(t *testing.T) *Router {
	t.Helper()
	return &Router{plugins: map[string]SourcePlugin{}}
}

// TestStackExchange_DedupWithinSiteMerges asserts the positive side of the
// QA dedup family: two SE results sharing site+question_id merge into one.
// Complements TestHackerNews_DedupAcrossSitesIsImpossible (negative case).
func TestStackExchange_DedupWithinSiteMerges(t *testing.T) {
	t.Parallel()
	q := stackExchangeQuestion{
		QuestionID: 99,
		Title:      "same Q from two SE fan-out workers",
		Owner:      stackExchangeOwner{DisplayName: "x"},
	}
	a := stackExchangeQuestionToPublication(q, stackExchangeTestSite)
	b := stackExchangeQuestionToPublication(q, stackExchangeTestSite)
	merged := dedup([]Publication{a, b})
	require.Len(t, merged, 1, "two SE results with the same namespaced qa_question_id must merge into one")
}

// TestStackExchange_LicenseDocumentsCCBYSA ensures the plugin description
// flags the CC-BY-SA licensing so consumers know attribution is required.
func TestStackExchange_DescriptionMentionsLicense(t *testing.T) {
	t.Parallel()
	p := &StackExchangePlugin{}
	desc := strings.ToLower(p.Description())
	assert.Contains(t, desc, "cc-by-sa")
}
