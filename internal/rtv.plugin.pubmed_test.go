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
	testPMPMID1        = "12345678"
	testPMPMID2        = "23456789"
	testPMTitle1       = "CRISPR-Cas9 Gene Editing in Human Embryos"
	testPMTitle2       = "Immunotherapy Response Biomarkers in Melanoma"
	testPMAbstract1    = "We demonstrate a novel application of CRISPR-Cas9 in gene editing."
	testPMAbstract2    = "This study identifies biomarkers predicting immunotherapy response."
	testPMAuthorLast1  = "Smith"
	testPMAuthorFirst1 = "John"
	testPMAuthorInit1  = "J"
	testPMAuthorLast2  = "Chen"
	testPMAuthorFirst2 = "Wei"
	testPMAuthorInit2  = "W"
	testPMAffil1       = "Harvard Medical School"
	testPMAffil2       = "Peking University"
	testPMDOI1         = "10.1234/pmtest.2024.001"
	testPMDOI2         = "10.5678/pmtest.2024.002"
	testPMPMCID1       = "PMC1234567"
	testPMDate1Year    = "2024"
	testPMDate1Month   = "Jan"
	testPMDate1Day     = "15"
	testPMDate2Year    = "2023"
	testPMDate2Month   = "Mar"
	testPMDate2Day     = "20"
	testPMJournal1     = "New England Journal of Medicine"
	testPMJournal2     = "The Lancet"
	testPMVolume1      = "390"
	testPMIssue1       = "1"
	testPMPages1       = "45-58"
	testPMISSN1        = "0028-4793"
	testPMMeSH1        = "CRISPR-Cas Systems"
	testPMMeSH2        = "Gene Editing"
	testPMPubType1     = "Journal Article"
	testPMLanguage1    = "eng"

	testPMPluginTimeout        = 5 * time.Second
	testPMSearchCount          = 150
	testPMConcurrentGoroutines = 10
	testPMAPIKey               = "test-pubmed-api-key"
	testPMCallAPIKey           = "call-level-pubmed-key"
	testPMToolName             = "retrievr-mcp"
	testPMEmail                = "test@example.com"
	testPMWebEnv               = "MCID_test_webenv_123"
	testPMQueryKey             = "1"
)

// ---------------------------------------------------------------------------
// XML fixture builder types
// ---------------------------------------------------------------------------

type pmTestArticle struct {
	PMID         string
	Title        string
	Abstract     string
	Authors      []pmTestAuthor
	DOI          string
	PMCID        string
	JournalTitle string
	Volume       string
	Issue        string
	ISSN         string
	PubYear      string
	PubMonth     string
	PubDay       string
	MeSHTerms    []string
	PubTypes     []string
	Language     string
}

type pmTestAuthor struct {
	LastName    string
	ForeName    string
	Initials    string
	Affiliation string
}

// ---------------------------------------------------------------------------
// XML fixture builders
// ---------------------------------------------------------------------------

// buildPMTestESearchXML generates a complete esearch XML response.
func buildPMTestESearchXML(count, retStart, retMax int, queryKey, webEnv string, pmids []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" ?>
<eSearchResult>
  <Count>%d</Count>
  <RetMax>%d</RetMax>
  <RetStart>%d</RetStart>
  <QueryKey>%s</QueryKey>
  <WebEnv>%s</WebEnv>
  <IdList>`, count, retMax, retStart, queryKey, webEnv))

	for _, pmid := range pmids {
		b.WriteString(fmt.Sprintf("\n    <Id>%s</Id>", pmid))
	}

	b.WriteString("\n  </IdList>\n</eSearchResult>")
	return b.String()
}

// buildPMTestEFetchXML generates a complete PubmedArticleSet XML response.
func buildPMTestEFetchXML(articles []pmTestArticle) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" ?>
<!DOCTYPE PubmedArticleSet PUBLIC "-//NLM//DTD PubMedArticle, 1st January 2024//EN" "https://dtd.nlm.nih.gov/ncbi/pubmed/out/pubmed_240101.dtd">
<PubmedArticleSet>`)

	for _, a := range articles {
		b.WriteString("\n")
		b.WriteString(buildPMTestArticleXML(a))
	}

	b.WriteString("\n</PubmedArticleSet>")
	return b.String()
}

