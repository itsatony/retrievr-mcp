package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	testDBLPKey1    = "journals/corr/abs-2401-12345"
	testDBLPKey2    = "conf/neurips/SmithDoe2024"
	testDBLPTitle1  = "Attention Is All You Need"
	testDBLPTitle2  = "Deep Learning for NLP"
	testDBLPAuthor1 = "John Smith"
	testDBLPAuthor2 = "Jane Doe"
	testDBLPPID1    = "s/JohnSmith"
	testDBLPPID2    = "d/JaneDoe"
	testDBLPVenue1  = "NeurIPS"
	testDBLPVenue2  = "ICML"
	testDBLPYear1   = "2024"
	testDBLPYear2   = "2023"
	testDBLPDOI1    = "10.1234/dblptest.2024.001"
	testDBLPDOI2    = "10.5678/dblptest.2023.002"
	testDBLPEE1     = "https://doi.org/10.1234/dblptest.2024.001"
	testDBLPEE2     = "https://doi.org/10.5678/dblptest.2023.002"
	testDBLPURL1    = "https://dblp.org/rec/journals/corr/abs-2401-12345"
	testDBLPURL2    = "https://dblp.org/rec/conf/neurips/SmithDoe2024"
	testDBLPType1   = "Conference and Workshop Papers"
	testDBLPType2   = "Journal Articles"

	testDBLPPluginTimeout        = 5 * time.Second
	testDBLPTotalResults         = 1234
	testDBLPConcurrentGoroutines = 10
)

// ---------------------------------------------------------------------------
// JSON fixture builder
// ---------------------------------------------------------------------------

// buildDBLPTestSearchJSON generates a complete DBLP search response JSON string.
func buildDBLPTestSearchJSON(total int, hits []dblpTestHit) string {
	hitsJSON := "null"
	if hits != nil {
		if len(hits) == 0 {
			hitsJSON = "[]"
		} else {
			items := make([]string, 0, len(hits))
			for _, h := range hits {
				items = append(items, buildDBLPTestHitJSON(h))
			}
			hitsJSON = "[" + joinStrings(items, ",") + "]"
		}
	}

	return fmt.Sprintf(`{"result":{"hits":{"@total":"%d","hit":%s}}}`,
		total, hitsJSON)
}

// buildDBLPTestSearchJSONWithStringTotal allows specifying the total as a raw string.
func buildDBLPTestSearchJSONWithStringTotal(total string, hits []dblpTestHit) string {
	hitsJSON := "null"
	if hits != nil {
		if len(hits) == 0 {
			hitsJSON = "[]"
		} else {
			items := make([]string, 0, len(hits))
			for _, h := range hits {
				items = append(items, buildDBLPTestHitJSON(h))
			}
			hitsJSON = "[" + joinStrings(items, ",") + "]"
		}
	}

	return fmt.Sprintf(`{"result":{"hits":{"@total":%s,"hit":%s}}}`,
		jsonString(total), hitsJSON)
}

type dblpTestHit struct {
	Key     string
	Title   string
	Authors []dblpTestAuthor
	Venue   string
	Year    string
	Type    string
	DOI     string
	EE      string
	URL     string
}

type dblpTestAuthor struct {
	Name string
	PID  string
}

// buildDBLPTestHitJSON generates a single DBLP hit JSON object.
func buildDBLPTestHitJSON(h dblpTestHit) string {
	// Build authors.
	authorsJSON := "null"
	if len(h.Authors) > 0 {
		if len(h.Authors) == 1 {
			// Single author as object (testing polymorphism).
			authorsJSON = fmt.Sprintf(`{"author":{"text":%s,"@pid":%s}}`,
				jsonString(h.Authors[0].Name), jsonString(h.Authors[0].PID))
		} else {
			items := make([]string, 0, len(h.Authors))
			for _, a := range h.Authors {
				items = append(items, fmt.Sprintf(`{"text":%s,"@pid":%s}`,
					jsonString(a.Name), jsonString(a.PID)))
			}
			authorsJSON = fmt.Sprintf(`{"author":[%s]}`, joinStrings(items, ","))
		}
	}

	return fmt.Sprintf(`{"info":{`+
		`"key":%s,`+
		`"title":%s,`+
		`"authors":%s,`+
		`"venue":%s,`+
		`"year":%s,`+
		`"type":%s,`+
		`"doi":%s,`+
		`"ee":%s,`+
		`"url":%s`+
		`}}`,
		jsonString(h.Key),
		jsonString(h.Title),
		authorsJSON,
		jsonStringOrNull(h.Venue),
		jsonStringOrNull(h.Year),
		jsonStringOrNull(h.Type),
		jsonStringOrNull(h.DOI),
		jsonStringOrNull(h.EE),
		jsonStringOrNull(h.URL),
	)
}

