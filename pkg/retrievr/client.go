package retrievr

import (
	"context"
	"io"
	"log/slog"

	"github.com/itsatony/retrievr-mcp/internal"
)

// Client is the public entry-point for direct in-process consumers (liz,
// nexus, retrievr-cli) and for the MCP wrapper at cmd/retrievr-mcp/.
//
// Cycle-1 status: SKELETON. Search/Get/ListSources delegate to a wrapped
// *internal.Router constructed via NewClientFromRouter (the cycle-1 escape
// hatch — cmd/retrievr-mcp keeps using internal.Server for now and passes
// its already-built Router here so external Go callers can adopt the public
// import path early).
//
// Cycle 2 introduces a real NewClient(opts ...ClientOption) (*Client, error)
// constructor that builds the router from config + options, removes the
// escape hatch, and adds the middleware chain.
type Client struct {
	router *internal.Router
	logger *slog.Logger

	// Recorded for cycle-2 enforcement; ignored in cycle 1.
	euMode                  EUMode
	euIncludePublicResearch bool
	auditSink               AuditSink
	primaryChains           map[Intent][]string
	fallbackChains          map[Intent][]string
	includeRaw              bool
}

// NewClientFromRouter wraps an existing *internal.Router as a public Client.
// Cycle-1 escape hatch — see Client godoc. Removed in cycle 2 in favor of
// NewClient(opts ...ClientOption).
func NewClientFromRouter(router *internal.Router, opts ...ClientOption) *Client {
	c := &Client{
		router:    router,
		logger:    slog.New(slog.NewJSONHandler(io.Discard, nil)),
		euMode:    EUModeOff,
		auditSink: NoopAuditSink(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Search executes a search request against the configured provider set.
//
// sources is an optional explicit source-ID allowlist; pass nil/empty to use
// the configured defaults. Cycle 2 moves Sources (and Intent + EUMode) onto
// SearchParams; the standalone parameter goes away then.
//
// Credentials may be attached via either WithCredentials(ctx, map[string]string)
// (preferred) or WithLegacyCredentials(ctx, *CallCredentials). Both surfaces
// are honored by every plugin via internal.CredentialFor.
func (c *Client) Search(ctx context.Context, params SearchParams, sources []string) (*MergedSearchResult, error) {
	if c.router == nil {
		return nil, ErrNotImplemented
	}
	ctx = mirrorCredsToInternal(ctx)
	creds := legacyCredsFromContext(ctx)
	return c.router.Search(ctx, params, sources, creds)
}

// StreamEvent re-exports internal.StreamEvent for cross-module Stream consumers.
type StreamEvent = internal.StreamEvent

// Stream runs a search and forwards per-source SearchResults as they
// arrive on the returned channel. The channel closes when all sources
// complete or ctx is cancelled. Cycle 3 entry point — useful for slow
// providers (Perplexity Sonar median 5–13s) where the caller wants to
// render hits incrementally.
//
// Trade-offs vs. Search:
//   - No cross-source dedup (the streamed results may include duplicates
//     across sources sharing a DOI/URL).
//   - No fallback walk (streaming can't decide without buffering).
//   - EU-mode gate, refusal path, audit event, and HTTP hygiene all still apply.
//
// Not exposed via MCP — MCP tool results aren't streaming. retrievr-cli
// exposes via the --stream flag.
func (c *Client) Stream(ctx context.Context, params SearchParams, sources []string) (<-chan StreamEvent, error) {
	if c.router == nil {
		return nil, ErrNotImplemented
	}
	ctx = mirrorCredsToInternal(ctx)
	creds := legacyCredsFromContext(ctx)
	return c.router.Stream(ctx, params, sources, creds)
}

// SearchV2 runs Search and returns the v2 fat-struct shape with per-kind
// data blocks (Result.Kind discriminator + Paper / Web / Code / etc.).
// Cycle-2 entry point for v2 callers; cycle-1 Search keeps returning v1
// Publication shape so existing consumers stay byte-stable.
func (c *Client) SearchV2(ctx context.Context, params SearchParams, sources []string) (*MergedSearchResultV2, error) {
	if c.router == nil {
		return nil, ErrNotImplemented
	}
	ctx = mirrorCredsToInternal(ctx)
	creds := legacyCredsFromContext(ctx)
	return c.router.SearchV2(ctx, params, sources, creds)
}

// Get retrieves a single result by its prefixed ID (e.g. "arxiv:2401.12345").
func (c *Client) Get(ctx context.Context, prefixedID string, include []IncludeField, format ContentFormat) (*Publication, error) {
	if c.router == nil {
		return nil, ErrNotImplemented
	}
	ctx = mirrorCredsToInternal(ctx)
	creds := legacyCredsFromContext(ctx)
	return c.router.Get(ctx, prefixedID, include, format, creds)
}

// mirrorCredsToInternal copies any per-call credential map attached via
// WithCredentials into the internal-package's ctx slot so plugins can read
// it via internal.CredentialFor.
func mirrorCredsToInternal(ctx context.Context) context.Context {
	if m := CredentialsFromContext(ctx); m != nil {
		ctx = internal.WithPerCallCredsMap(ctx, m)
	}
	return ctx
}

// ListSources returns capability + health info for every registered provider.
func (c *Client) ListSources(ctx context.Context) []SourceInfo {
	if c.router == nil {
		return nil
	}
	return c.router.ListSources(ctx)
}

// ---------------------------------------------------------------------------
// Cycle-1 ctx bridge
//
// The legacy *CallCredentials pointer is what the existing internal Router
// expects. The MCP wrapper attaches it under legacyCredsKey while it
// progressively migrates to the new map-based WithCredentials. Both paths
// coexist in cycle 1; task #2 removes legacyCredsKey entirely.
// ---------------------------------------------------------------------------

type legacyCredsKeyT struct{}

var legacyCredsKey = legacyCredsKeyT{}

// WithLegacyCredentials attaches the v1 *CallCredentials struct to ctx. Used
// by cmd/retrievr-mcp during cycle 1; removed in cycle 2.
func WithLegacyCredentials(ctx context.Context, creds *CallCredentials) context.Context {
	if creds == nil {
		return ctx
	}
	return context.WithValue(ctx, legacyCredsKey, creds)
}

func legacyCredsFromContext(ctx context.Context) *CallCredentials {
	if v, ok := ctx.Value(legacyCredsKey).(*CallCredentials); ok {
		return v
	}
	return nil
}
