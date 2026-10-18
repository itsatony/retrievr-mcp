package internal

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Per-source failure classification — v2.28.0
//
// Before v2.28.0 a failed source reached the caller as a bare id in
// SourcesFailed, and a search whose every source failed returned
// "all sources failed: queried 1 sources" — the cause was logged and then
// dropped. For a YouTube-only search with an exhausted daily quota that read
// exactly like an upstream outage, and the 403 was retried three times
// although the quota resets at midnight Pacific, not within a backoff.
//
// Now every failed source carries a SourceError with a closed-set Code, and
// the all-failed error carries the per-source causes so errors.Is reaches
// them (errors.Is(err, ErrQuotaExhausted) is true for a YouTube-only search
// whose key ran out).
// ---------------------------------------------------------------------------

// ErrMsgQuotaExhausted is the message of ErrQuotaExhausted.
const ErrMsgQuotaExhausted = "upstream quota exhausted"

// ErrQuotaExhausted is returned when a provider refuses because a quota that
// does NOT recover within a retry window is spent (YouTube Data API v3:
// 10,000 units/day per key, search.list = 100 units). It is permanent for the
// call — retrying cannot succeed before the quota resets.
//
// It is distinct from ErrRateLimitExceeded (a short-window throttle that a
// backoff does fix). A QuotaExhaustedError ALSO matches ErrRateLimitExceeded,
// so every matcher written before v2.28.0 keeps classifying it as a
// rate-limit; match ErrQuotaExhausted FIRST to tell the two apart.
var ErrQuotaExhausted = errors.New(ErrMsgQuotaExhausted)

// QuotaExhaustedError is the typed form of ErrQuotaExhausted.
type QuotaExhaustedError struct {
	// Source is the source ID whose quota is spent.
	Source string
	// Reason is the upstream's own reason code (e.g. "quotaExceeded").
	Reason string
}

// Error implements error.
func (e *QuotaExhaustedError) Error() string {
	return fmt.Sprintf("%s: %s reason=%s", ErrMsgQuotaExhausted, e.Source, e.Reason)
}

// Is matches ErrQuotaExhausted and, for backward compatibility,
// ErrRateLimitExceeded.
func (e *QuotaExhaustedError) Is(target error) bool {
	return target == ErrQuotaExhausted || target == ErrRateLimitExceeded
}

// SourceErrorCode is the closed-set classification of a per-source failure.
type SourceErrorCode string

// SourceErrorCode values.
const (
	// SourceErrQuotaExhausted — a daily/period quota is spent; retrying
	// before it resets cannot succeed.
	SourceErrQuotaExhausted SourceErrorCode = "quota_exhausted"
	// SourceErrRateLimited — a short-window throttle; retry after a backoff.
	SourceErrRateLimited SourceErrorCode = "rate_limited"
	// SourceErrCredentialRequired — no key was configured or supplied.
	SourceErrCredentialRequired SourceErrorCode = "credential_required"
	// SourceErrCredentialInvalid — the upstream rejected the key.
	SourceErrCredentialInvalid SourceErrorCode = "credential_invalid"
	// SourceErrTimeout — the upstream did not answer in time.
	SourceErrTimeout SourceErrorCode = "timeout"
	// SourceErrCanceled — the caller cancelled the request.
	SourceErrCanceled SourceErrorCode = "canceled"
	// SourceErrInvalidRequest — the request was malformed for this source.
	SourceErrInvalidRequest SourceErrorCode = "invalid_request"
	// SourceErrUnsupported — the operation does not exist for this source.
	SourceErrUnsupported SourceErrorCode = "unsupported"
	// SourceErrUpstream — any other upstream or transport failure.
	SourceErrUpstream SourceErrorCode = "upstream_error"
)

// SourceError reports why one source failed in a search.
type SourceError struct {
	Source  string          `json:"source"`
	Code    SourceErrorCode `json:"code"`
	Message string          `json:"message"`
	// Retryable is false when repeating the same call cannot succeed
	// (quota exhausted, bad key, malformed request, unsupported operation).
	Retryable bool `json:"retryable"`
}

