package internal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

const (
	testArxivID1       = "2401.12345"
	testArxivID2       = "2401.67890"
	testArxivTitle1    = "Attention Is All You Need (Again)"
	testArxivTitle2    = "Deep Learning for Scientific Discovery"
	testArxivAuthor1   = "Jane Smith"
	testArxivAuthor2   = "Bob Jones"
	testArxivAffil1    = "MIT"
	testArxivDOI1      = "10.1234/test.2024.001"
	testArxivCategory1 = "cs.CL"
	testArxivCategory2 = "cs.AI"
	testArxivDate1     = "2024-01-15T08:00:00Z"
	testArxivDate2     = "2024-03-20T12:30:00Z"
	testArxivComment1  = "15 pages, 3 figures"
	testArxivJournal1  = "Nature 2024"

	testArxivPluginTimeout = 5 * time.Second
	testArxivTotalResults  = 42

	testArxivConcurrentGoroutines = 10
)

// ---------------------------------------------------------------------------
// XML fixture builder
// ---------------------------------------------------------------------------

// arxivTestEntry holds parameters for building a test Atom entry.
type arxivTestEntry struct {
	ID              string
	Title           string
	Summary         string
	Published       string
	Updated         string
	Authors         []arxivTestAuthor
	Categories      []string
	DOI             string
	Comment         string
	JournalRef      string
	PrimaryCategory string
}

type arxivTestAuthor struct {
	Name        string
	Affiliation string
}

// buildArxivTestFeedXML generates a complete ArXiv Atom feed XML string.
func buildArxivTestFeedXML(totalResults, startIndex int, entries []arxivTestEntry) string {
	var b strings.Builder

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString("\n")
	b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom"`)
	b.WriteString(` xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/"`)
	b.WriteString(` xmlns:arxiv="http://arxiv.org/schemas/atom">`)
	b.WriteString("\n")

	fmt.Fprintf(&b, "  <opensearch:totalResults>%d</opensearch:totalResults>\n", totalResults)
	fmt.Fprintf(&b, "  <opensearch:startIndex>%d</opensearch:startIndex>\n", startIndex)
	fmt.Fprintf(&b, "  <opensearch:itemsPerPage>%d</opensearch:itemsPerPage>\n", len(entries))

	for _, e := range entries {
		b.WriteString("  <entry>\n")
		fmt.Fprintf(&b, "    <id>http://arxiv.org/abs/%sv1</id>\n", e.ID)
		fmt.Fprintf(&b, "    <title>%s</title>\n", e.Title)
		fmt.Fprintf(&b, "    <summary>%s</summary>\n", e.Summary)
		fmt.Fprintf(&b, "    <published>%s</published>\n", e.Published)
		fmt.Fprintf(&b, "    <updated>%s</updated>\n", e.Updated)

		for _, a := range e.Authors {
			b.WriteString("    <author>\n")
			fmt.Fprintf(&b, "      <name>%s</name>\n", a.Name)
			if a.Affiliation != "" {
				fmt.Fprintf(&b, "      <arxiv:affiliation>%s</arxiv:affiliation>\n", a.Affiliation)
			}
			b.WriteString("    </author>\n")
		}

		fmt.Fprintf(&b, `    <link href="http://arxiv.org/abs/%sv1" rel="alternate" type="text/html"/>`, e.ID)
		b.WriteString("\n")
		fmt.Fprintf(&b, `    <link href="http://arxiv.org/pdf/%sv1" rel="related" type="application/pdf" title="pdf"/>`, e.ID)
		b.WriteString("\n")

		for _, cat := range e.Categories {
			fmt.Fprintf(&b, `    <category term="%s" scheme="http://arxiv.org/schemas/atom"/>`, cat)
			b.WriteString("\n")
		}

		if e.PrimaryCategory != "" {
			fmt.Fprintf(&b, `    <arxiv:primary_category term="%s" scheme="http://arxiv.org/schemas/atom"/>`, e.PrimaryCategory)
			b.WriteString("\n")
		}

		if e.DOI != "" {
			fmt.Fprintf(&b, "    <arxiv:doi>%s</arxiv:doi>\n", e.DOI)
		}
		if e.Comment != "" {
			fmt.Fprintf(&b, "    <arxiv:comment>%s</arxiv:comment>\n", e.Comment)
		}
		if e.JournalRef != "" {
			fmt.Fprintf(&b, "    <arxiv:journal_ref>%s</arxiv:journal_ref>\n", e.JournalRef)
		}

		b.WriteString("  </entry>\n")
	}

	b.WriteString("</feed>\n")
	return b.String()
}