// buildPMTestArticleXML generates a single PubmedArticle XML block.
func buildPMTestArticleXML(a pmTestArticle) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf(`<PubmedArticle>
  <MedlineCitation>
    <PMID>%s</PMID>
    <Article>
      <Journal>
        <ISSN>%s</ISSN>
        <JournalIssue>
          <Volume>%s</Volume>
          <Issue>%s</Issue>
          <PubDate>
            <Year>%s</Year>
            <Month>%s</Month>
            <Day>%s</Day>
          </PubDate>
        </JournalIssue>
        <Title>%s</Title>
      </Journal>
      <ArticleTitle>%s</ArticleTitle>`,
		a.PMID, a.ISSN, a.Volume, a.Issue,
		a.PubYear, a.PubMonth, a.PubDay,
		a.JournalTitle, a.Title))

	// Abstract.
	if a.Abstract != "" {
		b.WriteString(fmt.Sprintf(`
      <Abstract>
        <AbstractText>%s</AbstractText>
      </Abstract>`, a.Abstract))
	}

	// Authors.
	if len(a.Authors) > 0 {
		b.WriteString("\n      <AuthorList>")
		for _, auth := range a.Authors {
			b.WriteString(fmt.Sprintf(`
        <Author>
          <LastName>%s</LastName>
          <ForeName>%s</ForeName>
          <Initials>%s</Initials>`, auth.LastName, auth.ForeName, auth.Initials))
			if auth.Affiliation != "" {
				b.WriteString(fmt.Sprintf(`
          <AffiliationInfo>
            <Affiliation>%s</Affiliation>
          </AffiliationInfo>`, auth.Affiliation))
			}
			b.WriteString("\n        </Author>")
		}
		b.WriteString("\n      </AuthorList>")
	}

	// Language.
	if a.Language != "" {
		b.WriteString(fmt.Sprintf("\n      <Language>%s</Language>", a.Language))
	}

	// Publication types.
	if len(a.PubTypes) > 0 {
		b.WriteString("\n      <PublicationTypeList>")
		for _, pt := range a.PubTypes {
			b.WriteString(fmt.Sprintf("\n        <PublicationType>%s</PublicationType>", pt))
		}
		b.WriteString("\n      </PublicationTypeList>")
	}

	// ELocationID (DOI).
	if a.DOI != "" {
		b.WriteString(fmt.Sprintf(`
      <ELocationID EIdType="doi">%s</ELocationID>`, a.DOI))
	}

	b.WriteString("\n    </Article>")

	// MeSH headings.
	if len(a.MeSHTerms) > 0 {
		b.WriteString("\n    <MeshHeadingList>")
		for _, term := range a.MeSHTerms {
			b.WriteString(fmt.Sprintf(`
      <MeshHeading>
        <DescriptorName>%s</DescriptorName>
      </MeshHeading>`, term))
		}
		b.WriteString("\n    </MeshHeadingList>")
	}

	b.WriteString("\n  </MedlineCitation>")

	// PubmedData with ArticleIdList.
	b.WriteString("\n  <PubmedData>\n    <ArticleIdList>")
	b.WriteString(fmt.Sprintf(`
      <ArticleId IdType="pubmed">%s</ArticleId>`, a.PMID))
	if a.DOI != "" {
		b.WriteString(fmt.Sprintf(`
      <ArticleId IdType="doi">%s</ArticleId>`, a.DOI))
	}
	if a.PMCID != "" {
		b.WriteString(fmt.Sprintf(`
      <ArticleId IdType="pmc">%s</ArticleId>`, a.PMCID))
	}
	b.WriteString("\n    </ArticleIdList>\n  </PubmedData>")

	b.WriteString("\n</PubmedArticle>")
	return b.String()
}

// ---------------------------------------------------------------------------
// Default test articles
// ---------------------------------------------------------------------------

func defaultPMTestArticle1() pmTestArticle {
	return pmTestArticle{
		PMID:     testPMPMID1,
		Title:    testPMTitle1,
		Abstract: testPMAbstract1,
		Authors: []pmTestAuthor{
			{LastName: testPMAuthorLast1, ForeName: testPMAuthorFirst1, Initials: testPMAuthorInit1, Affiliation: testPMAffil1},
			{LastName: testPMAuthorLast2, ForeName: testPMAuthorFirst2, Initials: testPMAuthorInit2, Affiliation: testPMAffil2},
		},
		DOI:          testPMDOI1,
		PMCID:        testPMPMCID1,
		JournalTitle: testPMJournal1,
		Volume:       testPMVolume1,
		Issue:        testPMIssue1,
		ISSN:         testPMISSN1,
		PubYear:      testPMDate1Year,
		PubMonth:     testPMDate1Month,
		PubDay:       testPMDate1Day,
		MeSHTerms:    []string{testPMMeSH1, testPMMeSH2},
		PubTypes:     []string{testPMPubType1},
		Language:     testPMLanguage1,
	}
}

func defaultPMTestArticle2() pmTestArticle {
	return pmTestArticle{
		PMID:     testPMPMID2,
		Title:    testPMTitle2,
		Abstract: testPMAbstract2,
		Authors: []pmTestAuthor{
			{LastName: testPMAuthorLast2, ForeName: testPMAuthorFirst2, Initials: testPMAuthorInit2},
		},
		DOI:          testPMDOI2,
		JournalTitle: testPMJournal2,
		PubYear:      testPMDate2Year,
		PubMonth:     testPMDate2Month,
		PubDay:       testPMDate2Day,
		PubTypes:     []string{testPMPubType1},
		Language:     testPMLanguage1,
	}
}

// ---------------------------------------------------------------------------
// Test plugin factory
// ---------------------------------------------------------------------------

func newPMTestPlugin(t *testing.T, baseURL string) *PubMedPlugin {
	t.Helper()
	plugin := &PubMedPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testPMPluginTimeout},
		RateLimit: 3.0,
		Extra: map[string]string{
			pmExtraKeyTool:  testPMToolName,
			pmExtraKeyEmail: testPMEmail,
		},
	})
	require.NoError(t, err)
	return plugin
}

func newPMTestPluginWithAPIKey(t *testing.T, baseURL, apiKey string) *PubMedPlugin {
	t.Helper()
	plugin := &PubMedPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		Timeout:   Duration{Duration: testPMPluginTimeout},
		RateLimit: 10.0,
		Extra: map[string]string{
			pmExtraKeyTool:  testPMToolName,
			pmExtraKeyEmail: testPMEmail,
		},
	})
	require.NoError(t, err)
	return plugin
}

// ---------------------------------------------------------------------------
// Contract test
// ---------------------------------------------------------------------------

func TestPubMedPluginContract(t *testing.T) {
	t.Parallel()
	plugin := newPMTestPlugin(t, "http://unused.test")
	PluginContractTest(t, plugin)
}

// ---------------------------------------------------------------------------
// Search tests
// ---------------------------------------------------------------------------

