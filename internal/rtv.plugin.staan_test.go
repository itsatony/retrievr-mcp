package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v2.29.0: Staan (EU web search) provider tests. Fixtures follow the
// SearchWebResponse schema at https://docs.staan.ai/api/~schemas.
// ---------------------------------------------------------------------------

const (
	staanTestServerKey  = "staan-server-key"
	staanTestPerCallKey = "staan-per-call-key"
	staanTestSearchID   = "01928f3e-6b1c-7d2a-9c4e-0123456789ab"
)

func newStaanTestPlugin(t *testing.T, baseURL, apiKey string, extra map[string]string) *StaanPlugin {
	t.Helper()
	p := &StaanPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		RateLimit: 10,
		Extra:     map[string]string{},
	}
	for k, v := range extra {
		cfg.Extra[k] = v
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

// staanFixtureResults builds n documented-shape results.
func staanFixtureResults(n int) []staanResult {
	out := make([]staanResult, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, staanResult{
			Title:      fmt.Sprintf("Result %d", i),
			URL:        fmt.Sprintf("https://example.eu/page/%d", i),
			Snippet:    fmt.Sprintf("snippet %d", i),
			DisplayURL: fmt.Sprintf("example.eu > page > %d", i),
			Hostname:   "example.eu",
		})
	}
	return out
}

func staanFixtureBody(results []staanResult, market string, offset int) string {
	resp := staanSearchResponse{
		SearchID: staanTestSearchID,
		Query:    staanQuery{Q: "q", Market: market, Count: len(results), Offset: offset},
		Web:      staanWebBlock{Results: results},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// staanServer returns a server that records the decoded request body and
// answers with the given status + body.
func staanServer(t *testing.T, status int, body string, seen *staanSearchRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			require.NoError(t, json.NewDecoder(r.Body).Decode(seen))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStaan_Identity(t *testing.T) {
	t.Parallel()
	p := &StaanPlugin{}
	assert.Equal(t, "staan", p.ID())
	assert.Equal(t, "Staan", p.Name())
	assert.NotEmpty(t, p.Description())
	assert.Equal(t, []ContentType{ContentTypeAny}, p.ContentTypes())
	assert.Equal(t, FormatJSON, p.NativeFormat())
	assert.Contains(t, p.AvailableFormats(), FormatMarkdown)
}

func TestStaan_Capabilities(t *testing.T) {
	t.Parallel()
	caps := (&StaanPlugin{}).Capabilities()
	assert.Equal(t, []ResultKind{KindWeb}, caps.Kinds)
	assert.ElementsMatch(t, []Intent{IntentQuickLookup, IntentReference, IntentDeepResearch}, caps.QueryIntents)
	assert.NotContains(t, caps.QueryIntents, IntentNews, "Staan documents no freshness/date filter")
	assert.False(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsDomainFilter)
	assert.True(t, caps.SupportsPagination)
	assert.True(t, caps.RequiresCredential)
	assert.Equal(t, staanPageSize, caps.MaxResultsPerQuery)
}

func TestStaan_Residency_IsEUWithoutASignedDPA(t *testing.T) {
	t.Parallel()
	tag := (&StaanPlugin{}).Residency()
	assert.Equal(t, RegionEU, tag.Region)
	assert.True(t, tag.Region.IsEU(), "Staan must pass the eu_strict gate")
	assert.Equal(t, DPAUnknown, tag.DPAStatus, "the DPA is offered on request; none is signed")
	assert.Equal(t, staanSubprocessorURL, tag.SubprocessorURL)
	assert.Equal(t, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), tag.LastVerifiedAt)
}

func TestStaan_Initialize_Defaults(t *testing.T) {
	t.Parallel()
	p := &StaanPlugin{}
	require.NoError(t, p.Initialize(context.Background(), PluginConfig{Enabled: true}))
	assert.Equal(t, staanDefaultBaseURL, p.baseURL)
	assert.Equal(t, staanDefaultMarket, p.market)
	assert.InDelta(t, staanDefaultRPS, p.rateLimit, 0)
	assert.False(t, p.extraSnippets)
	assert.Empty(t, p.fullContent)
	h := p.Health(context.Background())
	assert.True(t, h.Enabled)
	assert.True(t, h.Healthy)
}

func TestStaan_Initialize_RejectsBadExtra(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]string{
		"full_content": {staanExtraFullContent: "pdf"},
		"max_snippets": {staanExtraMaxSnippets: "11"},
		"max_nonnum":   {staanExtraMaxSnippets: "many"},
		"min_score":    {staanExtraMinScore: "1.5"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := (&StaanPlugin{}).Initialize(context.Background(), PluginConfig{Extra: extra})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidInput)
		})
	}
}

