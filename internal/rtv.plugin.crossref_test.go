package internal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	testCRDOI1          = "10.1038/s41586-024-07487-w"
	testCRTitle1        = "A Breakthrough in Quantum Computing"
	testCRAuthorGiven1  = "John"
	testCRAuthorFamily1 = "Smith"
	testCRAuthorName1   = "John Smith"
	testCRORCID1        = "https://orcid.org/0000-0001-2345-6789"
	testCRORCIDBare1    = "0000-0001-2345-6789"
	testCRAffil1        = "MIT"
	testCRDate1         = "2024-01-15"
	testCRYear1         = 2024
	testCRMonth1        = 1
	testCRDay1          = 15
	testCRYear2         = 2023
	testCRMonth2        = 6
	testCRDay2          = 1
	testCRDate2         = "2023-06-01"
	testCRCitations1    = 42
	testCRURL1          = "https://doi.org/10.1038/s41586-024-07487-w"
	testCRType1         = "journal-article"
	testCRJournal1      = "Nature"
	testCRISSN1         = "1234-5678"
	testCRVolume1       = "123"
	testCRIssue1        = "4"
	testCRPage1         = "456-789"
	testCRAbstractJATS  = "<jats:p>Abstract text with <jats:italic>XML</jats:italic> tags</jats:p>"
	testCRAbstractClean = "Abstract text with XML tags"

	testCRPluginTimeout        = 5 * time.Second
	testCRTotalResults         = 1234
	testCRConcurrentGoroutines = 10

	testCRMailto = "test@example.com"
)

// ---------------------------------------------------------------------------
// JSON fixture builder types
// ---------------------------------------------------------------------------

type crTestWork struct {
	DOI             string
	Title           string
	Authors         []crTestAuthor
	Abstract        string
	CitationCount   int
	URL             string
	Type            string
	Journal         string
	PublishedPrint  []int // [year, month, day]
	PublishedOnline []int // [year, month, day]
	ISSN            string
	Volume          string
	Issue           string
	Page            string
}

type crTestAuthor struct {
	Given       string
	Family      string
	ORCID       string
	Affiliation string
}

// ---------------------------------------------------------------------------
// JSON fixture builders
// ---------------------------------------------------------------------------

// buildCRTestSearchJSON generates a complete CrossRef search response JSON string.
func buildCRTestSearchJSON(totalResults int, works []crTestWork) string {
	worksJSON := "[]"
	if len(works) > 0 {
		items := make([]string, 0, len(works))
		for _, w := range works {
			items = append(items, buildCRTestWorkJSON(w))
		}
		worksJSON = "[" + joinStrings(items, ",") + "]"
	}

	return fmt.Sprintf(`{"status":"ok","message":{"total-results":%d,"items":%s}}`,
		totalResults, worksJSON)
}

// buildCRTestGetJSON generates a CrossRef single-work response JSON string.
func buildCRTestGetJSON(w crTestWork) string {
	return fmt.Sprintf(`{"status":"ok","message":%s}`, buildCRTestWorkJSON(w))
}

