package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// ---------------------------------------------------------------------------
// Server constants
// ---------------------------------------------------------------------------

const (
	healthEndpointPath  = "/health"
	versionEndpointPath = "/version"
	mcpEndpointPath     = "/mcp"

	healthStatusOK = "ok"

	httpContentTypeJSON   = "application/json"
	httpHeaderContentType = "Content-Type"

	// ShutdownTimeout is the maximum duration to wait for graceful HTTP drain.
	// Exported for use by cmd/retrievr-mcp.
	ShutdownTimeout = 10 * time.Second

	// readHeaderTimeout protects against slowloris attacks by limiting how long
	// the server waits for request headers.
	readHeaderTimeout = 10 * time.Second
)

// ---------------------------------------------------------------------------
// Server log message constants
// ---------------------------------------------------------------------------

const (
	logMsgServerStarting = "server starting"
	logMsgServerStopping = "server shutting down"
	logMsgServerStopped  = "server stopped"
	logMsgEncodeFailed   = "response encode failed"
)

// ---------------------------------------------------------------------------
// Server types
// ---------------------------------------------------------------------------

// healthResponse is the JSON shape returned by the /health endpoint.
type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// Server is the top-level runtime container that holds all wired components.
// It manages the MCP server, HTTP transport, and graceful lifecycle.
// Thread-safe: all internal components handle their own concurrency.
type Server struct {
	mcpServer      *server.MCPServer
	mcpHTTPHandler *server.StreamableHTTPServer
	httpServer     *http.Server
	rateLimits     *SourceRateLimitManager
	logger         *slog.Logger
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewServer constructs a fully-wired Server. It creates the MCP server,
// registers all three tools with middleware, builds the HTTP mux with
// /health, /version, and /mcp endpoints, and prepares an http.Server
// ready for Start().
func NewServer(
	cfg *Config,
	router *Router,
	rateLimits *SourceRateLimitManager,
	logger *slog.Logger,
) *Server {
	// Create MCP server with tool capabilities.
	mcpSrv := server.NewMCPServer(
		cfg.Server.Name,
		GetVersion(),
		server.WithToolCapabilities(true),
	)

	// Register tool logging middleware.
	mcpSrv.Use(ToolLoggingMiddleware(logger))

	// Register all three tools.
	mcpSrv.AddTool(SearchToolDefinition(), NewSearchHandler(router))
	mcpSrv.AddTool(GetToolDefinition(), NewGetHandler(router))
	mcpSrv.AddTool(ListSourcesToolDefinition(), NewListSourcesHandler(router))

	// Create StreamableHTTPServer as an http.Handler with request ID injection.
	mcpHTTP := server.NewStreamableHTTPServer(mcpSrv,
		server.WithHTTPContextFunc(RequestIDContextFunc()),
	)

	// Build the HTTP mux with all endpoints.
	s := &Server{
		mcpServer:      mcpSrv,
		mcpHTTPHandler: mcpHTTP,
		rateLimits:     rateLimits,
		logger:         logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc(healthEndpointPath, s.handleHealth)
	mux.HandleFunc(versionEndpointPath, s.handleVersion)
	mux.Handle(mcpEndpointPath, mcpHTTP)

	s.httpServer = &http.Server{
		Addr:              cfg.Server.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	return s
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start begins listening for HTTP connections. This method blocks until the
// server is shut down. Returns ErrServerStart if the listener fails to bind.
func (s *Server) Start() error {
	s.logger.Info(logMsgServerStarting,
		slog.String(LogKeyAddr, s.httpServer.Addr),
	)

	err := s.httpServer.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("%w: %w", ErrServerStart, err)
	}
	return nil
}

// Shutdown performs graceful shutdown: drains HTTP connections, then stops
// background goroutines (rate limit cleanup). Returns ErrServerShutdown on failure.
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info(logMsgServerStopping)

	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrServerShutdown, err)
	}

	if s.rateLimits != nil {
		s.rateLimits.Stop()
	}

	s.logger.Info(logMsgServerStopped)
	return nil
}

// Handler returns the server's HTTP handler. Useful for testing with httptest.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// ---------------------------------------------------------------------------
// HTTP endpoint handlers
// ---------------------------------------------------------------------------

// handleHealth returns a JSON health check response.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set(httpHeaderContentType, httpContentTypeJSON)
	resp := healthResponse{
		Status:  healthStatusOK,
		Version: GetVersion(),
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Warn(logMsgEncodeFailed,
			slog.String(LogKeyEndpoint, healthEndpointPath),
			slog.String(LogKeyError, err.Error()),
		)
	}
}

// handleVersion returns full version information as JSON.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set(httpHeaderContentType, httpContentTypeJSON)
	if err := json.NewEncoder(w).Encode(GetVersionInfo()); err != nil {
		s.logger.Warn(logMsgEncodeFailed,
			slog.String(LogKeyEndpoint, versionEndpointPath),
			slog.String(LogKeyError, err.Error()),
		)
	}
}
