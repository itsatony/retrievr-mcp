package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	testS2PaperID1   = "abc123def456789012345678901234567890abcd"
	testS2PaperID2   = "def789ghi012345678901234567890123456789a"
	testS2Title1     = "Deep Learning for Protein Structure Prediction"
	testS2Title2     = "Graph Neural Networks: A Comprehensive Survey"
	testS2Abstract1  = "We present a novel deep learning approach for protein structure prediction."
	testS2Abstract2  = "This paper surveys graph neural network architectures."
	testS2Author1    = "Alice Researcher"
	testS2Author2    = "Bob Scientist"
	testS2AuthorID1  = "12345"
	testS2AuthorID2  = "67890"
	testS2DOI1       = "10.1234/s2test.2024.001"
	testS2ArXivID1   = "2401.12345"
	testS2CorpusID1  = 12345
	testS2Category1  = "Computer Science"
	testS2Category2  = "Biology"
	testS2Date1      = "2024-01-15"
	testS2Date2      = "2024-03-20"
	testS2Journal1   = "Nature"
	testS2Volume1    = "625"
	testS2Pages1     = "123-130"
	testS2PubType1   = "JournalArticle"
	testS2Year1      = 2024
	testS2Year2      = 2023
	testS2Citations1 = 42
	testS2RefCount1  = 30
	testS2PDFURL1    = "https://example.com/paper.pdf"
	testS2URL1       = "https://www.semanticscholar.org/paper/abc123"
	testS2URL2       = "https://www.semanticscholar.org/paper/def789"

	testS2PluginTimeout        = 5 * time.Second
	testS2TotalResults         = 150
	testS2ConcurrentGoroutines = 10

	testS2APIKey = "test-s2-api-key"
)

// ---------------------------------------------------------------------------
// JSON fixture builder types
// ---------------------------------------------------------------------------

type s2TestPaper struct {
	PaperID         string
	Title           string
	Abstract        string
	Year            int
	Authors         []s2TestAuthor
	DOI             string
	ArXivID         string
	PMID            string
	CorpusID        int
	CitationCount   int
	ReferenceCount  int
	PublicationDate string
	JournalName     string
	JournalVolume   string
	JournalPages    string
	OpenAccessURL   string
	FieldsOfStudy   []string
	URL             string
	IsOpenAccess    bool
	PubTypes        []string
}

type s2TestAuthor struct {
	AuthorID string
	Name     string
}

// ---------------------------------------------------------------------------
// JSON fixture builders
// ---------------------------------------------------------------------------

// buildS2TestSearchJSON generates a complete S2 search response JSON string.
func buildS2TestSearchJSON(total, offset int, next *int, papers []s2TestPaper) string {
	var nextJSON string
	if next != nil {
		nextJSON = fmt.Sprintf("%d", *next)
	} else {
		nextJSON = "null"
	}

	papersJSON := "[]"
	if len(papers) > 0 {
		items := make([]string, 0, len(papers))
		for _, p := range papers {
			items = append(items, buildS2TestPaperJSON(p))
		}
		papersJSON = "[" + joinStrings(items, ",") + "]"
	}

	return fmt.Sprintf(`{"total":%d,"offset":%d,"next":%s,"data":%s}`,
		total, offset, nextJSON, papersJSON)
}