// buildCRTestWorkJSON generates a single CrossRef work JSON object.
func buildCRTestWorkJSON(w crTestWork) string {
	// Build authors.
	authorsJSON := "[]"
	if len(w.Authors) > 0 {
		items := make([]string, 0, len(w.Authors))
		for _, a := range w.Authors {
			affilJSON := "[]"
			if a.Affiliation != "" {
				affilJSON = fmt.Sprintf(`[{"name":%s}]`, jsonString(a.Affiliation))
			}
			orcidJSON := jsonStringOrNull(a.ORCID)
			items = append(items, fmt.Sprintf(`{"given":%s,"family":%s,"ORCID":%s,"affiliation":%s}`,
				jsonString(a.Given), jsonString(a.Family), orcidJSON, affilJSON))
		}
		authorsJSON = "[" + joinStrings(items, ",") + "]"
	}

	// Build published-print.
	pubPrintJSON := `{"date-parts":[[]]}`
	if len(w.PublishedPrint) > 0 {
		pubPrintJSON = buildCRDatePartsJSON(w.PublishedPrint)
	}

	// Build published-online.
	pubOnlineJSON := `{"date-parts":[[]]}`
	if len(w.PublishedOnline) > 0 {
		pubOnlineJSON = buildCRDatePartsJSON(w.PublishedOnline)
	}

	// Build title.
	titleJSON := "[]"
	if w.Title != "" {
		titleJSON = fmt.Sprintf(`[%s]`, jsonString(w.Title))
	}

	// Build container-title.
	journalJSON := "[]"
	if w.Journal != "" {
		journalJSON = fmt.Sprintf(`[%s]`, jsonString(w.Journal))
	}

	// Build ISSN.
	issnJSON := "[]"
	if w.ISSN != "" {
		issnJSON = fmt.Sprintf(`[%s]`, jsonString(w.ISSN))
	}

	abstractJSON := jsonStringOrNull(w.Abstract)

	return fmt.Sprintf(`{`+
		`"DOI":%s,`+
		`"title":%s,`+
		`"author":%s,`+
		`"abstract":%s,`+
		`"is-referenced-by-count":%d,`+
		`"URL":%s,`+
		`"type":%s,`+
		`"container-title":%s,`+
		`"published-print":%s,`+
		`"published-online":%s,`+
		`"ISSN":%s,`+
		`"volume":%s,`+
		`"issue":%s,`+
		`"page":%s`+
		`}`,
		jsonString(w.DOI),
		titleJSON,
		authorsJSON,
		abstractJSON,
		w.CitationCount,
		jsonString(w.URL),
		jsonStringOrNull(w.Type),
		journalJSON,
		pubPrintJSON,
		pubOnlineJSON,
		issnJSON,
		jsonStringOrNull(w.Volume),
		jsonStringOrNull(w.Issue),
		jsonStringOrNull(w.Page),
	)
}

// buildCRDatePartsJSON builds a CrossRef date-parts JSON object.
func buildCRDatePartsJSON(parts []int) string {
	partsJSON := make([]string, len(parts))
	for i, p := range parts {
		partsJSON[i] = strconv.Itoa(p)
	}
	return fmt.Sprintf(`{"date-parts":[[%s]]}`, joinStrings(partsJSON, ","))
}

// ---------------------------------------------------------------------------
// Default test works
// ---------------------------------------------------------------------------

func defaultCRTestWork1() crTestWork {
	return crTestWork{
		DOI:   testCRDOI1,
		Title: testCRTitle1,
		Authors: []crTestAuthor{
			{Given: testCRAuthorGiven1, Family: testCRAuthorFamily1, ORCID: testCRORCID1, Affiliation: testCRAffil1},
		},
		Abstract:       testCRAbstractJATS,
		CitationCount:  testCRCitations1,
		URL:            testCRURL1,
		Type:           testCRType1,
		Journal:        testCRJournal1,
		PublishedPrint: []int{testCRYear1, testCRMonth1, testCRDay1},
		ISSN:           testCRISSN1,
		Volume:         testCRVolume1,
		Issue:          testCRIssue1,
		Page:           testCRPage1,
	}
}

// ---------------------------------------------------------------------------
// httptest server factory
// ---------------------------------------------------------------------------

// newCrossRefTestPlugin creates a CrossRefPlugin pointing at a custom base URL.
func newCrossRefTestPlugin(t *testing.T, baseURL string) *CrossRefPlugin {
	t.Helper()
	plugin := &CrossRefPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testCRPluginTimeout},
		RateLimit: 10.0,
	})
	require.NoError(t, err)
	return plugin
}

// newCrossRefTestPluginWithMailto creates a CrossRefPlugin with a mailto config.
func newCrossRefTestPluginWithMailto(t *testing.T, baseURL, mailto string) *CrossRefPlugin {
	t.Helper()
	plugin := &CrossRefPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testCRPluginTimeout},
		RateLimit: 10.0,
		Extra:     map[string]string{crossrefExtraKeyMailto: mailto},
	})
	require.NoError(t, err)
	return plugin
}