func TestStaan_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newStaanTestPlugin(t, "http://127.0.0.1:1", "", nil)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrCredentialRequired)
	assert.Equal(t, SourceErrCredentialRequired, ClassifySourceError(err))
}

func TestStaan_Search_HappyPath(t *testing.T) {
	t.Parallel()
	results := staanFixtureResults(2)
	results[0].PublishedDate = "2024-08-25T15:00:00Z"
	results[0].FaviconURL = "https://example.eu/favicon.ico"
	results[0].Thumbnail = "https://example.eu/thumb.jpg"
	results = append(results, staanResult{Title: "no url"}) // dropped

	var gotPath, gotAuth, gotMethod string
	var seen staanSearchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get(staanAuthHeader), r.Method
		require.NoError(t, json.NewDecoder(r.Body).Decode(&seen))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated) // documented POST success status
		resp := staanSearchResponse{
			SearchID: staanTestSearchID,
			Query:    staanQuery{Q: "eu search", AlteredQuery: "eu search engine", Market: "de-de", Count: 3},
			Web:      staanWebBlock{Results: results},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, nil)
	got, err := p.Search(context.Background(), SearchParams{
		Query:   "  eu search ",
		Limit:   5,
		Filters: SearchFilters{Language: "de", IncludeDomains: []string{"example.eu"}},
	})
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, staanSearchPath, gotPath)
	assert.Equal(t, staanAuthScheme+staanTestServerKey, gotAuth)
	assert.Equal(t, "eu search", seen.Q)
	assert.Equal(t, "de-de", seen.Market, "language de must select the de-de market")
	assert.Equal(t, 0, seen.Offset)
	assert.Equal(t, []string{"example.eu"}, seen.IncludeDomains)
	assert.False(t, seen.ExtraSnippets)
	assert.Empty(t, seen.FullContent)

	require.Len(t, got.Results, 2, "a result without a url is dropped")
	assert.Equal(t, 2, got.Total)
	assert.False(t, got.HasMore, "a short page is the last page")

	first := got.Results[0]
	assert.True(t, strings.HasPrefix(first.ID, staanIDPrefix))
	assert.Equal(t, SourceStaan, first.Source)
	assert.Equal(t, "Result 0", first.Title)
	assert.Equal(t, "https://example.eu/page/0", first.URL)
	assert.Equal(t, "snippet 0", first.Abstract)
	assert.Equal(t, "2024-08-25", first.Published)
	assert.Equal(t, "https://example.eu/thumb.jpg", first.ThumbnailURL)
	assert.Equal(t, "example.eu", first.SourceMetadata[smetaDomain])
	assert.Equal(t, "2024-08-25T15:00:00Z", first.SourceMetadata[smetaPublishedAt])
	assert.Equal(t, staanTestSearchID, first.SourceMetadata[staanMetaSearchID])
	assert.Equal(t, "eu search engine", first.SourceMetadata[staanMetaAlteredQuery])
	assert.Equal(t, "de-de", first.SourceMetadata[staanMetaMarket])
	assert.Equal(t, "https://example.eu/favicon.ico", first.SourceMetadata[staanMetaFaviconURL])
	assert.Equal(t, "example.eu > page > 0", first.SourceMetadata[staanMetaDisplayURL])
	assert.Nil(t, first.FullText)

	assert.True(t, p.Health(context.Background()).Healthy)
}

