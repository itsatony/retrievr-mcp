package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

const (
	testOAWorkID1    = "W2741809807"
	testOAWorkURL1   = "https://openalex.org/W2741809807"
	testOAWorkURL2   = "https://openalex.org/W3045685553"
	testOATitle1     = "Attention Is All You Need"
	testOATitle2     = "BERT: Pre-training of Deep Bidirectional Transformers"
	testOAAuthor1    = "Ashish Vaswani"
	testOAAuthor2    = "Jacob Devlin"
	testOAORCID1     = "https://orcid.org/0000-0001-2345-6789"
	testOAORCIDBare  = "0000-0001-2345-6789"
	testOAAffil1     = "Google Brain"
	testOAAffil2     = "Google AI Language"
	testOADOIFull1   = "https://doi.org/10.1234/oatest.2024.001"
	testOADOIBare1   = "10.1234/oatest.2024.001"
	testOADOIFull2   = "https://doi.org/10.5678/oatest.2023.002"
	testOADate1      = "2024-01-15"
	testOADate2      = "2023-06-01"
	testOAYear1      = 2024
	testOAYear2      = 2023
	testOACitations1 = 95000
	testOACitations2 = 42000
	testOAPDFURL1    = "https://arxiv.org/pdf/1706.03762"
	testOALanding1   = "https://proceedings.neurips.cc/paper/7181"
	testOAType1      = "article"
	testOAConcept1   = "Computer Science"
	testOAConcept2   = "Artificial Intelligence"
	testOATopic1     = "Transformer Architecture"
	testOAOAStatus1  = "gold"
	testOAVenue1     = "Nature"
	testOALicense1   = "cc-by"

	testOAPluginTimeout        = 5 * time.Second
	testOATotalResults         = 250
	testOAConcurrentGoroutines = 10

	testOAAPIKey = "test-oa-api-key"
	testOAMailto = "test@example.com"
)

// ---------------------------------------------------------------------------
// JSON fixture builder types
// ---------------------------------------------------------------------------

type oaTestWork struct {
	ID               string
	DOI              string
	Title            string
	PubYear          int
	PubDate          string
	Type             string
	CitedByCount     int
	Authors          []oaTestAuthor
	VenueName        string
	PDFURL           string
	LandingPageURL   string
	IsOA             bool
	OAURL            string
	OAStatus         string
	InvertedAbstract map[string][]int
	Concepts         []oaTestConcept
	Topics           []oaTestTopic
	PrimaryTopic     string
	License          string
}

type oaTestAuthor struct {
	Name        string
	ORCID       string
	Institution string
}

type oaTestConcept struct {
	Name  string
	Level int
	Score float64
}

type oaTestTopic struct {
	Name string
}

// ---------------------------------------------------------------------------
// JSON fixture builders
// ---------------------------------------------------------------------------

// buildOATestSearchJSON generates a complete OpenAlex search response JSON string.
func buildOATestSearchJSON(count, page, perPage int, works []oaTestWork) string {
	worksJSON := "[]"
	if len(works) > 0 {
		items := make([]string, 0, len(works))
		for _, w := range works {
			items = append(items, buildOATestWorkJSON(w))
		}
		worksJSON = "[" + joinStrings(items, ",") + "]"
	}

	return fmt.Sprintf(`{"meta":{"count":%d,"page":%d,"per_page":%d},"results":%s}`,
		count, page, perPage, worksJSON)
}