// ---------------------------------------------------------------------------
// Contract test
// ---------------------------------------------------------------------------

func TestCrossRefPluginContract(t *testing.T) {
	plugin := newCrossRefTestPlugin(t, "http://unused.test")
	PluginContractTest(t, plugin)
}

// ---------------------------------------------------------------------------
// Search tests
// ---------------------------------------------------------------------------

func TestCrossRefSearch(t *testing.T) {
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
				Query: "quantum computing",
				Limit: 10,
			},
			responseJSON:   buildCRTestSearchJSON(testCRTotalResults, []crTestWork{defaultCRTestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      testCRTotalResults,
			wantHasMore:    true,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "quantum computing", r.URL.Query().Get(crossrefParamQuery))
				assert.Equal(t, "10", r.URL.Query().Get(crossrefParamRows))
			},
		},
		{
			name: "search_with_date_filter",
			params: SearchParams{
				Query: "machine learning",
				Filters: SearchFilters{
					DateFrom: "2024-01-01",
					DateTo:   "2024-06-30",
				},
				Limit: 10,
			},
			responseJSON:   buildCRTestSearchJSON(1, []crTestWork{defaultCRTestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				filterParam := r.URL.Query().Get(crossrefParamFilter)
				assert.Contains(t, filterParam, "from-pub-date:2024-01-01")
				assert.Contains(t, filterParam, "until-pub-date:2024-06-30")
			},
		},
		{
			name: "search_with_pagination",
			params: SearchParams{
				Query:  "neural networks",
				Limit:  20,
				Offset: 40,
			},
			responseJSON:   buildCRTestSearchJSON(100, []crTestWork{defaultCRTestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      100,
			wantHasMore:    true,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "20", r.URL.Query().Get(crossrefParamRows))
				assert.Equal(t, "40", r.URL.Query().Get(crossrefParamOffset))
			},
		},
		{
			name: "search_empty_results",
			params: SearchParams{
				Query: "xyznonexistenttopic",
				Limit: 10,
			},
			responseJSON:   buildCRTestSearchJSON(0, nil),
			wantResultsCnt: 0,
			wantTotal:      0,
			wantHasMore:    false,
		},
		{
			name: "search_empty_query",
			params: SearchParams{
				Limit: 10,
			},
			wantErr: ErrCrossRefEmptyQuery,
		},
		{
			name: "search_sort_relevance",
			params: SearchParams{
				Query: "deep learning",
				Sort:  SortRelevance,
				Limit: 10,
			},
			responseJSON:   buildCRTestSearchJSON(1, []crTestWork{defaultCRTestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, crossrefSortRelevance, r.URL.Query().Get(crossrefParamSort))
				assert.Equal(t, crossrefOrderDesc, r.URL.Query().Get(crossrefParamOrder))
			},
		},
		{
			name: "search_sort_date_desc",
			params: SearchParams{
				Query: "deep learning",
				Sort:  SortDateDesc,
				Limit: 10,
			},
			responseJSON:   buildCRTestSearchJSON(1, []crTestWork{defaultCRTestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, crossrefSortPublished, r.URL.Query().Get(crossrefParamSort))
				assert.Equal(t, crossrefOrderDesc, r.URL.Query().Get(crossrefParamOrder))
			},
		},
		{
			name: "search_sort_date_asc",
			params: SearchParams{
				Query: "deep learning",
				Sort:  SortDateAsc,
				Limit: 10,
			},
			responseJSON:   buildCRTestSearchJSON(1, []crTestWork{defaultCRTestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, crossrefSortPublished, r.URL.Query().Get(crossrefParamSort))
				assert.Equal(t, crossrefOrderAsc, r.URL.Query().Get(crossrefParamOrder))
			},
		},
		{
			name: "search_sort_citations",
			params: SearchParams{
				Query: "deep learning",
				Sort:  SortCitations,
				Limit: 10,
			},
			responseJSON:   buildCRTestSearchJSON(1, []crTestWork{defaultCRTestWork1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, crossrefSortCitations, r.URL.Query().Get(crossrefParamSort))
				assert.Equal(t, crossrefOrderDesc, r.URL.Query().Get(crossrefParamOrder))
			},
		},
		{
			name: "search_http_500",
			params: SearchParams{
				Query: "test",
				Limit: 10,
			},
			responseJSON: "", // unused; server returns 500
			wantErr:      ErrSearchFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.wantErr != nil && tc.name == "search_empty_query" {
				plugin := newCrossRefTestPlugin(t, "http://unused.test")
				_, err := plugin.Search(context.Background(), tc.params, nil)
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "search_http_500" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if tc.validateReq != nil {
					tc.validateReq(t, r)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.responseJSON)
			}))
			t.Cleanup(ts.Close)

			plugin := newCrossRefTestPlugin(t, ts.URL)
			result, err := plugin.Search(context.Background(), tc.params, nil)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			assert.Len(t, result.Results, tc.wantResultsCnt)
			assert.Equal(t, tc.wantTotal, result.Total)
			assert.Equal(t, tc.wantHasMore, result.HasMore)
		})
	}
}