// defaultTestEntry1 returns a standard test entry with all fields populated.
func defaultTestEntry1() arxivTestEntry {
	return arxivTestEntry{
		ID:      testArxivID1,
		Title:   testArxivTitle1,
		Summary: "We present a novel approach to attention mechanisms.",
		Published:       testArxivDate1,
		Updated:         testArxivDate2,
		Authors:         []arxivTestAuthor{{Name: testArxivAuthor1, Affiliation: testArxivAffil1}},
		Categories:      []string{testArxivCategory1, testArxivCategory2},
		DOI:             testArxivDOI1,
		Comment:         testArxivComment1,
		JournalRef:      testArxivJournal1,
		PrimaryCategory: testArxivCategory1,
	}
}

// defaultTestEntry2 returns a second test entry with minimal fields.
func defaultTestEntry2() arxivTestEntry {
	return arxivTestEntry{
		ID:              testArxivID2,
		Title:           testArxivTitle2,
		Summary:         "Exploring deep learning applications.",
		Published:       testArxivDate2,
		Updated:         testArxivDate2,
		Authors:         []arxivTestAuthor{{Name: testArxivAuthor2}},
		Categories:      []string{testArxivCategory2},
		PrimaryCategory: testArxivCategory2,
	}
}

// ---------------------------------------------------------------------------
// httptest server factory
// ---------------------------------------------------------------------------

// newArxivTestPlugin creates an ArXivPlugin pointing at a custom base URL.
func newArxivTestPlugin(t *testing.T, baseURL string) *ArXivPlugin {
	t.Helper()
	plugin := &ArXivPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testArxivPluginTimeout},
		RateLimit: 0.33,
	})
	require.NoError(t, err)
	return plugin
}

// ---------------------------------------------------------------------------
// Contract test
// ---------------------------------------------------------------------------

func TestArXivPluginContract(t *testing.T) {
	plugin := newArxivTestPlugin(t, "http://unused.test")
	PluginContractTest(t, plugin)
}

// ---------------------------------------------------------------------------
// Search tests
// ---------------------------------------------------------------------------

