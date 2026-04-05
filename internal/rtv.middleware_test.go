package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// RequestIDContextFunc tests
// ---------------------------------------------------------------------------

func TestRequestIDContextFunc(t *testing.T) {
	t.Parallel()
	fn := RequestIDContextFunc()
	require.NotNil(t, fn)

	ctx := fn(context.Background(), &http.Request{})
	requestID := RequestIDFromContext(ctx)
	assert.NotEmpty(t, requestID, "request ID should be injected into context")
	assert.Len(t, requestID, requestIDBytes*2, "request ID should be 32 hex chars")
}

func TestRequestIDContextFunc_UniquePerCall(t *testing.T) {
	t.Parallel()
	fn := RequestIDContextFunc()

	ctx1 := fn(context.Background(), &http.Request{})
	ctx2 := fn(context.Background(), &http.Request{})

	id1 := RequestIDFromContext(ctx1)
	id2 := RequestIDFromContext(ctx2)
	assert.NotEqual(t, id1, id2, "each call should produce a unique request ID")
}

// ---------------------------------------------------------------------------
// ToolLoggingMiddleware tests
// ---------------------------------------------------------------------------

func TestToolLoggingMiddleware_Success(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	middleware := ToolLoggingMiddleware(logger)
	require.NotNil(t, middleware)

	// Create a handler that returns success.
	inner := func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}

	wrapped := middleware(inner)

	ctx := WithRequestID(context.Background(), "test-request-id-123")
	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameSearch

	result, err := wrapped(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	// Verify log output contains expected fields.
	output := buf.String()
	assert.Contains(t, output, logMsgToolCall)
	assert.Contains(t, output, logMsgToolComplete)
	assert.Contains(t, output, "test-request-id-123")
	assert.Contains(t, output, ToolNameSearch)
	assert.Contains(t, output, LogKeyDuration)
	assert.NotContains(t, output, logMsgToolError, "should not log error on success")
}

func TestToolLoggingMiddleware_ErrorResult(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	middleware := ToolLoggingMiddleware(logger)

	// Create a handler that returns an error result (isError=true).
	inner := func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultError("something failed"), nil
	}

	wrapped := middleware(inner)

	ctx := WithRequestID(context.Background(), "test-error-req")
	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameGet

	result, err := wrapped(ctx, req)
	require.NoError(t, err, "Go error should be nil; error is in the result")
	require.NotNil(t, result)
	assert.True(t, result.IsError)

	output := buf.String()
	assert.Contains(t, output, logMsgToolCall)
	assert.Contains(t, output, logMsgToolError)
	assert.Contains(t, output, ToolNameGet)
}

func TestToolLoggingMiddleware_GoError(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	middleware := ToolLoggingMiddleware(logger)

	// Create a handler that returns a Go error.
	inner := func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, ErrServerStart
	}

	wrapped := middleware(inner)

	ctx := WithRequestID(context.Background(), "test-go-err")
	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameListSources

	result, err := wrapped(ctx, req)
	require.Error(t, err)
	require.Nil(t, result)

	output := buf.String()
	assert.Contains(t, output, logMsgToolError)
	assert.Contains(t, output, ErrMsgServerStart)
}

func TestToolLoggingMiddleware_LogStructure(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	middleware := ToolLoggingMiddleware(logger)
	inner := server.ToolHandlerFunc(func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("done"), nil
	})

	wrapped := middleware(inner)
	ctx := WithRequestID(context.Background(), "struct-test-id")
	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameSearch

	_, _ = wrapped(ctx, req)

	// Parse each JSON line and verify structure of the completion log.
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	require.GreaterOrEqual(t, len(lines), 2, "expected at least start + complete log lines")

	var completionLog map[string]any
	err := json.Unmarshal(lines[len(lines)-1], &completionLog)
	require.NoError(t, err)

	assert.Equal(t, logMsgToolComplete, completionLog["msg"])
	assert.Equal(t, "struct-test-id", completionLog[LogKeyRequestID])
	assert.Equal(t, ToolNameSearch, completionLog[LogKeyTool])
	assert.Contains(t, completionLog, LogKeyDuration)
}