// ---------------------------------------------------------------------------
// Search result mapping
// ---------------------------------------------------------------------------

func TestCrossRefSearchResultMapping(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildCRTestSearchJSON(1, []crTestWork{defaultCRTestWork1()}))
	}))
	t.Cleanup(ts.Close)

	plugin := newCrossRefTestPlugin(t, ts.URL)
	result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)

	pub := result.Results[0]
	assert.Equal(t, crossrefPluginID+prefixedIDSeparator+testCRDOI1, pub.ID)
	assert.Equal(t, SourceCrossRef, pub.Source)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, testCRTitle1, pub.Title)
	assert.Equal(t, testCRAbstractClean, pub.Abstract)
	assert.Equal(t, testCRURL1, pub.URL)
	assert.Equal(t, testCRDOI1, pub.DOI)
	assert.Equal(t, testCRDate1, pub.Published)

	require.NotNil(t, pub.CitationCount)
	assert.Equal(t, testCRCitations1, *pub.CitationCount)

	require.Len(t, pub.Authors, 1)
	assert.Equal(t, testCRAuthorName1, pub.Authors[0].Name)
	assert.Equal(t, testCRORCIDBare1, pub.Authors[0].ORCID)
	assert.Equal(t, testCRAffil1, pub.Authors[0].Affiliation)

	// Source metadata.
	require.NotNil(t, pub.SourceMetadata)
	assert.Equal(t, testCRJournal1, pub.SourceMetadata[crossrefMetaKeyJournal])
	assert.Equal(t, testCRType1, pub.SourceMetadata[crossrefMetaKeyType])
	assert.Equal(t, testCRISSN1, pub.SourceMetadata[crossrefMetaKeyISSN])
	assert.Equal(t, testCRVolume1, pub.SourceMetadata[crossrefMetaKeyVolume])
	assert.Equal(t, testCRIssue1, pub.SourceMetadata[crossrefMetaKeyIssue])
	assert.Equal(t, testCRPage1, pub.SourceMetadata[crossrefMetaKeyPage])
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestCrossRefGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		format       ContentFormat
		wantErr      error
		wantErrIs    error
		responseCode int
	}{
		{
			name:         "get_by_doi_native",
			format:       FormatNative,
			responseCode: http.StatusOK,
		},
		{
			name:         "get_format_json",
			format:       FormatJSON,
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
		{
			name:         "get_http_500",
			format:       FormatNative,
			wantErrIs:    ErrGetFailed,
			responseCode: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.responseCode != http.StatusOK {
					w.WriteHeader(tc.responseCode)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, buildCRTestGetJSON(defaultCRTestWork1()))
			}))
			t.Cleanup(ts.Close)

			plugin := newCrossRefTestPlugin(t, ts.URL)
			pub, err := plugin.Get(context.Background(), testCRDOI1, nil, tc.format, nil)

			if tc.wantErrIs != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErrIs), "expected %v, got %v", tc.wantErrIs, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, pub)
			assert.Equal(t, testCRTitle1, pub.Title)
			assert.Equal(t, SourceCrossRef, pub.Source)
			assert.Equal(t, testCRDOI1, pub.DOI)
		})
	}
}