func TestArXivSearch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		params         SearchParams
		feedXML        string
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
			feedXML:        buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				q := r.URL.Query().Get(arxivParamSearchQuery)
				assert.Contains(t, q, arxivFieldAll+"attention mechanism")
			},
		},
		{
			name: "search_with_title_filter",
			params: SearchParams{
				Query:   "transformers",
				Filters: SearchFilters{Title: "attention"},
				Limit:   10,
			},
			feedXML:        buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				q := r.URL.Query().Get(arxivParamSearchQuery)
				assert.Contains(t, q, arxivFieldTitle+"attention")
				assert.Contains(t, q, arxivFieldAll+"transformers")
			},
		},
		{
			name: "search_with_author_filter",
			params: SearchParams{
				Query:   "deep learning",
				Filters: SearchFilters{Authors: []string{"Smith", "Jones"}},
				Limit:   10,
			},
			feedXML:        buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				q := r.URL.Query().Get(arxivParamSearchQuery)
				assert.Contains(t, q, arxivFieldAuthor+"Smith")
				assert.Contains(t, q, arxivFieldAuthor+"Jones")
			},
		},
		{
			name: "search_with_category_filter",
			params: SearchParams{
				Query:   "reinforcement",
				Filters: SearchFilters{Categories: []string{"cs.AI", "cs.LG"}},
				Limit:   10,
			},
			feedXML:        buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				q := r.URL.Query().Get(arxivParamSearchQuery)
				assert.Contains(t, q, arxivFieldCategory+"cs.AI")
				assert.Contains(t, q, arxivFieldCategory+"cs.LG")
			},
		},
		{
			name: "search_with_date_filter",
			params: SearchParams{
				Query: "neural networks",
				Filters: SearchFilters{
					DateFrom: "2024-01-01",
					DateTo:   "2024-06-30",
				},
				Limit: 10,
			},
			feedXML:        buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				q := r.URL.Query().Get(arxivParamSearchQuery)
				assert.Contains(t, q, "submittedDate:[202401010000 TO 202406302359]")
			},
		},
		{
			name: "search_with_year_only_dates",
			params: SearchParams{
				Query: "quantum computing",
				Filters: SearchFilters{
					DateFrom: "2023",
					DateTo:   "2024",
				},
				Limit: 10,
			},
			feedXML:        buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				q := r.URL.Query().Get(arxivParamSearchQuery)
				assert.Contains(t, q, "submittedDate:[202301010000 TO 202412312359]")
			},
		},
		{
			name: "search_sort_date_desc",
			params: SearchParams{
				Query: "LLM",
				Sort:  SortDateDesc,
				Limit: 10,
			},
			feedXML:        buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, arxivSortSubmitted, r.URL.Query().Get(arxivParamSortBy))
				assert.Equal(t, arxivSortOrderDesc, r.URL.Query().Get(arxivParamSortOrder))
			},
		},
		{
			name: "search_sort_date_asc",
			params: SearchParams{
				Query: "LLM",
				Sort:  SortDateAsc,
				Limit: 10,
			},
			feedXML:        buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, arxivSortSubmitted, r.URL.Query().Get(arxivParamSortBy))
				assert.Equal(t, arxivSortOrderAsc, r.URL.Query().Get(arxivParamSortOrder))
			},
		},
		{
			name: "search_sort_relevance",
			params: SearchParams{
				Query: "GPT",
				Sort:  SortRelevance,
				Limit: 10,
			},
			feedXML:        buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, arxivSortRelevance, r.URL.Query().Get(arxivParamSortBy))
			},
		},
		{
			name: "search_pagination",
			params: SearchParams{
				Query:  "attention",
				Limit:  5,
				Offset: 10,
			},
			feedXML:        buildArxivTestFeedXML(testArxivTotalResults, 10, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      testArxivTotalResults,
			wantHasMore:    true,
			validateReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "10", r.URL.Query().Get(arxivParamStart))
				assert.Equal(t, "5", r.URL.Query().Get(arxivParamMaxResults))
			},
		},
		{
			name: "search_has_more_true",
			params: SearchParams{
				Query: "attention",
				Limit: 10,
			},
			feedXML:        buildArxivTestFeedXML(100, 0, []arxivTestEntry{defaultTestEntry1(), defaultTestEntry2()}),
			wantResultsCnt: 2,
			wantTotal:      100,
			wantHasMore:    true,
		},
		{
			name: "search_has_more_false",
			params: SearchParams{
				Query: "attention",
				Limit: 10,
			},
			feedXML:        buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
		},
		{
			name: "search_empty_results",
			params: SearchParams{
				Query: "nonexistent paper xyz",
				Limit: 10,
			},
			feedXML:        buildArxivTestFeedXML(0, 0, nil),
			wantResultsCnt: 0,
			wantTotal:      0,
			wantHasMore:    false,
		},
		{
			name: "search_empty_query",
			params: SearchParams{
				Limit: 10,
			},
			wantErr: ErrArxivEmptyQuery,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.wantErr != nil {
				// No server needed for error cases that fail before HTTP.
				plugin := newArxivTestPlugin(t, "http://unused.test")
				_, err := plugin.Search(context.Background(), tc.params, nil)
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.validateReq != nil {
					tc.validateReq(t, r)
				}
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(w, tc.feedXML)
			}))
			t.Cleanup(ts.Close)

			plugin := newArxivTestPlugin(t, ts.URL)
			result, err := plugin.Search(context.Background(), tc.params, nil)
			require.NoError(t, err)
			require.NotNil(t, result)

			assert.Len(t, result.Results, tc.wantResultsCnt)
			assert.Equal(t, tc.wantTotal, result.Total)
			assert.Equal(t, tc.wantHasMore, result.HasMore)
		})
	}
}

