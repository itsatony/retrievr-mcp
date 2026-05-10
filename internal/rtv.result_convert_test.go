package internal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Cycle 2 task #10 — Publication -> Result conversion tests.
// ---------------------------------------------------------------------------

func TestToResult_DefaultsToKindPaper(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv: &ArXivPlugin{},
	}
	r := testRouter(plugins)

	doi := testDOI1
	cit := 42
	pub := Publication{
		ID:            "arxiv:2401.12345",
		Source:        SourceArXiv,
		Title:         "Efficient Attention",
		Authors:       []Author{{Name: "J. Smith"}},
		Published:     "2024-01-15",
		Abstract:      "We propose a sparse-attention variant that reduces FLOPs by 40%.",
		URL:           "https://arxiv.org/abs/2401.12345",
		PDFURL:        "https://arxiv.org/pdf/2401.12345",
		DOI:           doi,
		ArXivID:       "2401.12345",
		CitationCount: &cit,
	}

	got := r.toResult(pub, 0)

	assert.Equal(t, KindPaper, got.Kind)
	assert.Equal(t, "arxiv:2401.12345", got.ID)
	assert.Equal(t, "Efficient Attention", got.Title)
	assert.Equal(t, "arxiv.org", got.Domain, "Domain must be derived from URL when SourceMetadata is empty")
	assert.Empty(t, got.Snippet, "Paper kind keeps Abstract; Snippet stays empty unless explicitly set")
	assert.Equal(t, 1.0, got.Score, "rank=0 yields score 1.0")
	require.NotNil(t, got.Paper)
	assert.Equal(t, doi, got.Paper.DOI)
	assert.Equal(t, "2401.12345", got.Paper.ArXivID)
	require.NotNil(t, got.Paper.CitationCount)
	assert.Equal(t, 42, *got.Paper.CitationCount)
}

func TestToResult_WebKindUnpacksSourceMetadata(t *testing.T) {
	t.Parallel()

	// Use a mockPlugin with Capabilities that declares KindWeb so the
	// converter picks the right kind without needing SourceMetadata.kind.
	mp := newMockPlugin("webA", nil)
	mp.capabilities = SourceCapabilities{Kinds: []ResultKind{KindWeb}}
	plugins := map[string]SourcePlugin{"webA": mp}
	r := testRouter(plugins)

	pub := Publication{
		ID:        "webA:abc",
		Source:    "webA",
		Title:     "Sparse Transformers Practitioner's Guide",
		URL:       "https://example.eu/blog/sparse",
		Abstract:  "After running sparse-attention models in production for a year...",
		Published: "2024-09-12",
		SourceMetadata: map[string]any{
			smetaSnippet:     "After a year in production, here's what we learned",
			smetaSiteName:    "Example Engineering",
			smetaPublishedAt: "2024-09-12T10:00:00Z",
			smetaReadingMins: 8,
			smetaLanguage:    "en",
		},
	}

	got := r.toResult(pub, 1)

	assert.Equal(t, KindWeb, got.Kind)
	assert.Equal(t, "After a year in production, here's what we learned", got.Snippet)
	assert.Equal(t, "en", got.Language)
	assert.Equal(t, "example.eu", got.Domain)
	assert.InDelta(t, 0.5, got.Score, 1e-9, "rank=1 yields 1/(1+1)=0.5")
	require.NotNil(t, got.Web)
	assert.Equal(t, "Example Engineering", got.Web.SiteName)
	assert.Equal(t, "2024-09-12T10:00:00Z", got.Web.PublishedAt)
	assert.Equal(t, 8, got.Web.ReadingMins)
	assert.Nil(t, got.Paper, "Paper block must NOT be populated for kind=web")
}

