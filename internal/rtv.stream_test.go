package internal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Cycle 3 task #23 — Router.Stream tests.
// ---------------------------------------------------------------------------

func TestStream_FanOutEmitsPerSourceEvents(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		"sourceA": newMockPlugin("sourceA", []Publication{testPub("sourceA", "a:1", testDOI1, nil)}),
		"sourceB": newMockPlugin("sourceB", []Publication{testPub("sourceB", "b:1", testDOI2, nil)}),
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{"sourceA", "sourceB"}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())

	ch, err := r.Stream(context.Background(), SearchParams{Query: "x", Limit: 5}, nil, nil)
	require.NoError(t, err)

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	require.Len(t, events, 2, "expected one event per source")
	gotSources := map[string]bool{}
	for _, ev := range events {
		gotSources[ev.Source] = true
		assert.NoError(t, ev.Err, "no source should error in this scenario")
		assert.NotNil(t, ev.Result)
	}
	assert.True(t, gotSources["sourceA"])
	assert.True(t, gotSources["sourceB"])
}

func TestStream_PartialFailureSurfacesPerSourceError(t *testing.T) {
	t.Parallel()

	failing := newMockPlugin("badA", nil)
	failing.searchFunc = func(_ context.Context, _ SearchParams) (*SearchResult, error) {
		return nil, errors.New("upstream 503")
	}
	plugins := map[string]SourcePlugin{
		"badA":    failing,
		"goodB":   newMockPlugin("goodB", []Publication{testPub("goodB", "b:1", testDOI1, nil)}),
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{"badA", "goodB"}
	cfg.Retry = RouterRetryConfig{MaxAttempts: 1}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())

	ch, err := r.Stream(context.Background(), SearchParams{Query: "x", Limit: 5}, nil, nil)
	require.NoError(t, err)

	var (
		gotErr     bool
		gotSuccess bool
	)
	for ev := range ch {
		if ev.Err != nil {
			gotErr = true
			assert.Equal(t, "badA", ev.Source)
		} else {
			gotSuccess = true
			assert.Equal(t, "goodB", ev.Source)
		}
	}
	assert.True(t, gotErr, "failing source must surface as a per-source error event")
	assert.True(t, gotSuccess, "successful source must continue independently")
}

func TestStream_RespectsContextCancel(t *testing.T) {
	t.Parallel()

	slow := newMockPlugin("slowA", nil)
	slow.searchFunc = func(ctx context.Context, _ SearchParams) (*SearchResult, error) {
		select {
		case <-time.After(2 * time.Second):
			return &SearchResult{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	plugins := map[string]SourcePlugin{"slowA": slow}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{"slowA"}
	cfg.Retry = RouterRetryConfig{MaxAttempts: 1}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := r.Stream(ctx, SearchParams{Query: "x"}, nil, nil)
	require.NoError(t, err)
	cancel()

	// Channel must close eventually even though the slow provider never
	// returned naturally.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed — pass
			}
		case <-deadline:
			t.Fatal("Stream channel did not close after ctx cancellation")
		}
	}
}

func TestStream_EUModeGateAppliesPreFanout(t *testing.T) {
	t.Parallel()

	euPlugin := &euTestPlugin{
		mockPlugin: newMockPlugin("euA", []Publication{testPub("euA", "eu:1", testDOI1, nil)}),
		residency:  ResidencyTag{Region: RegionEU, DPAStatus: DPASigned},
	}
	usPlugin := &euTestPlugin{
		mockPlugin: newMockPlugin("usB", []Publication{testPub("usB", "us:1", testDOI2, nil)}),
		residency:  ResidencyTag{Region: RegionUS, DPAStatus: DPAUnknown},
	}
	plugins := map[string]SourcePlugin{"euA": euPlugin, "usB": usPlugin}

	cfg := testRouterConfig()
	cfg.DefaultSources = []string{"euA", "usB"}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger(),
		WithEUMode(EUModeStrict, false),
	)

	ch, err := r.Stream(context.Background(), SearchParams{Query: "x"}, nil, nil)
	require.NoError(t, err)

	var sources []string
	for ev := range ch {
		sources = append(sources, ev.Source)
	}
	assert.Equal(t, []string{"euA"}, sources, "eu_strict must filter usB before fan-out")
}

func TestStream_RefusalPathRejectsExplicitNonEUInStrict(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		"euA": &euTestPlugin{
			mockPlugin: newMockPlugin("euA", nil),
			residency:  ResidencyTag{Region: RegionEU},
		},
		"usB": &euTestPlugin{
			mockPlugin: newMockPlugin("usB", nil),
			residency:  ResidencyTag{Region: RegionUS},
		},
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{"euA"}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger(),
		WithEUMode(EUModeStrict, false),
	)

	_, err := r.Stream(context.Background(), SearchParams{Query: "x"}, []string{"usB"}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEUModeProviderConflict),
		"explicit eu_strict + non-EU sources must surface ErrEUModeProviderConflict pre-fanout")
}

func TestStream_NoSourcesReturnsError(t *testing.T) {
	t.Parallel()
	plugins := map[string]SourcePlugin{}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{"nonexistent"}
	r := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())

	_, err := r.Stream(context.Background(), SearchParams{Query: "x"}, nil, nil)
	require.Error(t, err)
}
