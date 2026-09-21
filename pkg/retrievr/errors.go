package retrievr

import (
	"errors"

	"github.com/itsatony/retrievr-mcp/v2/internal"
)

// Sentinel errors exposed by the public API. Cycle 2 wires the EU-mode and
// fallback errors into actual control-flow paths; cycle 1 declares them so
// downstream code can already type-switch against them.
var (
	// ErrNotImplemented is returned by Client methods that are stubbed in
	// cycle 1 and gain real bodies in cycles 2-3.
	ErrNotImplemented = errors.New("retrievr: not implemented in this cycle")

	// ErrNoProviders is returned when no provider matches the request after
	// applying intent + eu_mode filters.
	ErrNoProviders = errors.New("retrievr: no providers eligible for request")

	// ErrAllProvidersFailed is returned when every provider in the resolved
	// set returned an error (or zero results in zero-tolerance mode).
	ErrAllProvidersFailed = errors.New("retrievr: all providers failed")

	// ErrFallbackExhausted is returned when both the primary and fallback
	// chain for an intent fail.
	ErrFallbackExhausted = errors.New("retrievr: fallback chain exhausted")

	// ErrEUModeProviderConflict is returned when the caller requests
	// eu_strict mode together with an explicit Sources list that contains
	// a non-EU provider. Cycle 2 attaches structured detail (requested,
	// blocked) via a typed error wrapper.
	ErrEUModeProviderConflict = errors.New("retrievr: eu_strict mode incompatible with requested sources")

	// ErrUnsupportedKind is returned by Client.Get for kinds whose providers
	// do not support stable cross-call IDs (web, news in some cases).
	//
	// Deprecated: nothing produces this. Classify a refused Get with
	// ErrGetUnsupported, which the router and every search-only plugin
	// actually return.
	ErrUnsupportedKind = errors.New("retrievr: kind does not support get-by-id")

	// ErrGetUnsupported is returned by Client.Get when the addressed source
	// has no per-record retrieval API — every web and news provider, and
	// most place/social ones. It is refused BEFORE dispatch, so it costs no
	// upstream call and no retry.
	//
	// Added in v2.26.0 (issue #1) so an embedding caller can CLASSIFY this
	// refusal instead of matching a message substring. Reach the content
	// through the search result's `url` field with a page-fetching tool;
	// retrievr deliberately does not fetch pages. Check
	// SourceInfo.SupportsGet from ListSources to know in advance.
	//
	// errors.Is also matches the historical internal ErrFormatUnsupported,
	// so the change is additive for anyone who was pattern-matching the old
	// message.
	ErrGetUnsupported = internal.ErrGetUnsupported
)
