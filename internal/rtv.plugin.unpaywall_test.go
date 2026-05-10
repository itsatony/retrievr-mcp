package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newUnpaywallTestPlugin(t *testing.T, baseURL, email string) *UnpaywallPlugin {
	t.Helper()
	p := &UnpaywallPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		RateLimit: 5,
		Extra:     map[string]string{unpaywallExtraEmail: email},
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestUnpaywall_IdentityAndCapabilities(t *testing.T) {
	t.Parallel()
	p := &UnpaywallPlugin{}
	assert.Equal(t, "unpaywall", p.ID())
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPaper)
	assert.Contains(t, caps.QueryIntents, IntentPrimarySource)
	assert.True(t, caps.SupportsOpenAccessFilter)
}

func TestUnpaywall_Residency_PublicResearch(t *testing.T) {
	t.Parallel()
	tag := (&UnpaywallPlugin{}).Residency()
	assert.Equal(t, RegionPublicResearch, tag.Region)
	assert.True(t, tag.Region.IsPublicResearch())
}

func TestUnpaywall_Search_AlwaysEmpty(t *testing.T) {
	t.Parallel()
	p := newUnpaywallTestPlugin(t, "http://unused", "test@example.com")
	got, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.NoError(t, err)
	assert.Empty(t, got.Results, "Unpaywall has no keyword search; Search must return empty")
}

func TestUnpaywall_Get_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test@example.com", r.URL.Query().Get(unpaywallEmailParam))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(unpaywallResponse{
			DOI:   "10.1234/abc",
			Title: "Test Paper",
			IsOA:  true,
			BestOALocation: &unpaywallOALocation{
				URL:       "https://example.com/page",
				URLForPDF: "https://example.com/paper.pdf",
				License:   "cc-by",
			},
			JournalName: "Test Journal",
		})
	}))
	defer srv.Close()

	p := newUnpaywallTestPlugin(t, srv.URL, "test@example.com")
	pub, err := p.Get(context.Background(), "10.1234/abc", nil, FormatNative)
	require.NoError(t, err)
	assert.Equal(t, "10.1234/abc", pub.DOI)
	assert.Equal(t, "https://example.com/paper.pdf", pub.PDFURL)
	assert.Equal(t, "cc-by", pub.License)
	assert.Equal(t, true, pub.SourceMetadata["open_access"])
	assert.Equal(t, "Test Journal", pub.SourceMetadata["venue"])
}

func TestUnpaywall_Get_RequiresEmail(t *testing.T) {
	t.Parallel()
	p := newUnpaywallTestPlugin(t, "http://unused", "")
	_, err := p.Get(context.Background(), "10.1234/abc", nil, FormatNative)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestUnpaywall_Get_NotFoundMaps(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	p := newUnpaywallTestPlugin(t, srv.URL, "test@example.com")
	_, err := p.Get(context.Background(), "10.1234/missing", nil, FormatNative)
	assert.True(t, errors.Is(err, ErrSourceNotFound))
}

func TestUnpaywall_Enrichment_FillsMissingPDF(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(unpaywallResponse{
			DOI:            "10.1234/abc",
			IsOA:           true,
			BestOALocation: &unpaywallOALocation{URLForPDF: "https://oa.example/paper.pdf", License: "cc-by"},
		})
	}))
	defer srv.Close()
	up := newUnpaywallTestPlugin(t, srv.URL, "test@example.com")

	pub := Publication{Source: SourceArXiv, DOI: "10.1234/abc", Title: "T"}
	changed, err := up.EnrichPublication(context.Background(), &pub)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "https://oa.example/paper.pdf", pub.PDFURL)
	assert.Equal(t, "cc-by", pub.License)
	assert.Equal(t, true, pub.SourceMetadata["open_access"])
}

func TestUnpaywall_Enrichment_SkipsWhenAlreadyHasPDF(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("Unpaywall must not be called when PDFURL is already set")
	}))
	defer srv.Close()
	up := newUnpaywallTestPlugin(t, srv.URL, "test@example.com")

	pub := Publication{DOI: "10.1234/abc", PDFURL: "https://existing.example/paper.pdf"}
	changed, err := up.EnrichPublication(context.Background(), &pub)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestRouter_EnrichWithUnpaywall_Integration(t *testing.T) {
	t.Parallel()
	// Mock Unpaywall endpoint.
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(unpaywallResponse{
			DOI:            "10.5555/x",
			IsOA:           true,
			BestOALocation: &unpaywallOALocation{URLForPDF: "https://oa/x.pdf"},
		})
	}))
	defer upSrv.Close()
	up := newUnpaywallTestPlugin(t, upSrv.URL, "test@example.com")

	// Stub plugin returns one paper with a DOI but no PDFURL.
	citations := 5
	paper := Publication{
		ID:            "arxiv:1",
		Source:        SourceArXiv,
		Title:         "Test",
		DOI:           "10.5555/x",
		Authors:       []Author{{Name: "A"}},
		CitationCount: &citations,
	}
	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, []Publication{paper}),
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{SourceArXiv}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger(),
		WithUnpaywallEnrichment(up),
	)

	merged, err := r.Search(context.Background(), SearchParams{Query: "x", Limit: 5}, nil, nil)
	require.NoError(t, err)
	require.Len(t, merged.Results, 1)
	assert.Equal(t, "https://oa/x.pdf", merged.Results[0].PDFURL,
		"Unpaywall enrichment must fill PDFURL post-merge when DOI is present")
}
