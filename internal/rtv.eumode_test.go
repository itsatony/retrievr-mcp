package internal

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Cycle 2 task #9 — EU-mode conformance suite.
//
// Six hooks, six test groups. Each test stays small and asserts one
// hook's contract independently of the others.
// ---------------------------------------------------------------------------

// euTestPlugin is a mock SourcePlugin with a configurable Residency tag.
// We re-use mockPlugin (router_test.go) and override its Residency() via a
// thin embedding wrapper.
type euTestPlugin struct {
	*mockPlugin
	residency ResidencyTag
}

func (p *euTestPlugin) Residency() ResidencyTag { return p.residency }

func newEUPlugin(id string, region Region) *euTestPlugin {
	return &euTestPlugin{
		mockPlugin: newMockPlugin(id, []Publication{testPub(id, id+":1", testDOI1, nil)}),
		residency:  ResidencyTag{Region: region, DPAStatus: DPAUnknown},
	}
}

// ---------------------------------------------------------------------------
// Hook #1 — residency tags surfaced.
// ---------------------------------------------------------------------------

func TestEUMode_Hook1_PluginResidencyTagsAccessible(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv:     &ArXivPlugin{},
		SourceDBLP:      &DBLPPlugin{},
		SourceEuropePMC: &EuropePMCPlugin{},
		SourceADS:       &ADSPlugin{},
	}

	cases := []struct {
		id          string
		wantRegion  Region
		isEU        bool
		isPublicRes bool
	}{
		{SourceArXiv, RegionPublicResearch, false, true},
		{SourceDBLP, RegionEU, true, false},
		{SourceEuropePMC, RegionUKAdequacy, true, false},
		{SourceADS, RegionUS, false, false},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			tag := plugins[c.id].Residency()
			assert.Equal(t, c.wantRegion, tag.Region)
			assert.Equal(t, c.isEU, tag.Region.IsEU())
			assert.Equal(t, c.isPublicRes, tag.Region.IsPublicResearch())
		})
	}
}

// ---------------------------------------------------------------------------
// Hook #2 — mode gate pre-fanout.
// ---------------------------------------------------------------------------

func TestEUMode_Hook2_StrictAdmitsOnlyEU(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{
		"euA": newEUPlugin("euA", RegionEU),
		"ukB": newEUPlugin("ukB", RegionUKAdequacy),
		"usC": newEUPlugin("usC", RegionUS),
		"prD": newEUPlugin("prD", RegionPublicResearch),
	}
	gate := applyEUGate([]string{"euA", "ukB", "usC", "prD"}, plugins, EUModeStrict, false)
	assert.ElementsMatch(t, []string{"euA", "ukB"}, gate.Admitted)
	assert.Len(t, gate.Skipped, 2)
	for _, s := range gate.Skipped {
		assert.Equal(t, skipReasonEUStrict, s.Reason)
	}
}

func TestEUMode_Hook2_StrictWithPublicResearchOptIn(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{
		"euA": newEUPlugin("euA", RegionEU),
		"prB": newEUPlugin("prB", RegionPublicResearch),
		"usC": newEUPlugin("usC", RegionUS),
	}
	gate := applyEUGate([]string{"euA", "prB", "usC"}, plugins, EUModeStrict, true)
	assert.ElementsMatch(t, []string{"euA", "prB"}, gate.Admitted)
	require.Len(t, gate.Skipped, 1)
	assert.Equal(t, "usC", gate.Skipped[0].ID)
}

func TestEUMode_Hook2_OffAndPreferredAdmitEveryone(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{
		"euA": newEUPlugin("euA", RegionEU),
		"usC": newEUPlugin("usC", RegionUS),
	}
	for _, mode := range []string{EUModeOff, EUModePreferred, ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			gate := applyEUGate([]string{"euA", "usC"}, plugins, mode, false)
			assert.ElementsMatch(t, []string{"euA", "usC"}, gate.Admitted)
			assert.Empty(t, gate.Skipped)
		})
	}
}

// ---------------------------------------------------------------------------
// Hook #3 — outbound query audit log.
// ---------------------------------------------------------------------------

type captureSink struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (c *captureSink) Emit(_ context.Context, evt AuditEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, evt)
}

func (c *captureSink) Events() []AuditEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]AuditEvent, len(c.events))
	copy(out, c.events)
	return out
}

func TestEUMode_Hook3_AuditEventEmittedPerSearch(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{
		"euA": newEUPlugin("euA", RegionEU),
	}
	sink := &captureSink{}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{"euA"}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger(),
		WithEUMode(EUModeStrict, false),
		WithAuditSink(sink),
	)

	merged, err := r.Search(context.Background(), SearchParams{Query: "needle in haystack", Limit: 5}, nil, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, merged.AuditRef, "AuditRef must be surfaced in response")

	events := sink.Events()
	require.Len(t, events, 1)
	evt := events[0]
	assert.Equal(t, merged.AuditRef, evt.AuditRef)
	assert.Equal(t, EUModeStrict, evt.Mode)
	assert.NotEmpty(t, evt.QueryHash, "query must be hashed by default")
	assert.Empty(t, evt.QueryPlaintext, "query plaintext must be omitted by default")
	assert.Contains(t, evt.ProvidersInvoked, "euA")
}

func TestEUMode_Hook3_PlaintextOptInRecordsRawQuery(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{
		"euA": newEUPlugin("euA", RegionEU),
	}
	sink := &captureSink{}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{"euA"}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger(),
		WithEUMode(EUModeStrict, false),
		WithAuditSink(sink),
		WithAuditLogQueryPlaintext(true),
	)
	const rawQuery = "sensitive query string"
	_, err := r.Search(context.Background(), SearchParams{Query: rawQuery, Limit: 5}, nil, nil)
	require.NoError(t, err)
	require.Len(t, sink.Events(), 1)
	assert.Equal(t, rawQuery, sink.Events()[0].QueryPlaintext)
}