// buildOATestWorkJSON generates a single OpenAlex work JSON object.
func buildOATestWorkJSON(w oaTestWork) string {
	// Build authorships.
	authorshipsJSON := "[]"
	if len(w.Authors) > 0 {
		items := make([]string, 0, len(w.Authors))
		for _, a := range w.Authors {
			instJSON := "[]"
			if a.Institution != "" {
				instJSON = fmt.Sprintf(`[{"id":"https://openalex.org/I1","display_name":%s}]`, jsonString(a.Institution))
			}
			orcidJSON := "null"
			if a.ORCID != "" {
				orcidJSON = jsonString(a.ORCID)
			}
			items = append(items, fmt.Sprintf(`{"author_position":"first","author":{"id":"https://openalex.org/A1","display_name":%s,"orcid":%s},"institutions":%s}`,
				jsonString(a.Name), orcidJSON, instJSON))
		}
		authorshipsJSON = "[" + joinStrings(items, ",") + "]"
	}

	// Build primary_location.
	primaryLocJSON := "null"
	if w.VenueName != "" || w.PDFURL != "" || w.LandingPageURL != "" {
		sourceJSON := "null"
		if w.VenueName != "" {
			sourceJSON = fmt.Sprintf(`{"id":"https://openalex.org/S1","display_name":%s,"type":"journal","issn_l":"1234-5678"}`, jsonString(w.VenueName))
		}
		primaryLocJSON = fmt.Sprintf(`{"source":%s,"pdf_url":%s,"landing_page_url":%s,"is_oa":%t}`,
			sourceJSON, jsonStringOrNull(w.PDFURL), jsonStringOrNull(w.LandingPageURL), w.IsOA)
	}

	// Build open_access.
	oaJSON := "null"
	if w.OAStatus != "" || w.OAURL != "" || w.IsOA {
		oaJSON = fmt.Sprintf(`{"is_oa":%t,"oa_url":%s,"oa_status":%s}`,
			w.IsOA, jsonStringOrNull(w.OAURL), jsonStringOrNull(w.OAStatus))
	}

	// Build abstract_inverted_index.
	abstractJSON := "null"
	if w.InvertedAbstract != nil {
		b, _ := json.Marshal(w.InvertedAbstract)
		abstractJSON = string(b)
	}

	// Build concepts.
	conceptsJSON := "[]"
	if len(w.Concepts) > 0 {
		items := make([]string, 0, len(w.Concepts))
		for _, c := range w.Concepts {
			items = append(items, fmt.Sprintf(`{"id":"https://openalex.org/C1","display_name":%s,"level":%d,"score":%.2f}`,
				jsonString(c.Name), c.Level, c.Score))
		}
		conceptsJSON = "[" + joinStrings(items, ",") + "]"
	}

	// Build topics.
	topicsJSON := "[]"
	if len(w.Topics) > 0 {
		items := make([]string, 0, len(w.Topics))
		for _, t := range w.Topics {
			items = append(items, fmt.Sprintf(`{"id":"https://openalex.org/T1","display_name":%s}`, jsonString(t.Name)))
		}
		topicsJSON = "[" + joinStrings(items, ",") + "]"
	}

	// Build primary_topic.
	primaryTopicJSON := "null"
	if w.PrimaryTopic != "" {
		primaryTopicJSON = fmt.Sprintf(`{"id":"https://openalex.org/T1","display_name":%s}`, jsonString(w.PrimaryTopic))
	}

	doiJSON := jsonStringOrNull(w.DOI)
	licenseJSON := jsonStringOrNull(w.License)

	return fmt.Sprintf(`{`+
		`"id":%s,`+
		`"doi":%s,`+
		`"title":%s,`+
		`"display_name":%s,`+
		`"publication_year":%d,`+
		`"publication_date":%s,`+
		`"type":%s,`+
		`"cited_by_count":%d,`+
		`"authorships":%s,`+
		`"primary_location":%s,`+
		`"open_access":%s,`+
		`"abstract_inverted_index":%s,`+
		`"concepts":%s,`+
		`"topics":%s,`+
		`"primary_topic":%s,`+
		`"biblio":null,`+
		`"ids":null,`+
		`"referenced_works":[],`+
		`"related_works":[],`+
		`"license":%s`+
		`}`,
		jsonString(w.ID),
		doiJSON,
		jsonString(w.Title),
		jsonString(w.Title),
		w.PubYear,
		jsonStringOrNull(w.PubDate),
		jsonStringOrNull(w.Type),
		w.CitedByCount,
		authorshipsJSON,
		primaryLocJSON,
		oaJSON,
		abstractJSON,
		conceptsJSON,
		topicsJSON,
		primaryTopicJSON,
		licenseJSON,
	)
}

// jsonStringOrNull returns a JSON-encoded string or "null" if empty.
func jsonStringOrNull(s string) string {
	if s == "" {
		return "null"
	}
	b, _ := json.Marshal(s)
	return string(b)
}

// ---------------------------------------------------------------------------
// Default test works
// ---------------------------------------------------------------------------

func defaultOATestWork1() oaTestWork {
	return oaTestWork{
		ID:           testOAWorkURL1,
		DOI:          testOADOIFull1,
		Title:        testOATitle1,
		PubYear:      testOAYear1,
		PubDate:      testOADate1,
		Type:         testOAType1,
		CitedByCount: testOACitations1,
		Authors: []oaTestAuthor{
			{Name: testOAAuthor1, ORCID: testOAORCID1, Institution: testOAAffil1},
		},
		VenueName:      testOAVenue1,
		PDFURL:         testOAPDFURL1,
		LandingPageURL: testOALanding1,
		IsOA:           true,
		OAURL:          testOAPDFURL1,
		OAStatus:       testOAOAStatus1,
		InvertedAbstract: map[string][]int{
			"The":       {0},
			"dominant":  {1},
			"sequence":  {2},
			"models":    {3},
			"are":       {4},
			"based":     {5},
			"on":        {6},
			"attention": {7},
		},
		Concepts:     []oaTestConcept{{Name: testOAConcept1, Level: 0, Score: 0.95}, {Name: testOAConcept2, Level: 1, Score: 0.87}},
		Topics:       []oaTestTopic{{Name: testOATopic1}},
		PrimaryTopic: testOATopic1,
		License:      testOALicense1,
	}
}

func defaultOATestWork2() oaTestWork {
	return oaTestWork{
		ID:           testOAWorkURL2,
		DOI:          testOADOIFull2,
		Title:        testOATitle2,
		PubYear:      testOAYear2,
		PubDate:      testOADate2,
		CitedByCount: testOACitations2,
		Authors: []oaTestAuthor{
			{Name: testOAAuthor2, Institution: testOAAffil2},
		},
		InvertedAbstract: map[string][]int{"BERT": {0}, "is": {1}, "great": {2}},
		Concepts:         []oaTestConcept{{Name: testOAConcept1, Level: 0, Score: 0.90}},
	}
}

// ---------------------------------------------------------------------------
// httptest server factory
// ---------------------------------------------------------------------------

// newOATestPlugin creates an OpenAlexPlugin pointing at a custom base URL.
func newOATestPlugin(t *testing.T, baseURL string) *OpenAlexPlugin {
	t.Helper()
	plugin := &OpenAlexPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testOAPluginTimeout},
		RateLimit: 10.0,
	})
	require.NoError(t, err)
	return plugin
}

