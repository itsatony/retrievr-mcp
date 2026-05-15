package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Cross-tenant token isolation — review-hardening regression test.
//
// The H1 finding (security review) was that Reddit / Dimensions / EPO OPS
// kept a single accessToken on the plugin struct, so tenant A's freshly-
// minted token leaked to tenant B's subsequent call until expiry.
//
// This test concretely simulates two distinct API keys and verifies:
//   1. /api/auth is hit once per distinct key (not shared).
//   2. The Authorization header on /api/dsl carries the per-tenant token.
// ---------------------------------------------------------------------------

func TestDimensions_CrossTenantTokenIsolation(t *testing.T) {
	t.Parallel()

	var authForKey1, authForKey2 atomic.Int32
	var searchTokensSeen sync.Map // map[token]bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, dimensionsAuthPath):
			var body dimensionsAuthRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			switch body.Key {
			case "tenant1-key":
				authForKey1.Add(1)
				_, _ = w.Write([]byte(`{"token":"tok-for-tenant1"}`))
			case "tenant2-key":
				authForKey2.Add(1)
				_, _ = w.Write([]byte(`{"token":"tok-for-tenant2"}`))
			default:
				t.Fatalf("unexpected key %q", body.Key)
			}
		case strings.HasSuffix(r.URL.Path, dimensionsDSLPath):
			tok := r.Header.Get(dimensionsHeaderAuth)
			searchTokensSeen.Store(tok, true)
			_, _ = w.Write([]byte(`{"_stats":{"total_count":0},"publications":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := &DimensionsPlugin{}
	require.NoError(t, p.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   srv.URL,
		RateLimit: 100,
	}))

	// Tenant 1 calls.
	ctx1 := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceDimensions: "tenant1-key",
	})
	_, err := p.Search(ctx1, SearchParams{Query: "x"})
	require.NoError(t, err)

	// Tenant 2 calls.
	ctx2 := WithPerCallCredsMap(context.Background(), map[string]string{
		SourceDimensions: "tenant2-key",
	})
	_, err = p.Search(ctx2, SearchParams{Query: "y"})
	require.NoError(t, err)

	// Tenant 1 calls AGAIN — should hit the cache (not re-auth).
	_, err = p.Search(ctx1, SearchParams{Query: "z"})
	require.NoError(t, err)

	// Each key auth'd once; tenant 1's second call reused the cache.
	assert.Equal(t, int32(1), authForKey1.Load(), "tenant 1 should auth once and reuse")
	assert.Equal(t, int32(1), authForKey2.Load(), "tenant 2 should auth once")

	// Each tenant's token reached /api/dsl distinctly.
	_, t1 := searchTokensSeen.Load("tok-for-tenant1")
	_, t2 := searchTokensSeen.Load("tok-for-tenant2")
	assert.True(t, t1, "tenant 1 token must have been used")
	assert.True(t, t2, "tenant 2 token must have been used")
}
