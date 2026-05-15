package internal

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Per-credential token cache — review-hardening pass.
//
// Plugins that mint short-lived bearer tokens (Reddit OAuth, EPO OPS
// client_credentials, Dimensions DSL auth) previously kept one
// `accessToken/tokenExpiry` pair per plugin instance. Under multi-
// tenant per-call credentials, this was unsafe: tenant A's token
// would be re-used by tenant B's subsequent call until expiry,
// silently mis-attributing requests and billing.
//
// tokenCache fixes that by keying cached tokens on the CredentialHash
// of the supplied API key. Each tenant gets an isolated entry; expiry
// is per-entry; TTL-driven eviction prunes unused entries.
//
// Plugins call:
//
//	if tok, ok := p.tokens.Get(apiKey); ok {
//	    return tok, nil
//	}
//	// ... mint a fresh token via http ...
//	p.tokens.Set(apiKey, freshToken, lifetime)
//
// And on auth-rejection (401/403):
//
//	p.tokens.Invalidate(apiKey)
//
// Thread-safe. Lookups and writes both take the same RWMutex.
// ---------------------------------------------------------------------------

// tokenCacheEntry holds a single tenant's cached token + expiry.
type tokenCacheEntry struct {
	token  string
	expiry time.Time
}

// tokenCache is a map of `<sourceID>:<credentialHash>` → entry.
// SourceID prefix prevents collisions when two plugin types share an
// instance (none today, but cheap insurance).
type tokenCache struct {
	mu       sync.RWMutex
	entries  map[string]tokenCacheEntry
	sourceID string
}

// newTokenCache constructs a cache scoped to one sourceID.
func newTokenCache(sourceID string) *tokenCache {
	return &tokenCache{
		entries:  make(map[string]tokenCacheEntry),
		sourceID: sourceID,
	}
}

// keyFor returns the credential-hashed cache key.
func (c *tokenCache) keyFor(apiKey string) string {
	return CredentialHash(c.sourceID, apiKey)
}

// Get returns the cached token for apiKey when one is present AND has
// not expired. The second return value is false otherwise.
func (c *tokenCache) Get(apiKey string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[c.keyFor(apiKey)]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiry) {
		return "", false
	}
	return e.token, true
}

// Set installs a token for apiKey with the given lifetime relative to
// now. lifetime ≤ 0 is treated as already-expired (effectively a no-op
// for subsequent Get calls).
func (c *tokenCache) Set(apiKey, token string, lifetime time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[c.keyFor(apiKey)] = tokenCacheEntry{
		token:  token,
		expiry: time.Now().Add(lifetime),
	}
}

// Invalidate removes the cached token for apiKey, forcing the next
// Get call to miss. Use this on auth-rejection responses (401/403)
// from upstream so the plugin re-mints rather than retrying with a
// known-bad token.
func (c *tokenCache) Invalidate(apiKey string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, c.keyFor(apiKey))
}

// PruneExpired drops any entries whose expiry is now in the past.
// Intended for periodic maintenance from a plugin's background loop;
// not required for correctness because Get already short-circuits on
// expiry.
func (c *tokenCache) PruneExpired() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, e := range c.entries {
		if now.After(e.expiry) {
			delete(c.entries, k)
		}
	}
}