// newOATestPluginWithAPIKey creates an OpenAlexPlugin with a server-level API key.
func newOATestPluginWithAPIKey(t *testing.T, baseURL, apiKey string) *OpenAlexPlugin {
	t.Helper()
	plugin := &OpenAlexPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		Timeout:   Duration{Duration: testOAPluginTimeout},
		RateLimit: 10.0,
	})
	require.NoError(t, err)
	return plugin
}

// newOATestPluginWithMailto creates an OpenAlexPlugin with a mailto config.
func newOATestPluginWithMailto(t *testing.T, baseURL, mailto string) *OpenAlexPlugin {
	t.Helper()
	plugin := &OpenAlexPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testOAPluginTimeout},
		RateLimit: 10.0,
		Extra:     map[string]string{oaExtraKeyMailto: mailto},
	})
	require.NoError(t, err)
	return plugin
}

// ---------------------------------------------------------------------------
// Contract test
// ---------------------------------------------------------------------------

func TestOpenAlexPluginContract(t *testing.T) {
	plugin := newOATestPlugin(t, "http://unused.test")
	PluginContractTest(t, plugin)
}

// ---------------------------------------------------------------------------
// Search tests
// ---------------------------------------------------------------------------

func TestOASearch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		params         SearchParams
		responseJSON   string
		wantResultsCnt int
		wantHasMore    bool
		wantTotal      int
		wantErr        error
		validateReq    func(t *testing.T, r *http.Request)
	}{
		{
			name: "basic_search",
			params: SearchParams{
				Query: "attention mechanism",
				Limit: 10,
			},
			responseJSON:   buildOATestSearchJSON(testOATotalResults, 1, 10, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      testOATotalResults,
			wantHasMore:    true,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "attention mechanism", r.URL.Query().Get(oaParamSearch))
				assert.Equal(t, "1", r.URL.Query().Get(oaParamPage))
				assert.Equal(t, "10", r.URL.Query().Get(oaParamPerPage))
			},
		},
		{
			name: "search_with_date_filter_full",
			params: SearchParams{
				Query: "neural networks",
				Filters: SearchFilters{
					DateFrom: "2024-01-01",
					DateTo:   "2024-06-30",
				},
				Limit: 10,
			},
			responseJSON:   buildOATestSearchJSON(1, 1, 10, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				filterParam := r.URL.Query().Get(oaParamFilter)
				assert.Contains(t, filterParam, "from_publication_date:2024-01-01")
				assert.Contains(t, filterParam, "to_publication_date:2024-06-30")
			},
		},
		{
			name: "search_with_date_filter_year_only_from",
			params: SearchParams{
				Query:   "transformers",
				Filters: SearchFilters{DateFrom: "2023"},
				Limit:   10,
			},
			responseJSON:   buildOATestSearchJSON(1, 1, 10, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				filterParam := r.URL.Query().Get(oaParamFilter)
				assert.Contains(t, filterParam, "publication_year:2023")
			},
		},
		{
			name: "search_with_open_access_filter",
			params: SearchParams{
				Query:   "biology",
				Filters: SearchFilters{OpenAccess: boolPtr(true)},
				Limit:   10,
			},
			responseJSON:   buildOATestSearchJSON(1, 1, 10, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				filterParam := r.URL.Query().Get(oaParamFilter)
				assert.Contains(t, filterParam, "open_access.is_oa:true")
			},
		},
		{
			name: "search_with_min_citations_filter",
			params: SearchParams{
				Query:   "physics",
				Filters: SearchFilters{MinCitations: intPtr(100)},
				Limit:   10,
			},
			responseJSON:   buildOATestSearchJSON(1, 1, 10, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				filterParam := r.URL.Query().Get(oaParamFilter)
				assert.Contains(t, filterParam, "cited_by_count:>100")
			},
		},
		{
			name: "search_with_combined_filters",
			params: SearchParams{
				Query: "machine learning",
				Filters: SearchFilters{
					DateFrom:     "2024-01-01",
					MinCitations: intPtr(50),
					OpenAccess:   boolPtr(true),
				},
				Limit: 10,
			},
			responseJSON:   buildOATestSearchJSON(1, 1, 10, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				filterParam := r.URL.Query().Get(oaParamFilter)
				assert.Contains(t, filterParam, "from_publication_date:2024-01-01")
				assert.Contains(t, filterParam, "cited_by_count:>50")
				assert.Contains(t, filterParam, "open_access.is_oa:true")
			},
		},
		{
			name: "search_pagination",
			params: SearchParams{
				Query:  "reinforcement learning",
				Limit:  20,
				Offset: 40,
			},
			responseJSON:   buildOATestSearchJSON(100, 3, 20, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      100,
			wantHasMore:    true,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "3", r.URL.Query().Get(oaParamPage))
				assert.Equal(t, "20", r.URL.Query().Get(oaParamPerPage))
			},
		},
		{
			name: "search_has_more_false",
			params: SearchParams{
				Query: "rare topic",
				Limit: 10,
			},
			responseJSON:   buildOATestSearchJSON(3, 1, 10, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      3,
			wantHasMore:    false,
		},
		{
			name: "search_empty_results",
			params: SearchParams{
				Query: "xyznonexistenttopic",
				Limit: 10,
			},
			responseJSON:   buildOATestSearchJSON(0, 1, 10, nil),
			wantResultsCnt: 0,
			wantTotal:      0,
			wantHasMore:    false,
		},
		{
			name: "search_empty_query",
			params: SearchParams{
				Limit: 10,
			},
			wantErr: ErrOAEmptyQuery,
		},
		{
			name: "search_sort_relevance",
			params: SearchParams{
				Query: "deep learning",
				Sort:  SortRelevance,
				Limit: 10,
			},
			responseJSON:   buildOATestSearchJSON(1, 1, 10, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, oaSortRelevanceDesc, r.URL.Query().Get(oaParamSort))
			},
		},
		{
			name: "search_sort_date",
			params: SearchParams{
				Query: "deep learning",
				Sort:  SortDateDesc,
				Limit: 10,
			},
			responseJSON:   buildOATestSearchJSON(1, 1, 10, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, oaSortPubDateDesc, r.URL.Query().Get(oaParamSort))
			},
		},
		{
			name: "search_sort_citations",
			params: SearchParams{
				Query: "deep learning",
				Sort:  SortCitations,
				Limit: 10,
			},
			responseJSON:   buildOATestSearchJSON(1, 1, 10, []oaTestWork{defaultOATestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, oaSortCitedByCountDesc, r.URL.Query().Get(oaParamSort))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.wantErr != nil {
				plugin := newOATestPlugin(t, "http://unused.test")
				_, err := plugin.Search(context.Background(), tc.params, nil)
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.validateReq != nil {
					tc.validateReq(t, r)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.responseJSON)
			}))
			t.Cleanup(ts.Close)

			plugin := newOATestPlugin(t, ts.URL)
			result, err := plugin.Search(context.Background(), tc.params, nil)
			require.NoError(t, err)
			require.NotNil(t, result)

			assert.Len(t, result.Results, tc.wantResultsCnt)
			assert.Equal(t, tc.wantTotal, result.Total)
			assert.Equal(t, tc.wantHasMore, result.HasMore)
		})
	}
}