// buildS2TestPaperJSON generates a single S2 paper JSON object.
func buildS2TestPaperJSON(p s2TestPaper) string {
	// Build externalIds.
	externalIDs := "null"
	if p.DOI != "" || p.ArXivID != "" || p.PMID != "" || p.CorpusID > 0 {
		externalIDs = fmt.Sprintf(`{"DOI":%s,"ArXiv":%s,"PMID":%s,"CorpusId":%d}`,
			jsonString(p.DOI), jsonString(p.ArXivID), jsonString(p.PMID), p.CorpusID)
	}

	// Build authors.
	authorsJSON := "[]"
	if len(p.Authors) > 0 {
		items := make([]string, 0, len(p.Authors))
		for _, a := range p.Authors {
			items = append(items, fmt.Sprintf(`{"authorId":%s,"name":%s}`,
				jsonString(a.AuthorID), jsonString(a.Name)))
		}
		authorsJSON = "[" + joinStrings(items, ",") + "]"
	}

	// Build journal.
	journalJSON := "null"
	if p.JournalName != "" {
		journalJSON = fmt.Sprintf(`{"name":%s,"volume":%s,"pages":%s}`,
			jsonString(p.JournalName), jsonString(p.JournalVolume), jsonString(p.JournalPages))
	}

	// Build openAccessPdf.
	oaPdfJSON := "null"
	if p.OpenAccessURL != "" {
		oaPdfJSON = fmt.Sprintf(`{"url":%s}`, jsonString(p.OpenAccessURL))
	}

	// Build fieldsOfStudy.
	fosJSON := "null"
	if len(p.FieldsOfStudy) > 0 {
		items := make([]string, 0, len(p.FieldsOfStudy))
		for _, f := range p.FieldsOfStudy {
			items = append(items, jsonString(f))
		}
		fosJSON = "[" + joinStrings(items, ",") + "]"
	}

	// Build publicationTypes.
	ptJSON := "null"
	if len(p.PubTypes) > 0 {
		items := make([]string, 0, len(p.PubTypes))
		for _, pt := range p.PubTypes {
			items = append(items, jsonString(pt))
		}
		ptJSON = "[" + joinStrings(items, ",") + "]"
	}

	return fmt.Sprintf(`{`+
		`"paperId":%s,`+
		`"externalIds":%s,`+
		`"title":%s,`+
		`"abstract":%s,`+
		`"year":%d,`+
		`"authors":%s,`+
		`"citationCount":%d,`+
		`"referenceCount":%d,`+
		`"publicationDate":%s,`+
		`"journal":%s,`+
		`"openAccessPdf":%s,`+
		`"fieldsOfStudy":%s,`+
		`"url":%s,`+
		`"isOpenAccess":%t,`+
		`"publicationTypes":%s`+
		`}`,
		jsonString(p.PaperID),
		externalIDs,
		jsonString(p.Title),
		jsonString(p.Abstract),
		p.Year,
		authorsJSON,
		p.CitationCount,
		p.ReferenceCount,
		jsonString(p.PublicationDate),
		journalJSON,
		oaPdfJSON,
		fosJSON,
		jsonString(p.URL),
		p.IsOpenAccess,
		ptJSON,
	)
}

// buildS2TestCitationsJSON generates an S2 citations/references response.
func buildS2TestCitationsJSON(wrapKey string, papers []s2TestPaper) string {
	items := make([]string, 0, len(papers))
	for _, p := range papers {
		items = append(items, fmt.Sprintf(`{%q:%s}`, wrapKey, buildS2TestPaperJSON(p)))
	}
	return fmt.Sprintf(`{"offset":0,"next":null,"data":[%s]}`, joinStrings(items, ","))
}

// joinStrings joins strings with a separator.
func joinStrings(items []string, sep string) string {
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(item)
	}
	return b.String()
}

// jsonString returns a JSON-encoded string value (with quotes, escaping).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ---------------------------------------------------------------------------
// Default test papers
// ---------------------------------------------------------------------------

func defaultS2TestPaper1() s2TestPaper {
	return s2TestPaper{
		PaperID:         testS2PaperID1,
		Title:           testS2Title1,
		Abstract:        testS2Abstract1,
		Year:            testS2Year1,
		Authors:         []s2TestAuthor{{AuthorID: testS2AuthorID1, Name: testS2Author1}},
		DOI:             testS2DOI1,
		ArXivID:         testS2ArXivID1,
		CorpusID:        testS2CorpusID1,
		CitationCount:   testS2Citations1,
		ReferenceCount:  testS2RefCount1,
		PublicationDate: testS2Date1,
		JournalName:     testS2Journal1,
		JournalVolume:   testS2Volume1,
		JournalPages:    testS2Pages1,
		OpenAccessURL:   testS2PDFURL1,
		FieldsOfStudy:   []string{testS2Category1, testS2Category2},
		URL:             testS2URL1,
		IsOpenAccess:    true,
		PubTypes:        []string{testS2PubType1},
	}
}

func defaultS2TestPaper2() s2TestPaper {
	return s2TestPaper{
		PaperID:         testS2PaperID2,
		Title:           testS2Title2,
		Abstract:        testS2Abstract2,
		Year:            testS2Year2,
		Authors:         []s2TestAuthor{{AuthorID: testS2AuthorID2, Name: testS2Author2}},
		PublicationDate: testS2Date2,
		FieldsOfStudy:   []string{testS2Category1},
		URL:             testS2URL2,
	}
}

// ---------------------------------------------------------------------------
// httptest server factory
// ---------------------------------------------------------------------------

// newS2TestPlugin creates an S2Plugin pointing at a custom base URL.
func newS2TestPlugin(t *testing.T, baseURL string) *S2Plugin {
	t.Helper()
	plugin := &S2Plugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testS2PluginTimeout},
		RateLimit: 1.0,
	})
	require.NoError(t, err)
	return plugin
}

// newS2TestPluginWithAPIKey creates an S2Plugin with a server-level API key.
func newS2TestPluginWithAPIKey(t *testing.T, baseURL, apiKey string) *S2Plugin {
	t.Helper()
	plugin := &S2Plugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		Timeout:   Duration{Duration: testS2PluginTimeout},
		RateLimit: 1.0,
	})
	require.NoError(t, err)
	return plugin
}

// ---------------------------------------------------------------------------
// Contract test
// ---------------------------------------------------------------------------