// ---------------------------------------------------------------------------
// Default test hits
// ---------------------------------------------------------------------------

func defaultDBLPTestHit1() dblpTestHit {
	return dblpTestHit{
		Key:   testDBLPKey1,
		Title: testDBLPTitle1,
		Authors: []dblpTestAuthor{
			{Name: testDBLPAuthor1, PID: testDBLPPID1},
			{Name: testDBLPAuthor2, PID: testDBLPPID2},
		},
		Venue: testDBLPVenue1,
		Year:  testDBLPYear1,
		Type:  testDBLPType1,
		DOI:   testDBLPDOI1,
		EE:    testDBLPEE1,
		URL:   testDBLPURL1,
	}
}

func defaultDBLPTestHit2() dblpTestHit {
	return dblpTestHit{
		Key:   testDBLPKey2,
		Title: testDBLPTitle2,
		Authors: []dblpTestAuthor{
			{Name: testDBLPAuthor2, PID: testDBLPPID2},
		},
		Venue: testDBLPVenue2,
		Year:  testDBLPYear2,
		Type:  testDBLPType2,
		DOI:   testDBLPDOI2,
		EE:    testDBLPEE2,
		URL:   testDBLPURL2,
	}
}

// ---------------------------------------------------------------------------
// httptest server factory
// ---------------------------------------------------------------------------

// newDBLPTestPlugin creates a DBLPPlugin pointing at a custom base URL.
func newDBLPTestPlugin(t *testing.T, baseURL string) *DBLPPlugin {
	t.Helper()
	plugin := &DBLPPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testDBLPPluginTimeout},
		RateLimit: 10.0,
	})
	require.NoError(t, err)
	return plugin
}

// ---------------------------------------------------------------------------
// Contract test
// ---------------------------------------------------------------------------

func TestDBLPPluginContract(t *testing.T) {
	plugin := newDBLPTestPlugin(t, "http://unused.test")
	PluginContractTest(t, plugin)
}

// ---------------------------------------------------------------------------
// Search tests
// ---------------------------------------------------------------------------