// ---------------------------------------------------------------------------
// Search credential tests
// ---------------------------------------------------------------------------

func TestOASearchWithAPIKey(t *testing.T) {
	t.Parallel()

	t.Run("server_level_api_key", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, testOAAPIKey, r.URL.Query().Get(oaParamAPIKey))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(0, 1, 10, nil))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPluginWithAPIKey(t, ts.URL, testOAAPIKey)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
	})

	t.Run("per_call_api_key_overrides_server", func(t *testing.T) {
		t.Parallel()

		const perCallKey = "per-call-oa-key"
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, perCallKey, r.URL.Query().Get(oaParamAPIKey))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(0, 1, 10, nil))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPluginWithAPIKey(t, ts.URL, testOAAPIKey)
		creds := &CallCredentials{OpenAlexAPIKey: perCallKey}
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, creds)
		require.NoError(t, err)
	})

	t.Run("no_api_key_no_param", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Empty(t, r.URL.Query().Get(oaParamAPIKey))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(0, 1, 10, nil))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// Search mailto tests
// ---------------------------------------------------------------------------

func TestOASearchWithMailto(t *testing.T) {
	t.Parallel()

	t.Run("mailto_included_in_request", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, testOAMailto, r.URL.Query().Get(oaParamMailto))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(0, 1, 10, nil))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPluginWithMailto(t, ts.URL, testOAMailto)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
	})

	t.Run("no_mailto_no_param", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Empty(t, r.URL.Query().Get(oaParamMailto))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(0, 1, 10, nil))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// Search result mapping
// ---------------------------------------------------------------------------

func TestOASearchResultMapping(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildOATestSearchJSON(1, 1, 10, []oaTestWork{defaultOATestWork1()}))
	}))
	t.Cleanup(ts.Close)

	plugin := newOATestPlugin(t, ts.URL)
	result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)

	pub := result.Results[0]
	assert.Equal(t, SourceOpenAlex+prefixedIDSeparator+testOAWorkID1, pub.ID)
	assert.Equal(t, SourceOpenAlex, pub.Source)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, testOATitle1, pub.Title)
	assert.NotEmpty(t, pub.Abstract, "abstract should be reconstructed from inverted index")
	assert.Contains(t, pub.Abstract, "dominant")
	assert.Contains(t, pub.Abstract, "attention")
	assert.Equal(t, testOAWorkURL1, pub.URL)
	assert.Equal(t, testOADOIBare1, pub.DOI)
	assert.Equal(t, testOADate1, pub.Published)
	assert.Equal(t, testOAPDFURL1, pub.PDFURL)
	assert.Equal(t, testOALicense1, pub.License)

	require.NotNil(t, pub.CitationCount)
	assert.Equal(t, testOACitations1, *pub.CitationCount)

	require.Len(t, pub.Authors, 1)
	assert.Equal(t, testOAAuthor1, pub.Authors[0].Name)
	assert.Equal(t, testOAORCIDBare, pub.Authors[0].ORCID)
	assert.Equal(t, testOAAffil1, pub.Authors[0].Affiliation)

	// Only level-0 concepts become categories.
	assert.Contains(t, pub.Categories, testOAConcept1)
	assert.NotContains(t, pub.Categories, testOAConcept2, "level-1 concepts excluded")

	// Source metadata.
	require.NotNil(t, pub.SourceMetadata)
	assert.Equal(t, testOAType1, pub.SourceMetadata[oaMetaKeyType])
	assert.Equal(t, testOATopic1, pub.SourceMetadata[oaMetaKeyPrimaryTopic])
	assert.Equal(t, testOAVenue1, pub.SourceMetadata[oaMetaKeyVenue])
	assert.Equal(t, true, pub.SourceMetadata[oaMetaKeyIsOA])
	assert.Equal(t, testOAOAStatus1, pub.SourceMetadata[oaMetaKeyOAStatus])
	assert.Equal(t, testOAWorkID1, pub.SourceMetadata[oaMetaKeyOpenAlexID])
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestOAGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		format       ContentFormat
		wantErr      error
		wantErrIs    error
		responseCode int
	}{
		{
			name:         "get_by_id_native",
			format:       FormatNative,
			responseCode: http.StatusOK,
		},
		{
			name:         "get_format_json",
			format:       FormatJSON,
			responseCode: http.StatusOK,
		},
		{
			name:         "get_format_bibtex_unsupported_at_plugin",
			format:       FormatBibTeX,
			wantErrIs:    ErrFormatUnsupported,
			responseCode: http.StatusOK,
		},
		{
			name:         "get_format_unsupported",
			format:       FormatXML,
			wantErrIs:    ErrFormatUnsupported,
			responseCode: http.StatusOK,
		},
		{
			name:         "get_not_found",
			format:       FormatNative,
			wantErrIs:    ErrGetFailed,
			responseCode: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.responseCode != http.StatusOK {
					w.WriteHeader(tc.responseCode)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, buildOATestWorkJSON(defaultOATestWork1()))
			}))
			t.Cleanup(ts.Close)

			plugin := newOATestPlugin(t, ts.URL)
			pub, err := plugin.Get(context.Background(), testOAWorkID1, nil, tc.format, nil)

			if tc.wantErrIs != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErrIs), "expected %v, got %v", tc.wantErrIs, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, pub)
			assert.Equal(t, testOATitle1, pub.Title)
			assert.Equal(t, SourceOpenAlex, pub.Source)
			assert.Equal(t, testOADOIBare1, pub.DOI)
		})
	}
}