// invalidRequestSentinels classify as SourceErrInvalidRequest.
var invalidRequestSentinels = []error{
	ErrInvalidID, ErrInvalidInput, ErrInvalidDateFormat, ErrInvalidPublishedAt,
	ErrInvalidLanguageTag, ErrInvalidDomainList, ErrTooManyChannels,
	ErrTooManySubreddits,
}

// ClassifySourceError maps a per-source failure onto its SourceErrorCode.
// The order is load-bearing: a QuotaExhaustedError also matches
// ErrRateLimitExceeded, so quota is tested first.
func ClassifySourceError(err error) SourceErrorCode {
	switch {
	case errors.Is(err, ErrQuotaExhausted):
		return SourceErrQuotaExhausted
	case errors.Is(err, ErrRateLimitExceeded):
		return SourceErrRateLimited
	case errors.Is(err, ErrCredentialRequired):
		return SourceErrCredentialRequired
	case errors.Is(err, ErrCredentialInvalid):
		return SourceErrCredentialInvalid
	case errors.Is(err, context.Canceled):
		return SourceErrCanceled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrUpstreamTimeout):
		return SourceErrTimeout
	case errors.Is(err, ErrGetUnsupported), errors.Is(err, ErrFormatUnsupported),
		errors.Is(err, ErrBibTeXUnsupported):
		return SourceErrUnsupported
	}
	for _, sentinel := range invalidRequestSentinels {
		if errors.Is(err, sentinel) {
			return SourceErrInvalidRequest
		}
	}
	return SourceErrUpstream
}

// newSourceError builds the wire report for one failed source. The message
// is sanitized exactly like SourceHealth.LastError (no response bodies, no
// query strings — a key can ride in either).
func newSourceError(sourceID string, err error) SourceError {
	return SourceError{
		Source:    sourceID,
		Code:      ClassifySourceError(err),
		Message:   sanitizeHealthError(err),
		Retryable: !IsPermanentError(err),
	}
}

// AllSourcesFailedError is returned by Router.Search when every queried
// source failed. It matches ErrAllSourcesFailed, and errors.Is also reaches
// every per-source cause — so a caller can ask errors.Is(err,
// ErrQuotaExhausted) without parsing the message.
type AllSourcesFailedError struct {
	// Queried is how many sources were queried (primary + fallback).
	Queried int
	// Failures is the per-source report, in query order.
	Failures []SourceError

	causes []error
}

// Error implements error. The prefix is the pre-v2.28.0 message unchanged;
// the per-source codes are appended.
func (e *AllSourcesFailedError) Error() string {
	msg := fmt.Sprintf("%s: "+errDetailAllSourcesFailed, ErrMsgAllSourcesFailed, e.Queried)
	if len(e.Failures) == 0 {
		return msg
	}
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		parts = append(parts, f.Source+"="+string(f.Code))
	}
	return msg + " (" + strings.Join(parts, ", ") + ")"
}

// Unwrap returns ErrAllSourcesFailed, so the MCP error envelope keeps
// "all sources failed" as its message.
func (e *AllSourcesFailedError) Unwrap() error { return ErrAllSourcesFailed }

// Is reports whether any per-source cause matches target.
func (e *AllSourcesFailedError) Is(target error) bool {
	for _, c := range e.causes {
		if errors.Is(c, target) {
			return true
		}
	}
	return false
}

// newAllSourcesFailedError collects the failed sourceResults into the typed
// all-failed error.
func newAllSourcesFailedError(queried int, results []sourceResult) error {
	e := &AllSourcesFailedError{Queried: queried}
	for _, sr := range results {
		if sr.err == nil {
			continue
		}
		e.Failures = append(e.Failures, newSourceError(sr.sourceID, sr.err))
		e.causes = append(e.causes, sr.err)
	}
	return e
}