func TestStaan_Search_EnrichmentIsMapped(t *testing.T) {
	t.Parallel()
	results := staanFixtureResults(2)
	results[0].ExtraSnippets = []staanChunk{{Chunk: "chunk a", Score: 0.9}}
	results[0].FullContent = &staanFullContentWS{Text: "# Page", Format: "markdown", Length: 6}
	results[1].FullContent = &staanFullContentWS{Text: "", Format: "markdown", Length: 0} // failed fetch

	var seen staanSearchRequest
	srv := staanServer(t, http.StatusCreated, staanFixtureBody(results, "en-us", 0), &seen)
	p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, map[string]string{
		staanExtraExtraSnippets: "true",
		staanExtraFullContent:   "markdown",
		staanExtraMaxSnippets:   "5",
		staanExtraMinScore:      "0.3",
		staanExtraMarket:        "FR-FR",
	})
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Filters: SearchFilters{Language: "pt-BR"}})
	require.NoError(t, err)

	assert.True(t, seen.ExtraSnippets)
	assert.Equal(t, staanFullContentMarkdown, seen.FullContent)
	assert.Equal(t, 5, seen.MaxSnippets)
	require.NotNil(t, seen.MinScore)
	assert.InDelta(t, 0.3, *seen.MinScore, 1e-9)
	assert.Equal(t, "fr-fr", seen.Market, "an undocumented language falls back to the configured market")

	require.Len(t, got.Results, 2)
	require.NotNil(t, got.Results[0].FullText)
	assert.Equal(t, "# Page", got.Results[0].FullText.Content)
	assert.Equal(t, FormatMarkdown, got.Results[0].FullText.ContentFormat)
	chunks, ok := got.Results[0].SourceMetadata[staanMetaExtraSnippets].([]map[string]any)
	require.True(t, ok)
	assert.Equal(t, "chunk a", chunks[0][staanMetaChunk])
	assert.Nil(t, got.Results[1].FullText, "length 0 means the page fetch failed")
}

func TestStaan_Search_HTMLFullContentIsNative(t *testing.T) {
	t.Parallel()
	results := staanFixtureResults(1)
	results[0].FullContent = &staanFullContentWS{Text: "<p>x</p>", Format: "html", Length: 8}
	srv := staanServer(t, http.StatusOK, staanFixtureBody(results, "en-us", 0), nil)
	p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, map[string]string{staanExtraFullContent: "html"})
	got, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.NoError(t, err)
	require.NotNil(t, got.Results[0].FullText)
	assert.Equal(t, FormatNative, got.Results[0].FullText.ContentFormat)
}

func TestStaan_Search_PaginationAndLimit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		offset     int
		limit      int
		wantOffset int
		wantFirst  string
		wantLen    int
		wantMore   bool
	}{
		{"first page", 0, 0, 0, "Result 0", 10, true},
		{"limit truncates", 0, 3, 0, "Result 0", 3, true},
		{"aligned page", 20, 0, 20, "Result 0", 10, true},
		{"unaligned offset drops leading rows", 15, 0, 10, "Result 5", 5, true},
		{"last page has no more", 30, 0, 30, "Result 0", 10, false},
		{"negative offset is zero", -4, 0, 0, "Result 0", 10, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var seen staanSearchRequest
			srv := staanServer(t, http.StatusCreated, staanFixtureBody(staanFixtureResults(10), "en-us", tc.wantOffset), &seen)
			p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, nil)
			got, err := p.Search(context.Background(), SearchParams{Query: "x", Offset: tc.offset, Limit: tc.limit})
			require.NoError(t, err)
			assert.Equal(t, tc.wantOffset, seen.Offset)
			require.Len(t, got.Results, tc.wantLen)
			assert.Equal(t, tc.wantFirst, got.Results[0].Title)
			assert.Equal(t, tc.wantMore, got.HasMore)
		})
	}
}

func TestStaan_Search_OffsetPastLastPageMakesNoRequest(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, nil)
	got, err := p.Search(context.Background(), SearchParams{Query: "x", Offset: 40})
	require.NoError(t, err)
	assert.Empty(t, got.Results)
	assert.False(t, got.HasMore)
	assert.Zero(t, calls.Load())
}

