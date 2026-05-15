package internal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTokenCache_PerCredentialIsolation(t *testing.T) {
	t.Parallel()
	c := newTokenCache("testsource")
	c.Set("keyA", "tokenA", time.Hour)
	c.Set("keyB", "tokenB", time.Hour)

	got, ok := c.Get("keyA")
	assert.True(t, ok)
	assert.Equal(t, "tokenA", got)

	got, ok = c.Get("keyB")
	assert.True(t, ok)
	assert.Equal(t, "tokenB", got)

	// Anonymous (empty key) stays distinct.
	c.Set("", "tokenAnon", time.Hour)
	got, ok = c.Get("")
	assert.True(t, ok)
	assert.Equal(t, "tokenAnon", got)
	// Still doesn't leak to keyA/keyB.
	gotA, _ := c.Get("keyA")
	assert.Equal(t, "tokenA", gotA)
}

func TestTokenCache_ExpiryRespected(t *testing.T) {
	t.Parallel()
	c := newTokenCache("testsource")
	c.Set("k", "t", -time.Second) // already-expired
	_, ok := c.Get("k")
	assert.False(t, ok)
}

func TestTokenCache_Invalidate(t *testing.T) {
	t.Parallel()
	c := newTokenCache("testsource")
	c.Set("k", "t", time.Hour)
	_, ok := c.Get("k")
	assert.True(t, ok)
	c.Invalidate("k")
	_, ok = c.Get("k")
	assert.False(t, ok)
}

func TestTokenCache_NilSafe(t *testing.T) {
	t.Parallel()
	var c *tokenCache
	c.Set("k", "t", time.Hour) // must not panic
	_, ok := c.Get("k")
	assert.False(t, ok)
	c.Invalidate("k")
	c.PruneExpired()
}

func TestTokenCache_PruneExpired(t *testing.T) {
	t.Parallel()
	c := newTokenCache("testsource")
	c.Set("alive", "t1", time.Hour)
	c.Set("dead", "t2", -time.Second)
	c.PruneExpired()
	c.mu.RLock()
	defer c.mu.RUnlock()
	assert.Len(t, c.entries, 1)
}

func TestTokenCache_CrossTenantNoLeak(t *testing.T) {
	t.Parallel()
	// Concrete simulation of the H1 review finding: two tenants with
	// distinct credentials must each get their own token.
	c := newTokenCache("reddit")
	c.Set("cidA:secretA", "tokenForA", time.Hour)
	gotB, ok := c.Get("cidB:secretB")
	assert.False(t, ok, "tenant B must NOT see tenant A's token")
	assert.Empty(t, gotB)
}
