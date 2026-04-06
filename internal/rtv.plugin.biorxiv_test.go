package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// bioRxiv test constants
// ---------------------------------------------------------------------------

const (
	testBiorxivDOI1                = "10.1101/2024.01.15.575123"
	testBiorxivTitle1              = "Neural Circuit Dynamics in Prefrontal Cortex"
	testBiorxivAuthors1            = "Smith, John; Doe, Jane; Lee, Wei"
	testBiorxivAbstract1           = "We investigate neural circuit dynamics."
	testBiorxivDate1               = "2024-01-15"
	testBiorxivDateFrom            = "2024-01-01"
	testBiorxivDateTo              = "2024-01-31"
	testBiorxivCategory1           = "neuroscience"
	testBiorxivVersion1            = "1"
	testBiorxivPublishedDOI1       = "10.1038/s41586-024-07487-w"
	testBiorxivTotalStr            = "42"
	testBiorxivTotalInt            = 42
	testBiorxivExpectedAuthorCount = 3
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newBiorxivTestPlugin(t *testing.T, baseURL string) *BioRxivPlugin {
	t.Helper()
	plugin := &BioRxivPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled: true,
		BaseURL: baseURL,
		Extra:   map[string]string{biorxivExtraKeyServers: biorxivServerBiorxiv},
	})
	require.NoError(t, err)
	return plugin
}

func newBiorxivDualServerTestPlugin(t *testing.T, baseURL string) *BioRxivPlugin {
	t.Helper()
	plugin := &BioRxivPlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled: true,
		BaseURL: baseURL,
		Extra:   map[string]string{biorxivExtraKeyServers: biorxivServerBiorxiv + biorxivServerSeparator + biorxivServerMedrxiv},
	})
	require.NoError(t, err)
	return plugin
}

func buildBiorxivTestArticle1() biorxivArticle {
	return biorxivArticle{
		DOI:          testBiorxivDOI1,
		Title:        testBiorxivTitle1,
		Authors:      testBiorxivAuthors1,
		Abstract:     testBiorxivAbstract1,
		Date:         testBiorxivDate1,
		Category:     testBiorxivCategory1,
		Version:      testBiorxivVersion1,
		Server:       biorxivServerBiorxiv,
		PublishedDOI: testBiorxivPublishedDOI1,
	}
}

func buildBiorxivTestResponse() biorxivResponse {
	return biorxivResponse{
		Collection: []biorxivArticle{buildBiorxivTestArticle1()},
		Messages: []biorxivMsg{{
			Status: "ok",
			Total:  testBiorxivTotalStr,
			Count:  "1",
		}},
	}
}

// ---------------------------------------------------------------------------
// Contract test
// ---------------------------------------------------------------------------

func TestBioRxivPluginContract(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(buildBiorxivTestResponse())
	}))
	defer ts.Close()

	plugin := newBiorxivTestPlugin(t, ts.URL)
	PluginContractTest(t, plugin)
}

// ---------------------------------------------------------------------------
// Search tests
// ---------------------------------------------------------------------------

func TestBioRxivSearch(t *testing.T) {
	t.Parallel()

	t.Run("with_date_from", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, testBiorxivDateFrom)
			_ = json.NewEncoder(w).Encode(buildBiorxivTestResponse())
		}))
		defer ts.Close()

		plugin := newBiorxivTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query: "ignored for biorxiv",
			Limit: 10,
			Filters: SearchFilters{
				DateFrom: testBiorxivDateFrom,
				DateTo:   testBiorxivDateTo,
			},
		}, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, testBiorxivTotalInt, result.Total)
		assert.Len(t, result.Results, 1)

		pub := result.Results[0]
		assert.Equal(t, biorxivPluginID+prefixedIDSeparator+testBiorxivDOI1, pub.ID)
		assert.Equal(t, testBiorxivTitle1, pub.Title)
		assert.Equal(t, testBiorxivAbstract1, pub.Abstract)
		assert.Equal(t, testBiorxivDate1, pub.Published)
		assert.Equal(t, testBiorxivDOI1, pub.DOI)
	})

	t.Run("without_date_from_returns_error", func(t *testing.T) {
		t.Parallel()
		plugin := &BioRxivPlugin{}
		_, err := plugin.Search(context.Background(), SearchParams{
			Query: "test",
			Limit: 10,
		}, nil)
		assert.ErrorIs(t, err, ErrBiorxivDateRequired)
	})

	t.Run("dual_server_merge", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(buildBiorxivTestResponse())
		}))
		defer ts.Close()

		plugin := newBiorxivDualServerTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query: "test",
			Limit: 50,
			Filters: SearchFilters{
				DateFrom: testBiorxivDateFrom,
			},
		}, nil)

		require.NoError(t, err)
		// Both servers return 1 article each → 2 results.
		assert.Len(t, result.Results, 2)
	})

	t.Run("empty_results", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(biorxivResponse{
				Collection: []biorxivArticle{},
				Messages:   []biorxivMsg{{Total: "0"}},
			})
		}))
		defer ts.Close()

		plugin := newBiorxivTestPlugin(t, ts.URL)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:   "test",
			Limit:   10,
			Filters: SearchFilters{DateFrom: testBiorxivDateFrom},
		}, nil)
		require.NoError(t, err)
		assert.Empty(t, result.Results)
	})
}

