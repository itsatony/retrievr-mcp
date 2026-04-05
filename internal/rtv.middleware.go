package internal

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---------------------------------------------------------------------------
// Middleware log message constants
// ---------------------------------------------------------------------------

const (
	logMsgToolCall     = "tool call"
	logMsgToolComplete = "tool call complete"
	logMsgToolError    = "tool call error"
)

// ---------------------------------------------------------------------------
// Request ID context injection
// ---------------------------------------------------------------------------

// RequestIDContextFunc returns an HTTPContextFunc that generates a unique
// request ID for each incoming HTTP request and injects it into the context
// via WithRequestID. This is registered on the StreamableHTTPServer via
// server.WithHTTPContextFunc().
func RequestIDContextFunc() server.HTTPContextFunc {
	return func(ctx context.Context, _ *http.Request) context.Context {
		return WithRequestID(ctx, GenerateRequestID())
	}
}

// ---------------------------------------------------------------------------
// Tool logging middleware
// ---------------------------------------------------------------------------

// ToolLoggingMiddleware returns a ToolHandlerMiddleware that logs every tool
// call with structured slog output: tool name, request ID, duration, and
// whether the result was an error.
func ToolLoggingMiddleware(logger *slog.Logger) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			requestID := RequestIDFromContext(ctx)
			toolName := req.Params.Name

			logger.Info(logMsgToolCall,
				slog.String(LogKeyRequestID, requestID),
				slog.String(LogKeyTool, toolName),
			)

			result, err := next(ctx, req)

			duration := time.Since(start)

			if err != nil || (result != nil && result.IsError) {
				errMsg := ""
				if err != nil {
					errMsg = err.Error()
				} else if result != nil && len(result.Content) > 0 {
					if tc, ok := result.Content[0].(mcp.TextContent); ok {
						errMsg = tc.Text
					}
				}
				logger.Warn(logMsgToolError,
					slog.String(LogKeyRequestID, requestID),
					slog.String(LogKeyTool, toolName),
					slog.Duration(LogKeyDuration, duration),
					slog.String(LogKeyError, errMsg),
				)
			} else {
				logger.Info(logMsgToolComplete,
					slog.String(LogKeyRequestID, requestID),
					slog.String(LogKeyTool, toolName),
					slog.Duration(LogKeyDuration, duration),
				)
			}

			return result, err
		}
	}
}