func TestS2PluginContract(t *testing.T) {
	plugin := newS2TestPlugin(t, "http://unused.test")
	PluginContractTest(t, plugin)
}

// ---------------------------------------------------------------------------
// Search tests
// ---------------------------------------------------------------------------

func TestS2Search(t *testing.T) {
	t.Parallel()

	nextOffset := 10

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
				Query: "protein structure",
				Limit: 10,
			},
			responseJSON:   buildS2TestSearchJSON(testS2TotalResults, 0, &nextOffset, []s2TestPaper{defaultS2TestPaper1()}),
			wantResultsCnt: 1,
			wantTotal:      testS2TotalResults,
			wantHasMore:    true,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "protein structure", r.URL.Query().Get(s2ParamQuery))
				assert.Equal(t, "10", r.URL.Query().Get(s2ParamLimit))
				assert.Equal(t, "0", r.URL.Query().Get(s2ParamOffset))
				assert.NotEmpty(t, r.URL.Query().Get(s2ParamFields))
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
			responseJSON:   buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{defaultS2TestPaper1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				dateParam := r.URL.Query().Get(s2ParamPublicationDateOrYear)
				assert.Equal(t, "2024-01-01:2024-06-30", dateParam)
			},
		},
		{
			name: "search_with_date_filter_year_only",
			params: SearchParams{
				Query: "transformers",
				Filters: SearchFilters{
					DateFrom: "2023",
					DateTo:   "2024",
				},
				Limit: 10,
			},
			responseJSON:   buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{defaultS2TestPaper1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				dateParam := r.URL.Query().Get(s2ParamPublicationDateOrYear)
				assert.Equal(t, "2023-01-01:2024-12-31", dateParam)
			},
		},
		{
			name: "search_with_date_from_only",
			params: SearchParams{
				Query:   "attention",
				Filters: SearchFilters{DateFrom: "2024-03-15"},
				Limit:   10,
			},
			responseJSON:   buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{defaultS2TestPaper1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				dateParam := r.URL.Query().Get(s2ParamPublicationDateOrYear)
				assert.Equal(t, "2024-03-15:", dateParam)
			},
		},
		{
			name: "search_with_date_to_only",
			params: SearchParams{
				Query:   "diffusion",
				Filters: SearchFilters{DateTo: "2024-06-30"},
				Limit:   10,
			},
			responseJSON:   buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{defaultS2TestPaper1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				dateParam := r.URL.Query().Get(s2ParamPublicationDateOrYear)
				assert.Equal(t, ":2024-06-30", dateParam)
			},
		},
		{
			name: "search_pagination",
			params: SearchParams{
				Query:  "reinforcement learning",
				Limit:  20,
				Offset: 40,
			},
			responseJSON:   buildS2TestSearchJSON(100, 40, &nextOffset, []s2TestPaper{defaultS2TestPaper1()}),
			wantResultsCnt: 1,
			wantTotal:      100,
			wantHasMore:    true,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "40", r.URL.Query().Get(s2ParamOffset))
				assert.Equal(t, "20", r.URL.Query().Get(s2ParamLimit))
			},
		},
		{
			name: "search_has_more_false",
			params: SearchParams{
				Query: "rare topic",
				Limit: 10,
			},
			responseJSON:   buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{defaultS2TestPaper1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
		},
		{
			name: "search_empty_results",
			params: SearchParams{
				Query: "xyznonexistenttopic",
				Limit: 10,
			},
			responseJSON:   buildS2TestSearchJSON(0, 0, nil, nil),
			wantResultsCnt: 0,
			wantTotal:      0,
			wantHasMore:    false,
		},
		{
			name: "search_empty_query",
			params: SearchParams{
				Limit: 10,
			},
			wantErr: ErrS2EmptyQuery,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.wantErr != nil {
				plugin := newS2TestPlugin(t, "http://unused.test")
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

			plugin := newS2TestPlugin(t, ts.URL)
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
// Search with API key test
// ---------------------------------------------------------------------------

func TestS2SearchWithAPIKey(t *testing.T) {
	t.Parallel()

	t.Run("server_level_api_key", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, testS2APIKey, r.Header.Get(s2APIKeyHeader))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildS2TestSearchJSON(0, 0, nil, nil))
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPluginWithAPIKey(t, ts.URL, testS2APIKey)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
	})

	t.Run("per_call_api_key_overrides_server", func(t *testing.T) {
		t.Parallel()

		perCallKey := "per-call-key-override"
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, perCallKey, r.Header.Get(s2APIKeyHeader))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildS2TestSearchJSON(0, 0, nil, nil))
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPluginWithAPIKey(t, ts.URL, testS2APIKey)
		creds := &CallCredentials{S2APIKey: perCallKey}
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, creds)
		require.NoError(t, err)
	})

	t.Run("no_api_key_no_header", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Empty(t, r.Header.Get(s2APIKeyHeader))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildS2TestSearchJSON(0, 0, nil, nil))
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// Search result mapping tests
// ---------------------------------------------------------------------------