func TestPubMedSearch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		params             SearchParams
		esearchXML         string
		efetchXML          string
		wantResultsCnt     int
		wantTotal          int
		wantHasMore        bool
		wantErr            error
		validateESearchReq func(t *testing.T, r *http.Request)
		validateEFetchReq  func(t *testing.T, r *http.Request)
	}{
		{
			name:   "basic_search",
			params: SearchParams{Query: "CRISPR", Limit: 10},
			esearchXML: buildPMTestESearchXML(
				testPMSearchCount, 0, 10, testPMQueryKey, testPMWebEnv,
				[]string{testPMPMID1, testPMPMID2},
			),
			efetchXML:      buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1(), defaultPMTestArticle2()}),
			wantResultsCnt: 2,
			wantTotal:      testPMSearchCount,
			wantHasMore:    true,
			validateESearchReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, pmDBPubMed, r.URL.Query().Get(pmParamDB))
				assert.Equal(t, "CRISPR", r.URL.Query().Get(pmParamTerm))
				assert.Equal(t, pmHistoryY, r.URL.Query().Get(pmParamUseHistory))
				assert.Equal(t, testPMToolName, r.URL.Query().Get(pmParamTool))
				assert.Equal(t, testPMEmail, r.URL.Query().Get(pmParamEmail))
			},
			validateEFetchReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, pmDBPubMed, r.URL.Query().Get(pmParamDB))
				assert.Equal(t, testPMWebEnv, r.URL.Query().Get(pmParamWebEnv))
				assert.Equal(t, testPMQueryKey, r.URL.Query().Get(pmParamQueryKey))
				assert.Equal(t, pmRetTypeXML, r.URL.Query().Get(pmParamRetType))
				assert.Equal(t, pmRetModeXML, r.URL.Query().Get(pmParamRetMode))
			},
		},
		{
			name: "search_with_date_filter_full",
			params: SearchParams{
				Query: "cancer",
				Limit: 10,
				Filters: SearchFilters{
					DateFrom: "2023-01-15",
					DateTo:   "2024-06-30",
				},
			},
			esearchXML: buildPMTestESearchXML(
				1, 0, 10, testPMQueryKey, testPMWebEnv,
				[]string{testPMPMID1},
			),
			efetchXML:      buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateESearchReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, pmDateTypePDAT, r.URL.Query().Get(pmParamDateType))
				assert.Equal(t, "2023/01/15", r.URL.Query().Get(pmParamMinDate))
				assert.Equal(t, "2024/06/30", r.URL.Query().Get(pmParamMaxDate))
			},
		},
		{
			name: "search_with_date_filter_year_only",
			params: SearchParams{
				Query: "gene therapy",
				Limit: 10,
				Filters: SearchFilters{
					DateFrom: "2020",
					DateTo:   "2024",
				},
			},
			esearchXML: buildPMTestESearchXML(
				1, 0, 10, testPMQueryKey, testPMWebEnv,
				[]string{testPMPMID1},
			),
			efetchXML:      buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateESearchReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "2020/01/01", r.URL.Query().Get(pmParamMinDate))
				assert.Equal(t, "2024/12/31", r.URL.Query().Get(pmParamMaxDate))
			},
		},
		{
			name: "search_with_date_from_only",
			params: SearchParams{
				Query: "diabetes",
				Limit: 10,
				Filters: SearchFilters{
					DateFrom: "2023-06-01",
				},
			},
			esearchXML: buildPMTestESearchXML(
				1, 0, 10, testPMQueryKey, testPMWebEnv,
				[]string{testPMPMID1},
			),
			efetchXML:      buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateESearchReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "2023/06/01", r.URL.Query().Get(pmParamMinDate))
				assert.Empty(t, r.URL.Query().Get(pmParamMaxDate))
			},
		},
		{
			name: "search_with_title_filter",
			params: SearchParams{
				Query: "cancer",
				Limit: 10,
				Filters: SearchFilters{
					Title: "immunotherapy",
				},
			},
			esearchXML: buildPMTestESearchXML(
				1, 0, 10, testPMQueryKey, testPMWebEnv,
				[]string{testPMPMID1},
			),
			efetchXML:      buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateESearchReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				term := r.URL.Query().Get(pmParamTerm)
				assert.Contains(t, term, "cancer")
				assert.Contains(t, term, pmQueryQuote+"immunotherapy"+pmQueryQuote+pmFieldTitle)
			},
		},
		{
			name: "search_with_author_filter",
			params: SearchParams{
				Query: "gene editing",
				Limit: 10,
				Filters: SearchFilters{
					Authors: []string{"Smith J", "Chen W"},
				},
			},
			esearchXML: buildPMTestESearchXML(
				1, 0, 10, testPMQueryKey, testPMWebEnv,
				[]string{testPMPMID1},
			),
			efetchXML:      buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateESearchReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				term := r.URL.Query().Get(pmParamTerm)
				assert.Contains(t, term, pmQueryQuote+"Smith J"+pmQueryQuote+pmFieldAuthor)
				assert.Contains(t, term, pmQueryQuote+"Chen W"+pmQueryQuote+pmFieldAuthor)
			},
		},
		{
			name: "search_with_category_filter",
			params: SearchParams{
				Query: "protein folding",
				Limit: 10,
				Filters: SearchFilters{
					Categories: []string{testPMMeSH1},
				},
			},
			esearchXML: buildPMTestESearchXML(
				1, 0, 10, testPMQueryKey, testPMWebEnv,
				[]string{testPMPMID1},
			),
			efetchXML:      buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateESearchReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				term := r.URL.Query().Get(pmParamTerm)
				assert.Contains(t, term, pmQueryQuote+testPMMeSH1+pmQueryQuote+pmFieldMeSH)
			},
		},
		{
			name: "search_with_sort_date",
			params: SearchParams{
				Query: "alzheimer",
				Limit: 10,
				Sort:  SortDateDesc,
			},
			esearchXML: buildPMTestESearchXML(
				1, 0, 10, testPMQueryKey, testPMWebEnv,
				[]string{testPMPMID1},
			),
			efetchXML:      buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantResultsCnt: 1,
			wantTotal:      1,
			wantHasMore:    false,
			validateESearchReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, pmSortPubDate, r.URL.Query().Get(pmParamSort))
			},
		},
		{
			name: "search_pagination",
			params: SearchParams{
				Query:  "oncology",
				Limit:  5,
				Offset: 10,
			},
			esearchXML: buildPMTestESearchXML(
				50, 10, 5, testPMQueryKey, testPMWebEnv,
				[]string{testPMPMID1},
			),
			efetchXML:      buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantResultsCnt: 1,
			wantTotal:      50,
			wantHasMore:    true,
			validateESearchReq: func(t *testing.T, r *http.Request) {
				t.Helper()
				assert.Equal(t, "10", r.URL.Query().Get(pmParamRetStart))
				assert.Equal(t, "5", r.URL.Query().Get(pmParamRetMax))
			},
		},
		{
			name:   "search_empty_results",
			params: SearchParams{Query: "xyznonexistent", Limit: 10},
			esearchXML: buildPMTestESearchXML(
				0, 0, 10, testPMQueryKey, testPMWebEnv,
				[]string{},
			),
			efetchXML:      "",
			wantResultsCnt: 0,
			wantTotal:      0,
			wantHasMore:    false,
		},
		{
			name:    "search_empty_query",
			params:  SearchParams{Query: "", Limit: 10},
			wantErr: ErrPubMedEmptyQuery,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.wantErr != nil {
				plugin := newPMTestPlugin(t, "http://unused.test")
				_, err := plugin.Search(context.Background(), tc.params, nil)
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")

				if strings.Contains(r.URL.Path, pmESearchPath) {
					if tc.validateESearchReq != nil {
						tc.validateESearchReq(t, r)
					}
					fmt.Fprint(w, tc.esearchXML)
					return
				}
				if strings.Contains(r.URL.Path, pmEFetchPath) {
					if tc.validateEFetchReq != nil {
						tc.validateEFetchReq(t, r)
					}
					fmt.Fprint(w, tc.efetchXML)
					return
				}
				http.NotFound(w, r)
			}))
			defer ts.Close()

			plugin := newPMTestPlugin(t, ts.URL+"/")
			result, err := plugin.Search(context.Background(), tc.params, nil)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tc.wantTotal, result.Total)
			assert.Len(t, result.Results, tc.wantResultsCnt)
			assert.Equal(t, tc.wantHasMore, result.HasMore)
		})
	}
}

