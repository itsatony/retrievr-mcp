package internal

import (
	"context"
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
	testEMCID1       = "12345678"
	testEMCID2       = "23456789"
	testEMCPMID1     = "12345678"
	testEMCPMID2     = "23456789"
	testEMCPMCID1    = "PMC1234567"
	testEMCTitle1    = "CRISPR-Cas9 Gene Editing in Human Embryos"
	testEMCTitle2    = "Immunotherapy Response Biomarkers in Melanoma"
	testEMCAbstract1 = "We demonstrate a novel application of CRISPR-Cas9 in gene editing."
	testEMCAbstract2 = "This study identifies biomarkers predicting immunotherapy response."
	testEMCAuthors1  = "Smith J, Chen W"
	testEMCAuthors2  = "Jones A, Lee B"
	testEMCDOI1      = "10.1234/emctest.2024.001"
	testEMCDOI2      = "10.5678/emctest.2024.002"
	testEMCDate1     = "2024-01-15"
	testEMCDate2     = "2023-03-20"
	testEMCJournal1  = "Nature Medicine"
	testEMCJournal2  = "The Lancet"
	testEMCVolume1   = "30"
	testEMCIssue1    = "1"
	testEMCISSN1     = "1078-8956"
	testEMCMeSH1     = "CRISPR-Cas Systems"
	testEMCMeSH2     = "Gene Editing"
	testEMCSource1   = "MED"

	testEMCPluginTimeout        = 5 * time.Second
	testEMCSearchCount          = 150
	testEMCConcurrentGoroutines = 10
	testEMCCitedBy1             = 42
	testEMCCitedBy2             = 10
)

// ---------------------------------------------------------------------------
// JSON fixture builder types
// ---------------------------------------------------------------------------

type emcTestResult struct {
	ID                   string
	Source               string
	PMID                 string
	PMCID                string
	DOI                  string
	Title                string
	AuthorString         string
	AbstractText         string
	FirstPublicationDate string
	IsOpenAccess         string
	CitedByCount         int
	JournalTitle         string
	JournalISSN          string
	Volume               string
	Issue                string
	MeSHTerms            []string
}

// ---------------------------------------------------------------------------
// JSON fixture builders
// ---------------------------------------------------------------------------