func TestOAGetWithAPIKey(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, testOAAPIKey, r.URL.Query().Get(oaParamAPIKey))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildOATestWorkJSON(defaultOATestWork1()))
	}))
	t.Cleanup(ts.Close)

	plugin := newOATestPluginWithAPIKey(t, ts.URL, testOAAPIKey)
	pub, err := plugin.Get(context.Background(), testOAWorkID1, nil, FormatNative, nil)
	require.NoError(t, err)
	require.NotNil(t, pub)
}

func TestOAGetWithPerCallAPIKey(t *testing.T) {
	t.Parallel()

	const perCallKey = "per-call-get-key"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, perCallKey, r.URL.Query().Get(oaParamAPIKey))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildOATestWorkJSON(defaultOATestWork1()))
	}))
	t.Cleanup(ts.Close)

	plugin := newOATestPluginWithAPIKey(t, ts.URL, testOAAPIKey)
	creds := &CallCredentials{OpenAlexAPIKey: perCallKey}
	pub, err := plugin.Get(context.Background(), testOAWorkID1, nil, FormatNative, creds)
	require.NoError(t, err)
	require.NotNil(t, pub)
}

func TestOAGetWithMailto(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, testOAMailto, r.URL.Query().Get(oaParamMailto))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildOATestWorkJSON(defaultOATestWork1()))
	}))
	t.Cleanup(ts.Close)

	plugin := newOATestPluginWithMailto(t, ts.URL, testOAMailto)
	pub, err := plugin.Get(context.Background(), testOAWorkID1, nil, FormatNative, nil)
	require.NoError(t, err)
	require.NotNil(t, pub)
}

func TestOAGetNotFoundIncludesOANotFoundSentinel(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)

	plugin := newOATestPlugin(t, ts.URL)
	_, err := plugin.Get(context.Background(), testOAWorkID1, nil, FormatNative, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGetFailed)
	assert.ErrorIs(t, err, ErrOANotFound, "error chain should include OA-specific not-found sentinel")
}

// ---------------------------------------------------------------------------
// Inverted abstract reconstruction tests
// ---------------------------------------------------------------------------