// TestArXivSearchResultMapping verifies full field mapping from XML to Publication.
func TestArXivSearchResultMapping(t *testing.T) {
	t.Parallel()

	feedXML := buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, feedXML)
	}))
	t.Cleanup(ts.Close)

	plugin := newArxivTestPlugin(t, ts.URL)
	result, err := plugin.Search(context.Background(), SearchParams{
		Query: "attention",
		Limit: 10,
	}, nil)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)

	pub := result.Results[0]
	assert.Equal(t, SourceArXiv+prefixedIDSeparator+testArxivID1, pub.ID)
	assert.Equal(t, SourceArXiv, pub.Source)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, testArxivTitle1, pub.Title)
	assert.Equal(t, "We present a novel approach to attention mechanisms.", pub.Abstract)
	assert.Equal(t, testArxivID1, pub.ArXivID)
	assert.Equal(t, arxivAbsURLPrefix+testArxivID1, pub.URL)
	assert.Equal(t, arxivPDFURLPrefix+testArxivID1, pub.PDFURL)
	assert.Equal(t, testArxivDOI1, pub.DOI)
	assert.Equal(t, "2024-01-15", pub.Published)
	assert.Equal(t, "2024-03-20", pub.Updated)

	require.Len(t, pub.Authors, 1)
	assert.Equal(t, testArxivAuthor1, pub.Authors[0].Name)
	assert.Equal(t, testArxivAffil1, pub.Authors[0].Affiliation)

	require.Len(t, pub.Categories, 2)
	assert.Equal(t, testArxivCategory1, pub.Categories[0])
	assert.Equal(t, testArxivCategory2, pub.Categories[1])

	require.NotNil(t, pub.SourceMetadata)
	assert.Equal(t, testArxivComment1, pub.SourceMetadata[arxivMetaKeyComment])
	assert.Equal(t, testArxivJournal1, pub.SourceMetadata[arxivMetaKeyJournalRef])
	assert.Equal(t, testArxivCategory1, pub.SourceMetadata[arxivMetaKeyPrimaryCategory])
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestArXivGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		id        string
		format    ContentFormat
		feedXML   string
		wantErr   error
		wantTitle string
		wantFT    bool // expect FullText to be non-nil
	}{
		{
			name:      "get_by_id_native",
			id:        testArxivID1,
			format:    FormatNative,
			feedXML:   buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantTitle: testArxivTitle1,
			wantFT:    false,
		},
		{
			name:      "get_format_xml",
			id:        testArxivID1,
			format:    FormatXML,
			feedXML:   buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantTitle: testArxivTitle1,
			wantFT:    false,
		},
		{
			name:      "get_format_json",
			id:        testArxivID1,
			format:    FormatJSON,
			feedXML:   buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantTitle: testArxivTitle1,
			wantFT:    false, // JSON is native to Publication struct
		},
		{
			name:      "get_format_bibtex",
			id:        testArxivID1,
			format:    FormatBibTeX,
			feedXML:   buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantTitle: testArxivTitle1,
			wantFT:    true,
		},
		{
			name:    "get_format_unsupported",
			id:      testArxivID1,
			format:  FormatMarkdown,
			feedXML: buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}),
			wantErr: ErrFormatUnsupported,
		},
		{
			name:    "get_not_found",
			id:      "9999.99999",
			format:  FormatNative,
			feedXML: buildArxivTestFeedXML(0, 0, nil),
			wantErr: ErrArxivNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Validate id_list parameter is present for Get requests.
				assert.NotEmpty(t, r.URL.Query().Get(arxivParamIDList))
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(w, tc.feedXML)
			}))
			t.Cleanup(ts.Close)

			plugin := newArxivTestPlugin(t, ts.URL)
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
// Query builder tests
// ---------------------------------------------------------------------------