// ---------------------------------------------------------------------------
// Search API key tests
// ---------------------------------------------------------------------------

func TestPubMedSearchWithAPIKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		serverKey      string
		callCreds      *CallCredentials
		wantKeyInParam string
	}{
		{
			name:           "server_level_api_key",
			serverKey:      testPMAPIKey,
			callCreds:      nil,
			wantKeyInParam: testPMAPIKey,
		},
		{
			name:           "per_call_api_key_overrides_server",
			serverKey:      testPMAPIKey,
			callCreds:      &CallCredentials{PubMedAPIKey: testPMCallAPIKey},
			wantKeyInParam: testPMCallAPIKey,
		},
		{
			name:           "no_api_key_no_param",
			serverKey:      "",
			callCreds:      nil,
			wantKeyInParam: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")

				if strings.Contains(r.URL.Path, pmESearchPath) {
					if tc.wantKeyInParam != "" {
						assert.Equal(t, tc.wantKeyInParam, r.URL.Query().Get(pmParamAPIKey))
					} else {
						assert.Empty(t, r.URL.Query().Get(pmParamAPIKey))
					}
					fmt.Fprint(w, buildPMTestESearchXML(1, 0, 10, testPMQueryKey, testPMWebEnv, []string{testPMPMID1}))
					return
				}
				if strings.Contains(r.URL.Path, pmEFetchPath) {
					if tc.wantKeyInParam != "" {
						assert.Equal(t, tc.wantKeyInParam, r.URL.Query().Get(pmParamAPIKey))
					} else {
						assert.Empty(t, r.URL.Query().Get(pmParamAPIKey))
					}
					fmt.Fprint(w, buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}))
					return
				}
			}))
			defer ts.Close()

			var plugin *PubMedPlugin
			if tc.serverKey != "" {
				plugin = newPMTestPluginWithAPIKey(t, ts.URL+"/", tc.serverKey)
			} else {
				plugin = newPMTestPlugin(t, ts.URL+"/")
			}

			_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, tc.callCreds)
			require.NoError(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// Tool and email parameter tests
// ---------------------------------------------------------------------------

func TestPubMedSearchToolAndEmailParams(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")

		assert.Equal(t, testPMToolName, r.URL.Query().Get(pmParamTool))
		assert.Equal(t, testPMEmail, r.URL.Query().Get(pmParamEmail))

		if strings.Contains(r.URL.Path, pmESearchPath) {
			fmt.Fprint(w, buildPMTestESearchXML(1, 0, 10, testPMQueryKey, testPMWebEnv, []string{testPMPMID1}))
			return
		}
		if strings.Contains(r.URL.Path, pmEFetchPath) {
			fmt.Fprint(w, buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}))
			return
		}
	}))
	defer ts.Close()

	plugin := newPMTestPlugin(t, ts.URL+"/")
	_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Search result mapping tests
// ---------------------------------------------------------------------------