func TestToResult_CodeKindUnpacksRepoSlugs(t *testing.T) {
	t.Parallel()
	mp := newMockPlugin("gh", nil)
	mp.capabilities = SourceCapabilities{Kinds: []ResultKind{KindCode}}
	plugins := map[string]SourcePlugin{"gh": mp}
	r := testRouter(plugins)

	pub := Publication{
		ID:     "gh:openai/sparse-attention#main",
		Source: "gh",
		Title:  "openai/sparse-attention",
		URL:    "https://github.com/openai/sparse-attention",
		SourceMetadata: map[string]any{
			smetaRepo:     "openai/sparse-attention",
			smetaPath:     "sparse.py",
			smetaSHA:      "9f3c12a",
			smetaCodeLang: "python",
			smetaStars:    1240,
		},
	}

	got := r.toResult(pub, 0)

	assert.Equal(t, KindCode, got.Kind)
	require.NotNil(t, got.Stars)
	assert.Equal(t, 1240, *got.Stars)
	require.NotNil(t, got.Code)
	assert.Equal(t, "openai/sparse-attention", got.Code.Repo)
	assert.Equal(t, "sparse.py", got.Code.Path)
	assert.Equal(t, "python", got.Code.Language)
	assert.Equal(t, "9f3c12a", got.Code.SHA)
}

func TestToResult_KindOverrideViaSourceMetadata(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{
		SourceArXiv: &ArXivPlugin{}, // declares no Kinds → defaults to paper
	}
	r := testRouter(plugins)

	pub := Publication{
		ID:     "arxiv:2401.x",
		Source: SourceArXiv,
		Title:  "Generic web result smuggled via arxiv source",
		URL:    "https://example.com/page",
		SourceMetadata: map[string]any{
			smetaKindOverride: string(KindWeb),
			smetaSnippet:      "explicit kind override beats source defaults",
		},
	}
	got := r.toResult(pub, 0)
	assert.Equal(t, KindWeb, got.Kind, "smetaKindOverride must beat plugin defaults")
	require.NotNil(t, got.Web)
	assert.Nil(t, got.Paper)
}

func TestToResult_ScoreDecaysWithRank(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{SourceArXiv: &ArXivPlugin{}}
	r := testRouter(plugins)

	cases := []struct {
		rank int
		want float64
	}{
		{0, 1.0},
		{1, 0.5},
		{2, 1.0 / 3},
		{9, 0.1},
	}
	for _, c := range cases {
		got := r.toResult(Publication{ID: "x", Source: SourceArXiv}, c.rank)
		assert.InDelta(t, c.want, got.Score, 1e-9, "rank=%d", c.rank)
		require.NotNil(t, got.ScoreParts)
		assert.InDelta(t, c.want, got.ScoreParts.Lexical, 1e-9)
	}
}

func TestRouter_SearchV2WrapsSearch(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, []Publication{
			testPub(SourceArXiv, "arxiv:1", testDOI1, intPtr(10)),
			testPub(SourceArXiv, "arxiv:2", testDOI2, intPtr(20)),
		}),
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{SourceArXiv}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())

	v2, err := r.SearchV2(context.Background(), SearchParams{Query: "x", Limit: 5}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, v2.TotalResults)
	assert.Len(t, v2.Results, 2)
	for _, res := range v2.Results {
		assert.Equal(t, KindPaper, res.Kind, "all results should be Paper kind for cycle-1 source")
		assert.Equal(t, SourceArXiv, res.Source)
		assert.NotZero(t, res.Score)
	}
}

func TestTruncateSnippet(t *testing.T) {
	t.Parallel()
	short := "this is short"
	assert.Equal(t, short, truncateSnippet(short))

	// Longer than snippetMaxRunes — must be truncated with ellipsis.
	long := ""
	for i := 0; i < 50; i++ {
		long += "this is a long sentence "
	}
	got := truncateSnippet(long)
	assert.Less(t, len([]rune(got)), len([]rune(long)))
	assert.Contains(t, got, "…")
}

func TestIsValidResultKind(t *testing.T) {
	t.Parallel()
	for _, k := range []ResultKind{KindPaper, KindModel, KindDataset, KindWeb, KindNews, KindCode, KindEncyclopedia} {
		assert.True(t, IsValidResultKind(string(k)))
	}
	assert.False(t, IsValidResultKind("garbage"))
	assert.False(t, IsValidResultKind(""))
}
