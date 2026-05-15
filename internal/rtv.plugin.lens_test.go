package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v6 cycle 3 / v2.16.0 — Lens.org tests.
// ---------------------------------------------------------------------------

func newLensTestPlugin(t *testing.T, baseURL, apiKey string) *LensPlugin {
	t.Helper()
	p := &LensPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestLens_Identity(t *testing.T) {
	t.Parallel()
	p := &LensPlugin{}
	assert.Equal(t, SourceLens, p.ID())
}

func TestLens_Capabilities(t *testing.T) {
	t.Parallel()
	p := &LensPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPaper)
	assert.True(t, caps.RequiresCredential)
	assert.True(t, caps.SupportsCitations)
}

func TestLens_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newLensTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestLens_Search_HappyPath(t *testing.T) {
	t.Parallel()
	resp := lensSearchResponse{
		Total: 1,
		Data: []lensWork{{
			LensID:        "100-000-000-001-DOI",
			Title:         "Attention is All You Need",
			Abstract:      "<p>Transformer architecture...</p>",
			YearPublished: 2017,
			DatePublished: "2017-06-12",
			ExternalIDs: []lensExtID{
				{Type: "doi", Value: "10.5555/3295222.3295349"},
				{Type: "arxiv", Value: "1706.03762"},
			},
			Authors: []lensAuthor{
				{DisplayName: "Ashish Vaswani", ORCID: "0000-0001-2345-6789"},
				{FirstName: "Noam", LastName: "Shazeer"},
			},
			Source:                  &lensSource{Title: "NeurIPS"},
			PublicationType:         "conference paper",
			ScholarlyCitationsCount: 75000,
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, lensSearchPath, r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get(lensHeaderAuth))

		var body lensSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, 25, body.Size)
		assert.NotEmpty(t, body.Include)

		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newLensTestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "attention", Limit: 25})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "lens:100-000-000-001-DOI", pub.ID)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, "10.5555/3295222.3295349", pub.DOI)
	assert.Equal(t, "1706.03762", pub.ArXivID)
	assert.Equal(t, "2017-06-12", pub.Published)
	require.NotNil(t, pub.CitationCount)
	assert.Equal(t, 75000, *pub.CitationCount)
	require.Len(t, pub.Authors, 2)
	assert.Equal(t, "Ashish Vaswani", pub.Authors[0].Name)
	assert.Equal(t, "Shazeer, Noam", pub.Authors[1].Name)
	assert.Equal(t, "NeurIPS", pub.SourceMetadata[lensMetaKeySource])
	assert.Equal(t, "conference paper", pub.SourceMetadata[lensMetaKeyPublicationType])
}

func TestLens_Search_DateAndCategoryFilters(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body lensSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		// The body must contain a bool.must clause with range + term entries.
		queryJSON, _ := json.Marshal(body.Query)
		s := string(queryJSON)
		assert.Contains(t, s, `"range"`)
		assert.Contains(t, s, `"year_published"`)
		assert.Contains(t, s, `"term"`)
		assert.Contains(t, s, "Computer Science")

		require.Len(t, body.Sort, 1)
		assert.Equal(t, "desc", body.Sort[0]["scholarly_citations_count"])

		_, _ = io.WriteString(w, `{"total":0,"data":[]}`)
	}))
	defer srv.Close()

	p := newLensTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{
		Query: "x",
		Sort:  SortCitations,
		Filters: SearchFilters{
			DateFrom:   "2020",
			DateTo:     "2024",
			Categories: []string{"Computer Science"},
		},
	})
	require.NoError(t, err)
}

func TestLens_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newLensTestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestLens_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newLensTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestLens_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &LensPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}
