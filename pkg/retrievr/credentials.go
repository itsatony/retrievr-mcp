package retrievr

import (
	"context"

	"github.com/itsatony/retrievr-mcp/v2/internal"
)

// CallCredentials carries per-call auth that overrides server config.
//
// v1 shape (typed-fields, source-specific). Cycle 2 introduces a parallel
// map[string]string surface keyed by source ID; both will coexist in v1.6.0
// and the typed struct is removed at v2.0.0.
type CallCredentials = internal.CallCredentials

// ---------------------------------------------------------------------------
// Cycle-2 forward-compat: ctx-based credential map.
//
// These helpers exist now (cycle 1) so callers can adopt the new pattern
// early. The internal Router still consumes *CallCredentials directly until
// task #2 lands (cycle 1) — at which point plugin signatures drop the creds
// param and read from ctx.
// ---------------------------------------------------------------------------

type credKey struct{}

// WithCredentials attaches a per-call credential map to ctx, keyed by source
// ID (e.g., "exa", "github", "linkup"). Plugins read via CredentialsFromContext.
func WithCredentials(ctx context.Context, m map[string]string) context.Context {
	if m == nil {
		return ctx
	}
	// Defensive copy so callers can't mutate after attaching.
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return context.WithValue(ctx, credKey{}, cp)
}

// CredentialsFromContext returns the credential map attached via
// WithCredentials, or nil when no credentials are present.
func CredentialsFromContext(ctx context.Context) map[string]string {
	if v, ok := ctx.Value(credKey{}).(map[string]string); ok {
		return v
	}
	return nil
}

// CredentialFor returns the credential for a specific source ID from ctx, or
// the empty string when not present. Convenience for plugins.
func CredentialFor(ctx context.Context, sourceID string) string {
	m := CredentialsFromContext(ctx)
	if m == nil {
		return ""
	}
	return m[sourceID]
}