func TestPubMedSearchResultMapping(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if strings.Contains(r.URL.Path, pmESearchPath) {
			fmt.Fprint(w, buildPMTestESearchXML(1, 0, 10, testPMQueryKey, testPMWebEnv, []string{testPMPMID1}))
			return
		}
		if strings.Contains(r.URL.Path, pmEFetchPath) {
			fmt.Fprint(w, buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}))
			return
		}
	}))
	defer ts.Close()

	plugin := newPMTestPlugin(t, ts.URL+"/")
	result, err := plugin.Search(context.Background(), SearchParams{Query: "CRISPR", Limit: 10}, nil)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)

	pub := result.Results[0]

	// Core fields.
	assert.Equal(t, SourcePubMed+":"+testPMPMID1, pub.ID)
	assert.Equal(t, SourcePubMed, pub.Source)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, testPMTitle1, pub.Title)
	assert.Equal(t, testPMAbstract1, pub.Abstract)
	assert.Equal(t, pmAbsURLPrefix+testPMPMID1, pub.URL)
	assert.Equal(t, testPMDOI1, pub.DOI)

	// Authors.
	require.Len(t, pub.Authors, 2)
	assert.Equal(t, testPMAuthorFirst1+" "+testPMAuthorLast1, pub.Authors[0].Name)
	assert.Equal(t, testPMAffil1, pub.Authors[0].Affiliation)
	assert.Equal(t, testPMAuthorFirst2+" "+testPMAuthorLast2, pub.Authors[1].Name)
	assert.Equal(t, testPMAffil2, pub.Authors[1].Affiliation)

	// Date: 2024-01-15.
	assert.Equal(t, testPMDate1Year+"-01-"+testPMDate1Day, pub.Published)

	// PMC URL as PDFURL.
	assert.Equal(t, pmPMCURLPrefix+testPMPMCID1, pub.PDFURL)

	// MeSH terms as categories.
	assert.Contains(t, pub.Categories, testPMMeSH1)
	assert.Contains(t, pub.Categories, testPMMeSH2)

	// Source metadata.
	require.NotNil(t, pub.SourceMetadata)
	assert.Equal(t, testPMPMID1, pub.SourceMetadata[pmMetaKeyPMID])
	assert.Equal(t, testPMPMCID1, pub.SourceMetadata[pmMetaKeyPMCID])
	assert.Equal(t, testPMJournal1, pub.SourceMetadata[pmMetaKeyJournal])
	assert.Equal(t, testPMVolume1, pub.SourceMetadata[pmMetaKeyVolume])
	assert.Equal(t, testPMIssue1, pub.SourceMetadata[pmMetaKeyIssue])
	assert.Equal(t, testPMISSN1, pub.SourceMetadata[pmMetaKeyISSN])
	assert.Equal(t, testPMLanguage1, pub.SourceMetadata[pmMetaKeyLanguage])
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestPubMedGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		format     ContentFormat
		efetchXML  string
		wantErr    error
		wantTitle  string
		wantBibTeX bool
	}{
		{
			name:      "get_by_pmid_native",
			id:        testPMPMID1,
			format:    FormatNative,
			efetchXML: buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantTitle: testPMTitle1,
		},
		{
			name:       "get_bibtex_format",
			id:         testPMPMID1,
			format:     FormatBibTeX,
			efetchXML:  buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantTitle:  testPMTitle1,
			wantBibTeX: true,
		},
		{
			name:      "get_json_format",
			id:        testPMPMID1,
			format:    FormatJSON,
			efetchXML: buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantTitle: testPMTitle1,
		},
		{
			name:      "get_not_found",
			id:        "99999999",
			format:    FormatNative,
			efetchXML: buildPMTestEFetchXML([]pmTestArticle{}),
			wantErr:   ErrPubMedNotFound,
		},
		{
			name:      "get_unsupported_format",
			id:        testPMPMID1,
			format:    FormatMarkdown,
			efetchXML: buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}),
			wantErr:   ErrFormatUnsupported,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				assert.Equal(t, tc.id, r.URL.Query().Get(pmParamID))
				fmt.Fprint(w, tc.efetchXML)
			}))
			defer ts.Close()

			plugin := newPMTestPlugin(t, ts.URL+"/")
			pub, err := plugin.Get(context.Background(), tc.id, nil, tc.format, nil)

			if tc.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErr), "expected %v, got %v", tc.wantErr, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, pub)
			assert.Equal(t, tc.wantTitle, pub.Title)

			if tc.wantBibTeX {
				require.NotNil(t, pub.FullText)
				assert.Equal(t, FormatBibTeX, pub.FullText.ContentFormat)
				assert.Contains(t, pub.FullText.Content, "@article{")
				assert.Contains(t, pub.FullText.Content, testPMTitle1)
				assert.Contains(t, pub.FullText.Content, testPMDOI1)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Get with full text test
// ---------------------------------------------------------------------------

func TestPubMedGetWithFullText(t *testing.T) {
	t.Parallel()

	const testPMCFullText = "<article><body><p>Full text content here.</p></body></article>"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")

		db := r.URL.Query().Get(pmParamDB)
		switch db {
		case pmDBPMC:
			// PMC full text request.
			assert.Equal(t, testPMPMCID1, r.URL.Query().Get(pmParamID))
			fmt.Fprint(w, testPMCFullText)
		case pmDBPubMed:
			// Regular efetch request.
			fmt.Fprint(w, buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	plugin := newPMTestPlugin(t, ts.URL+"/")
	pub, err := plugin.Get(context.Background(), testPMPMID1, []IncludeField{IncludeFullText}, FormatNative, nil)
	require.NoError(t, err)
	require.NotNil(t, pub)
	require.NotNil(t, pub.FullText)
	assert.Equal(t, FormatXML, pub.FullText.ContentFormat)
	assert.Contains(t, pub.FullText.Content, "Full text content here.")
	assert.Equal(t, len(testPMCFullText), pub.FullText.ContentLength)
}

// ---------------------------------------------------------------------------
// HTTP error tests
// ---------------------------------------------------------------------------

func TestPubMedHTTPErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		esearchStatus int
		esearchBody   string
		efetchStatus  int
		efetchBody    string
		failOnESearch bool
		wantErrSearch error
		wantErrGet    error
	}{
		{
			name:          "esearch_500",
			esearchStatus: http.StatusInternalServerError,
			esearchBody:   "",
			failOnESearch: true,
			wantErrSearch: ErrSearchFailed,
		},
		{
			name:          "efetch_500",
			esearchStatus: http.StatusOK,
			esearchBody: buildPMTestESearchXML(
				1, 0, 10, testPMQueryKey, testPMWebEnv, []string{testPMPMID1},
			),
			efetchStatus:  http.StatusInternalServerError,
			efetchBody:    "",
			failOnESearch: false,
			wantErrSearch: ErrSearchFailed,
		},
		{
			name:          "efetch_404_on_get",
			esearchStatus: http.StatusOK,
			esearchBody:   "",
			efetchStatus:  http.StatusNotFound,
			efetchBody:    "",
			failOnESearch: false,
			wantErrGet:    ErrPubMedNotFound,
		},
		{
			name:          "esearch_malformed_xml",
			esearchStatus: http.StatusOK,
			esearchBody:   "not valid xml <<<<",
			failOnESearch: true,
			wantErrSearch: ErrSearchFailed,
		},
		{
			name:          "efetch_malformed_xml",
			esearchStatus: http.StatusOK,
			esearchBody: buildPMTestESearchXML(
				1, 0, 10, testPMQueryKey, testPMWebEnv, []string{testPMPMID1},
			),
			efetchStatus:  http.StatusOK,
			efetchBody:    "not valid xml <<<<",
			failOnESearch: false,
			wantErrSearch: ErrSearchFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, pmESearchPath) {
					w.WriteHeader(tc.esearchStatus)
					fmt.Fprint(w, tc.esearchBody)
					return
				}
				if strings.Contains(r.URL.Path, pmEFetchPath) {
					status := tc.efetchStatus
					if status == 0 {
						status = http.StatusOK
					}
					w.WriteHeader(status)
					fmt.Fprint(w, tc.efetchBody)
					return
				}
			}))
			defer ts.Close()

			plugin := newPMTestPlugin(t, ts.URL+"/")

			// Test search errors.
			if tc.wantErrSearch != nil {
				_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErrSearch), "search: expected %v, got %v", tc.wantErrSearch, err)
			}

			// Test get errors.
			if tc.wantErrGet != nil {
				_, err := plugin.Get(context.Background(), testPMPMID1, nil, FormatNative, nil)
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErrGet), "get: expected %v, got %v", tc.wantErrGet, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Context cancellation test
// ---------------------------------------------------------------------------

