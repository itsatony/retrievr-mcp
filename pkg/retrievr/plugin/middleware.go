package plugin

import (
	"context"

	"github.com/itsatony/retrievr-mcp/v2/internal"
)

// Handler is the plugin-call shape that middleware wraps. It carries the
// resolved sourceID alongside SearchParams so middleware (rate-limit, retry,
// metrics) can key its state without re-reading from params.
//
// Cycle-1 status: type defined, not yet wired into Router. Cycle 1 task #3
// extracts the existing hardcoded chain from internal.Router.searchOneSource
// into composable Middleware values applied in this order (outermost first):
// metrics → cache → fallback → retry → rate-limit → timeout → plugin.
type Handler func(ctx context.Context, sourceID string, params internal.SearchParams) (*internal.SearchResult, error)

// Middleware is a Handler decorator. Composition is deferred to a future
// commit in cycle 1 (task #3); declaring the type here lets future middleware
// implementations type-check immediately.
type Middleware func(next Handler) Handler

// Chain composes middlewares into a single Middleware applied left-to-right
// (the first middleware is outermost). Calling Chain()(final) returns a
// Handler equivalent to applying each Middleware in order around final.
func Chain(mws ...Middleware) Middleware {
	return func(final Handler) Handler {
		// Apply right-to-left so the leftmost ends up outermost.
		for i := len(mws) - 1; i >= 0; i-- {
			final = mws[i](final)
		}
		return final
	}
}
