package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ADS test constants
// ---------------------------------------------------------------------------

const (
	testADSBibcode1     = "2024ApJ...123..456A"
	testADSTitle1       = "Stellar Evolution in Binary Systems"
	testADSAuthor1      = "Smith, J."
	testADSAuthor2      = "Doe, A."
	testADSAff1         = "Harvard-Smithsonian CfA"
	testADSAff2         = "MIT Kavli Institute"
	testADSOrcid1       = "0000-0001-2345-6789"
	testADSAbstract1    = "We study stellar evolution in binary systems."
	testADSDate1        = "2024-01"
	testADSRawDate1     = "2024-01-00"
	testADSDOI1         = "10.3847/1538-4357/abc123"
	testADSCitations1   = 42
	testADSJournal1     = "The Astrophysical Journal"
	testADSVolume1      = "123"
	testADSIssue1       = "4"
	testADSPage1        = "456"
	testADSYear1        = "2024"
	testADSAPIKey       = "test-ads-api-key"
	testADSPerCallKey   = "per-call-ads-key"
	testADSNumFound     = 2
	testADSExpectedPubs = 2
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newADSTestPlugin(t *testing.T, baseURL string) *ADSPlugin {
	t.Helper()
	plugin := &ADSPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled: true,
		BaseURL: baseURL,
		APIKey:  testADSAPIKey,
	})
	require.NoError(t, err)
	return plugin
}

func buildADSTestDoc1() adsDoc {
	return adsDoc{
		Bibcode:       testADSBibcode1,
		Title:         []string{testADSTitle1},
		Author:        []string{testADSAuthor1, testADSAuthor2},
		Abstract:      testADSAbstract1,
		Pubdate:       testADSRawDate1,
		DOI:           []string{testADSDOI1},
		CitationCount: testADSCitations1,
		Year:          testADSYear1,
		Pub:           testADSJournal1,
		Volume:        testADSVolume1,
		Issue:         testADSIssue1,
		Page:          []string{testADSPage1},
		Aff:           []string{testADSAff1, testADSAff2},
		OrcidPub:      []string{testADSOrcid1, ""},
	}
}

func buildADSTestSearchResponse() adsSearchResponse {
	return adsSearchResponse{
		Response: adsResponseBody{
			NumFound: testADSNumFound,
			Start:    0,
			Docs:     []adsDoc{buildADSTestDoc1()},
		},
	}
}

// ---------------------------------------------------------------------------
// Contract test
// ---------------------------------------------------------------------------

func TestADSPluginContract(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(buildADSTestSearchResponse())
	}))
	defer ts.Close()

	plugin := newADSTestPlugin(t, ts.URL)
	PluginContractTest(t, plugin)
}

// ---------------------------------------------------------------------------
// Search tests
// ---------------------------------------------------------------------------

