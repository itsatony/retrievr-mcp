package internal

import (
	"encoding/json"
	"errors"
)

// ---------------------------------------------------------------------------
// Domain error message constants (spec section 11)
// ---------------------------------------------------------------------------

const (
	ErrMsgSourceNotFound      = "source not found"
	ErrMsgSourceDisabled      = "source is disabled"
	ErrMsgInvalidID           = "invalid publication id format"
	ErrMsgSearchFailed        = "search failed"
	ErrMsgGetFailed           = "retrieval failed"
	ErrMsgRateLimitExceeded   = "rate limit exceeded"
	ErrMsgUpstreamTimeout     = "upstream source timeout"
	ErrMsgInvalidDateFormat   = "invalid date format, expected YYYY-MM-DD or YYYY"
	ErrMsgAllSourcesFailed    = "all sources failed"
	ErrMsgFullTextUnavailable = "full text not available for this publication"
	ErrMsgFormatUnsupported   = "requested format not supported by this source"
	ErrMsgCredentialInvalid   = "provided credential was rejected by upstream source"
	ErrMsgCredentialRequired  = "this source requires credentials for the requested operation"
	ErrMsgCacheKeyGeneration  = "failed to generate cache key"
)

// ---------------------------------------------------------------------------
// Config / version error message constants
// ---------------------------------------------------------------------------

const (
	ErrMsgConfigLoad       = "failed to load config"
	ErrMsgConfigParse      = "failed to parse config"
	ErrMsgConfigValidation = "config validation failed"
	ErrMsgVersionLoad      = "failed to load version"
	ErrMsgDurationParse    = "failed to parse duration"
	ErrMsgUnknownError     = "unknown error"
)

// ---------------------------------------------------------------------------
// Sentinel errors — domain
// ---------------------------------------------------------------------------

var (
	ErrSourceNotFound      = errors.New(ErrMsgSourceNotFound)
	ErrSourceDisabled      = errors.New(ErrMsgSourceDisabled)
	ErrInvalidID           = errors.New(ErrMsgInvalidID)
	ErrSearchFailed        = errors.New(ErrMsgSearchFailed)
	ErrGetFailed           = errors.New(ErrMsgGetFailed)
	ErrRateLimitExceeded   = errors.New(ErrMsgRateLimitExceeded)
	ErrUpstreamTimeout     = errors.New(ErrMsgUpstreamTimeout)
	ErrInvalidDateFormat   = errors.New(ErrMsgInvalidDateFormat)
	ErrAllSourcesFailed    = errors.New(ErrMsgAllSourcesFailed)
	ErrFullTextUnavailable = errors.New(ErrMsgFullTextUnavailable)
	ErrFormatUnsupported   = errors.New(ErrMsgFormatUnsupported)
	ErrCredentialInvalid   = errors.New(ErrMsgCredentialInvalid)
	ErrCredentialRequired  = errors.New(ErrMsgCredentialRequired)
	ErrCacheKeyGeneration  = errors.New(ErrMsgCacheKeyGeneration)
)

// ---------------------------------------------------------------------------
// Sentinel errors — config / version
// ---------------------------------------------------------------------------

var (
	ErrConfigLoad       = errors.New(ErrMsgConfigLoad)
	ErrConfigParse      = errors.New(ErrMsgConfigParse)
	ErrConfigValidation = errors.New(ErrMsgConfigValidation)
	ErrVersionLoad      = errors.New(ErrMsgVersionLoad)
	ErrDurationParse    = errors.New(ErrMsgDurationParse)
)

// ---------------------------------------------------------------------------
// Structured MCP error response
// ---------------------------------------------------------------------------

// MCPError is the structured error format returned in MCP tool responses.
type MCPError struct {
	Error  string `json:"error"`
	Source string `json:"source,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// NewMCPError creates a JSON-encoded MCP error string.
func NewMCPError(msg, source, detail string) string {
	e := MCPError{
		Error:  msg,
		Source: source,
		Detail: detail,
	}
	b, _ := json.Marshal(e) //nolint:errchkjson // MCPError fields are always serializable
	return string(b)
}

// NewMCPErrorFromErr creates a JSON-encoded MCP error string from a Go error.
func NewMCPErrorFromErr(err error, source string) string {
	if err == nil {
		return NewMCPError(ErrMsgUnknownError, source, "")
	}

	// Unwrap to find the root sentinel error for the message field.
	// Use the full error string (with wrapping context) as the detail.
	rootErr := err
	for {
		unwrapped := errors.Unwrap(rootErr)
		if unwrapped == nil {
			break
		}
		rootErr = unwrapped
	}

	detail := ""
	if err.Error() != rootErr.Error() {
		detail = err.Error()
	}

	return NewMCPError(rootErr.Error(), source, detail)
}
