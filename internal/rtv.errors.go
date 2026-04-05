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
	ErrMsgBibTeXGeneration    = "failed to generate bibtex"
)

// ---------------------------------------------------------------------------
// Server error message constants
// ---------------------------------------------------------------------------

const (
	ErrMsgServerStart    = "server start failed"
	ErrMsgServerShutdown = "server shutdown failed"
	ErrMsgInvalidInput   = "invalid tool input"
	ErrMsgJSONMarshal    = "failed to marshal response"
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
	ErrBibTeXGeneration    = errors.New(ErrMsgBibTeXGeneration)
)

// ---------------------------------------------------------------------------
// Sentinel errors — server
// ---------------------------------------------------------------------------

var (
	ErrServerStart    = errors.New(ErrMsgServerStart)
	ErrServerShutdown = errors.New(ErrMsgServerShutdown)
	ErrInvalidInput   = errors.New(ErrMsgInvalidInput)
	ErrJSONMarshal    = errors.New(ErrMsgJSONMarshal)
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
// Sentinel errors — plugin: ArXiv
// ---------------------------------------------------------------------------

const (
	ErrMsgArxivXMLParse    = "failed to parse arxiv xml response"
	ErrMsgArxivHTTPRequest = "arxiv http request failed"
	ErrMsgArxivNotFound    = "arxiv entry not found"
	ErrMsgArxivEmptyQuery  = "search query is empty"
)

var (
	ErrArxivXMLParse    = errors.New(ErrMsgArxivXMLParse)
	ErrArxivHTTPRequest = errors.New(ErrMsgArxivHTTPRequest)
	ErrArxivNotFound    = errors.New(ErrMsgArxivNotFound)
	ErrArxivEmptyQuery  = errors.New(ErrMsgArxivEmptyQuery)
)

// ---------------------------------------------------------------------------
// Sentinel errors — plugin: S2 (Semantic Scholar)
// ---------------------------------------------------------------------------

const (
	ErrMsgS2JSONParse   = "failed to parse s2 json response"
	ErrMsgS2HTTPRequest = "s2 http request failed"
	ErrMsgS2NotFound    = "s2 paper not found"
	ErrMsgS2EmptyQuery  = "search query is empty"
)

var (
	ErrS2JSONParse   = errors.New(ErrMsgS2JSONParse)
	ErrS2HTTPRequest = errors.New(ErrMsgS2HTTPRequest)
	ErrS2NotFound    = errors.New(ErrMsgS2NotFound)
	ErrS2EmptyQuery  = errors.New(ErrMsgS2EmptyQuery)
)

// ---------------------------------------------------------------------------
// Sentinel errors — plugin: OpenAlex
// ---------------------------------------------------------------------------

const (
	ErrMsgOAJSONParse   = "failed to parse openalex json response"
	ErrMsgOAHTTPRequest = "openalex http request failed"
	ErrMsgOANotFound    = "openalex work not found"
	ErrMsgOAEmptyQuery  = "search query is empty"
)

var (
	ErrOAJSONParse   = errors.New(ErrMsgOAJSONParse)
	ErrOAHTTPRequest = errors.New(ErrMsgOAHTTPRequest)
	ErrOANotFound    = errors.New(ErrMsgOANotFound)
	ErrOAEmptyQuery  = errors.New(ErrMsgOAEmptyQuery)
)

// ---------------------------------------------------------------------------
// Sentinel errors — plugin: PubMed
// ---------------------------------------------------------------------------

const (
	ErrMsgPubMedXMLParse    = "failed to parse pubmed xml response"
	ErrMsgPubMedHTTPRequest = "pubmed http request failed"
	ErrMsgPubMedNotFound    = "pubmed article not found"
	ErrMsgPubMedEmptyQuery  = "search query is empty"
)

var (
	ErrPubMedXMLParse    = errors.New(ErrMsgPubMedXMLParse)
	ErrPubMedHTTPRequest = errors.New(ErrMsgPubMedHTTPRequest)
	ErrPubMedNotFound    = errors.New(ErrMsgPubMedNotFound)
	ErrPubMedEmptyQuery  = errors.New(ErrMsgPubMedEmptyQuery)
)

// ---------------------------------------------------------------------------
// Sentinel errors — plugin: EuropePMC
// ---------------------------------------------------------------------------

const (
	ErrMsgEuropePMCJSONParse   = "failed to parse europepmc json response"
	ErrMsgEuropePMCHTTPRequest = "europepmc http request failed"
	ErrMsgEuropePMCNotFound    = "europepmc article not found"
	ErrMsgEuropePMCEmptyQuery  = "search query is empty"
)

var (
	ErrEuropePMCJSONParse   = errors.New(ErrMsgEuropePMCJSONParse)
	ErrEuropePMCHTTPRequest = errors.New(ErrMsgEuropePMCHTTPRequest)
	ErrEuropePMCNotFound    = errors.New(ErrMsgEuropePMCNotFound)
	ErrEuropePMCEmptyQuery  = errors.New(ErrMsgEuropePMCEmptyQuery)
)

// ---------------------------------------------------------------------------
// Sentinel errors — plugin: HuggingFace
// ---------------------------------------------------------------------------

const (
	ErrMsgHFJSONParse   = "failed to parse huggingface json response"
	ErrMsgHFHTTPRequest = "huggingface http request failed"
	ErrMsgHFNotFound    = "huggingface item not found"
	ErrMsgHFEmptyQuery  = "search query is empty"
)

var (
	ErrHFJSONParse   = errors.New(ErrMsgHFJSONParse)
	ErrHFHTTPRequest = errors.New(ErrMsgHFHTTPRequest)
	ErrHFNotFound    = errors.New(ErrMsgHFNotFound)
	ErrHFEmptyQuery  = errors.New(ErrMsgHFEmptyQuery)
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