func TestReconstructAbstract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    map[string][]int
		expected string
	}{
		{
			name: "basic_reconstruction",
			input: map[string][]int{
				"machine":  {0, 4},
				"learning": {1},
				"is":       {2},
				"great":    {3},
				"and":      {5},
				"fun":      {6},
			},
			expected: "machine learning is great machine and fun",
		},
		{
			name:     "empty_index",
			input:    map[string][]int{},
			expected: "",
		},
		{
			name:     "nil_index",
			input:    nil,
			expected: "",
		},
		{
			name:     "single_word",
			input:    map[string][]int{"hello": {0}},
			expected: "hello",
		},
		{
			name: "gaps_in_positions",
			input: map[string][]int{
				"first": {0},
				"third": {2},
				"sixth": {5},
			},
			expected: "first third sixth",
		},
		{
			name: "repeated_word_multiple_positions",
			input: map[string][]int{
				"the": {0, 3, 6},
				"cat": {1, 4},
				"sat": {2, 5},
			},
			expected: "the cat sat the cat sat the",
		},
		{
			name: "real_world_abstract_snippet",
			input: map[string][]int{
				"We":        {0},
				"propose":   {1},
				"a":         {2, 8},
				"new":       {3},
				"method":    {4},
				"for":       {5},
				"solving":   {6},
				"problems":  {7},
				"in":        {9},
				"efficient": {10},
				"way.":      {11},
			},
			expected: "We propose a new method for solving problems a in efficient way.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := reconstructAbstract(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestReconstructAbstractSafetyLimit(t *testing.T) {
	t.Parallel()

	// Position exceeding oaMaxAbstractPositions should be capped.
	input := map[string][]int{
		"hello":     {0},
		"oversized": {oaMaxAbstractPositions + 100},
	}
	result := reconstructAbstract(input)
	assert.Equal(t, "hello", result, "position beyond safety limit should be dropped")
}

// ---------------------------------------------------------------------------
// URL builder tests
// ---------------------------------------------------------------------------

func TestBuildOASearchURL(t *testing.T) {
	t.Parallel()

	t.Run("basic_search", func(t *testing.T) {
		t.Parallel()
		u := buildOASearchURL("https://api.openalex.org", SearchParams{
			Query: "attention",
			Limit: 10,
		}, "", "")
		assert.Contains(t, u, "search=attention")
		assert.Contains(t, u, "page=1")
		assert.Contains(t, u, "per_page=10")
		assert.NotContains(t, u, "mailto=")
		assert.NotContains(t, u, "api_key=")
	})

	t.Run("with_mailto_and_api_key", func(t *testing.T) {
		t.Parallel()
		u := buildOASearchURL("https://api.openalex.org", SearchParams{
			Query: "test",
			Limit: 10,
		}, testOAMailto, testOAAPIKey)
		assert.Contains(t, u, "mailto="+url.QueryEscape(testOAMailto))
		assert.Contains(t, u, "api_key="+testOAAPIKey)
	})

	t.Run("with_filters", func(t *testing.T) {
		t.Parallel()
		u := buildOASearchURL("https://api.openalex.org", SearchParams{
			Query:   "test",
			Filters: SearchFilters{MinCitations: intPtr(50), OpenAccess: boolPtr(true)},
			Limit:   10,
		}, "", "")
		assert.Contains(t, u, "filter=")
		assert.Contains(t, u, "cited_by_count")
		assert.Contains(t, u, "open_access")
	})

	t.Run("with_sort", func(t *testing.T) {
		t.Parallel()
		u := buildOASearchURL("https://api.openalex.org", SearchParams{
			Query: "test",
			Sort:  SortCitations,
			Limit: 10,
		}, "", "")
		assert.Contains(t, u, "sort=cited_by_count")
	})
}

func TestBuildOAGetURL(t *testing.T) {
	t.Parallel()

	t.Run("basic_get", func(t *testing.T) {
		t.Parallel()
		u := buildOAGetURL("https://api.openalex.org", testOAWorkID1, "", "")
		assert.Contains(t, u, "/works/"+testOAWorkID1)
		assert.NotContains(t, u, "?")
	})

	t.Run("with_mailto", func(t *testing.T) {
		t.Parallel()
		u := buildOAGetURL("https://api.openalex.org", testOAWorkID1, testOAMailto, "")
		assert.Contains(t, u, "mailto="+url.QueryEscape(testOAMailto))
	})

	t.Run("with_api_key", func(t *testing.T) {
		t.Parallel()
		u := buildOAGetURL("https://api.openalex.org", testOAWorkID1, "", testOAAPIKey)
		assert.Contains(t, u, "api_key="+testOAAPIKey)
	})
}

// ---------------------------------------------------------------------------
// Filter builder tests
// ---------------------------------------------------------------------------

func TestBuildOAFilterString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filters  SearchFilters
		contains []string
		empty    bool
	}{
		{
			name:  "empty_filters",
			empty: true,
		},
		{
			name:     "date_from_full",
			filters:  SearchFilters{DateFrom: "2024-01-01"},
			contains: []string{"from_publication_date:2024-01-01"},
		},
		{
			name:     "date_from_year_only",
			filters:  SearchFilters{DateFrom: "2024"},
			contains: []string{"publication_year:2024"},
		},
		{
			name:     "date_to_full",
			filters:  SearchFilters{DateTo: "2024-06-30"},
			contains: []string{"to_publication_date:2024-06-30"},
		},
		{
			name:     "date_to_year_only",
			filters:  SearchFilters{DateTo: "2024"},
			contains: []string{"to_publication_date:2024-12-31"},
		},
		{
			name:     "min_citations",
			filters:  SearchFilters{MinCitations: intPtr(100)},
			contains: []string{"cited_by_count:>100"},
		},
		{
			name:     "open_access_true",
			filters:  SearchFilters{OpenAccess: boolPtr(true)},
			contains: []string{"open_access.is_oa:true"},
		},
		{
			name:    "open_access_false_ignored",
			filters: SearchFilters{OpenAccess: boolPtr(false)},
			empty:   true,
		},
		{
			name:     "title_filter",
			filters:  SearchFilters{Title: "neural networks"},
			contains: []string{"title.search:neural networks"},
		},
		{
			name: "combined_filters",
			filters: SearchFilters{
				DateFrom:     "2024-01-01",
				MinCitations: intPtr(50),
				OpenAccess:   boolPtr(true),
			},
			contains: []string{"from_publication_date:2024-01-01", "cited_by_count:>50", "open_access.is_oa:true"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := buildOAFilterString(tc.filters)
			if tc.empty {
				assert.Empty(t, result)
				return
			}
			for _, c := range tc.contains {
				assert.Contains(t, result, c)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Sort mapping tests
// ---------------------------------------------------------------------------

func TestMapOASortOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sort     SortOrder
		expected string
	}{
		{"relevance", SortRelevance, oaSortRelevanceDesc},
		{"date_desc", SortDateDesc, oaSortPubDateDesc},
		{"citations", SortCitations, oaSortCitedByCountDesc},
		{"date_asc_unsupported", SortDateAsc, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, mapOASortOrder(tc.sort))
		})
	}
}

