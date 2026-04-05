package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Sentinel error message consistency
// ---------------------------------------------------------------------------

func TestSentinelErrorMessages(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		message string
	}{
		{"SourceNotFound", ErrSourceNotFound, ErrMsgSourceNotFound},
		{"SourceDisabled", ErrSourceDisabled, ErrMsgSourceDisabled},
		{"InvalidID", ErrInvalidID, ErrMsgInvalidID},
		{"SearchFailed", ErrSearchFailed, ErrMsgSearchFailed},
		{"GetFailed", ErrGetFailed, ErrMsgGetFailed},
		{"RateLimitExceeded", ErrRateLimitExceeded, ErrMsgRateLimitExceeded},
		{"UpstreamTimeout", ErrUpstreamTimeout, ErrMsgUpstreamTimeout},
		{"InvalidDateFormat", ErrInvalidDateFormat, ErrMsgInvalidDateFormat},
		{"AllSourcesFailed", ErrAllSourcesFailed, ErrMsgAllSourcesFailed},
		{"FullTextUnavailable", ErrFullTextUnavailable, ErrMsgFullTextUnavailable},
		{"FormatUnsupported", ErrFormatUnsupported, ErrMsgFormatUnsupported},
		{"CredentialInvalid", ErrCredentialInvalid, ErrMsgCredentialInvalid},
		{"CredentialRequired", ErrCredentialRequired, ErrMsgCredentialRequired},
		{"ConfigLoad", ErrConfigLoad, ErrMsgConfigLoad},
		{"ConfigParse", ErrConfigParse, ErrMsgConfigParse},
		{"ConfigValidation", ErrConfigValidation, ErrMsgConfigValidation},
		{"VersionLoad", ErrVersionLoad, ErrMsgVersionLoad},
		{"DurationParse", ErrDurationParse, ErrMsgDurationParse},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.message, tt.err.Error())
		})
	}
}

// ---------------------------------------------------------------------------
// Error wrapping with errors.Is
// ---------------------------------------------------------------------------

func TestErrorWrapping(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{"SourceNotFound", ErrSourceNotFound},
		{"SearchFailed", ErrSearchFailed},
		{"ConfigLoad", ErrConfigLoad},
		{"RateLimitExceeded", ErrRateLimitExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := fmt.Errorf("outer context: %w", tt.sentinel)
			assert.True(t, errors.Is(wrapped, tt.sentinel))
			assert.Contains(t, wrapped.Error(), tt.sentinel.Error())
		})
	}
}

func TestDoubleWrapping(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	mid := fmt.Errorf("%w: arxiv: %w", ErrSearchFailed, inner)
	outer := fmt.Errorf("router: %w", mid)

	assert.True(t, errors.Is(outer, ErrSearchFailed))
	assert.Contains(t, outer.Error(), "connection refused")
	assert.Contains(t, outer.Error(), ErrMsgSearchFailed)
}

// ---------------------------------------------------------------------------
// MCPError JSON tests
// ---------------------------------------------------------------------------

func TestNewMCPError(t *testing.T) {
	tests := []struct {
		name           string
		msg            string
		source         string
		detail         string
		expectedFields map[string]string
	}{
		{
			name:   "full error",
			msg:    ErrMsgSearchFailed,
			source: SourceArXiv,
			detail: "upstream timeout after 10s",
			expectedFields: map[string]string{
				"error":  ErrMsgSearchFailed,
				"source": SourceArXiv,
				"detail": "upstream timeout after 10s",
			},
		},
		{
			name:   "no detail",
			msg:    ErrMsgSourceNotFound,
			source: "unknown",
			detail: "",
			expectedFields: map[string]string{
				"error":  ErrMsgSourceNotFound,
				"source": "unknown",
			},
		},
		{
			name:   "no source",
			msg:    ErrMsgAllSourcesFailed,
			source: "",
			detail: "all 3 sources timed out",
			expectedFields: map[string]string{
				"error":  ErrMsgAllSourcesFailed,
				"detail": "all 3 sources timed out",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewMCPError(tt.msg, tt.source, tt.detail)

			// Verify it's valid JSON.
			var parsed MCPError
			err := json.Unmarshal([]byte(result), &parsed)
			require.NoError(t, err)

			assert.Equal(t, tt.msg, parsed.Error)
			assert.Equal(t, tt.source, parsed.Source)
			assert.Equal(t, tt.detail, parsed.Detail)
		})
	}
}

func TestNewMCPErrorFromErr(t *testing.T) {
	t.Run("simple_error", func(t *testing.T) {
		result := NewMCPErrorFromErr(ErrSourceNotFound, SourceArXiv)

		var parsed MCPError
		err := json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)

		assert.Equal(t, ErrMsgSourceNotFound, parsed.Error)
		assert.Equal(t, SourceArXiv, parsed.Source)
		assert.Empty(t, parsed.Detail)
	})

	t.Run("wrapped_error", func(t *testing.T) {
		wrapped := fmt.Errorf("arxiv plugin: %w", ErrSearchFailed)
		result := NewMCPErrorFromErr(wrapped, SourceArXiv)

		var parsed MCPError
		err := json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)

		assert.Equal(t, ErrMsgSearchFailed, parsed.Error)
		assert.Equal(t, SourceArXiv, parsed.Source)
		assert.Contains(t, parsed.Detail, "arxiv plugin")
	})

	t.Run("nil_error", func(t *testing.T) {
		result := NewMCPErrorFromErr(nil, SourceS2)

		var parsed MCPError
		err := json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)

		assert.Equal(t, ErrMsgUnknownError, parsed.Error)
	})

	t.Run("deeply_wrapped", func(t *testing.T) {
		inner := fmt.Errorf("connection refused")
		mid := fmt.Errorf("%w: %w", ErrUpstreamTimeout, inner)
		outer := fmt.Errorf("s2 plugin: %w", mid)

		result := NewMCPErrorFromErr(outer, SourceS2)

		var parsed MCPError
		err := json.Unmarshal([]byte(result), &parsed)
		require.NoError(t, err)

		// The deepest unwrap finds "connection refused" since it's a plain error.
		// The detail contains the full chain.
		assert.NotEmpty(t, parsed.Error)
		assert.Equal(t, SourceS2, parsed.Source)
	})
}
