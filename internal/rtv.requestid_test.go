package internal

import (
	"context"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

const (
	expectedRequestIDLength = requestIDBytes * 2 // 32 hex chars
	requestIDUniquenessN    = 100
	requestIDConcurrencyN   = 50
)

// ---------------------------------------------------------------------------
// GenerateRequestID tests
// ---------------------------------------------------------------------------

func TestGenerateRequestIDLength(t *testing.T) {
	t.Parallel()

	id := GenerateRequestID()
	assert.Len(t, id, expectedRequestIDLength)
}

func TestGenerateRequestIDValidHex(t *testing.T) {
	t.Parallel()

	id := GenerateRequestID()
	_, err := hex.DecodeString(id)
	assert.NoError(t, err, "request ID must be valid hex")
}

func TestGenerateRequestIDUniqueness(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool, requestIDUniquenessN)
	for range requestIDUniquenessN {
		id := GenerateRequestID()
		require.False(t, seen[id], "duplicate request ID: %s", id)
		seen[id] = true
	}
}

func TestGenerateRequestIDConcurrent(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	seen := make(map[string]bool, requestIDConcurrencyN)
	var wg sync.WaitGroup
	wg.Add(requestIDConcurrencyN)

	for range requestIDConcurrencyN {
		go func() {
			defer wg.Done()
			id := GenerateRequestID()

			mu.Lock()
			defer mu.Unlock()
			seen[id] = true
		}()
	}

	wg.Wait()
	assert.Len(t, seen, requestIDConcurrencyN, "all generated IDs should be unique")
}

// ---------------------------------------------------------------------------
// Context helper tests
// ---------------------------------------------------------------------------

func TestWithRequestIDRoundTrip(t *testing.T) {
	t.Parallel()

	id := GenerateRequestID()
	ctx := WithRequestID(context.Background(), id)
	got := RequestIDFromContext(ctx)
	assert.Equal(t, id, got)
}

func TestRequestIDFromContextEmpty(t *testing.T) {
	t.Parallel()

	got := RequestIDFromContext(context.Background())
	assert.Empty(t, got, "empty context should return empty string")
}

func TestRequestIDFromContextWrongType(t *testing.T) {
	t.Parallel()

	// Simulate a wrong value type stored under the same key.
	//nolint:staticcheck // deliberately using wrong type for test
	ctx := context.WithValue(context.Background(), contextKeyRequestID, 12345)
	got := RequestIDFromContext(ctx)
	assert.Empty(t, got, "non-string value should return empty string")
}