func TestS2SearchResultMapping(t *testing.T) {
	t.Parallel()

	responseJSON := buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{defaultS2TestPaper1()})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, responseJSON)
	}))
	t.Cleanup(ts.Close)

	plugin := newS2TestPlugin(t, ts.URL)
	result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)

	pub := result.Results[0]

	// Identity.
	assert.Equal(t, SourceS2+prefixedIDSeparator+testS2PaperID1, pub.ID)
	assert.Equal(t, SourceS2, pub.Source)
	assert.Equal(t, ContentTypePaper, pub.ContentType)

	// Core fields.
	assert.Equal(t, testS2Title1, pub.Title)
	assert.Equal(t, testS2Abstract1, pub.Abstract)
	assert.Equal(t, testS2URL1, pub.URL)
	assert.Equal(t, testS2Date1, pub.Published)

	// Authors.
	require.Len(t, pub.Authors, 1)
	assert.Equal(t, testS2Author1, pub.Authors[0].Name)

	// External IDs.
	assert.Equal(t, testS2DOI1, pub.DOI)
	assert.Equal(t, testS2ArXivID1, pub.ArXivID)

	// Citation count.
	require.NotNil(t, pub.CitationCount)
	assert.Equal(t, testS2Citations1, *pub.CitationCount)

	// PDF URL.
	assert.Equal(t, testS2PDFURL1, pub.PDFURL)

	// Categories from fieldsOfStudy.
	assert.Equal(t, []string{testS2Category1, testS2Category2}, pub.Categories)

	// Source metadata.
	require.NotNil(t, pub.SourceMetadata)
	assert.Equal(t, testS2Journal1, pub.SourceMetadata[s2MetaKeyJournal])
	assert.Equal(t, []string{testS2Category1, testS2Category2}, pub.SourceMetadata[s2MetaKeyFieldsOfStudy])
	assert.Equal(t, []string{testS2PubType1}, pub.SourceMetadata[s2MetaKeyPublicationTypes])
	assert.Equal(t, testS2CorpusID1, pub.SourceMetadata[s2MetaKeyCorpusID])
	assert.Equal(t, testS2RefCount1, pub.SourceMetadata[s2MetaKeyReferenceCount])
	assert.Equal(t, true, pub.SourceMetadata[s2MetaKeyIsOpenAccess])
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestS2Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		id        string
		format    ContentFormat
		paperJSON string
		wantErr   error
		wantTitle string
		wantFT    bool // expect FullText to be non-nil
	}{
		{
			name:      "get_by_id_native",
			id:        testS2PaperID1,
			format:    FormatNative,
			paperJSON: buildS2TestPaperJSON(defaultS2TestPaper1()),
			wantTitle: testS2Title1,
			wantFT:    false,
		},
		{
			name:      "get_format_json",
			id:        testS2PaperID1,
			format:    FormatJSON,
			paperJSON: buildS2TestPaperJSON(defaultS2TestPaper1()),
			wantTitle: testS2Title1,
			wantFT:    false,
		},
		{
			name:      "get_format_bibtex",
			id:        testS2PaperID1,
			format:    FormatBibTeX,
			paperJSON: buildS2TestPaperJSON(defaultS2TestPaper1()),
			wantTitle: testS2Title1,
			wantFT:    true,
		},
		{
			name:      "get_format_unsupported",
			id:        testS2PaperID1,
			format:    FormatMarkdown,
			paperJSON: buildS2TestPaperJSON(defaultS2TestPaper1()),
			wantErr:   ErrFormatUnsupported,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.paperJSON)
			}))
			t.Cleanup(ts.Close)

			plugin := newS2TestPlugin(t, ts.URL)
			pub, err := plugin.Get(context.Background(), tc.id, nil, tc.format, nil)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErr), "expected %v, got %v", tc.wantErr, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, pub)
			assert.Equal(t, tc.wantTitle, pub.Title)

			if tc.wantFT {
				require.NotNil(t, pub.FullText)
				assert.NotEmpty(t, pub.FullText.Content)
				assert.Equal(t, tc.format, pub.FullText.ContentFormat)
				assert.Greater(t, pub.FullText.ContentLength, 0)
			} else {
				assert.Nil(t, pub.FullText)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Get not found test
// ---------------------------------------------------------------------------

func TestS2GetNotFound(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)

	plugin := newS2TestPlugin(t, ts.URL)
	_, err := plugin.Get(context.Background(), "nonexistent", nil, FormatNative, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGetFailed)
}

// ---------------------------------------------------------------------------
// Get with citations and references
// ---------------------------------------------------------------------------