func TestCrossRefGetNotFoundIncludesCrossRefSentinel(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)

	plugin := newCrossRefTestPlugin(t, ts.URL)
	_, err := plugin.Get(context.Background(), testCRDOI1, nil, FormatNative, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGetFailed)
	assert.ErrorIs(t, err, ErrCrossRefNotFound, "error chain should include CrossRef-specific not-found sentinel")
}

// ---------------------------------------------------------------------------
// Search mailto tests
// ---------------------------------------------------------------------------

func TestCrossRefSearchWithMailto(t *testing.T) {
	t.Parallel()

	t.Run("mailto_included_in_request", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, testCRMailto, r.URL.Query().Get(crossrefParamMailto))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildCRTestSearchJSON(0, nil))
		}))
		t.Cleanup(ts.Close)

		plugin := newCrossRefTestPluginWithMailto(t, ts.URL, testCRMailto)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
	})

	t.Run("no_mailto_no_param", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Empty(t, r.URL.Query().Get(crossrefParamMailto))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildCRTestSearchJSON(0, nil))
		}))
		t.Cleanup(ts.Close)

		plugin := newCrossRefTestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
	})

	t.Run("mailto_included_in_get_request", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, testCRMailto, r.URL.Query().Get(crossrefParamMailto))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildCRTestGetJSON(defaultCRTestWork1()))
		}))
		t.Cleanup(ts.Close)

		plugin := newCrossRefTestPluginWithMailto(t, ts.URL, testCRMailto)
		pub, err := plugin.Get(context.Background(), testCRDOI1, nil, FormatNative, nil)
		require.NoError(t, err)
		require.NotNil(t, pub)
	})
}

// ---------------------------------------------------------------------------
// HTTP error tests
// ---------------------------------------------------------------------------

func TestCrossRefHTTPErrors(t *testing.T) {
	t.Parallel()

	t.Run("server_500", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		plugin := newCrossRefTestPlugin(t, ts.URL)
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

		plugin := newCrossRefTestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})

	t.Run("context_canceled", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(testCRPluginTimeout)
		}))
		t.Cleanup(ts.Close)

		plugin := newCrossRefTestPlugin(t, ts.URL)
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

		plugin := newCrossRefTestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})
}

// ---------------------------------------------------------------------------
// XML tag stripping tests
// ---------------------------------------------------------------------------

func TestStripXMLTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "jats_tags",
			input:    "<jats:p>Abstract text with <jats:italic>XML</jats:italic> tags</jats:p>",
			expected: "Abstract text with XML tags",
		},
		{
			name:     "nested_tags",
			input:    "<p>Outer <b>bold <i>italic</i></b> text</p>",
			expected: "Outer bold italic text",
		},
		{
			name:     "whitespace_collapse",
			input:    "<p>  Multiple   spaces   here  </p>",
			expected: "Multiple spaces here",
		},
		{
			name:     "empty_input",
			input:    "",
			expected: "",
		},
		{
			name:     "no_tags",
			input:    "Plain text without any tags",
			expected: "Plain text without any tags",
		},
		{
			name:     "self_closing_tags",
			input:    "Before<br/>After",
			expected: "Before After",
		},
		{
			name:     "only_tags",
			input:    "<p><br/></p>",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, stripXMLTags(tc.input))
		})
	}
}

// ---------------------------------------------------------------------------
// Date-parts conversion tests
// ---------------------------------------------------------------------------