// ---------------------------------------------------------------------------
// Pagination mapping tests
// ---------------------------------------------------------------------------

func TestMapOAPagination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		offset      int
		limit       int
		wantPage    int
		wantPerPage int
	}{
		{"zero_offset", 0, 10, 1, 10},
		{"offset_20_limit_10", 20, 10, 3, 10},
		{"offset_40_limit_20", 40, 20, 3, 20},
		{"zero_limit_defaults", 0, 0, 1, oaDefaultPerPage},
		{"exceeds_max", 0, 500, 1, oaMaxResultsPerPage},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page, perPage := mapOAPagination(tc.offset, tc.limit)
			assert.Equal(t, tc.wantPage, page)
			assert.Equal(t, tc.wantPerPage, perPage)
		})
	}
}

// ---------------------------------------------------------------------------
// DOI normalization tests
// ---------------------------------------------------------------------------

func TestNormalizeOADOI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"full_url", "https://doi.org/10.1234/test", "10.1234/test"},
		{"bare_doi", "10.1234/test", "10.1234/test"},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, normalizeOADOI(tc.input))
		})
	}
}

// ---------------------------------------------------------------------------
// ID extraction tests
// ---------------------------------------------------------------------------

func TestExtractOAWorkID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"full_url", "https://openalex.org/W2741809807", "W2741809807"},
		{"bare_id", "W2741809807", "W2741809807"},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, extractOAWorkID(tc.input))
		})
	}
}

// ---------------------------------------------------------------------------
// ORCID normalization tests
// ---------------------------------------------------------------------------

func TestNormalizeOAORCID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"full_url", "https://orcid.org/0000-0001-2345-6789", "0000-0001-2345-6789"},
		{"bare_orcid", "0000-0001-2345-6789", "0000-0001-2345-6789"},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, normalizeOAORCID(tc.input))
		})
	}
}

// ---------------------------------------------------------------------------
// Health tracking tests
// ---------------------------------------------------------------------------

func TestOAHealthTracking(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		if count%2 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildOATestSearchJSON(0, 1, 10, nil))
	}))
	t.Cleanup(ts.Close)

	plugin := newOATestPlugin(t, ts.URL)
	ctx := context.Background()
	params := SearchParams{Query: "test", Limit: 10}

	// First call succeeds → healthy.
	_, err := plugin.Search(ctx, params, nil)
	require.NoError(t, err)
	health := plugin.Health(ctx)
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)

	// Second call fails → unhealthy.
	_, err = plugin.Search(ctx, params, nil)
	require.Error(t, err)
	health = plugin.Health(ctx)
	assert.False(t, health.Healthy)
	assert.NotEmpty(t, health.LastError)

	// Third call succeeds → healthy again.
	_, err = plugin.Search(ctx, params, nil)
	require.NoError(t, err)
	health = plugin.Health(ctx)
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)
}

// ---------------------------------------------------------------------------
// HTTP error tests
// ---------------------------------------------------------------------------