func TestS2GetWithCitationsAndReferences(t *testing.T) {
	t.Parallel()

	citingPaper := s2TestPaper{
		PaperID: "citing001",
		Title:   "Citing Paper One",
		Year:    testS2Year1,
		Authors: []s2TestAuthor{{AuthorID: "999", Name: "Citing Author"}},
	}
	citedPaper := s2TestPaper{
		PaperID: "cited001",
		Title:   "Referenced Paper One",
		Year:    testS2Year2,
		Authors: []s2TestAuthor{{AuthorID: "888", Name: "Referenced Author"}},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case containsPath(r.URL.Path, s2CitationsPath):
			fmt.Fprint(w, buildS2TestCitationsJSON("citingPaper", []s2TestPaper{citingPaper}))
		case containsPath(r.URL.Path, s2ReferencesPath):
			fmt.Fprint(w, buildS2TestCitationsJSON("citedPaper", []s2TestPaper{citedPaper}))
		default:
			fmt.Fprint(w, buildS2TestPaperJSON(defaultS2TestPaper1()))
		}
	}))
	t.Cleanup(ts.Close)

	plugin := newS2TestPlugin(t, ts.URL)

	t.Run("with_citations", func(t *testing.T) {
		t.Parallel()
		pub, err := plugin.Get(context.Background(), testS2PaperID1,
			[]IncludeField{IncludeCitations}, FormatNative, nil)
		require.NoError(t, err)
		require.NotNil(t, pub)
		require.Len(t, pub.Citations, 1)
		assert.Equal(t, "Citing Paper One", pub.Citations[0].Title)
		assert.Equal(t, testS2Year1, pub.Citations[0].Year)
		assert.Contains(t, pub.Citations[0].ID, SourceS2+prefixedIDSeparator)
	})

	t.Run("with_references", func(t *testing.T) {
		t.Parallel()
		pub, err := plugin.Get(context.Background(), testS2PaperID1,
			[]IncludeField{IncludeReferences}, FormatNative, nil)
		require.NoError(t, err)
		require.NotNil(t, pub)
		require.Len(t, pub.References, 1)
		assert.Equal(t, "Referenced Paper One", pub.References[0].Title)
		assert.Equal(t, testS2Year2, pub.References[0].Year)
	})

	t.Run("with_both", func(t *testing.T) {
		t.Parallel()
		pub, err := plugin.Get(context.Background(), testS2PaperID1,
			[]IncludeField{IncludeCitations, IncludeReferences}, FormatNative, nil)
		require.NoError(t, err)
		require.NotNil(t, pub)
		assert.Len(t, pub.Citations, 1)
		assert.Len(t, pub.References, 1)
	})
}

// containsPath checks if a URL path contains the given suffix.
func containsPath(path, suffix string) bool {
	return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
}

// ---------------------------------------------------------------------------
// Get with API key test
// ---------------------------------------------------------------------------

func TestS2GetWithAPIKey(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, testS2APIKey, r.Header.Get(s2APIKeyHeader))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildS2TestPaperJSON(defaultS2TestPaper1()))
	}))
	t.Cleanup(ts.Close)

	plugin := newS2TestPluginWithAPIKey(t, ts.URL, testS2APIKey)
	pub, err := plugin.Get(context.Background(), testS2PaperID1, nil, FormatNative, nil)
	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.Equal(t, testS2Title1, pub.Title)
}

// ---------------------------------------------------------------------------
// URL builder tests
// ---------------------------------------------------------------------------

func TestS2URLBuilders(t *testing.T) {
	t.Parallel()

	baseURL := "https://api.test.com/graph/v1"

	t.Run("search_url_basic", func(t *testing.T) {
		t.Parallel()
		params := SearchParams{Query: "attention", Limit: 10, Offset: 0}
		u := buildS2SearchURL(baseURL, params)
		assert.Contains(t, u, baseURL+s2PaperSearchPath)
		assert.Contains(t, u, s2ParamQuery+"=attention")
		assert.Contains(t, u, s2ParamLimit+"=10")
		assert.Contains(t, u, s2ParamOffset+"=0")
		assert.Contains(t, u, s2ParamFields+"=")
	})

	t.Run("search_url_with_date", func(t *testing.T) {
		t.Parallel()
		params := SearchParams{
			Query:   "test",
			Limit:   10,
			Filters: SearchFilters{DateFrom: "2024-01-01", DateTo: "2024-12-31"},
		}
		u := buildS2SearchURL(baseURL, params)
		assert.Contains(t, u, s2ParamPublicationDateOrYear+"=")
	})

	t.Run("get_url", func(t *testing.T) {
		t.Parallel()
		u := buildS2GetURL(baseURL, testS2PaperID1)
		assert.Contains(t, u, baseURL+s2PaperGetPath+testS2PaperID1)
		assert.Contains(t, u, s2ParamFields+"=")
	})

	t.Run("citations_url", func(t *testing.T) {
		t.Parallel()
		u := buildS2CitationsURL(baseURL, testS2PaperID1)
		assert.Contains(t, u, baseURL+s2PaperGetPath+testS2PaperID1+s2CitationsPath)
		assert.Contains(t, u, s2ParamFields+"=")
	})

	t.Run("references_url", func(t *testing.T) {
		t.Parallel()
		u := buildS2ReferencesURL(baseURL, testS2PaperID1)
		assert.Contains(t, u, baseURL+s2PaperGetPath+testS2PaperID1+s2ReferencesPath)
		assert.Contains(t, u, s2ParamFields+"=")
	})
}

