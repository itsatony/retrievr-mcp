package internal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Request ID constants
// ---------------------------------------------------------------------------

const (
	// requestIDBytes is the number of random bytes used to generate a request ID.
	// 16 bytes = 32 hex characters.
	requestIDBytes = 16

	// requestIDFallbackPrefix is used when crypto/rand fails (extremely rare).
	requestIDFallbackPrefix = "fallback-"
)

// ---------------------------------------------------------------------------
// Context key
// ---------------------------------------------------------------------------

// contextKey is an unexported type used for context value keys to avoid collisions.
type contextKey string

const contextKeyRequestID contextKey = "request_id"

// ---------------------------------------------------------------------------
// Request ID generation
// ---------------------------------------------------------------------------

// GenerateRequestID returns a 32-character hex string generated from
// cryptographically random bytes. On the rare chance that crypto/rand fails,
// it falls back to a time-based identifier.
func GenerateRequestID() string {
	b := make([]byte, requestIDBytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s%d", requestIDFallbackPrefix, time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Context helpers
// ---------------------------------------------------------------------------

// WithRequestID returns a new context that carries the given request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKeyRequestID, id)
}

// RequestIDFromContext extracts the request ID from the context.
// Returns an empty string if no request ID is present or the value
// is not a string.
func RequestIDFromContext(ctx context.Context) string {
	v, ok := ctx.Value(contextKeyRequestID).(string)
	if !ok {
		return ""
	}
	return v
}