func TestADSSearch(t *testing.T) {
	t.Parallel()

	t.Run("basic_search", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Query().Get(adsParamQuery), "stellar evolution")
			assert.Equal(t, adsDefaultFields, r.URL.Query().Get(adsParamFields))
			assert.Equal(t, testADSAPIKey, strings.TrimPrefix(r.Header.Get(adsAuthHeader), adsAuthPrefix))
			_ = json.NewEncoder(w).Encode(buildADSTestSearchResponse())
		}))
		defer ts.Close()

		plugin := newADSTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query: "stellar evolution",
			Limit: 10,
		}, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, testADSNumFound, result.Total)
		assert.Len(t, result.Results, 1)

		pub := result.Results[0]
		assert.Equal(t, adsPluginID+prefixedIDSeparator+testADSBibcode1, pub.ID)
		assert.Equal(t, testADSTitle1, pub.Title)
		assert.Equal(t, testADSAbstract1, pub.Abstract)
		assert.Equal(t, testADSDate1, pub.Published)
		assert.Equal(t, testADSDOI1, pub.DOI)
		assert.NotNil(t, pub.CitationCount)
		assert.Equal(t, testADSCitations1, *pub.CitationCount)
	})

	t.Run("with_date_filter", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query().Get(adsParamQuery)
			assert.Contains(t, q, "pubdate:[2024-01 TO 2024-06]")
			_ = json.NewEncoder(w).Encode(buildADSTestSearchResponse())
		}))
		defer ts.Close()

		plugin := newADSTestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query: "test",
			Limit: 10,
			Filters: SearchFilters{
				DateFrom: "2024-01",
				DateTo:   "2024-06",
			},
		}, nil)
		require.NoError(t, err)
	})

	t.Run("empty_query", func(t *testing.T) {
		t.Parallel()
		plugin := &ADSPlugin{}
		_, err := plugin.Search(context.Background(), SearchParams{Query: ""}, nil)
		assert.ErrorIs(t, err, ErrADSEmptyQuery)
	})

	t.Run("empty_results", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(adsSearchResponse{
				Response: adsResponseBody{NumFound: 0, Docs: []adsDoc{}},
			})
		}))
		defer ts.Close()

		plugin := newADSTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query: "nonexistent topic xyz",
			Limit: 10,
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, result.Total)
		assert.Empty(t, result.Results)
	})
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestADSGet(t *testing.T) {
	t.Parallel()

	t.Run("valid_bibcode", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query().Get(adsParamQuery)
			assert.Contains(t, q, "bibcode:"+testADSBibcode1)
			_ = json.NewEncoder(w).Encode(adsSearchResponse{
				Response: adsResponseBody{
					NumFound: 1,
					Docs:     []adsDoc{buildADSTestDoc1()},
				},
			})
		}))
		defer ts.Close()

		plugin := newADSTestPlugin(t, ts.URL)
		pub, err := plugin.Get(context.Background(), testADSBibcode1, nil, FormatNative, nil)

		require.NoError(t, err)
		assert.Equal(t, testADSTitle1, pub.Title)
		assert.Equal(t, testADSDOI1, pub.DOI)
		assert.Len(t, pub.Authors, testADSExpectedPubs)
		assert.Equal(t, testADSAuthor1, pub.Authors[0].Name)
		assert.Equal(t, testADSAff1, pub.Authors[0].Affiliation)
		assert.Equal(t, testADSOrcid1, pub.Authors[0].ORCID)
	})

	t.Run("not_found", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(adsSearchResponse{
				Response: adsResponseBody{NumFound: 0, Docs: []adsDoc{}},
			})
		}))
		defer ts.Close()

		plugin := newADSTestPlugin(t, ts.URL)
		_, err := plugin.Get(context.Background(), "INVALID_BIBCODE", nil, FormatNative, nil)
		assert.True(t, errors.Is(err, ErrADSNotFound))
	})
}

// ---------------------------------------------------------------------------
// Auth tests
// ---------------------------------------------------------------------------

func TestADSAuthHeader(t *testing.T) {
	t.Parallel()

	t.Run("server_level_key", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, adsAuthPrefix+testADSAPIKey, r.Header.Get(adsAuthHeader))
			_ = json.NewEncoder(w).Encode(buildADSTestSearchResponse())
		}))
		defer ts.Close()

		plugin := newADSTestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
	})

	t.Run("per_call_override", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, adsAuthPrefix+testADSPerCallKey, r.Header.Get(adsAuthHeader))
			_ = json.NewEncoder(w).Encode(buildADSTestSearchResponse())
		}))
		defer ts.Close()

		plugin := newADSTestPlugin(t, ts.URL)
		creds := &CallCredentials{ADSAPIKey: testADSPerCallKey}
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, creds)
		require.NoError(t, err)
	})

	t.Run("no_key_no_header", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Empty(t, r.Header.Get(adsAuthHeader))
			_ = json.NewEncoder(w).Encode(buildADSTestSearchResponse())
		}))
		defer ts.Close()

		plugin := &ADSPlugin{}
		_ = plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			BaseURL: ts.URL,
		})
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		require.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// Mapping tests
// ---------------------------------------------------------------------------

