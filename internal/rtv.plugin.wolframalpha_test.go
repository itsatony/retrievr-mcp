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
// v6 cycle 5 / v2.18.0 — Wolfram Alpha tests.
// ---------------------------------------------------------------------------

func newWolframAlphaTestPlugin(t *testing.T, baseURL, apiKey string) *WolframAlphaPlugin {
	t.Helper()
	p := &WolframAlphaPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestWolframAlpha_Identity(t *testing.T) {
	t.Parallel()
	p := &WolframAlphaPlugin{}
	assert.Equal(t, SourceWolframAlpha, p.ID())
}

func TestWolframAlpha_Capabilities(t *testing.T) {
	t.Parallel()
	p := &WolframAlphaPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindFact)
	assert.True(t, caps.RequiresCredential)
}

func TestWolframAlpha_Residency(t *testing.T) {
	t.Parallel()
	p := &WolframAlphaPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestWolframAlpha_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newWolframAlphaTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestWolframAlpha_Search_HappyPath_PrimaryHoistedFirst(t *testing.T) {
	t.Parallel()
	// Pods returned in non-primary-first order to exercise reordering.
	resp := wolframAlphaResponse{
		QueryResult: wolframAlphaQueryResult{
			Success: true,
			NumPods: 2,
			Pods: []wolframAlphaPod{
				{Title: "Input", ID: "Input", Scanner: "Identity", Subpods: []wolframAlphaSubpod{{Plaintext: "2 pi"}}},
				{Title: "Result", ID: "Result", Primary: true, Subpods: []wolframAlphaSubpod{{Plaintext: "6.28318530717958..."}}},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, wolframAlphaQueryPath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "2 pi", q.Get(wolframAlphaParamInput))
		assert.Equal(t, "test-key", q.Get(wolframAlphaParamAppID))
		assert.Equal(t, wolframAlphaOutputJSON, q.Get(wolframAlphaParamOutput))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newWolframAlphaTestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "2 pi"})
	require.NoError(t, err)
	require.Len(t, res.Results, 2)

	// Result pod must be first.
	first := res.Results[0]
	assert.Equal(t, "wolframalpha:Result", first.ID)
	assert.Equal(t, "Result", first.Title)
	assert.Contains(t, first.Abstract, "6.28318")
	assert.Equal(t, "Result", first.SourceMetadata[wolframAlphaMetaKeyPodID])

	second := res.Results[1]
	assert.Equal(t, "Input", second.Title)
}

func TestWolframAlpha_Search_SkipsEmptyPods(t *testing.T) {
	t.Parallel()
	resp := wolframAlphaResponse{
		QueryResult: wolframAlphaQueryResult{
			Pods: []wolframAlphaPod{
				{Title: "Empty", ID: "Empty"}, // no subpods
				{Title: "OK", ID: "OK", Subpods: []wolframAlphaSubpod{{Plaintext: "value"}}},
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	p := newWolframAlphaTestPlugin(t, srv.URL, "test-key")
	res, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	assert.Equal(t, "OK", res.Results[0].Title)
}

func TestWolframAlpha_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newWolframAlphaTestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestWolframAlpha_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newWolframAlphaTestPlugin(t, srv.URL, "test-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestWolframAlpha_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &WolframAlphaPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestWolframAlpha_ReorderPrimaryFirst(t *testing.T) {
	t.Parallel()
	in := []wolframAlphaPod{
		{ID: "A"},
		{ID: "B"},
		{ID: "Result", Primary: true},
		{ID: "D"},
	}
	out := reorderPrimaryFirst(in)
	assert.Equal(t, "Result", out[0].ID)
	assert.Equal(t, "A", out[1].ID)
	assert.Equal(t, "B", out[2].ID)
	assert.Equal(t, "D", out[3].ID)
}

// keep io alive
var _ = io.Discard
