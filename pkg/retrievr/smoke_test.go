package retrievr_test

import (
	"context"
	"testing"

	"github.com/itsatony/retrievr-mcp/v2/pkg/retrievr"
	"github.com/itsatony/retrievr-mcp/v2/pkg/retrievr/plugin"
)

// TestPublicSurfaceCompiles is a deliberately tiny test whose only purpose is
// to exercise every exported identifier the cycle-1 skeleton commits to. If a
// later commit accidentally renames or removes one of these, this test fails
// and the breakage surfaces immediately rather than rippling into liz/nexus.
func TestPublicSurfaceCompiles(t *testing.T) {
	t.Parallel()

	// Type aliases — instantiable.
	_ = retrievr.Publication{}
	_ = retrievr.Author{}
	_ = retrievr.Reference{}
	_ = retrievr.SearchParams{}
	_ = retrievr.SearchFilters{}
	_ = retrievr.SearchResult{}
	_ = retrievr.MergedSearchResult{}
	_ = retrievr.SourceCapabilities{}
	_ = retrievr.SourceHealth{}
	_ = retrievr.SourceInfo{}

	// New cycle-1 types.
	_ = retrievr.Result{Kind: retrievr.KindPaper}
	_ = retrievr.PaperData{}
	_ = retrievr.WebData{}
	_ = retrievr.CodeData{}
	_ = retrievr.NewsData{}
	_ = retrievr.ModelData{}
	_ = retrievr.DatasetData{}
	_ = retrievr.EncyclopediaData{}
	_ = retrievr.ScoreBreakdown{}
	_ = retrievr.ProvenanceTag{}

	// Enums + helpers.
	if !retrievr.IsValidIntent(string(retrievr.IntentDeepResearch)) {
		t.Fatalf("expected deep_research intent to be valid")
	}
	if retrievr.IsValidIntent("garbage") {
		t.Fatalf("expected garbage intent to be invalid")
	}

	// Intent flows onto SearchParams for the new chain-resolution path.
	_ = retrievr.SearchParams{Intent: retrievr.IntentDeepResearch}

	// Default fallback config is exposed.
	fb := retrievr.DefaultFallbackConfig()
	if len(fb.Chains) == 0 {
		t.Fatalf("DefaultFallbackConfig must include at least one chain")
	}
	_ = retrievr.FallbackChain{Primary: []string{"arxiv"}}
	if !retrievr.IsValidEUMode(string(retrievr.EUModePreferred)) {
		t.Fatalf("expected eu_preferred to be valid")
	}
	if !retrievr.RegionEU.IsEU() {
		t.Fatalf("RegionEU.IsEU() must be true")
	}
	if retrievr.RegionUS.IsEU() {
		t.Fatalf("RegionUS.IsEU() must be false")
	}
	if !retrievr.RegionPublicResearch.IsPublicResearch() {
		t.Fatalf("RegionPublicResearch.IsPublicResearch() must be true")
	}

	// Source ID constants resolve.
	if !retrievr.IsValidSourceID(retrievr.SourceArXiv) {
		t.Fatalf("SourceArXiv must be valid")
	}
	if got := retrievr.AllSourceIDs(); len(got) == 0 {
		t.Fatalf("AllSourceIDs returned empty slice")
	}

	// Credentials surface.
	ctx := retrievr.WithCredentials(context.Background(), map[string]string{
		"exa":    "test-exa-key",
		"github": "ghp_test",
	})
	if got := retrievr.CredentialFor(ctx, "exa"); got != "test-exa-key" {
		t.Fatalf("CredentialFor(exa) = %q; want test-exa-key", got)
	}
	if got := retrievr.CredentialFor(ctx, "missing"); got != "" {
		t.Fatalf("CredentialFor(missing) = %q; want empty", got)
	}
	if m := retrievr.CredentialsFromContext(ctx); len(m) != 2 {
		t.Fatalf("CredentialsFromContext: len=%d; want 2", len(m))
	}
	if m := retrievr.CredentialsFromContext(context.Background()); m != nil {
		t.Fatalf("CredentialsFromContext on bare ctx must be nil; got %v", m)
	}

	// Audit sink.
	var sink retrievr.AuditSink = retrievr.NoopAuditSink()
	sink.Emit(context.Background(), retrievr.AuditEvent{
		AuditRef: "evt_test",
		Mode:     string(retrievr.EUModeOff),
		Intent:   string(retrievr.IntentQuickLookup),
	})

	// Errors are typed sentinels.
	if retrievr.ErrNotImplemented == nil {
		t.Fatal("ErrNotImplemented sentinel must be non-nil")
	}
	if retrievr.ErrEUModeProviderConflict == nil {
		t.Fatal("ErrEUModeProviderConflict sentinel must be non-nil")
	}

	// Plugin subpackage.
	reg := plugin.NewRegistry()
	if reg == nil {
		t.Fatal("plugin.NewRegistry returned nil")
	}
	if got := reg.Len(); got != 0 {
		t.Fatalf("fresh Registry.Len() = %d; want 0", got)
	}
	if _, ok := reg.Resolve("nonexistent"); ok {
		t.Fatal("Resolve on empty registry must miss")
	}

	// ResidencyTag is constructible (cycle 2 made Region/DPAStatus typed enums).
	_ = plugin.ResidencyTag{
		Region:    retrievr.RegionEU,
		DPAStatus: retrievr.DPASigned,
	}

	// Middleware composition.
	final := func(_ context.Context, _ string, _ retrievr.SearchParams) (*retrievr.SearchResult, error) {
		return &retrievr.SearchResult{}, nil
	}
	noop := plugin.Chain()(final)
	if _, err := noop(context.Background(), "arxiv", retrievr.SearchParams{}); err != nil {
		t.Fatalf("empty middleware chain returned error: %v", err)
	}
}

// TestClientStubReturnsNotImplemented ensures the cycle-1 stub Client returns
// a recognizable error rather than panicking on a nil router.
func TestClientStubReturnsNotImplemented(t *testing.T) {
	t.Parallel()

	c := retrievr.NewClientFromRouter(nil)
	if c == nil {
		t.Fatal("NewClientFromRouter(nil) returned nil")
	}

	_, err := c.Search(context.Background(), retrievr.SearchParams{Query: "x"}, nil)
	if err != retrievr.ErrNotImplemented {
		t.Fatalf("Search on nil-router client = %v; want ErrNotImplemented", err)
	}

	// Credentials map flows into internal-package ctx slot via mirror.
	ctxWithCreds := retrievr.WithCredentials(context.Background(), map[string]string{
		"pubmed": "test-pubmed-key",
	})
	_, err = c.Search(ctxWithCreds, retrievr.SearchParams{Query: "x"}, nil)
	if err != retrievr.ErrNotImplemented {
		t.Fatalf("Search on nil-router client = %v; want ErrNotImplemented", err)
	}

	_, err = c.Get(context.Background(), "arxiv:2401.12345", nil, retrievr.FormatNative)
	if err != retrievr.ErrNotImplemented {
		t.Fatalf("Get on nil-router client = %v; want ErrNotImplemented", err)
	}

	if got := c.ListSources(context.Background()); got != nil {
		t.Fatalf("ListSources on nil-router client = %v; want nil", got)
	}
}
