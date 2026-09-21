package internal

import (
	"context"
	"errors"
)

// ---------------------------------------------------------------------------
// Get-unsupported: the typed refusal
//
// A plugin whose source has no per-record retrieval API (every web, news and
// most place/social provider) refuses Get. Before v2.26.0 that refusal wrapped
// ErrFormatUnsupported, which conflates two different facts: "the FORMAT you
// asked for is unavailable here" and "this source has no get-by-id AT ALL".
// Only the second is a property of the source rather than of the request, and
// only the second is what a caller must know before it spends a call.
//
// GetUnsupportedError answers to BOTH sentinels on purpose. ErrGetUnsupported
// is the precise one and is re-exported from pkg/retrievr so an embedding
// caller can classify the refusal instead of matching a string;
// ErrFormatUnsupported keeps every pre-existing matcher in this tree working.
// ---------------------------------------------------------------------------

// GetUnsupportedError is the refusal returned when a source cannot retrieve a
// single record by id. Detail names the source and, where useful, what to do
// instead (the result's url).
type GetUnsupportedError struct {
	// Detail is the source-specific explanation, e.g.
	// "linkup has no per-result Get API".
	Detail string
}

// Error implements error.
func (e *GetUnsupportedError) Error() string {
	if e.Detail == "" {
		return ErrMsgGetUnsupported
	}
	return ErrMsgGetUnsupported + ": " + e.Detail
}

// Is makes errors.Is match both the precise ErrGetUnsupported sentinel and
// the historical ErrFormatUnsupported one. Dropping the second would silently
// change what ~50 existing call sites and tests classify.
func (e *GetUnsupportedError) Is(target error) bool {
	return target == ErrGetUnsupported || target == ErrFormatUnsupported
}

// NewGetUnsupportedError builds the refusal for a source with no get-by-id.
func NewGetUnsupportedError(detail string) error {
	return &GetUnsupportedError{Detail: detail}
}

// ---------------------------------------------------------------------------
// Permanent vs transient
//
// withRetry's default predicate used to treat EVERY non-context error as
// transient, so a refusal whose entire meaning is "this can never work" cost
// three attempts and two backoff sleeps before it surfaced.
//
// The rule applied below: an error is PERMANENT when it is a statement about
// the REQUEST or about the SOURCE'S CONTRACT — neither of which changes
// between attempts of the same call. An error that is a statement about the
// upstream's ANSWER or about the TRANSPORT stays transient, including every
// *NotFound (several are derived from an empty result list, which a degraded
// upstream can also produce), every *HTTPRequest, every *JSONParse/*XMLParse,
// ErrRateLimitExceeded (429 — backoff is exactly the right response) and
// ErrUpstreamTimeout.
//
// ErrSearchFailed and ErrGetFailed are deliberately absent: they are WRAPPERS
// that plugins put around transport failures, so classifying either permanent
// would disable retry for the whole tree.
// ---------------------------------------------------------------------------

// permanentSentinels is the closed set of errors withRetry must not retry.
// Each entry is a statement about the request or the source contract.
var permanentSentinels = []error{
	// Source contract — the operation does not exist here, at any time.
	ErrGetUnsupported,
	ErrFormatUnsupported,
	ErrBibTeXUnsupported,
	ErrFullTextUnavailable,

	// Routing / configuration facts, fixed for the lifetime of the call.
	ErrSourceNotFound,
	ErrSourceDisabled,
	ErrEUModeProviderConflict,
	ErrCompatV1Sunset,

	// Credentials. Absent stays absent; a key rejected with 401/403 is
	// re-sent byte-identically on the next attempt.
	ErrCredentialRequired,
	ErrCredentialInvalid,

	// Request validation — the same malformed request would be re-sent.
	ErrInvalidID,
	ErrInvalidInput,
	ErrInvalidDateFormat,
	ErrInvalidPublishedAt,
	ErrInvalidLanguageTag,
	ErrInvalidDomainList,
	ErrTooManyChannels,
	ErrTooManySubreddits,
	ErrBiorxivDateRequired,

	// Per-plugin request validation (empty query).
	ErrArxivEmptyQuery,
	ErrS2EmptyQuery,
	ErrOAEmptyQuery,
	ErrPubMedEmptyQuery,
	ErrEuropePMCEmptyQuery,
	ErrHFEmptyQuery,
	ErrCrossRefEmptyQuery,
	ErrDBLPEmptyQuery,
	ErrADSEmptyQuery,
}

// IsPermanentError reports whether err is one a retry cannot fix. Context
// cancellation and deadline expiry are permanent too — they reflect caller
// intent, and withRetry has always honored them.
func IsPermanentError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	for _, sentinel := range permanentSentinels {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}