func TestADSPubdateCleanup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"full_date_with_zeros", "2024-01-00", "2024-01"},
		{"year_month_day", "2024-06-15", "2024-06-15"},
		{"year_only_with_zeros", "2024-00-00", "2024"},
		{"already_clean", "2024-03", "2024-03"},
		{"year_only", "2024", "2024"},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cleanADSPubdate(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestADSAuthorMapping(t *testing.T) {
	t.Parallel()

	t.Run("full_parallel_arrays", func(t *testing.T) {
		t.Parallel()
		authors := mapADSAuthors(
			[]string{testADSAuthor1, testADSAuthor2},
			[]string{testADSAff1, testADSAff2},
			[]string{testADSOrcid1, ""},
		)
		assert.Len(t, authors, testADSExpectedPubs)
		assert.Equal(t, testADSAuthor1, authors[0].Name)
		assert.Equal(t, testADSAff1, authors[0].Affiliation)
		assert.Equal(t, testADSOrcid1, authors[0].ORCID)
		assert.Equal(t, testADSAuthor2, authors[1].Name)
		assert.Equal(t, testADSAff2, authors[1].Affiliation)
		assert.Empty(t, authors[1].ORCID)
	})

	t.Run("mismatched_lengths", func(t *testing.T) {
		t.Parallel()
		authors := mapADSAuthors(
			[]string{testADSAuthor1, testADSAuthor2},
			[]string{testADSAff1},
			nil,
		)
		assert.Len(t, authors, testADSExpectedPubs)
		assert.Equal(t, testADSAff1, authors[0].Affiliation)
		assert.Empty(t, authors[1].Affiliation)
	})

	t.Run("dash_placeholders_ignored", func(t *testing.T) {
		t.Parallel()
		authors := mapADSAuthors(
			[]string{testADSAuthor1},
			[]string{"-"},
			[]string{"-"},
		)
		assert.Empty(t, authors[0].Affiliation)
		assert.Empty(t, authors[0].ORCID)
	})

	t.Run("orcid_url_prefix_stripped", func(t *testing.T) {
		t.Parallel()
		authors := mapADSAuthors(
			[]string{testADSAuthor1},
			nil,
			[]string{"https://orcid.org/0000-0001-2345-6789"},
		)
		assert.Equal(t, testADSOrcid1, authors[0].ORCID)
	})
}

// ---------------------------------------------------------------------------
// HTTP error tests
// ---------------------------------------------------------------------------

func TestADSHTTPErrors(t *testing.T) {
	t.Parallel()

	t.Run("server_500", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer ts.Close()

		plugin := newADSTestPlugin(t, ts.URL)
		_, err := plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 10}, nil)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrSearchFailed))
	})

	t.Run("context_canceled", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Never respond — let context cancel
			select {}
		}))
		defer ts.Close()

		plugin := newADSTestPlugin(t, ts.URL)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := plugin.Search(ctx, SearchParams{Query: "test", Limit: 10}, nil)
		assert.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// Sort mapping test
// ---------------------------------------------------------------------------

func TestADSSortMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sort SortOrder
		want string
	}{
		{"relevance", SortRelevance, adsSortRelevance},
		{"date_desc", SortDateDesc, adsSortDateDesc},
		{"date_asc", SortDateAsc, adsSortDateAsc},
		{"citations", SortCitations, adsSortCitationsDesc},
		{"unknown", SortOrder("unknown"), ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapADSSortOrder(tc.sort)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestADSConcurrentSafety(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(buildADSTestSearchResponse())
	}))
	defer ts.Close()

	plugin := newADSTestPlugin(t, ts.URL)

	const goroutines = 10
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			_, _ = plugin.Search(context.Background(), SearchParams{Query: "test", Limit: 1}, nil)
			_, _ = plugin.Get(context.Background(), testADSBibcode1, nil, FormatNative, nil)
			_ = plugin.Health(context.Background())
		}(i)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
}