func TestPubMedContextCancellation(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(testPMPluginTimeout)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	plugin := newPMTestPlugin(t, ts.URL+"/")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := plugin.Search(ctx, SearchParams{Query: "test", Limit: 10}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSearchFailed), "expected ErrSearchFailed, got %v", err)
}

// ---------------------------------------------------------------------------
// Context timeout test
// ---------------------------------------------------------------------------

func TestPubMedContextTimeout(t *testing.T) {
	t.Parallel()

	const testShortTimeout = 50 * time.Millisecond
	const testBlockDuration = 2 * time.Second

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(testBlockDuration)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	plugin := newPMTestPlugin(t, ts.URL+"/")

	ctx, cancel := context.WithTimeout(context.Background(), testShortTimeout)
	defer cancel()

	_, err := plugin.Search(ctx, SearchParams{Query: "test", Limit: 10}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSearchFailed), "expected ErrSearchFailed, got %v", err)
}

// ---------------------------------------------------------------------------
// Concurrent search test
// ---------------------------------------------------------------------------

func TestPubMedConcurrentSearch(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if strings.Contains(r.URL.Path, pmESearchPath) {
			fmt.Fprint(w, buildPMTestESearchXML(1, 0, 10, testPMQueryKey, testPMWebEnv, []string{testPMPMID1}))
			return
		}
		if strings.Contains(r.URL.Path, pmEFetchPath) {
			fmt.Fprint(w, buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}))
			return
		}
	}))
	defer ts.Close()

	plugin := newPMTestPlugin(t, ts.URL+"/")

	var wg sync.WaitGroup
	var errCount atomic.Int32

	for i := 0; i < testPMConcurrentGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := plugin.Search(context.Background(), SearchParams{Query: "concurrent", Limit: 10}, nil)
			if err != nil || result == nil || len(result.Results) == 0 {
				errCount.Add(1)
			}
		}()
	}

	wg.Wait()
	assert.Equal(t, int32(0), errCount.Load(), "concurrent searches should all succeed")
}

// ---------------------------------------------------------------------------
// Health tracking test
// ---------------------------------------------------------------------------

func TestPubMedHealthTracking(t *testing.T) {
	t.Parallel()

	// Success path.
	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if strings.Contains(r.URL.Path, pmESearchPath) {
			fmt.Fprint(w, buildPMTestESearchXML(1, 0, 10, testPMQueryKey, testPMWebEnv, []string{testPMPMID1}))
			return
		}
		if strings.Contains(r.URL.Path, pmEFetchPath) {
			fmt.Fprint(w, buildPMTestEFetchXML([]pmTestArticle{defaultPMTestArticle1()}))
			return
		}
	}))
	defer successServer.Close()

	plugin := newPMTestPlugin(t, successServer.URL+"/")

	// Initially healthy.
	health := plugin.Health(context.Background())
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)

	// After successful search, still healthy.
	_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.NoError(t, err)
	health = plugin.Health(context.Background())
	assert.True(t, health.Healthy)
	assert.Empty(t, health.LastError)

	// Error path.
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errorServer.Close()

	errorPlugin := newPMTestPlugin(t, errorServer.URL+"/")
	_, err = errorPlugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
	require.Error(t, err)
	health = errorPlugin.Health(context.Background())
	assert.False(t, health.Healthy)
	assert.NotEmpty(t, health.LastError)
}

// ---------------------------------------------------------------------------
// Query building tests
// ---------------------------------------------------------------------------

func TestBuildPMSearchQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		params   SearchParams
		expected string
	}{
		{
			name:     "query_only",
			params:   SearchParams{Query: "CRISPR"},
			expected: "CRISPR",
		},
		{
			name: "query_with_title",
			params: SearchParams{
				Query:   "gene therapy",
				Filters: SearchFilters{Title: "CRISPR"},
			},
			expected: "gene therapy" + pmQueryAND + pmQueryQuote + "CRISPR" + pmQueryQuote + pmFieldTitle,
		},
		{
			name: "query_with_authors",
			params: SearchParams{
				Query:   "cancer",
				Filters: SearchFilters{Authors: []string{"Smith J"}},
			},
			expected: "cancer" + pmQueryAND + pmQueryQuote + "Smith J" + pmQueryQuote + pmFieldAuthor,
		},
		{
			name: "query_with_categories",
			params: SearchParams{
				Query:   "protein",
				Filters: SearchFilters{Categories: []string{"Proteins"}},
			},
			expected: "protein" + pmQueryAND + pmQueryQuote + "Proteins" + pmQueryQuote + pmFieldMeSH,
		},
		{
			name: "all_filters_combined",
			params: SearchParams{
				Query: "drug",
				Filters: SearchFilters{
					Title:      "aspirin",
					Authors:    []string{"Doe A"},
					Categories: []string{"Pain"},
				},
			},
			expected: "drug" + pmQueryAND +
				pmQueryQuote + "aspirin" + pmQueryQuote + pmFieldTitle + pmQueryAND +
				pmQueryQuote + "Doe A" + pmQueryQuote + pmFieldAuthor + pmQueryAND +
				pmQueryQuote + "Pain" + pmQueryQuote + pmFieldMeSH,
		},
		{
			name:     "empty_query_empty_filters",
			params:   SearchParams{},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := buildPMSearchQuery(tc.params)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// ---------------------------------------------------------------------------
// Date conversion tests
// ---------------------------------------------------------------------------

func TestConvertPMDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		date      string
		isEndDate bool
		expected  string
	}{
		{
			name:      "full_date_start",
			date:      "2024-01-15",
			isEndDate: false,
			expected:  "2024/01/15",
		},
		{
			name:      "full_date_end",
			date:      "2024-06-30",
			isEndDate: true,
			expected:  "2024/06/30",
		},
		{
			name:      "year_only_start",
			date:      "2020",
			isEndDate: false,
			expected:  "2020/01/01",
		},
		{
			name:      "year_only_end",
			date:      "2024",
			isEndDate: true,
			expected:  "2024/12/31",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := convertPMDate(tc.date, tc.isEndDate)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// ---------------------------------------------------------------------------
// Date assembly tests
// ---------------------------------------------------------------------------

func TestAssemblePMDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pubDate  pmPubDate
		expected string
	}{
		{
			name:     "full_structured_date",
			pubDate:  pmPubDate{Year: "2024", Month: "Jan", Day: "15"},
			expected: "2024-01-15",
		},
		{
			name:     "year_and_month_only",
			pubDate:  pmPubDate{Year: "2024", Month: "Mar"},
			expected: "2024-03",
		},
		{
			name:     "year_only",
			pubDate:  pmPubDate{Year: "2024"},
			expected: "2024",
		},
		{
			name:     "medline_date_fallback",
			pubDate:  pmPubDate{MedlineDate: "2024 Jan-Feb"},
			expected: "2024",
		},
		{
			name:     "numeric_month",
			pubDate:  pmPubDate{Year: "2024", Month: "06", Day: "01"},
			expected: "2024-06-01",
		},
		{
			name:     "single_digit_day",
			pubDate:  pmPubDate{Year: "2024", Month: "Jan", Day: "5"},
			expected: "2024-01-05",
		},
		{
			name:     "empty_date",
			pubDate:  pmPubDate{},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := assemblePMDate(tc.pubDate)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// ---------------------------------------------------------------------------
// Initialize tests
// ---------------------------------------------------------------------------

func TestPubMedInitialize(t *testing.T) {
	t.Parallel()

	t.Run("default_values", func(t *testing.T) {
		t.Parallel()
		plugin := &PubMedPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
		})
		require.NoError(t, err)
		assert.True(t, plugin.enabled)
		assert.Equal(t, pmDefaultBaseURL, plugin.baseURL)
		assert.Equal(t, pmDefaultTool, plugin.toolName)
		assert.Equal(t, DefaultPluginTimeout, plugin.httpClient.Timeout)
		assert.True(t, plugin.healthy)
	})

	t.Run("custom_values", func(t *testing.T) {
		t.Parallel()
		plugin := &PubMedPlugin{}
		customTimeout := 30 * time.Second
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled:   true,
			APIKey:    testPMAPIKey,
			BaseURL:   "https://custom.test/",
			Timeout:   Duration{Duration: customTimeout},
			RateLimit: 10.0,
			Extra: map[string]string{
				pmExtraKeyTool:  "custom-tool",
				pmExtraKeyEmail: "custom@example.com",
			},
		})
		require.NoError(t, err)
		assert.Equal(t, testPMAPIKey, plugin.apiKey)
		assert.Equal(t, "https://custom.test/", plugin.baseURL)
		assert.Equal(t, customTimeout, plugin.httpClient.Timeout)
		assert.Equal(t, "custom-tool", plugin.toolName)
		assert.Equal(t, "custom@example.com", plugin.email)
		assert.Equal(t, 10.0, plugin.rateLimit)
	})

	t.Run("nil_extra_uses_defaults", func(t *testing.T) {
		t.Parallel()
		plugin := &PubMedPlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
		})
		require.NoError(t, err)
		assert.Equal(t, pmDefaultTool, plugin.toolName)
		assert.Empty(t, plugin.email)
	})
}

// ---------------------------------------------------------------------------
// BibTeX assembly test
// ---------------------------------------------------------------------------