func TestArXivQueryBuilder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		params   SearchParams
		wantQ    string
		wantErr  error
	}{
		{
			name:   "query_only",
			params: SearchParams{Query: "attention"},
			wantQ:  "all:attention",
		},
		{
			name: "query_with_title",
			params: SearchParams{
				Query:   "transformers",
				Filters: SearchFilters{Title: "self-attention"},
			},
			wantQ: "all:transformers" + arxivQueryAND + "ti:self-attention",
		},
		{
			name: "query_with_authors",
			params: SearchParams{
				Query:   "NLP",
				Filters: SearchFilters{Authors: []string{"Vaswani", "Shazeer"}},
			},
			wantQ: "all:NLP" + arxivQueryAND + "au:Vaswani" + arxivQueryAND + "au:Shazeer",
		},
		{
			name: "query_with_categories",
			params: SearchParams{
				Query:   "vision",
				Filters: SearchFilters{Categories: []string{"cs.CV"}},
			},
			wantQ: "all:vision" + arxivQueryAND + "cat:cs.CV",
		},
		{
			name: "query_with_date_range",
			params: SearchParams{
				Query: "GAN",
				Filters: SearchFilters{
					DateFrom: "2024-01-01",
					DateTo:   "2024-12-31",
				},
			},
			wantQ: "all:GAN" + arxivQueryAND + "submittedDate:[202401010000 TO 202412312359]",
		},
		{
			name: "query_with_date_from_only",
			params: SearchParams{
				Query:   "diffusion",
				Filters: SearchFilters{DateFrom: "2023-06-15"},
			},
			wantQ: "all:diffusion" + arxivQueryAND + "submittedDate:[202306150000 TO *]",
		},
		{
			name: "query_with_date_to_only",
			params: SearchParams{
				Query:   "RLHF",
				Filters: SearchFilters{DateTo: "2024-03-01"},
			},
			wantQ: "all:RLHF" + arxivQueryAND + "submittedDate:[* TO 202403012359]",
		},
		{
			name: "query_combined_all_filters",
			params: SearchParams{
				Query: "attention",
				Filters: SearchFilters{
					Title:      "transformer",
					Authors:    []string{"Smith"},
					Categories: []string{"cs.CL"},
					DateFrom:   "2024",
					DateTo:     "2024",
				},
			},
			wantQ: "all:attention" + arxivQueryAND +
				"ti:transformer" + arxivQueryAND +
				"au:Smith" + arxivQueryAND +
				"cat:cs.CL" + arxivQueryAND +
				"submittedDate:[202401010000 TO 202412312359]",
		},
		{
			name: "title_only_no_query",
			params: SearchParams{
				Filters: SearchFilters{Title: "attention"},
			},
			wantQ: "ti:attention",
		},
		{
			name:    "empty_query_and_filters",
			params:  SearchParams{},
			wantErr: ErrArxivEmptyQuery,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			q, err := buildArxivQuery(tc.params)
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantQ, q)
		})
	}
}

// ---------------------------------------------------------------------------
// ID extraction tests
// ---------------------------------------------------------------------------

func TestArXivExtractID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		atomID string
		wantID string
	}{
		{
			name:   "new_style_versioned",
			atomID: "http://arxiv.org/abs/2401.12345v1",
			wantID: "2401.12345",
		},
		{
			name:   "new_style_v2",
			atomID: "http://arxiv.org/abs/2401.12345v2",
			wantID: "2401.12345",
		},
		{
			name:   "new_style_no_version",
			atomID: "http://arxiv.org/abs/2401.12345",
			wantID: "2401.12345",
		},
		{
			name:   "old_style_versioned",
			atomID: "http://arxiv.org/abs/hep-th/9901001v1",
			wantID: "hep-th/9901001",
		},
		{
			name:   "old_style_no_version",
			atomID: "http://arxiv.org/abs/hep-th/9901001",
			wantID: "hep-th/9901001",
		},
		{
			name:   "five_digit_id",
			atomID: "http://arxiv.org/abs/2401.00001v3",
			wantID: "2401.00001",
		},
		{
			name:   "bare_id_no_url_prefix",
			atomID: "2401.12345v1",
			wantID: "2401.12345",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.wantID, extractArxivID(tc.atomID))
		})
	}
}

