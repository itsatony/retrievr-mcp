package retrievr

import "errors"

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
	ErrUnsupportedKind = errors.New("retrievr: kind does not support get-by-id")
)