func TestPubMedBibTeXAssembly(t *testing.T) {
	t.Parallel()

	pub := &Publication{
		Title:     testPMTitle1,
		Published: testPMDate1Year + "-01-" + testPMDate1Day,
		DOI:       testPMDOI1,
		URL:       pmAbsURLPrefix + testPMPMID1,
		Authors: []Author{
			{Name: testPMAuthorFirst1 + " " + testPMAuthorLast1},
			{Name: testPMAuthorFirst2 + " " + testPMAuthorLast2},
		},
		SourceMetadata: map[string]any{
			pmMetaKeyPMID:    testPMPMID1,
			pmMetaKeyJournal: testPMJournal1,
		},
	}

	bibtex := assemblePMBibTeX(pub)
	assert.Contains(t, bibtex, "@article{"+pmBibTeXKeyPrefix+testPMPMID1)
	assert.Contains(t, bibtex, testPMTitle1)
	assert.Contains(t, bibtex, testPMDOI1)
	assert.Contains(t, bibtex, testPMJournal1)
	assert.Contains(t, bibtex, testPMPMID1)
	assert.Contains(t, bibtex, testPMAuthorFirst1+" "+testPMAuthorLast1+pmBibTeXAuthorSeparator+testPMAuthorFirst2+" "+testPMAuthorLast2)
	assert.Contains(t, bibtex, testPMDate1Year)
}

// ---------------------------------------------------------------------------
// Sort order mapping test
// ---------------------------------------------------------------------------

func TestMapPMSortOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sort     SortOrder
		expected string
	}{
		{"relevance", SortRelevance, pmSortRelevance},
		{"date_desc", SortDateDesc, pmSortPubDate},
		{"citations_unsupported", SortCitations, ""},
		{"date_asc_unsupported", SortDateAsc, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, mapPMSortOrder(tc.sort))
		})
	}
}

// ---------------------------------------------------------------------------
// Credential resolution test
// ---------------------------------------------------------------------------

func TestResolvePMAPIKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		creds         *CallCredentials
		serverDefault string
		expected      string
	}{
		{
			name:          "nil_creds_returns_server_default",
			creds:         nil,
			serverDefault: testPMAPIKey,
			expected:      testPMAPIKey,
		},
		{
			name:          "empty_creds_returns_server_default",
			creds:         &CallCredentials{},
			serverDefault: testPMAPIKey,
			expected:      testPMAPIKey,
		},
		{
			name:          "per_call_overrides_server",
			creds:         &CallCredentials{PubMedAPIKey: testPMCallAPIKey},
			serverDefault: testPMAPIKey,
			expected:      testPMCallAPIKey,
		},
		{
			name:          "per_call_with_no_server_default",
			creds:         &CallCredentials{PubMedAPIKey: testPMCallAPIKey},
			serverDefault: "",
			expected:      testPMCallAPIKey,
		},
		{
			name:          "no_creds_no_default",
			creds:         nil,
			serverDefault: "",
			expected:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := resolvePMAPIKey(tc.creds, tc.serverDefault)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// ---------------------------------------------------------------------------
// Author name assembly test
// ---------------------------------------------------------------------------

func TestAssemblePMAuthorName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		author   pmAuthor
		expected string
	}{
		{
			name:     "full_name",
			author:   pmAuthor{ForeName: "John", LastName: "Smith"},
			expected: "John Smith",
		},
		{
			name:     "last_name_only",
			author:   pmAuthor{LastName: "Smith"},
			expected: "Smith",
		},
		{
			name:     "empty_author",
			author:   pmAuthor{},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := assemblePMAuthorName(tc.author)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// ---------------------------------------------------------------------------
// ArticleID extraction tests
// ---------------------------------------------------------------------------

func TestExtractPMArticleID(t *testing.T) {
	t.Parallel()

	data := &pmPubmedData{
		ArticleIDList: pmArticleIDList{
			ArticleIDs: []pmArticleID{
				{IDType: pmArticleIDTypePubmed, Value: testPMPMID1},
				{IDType: pmArticleIDTypeDOI, Value: testPMDOI1},
				{IDType: pmArticleIDTypePMC, Value: testPMPMCID1},
			},
		},
	}

	assert.Equal(t, testPMDOI1, extractPMArticleID(data, pmArticleIDTypeDOI))
	assert.Equal(t, testPMPMCID1, extractPMArticleID(data, pmArticleIDTypePMC))
	assert.Equal(t, testPMPMID1, extractPMArticleID(data, pmArticleIDTypePubmed))
	assert.Empty(t, extractPMArticleID(data, "nonexistent"))
}

func TestExtractPMDOIFromELocation(t *testing.T) {
	t.Parallel()

	elocations := []pmELocationID{
		{EIDType: pmArticleIDTypeDOI, Value: testPMDOI1},
	}
	assert.Equal(t, testPMDOI1, extractPMDOIFromELocation(elocations))
	assert.Empty(t, extractPMDOIFromELocation(nil))
	assert.Empty(t, extractPMDOIFromELocation([]pmELocationID{{EIDType: "pii", Value: "S0140-6736(24)00001-1"}}))
}

// ---------------------------------------------------------------------------
// MeSH term extraction test
// ---------------------------------------------------------------------------

func TestExtractPMMeSHTerms(t *testing.T) {
	t.Parallel()

	meshList := pmMeshHeadingList{
		Headings: []pmMeshHeading{
			{DescriptorName: testPMMeSH1},
			{DescriptorName: testPMMeSH2},
			{DescriptorName: ""}, // empty should be skipped
		},
	}

	terms := extractPMMeSHTerms(meshList)
	assert.Len(t, terms, 2)
	assert.Contains(t, terms, testPMMeSH1)
	assert.Contains(t, terms, testPMMeSH2)

	// Empty list.
	assert.Nil(t, extractPMMeSHTerms(pmMeshHeadingList{}))
}