// ---------------------------------------------------------------------------
// Text cleaning tests
// ---------------------------------------------------------------------------

func TestArXivCleanText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "multiline_to_single",
			input: "This is\na multi-line\ntext",
			want:  "This is a multi-line text",
		},
		{
			name:  "collapse_spaces",
			input: "Too   many    spaces",
			want:  "Too many spaces",
		},
		{
			name:  "trim_whitespace",
			input: "  leading and trailing  ",
			want:  "leading and trailing",
		},
		{
			name:  "mixed_whitespace",
			input: "\n  Line one\n  Line two  \n  ",
			want:  "Line one Line two",
		},
		{
			name:  "already_clean",
			input: "Clean text",
			want:  "Clean text",
		},
		{
			name:  "empty_string",
			input: "",
			want:  "",
		},
		{
			name:  "preserve_latex",
			input: "An $O(n \\log n)$ algorithm",
			want:  "An $O(n \\log n)$ algorithm",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, cleanArxivText(tc.input))
		})
	}
}

// ---------------------------------------------------------------------------
// Date parsing tests
// ---------------------------------------------------------------------------

func TestArXivDateParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "rfc3339_standard", raw: "2024-01-15T08:00:00Z", want: "2024-01-15"},
		{name: "rfc3339_with_offset", raw: "2024-03-20T12:30:00+05:00", want: "2024-03-20"},
		{name: "empty_string", raw: "", want: ""},
		{name: "invalid_format", raw: "not-a-date", want: "not-a-date"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, parseArxivDate(tc.raw))
		})
	}
}

// ---------------------------------------------------------------------------
// Date conversion tests
// ---------------------------------------------------------------------------

func TestArXivConvertDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		date      string
		isEndDate bool
		want      string
	}{
		{name: "full_date_start", date: "2024-01-15", isEndDate: false, want: "20240115"},
		{name: "full_date_end", date: "2024-06-30", isEndDate: true, want: "20240630"},
		{name: "year_only_start", date: "2024", isEndDate: false, want: "20240101"},
		{name: "year_only_end", date: "2024", isEndDate: true, want: "20241231"},
		{name: "unparseable", date: "invalid", isEndDate: false, want: "invalid"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, convertDateToArxivFormat(tc.date, tc.isEndDate))
		})
	}
}

// ---------------------------------------------------------------------------
// Health tracking tests
// ---------------------------------------------------------------------------

func TestArXivHealthTracking(t *testing.T) {
	t.Parallel()

	// Setup: server that alternates success/failure.
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		if callCount == 2 { //nolint:mnd // second call fails
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()}))
	}))
	t.Cleanup(ts.Close)

	plugin := newArxivTestPlugin(t, ts.URL)

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

func TestArXivHTTPErrors(t *testing.T) {
	t.Parallel()

	t.Run("server_500", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		plugin := newArxivTestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})

	t.Run("server_404", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(ts.Close)

		plugin := newArxivTestPlugin(t, ts.URL)
		_, err := plugin.Get(context.Background(), testArxivID1, nil, FormatNative, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrGetFailed)
	})

	t.Run("context_canceled", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(testArxivPluginTimeout)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(ts.Close)

		plugin := newArxivTestPlugin(t, ts.URL)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately.

		_, err := plugin.Search(ctx, SearchParams{Query: "test", Limit: 1}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})

	t.Run("malformed_xml", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, "<not-valid-xml>")
		}))
		t.Cleanup(ts.Close)

		plugin := newArxivTestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSearchFailed)
	})
}

// ---------------------------------------------------------------------------
// BibTeX assembly tests
// ---------------------------------------------------------------------------