func TestDBLPSearch(t *testing.T) {
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
			responseJSON:   buildDBLPTestSearchJSON(testDBLPTotalResults, []dblpTestHit{defaultDBLPTestHit1()}),
			wantResultsCnt: 1,
			wantTotal:      testDBLPTotalResults,
			wantHasMore:    true,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "attention mechanism", r.URL.Query().Get(dblpParamQuery))
				assert.Equal(t, dblpFormatJSON, r.URL.Query().Get(dblpParamFormat))
				assert.Equal(t, "10", r.URL.Query().Get(dblpParamHits))
			},
		},
		{
			name: "search_with_pagination",
			params: SearchParams{
				Query:  "deep learning",
				Limit:  20,
				Offset: 40,
			},
			responseJSON:   buildDBLPTestSearchJSON(100, []dblpTestHit{defaultDBLPTestHit1()}),
			wantResultsCnt: 1,
			wantTotal:      100,
			wantHasMore:    true,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "20", r.URL.Query().Get(dblpParamHits))
				assert.Equal(t, "40", r.URL.Query().Get(dblpParamFirst))
			},
		},
		{
			name: "search_multiple_results",
			params: SearchParams{
				Query: "neural networks",
				Limit: 10,
			},
			responseJSON:   buildDBLPTestSearchJSON(2, []dblpTestHit{defaultDBLPTestHit1(), defaultDBLPTestHit2()}),
			wantResultsCnt: 2,
			wantTotal:      2,
			wantHasMore:    false,
		},
		{
			name: "empty_results_nil_hit_array",
			params: SearchParams{
				Query: "xyznonexistenttopic",
				Limit: 10,
			},
			responseJSON:   `{"result":{"hits":{"@total":"0","hit":null}}}`,
			wantResultsCnt: 0,
			wantTotal:      0,
			wantHasMore:    false,
		},
		{
			name: "empty_results_empty_hit_array",
			params: SearchParams{
				Query: "xyznonexistenttopic",
				Limit: 10,
			},
			responseJSON:   buildDBLPTestSearchJSON(0, []dblpTestHit{}),
			wantResultsCnt: 0,
			wantTotal:      0,
			wantHasMore:    false,
		},
		{
			name: "empty_query_error",
			params: SearchParams{
				Limit: 10,
			},
			wantErr: ErrDBLPEmptyQuery,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.wantErr != nil {
				plugin := newDBLPTestPlugin(t, "http://unused.test")
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

			plugin := newDBLPTestPlugin(t, ts.URL)
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
// Search HTTP 500 test
// ---------------------------------------------------------------------------

func TestDBLPSearchHTTP500(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	plugin := newDBLPTestPlugin(t, ts.URL)
	_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSearchFailed)
}

// ---------------------------------------------------------------------------
// Search result mapping
// ---------------------------------------------------------------------------

func TestDBLPSearchResultMapping(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildDBLPTestSearchJSON(1, []dblpTestHit{defaultDBLPTestHit1()}))
	}))
	t.Cleanup(ts.Close)

	plugin := newDBLPTestPlugin(t, ts.URL)
	result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)

	pub := result.Results[0]
	assert.Equal(t, SourceDBLP+prefixedIDSeparator+testDBLPKey1, pub.ID)
	assert.Equal(t, SourceDBLP, pub.Source)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, testDBLPTitle1, pub.Title)
	assert.Empty(t, pub.Abstract, "DBLP does not provide abstracts")
	assert.Equal(t, testDBLPEE1, pub.URL)
	assert.Equal(t, testDBLPDOI1, pub.DOI)
	assert.Equal(t, testDBLPYear1, pub.Published)

	require.Len(t, pub.Authors, 2)
	assert.Equal(t, testDBLPAuthor1, pub.Authors[0].Name)
	assert.Equal(t, testDBLPAuthor2, pub.Authors[1].Name)

	// Source metadata.
	require.NotNil(t, pub.SourceMetadata)
	assert.Equal(t, testDBLPVenue1, pub.SourceMetadata[dblpMetaKeyVenue])
	assert.Equal(t, testDBLPType1, pub.SourceMetadata[dblpMetaKeyType])
	assert.Equal(t, testDBLPKey1, pub.SourceMetadata[dblpMetaKeyKey])
	assert.Equal(t, testDBLPEE1, pub.SourceMetadata[dblpMetaKeyEE])
	assert.Equal(t, testDBLPURL1, pub.SourceMetadata[dblpMetaKeyDBLPURL])
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestDBLPGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		format       ContentFormat
		responseJSON string
		responseCode int
		wantErr      error
		wantErrIs    error
	}{
		{
			name:         "get_by_key_native",
			format:       FormatNative,
			responseJSON: buildDBLPTestSearchJSON(1, []dblpTestHit{defaultDBLPTestHit1()}),
			responseCode: http.StatusOK,
		},
		{
			name:         "get_format_json",
			format:       FormatJSON,
			responseJSON: buildDBLPTestSearchJSON(1, []dblpTestHit{defaultDBLPTestHit1()}),
			responseCode: http.StatusOK,
		},
		{
			name:         "get_format_bibtex_unsupported_at_plugin",
			format:       FormatBibTeX,
			responseJSON: buildDBLPTestSearchJSON(1, []dblpTestHit{defaultDBLPTestHit1()}),
			responseCode: http.StatusOK,
			wantErrIs:    ErrFormatUnsupported,
		},
		{
			name:         "get_format_unsupported",
			format:       FormatXML,
			responseJSON: buildDBLPTestSearchJSON(1, []dblpTestHit{defaultDBLPTestHit1()}),
			responseCode: http.StatusOK,
			wantErrIs:    ErrFormatUnsupported,
		},
		{
			name:         "get_not_found_empty_results",
			format:       FormatNative,
			responseJSON: buildDBLPTestSearchJSON(0, []dblpTestHit{}),
			responseCode: http.StatusOK,
			wantErrIs:    ErrDBLPNotFound,
		},
		{
			name:         "get_not_found_nil_hits",
			format:       FormatNative,
			responseJSON: `{"result":{"hits":{"@total":"0","hit":null}}}`,
			responseCode: http.StatusOK,
			wantErrIs:    ErrDBLPNotFound,
		},
		{
			name:         "get_http_500",
			format:       FormatNative,
			responseCode: http.StatusInternalServerError,
			wantErrIs:    ErrGetFailed,
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
				// Validate that key: prefix is used in query.
				q := r.URL.Query().Get(dblpParamQuery)
				assert.Contains(t, q, dblpGetKeyPrefix)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.responseJSON)
			}))
			t.Cleanup(ts.Close)

			plugin := newDBLPTestPlugin(t, ts.URL)
			pub, err := plugin.Get(context.Background(), testDBLPKey1, nil, tc.format, nil)

			if tc.wantErrIs != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErrIs), "expected %v, got %v", tc.wantErrIs, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, pub)
			assert.Equal(t, testDBLPTitle1, pub.Title)
			assert.Equal(t, SourceDBLP, pub.Source)
			assert.Equal(t, testDBLPDOI1, pub.DOI)
		})
	}
}