func TestStaan_Search_RequestValidation(t *testing.T) {
	t.Parallel()
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = fmt.Sprintf("d%d.eu", i)
	}
	cases := map[string]struct {
		params SearchParams
		want   error
	}{
		"empty query":      {SearchParams{Query: "  "}, ErrInvalidInput},
		"query too long":   {SearchParams{Query: strings.Repeat("a", staanMaxQueryChars+1)}, ErrInvalidInput},
		"both domain sets": {SearchParams{Query: "x", Filters: SearchFilters{IncludeDomains: []string{"a.eu"}, ExcludeDomains: []string{"b.eu"}}}, ErrInvalidDomainList},
		"eleven domains":   {SearchParams{Query: "x", Filters: SearchFilters{ExcludeDomains: eleven}}, ErrInvalidDomainList},
		"bad domain":       {SearchParams{Query: "x", Filters: SearchFilters{IncludeDomains: []string{"https://a.eu"}}}, ErrInvalidDomainList},
		"bad exclude":      {SearchParams{Query: "x", Filters: SearchFilters{ExcludeDomains: []string{""}}}, ErrInvalidDomainList},
		"bad language":     {SearchParams{Query: "x", Filters: SearchFilters{Language: "not a tag!"}}, ErrInvalidLanguageTag},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newStaanTestPlugin(t, "http://127.0.0.1:1", staanTestServerKey, nil)
			_, err := p.Search(context.Background(), tc.params)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, SourceErrInvalidRequest, ClassifySourceError(err))
			assert.True(t, IsPermanentError(err))
		})
	}
}

func TestStaan_Search_ExactlyFourHundredCharsIsAccepted(t *testing.T) {
	t.Parallel()
	srv := staanServer(t, http.StatusCreated, staanFixtureBody(nil, "en-us", 0), nil)
	p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, nil)
	_, err := p.Search(context.Background(), SearchParams{Query: strings.Repeat("ä", staanMaxQueryChars)})
	require.NoError(t, err, "the limit counts characters, not bytes")
}

func TestStaan_ResolveMarket(t *testing.T) {
	t.Parallel()
	p := newStaanTestPlugin(t, "", staanTestServerKey, map[string]string{staanExtraMarket: "de-de"})
	cases := map[string]string{
		"":      "de-de",
		"en":    "en-us",
		"en-GB": "en-gb",
		"en-NZ": "en-us",
		"fr-CA": "fr-fr",
		"de-AT": "de-de",
		"it":    "de-de",
	}
	for lang, want := range cases {
		assert.Equal(t, want, p.resolveMarket(lang), lang)
	}
}

func TestStaan_Search_ErrorClasses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		status    int
		body      string
		code      SourceErrorCode
		permanent bool
	}{
		{"401 observed live body", http.StatusUnauthorized, `{"message":"Unauthorized","statusCode":401}`, SourceErrCredentialInvalid, true},
		{"403", http.StatusForbidden, `{"message":"Forbidden","statusCode":403}`, SourceErrCredentialInvalid, true},
		{"402", http.StatusPaymentRequired, `{"message":"Payment Required","statusCode":402}`, SourceErrQuotaExhausted, true},
		{"429 throttle", http.StatusTooManyRequests, `{"message":"Too Many Requests","statusCode":429}`, SourceErrRateLimited, false},
		{"429 monthly quota", http.StatusTooManyRequests, `{"message":"Monthly quota exceeded","statusCode":429}`, SourceErrQuotaExhausted, true},
		{"403 credits", http.StatusForbidden, `{"message":"No credits left","statusCode":403}`, SourceErrQuotaExhausted, true},
		{"400", http.StatusBadRequest, `{"message":"q must be shorter than 400 characters","statusCode":400}`, SourceErrInvalidRequest, true},
		{"422", http.StatusUnprocessableEntity, `{"message":"invalid market","statusCode":422}`, SourceErrInvalidRequest, true},
		{"500", http.StatusInternalServerError, `{"message":"Internal Server Error","statusCode":500}`, SourceErrUpstream, false},
		{"503", http.StatusServiceUnavailable, ``, SourceErrUpstream, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := staanServer(t, tc.status, tc.body, nil)
			p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, nil)
			_, err := p.Search(context.Background(), SearchParams{Query: "x"})
			require.Error(t, err)
			assert.Equal(t, tc.code, ClassifySourceError(err))
			assert.Equal(t, tc.permanent, IsPermanentError(err), "permanent")
			h := p.Health(context.Background())
			assert.False(t, h.Healthy)
			assert.NotEmpty(t, h.LastError)
		})
	}
}

