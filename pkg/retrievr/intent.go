package retrievr

import "github.com/itsatony/retrievr-mcp/internal"

// Intent declares what the caller is trying to accomplish, letting retrievr
// pick the right primary source set + fallback chain instead of forcing the
// caller to enumerate sources.
//
// Cycle-1: Intent is read from SearchParams.Intent and resolved against the
// configured RouterFallbackConfig. When unset, Router falls back to its
// default source set with no fallback walking (legacy behavior preserved).
type Intent = internal.Intent

// Intent constants — re-exported from internal for the public surface.
const (
	IntentDeepResearch   = internal.IntentDeepResearch
	IntentQuickLookup    = internal.IntentQuickLookup
	IntentPrimarySource  = internal.IntentPrimarySource
	IntentCodeProvenance = internal.IntentCodeProvenance
	IntentNews           = internal.IntentNews
	IntentReference      = internal.IntentReference
)

// IsValidIntent returns true if the given string is a known Intent.
func IsValidIntent(i string) bool { return internal.IsValidIntent(i) }

// FallbackChain re-exports the internal.FallbackChain for direct importers
// who want to construct a custom RouterFallbackConfig.
type FallbackChain = internal.FallbackChain

// RouterFallbackConfig re-exports internal.RouterFallbackConfig.
type RouterFallbackConfig = internal.RouterFallbackConfig

// DefaultFallbackConfig returns the cycle-1 default chain set (academic
// only). Cycle 2 adds wave-1 chains when web/code/encyclopedia providers
// land.
func DefaultFallbackConfig() RouterFallbackConfig { return internal.DefaultFallbackConfig() }
