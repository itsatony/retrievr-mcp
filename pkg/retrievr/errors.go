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

	// ErrAllProvidersFailed was declared for "every provider failed" but
	// nothing ever returned it — the router returns ErrAllSourcesFailed.
	//
	// Deprecated: match ErrAllSourcesFailed (or *AllSourcesFailedError).
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

	// ErrQuotaExhausted is a provider refusal because a quota that does not
	// recover within a retry window is spent — YouTube Data API v3's
	// 10,000 units/day (search.list = 100 units) is the first. It is not
	// retried. A quota error ALSO matches ErrRateLimitExceeded (so older
	// matchers keep working): test ErrQuotaExhausted FIRST to tell a spent
	// daily quota from a short-window throttle. v2.28.0.
	ErrQuotaExhausted = internal.ErrQuotaExhausted

	// ErrRateLimitExceeded is a short-window throttle (HTTP 429, YouTube
	// rateLimitExceeded). Retried with backoff. v2.28.0 re-export.
	ErrRateLimitExceeded = internal.ErrRateLimitExceeded

	// ErrCredentialRequired — the source needs a key and none was
	// configured or supplied. v2.28.0 re-export.
	ErrCredentialRequired = internal.ErrCredentialRequired

	// ErrCredentialInvalid — the upstream rejected the key. v2.28.0 re-export.
	ErrCredentialInvalid = internal.ErrCredentialInvalid

	// ErrAllSourcesFailed is what Search / SearchV2 return when every
	// queried source failed. The returned error is an *AllSourcesFailedError
	// whose errors.Is also reaches each per-source cause, so
	// errors.Is(err, ErrQuotaExhausted) answers "did the quota run out?"
	// without parsing a message. v2.28.0 re-export.
	ErrAllSourcesFailed = internal.ErrAllSourcesFailed
)

// AllSourcesFailedError carries the per-source failures of a search in
// which every source failed. v2.28.0.
type AllSourcesFailedError = internal.AllSourcesFailedError

// QuotaExhaustedError is the typed form of ErrQuotaExhausted. v2.28.0.
type QuotaExhaustedError = internal.QuotaExhaustedError

// SourceError reports why one source failed (MergedSearchResult.SourceErrors).
type SourceError = internal.SourceError

// SourceErrorCode is the closed-set classification of a per-source failure.
type SourceErrorCode = internal.SourceErrorCode

// SourceErrorCode values.
const (
	SourceErrQuotaExhausted     = internal.SourceErrQuotaExhausted
	SourceErrRateLimited        = internal.SourceErrRateLimited
	SourceErrCredentialRequired = internal.SourceErrCredentialRequired
	SourceErrCredentialInvalid  = internal.SourceErrCredentialInvalid
	SourceErrTimeout            = internal.SourceErrTimeout
	SourceErrCanceled           = internal.SourceErrCanceled
	SourceErrInvalidRequest     = internal.SourceErrInvalidRequest
	SourceErrUnsupported        = internal.SourceErrUnsupported
	SourceErrUpstream           = internal.SourceErrUpstream
)

// ClassifySourceError maps any per-source error onto its SourceErrorCode.
func ClassifySourceError(err error) SourceErrorCode { return internal.ClassifySourceError(err) }