func TestStaan_Search_QuotaIsTyped(t *testing.T) {
	t.Parallel()
	srv := staanServer(t, http.StatusPaymentRequired, `{}`, nil)
	p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, nil)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	var qe *QuotaExhaustedError
	require.True(t, errors.As(err, &qe))
	assert.Equal(t, SourceStaan, qe.Source)
	assert.Equal(t, "http_402", qe.Reason)
}

func TestStaan_Search_MalformedJSON(t *testing.T) {
	t.Parallel()
	srv := staanServer(t, http.StatusCreated, `{not json`, nil)
	p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, nil)
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
	assert.Equal(t, SourceErrUpstream, ClassifySourceError(err))
}

func TestStaan_Search_PerCallCredentialOverridesServerKey(t *testing.T) {
	t.Parallel()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get(staanAuthHeader)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, staanFixtureBody(nil, "en-us", 0))
	}))
	defer srv.Close()
	p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, nil)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{SourceStaan: staanTestPerCallKey})
	_, err := p.Search(ctx, SearchParams{Query: "x"})
	require.NoError(t, err)
	assert.Equal(t, staanAuthScheme+staanTestPerCallKey, gotAuth)
}

func TestStaan_Search_Timeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	p := &StaanPlugin{}
	require.NoError(t, p.Initialize(context.Background(), PluginConfig{
		Enabled: true, BaseURL: srv.URL, APIKey: staanTestServerKey,
		Timeout: Duration{Duration: 50 * time.Millisecond},
	}))
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.Equal(t, SourceErrTimeout, ClassifySourceError(err))
}

func TestStaan_Search_Canceled(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	p := newStaanTestPlugin(t, srv.URL, staanTestServerKey, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := p.Search(ctx, SearchParams{Query: "x"})
	require.Error(t, err)
	assert.Equal(t, SourceErrCanceled, ClassifySourceError(err))
}

func TestStaan_Get_Unsupported(t *testing.T) {
	t.Parallel()
	_, err := (&StaanPlugin{}).Get(context.Background(), "staan:abc", nil, FormatJSON)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGetUnsupported)
}

func TestStaan_IsRegisteredAndCatalogued(t *testing.T) {
	t.Parallel()
	assert.True(t, IsValidSourceID(SourceStaan))
	f, ok := PluginFactories()[SourceStaan]
	require.True(t, ok)
	_, isStaan := f().(*StaanPlugin)
	assert.True(t, isStaan)
	assert.Equal(t, CategoryWeb, sourceCategories[SourceStaan])
	assert.Equal(t, SourceStaan, DefaultFallbackConfig().Chains[fallbackChainQuickLookup].Primary[0])
}

// ---------------------------------------------------------------------------
// Live smoke (gated on RETRIEVR_STAAN_API_KEY). Unset → SKIPPED, never passed.
// ---------------------------------------------------------------------------

func TestStaan_LiveSmoke(t *testing.T) {
	apiKey := os.Getenv("RETRIEVR_STAAN_API_KEY")
	if apiKey == "" {
		t.Skip("SKIPPED (not passed): RETRIEVR_STAAN_API_KEY is not set; the live Staan smoke did not run")
	}
	p := newStaanTestPlugin(t, "", apiKey, nil)
	got, err := p.Search(context.Background(), SearchParams{Query: "European search engine", Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, got.Results, "live Staan search must return at least one result")
	for _, r := range got.Results {
		t.Logf("hit: id=%s title=%q url=%s", r.ID, r.Title, r.URL)
		assert.True(t, strings.HasPrefix(r.ID, staanIDPrefix))
		assert.NotEmpty(t, r.URL)
	}
}