// ---------------------------------------------------------------------------
// Date filter tests
// ---------------------------------------------------------------------------

func TestS2DateFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dateFrom string
		dateTo   string
		want     string
	}{
		{name: "both_full_dates", dateFrom: "2024-01-01", dateTo: "2024-06-30", want: "2024-01-01:2024-06-30"},
		{name: "both_year_only", dateFrom: "2023", dateTo: "2024", want: "2023-01-01:2024-12-31"},
		{name: "from_only_full", dateFrom: "2024-03-15", want: "2024-03-15:"},
		{name: "to_only_full", dateTo: "2024-06-30", want: ":2024-06-30"},
		{name: "from_year_only", dateFrom: "2024", want: "2024-01-01:"},
		{name: "to_year_only", dateTo: "2024", want: ":2024-12-31"},
		{name: "empty", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, buildS2DateFilter(tc.dateFrom, tc.dateTo))
		})
	}
}

// ---------------------------------------------------------------------------
// Date normalization tests
// ---------------------------------------------------------------------------

func TestS2NormalizeDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		date      string
		isEndDate bool
		want      string
	}{
		{name: "full_date_start", date: "2024-01-15", isEndDate: false, want: "2024-01-15"},
		{name: "full_date_end", date: "2024-06-30", isEndDate: true, want: "2024-06-30"},
		{name: "year_only_start", date: "2024", isEndDate: false, want: "2024-01-01"},
		{name: "year_only_end", date: "2024", isEndDate: true, want: "2024-12-31"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, normalizeS2Date(tc.date, tc.isEndDate))
		})
	}
}

// ---------------------------------------------------------------------------
// Health tracking tests
// ---------------------------------------------------------------------------

func TestS2HealthTracking(t *testing.T) {
	t.Parallel()

	// Setup: server that alternates success/failure.
	var callCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		if n == 2 { //nolint:mnd // second call fails
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{defaultS2TestPaper1()}))
	}))
	t.Cleanup(ts.Close)

	plugin := newS2TestPlugin(t, ts.URL)

	// Initially healthy.
	health := plugin.Health(context.Background())
	assert.True(t, health.Enabled)
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)

	// First search succeeds — stays healthy.
	_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
	require.NoError(t, err)
	health = plugin.Health(context.Background())
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)

	// Second search fails (500) — becomes unhealthy.
	_, err = plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
	require.Error(t, err)
	health = plugin.Health(context.Background())
	assert.False(t, health.Healthy)
	assert.NotEmpty(t, health.LastError)

	// Third search succeeds — recovers to healthy.
	_, err = plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
	require.NoError(t, err)
	health = plugin.Health(context.Background())
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)
}

// ---------------------------------------------------------------------------
// HTTP error tests
// ---------------------------------------------------------------------------

func TestS2HTTPErrors(t *testing.T) {
	t.Parallel()

	t.Run("server_500", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})

	t.Run("server_404_search", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})

	t.Run("server_404_get", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		_, err := plugin.Get(context.Background(), testS2PaperID1, nil, FormatNative, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGetFailed)
	})

	t.Run("context_canceled", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(testS2PluginTimeout)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately.

		_, err := plugin.Search(ctx, SearchParams{Query: "test", Limit: 1}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})

	t.Run("malformed_json", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "{invalid json")
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})
}

// ---------------------------------------------------------------------------
// BibTeX assembly tests
// ---------------------------------------------------------------------------