func TestOAHTTPErrors(t *testing.T) {
	t.Parallel()

	t.Run("server_500", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})

	t.Run("server_404_search", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})

	t.Run("context_canceled", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(testOAPluginTimeout)
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately
		_, err := plugin.Search(ctx, SearchParams{Query: "test", Limit: 10}, nil)
		require.Error(t, err)
	})

	t.Run("malformed_json", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{invalid json}`)
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})
}

// ---------------------------------------------------------------------------
// Initialize tests
// ---------------------------------------------------------------------------

func TestOAInitialize(t *testing.T) {
	t.Parallel()

	t.Run("default_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &OpenAlexPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, oaDefaultBaseURL, plugin.baseURL)
	})

	t.Run("custom_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &OpenAlexPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			BaseURL: "http://custom.test",
		})
		require.NoError(t, err)
		assert.Equal(t, "http://custom.test", plugin.baseURL)
	})

	t.Run("default_timeout", func(t *testing.T) {
		t.Parallel()
		plugin := &OpenAlexPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, DefaultPluginTimeout, plugin.httpClient.Timeout)
	})

	t.Run("custom_timeout", func(t *testing.T) {
		t.Parallel()
		customTimeout := 30 * time.Second
		plugin := &OpenAlexPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			Timeout: Duration{Duration: customTimeout},
		})
		require.NoError(t, err)
		assert.Equal(t, customTimeout, plugin.httpClient.Timeout)
	})

	t.Run("api_key_stored", func(t *testing.T) {
		t.Parallel()
		plugin := &OpenAlexPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			APIKey:  testOAAPIKey,
		})
		require.NoError(t, err)
		assert.Equal(t, testOAAPIKey, plugin.apiKey)
	})

	t.Run("mailto_from_extra", func(t *testing.T) {
		t.Parallel()
		plugin := &OpenAlexPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			Extra:   map[string]string{oaExtraKeyMailto: testOAMailto},
		})
		require.NoError(t, err)
		assert.Equal(t, testOAMailto, plugin.mailto)
	})

	t.Run("rate_limit_reported", func(t *testing.T) {
		t.Parallel()
		plugin := &OpenAlexPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled:   true,
			RateLimit: 10.0,
		})
		require.NoError(t, err)
		health := plugin.Health(context.Background())
		assert.InDelta(t, 10.0, health.RateLimit, 0.001)
	})

	t.Run("healthy_after_init", func(t *testing.T) {
		t.Parallel()
		plugin := &OpenAlexPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		health := plugin.Health(context.Background())
		assert.True(t, health.Healthy)
		assert.True(t, health.Enabled)
	})
}

// ---------------------------------------------------------------------------
// Concurrent access test
// ---------------------------------------------------------------------------

func TestOAConcurrentAccess(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/works/") && !strings.HasSuffix(r.URL.Path, "/works") {
			fmt.Fprint(w, buildOATestWorkJSON(defaultOATestWork1()))
			return
		}
		fmt.Fprint(w, buildOATestSearchJSON(1, 1, 10, []oaTestWork{defaultOATestWork1()}))
	}))
	t.Cleanup(ts.Close)

	plugin := newOATestPlugin(t, ts.URL)
	ctx := context.Background()

	var wg sync.WaitGroup
	for range testOAConcurrentGoroutines {
		wg.Go(func() {
			_, _ = plugin.Search(ctx, SearchParams{Query: "test", Limit: 10}, nil)
			_, _ = plugin.Get(ctx, testOAWorkID1, nil, FormatNative, nil)
			_ = plugin.Health(ctx)
		})
	}
	wg.Wait()

	health := plugin.Health(ctx)
	assert.True(t, health.Healthy)
}

// ---------------------------------------------------------------------------
// JSON parsing edge cases
// ---------------------------------------------------------------------------

func TestOAJSONEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("nil_open_access", func(t *testing.T) {
		t.Parallel()
		work := defaultOATestWork1()
		work.IsOA = false
		work.OAStatus = ""
		work.OAURL = ""

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(1, 1, 10, []oaTestWork{work}))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
	})

	t.Run("nil_primary_location", func(t *testing.T) {
		t.Parallel()
		work := defaultOATestWork1()
		work.VenueName = ""
		work.PDFURL = ""
		work.LandingPageURL = ""

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(1, 1, 10, []oaTestWork{work}))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		// PDFURL should come from OA URL if primary location is nil.
		assert.NotEmpty(t, result.Results[0].PDFURL)
	})

	t.Run("nil_inverted_abstract", func(t *testing.T) {
		t.Parallel()
		work := defaultOATestWork1()
		work.InvertedAbstract = nil

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(1, 1, 10, []oaTestWork{work}))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Empty(t, result.Results[0].Abstract)
	})

	t.Run("year_only_no_publication_date", func(t *testing.T) {
		t.Parallel()
		work := defaultOATestWork1()
		work.PubDate = ""

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(1, 1, 10, []oaTestWork{work}))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Equal(t, "2024", result.Results[0].Published)
	})

	t.Run("no_concepts", func(t *testing.T) {
		t.Parallel()
		work := defaultOATestWork1()
		work.Concepts = nil

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(1, 1, 10, []oaTestWork{work}))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Empty(t, result.Results[0].Categories)
	})

	t.Run("multiple_works_in_response", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildOATestSearchJSON(2, 1, 10, []oaTestWork{defaultOATestWork1(), defaultOATestWork2()}))
		}))
		t.Cleanup(ts.Close)

		plugin := newOATestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 2)
		assert.Equal(t, testOATitle1, result.Results[0].Title)
		assert.Equal(t, testOATitle2, result.Results[1].Title)
	})
}

// ---------------------------------------------------------------------------
// Credential resolution tests
// ---------------------------------------------------------------------------

func TestResolveOAAPIKey(t *testing.T) {
	t.Parallel()

	t.Run("nil_creds_returns_server_default", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "server-key", resolveOAAPIKey(nil, "server-key"))
	})

	t.Run("per_call_overrides_server", func(t *testing.T) {
		t.Parallel()
		creds := &CallCredentials{OpenAlexAPIKey: "per-call-key"}
		assert.Equal(t, "per-call-key", resolveOAAPIKey(creds, "server-key"))
	})

	t.Run("empty_per_call_falls_back", func(t *testing.T) {
		t.Parallel()
		creds := &CallCredentials{}
		assert.Equal(t, "server-key", resolveOAAPIKey(creds, "server-key"))
	})

	t.Run("both_empty", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, resolveOAAPIKey(nil, ""))
	})
}

// ---------------------------------------------------------------------------
// Concept mapping tests
// ---------------------------------------------------------------------------

func TestMapOAConcepts(t *testing.T) {
	t.Parallel()

	t.Run("only_level_zero", func(t *testing.T) {
		t.Parallel()
		concepts := []oaConcept{
			{DisplayName: "Computer Science", Level: 0},
			{DisplayName: "Machine Learning", Level: 1},
			{DisplayName: "Mathematics", Level: 0},
		}
		result := mapOAConcepts(concepts)
		assert.Equal(t, []string{"Computer Science", "Mathematics"}, result)
	})

	t.Run("no_level_zero", func(t *testing.T) {
		t.Parallel()
		concepts := []oaConcept{
			{DisplayName: "Deep Learning", Level: 2},
		}
		result := mapOAConcepts(concepts)
		assert.Nil(t, result)
	})

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		result := mapOAConcepts(nil)
		assert.Nil(t, result)
	})
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

func boolPtr(v bool) *bool {
	return &v
}