func TestCrossRefDateParts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    crossrefDateParts
		expected string
	}{
		{
			name:     "full_date",
			input:    crossrefDateParts{DateParts: [][]int{{testCRYear1, testCRMonth1, testCRDay1}}},
			expected: testCRDate1,
		},
		{
			name:     "year_and_month_only",
			input:    crossrefDateParts{DateParts: [][]int{{2024, 3}}},
			expected: "2024-03",
		},
		{
			name:     "year_only",
			input:    crossrefDateParts{DateParts: [][]int{{2024}}},
			expected: "2024",
		},
		{
			name:     "empty_date_parts",
			input:    crossrefDateParts{DateParts: [][]int{}},
			expected: "",
		},
		{
			name:     "nil_date_parts",
			input:    crossrefDateParts{},
			expected: "",
		},
		{
			name:     "empty_inner_array",
			input:    crossrefDateParts{DateParts: [][]int{{}}},
			expected: "",
		},
		{
			name:     "zero_year",
			input:    crossrefDateParts{DateParts: [][]int{{0}}},
			expected: "",
		},
		{
			name:     "zero_month",
			input:    crossrefDateParts{DateParts: [][]int{{2024, 0}}},
			expected: "2024",
		},
		{
			name:     "zero_day",
			input:    crossrefDateParts{DateParts: [][]int{{2024, 3, 0}}},
			expected: "2024-03",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, mapCrossRefDateParts(tc.input))
		})
	}
}

// ---------------------------------------------------------------------------
// Health tracking tests
// ---------------------------------------------------------------------------

func TestCrossRefHealthTracking(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := callCount.Add(1)
		if count%2 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildCRTestSearchJSON(0, nil))
	}))
	t.Cleanup(ts.Close)

	plugin := newCrossRefTestPlugin(t, ts.URL)
	ctx := context.Background()
	params := SearchParams{Query: "test", Limit: 10}

	// First call succeeds -> healthy.
	_, err := plugin.Search(ctx, params, nil)
	require.NoError(t, err)
	health := plugin.Health(ctx)
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)

	// Second call fails -> unhealthy.
	_, err = plugin.Search(ctx, params, nil)
	require.Error(t, err)
	health = plugin.Health(ctx)
	assert.False(t, health.Healthy)
	assert.NotEmpty(t, health.LastError)

	// Third call succeeds -> healthy again.
	_, err = plugin.Search(ctx, params, nil)
	require.NoError(t, err)
	health = plugin.Health(ctx)
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)
}

// ---------------------------------------------------------------------------
// Initialize tests
// ---------------------------------------------------------------------------

func TestCrossRefInitialize(t *testing.T) {
	t.Parallel()

	t.Run("default_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &CrossRefPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, crossrefDefaultBaseURL, plugin.baseURL)
	})

	t.Run("custom_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &CrossRefPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			BaseURL: "http://custom.test",
		})
		require.NoError(t, err)
		assert.Equal(t, "http://custom.test", plugin.baseURL)
	})

	t.Run("default_timeout", func(t *testing.T) {
		t.Parallel()
		plugin := &CrossRefPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, DefaultPluginTimeout, plugin.httpClient.Timeout)
	})

	t.Run("custom_timeout", func(t *testing.T) {
		t.Parallel()
		customTimeout := 30 * time.Second
		plugin := &CrossRefPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			Timeout: Duration{Duration: customTimeout},
		})
		require.NoError(t, err)
		assert.Equal(t, customTimeout, plugin.httpClient.Timeout)
	})

	t.Run("mailto_from_extra", func(t *testing.T) {
		t.Parallel()
		plugin := &CrossRefPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			Extra:   map[string]string{crossrefExtraKeyMailto: testCRMailto},
		})
		require.NoError(t, err)
		assert.Equal(t, testCRMailto, plugin.mailto)
	})

	t.Run("rate_limit_reported", func(t *testing.T) {
		t.Parallel()
		plugin := &CrossRefPlugin{}
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
		plugin := &CrossRefPlugin{}
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

func TestCrossRefConcurrentSafety(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/works/") && !strings.HasSuffix(r.URL.Path, "/works") {
			fmt.Fprint(w, buildCRTestGetJSON(defaultCRTestWork1()))
			return
		}
		fmt.Fprint(w, buildCRTestSearchJSON(1, []crTestWork{defaultCRTestWork1()}))
	}))
	t.Cleanup(ts.Close)

	plugin := newCrossRefTestPlugin(t, ts.URL)
	ctx := context.Background()

	var wg sync.WaitGroup
	for range testCRConcurrentGoroutines {
		wg.Go(func() {
			_, _ = plugin.Search(ctx, SearchParams{Query: "test", Limit: 10}, nil)
			_, _ = plugin.Get(ctx, testCRDOI1, nil, FormatNative, nil)
			_ = plugin.Health(ctx)
		})
	}
	wg.Wait()

	health := plugin.Health(ctx)
	assert.True(t, health.Healthy)
}