// ---------------------------------------------------------------------------
// Get tests
// ---------------------------------------------------------------------------

func TestBioRxivGet(t *testing.T) {
	t.Parallel()

	t.Run("found_on_first_server", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, testBiorxivDOI1)
			_ = json.NewEncoder(w).Encode(buildBiorxivTestResponse())
		}))
		defer ts.Close()

		plugin := newBiorxivTestPlugin(t, ts.URL)
		pub, err := plugin.Get(context.Background(), testBiorxivDOI1, nil, FormatNative, nil)

		require.NoError(t, err)
		assert.Equal(t, testBiorxivTitle1, pub.Title)
		assert.Equal(t, testBiorxivDOI1, pub.DOI)
		assert.Len(t, pub.Authors, testBiorxivExpectedAuthorCount)
	})

	t.Run("found_on_second_server", func(t *testing.T) {
		t.Parallel()
		callCount := 0
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount++
			if callCount == 1 {
				// First server returns empty.
				_ = json.NewEncoder(w).Encode(biorxivResponse{Collection: []biorxivArticle{}})
				return
			}
			// Second server returns the article.
			_ = json.NewEncoder(w).Encode(buildBiorxivTestResponse())
		}))
		defer ts.Close()

		plugin := newBiorxivDualServerTestPlugin(t, ts.URL)
		pub, err := plugin.Get(context.Background(), testBiorxivDOI1, nil, FormatNative, nil)

		require.NoError(t, err)
		assert.Equal(t, testBiorxivTitle1, pub.Title)
	})

	t.Run("not_found_on_any_server", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(biorxivResponse{Collection: []biorxivArticle{}})
		}))
		defer ts.Close()

		plugin := newBiorxivTestPlugin(t, ts.URL)
		_, err := plugin.Get(context.Background(), "10.1101/nonexistent", nil, FormatNative, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), ErrMsgBiorxivNotFound)
	})
}

// ---------------------------------------------------------------------------
// Author parsing tests
// ---------------------------------------------------------------------------

func TestBioRxivAuthorParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantCount int
		wantFirst string
	}{
		{"multiple_authors", testBiorxivAuthors1, testBiorxivExpectedAuthorCount, "Smith, John"},
		{"single_author", "Solo Author", 1, "Solo Author"},
		{"empty_string", "", 0, ""},
		{"trailing_semicolons", "A; B; ; ", 2, "A"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			authors := parseBiorxivAuthors(tc.input)
			assert.Len(t, authors, tc.wantCount)
			if tc.wantCount > 0 {
				assert.Equal(t, tc.wantFirst, authors[0].Name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HTTP error tests
// ---------------------------------------------------------------------------

func TestBioRxivHTTPErrors(t *testing.T) {
	t.Parallel()

	t.Run("server_500", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer ts.Close()

		plugin := newBiorxivTestPlugin(t, ts.URL)
		// Search: server failure means no results (partial failure).
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:   "test",
			Limit:   10,
			Filters: SearchFilters{DateFrom: testBiorxivDateFrom},
		}, nil)
		require.NoError(t, err)
		assert.Empty(t, result.Results)
	})

	t.Run("get_server_500", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer ts.Close()

		plugin := newBiorxivTestPlugin(t, ts.URL)
		_, err := plugin.Get(context.Background(), testBiorxivDOI1, nil, FormatNative, nil)
		assert.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestBioRxivConcurrentSafety(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(buildBiorxivTestResponse())
	}))
	defer ts.Close()

	plugin := newBiorxivTestPlugin(t, ts.URL)

	const goroutines = 10
	done := make(chan struct{}, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			_, _ = plugin.Search(context.Background(), SearchParams{
				Query:   "test",
				Limit:   1,
				Filters: SearchFilters{DateFrom: testBiorxivDateFrom},
			}, nil)
			_, _ = plugin.Get(context.Background(), testBiorxivDOI1, nil, FormatNative, nil)
			_ = plugin.Health(context.Background())
		}(i)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
}