// ---------------------------------------------------------------------------
// Hook #4 — outbound HTTP hygiene.
// ---------------------------------------------------------------------------

func TestEUMode_Hook4_EgressClientInjectsNeutralUA(t *testing.T) {
	t.Parallel()
	gotUA := ""
	gotReferer := ""
	gotXFF := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotReferer = r.Header.Get("Referer")
		gotXFF = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewEgressClient(5e9 /* 5s, avoid time import */)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	// Try to leak some headers; the round-tripper must strip them.
	req.Header.Set("Referer", "https://leaky.example.com/page")
	req.Header.Set("X-Forwarded-For", "203.0.113.42")
	resp, err := client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.True(t, strings.HasPrefix(gotUA, "retrievr/"), "UA must be retrievr-prefixed; got %q", gotUA)
	assert.Empty(t, gotReferer, "Referer must be stripped")
	assert.Empty(t, gotXFF, "X-Forwarded-For must be stripped")
}

// ---------------------------------------------------------------------------
// Hook #5 — refusal path.
// ---------------------------------------------------------------------------

func TestEUMode_Hook5_StrictPlusNonEUSourcesReturnsConflict(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{
		"euA": newEUPlugin("euA", RegionEU),
		"usC": newEUPlugin("usC", RegionUS),
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{"euA", "usC"}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger(),
		WithEUMode(EUModeStrict, false),
	)
	_, err := r.Search(context.Background(), SearchParams{Query: "x", Limit: 5}, []string{"usC"}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEUModeProviderConflict), "must satisfy errors.Is(err, ErrEUModeProviderConflict); got %v", err)

	var typed *EUModeProviderConflictError
	require.True(t, errors.As(err, &typed), "must satisfy errors.As to EUModeProviderConflictError")
	assert.Equal(t, []string{"usC"}, typed.Blocked)
}

func TestEUMode_Hook5_PreferredDoesNotRefuse(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{
		"euA": newEUPlugin("euA", RegionEU),
		"usC": newEUPlugin("usC", RegionUS),
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{"euA", "usC"}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger(),
		WithEUMode(EUModePreferred, false),
	)
	_, err := r.Search(context.Background(), SearchParams{Query: "x", Limit: 5}, []string{"usC"}, nil)
	require.NoError(t, err, "eu_preferred must NOT refuse explicit non-EU sources")
}

// ---------------------------------------------------------------------------
// Hook #6 — config drift guard.
// ---------------------------------------------------------------------------

func TestEUMode_Hook6_DriftWarnsButContinuesByDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "providers.yaml")
	sig := filepath.Join(dir, "providers.snapshot.sig")
	require.NoError(t, os.WriteFile(manifest, []byte("real-content\n"), 0o644))
	require.NoError(t, os.WriteFile(sig, []byte("0000000000000000000000000000000000000000000000000000000000000000\n"), 0o644))

	err := VerifyProvidersSnapshot(SnapshotConfig{
		ProvidersFile: manifest,
		SignatureFile: sig,
		Strict:        false,
	}, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1})))
	require.NoError(t, err, "non-strict drift must be a warn, not a fatal")
}

func TestEUMode_Hook6_DriftErrorsInStrictMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "providers.yaml")
	sig := filepath.Join(dir, "providers.snapshot.sig")
	require.NoError(t, os.WriteFile(manifest, []byte("real-content\n"), 0o644))
	require.NoError(t, os.WriteFile(sig, []byte("not-the-real-hash\n"), 0o644))

	err := VerifyProvidersSnapshot(SnapshotConfig{
		ProvidersFile: manifest,
		SignatureFile: sig,
		Strict:        true,
	}, discardLogger())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConfigDriftDetected))
}

func TestEUMode_Hook6_MatchingHashIsNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "providers.yaml")
	sig := filepath.Join(dir, "providers.snapshot.sig")
	content := []byte("real-content\n")
	require.NoError(t, os.WriteFile(manifest, content, 0o644))
	require.NoError(t, os.WriteFile(sig, []byte(computeSHA256Hex(content)+"\n"), 0o644))

	err := VerifyProvidersSnapshot(SnapshotConfig{
		ProvidersFile: manifest,
		SignatureFile: sig,
		Strict:        true,
	}, discardLogger())
	assert.NoError(t, err)
}

func TestEUMode_Hook6_DisabledWhenFilesUnset(t *testing.T) {
	t.Parallel()
	err := VerifyProvidersSnapshot(SnapshotConfig{}, discardLogger())
	assert.NoError(t, err, "snapshot guard must be a no-op when files unset")
}

// ---------------------------------------------------------------------------
// hashQuery + generateAuditRef sanity
// ---------------------------------------------------------------------------

func TestHashQuery_DeterministicAndCorrectLen(t *testing.T) {
	t.Parallel()
	a := hashQuery("transformer attention")
	b := hashQuery("transformer attention")
	assert.Equal(t, a, b)
	assert.Len(t, a, auditQueryHashHexLen)

	c := hashQuery("different query")
	assert.NotEqual(t, a, c)
}

func TestHashQuery_EmptyReturnsEmpty(t *testing.T) {
	t.Parallel()
	assert.Empty(t, hashQuery(""))
}

func TestGenerateAuditRef_PrefixedAndUnique(t *testing.T) {
	t.Parallel()
	a := generateAuditRef()
	b := generateAuditRef()
	assert.True(t, strings.HasPrefix(a, auditRefPrefix))
	assert.True(t, strings.HasPrefix(b, auditRefPrefix))
	assert.NotEqual(t, a, b)
}