// buildEMCTestSearchJSON generates a complete Europe PMC search JSON response.
func buildEMCTestSearchJSON(hitCount int, results []emcTestResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"version":"6.9","hitCount":%d,"nextCursorMark":"AoE+","resultList":{"result":[`, hitCount)

	for i, r := range results {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(buildEMCTestResultJSON(r))
	}

	b.WriteString("]}}")
	return b.String()
}

// buildEMCTestResultJSON generates a single Europe PMC result JSON object.
func buildEMCTestResultJSON(r emcTestResult) string {
	meshJSON := "null"
	if len(r.MeSHTerms) > 0 {
		meshItems := make([]string, 0, len(r.MeSHTerms))
		for _, term := range r.MeSHTerms {
			meshItems = append(meshItems, fmt.Sprintf(`{"descriptorName":%q}`, term))
		}
		meshJSON = fmt.Sprintf(`{"meshHeading":[%s]}`, strings.Join(meshItems, ","))
	}

	journalJSON := "null"
	if r.JournalTitle != "" {
		journalJSON = fmt.Sprintf(`{"journal":{"title":%q,"issn":%q},"volume":%q,"issue":%q}`,
			r.JournalTitle, r.JournalISSN, r.Volume, r.Issue)
	}

	return fmt.Sprintf(`{
		"id":%q,
		"source":%q,
		"pmid":%q,
		"pmcid":%q,
		"doi":%q,
		"title":%q,
		"authorString":%q,
		"abstractText":%q,
		"firstPublicationDate":%q,
		"isOpenAccess":%q,
		"citedByCount":%d,
		"journalInfo":%s,
		"meshHeadingList":%s,
		"fullTextUrlList":null
	}`, r.ID, r.Source, r.PMID, r.PMCID, r.DOI, r.Title, r.AuthorString,
		r.AbstractText, r.FirstPublicationDate, r.IsOpenAccess, r.CitedByCount,
		journalJSON, meshJSON)
}

func defaultEMCTestResult1() emcTestResult {
	return emcTestResult{
		ID:                   testEMCID1,
		Source:               testEMCSource1,
		PMID:                 testEMCPMID1,
		PMCID:                testEMCPMCID1,
		DOI:                  testEMCDOI1,
		Title:                testEMCTitle1,
		AuthorString:         testEMCAuthors1,
		AbstractText:         testEMCAbstract1,
		FirstPublicationDate: testEMCDate1,
		IsOpenAccess:         emcOpenAccessYes,
		CitedByCount:         testEMCCitedBy1,
		JournalTitle:         testEMCJournal1,
		JournalISSN:          testEMCISSN1,
		Volume:               testEMCVolume1,
		Issue:                testEMCIssue1,
		MeSHTerms:            []string{testEMCMeSH1, testEMCMeSH2},
	}
}

func defaultEMCTestResult2() emcTestResult {
	return emcTestResult{
		ID:                   testEMCID2,
		Source:               testEMCSource1,
		PMID:                 testEMCPMID2,
		DOI:                  testEMCDOI2,
		Title:                testEMCTitle2,
		AuthorString:         testEMCAuthors2,
		AbstractText:         testEMCAbstract2,
		FirstPublicationDate: testEMCDate2,
		IsOpenAccess:         "N",
		CitedByCount:         testEMCCitedBy2,
		JournalTitle:         testEMCJournal2,
		MeSHTerms:            nil,
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newEMCTestPlugin(t *testing.T, baseURL string) *EuropePMCPlugin {
	t.Helper()
	plugin := &EuropePMCPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testEMCPluginTimeout},
		RateLimit: 10.0,
	})
	require.NoError(t, err)
	return plugin
}

// ---------------------------------------------------------------------------
// Contract test
// ---------------------------------------------------------------------------

func TestEuropePMCPluginContract(t *testing.T) {
	t.Parallel()
	plugin := newEMCTestPlugin(t, "http://unused.test/")
	PluginContractTest(t, plugin)
}

// ---------------------------------------------------------------------------
// Search tests
// ---------------------------------------------------------------------------

func TestEuropePMCSearch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		params            SearchParams
		wantQueryContains []string
		wantQueryAbsent   []string
		wantErr           error
		wantResults       int
	}{
		{
			name: "basic_search",
			params: SearchParams{
				Query: "CRISPR gene editing",
				Limit: 10,
			},
			wantQueryContains: []string{"CRISPR+gene+editing"},
			wantResults:       2,
		},
		{
			name: "search_with_date_filter_full",
			params: SearchParams{
				Query: "cancer",
				Limit: 10,
				Filters: SearchFilters{
					DateFrom: "2024-01-01",
					DateTo:   "2024-12-31",
				},
			},
			wantQueryContains: []string{"cancer", "FIRST_PDATE", "2024-01-01", "2024-12-31"},
			wantResults:       2,
		},
		{
			name: "search_with_date_filter_year_only",
			params: SearchParams{
				Query: "cancer",
				Limit: 10,
				Filters: SearchFilters{
					DateFrom: "2024",
					DateTo:   "2024",
				},
			},
			wantQueryContains: []string{"FIRST_PDATE", "2024-01-01", "2024-12-31"},
			wantResults:       2,
		},
		{
			name: "search_with_date_from_only",
			params: SearchParams{
				Query: "cancer",
				Limit: 10,
				Filters: SearchFilters{
					DateFrom: "2024-01-01",
				},
			},
			wantQueryContains: []string{"FIRST_PDATE", "2024-01-01"},
			wantResults:       2,
		},
		{
			name: "search_with_title_filter",
			params: SearchParams{
				Query: "cancer",
				Limit: 10,
				Filters: SearchFilters{
					Title: "immune checkpoint",
				},
			},
			wantQueryContains: []string{"cancer", "TITLE", "immune+checkpoint"},
			wantResults:       2,
		},
		{
			name: "search_with_author_filter",
			params: SearchParams{
				Query: "cancer",
				Limit: 10,
				Filters: SearchFilters{
					Authors: []string{"Smith", "Chen"},
				},
			},
			wantQueryContains: []string{"cancer", "AUTH", "Smith", "Chen"},
			wantResults:       2,
		},
		{
			name: "search_with_category_filter",
			params: SearchParams{
				Query: "cancer",
				Limit: 10,
				Filters: SearchFilters{
					Categories: []string{"CRISPR-Cas Systems"},
				},
			},
			wantQueryContains: []string{"cancer", "CRISPR-Cas+Systems"},
			wantResults:       2,
		},
		{
			name: "search_with_sort_date",
			params: SearchParams{
				Query: "cancer",
				Limit: 10,
				Sort:  SortDateDesc,
			},
			wantQueryContains: []string{"cancer"},
			wantResults:       2,
		},
		{
			name: "search_with_sort_citations",
			params: SearchParams{
				Query: "cancer",
				Limit: 10,
				Sort:  SortCitations,
			},
			wantQueryContains: []string{"cancer"},
			wantResults:       2,
		},
		{
			name: "search_with_offset",
			params: SearchParams{
				Query:  "cancer",
				Limit:  10,
				Offset: 20,
			},
			wantQueryContains: []string{"cancer"},
			wantResults:       2,
		},
		{
			name: "search_empty_query",
			params: SearchParams{
				Query: "",
				Limit: 10,
			},
			wantErr: ErrEuropePMCEmptyQuery,
		},
		{
			name: "search_no_results",
			params: SearchParams{
				Query: "xyznonexistent12345",
				Limit: 10,
			},
			wantResults: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")

				query := r.URL.Query().Get(emcParamQuery)

				// Verify expected query fragments.
				for _, want := range tt.wantQueryContains {
					assert.Contains(t, r.URL.RawQuery, want,
						"expected query to contain %q, got URL: %s", want, r.URL.String())
				}

				// Verify sort param for date/citations sort tests.
				if tt.params.Sort == SortDateDesc {
					assert.Contains(t, r.URL.RawQuery, "FIRST_PDATE")
				}
				if tt.params.Sort == SortCitations {
					assert.Contains(t, r.URL.RawQuery, "CITED")
				}

				// Verify offset maps to page param.
				if tt.params.Offset > 0 {
					assert.Contains(t, r.URL.RawQuery, "page=3",
						"offset 20 with limit 10 should be page 3")
				}

				// Return empty results for the "no results" test.
				if strings.Contains(query, "xyznonexistent") {
					fmt.Fprint(w, buildEMCTestSearchJSON(0, nil))
					return
				}

				results := []emcTestResult{defaultEMCTestResult1(), defaultEMCTestResult2()}
				fmt.Fprint(w, buildEMCTestSearchJSON(testEMCSearchCount, results))
			}))
			defer server.Close()

			plugin := newEMCTestPlugin(t, server.URL+"/")
			result, err := plugin.Search(context.Background(), tt.params, nil)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Len(t, result.Results, tt.wantResults)

			if tt.wantResults > 0 {
				assert.Equal(t, testEMCSearchCount, result.Total)
				assert.True(t, result.HasMore)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Search result mapping tests
// ---------------------------------------------------------------------------

func TestEuropePMCSearchResultMapping(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		results := []emcTestResult{defaultEMCTestResult1()}
		fmt.Fprint(w, buildEMCTestSearchJSON(1, results))
	}))
	defer server.Close()

	plugin := newEMCTestPlugin(t, server.URL+"/")
	result, err := plugin.Search(context.Background(), SearchParams{
		Query: "CRISPR",
		Limit: 10,
	}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1)

	pub := result.Results[0]
	assert.Equal(t, SourceEuropePMC+prefixedIDSeparator+testEMCPMID1, pub.ID)
	assert.Equal(t, SourceEuropePMC, pub.Source)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, testEMCTitle1, pub.Title)
	assert.Equal(t, testEMCAbstract1, pub.Abstract)
	assert.Equal(t, testEMCDOI1, pub.DOI)
	assert.Equal(t, testEMCDate1, pub.Published)
	assert.Contains(t, pub.URL, "europepmc.org")

	// Authors parsed from author string.
	require.Len(t, pub.Authors, 2)
	assert.Equal(t, "Smith J", pub.Authors[0].Name)
	assert.Equal(t, "Chen W", pub.Authors[1].Name)

	// Citation count.
	require.NotNil(t, pub.CitationCount)
	assert.Equal(t, testEMCCitedBy1, *pub.CitationCount)

	// MeSH terms as categories.
	assert.Contains(t, pub.Categories, testEMCMeSH1)
	assert.Contains(t, pub.Categories, testEMCMeSH2)

	// Source metadata.
	require.NotNil(t, pub.SourceMetadata)
	assert.Equal(t, testEMCPMID1, pub.SourceMetadata[emcMetaKeyPMID])
	assert.Equal(t, testEMCPMCID1, pub.SourceMetadata[emcMetaKeyPMCID])
	assert.Equal(t, testEMCSource1, pub.SourceMetadata[emcMetaKeySource])
	assert.Equal(t, testEMCJournal1, pub.SourceMetadata[emcMetaKeyJournal])
	assert.Equal(t, testEMCVolume1, pub.SourceMetadata[emcMetaKeyJournalVol])
	assert.Equal(t, testEMCIssue1, pub.SourceMetadata[emcMetaKeyJournalIss])
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestEuropePMCGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		format     ContentFormat
		wantErr    error
		wantTitle  string
		wantBibTeX bool
	}{
		{
			name:      "get_by_id",
			id:        testEMCPMID1,
			format:    FormatNative,
			wantTitle: testEMCTitle1,
		},
		{
			name:    "get_not_found",
			id:      "99999999",
			format:  FormatNative,
			wantErr: ErrEuropePMCNotFound,
		},
		{
			name:       "get_with_bibtex_format",
			id:         testEMCPMID1,
			format:     FormatBibTeX,
			wantTitle:  testEMCTitle1,
			wantBibTeX: true,
		},
		{
			name:    "get_unsupported_format",
			id:      testEMCPMID1,
			format:  FormatXML,
			wantErr: ErrFormatUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")

				query := r.URL.Query().Get(emcParamQuery)

				if strings.Contains(query, "99999999") {
					fmt.Fprint(w, buildEMCTestSearchJSON(0, nil))
					return
				}

				results := []emcTestResult{defaultEMCTestResult1()}
				fmt.Fprint(w, buildEMCTestSearchJSON(1, results))
			}))
			defer server.Close()

			plugin := newEMCTestPlugin(t, server.URL+"/")
			pub, err := plugin.Get(context.Background(), tt.id, nil, tt.format, nil)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, pub)
			assert.Equal(t, tt.wantTitle, pub.Title)

			if tt.wantBibTeX {
				require.NotNil(t, pub.FullText)
				assert.Equal(t, FormatBibTeX, pub.FullText.ContentFormat)
				assert.Contains(t, pub.FullText.Content, "@article{")
				assert.Contains(t, pub.FullText.Content, testEMCTitle1)
				assert.Contains(t, pub.FullText.Content, testEMCDOI1)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Get with full text test
// ---------------------------------------------------------------------------

func TestEuropePMCGetWithFullText(t *testing.T) {
	t.Parallel()

	const testFullTextXML = "<article><body><p>Full text content here.</p></body></article>"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Full text XML request.
		if strings.HasSuffix(path, emcFullTextXMLPath) {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, testFullTextXML)
			return
		}

		// Search/Get request.
		w.Header().Set("Content-Type", "application/json")
		results := []emcTestResult{defaultEMCTestResult1()}
		fmt.Fprint(w, buildEMCTestSearchJSON(1, results))
	}))
	defer server.Close()

	plugin := newEMCTestPlugin(t, server.URL+"/")
	pub, err := plugin.Get(context.Background(), testEMCPMID1,
		[]IncludeField{IncludeFullText}, FormatNative, nil)

	require.NoError(t, err)
	require.NotNil(t, pub)
	require.NotNil(t, pub.FullText, "full text should be fetched for OA article")
	assert.Equal(t, FormatXML, pub.FullText.ContentFormat)
	assert.Contains(t, pub.FullText.Content, "Full text content here.")
	assert.Equal(t, len(testFullTextXML), pub.FullText.ContentLength)
}

// ---------------------------------------------------------------------------
// Full text error tests
// ---------------------------------------------------------------------------

func TestEuropePMCFetchFullTextErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		fullTextStatus int
		wantFullText   bool
	}{
		{
			name:           "full_text_404",
			fullTextStatus: http.StatusNotFound,
			wantFullText:   false,
		},
		{
			name:           "full_text_500",
			fullTextStatus: http.StatusInternalServerError,
			wantFullText:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.Path

				if strings.HasSuffix(path, emcFullTextXMLPath) {
					w.WriteHeader(tt.fullTextStatus)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				results := []emcTestResult{defaultEMCTestResult1()}
				fmt.Fprint(w, buildEMCTestSearchJSON(1, results))
			}))
			defer server.Close()

			plugin := newEMCTestPlugin(t, server.URL+"/")
			pub, err := plugin.Get(context.Background(), testEMCPMID1,
				[]IncludeField{IncludeFullText}, FormatNative, nil)

			require.NoError(t, err, "full text failure should be non-fatal")
			require.NotNil(t, pub)
			assert.Nil(t, pub.FullText, "full text should be nil when fetch fails")
		})
	}
}

// ---------------------------------------------------------------------------
// Full text skip for non-OA test
// ---------------------------------------------------------------------------

func TestEuropePMCGetFullTextSkippedForNonOA(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Full text endpoint should NOT be called for non-OA articles.
		if strings.HasSuffix(path, emcFullTextXMLPath) {
			t.Error("full text endpoint should not be called for non-OA article")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		results := []emcTestResult{defaultEMCTestResult2()} // IsOpenAccess = "N"
		fmt.Fprint(w, buildEMCTestSearchJSON(1, results))
	}))
	defer server.Close()

	plugin := newEMCTestPlugin(t, server.URL+"/")
	pub, err := plugin.Get(context.Background(), testEMCPMID2,
		[]IncludeField{IncludeFullText}, FormatNative, nil)

	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.Nil(t, pub.FullText, "non-OA article should not have full text fetched")
}

// ---------------------------------------------------------------------------
// Full text empty body test
// ---------------------------------------------------------------------------

func TestEuropePMCGetFullTextEmptyBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if strings.HasSuffix(path, emcFullTextXMLPath) {
			w.Header().Set("Content-Type", "application/xml")
			// Return 200 with empty body.
			return
		}

		w.Header().Set("Content-Type", "application/json")
		results := []emcTestResult{defaultEMCTestResult1()} // IsOpenAccess = "Y"
		fmt.Fprint(w, buildEMCTestSearchJSON(1, results))
	}))
	defer server.Close()

	plugin := newEMCTestPlugin(t, server.URL+"/")
	pub, err := plugin.Get(context.Background(), testEMCPMID1,
		[]IncludeField{IncludeFullText}, FormatNative, nil)

	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.Nil(t, pub.FullText, "empty full text body should result in nil FullText")
}

// ---------------------------------------------------------------------------
// HTTP error tests
// ---------------------------------------------------------------------------

func TestEuropePMCHTTPErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{
			name:    "server_error_500",
			status:  http.StatusInternalServerError,
			body:    "internal error",
			wantErr: ErrSearchFailed,
		},
		{
			name:    "service_unavailable_503",
			status:  http.StatusServiceUnavailable,
			body:    "unavailable",
			wantErr: ErrSearchFailed,
		},
		{
			name:    "malformed_json",
			status:  http.StatusOK,
			body:    "{invalid json",
			wantErr: ErrSearchFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			plugin := newEMCTestPlugin(t, server.URL+"/")
			_, err := plugin.Search(context.Background(), SearchParams{
				Query: "test",
				Limit: 10,
			}, nil)

			require.Error(t, err)
			assert.True(t, errors.Is(err, tt.wantErr), "expected %v, got %v", tt.wantErr, err)
		})
	}
}

// ---------------------------------------------------------------------------
// Context cancellation test
// ---------------------------------------------------------------------------

func TestEuropePMCContextCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	plugin := newEMCTestPlugin(t, server.URL+"/")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := plugin.Search(ctx, SearchParams{Query: "test", Limit: 10}, nil)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Context timeout test
// ---------------------------------------------------------------------------

func TestEuropePMCContextTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	plugin := &EuropePMCPlugin{}
	shortTimeout := 100 * time.Millisecond
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled: true,
		BaseURL: server.URL + "/",
		Timeout: Duration{Duration: shortTimeout},
	})
	require.NoError(t, err)

	_, searchErr := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.Error(t, searchErr)
}

// ---------------------------------------------------------------------------
// Concurrent search test
// ---------------------------------------------------------------------------

func TestEuropePMCConcurrentSearch(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		results := []emcTestResult{defaultEMCTestResult1()}
		fmt.Fprint(w, buildEMCTestSearchJSON(1, results))
	}))
	defer server.Close()

	plugin := newEMCTestPlugin(t, server.URL+"/")

	var wg sync.WaitGroup
	errs := make([]error, testEMCConcurrentGoroutines)

	for i := range testEMCConcurrentGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := plugin.Search(context.Background(), SearchParams{
				Query: "concurrent test",
				Limit: 10,
			}, nil)
			errs[idx] = err
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d should not error", i)
	}
	assert.Equal(t, int64(testEMCConcurrentGoroutines), requestCount.Load())
}

// ---------------------------------------------------------------------------
// Health tracking test
// ---------------------------------------------------------------------------

func TestEuropePMCHealthTracking(t *testing.T) {
	t.Parallel()

	// First: successful request.
	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildEMCTestSearchJSON(0, nil))
	}))
	defer successServer.Close()

	plugin := newEMCTestPlugin(t, successServer.URL+"/")

	_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.NoError(t, err)

	health := plugin.Health(context.Background())
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)
	assert.True(t, health.Enabled)

	// Second: failing request.
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	plugin.baseURL = failServer.URL + "/"
	_, err = plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.Error(t, err)

	health = plugin.Health(context.Background())
	assert.False(t, health.Healthy)
	assert.NotEmpty(t, health.LastError)

	// Third: successful again — should recover.
	plugin.baseURL = successServer.URL + "/"
	_, err = plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.NoError(t, err)

	health = plugin.Health(context.Background())
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)
}

// ---------------------------------------------------------------------------
// Query building tests
// ---------------------------------------------------------------------------

func TestBuildEMCSearchQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		params       SearchParams
		wantContains []string
		wantEmpty    bool
	}{
		{
			name:         "basic_query",
			params:       SearchParams{Query: "CRISPR"},
			wantContains: []string{"CRISPR"},
		},
		{
			name: "query_with_title",
			params: SearchParams{
				Query:   "cancer",
				Filters: SearchFilters{Title: "immune checkpoint"},
			},
			wantContains: []string{"cancer", `TITLE:"immune checkpoint"`},
		},
		{
			name: "query_with_authors",
			params: SearchParams{
				Query:   "cancer",
				Filters: SearchFilters{Authors: []string{"Smith", "Chen"}},
			},
			wantContains: []string{"cancer", `AUTH:"Smith"`, `AUTH:"Chen"`},
		},
		{
			name: "query_with_date_range",
			params: SearchParams{
				Query: "cancer",
				Filters: SearchFilters{
					DateFrom: "2024-01-01",
					DateTo:   "2024-12-31",
				},
			},
			wantContains: []string{"cancer", "FIRST_PDATE:", "2024-01-01", "2024-12-31"},
		},
		{
			name: "query_with_year_only_dates",
			params: SearchParams{
				Query: "cancer",
				Filters: SearchFilters{
					DateFrom: "2024",
					DateTo:   "2024",
				},
			},
			wantContains: []string{"cancer", "2024-01-01", "2024-12-31"},
		},
		{
			name: "query_with_categories",
			params: SearchParams{
				Query:   "cancer",
				Filters: SearchFilters{Categories: []string{"CRISPR-Cas Systems"}},
			},
			wantContains: []string{"cancer", `"CRISPR-Cas Systems"`},
		},
		{
			name: "combined_filters",
			params: SearchParams{
				Query: "cancer",
				Filters: SearchFilters{
					Title:      "immune",
					Authors:    []string{"Smith"},
					DateFrom:   "2024",
					Categories: []string{"Immunology"},
				},
			},
			wantContains: []string{"cancer", `TITLE:"immune"`, `AUTH:"Smith"`, "FIRST_PDATE:", `"Immunology"`},
		},
		{
			name:      "empty_query_no_filters",
			params:    SearchParams{},
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			query := buildEMCSearchQuery(tt.params)

			if tt.wantEmpty {
				assert.Empty(t, query)
				return
			}

			for _, want := range tt.wantContains {
				assert.Contains(t, query, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Initialize tests
// ---------------------------------------------------------------------------

func TestEuropePMCInitialize(t *testing.T) {
	t.Parallel()

	t.Run("default_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &EuropePMCPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
		})
		require.NoError(t, err)
		assert.Equal(t, emcDefaultBaseURL, plugin.baseURL)
		assert.True(t, plugin.enabled)
		assert.True(t, plugin.healthy)
	})

	t.Run("custom_base_url", func(t *testing.T) {
		t.Parallel()
		customURL := "https://custom.europepmc.org/api/"
		plugin := &EuropePMCPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			BaseURL: customURL,
		})
		require.NoError(t, err)
		assert.Equal(t, customURL, plugin.baseURL)
	})

	t.Run("default_timeout", func(t *testing.T) {
		t.Parallel()
		plugin := &EuropePMCPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
		})
		require.NoError(t, err)
		assert.Equal(t, DefaultPluginTimeout, plugin.httpClient.Timeout)
	})

	t.Run("custom_timeout", func(t *testing.T) {
		t.Parallel()
		customTimeout := 30 * time.Second
		plugin := &EuropePMCPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			Timeout: Duration{Duration: customTimeout},
		})
		require.NoError(t, err)
		assert.Equal(t, customTimeout, plugin.httpClient.Timeout)
	})

	t.Run("rate_limit_stored", func(t *testing.T) {
		t.Parallel()
		expectedRPS := 5.0
		plugin := &EuropePMCPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled:   true,
			RateLimit: expectedRPS,
		})
		require.NoError(t, err)
		assert.InDelta(t, expectedRPS, plugin.rateLimit, 0.001)
	})
}

// ---------------------------------------------------------------------------
// BibTeX assembly test
// ---------------------------------------------------------------------------

func TestEuropePMCBibTeXAssembly(t *testing.T) {
	t.Parallel()

	pub := &Publication{
		Title:     testEMCTitle1,
		Published: testEMCDate1,
		DOI:       testEMCDOI1,
		URL:       "https://europepmc.org/article/MED/" + testEMCPMID1,
		Authors: []Author{
			{Name: "Smith J"},
			{Name: "Chen W"},
		},
		SourceMetadata: map[string]any{
			emcMetaKeyPMID:    testEMCPMID1,
			emcMetaKeyJournal: testEMCJournal1,
		},
	}

	bibtex := assembleEMCBibTeX(pub)

	assert.Contains(t, bibtex, "@article{"+emcBibTeXKeyPrefix+testEMCPMID1)
	assert.Contains(t, bibtex, testEMCTitle1)
	assert.Contains(t, bibtex, "Smith J"+emcBibTeXAuthorSeparator+"Chen W")
	assert.Contains(t, bibtex, "2024")
	assert.Contains(t, bibtex, testEMCJournal1)
	assert.Contains(t, bibtex, testEMCDOI1)
	assert.Contains(t, bibtex, testEMCPMID1)
}

// ---------------------------------------------------------------------------
// Sort order mapping tests
// ---------------------------------------------------------------------------

func TestMapEMCSortOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sort SortOrder
		want string
	}{
		{name: "relevance", sort: SortRelevance, want: emcSortRelevance},
		{name: "date_desc", sort: SortDateDesc, want: emcSortDateDesc},
		{name: "date_asc", sort: SortDateAsc, want: emcSortDateAsc},
		{name: "citations", sort: SortCitations, want: emcSortCitationsDesc},
		{name: "unknown", sort: "unknown", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, mapEMCSortOrder(tt.sort))
		})
	}
}

// ---------------------------------------------------------------------------
// Author parsing tests
// ---------------------------------------------------------------------------

func TestParseEMCAuthors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		authorString string
		wantNames    []string
	}{
		{
			name:         "two_authors",
			authorString: "Smith J, Chen W",
			wantNames:    []string{"Smith J", "Chen W"},
		},
		{
			name:         "trailing_period",
			authorString: "Smith J, Chen W.",
			wantNames:    []string{"Smith J", "Chen W"},
		},
		{
			name:         "single_author",
			authorString: "Smith J",
			wantNames:    []string{"Smith J"},
		},
		{
			name:         "single_author_with_period",
			authorString: "Smith J.",
			wantNames:    []string{"Smith J"},
		},
		{
			name:         "three_authors",
			authorString: "Smith J, Chen W, Doe JA.",
			wantNames:    []string{"Smith J", "Chen W", "Doe JA"},
		},
		{
			name:         "empty_string",
			authorString: "",
			wantNames:    nil,
		},
		{
			name:         "only_period",
			authorString: ".",
			wantNames:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			authors := parseEMCAuthors(tt.authorString)

			if tt.wantNames == nil {
				assert.Nil(t, authors)
				return
			}

			require.Len(t, authors, len(tt.wantNames))
			for i, want := range tt.wantNames {
				assert.Equal(t, want, authors[i].Name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Date range building tests
// ---------------------------------------------------------------------------

func TestBuildEMCDateRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dateFrom string
		dateTo   string
		want     string
	}{
		{
			name:     "full_range",
			dateFrom: "2024-01-01",
			dateTo:   "2024-12-31",
			want:     "(FIRST_PDATE:[2024-01-01 TO 2024-12-31])",
		},
		{
			name:     "year_only",
			dateFrom: "2024",
			dateTo:   "2024",
			want:     "(FIRST_PDATE:[2024-01-01 TO 2024-12-31])",
		},
		{
			name:     "from_only",
			dateFrom: "2024-01-01",
			dateTo:   "",
			want:     "(FIRST_PDATE:[2024-01-01 TO *])",
		},
		{
			name:     "to_only",
			dateFrom: "",
			dateTo:   "2024-12-31",
			want:     "(FIRST_PDATE:[* TO 2024-12-31])",
		},
		{
			name:     "both_empty",
			dateFrom: "",
			dateTo:   "",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, buildEMCDateRange(tt.dateFrom, tt.dateTo))
		})
	}
}

// ---------------------------------------------------------------------------
// URL building tests
// ---------------------------------------------------------------------------

func TestBuildEMCGetURL(t *testing.T) {
	t.Parallel()

	url := buildEMCGetURL("https://example.com/", "12345678")
	assert.Contains(t, url, "EXT_ID%3A12345678")
	assert.Contains(t, url, "format=json")
	assert.Contains(t, url, "pageSize=1")
}

func TestBuildEMCFullTextURL(t *testing.T) {
	t.Parallel()

	url := buildEMCFullTextURL("https://example.com/", "MED", "12345678")
	assert.Equal(t, "https://example.com/MED/12345678/fullTextXML", url)
}