// ---------------------------------------------------------------------------
// Sort mapping tests
// ---------------------------------------------------------------------------

func TestMapCrossRefSortOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sort      SortOrder
		wantSort  string
		wantOrder string
	}{
		{"relevance", SortRelevance, crossrefSortRelevance, crossrefOrderDesc},
		{"date_desc", SortDateDesc, crossrefSortPublished, crossrefOrderDesc},
		{"date_asc", SortDateAsc, crossrefSortPublished, crossrefOrderAsc},
		{"citations", SortCitations, crossrefSortCitations, crossrefOrderDesc},
		{"unknown_empty", SortOrder("unknown"), "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotSort, gotOrder := mapCrossRefSortOrder(tc.sort)
			assert.Equal(t, tc.wantSort, gotSort)
			assert.Equal(t, tc.wantOrder, gotOrder)
		})
	}
}

// ---------------------------------------------------------------------------
// Filter builder tests
// ---------------------------------------------------------------------------

func TestBuildCrossRefFilterString(t *testing.T) {
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
			name:     "date_from",
			filters:  SearchFilters{DateFrom: "2024-01-01"},
			contains: []string{"from-pub-date:2024-01-01"},
		},
		{
			name:     "date_to",
			filters:  SearchFilters{DateTo: "2024-06-30"},
			contains: []string{"until-pub-date:2024-06-30"},
		},
		{
			name: "both_dates",
			filters: SearchFilters{
				DateFrom: "2024-01-01",
				DateTo:   "2024-06-30",
			},
			contains: []string{"from-pub-date:2024-01-01", "until-pub-date:2024-06-30"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := buildCrossRefFilterString(tc.filters)
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
// URL builder tests
// ---------------------------------------------------------------------------

func TestBuildCrossRefSearchURL(t *testing.T) {
	t.Parallel()

	t.Run("basic_search", func(t *testing.T) {
		t.Parallel()
		u := buildCrossRefSearchURL(crossrefDefaultBaseURL, SearchParams{
			Query: "quantum",
			Limit: 10,
		}, "")
		assert.Contains(t, u, "query=quantum")
		assert.Contains(t, u, "rows=10")
		assert.NotContains(t, u, "mailto=")
		assert.NotContains(t, u, "offset=")
	})

	t.Run("with_mailto", func(t *testing.T) {
		t.Parallel()
		u := buildCrossRefSearchURL(crossrefDefaultBaseURL, SearchParams{
			Query: "test",
			Limit: 10,
		}, testCRMailto)
		assert.Contains(t, u, "mailto=")
	})

	t.Run("with_offset", func(t *testing.T) {
		t.Parallel()
		u := buildCrossRefSearchURL(crossrefDefaultBaseURL, SearchParams{
			Query:  "test",
			Limit:  10,
			Offset: 20,
		}, "")
		assert.Contains(t, u, "offset=20")
	})

	t.Run("with_sort", func(t *testing.T) {
		t.Parallel()
		u := buildCrossRefSearchURL(crossrefDefaultBaseURL, SearchParams{
			Query: "test",
			Sort:  SortCitations,
			Limit: 10,
		}, "")
		assert.Contains(t, u, "sort=is-referenced-by-count")
		assert.Contains(t, u, "order=desc")
	})

	t.Run("with_filter", func(t *testing.T) {
		t.Parallel()
		u := buildCrossRefSearchURL(crossrefDefaultBaseURL, SearchParams{
			Query:   "test",
			Filters: SearchFilters{DateFrom: "2024-01-01"},
			Limit:   10,
		}, "")
		assert.Contains(t, u, "filter=")
		assert.Contains(t, u, "from-pub-date")
	})

	t.Run("limit_capped_at_max", func(t *testing.T) {
		t.Parallel()
		u := buildCrossRefSearchURL(crossrefDefaultBaseURL, SearchParams{
			Query: "test",
			Limit: 500,
		}, "")
		assert.Contains(t, u, fmt.Sprintf("rows=%d", crossrefMaxResultsPerPage))
	})
}

func TestBuildCrossRefGetURL(t *testing.T) {
	t.Parallel()

	t.Run("basic_get", func(t *testing.T) {
		t.Parallel()
		u := buildCrossRefGetURL(crossrefDefaultBaseURL, testCRDOI1, "")
		assert.Contains(t, u, "/works/")
		assert.NotContains(t, u, "?")
	})

	t.Run("with_mailto", func(t *testing.T) {
		t.Parallel()
		u := buildCrossRefGetURL(crossrefDefaultBaseURL, testCRDOI1, testCRMailto)
		assert.Contains(t, u, "mailto=")
	})
}

// ---------------------------------------------------------------------------
// Format conversion tests
// ---------------------------------------------------------------------------

func TestConvertCrossRefFormat(t *testing.T) {
	t.Parallel()

	t.Run("json_returns_nil", func(t *testing.T) {
		t.Parallel()
		err := convertCrossRefFormat(nil, FormatJSON)
		assert.NoError(t, err)
	})

	t.Run("unsupported_format", func(t *testing.T) {
		t.Parallel()
		err := convertCrossRefFormat(nil, FormatXML)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrFormatUnsupported)
	})
}

// ---------------------------------------------------------------------------
// JSON edge cases
// ---------------------------------------------------------------------------

func TestCrossRefJSONEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("no_abstract", func(t *testing.T) {
		t.Parallel()
		work := defaultCRTestWork1()
		work.Abstract = ""

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildCRTestSearchJSON(1, []crTestWork{work}))
		}))
		t.Cleanup(ts.Close)

		plugin := newCrossRefTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Empty(t, result.Results[0].Abstract)
	})

	t.Run("no_authors", func(t *testing.T) {
		t.Parallel()
		work := defaultCRTestWork1()
		work.Authors = nil

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildCRTestSearchJSON(1, []crTestWork{work}))
		}))
		t.Cleanup(ts.Close)

		plugin := newCrossRefTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Empty(t, result.Results[0].Authors)
	})

	t.Run("online_date_fallback", func(t *testing.T) {
		t.Parallel()
		work := defaultCRTestWork1()
		work.PublishedPrint = nil
		work.PublishedOnline = []int{testCRYear2, testCRMonth2, testCRDay2}

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildCRTestSearchJSON(1, []crTestWork{work}))
		}))
		t.Cleanup(ts.Close)

		plugin := newCrossRefTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Equal(t, testCRDate2, result.Results[0].Published)
	})

	t.Run("empty_title_array", func(t *testing.T) {
		t.Parallel()
		work := defaultCRTestWork1()
		work.Title = ""

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildCRTestSearchJSON(1, []crTestWork{work}))
		}))
		t.Cleanup(ts.Close)

		plugin := newCrossRefTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Empty(t, result.Results[0].Title)
	})
}