func TestS2BibTeXAssembly(t *testing.T) {
	t.Parallel()

	t.Run("single_author_with_doi", func(t *testing.T) {
		t.Parallel()
		pub := &Publication{
			ID:        SourceS2 + prefixedIDSeparator + testS2PaperID1,
			Title:     testS2Title1,
			Authors:   []Author{{Name: testS2Author1}},
			Published: testS2Date1,
			DOI:       testS2DOI1,
			URL:       testS2URL1,
		}
		bibtex := assembleS2BibTeX(pub)
		assert.Contains(t, bibtex, "@article{"+s2BibTeXKeyPrefix)
		assert.Contains(t, bibtex, testS2Title1)
		assert.Contains(t, bibtex, testS2Author1)
		assert.Contains(t, bibtex, "year   = {2024}")
		assert.Contains(t, bibtex, testS2DOI1)
		assert.Contains(t, bibtex, testS2URL1)
	})

	t.Run("multiple_authors", func(t *testing.T) {
		t.Parallel()
		pub := &Publication{
			ID:        SourceS2 + prefixedIDSeparator + testS2PaperID1,
			Title:     testS2Title1,
			Authors:   []Author{{Name: testS2Author1}, {Name: testS2Author2}},
			Published: testS2Date1,
			URL:       testS2URL1,
		}
		bibtex := assembleS2BibTeX(pub)
		assert.Contains(t, bibtex, testS2Author1+s2BibTeXAuthorSeparator+testS2Author2)
	})

	t.Run("no_doi", func(t *testing.T) {
		t.Parallel()
		pub := &Publication{
			ID:        SourceS2 + prefixedIDSeparator + testS2PaperID1,
			Title:     testS2Title1,
			Authors:   []Author{{Name: testS2Author1}},
			Published: testS2Date1,
			URL:       testS2URL1,
		}
		bibtex := assembleS2BibTeX(pub)
		assert.Contains(t, bibtex, "doi    = {}")
	})
}

// ---------------------------------------------------------------------------
// Initialize tests
// ---------------------------------------------------------------------------

func TestS2Initialize(t *testing.T) {
	t.Parallel()

	t.Run("default_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &S2Plugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled:   true,
			RateLimit: 1.0,
		})
		require.NoError(t, err)
		assert.Equal(t, s2DefaultBaseURL, plugin.baseURL)
		assert.True(t, plugin.enabled)
		assert.True(t, plugin.healthy)
	})

	t.Run("custom_base_url", func(t *testing.T) {
		t.Parallel()
		customURL := "http://custom.test/api"
		plugin := &S2Plugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			BaseURL: customURL,
		})
		require.NoError(t, err)
		assert.Equal(t, customURL, plugin.baseURL)
	})

	t.Run("default_timeout", func(t *testing.T) {
		t.Parallel()
		plugin := &S2Plugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, DefaultPluginTimeout, plugin.httpClient.Timeout)
	})

	t.Run("custom_timeout", func(t *testing.T) {
		t.Parallel()
		customTimeout := 30 * time.Second
		plugin := &S2Plugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			Timeout: Duration{Duration: customTimeout},
		})
		require.NoError(t, err)
		assert.Equal(t, customTimeout, plugin.httpClient.Timeout)
	})

	t.Run("api_key_stored", func(t *testing.T) {
		t.Parallel()
		plugin := &S2Plugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			APIKey:  testS2APIKey,
		})
		require.NoError(t, err)
		assert.Equal(t, testS2APIKey, plugin.apiKey)
	})

	t.Run("rate_limit_reported", func(t *testing.T) {
		t.Parallel()
		plugin := &S2Plugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled:   true,
			RateLimit: 1.0,
		})
		require.NoError(t, err)
		health := plugin.Health(context.Background())
		assert.InDelta(t, 1.0, health.RateLimit, 0.001)
	})
}

// ---------------------------------------------------------------------------
// Concurrent access test
// ---------------------------------------------------------------------------

func TestS2ConcurrentAccess(t *testing.T) {
	t.Parallel()

	responseJSON := buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{defaultS2TestPaper1()})
	paperJSON := buildS2TestPaperJSON(defaultS2TestPaper1())

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return search response for search path, paper for get path.
		if containsPath(r.URL.Path, s2PaperSearchPath) {
			fmt.Fprint(w, responseJSON)
		} else {
			fmt.Fprint(w, paperJSON)
		}
	}))
	t.Cleanup(ts.Close)

	plugin := newS2TestPlugin(t, ts.URL)

	var wg sync.WaitGroup
	for range testS2ConcurrentGoroutines {
		wg.Go(func() {
			// Mix of Search, Get, and Health calls.
			_, _ = plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
			_, _ = plugin.Get(context.Background(), testS2PaperID1, nil, FormatNative, nil)
			_ = plugin.Health(context.Background())
		})
	}
	wg.Wait()

	// If we get here without -race detector complaints, we're good.
	health := plugin.Health(context.Background())
	assert.True(t, health.Enabled)
}

// ---------------------------------------------------------------------------
// JSON parsing edge cases
// ---------------------------------------------------------------------------