func TestArXivBibTeXAssembly(t *testing.T) {
	t.Parallel()

	t.Run("single_author", func(t *testing.T) {
		t.Parallel()
		pub := &Publication{
			ArXivID:    testArxivID1,
			Title:      testArxivTitle1,
			Authors:    []Author{{Name: testArxivAuthor1}},
			Published:  "2024-01-15",
			Categories: []string{testArxivCategory1},
			URL:        arxivAbsURLPrefix + testArxivID1,
		}
		bibtex := assembleBibTeX(pub)
		assert.Contains(t, bibtex, "@article{"+testArxivID1)
		assert.Contains(t, bibtex, testArxivTitle1)
		assert.Contains(t, bibtex, testArxivAuthor1)
		assert.Contains(t, bibtex, "year          = {2024}")
		assert.Contains(t, bibtex, testArxivCategory1)
		assert.Contains(t, bibtex, "archivePrefix = {arXiv}")
	})

	t.Run("multiple_authors", func(t *testing.T) {
		t.Parallel()
		pub := &Publication{
			ArXivID:    testArxivID1,
			Title:      testArxivTitle1,
			Authors:    []Author{{Name: testArxivAuthor1}, {Name: testArxivAuthor2}},
			Published:  "2024-01-15",
			Categories: []string{testArxivCategory1},
			URL:        arxivAbsURLPrefix + testArxivID1,
		}
		bibtex := assembleBibTeX(pub)
		assert.Contains(t, bibtex, testArxivAuthor1+arxivBibTeXAuthorSeparator+testArxivAuthor2)
	})

	t.Run("no_categories", func(t *testing.T) {
		t.Parallel()
		pub := &Publication{
			ArXivID:   testArxivID1,
			Title:     testArxivTitle1,
			Authors:   []Author{{Name: testArxivAuthor1}},
			Published: "2024-01-15",
			URL:       arxivAbsURLPrefix + testArxivID1,
		}
		bibtex := assembleBibTeX(pub)
		assert.Contains(t, bibtex, "primaryClass  = {}")
	})
}

// ---------------------------------------------------------------------------
// Initialize tests
// ---------------------------------------------------------------------------

func TestArXivInitialize(t *testing.T) {
	t.Parallel()

	t.Run("default_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &ArXivPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled:   true,
			RateLimit: 0.33,
		})
		require.NoError(t, err)
		assert.Equal(t, arxivDefaultBaseURL, plugin.baseURL)
		assert.True(t, plugin.enabled)
		assert.True(t, plugin.healthy)
	})

	t.Run("custom_base_url", func(t *testing.T) {
		t.Parallel()
		customURL := "http://custom.test/api"
		plugin := &ArXivPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			BaseURL: customURL,
		})
		require.NoError(t, err)
		assert.Equal(t, customURL, plugin.baseURL)
	})

	t.Run("default_timeout", func(t *testing.T) {
		t.Parallel()
		plugin := &ArXivPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, DefaultPluginTimeout, plugin.httpClient.Timeout)
	})

	t.Run("custom_timeout", func(t *testing.T) {
		t.Parallel()
		customTimeout := 30 * time.Second
		plugin := &ArXivPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			Timeout: Duration{Duration: customTimeout},
		})
		require.NoError(t, err)
		assert.Equal(t, customTimeout, plugin.httpClient.Timeout)
	})

	t.Run("rate_limit_reported", func(t *testing.T) {
		t.Parallel()
		plugin := &ArXivPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled:   true,
			RateLimit: 0.33,
		})
		require.NoError(t, err)
		health := plugin.Health(context.Background())
		assert.InDelta(t, 0.33, health.RateLimit, 0.001)
	})
}

// ---------------------------------------------------------------------------
// Concurrent access test
// ---------------------------------------------------------------------------

func TestArXivConcurrentAccess(t *testing.T) {
	t.Parallel()

	feedXML := buildArxivTestFeedXML(1, 0, []arxivTestEntry{defaultTestEntry1()})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, feedXML)
	}))
	t.Cleanup(ts.Close)

	plugin := newArxivTestPlugin(t, ts.URL)

	var wg sync.WaitGroup
	for range testArxivConcurrentGoroutines {
		wg.Go(func() {
			// Mix of Search, Get, and Health calls.
			_, _ = plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
			_, _ = plugin.Get(context.Background(), testArxivID1, nil, FormatNative, nil)
			_ = plugin.Health(context.Background())
		})
	}
	wg.Wait()

	// If we get here without -race detector complaints, we're good.
	health := plugin.Health(context.Background())
	assert.True(t, health.Enabled)
}

