package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Cycle 1 task #4 — intent + fallback chain tests.
//
// The default fallback config maps `deep_research` and `primary_source` to
// the "academic" chain (primary: s2, openalex; fallback: arxiv, crossref,
// europmc, pubmed). These tests use a hand-built Router with custom chains
// to keep the assertions tight.
// ---------------------------------------------------------------------------

const (
	fbIntentTest    = Intent("test_intent")
	fbChainName     = "test_chain"
	fbSourcePrimary = "arxiv" // re-use existing source IDs so plugin
	fbSourceAlt     = "s2"    // factories accept the mock plugins.
	fbSourceFB1     = "crossref"
	fbSourceFB2     = "europmc"
)

func fbTestRouter(t *testing.T, plugins map[string]SourcePlugin, chain FallbackChain) *Router {
	t.Helper()
	cfg := testRouterConfig()
	cfg.Fallback = RouterFallbackConfig{
		Chains:        map[string]FallbackChain{fbChainName: chain},
		IntentToChain: map[string]string{string(fbIntentTest): fbChainName},
	}
	// Disable retry to keep timing tight in tests.
	cfg.Retry = RouterRetryConfig{MaxAttempts: 1}
	return NewRouter(
		cfg,
		plugins,
		nil,
		nil,
		testRateLimits(plugins),
		&CredentialResolver{},
		nil,
		discardLogger(),
	)
}

func TestSearch_IntentResolvesToFallbackChainPrimary(t *testing.T) {
	t.Parallel()

	pubs := []Publication{testPub(fbSourcePrimary, "arxiv:1", testDOI1, intPtr(5))}
	plugins := map[string]SourcePlugin{
		fbSourcePrimary: newMockPlugin(fbSourcePrimary, pubs),
		fbSourceAlt:     newMockPlugin(fbSourceAlt, nil), // not in chain
	}
	r := fbTestRouter(t, plugins, FallbackChain{
		Primary:  []string{fbSourcePrimary},
		Fallback: nil,
	})

	result, err := r.Search(context.Background(), SearchParams{
		Query:  "x",
		Limit:  10,
		Intent: fbIntentTest,
	}, nil, nil)

	require.NoError(t, err)
	assert.Equal(t, []string{fbSourcePrimary}, result.SourcesQueried)
	assert.Len(t, result.Results, 1)
}

func TestSearch_FallbackWalkedWhenPrimaryReturnsZero(t *testing.T) {
	t.Parallel()

	primary := newMockPlugin(fbSourcePrimary, nil) // returns empty
	primary.searchFunc = func(_ context.Context, _ SearchParams) (*SearchResult, error) {
		return &SearchResult{Total: 0, Results: nil}, nil
	}
	fallback1 := newMockPlugin(fbSourceFB1, []Publication{testPub(fbSourceFB1, "cr:1", testDOI1, nil)})

	plugins := map[string]SourcePlugin{
		fbSourcePrimary: primary,
		fbSourceFB1:     fallback1,
		fbSourceFB2:     newMockPlugin(fbSourceFB2, nil),
	}
	r := fbTestRouter(t, plugins, FallbackChain{
		Primary:  []string{fbSourcePrimary},
		Fallback: []string{fbSourceFB1, fbSourceFB2},
	})

	result, err := r.Search(context.Background(), SearchParams{
		Query:  "x",
		Limit:  10,
		Intent: fbIntentTest,
	}, nil, nil)

	require.NoError(t, err)
	assert.Contains(t, result.SourcesQueried, fbSourcePrimary)
	assert.Contains(t, result.SourcesQueried, fbSourceFB1, "first fallback must be queried")
	assert.NotContains(t, result.SourcesQueried, fbSourceFB2, "fallback walk must short-circuit on first hit")
	assert.Len(t, result.Results, 1)
}

func TestSearch_FallbackWalkedWhenPrimaryAllFail(t *testing.T) {
	t.Parallel()

	primary := newMockPlugin(fbSourcePrimary, nil)
	primary.searchFunc = func(_ context.Context, _ SearchParams) (*SearchResult, error) {
		return nil, errors.New("upstream 503")
	}
	fallback1 := newMockPlugin(fbSourceFB1, []Publication{testPub(fbSourceFB1, "cr:1", testDOI1, nil)})

	plugins := map[string]SourcePlugin{
		fbSourcePrimary: primary,
		fbSourceFB1:     fallback1,
	}
	r := fbTestRouter(t, plugins, FallbackChain{
		Primary:  []string{fbSourcePrimary},
		Fallback: []string{fbSourceFB1},
	})

	result, err := r.Search(context.Background(), SearchParams{
		Query:  "x",
		Limit:  10,
		Intent: fbIntentTest,
	}, nil, nil)

	require.NoError(t, err, "primary all-fail with successful fallback must not surface as error")
	assert.Contains(t, result.SourcesFailed, fbSourcePrimary)
	assert.Contains(t, result.SourcesQueried, fbSourceFB1)
	assert.Len(t, result.Results, 1)
}