// ---------------------------------------------------------------------------
// Get URL construction test
// ---------------------------------------------------------------------------

func TestDBLPGetURLConstruction(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get(dblpParamQuery)
		assert.Equal(t, dblpGetKeyPrefix+testDBLPKey1, q)
		assert.Equal(t, "1", r.URL.Query().Get(dblpParamHits))
		assert.Equal(t, dblpFormatJSON, r.URL.Query().Get(dblpParamFormat))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildDBLPTestSearchJSON(1, []dblpTestHit{defaultDBLPTestHit1()}))
	}))
	t.Cleanup(ts.Close)

	plugin := newDBLPTestPlugin(t, ts.URL)
	pub, err := plugin.Get(context.Background(), testDBLPKey1, nil, FormatNative, nil)
	require.NoError(t, err)
	require.NotNil(t, pub)
}

// ---------------------------------------------------------------------------
// Author unmarshal tests (critical: polymorphic author field)
// ---------------------------------------------------------------------------

func TestDBLPAuthorUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantCount int
		wantNames []string
		wantErr   bool
	}{
		{
			name:      "multiple_authors_array",
			input:     `{"author":[{"text":"John Smith","@pid":"s/JohnSmith"},{"text":"Jane Doe","@pid":"d/JaneDoe"}]}`,
			wantCount: 2,
			wantNames: []string{testDBLPAuthor1, testDBLPAuthor2},
		},
		{
			name:      "single_author_object",
			input:     `{"author":{"text":"John Smith","@pid":"s/JohnSmith"}}`,
			wantCount: 1,
			wantNames: []string{testDBLPAuthor1},
		},
		{
			name:      "empty_author_array",
			input:     `{"author":[]}`,
			wantCount: 0,
			wantNames: nil,
		},
		{
			name:    "invalid_json",
			input:   `{invalid}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var authors dblpAuthors
			err := json.Unmarshal([]byte(tc.input), &authors)

			if tc.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Len(t, authors.Author, tc.wantCount)

			for i, name := range tc.wantNames {
				assert.Equal(t, name, authors.Author[i].Text)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Author unmarshal integration with full response
// ---------------------------------------------------------------------------

func TestDBLPSingleAuthorInSearchResponse(t *testing.T) {
	t.Parallel()

	// Build a response with a single author (object, not array).
	singleAuthorHit := defaultDBLPTestHit2() // has 1 author
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildDBLPTestSearchJSON(1, []dblpTestHit{singleAuthorHit}))
	}))
	t.Cleanup(ts.Close)

	plugin := newDBLPTestPlugin(t, ts.URL)
	result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Len(t, result.Results[0].Authors, 1)
	assert.Equal(t, testDBLPAuthor2, result.Results[0].Authors[0].Name)
}

// ---------------------------------------------------------------------------
// Total as string parsing tests
// ---------------------------------------------------------------------------

func TestDBLPTotalAsString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		total     string
		wantTotal int
	}{
		{
			name:      "valid_number",
			total:     "1234",
			wantTotal: 1234,
		},
		{
			name:      "zero",
			total:     "0",
			wantTotal: 0,
		},
		{
			name:      "large_number",
			total:     "999999",
			wantTotal: 999999,
		},
		{
			name:      "non_numeric_defaults_to_zero",
			total:     "invalid",
			wantTotal: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			responseJSON := buildDBLPTestSearchJSONWithStringTotal(tc.total, []dblpTestHit{})

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, responseJSON)
			}))
			t.Cleanup(ts.Close)

			plugin := newDBLPTestPlugin(t, ts.URL)
			result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.wantTotal, result.Total)
		})
	}
}

// ---------------------------------------------------------------------------
// HTTP error tests
// ---------------------------------------------------------------------------

func TestDBLPHTTPErrors(t *testing.T) {
	t.Parallel()

	t.Run("server_500", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		plugin := newDBLPTestPlugin(t, ts.URL)
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

		plugin := newDBLPTestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})

	t.Run("context_canceled", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(testDBLPPluginTimeout)
		}))
		t.Cleanup(ts.Close)

		plugin := newDBLPTestPlugin(t, ts.URL)
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

		plugin := newDBLPTestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})
}

// ---------------------------------------------------------------------------
// Health tracking tests
// ---------------------------------------------------------------------------

func TestDBLPHealthTracking(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := callCount.Add(1)
		if count%2 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildDBLPTestSearchJSON(0, []dblpTestHit{}))
	}))
	t.Cleanup(ts.Close)

	plugin := newDBLPTestPlugin(t, ts.URL)
	ctx := context.Background()
	params := SearchParams{Query: "test", Limit: 10}

	// First call succeeds — healthy.
	_, err := plugin.Search(ctx, params, nil)
	require.NoError(t, err)
	health := plugin.Health(ctx)
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)

	// Second call fails — unhealthy.
	_, err = plugin.Search(ctx, params, nil)
	require.Error(t, err)
	health = plugin.Health(ctx)
	assert.False(t, health.Healthy)
	assert.NotEmpty(t, health.LastError)

	// Third call succeeds — healthy again.
	_, err = plugin.Search(ctx, params, nil)
	require.NoError(t, err)
	health = plugin.Health(ctx)
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)
}

// ---------------------------------------------------------------------------
// Initialize tests
// ---------------------------------------------------------------------------

func TestDBLPInitialize(t *testing.T) {
	t.Parallel()

	t.Run("default_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &DBLPPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, dblpDefaultBaseURL, plugin.baseURL)
	})

	t.Run("custom_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &DBLPPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			BaseURL: "http://custom.test",
		})
		require.NoError(t, err)
		assert.Equal(t, "http://custom.test", plugin.baseURL)
	})

	t.Run("default_timeout", func(t *testing.T) {
		t.Parallel()
		plugin := &DBLPPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, DefaultPluginTimeout, plugin.httpClient.Timeout)
	})

	t.Run("custom_timeout", func(t *testing.T) {
		t.Parallel()
		customTimeout := 30 * time.Second
		plugin := &DBLPPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			Timeout: Duration{Duration: customTimeout},
		})
		require.NoError(t, err)
		assert.Equal(t, customTimeout, plugin.httpClient.Timeout)
	})

	t.Run("rate_limit_reported", func(t *testing.T) {
		t.Parallel()
		plugin := &DBLPPlugin{}
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
		plugin := &DBLPPlugin{}
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

func TestDBLPConcurrentSafety(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildDBLPTestSearchJSON(1, []dblpTestHit{defaultDBLPTestHit1()}))
	}))
	t.Cleanup(ts.Close)

	plugin := newDBLPTestPlugin(t, ts.URL)
	ctx := context.Background()

	var wg sync.WaitGroup
	for range testDBLPConcurrentGoroutines {
		wg.Go(func() {
			_, _ = plugin.Search(ctx, SearchParams{Query: "test", Limit: 10}, nil)
			_, _ = plugin.Get(ctx, testDBLPKey1, nil, FormatNative, nil)
			_ = plugin.Health(ctx)
		})
	}
	wg.Wait()

	health := plugin.Health(ctx)
	assert.True(t, health.Healthy)
}

// ---------------------------------------------------------------------------
// URL builder tests
// ---------------------------------------------------------------------------

func TestBuildDBLPSearchURL(t *testing.T) {
	t.Parallel()

	t.Run("basic_search", func(t *testing.T) {
		t.Parallel()
		u := buildDBLPSearchURL(dblpDefaultBaseURL, SearchParams{
			Query: "attention",
			Limit: 10,
		})
		assert.Contains(t, u, dblpSearchPath)
		assert.Contains(t, u, "q=attention")
		assert.Contains(t, u, "format=json")
		assert.Contains(t, u, "h=10")
		assert.NotContains(t, u, "f=")
	})

	t.Run("with_offset", func(t *testing.T) {
		t.Parallel()
		u := buildDBLPSearchURL(dblpDefaultBaseURL, SearchParams{
			Query:  "test",
			Limit:  20,
			Offset: 40,
		})
		assert.Contains(t, u, "h=20")
		assert.Contains(t, u, "f=40")
	})

	t.Run("zero_offset_omitted", func(t *testing.T) {
		t.Parallel()
		u := buildDBLPSearchURL(dblpDefaultBaseURL, SearchParams{
			Query: "test",
			Limit: 10,
		})
		assert.NotContains(t, u, "f=")
	})

	t.Run("limit_capped_at_max", func(t *testing.T) {
		t.Parallel()
		u := buildDBLPSearchURL(dblpDefaultBaseURL, SearchParams{
			Query: "test",
			Limit: 5000,
		})
		assert.Contains(t, u, fmt.Sprintf("h=%d", dblpMaxResultsPerPage))
	})
}

func TestBuildDBLPGetURL(t *testing.T) {
	t.Parallel()

	u := buildDBLPGetURL(dblpDefaultBaseURL, testDBLPKey1)
	assert.Contains(t, u, dblpSearchPath)
	// key: is URL-encoded as %3A, slashes in key as %2F.
	assert.Contains(t, u, "q=key%3Ajournals%2Fcorr%2Fabs-2401-12345")
	assert.Contains(t, u, "h=1")
	assert.Contains(t, u, "format=json")
}

// ---------------------------------------------------------------------------
// Format conversion tests
// ---------------------------------------------------------------------------

func TestConvertDBLPFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		format  ContentFormat
		wantErr bool
	}{
		{"json_ok", FormatJSON, false},
		{"xml_unsupported", FormatXML, true},
		{"markdown_unsupported", FormatMarkdown, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pub := &Publication{}
			err := convertDBLPFormat(pub, tc.format)
			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFormatUnsupported)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Nil authors handling test
// ---------------------------------------------------------------------------

func TestDBLPNilAuthors(t *testing.T) {
	t.Parallel()

	// Build a response with null authors.
	responseJSON := `{"result":{"hits":{"@total":"1","hit":[{"info":{"key":"test/key","title":"No Authors","authors":null,"venue":"TestVenue","year":"2024","type":"Article","doi":"","ee":"","url":""}}]}}}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, responseJSON)
	}))
	t.Cleanup(ts.Close)

	plugin := newDBLPTestPlugin(t, ts.URL)
	result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	assert.Nil(t, result.Results[0].Authors)
}