// ---------------------------------------------------------------------------
// XML parsing edge cases
// ---------------------------------------------------------------------------

func TestArXivXMLParsingEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("entry_missing_optional_fields", func(t *testing.T) {
		t.Parallel()
		entry := arxivTestEntry{
			ID:        testArxivID1,
			Title:     testArxivTitle1,
			Summary:   "Just an abstract.",
			Published: testArxivDate1,
			Updated:   testArxivDate1,
			Authors:   []arxivTestAuthor{{Name: testArxivAuthor1}},
		}
		feedXML := buildArxivTestFeedXML(1, 0, []arxivTestEntry{entry})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, feedXML)
		}))
		t.Cleanup(ts.Close)

		plugin := newArxivTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)

		pub := result.Results[0]
		assert.Empty(t, pub.DOI)
		assert.Empty(t, pub.Categories)
		assert.Nil(t, pub.SourceMetadata)
	})

	t.Run("entry_multiple_authors", func(t *testing.T) {
		t.Parallel()
		entry := arxivTestEntry{
			ID:        testArxivID1,
			Title:     testArxivTitle1,
			Summary:   "Collaborative work.",
			Published: testArxivDate1,
			Updated:   testArxivDate1,
			Authors: []arxivTestAuthor{
				{Name: testArxivAuthor1, Affiliation: testArxivAffil1},
				{Name: testArxivAuthor2},
				{Name: "Alice Chen", Affiliation: "Stanford"},
			},
		}
		feedXML := buildArxivTestFeedXML(1, 0, []arxivTestEntry{entry})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, feedXML)
		}))
		t.Cleanup(ts.Close)

		plugin := newArxivTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 1)

		expectedAuthorCount := 3
		pub := result.Results[0]
		require.Len(t, pub.Authors, expectedAuthorCount)
		assert.Equal(t, testArxivAuthor1, pub.Authors[0].Name)
		assert.Equal(t, testArxivAffil1, pub.Authors[0].Affiliation)
		assert.Equal(t, testArxivAuthor2, pub.Authors[1].Name)
		assert.Empty(t, pub.Authors[1].Affiliation)
		assert.Equal(t, "Alice Chen", pub.Authors[2].Name)
		assert.Equal(t, "Stanford", pub.Authors[2].Affiliation)
	})

	t.Run("multiple_entries", func(t *testing.T) {
		t.Parallel()
		feedXML := buildArxivTestFeedXML(2, 0, []arxivTestEntry{
			defaultTestEntry1(),
			defaultTestEntry2(),
		})
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, feedXML)
		}))
		t.Cleanup(ts.Close)

		plugin := newArxivTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
		require.Len(t, result.Results, 2)
		assert.Equal(t, testArxivTitle1, result.Results[0].Title)
		assert.Equal(t, testArxivTitle2, result.Results[1].Title)
	})
}

// ---------------------------------------------------------------------------
// Sort order mapping tests
// ---------------------------------------------------------------------------

func TestArXivSortOrderMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		sort          SortOrder
		wantSortBy    string
		wantSortOrder string
	}{
		{name: "relevance", sort: SortRelevance, wantSortBy: arxivSortRelevance, wantSortOrder: ""},
		{name: "date_desc", sort: SortDateDesc, wantSortBy: arxivSortSubmitted, wantSortOrder: arxivSortOrderDesc},
		{name: "date_asc", sort: SortDateAsc, wantSortBy: arxivSortSubmitted, wantSortOrder: arxivSortOrderAsc},
		{name: "citations_unsupported", sort: SortCitations, wantSortBy: "", wantSortOrder: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sortBy, sortOrder := mapArxivSortOrder(tc.sort)
			assert.Equal(t, tc.wantSortBy, sortBy)
			assert.Equal(t, tc.wantSortOrder, sortOrder)
		})
	}
}