func TestSearch_NoFallbackWalkWhenSourcesExplicit(t *testing.T) {
	t.Parallel()

	primary := newMockPlugin(fbSourcePrimary, nil)
	primary.searchFunc = func(_ context.Context, _ SearchParams) (*SearchResult, error) {
		return &SearchResult{Total: 0, Results: nil}, nil
	}
	fallback1 := newMockPlugin(fbSourceFB1, []Publication{testPub(fbSourceFB1, "cr:1", testDOI1, nil)})

	plugins := map[string]SourcePlugin{
		fbSourcePrimary: primary,
		fbSourceFB1:     fallback1,
	}
	r := fbTestRouter(t, plugins, FallbackChain{
		Primary:  []string{fbSourcePrimary},
		Fallback: []string{fbSourceFB1},
	})

	// Explicit Sources arg overrides intent-based resolution; no fallback walk.
	result, err := r.Search(context.Background(), SearchParams{
		Query:  "x",
		Limit:  10,
		Intent: fbIntentTest, // ignored because Sources is non-empty
	}, []string{fbSourcePrimary}, nil)

	require.NoError(t, err)
	assert.Equal(t, []string{fbSourcePrimary}, result.SourcesQueried)
	assert.NotContains(t, result.SourcesQueried, fbSourceFB1)
	assert.Empty(t, result.Results)
}

func TestSearch_NoFallbackWalkWhenIntentEmpty(t *testing.T) {
	t.Parallel()

	primary := newMockPlugin(fbSourcePrimary, nil)
	primary.searchFunc = func(_ context.Context, _ SearchParams) (*SearchResult, error) {
		return &SearchResult{Total: 0, Results: nil}, nil
	}
	plugins := map[string]SourcePlugin{
		fbSourcePrimary: primary,
		fbSourceFB1:     newMockPlugin(fbSourceFB1, []Publication{testPub(fbSourceFB1, "cr:1", testDOI1, nil)}),
	}
	r := fbTestRouter(t, plugins, FallbackChain{
		Primary:  []string{fbSourcePrimary},
		Fallback: []string{fbSourceFB1},
	})

	// No intent → no chain lookup → no fallback walk.
	result, err := r.Search(context.Background(), SearchParams{
		Query: "x",
		Limit: 10,
	}, nil, nil)

	require.NoError(t, err)
	// Default sources from testRouterConfig include arxiv + s2; only arxiv has a plugin
	// returning empty, s2 returns nil from its plugin… but importantly fallback chain
	// is not consulted because Intent is empty.
	assert.NotContains(t, result.SourcesQueried, fbSourceFB1, "fallback must not run without an Intent")
}

func TestResolveByIntent_UnknownIntentReturnsDefaults(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		fbSourcePrimary: newMockPlugin(fbSourcePrimary, nil),
	}
	r := fbTestRouter(t, plugins, FallbackChain{
		Primary:  []string{fbSourcePrimary},
		Fallback: nil,
	})

	primary, fallback := r.resolveByIntent("nonsense_intent")
	assert.Nil(t, fallback)
	// testRouterConfig sets defaultSources = [arxiv, s2]; only arxiv is registered here.
	assert.Equal(t, []string{fbSourcePrimary}, primary)
}

func TestDefaultFallbackConfig_HasAcademicChain(t *testing.T) {
	t.Parallel()

	cfg := DefaultFallbackConfig()
	chain, ok := cfg.Chains[fallbackChainAcademic]
	require.True(t, ok, "default config must include the academic chain")
	assert.NotEmpty(t, chain.Primary)
	assert.NotEmpty(t, chain.Fallback)

	got := cfg.IntentToChain[string(IntentDeepResearch)]
	assert.Equal(t, fallbackChainAcademic, got)

	// IntentPrimarySource has its own OA-biased chain since v2.20.0
	// (cycle-1 had it aliased to academic). Both chains share most
	// scholarly providers but the primary order differs.
	got = cfg.IntentToChain[string(IntentPrimarySource)]
	assert.Equal(t, fallbackChainPrimarySource, got)
	_, ok = cfg.Chains[fallbackChainPrimarySource]
	assert.True(t, ok, "default config must include the primary_source chain")
}

func TestDefaultFallbackConfig_AllIntentsWired(t *testing.T) {
	t.Parallel()

	cfg := DefaultFallbackConfig()
	wantIntents := []Intent{
		IntentDeepResearch,
		IntentPrimarySource,
		IntentQuickLookup,
		IntentCodeProvenance,
		IntentNews,
		IntentReference,
	}
	for _, i := range wantIntents {
		chainName, ok := cfg.IntentToChain[string(i)]
		require.True(t, ok, "intent %q must be wired to a chain", i)
		chain, ok := cfg.Chains[chainName]
		require.True(t, ok, "chain %q for intent %q must exist", chainName, i)
		assert.NotEmpty(t, chain.Primary, "chain %q must have a non-empty primary set", chainName)
	}
}

func TestResolveFallbackConfig_ZeroValueGetsDefaults(t *testing.T) {
	t.Parallel()

	got := resolveFallbackConfig(RouterFallbackConfig{})
	assert.NotEmpty(t, got.Chains)
	assert.NotEmpty(t, got.IntentToChain)
}

func TestResolveFallbackConfig_NonZeroPassThrough(t *testing.T) {
	t.Parallel()

	custom := RouterFallbackConfig{
		Chains: map[string]FallbackChain{"x": {Primary: []string{"a"}}},
	}
	got := resolveFallbackConfig(custom)
	assert.Len(t, got.Chains, 1, "custom config must not be merged with defaults")
	_, ok := got.Chains["x"]
	assert.True(t, ok)
}
