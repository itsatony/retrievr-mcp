package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Cycle 2 Wave-1: Linkup (EU-resident) provider tests.
// ---------------------------------------------------------------------------

const (
	linkupTestServerKey  = "linkup-server-key"
	linkupTestPerCallKey = "linkup-per-call-key"
)

func newLinkupTestPlugin(t *testing.T, baseURL, apiKey string) *LinkupPlugin {
	t.Helper()
	p := &LinkupPlugin{}
	cfg := PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		RateLimit: 5,
	}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func buildLinkupTestResponse(results []linkupResult) string {
	body := linkupSearchResponse{Results: results}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestLinkup_Identity(t *testing.T) {
	t.Parallel()
	p := &LinkupPlugin{}
	assert.Equal(t, "linkup", p.ID())
	assert.Equal(t, "Linkup", p.Name())
	assert.NotEmpty(t, p.Description())
}

func TestLinkup_Capabilities(t *testing.T) {
	t.Parallel()
	p := &LinkupPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindWeb)
	assert.NotContains(t, caps.Kinds, KindNews, "Linkup is web-only in cycle 2")
	assert.Contains(t, caps.QueryIntents, IntentDeepResearch)
	assert.Contains(t, caps.QueryIntents, IntentQuickLookup)
	assert.True(t, caps.SupportsFullText)
}

// THIS is the headline assertion for cycle 2 — Linkup is the only Wave-1
// provider admitted under eu_strict.
func TestLinkup_Residency_IsEUWithSignedDPA(t *testing.T) {
	t.Parallel()
	p := &LinkupPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region, "Linkup MUST be EU-resident")
	assert.Equal(t, DPASigned, tag.DPAStatus, "Linkup MUST declare a signed DPA")
	assert.True(t, tag.Region.IsEU(), "Linkup must pass eu_strict gate")
	assert.NotEmpty(t, tag.SubprocessorURL, "DPA URL must be surfaced")
}

func TestLinkup_Search_HappyPath(t *testing.T) {
	t.Parallel()

	results := []linkupResult{
		{
			Type:    "text",
			Name:    "Sparse Transformers Practitioner's Guide",
			URL:     "https://example.eu/blog/sparse",
			Content: "After running sparse-attention models in production for a year, here's what we learned about both throughput and accuracy tradeoffs.",
		},
		{
			Type:    "image", // must be filtered out
			Name:    "diagram.png",
			URL:     "https://example.eu/img.png",
			Content: "image bytes",
		},
		{
			Type:    "text",
			Name:    "Attention Visualizations",
			URL:     "https://huggingface.co/blog/attention",
			Content: "Multi-head attention visualizations explained.",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, linkupSearchPath, r.URL.Path)
		assert.Equal(t, linkupAuthScheme+linkupTestServerKey, r.Header.Get(linkupAuthHeader),
			"Bearer prefix must be applied to the API key")

		var body linkupSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "transformer attention", body.Q)
		assert.Equal(t, linkupDefaultDepth, body.Depth, "default depth=standard must propagate")
		assert.Equal(t, linkupDefaultOutputType, body.OutputType)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildLinkupTestResponse(results))
	}))
	defer srv.Close()

	p := newLinkupTestPlugin(t, srv.URL, linkupTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "transformer attention", Limit: 5})
	require.NoError(t, err)
	require.Len(t, got.Results, 2, "image-typed result must be filtered out; web-only kept")
	assert.Equal(t, "Sparse Transformers Practitioner's Guide", got.Results[0].Title)
	assert.Contains(t, got.Results[0].SourceMetadata, smetaSnippet)
	assert.True(t, strings.HasPrefix(got.Results[0].ID, "linkup:"))
}

func TestLinkup_Search_PerCallCredentialOverridesServerKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, linkupAuthScheme+linkupTestPerCallKey, r.Header.Get(linkupAuthHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, buildLinkupTestResponse(nil))
	}))
	defer srv.Close()

	p := newLinkupTestPlugin(t, srv.URL, linkupTestServerKey)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceLinkup: linkupTestPerCallKey,
	})
	_, err := p.Search(ctx, SearchParams{Query: "x", Limit: 1})
	require.NoError(t, err)
}

func TestLinkup_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newLinkupTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestLinkup_Search_401ReturnsErrCredentialInvalid(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newLinkupTestPlugin(t, srv.URL, linkupTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
	assert.False(t, p.Health(context.Background()).Healthy)
}

func TestLinkup_Search_429ReturnsErrRateLimitExceeded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newLinkupTestPlugin(t, srv.URL, linkupTestServerKey)
	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestLinkup_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &LinkupPlugin{}
	_, err := p.Get(context.Background(), "abc", nil, FormatNative)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

// ---------------------------------------------------------------------------
// EU-mode end-to-end integration: gate must admit Linkup and refuse Exa
// when called with eu_strict + explicit Sources.
// ---------------------------------------------------------------------------

func TestEUMode_StrictAdmitsLinkupRefusesExa(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{
		SourceLinkup: &LinkupPlugin{},
		SourceExa:    &ExaPlugin{},
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{SourceLinkup}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger(),
		WithEUMode(EUModeStrict, false),
	)

	t.Run("linkup_admitted", func(t *testing.T) {
		// Use the gate function directly to validate admission without
		// hitting the Linkup network (the Linkup plugin would error on
		// missing credentials otherwise).
		gate := applyEUGate([]string{SourceLinkup}, plugins, EUModeStrict, false)
		assert.Equal(t, []string{SourceLinkup}, gate.Admitted)
		assert.Empty(t, gate.Skipped)
	})

	t.Run("exa_refused", func(t *testing.T) {
		_, err := r.Search(context.Background(), SearchParams{Query: "x", Limit: 1}, []string{SourceExa}, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEUModeProviderConflict),
			"explicit eu_strict + Sources=[exa] must surface ErrEUModeProviderConflict")
	})

	t.Run("mixed_request_blocks_only_exa", func(t *testing.T) {
		_, err := r.Search(context.Background(), SearchParams{Query: "x", Limit: 1}, []string{SourceLinkup, SourceExa}, nil)
		require.Error(t, err, "mixed eu/non-eu Sources under eu_strict must refuse the entire call")
		var typed *EUModeProviderConflictError
		require.True(t, errors.As(err, &typed))
		assert.Equal(t, []string{SourceExa}, typed.Blocked, "only the non-EU source must appear in Blocked")
	})
}

// ---------------------------------------------------------------------------
// Live test (gated on LINKUPSO_APIKEY env var).
// ---------------------------------------------------------------------------

func TestLinkup_LiveSmoke(t *testing.T) {
	apiKey := os.Getenv("LINKUPSO_APIKEY")
	if apiKey == "" {
		t.Skip("LINKUPSO_APIKEY env var not set; skipping live Linkup smoke test")
	}
	p := newLinkupTestPlugin(t, "", apiKey)

	got, err := p.Search(context.Background(), SearchParams{Query: "transformer attention mechanism", Limit: 3})
	require.NoError(t, err)
	require.NotEmpty(t, got.Results, "live Linkup search must return at least one result")
	for _, r := range got.Results {
		t.Logf("hit: id=%s title=%q url=%s", r.ID, r.Title, r.URL)
		assert.True(t, strings.HasPrefix(r.ID, "linkup:"))
		assert.NotEmpty(t, r.Title)
	}
}
