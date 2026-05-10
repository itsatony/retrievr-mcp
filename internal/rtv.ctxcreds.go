package internal

import "context"

// Cycle-1 task #2 — credentials flow through context, not function parameters.
//
// Two ctx keys coexist during the v1.5.0 → v1.6.0 transition:
//
//   - callCredsKey holds the legacy *CallCredentials struct (typed source-
//     specific fields). The MCP wrapper at cmd/retrievr-mcp/ continues to
//     populate it from JSON args during cycle 1; cycle 2 retires it.
//   - perCallCredsMapKey holds map[string]string keyed by source ID, set by
//     pkg/retrievr.WithCredentials and mirrored down into internal by
//     pkg/retrievr.Client before invoking Router.
//
// CredentialFor consults both, preferring the map when both are present.

type callCredsKeyT struct{}
type perCallCredsMapKeyT struct{}

var (
	callCredsKey       = callCredsKeyT{}
	perCallCredsMapKey = perCallCredsMapKeyT{}
)

// WithCallCredentials attaches the legacy *CallCredentials struct to ctx.
// Used by Router before invoking plugins so the plugin signature can drop
// its creds parameter.
func WithCallCredentials(ctx context.Context, c *CallCredentials) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, callCredsKey, c)
}

// CallCredentialsFromContext returns the legacy *CallCredentials attached via
// WithCallCredentials, or nil when none.
func CallCredentialsFromContext(ctx context.Context) *CallCredentials {
	if v, ok := ctx.Value(callCredsKey).(*CallCredentials); ok {
		return v
	}
	return nil
}

// WithPerCallCredsMap attaches a source-ID-keyed credential map. Used by
// pkg/retrievr.Client to mirror the public WithCredentials map into internal
// scope so plugins can read it.
func WithPerCallCredsMap(ctx context.Context, m map[string]string) context.Context {
	if len(m) == 0 {
		return ctx
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return context.WithValue(ctx, perCallCredsMapKey, cp)
}

// PerCallCredsMapFromContext returns the source-ID-keyed map attached via
// WithPerCallCredsMap, or nil when none.
func PerCallCredsMapFromContext(ctx context.Context) map[string]string {
	if v, ok := ctx.Value(perCallCredsMapKey).(map[string]string); ok {
		return v
	}
	return nil
}

// CredentialFor returns the effective credential string for sourceID, falling
// back to fallback when neither ctx surface carries one.
//
// Resolution order (first hit wins):
//  1. perCallCredsMap[sourceID] — new map-based path (pkg/retrievr.WithCredentials)
//  2. CallCredentials.ResolveForSource(sourceID, fallback) — legacy typed path
//  3. fallback (server-default config value)
//
// Returning "" is a deliberate signal to the caller that no credential is
// configured (e.g., anonymous-tier API access). Callers must not panic on
// empty.
func CredentialFor(ctx context.Context, sourceID, fallback string) string {
	if ctx == nil {
		return fallback
	}
	if m := PerCallCredsMapFromContext(ctx); m != nil {
		if v, ok := m[sourceID]; ok && v != "" {
			return v
		}
	}
	if c := CallCredentialsFromContext(ctx); c != nil {
		if v := c.ResolveForSource(sourceID, ""); v != "" {
			return v
		}
	}
	return fallback
}