func TestS2JSONParsingEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("nil_external_ids", func(t *testing.T) {
		t.Parallel()
		paper := defaultS2TestPaper1()
		paper.DOI = ""
		paper.ArXivID = ""
		paper.PMID = ""
		paper.CorpusID = 0

		responseJSON := buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{paper})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responseJSON)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)

		pub := result.Results[0]
		assert.Empty(t, pub.DOI)
		assert.Empty(t, pub.ArXivID)
	})

	t.Run("nil_open_access_pdf", func(t *testing.T) {
		t.Parallel()
		paper := defaultS2TestPaper1()
		paper.OpenAccessURL = ""

		responseJSON := buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{paper})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responseJSON)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Empty(t, result.Results[0].PDFURL)
	})

	t.Run("nil_journal", func(t *testing.T) {
		t.Parallel()
		paper := defaultS2TestPaper1()
		paper.JournalName = ""

		responseJSON := buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{paper})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responseJSON)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Nil(t, result.Results[0].SourceMetadata[s2MetaKeyJournal])
	})

	t.Run("empty_fields_of_study", func(t *testing.T) {
		t.Parallel()
		paper := defaultS2TestPaper1()
		paper.FieldsOfStudy = nil

		responseJSON := buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{paper})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responseJSON)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Nil(t, result.Results[0].Categories)
	})

	t.Run("year_only_no_publication_date", func(t *testing.T) {
		t.Parallel()
		paper := defaultS2TestPaper1()
		paper.PublicationDate = ""
		paper.Year = testS2Year1

		responseJSON := buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{paper})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responseJSON)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Equal(t, "2024", result.Results[0].Published)
	})

	t.Run("no_year_no_date", func(t *testing.T) {
		t.Parallel()
		paper := defaultS2TestPaper1()
		paper.PublicationDate = ""
		paper.Year = 0

		responseJSON := buildS2TestSearchJSON(1, 0, nil, []s2TestPaper{paper})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responseJSON)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)
		assert.Empty(t, result.Results[0].Published)
	})

	t.Run("multiple_papers_in_response", func(t *testing.T) {
		t.Parallel()
		papers := []s2TestPaper{defaultS2TestPaper1(), defaultS2TestPaper2()}

		next := 2
		responseJSON := buildS2TestSearchJSON(testS2TotalResults, 0, &next, papers)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responseJSON)
		}))
		t.Cleanup(ts.Close)

		plugin := newS2TestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		assert.Len(t, result.Results, 2)
		assert.Equal(t, testS2Title1, result.Results[0].Title)
		assert.Equal(t, testS2Title2, result.Results[1].Title)
	})
}

// ---------------------------------------------------------------------------
// mapS2Date unit tests
// ---------------------------------------------------------------------------

func TestMapS2Date(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		publicationDate string
		year            int
		want            string
	}{
		{name: "full_date", publicationDate: "2024-01-15", year: 2024, want: "2024-01-15"},
		{name: "year_only", publicationDate: "", year: 2024, want: "2024"},
		{name: "no_date_no_year", publicationDate: "", year: 0, want: ""},
		{name: "date_preferred_over_year", publicationDate: "2024-06-30", year: 2023, want: "2024-06-30"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, mapS2Date(tc.publicationDate, tc.year))
		})
	}
}

// ---------------------------------------------------------------------------
// mapS2PaperToReference unit tests
// ---------------------------------------------------------------------------

func TestMapS2PaperToReference(t *testing.T) {
	t.Parallel()

	t.Run("with_paper_id", func(t *testing.T) {
		t.Parallel()
		paper := &s2Paper{
			PaperID: testS2PaperID1,
			Title:   testS2Title1,
			Year:    testS2Year1,
		}
		ref := mapS2PaperToReference(paper)
		assert.Equal(t, SourceS2+prefixedIDSeparator+testS2PaperID1, ref.ID)
		assert.Equal(t, testS2Title1, ref.Title)
		assert.Equal(t, testS2Year1, ref.Year)
	})

	t.Run("without_paper_id", func(t *testing.T) {
		t.Parallel()
		paper := &s2Paper{
			Title: testS2Title1,
			Year:  testS2Year1,
		}
		ref := mapS2PaperToReference(paper)
		assert.Empty(t, ref.ID)
		assert.Equal(t, testS2Title1, ref.Title)
	})
}

// ---------------------------------------------------------------------------
// resolveS2APIKey unit tests
// ---------------------------------------------------------------------------

func TestResolveS2APIKey(t *testing.T) {
	t.Parallel()

	t.Run("nil_creds_uses_server_default", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "server-key", resolveS2APIKey(nil, "server-key"))
	})

	t.Run("empty_per_call_uses_server_default", func(t *testing.T) {
		t.Parallel()
		creds := &CallCredentials{}
		assert.Equal(t, "server-key", resolveS2APIKey(creds, "server-key"))
	})

	t.Run("per_call_overrides_server", func(t *testing.T) {
		t.Parallel()
		creds := &CallCredentials{S2APIKey: "per-call-key"}
		assert.Equal(t, "per-call-key", resolveS2APIKey(creds, "server-key"))
	})

	t.Run("both_empty", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "", resolveS2APIKey(nil, ""))
	})
}
