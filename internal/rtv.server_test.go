package internal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

const (
	testServerVersion   = "99.0.0"
	testServerGitCommit = "abc123"
	testServerBuildDate = "2024-01-01"
)

// ---------------------------------------------------------------------------
// Server construction tests
// ---------------------------------------------------------------------------

func TestNewServer(t *testing.T) {
	// Not parallel: mutates global version state.
	SetVersionForTesting(testServerVersion, testServerGitCommit, testServerBuildDate)
	t.Cleanup(ResetVersionForTesting)

	cfg := testServerConfig()
	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, nil),
	}
	router := testRouter(plugins)
	rateLimits := testRateLimits(plugins)

	srv := NewServer(cfg, router, rateLimits, discardLogger())
	require.NotNil(t, srv)
	require.NotNil(t, srv.mcpServer)
	require.NotNil(t, srv.mcpHTTPHandler)
	require.NotNil(t, srv.httpServer)
}

func TestServerHandler(t *testing.T) {
	// Not parallel: mutates global version state.
	SetVersionForTesting(testServerVersion, testServerGitCommit, testServerBuildDate)
	t.Cleanup(ResetVersionForTesting)

	cfg := testServerConfig()
	router := testRouter(nil)
	srv := NewServer(cfg, router, nil, discardLogger())

	handler := srv.Handler()
	require.NotNil(t, handler, "Handler() should return the HTTP mux")
}

// ---------------------------------------------------------------------------
// Health endpoint tests
// ---------------------------------------------------------------------------

func TestHealthEndpoint(t *testing.T) {
	// Not parallel: mutates global version state.
	SetVersionForTesting(testServerVersion, testServerGitCommit, testServerBuildDate)
	t.Cleanup(ResetVersionForTesting)

	cfg := testServerConfig()
	router := testRouter(nil)
	srv := NewServer(cfg, router, nil, discardLogger())

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + healthEndpointPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, httpContentTypeJSON, resp.Header.Get(httpHeaderContentType))

	var health healthResponse
	err = json.NewDecoder(resp.Body).Decode(&health)
	require.NoError(t, err)
	assert.Equal(t, healthStatusOK, health.Status)
	assert.Equal(t, testServerVersion, health.Version)
}

// ---------------------------------------------------------------------------
// Version endpoint tests
// ---------------------------------------------------------------------------

func TestVersionEndpoint(t *testing.T) {
	// Not parallel: mutates global version state.
	SetVersionForTesting(testServerVersion, testServerGitCommit, testServerBuildDate)
	t.Cleanup(ResetVersionForTesting)

	cfg := testServerConfig()
	router := testRouter(nil)
	srv := NewServer(cfg, router, nil, discardLogger())

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + versionEndpointPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, httpContentTypeJSON, resp.Header.Get(httpHeaderContentType))

	var versionInfo map[string]string
	err = json.NewDecoder(resp.Body).Decode(&versionInfo)
	require.NoError(t, err)
	assert.Equal(t, testServerVersion, versionInfo[LogKeyVersion])
	assert.Equal(t, testServerGitCommit, versionInfo[LogKeyGitCommit])
	assert.Equal(t, testServerBuildDate, versionInfo[LogKeyBuildDate])
}

// ---------------------------------------------------------------------------
// MCP endpoint test (basic reachability)
// ---------------------------------------------------------------------------

func TestMCPEndpointReachable(t *testing.T) {
	// Not parallel: mutates global version state.
	SetVersionForTesting(testServerVersion, testServerGitCommit, testServerBuildDate)
	t.Cleanup(ResetVersionForTesting)

	cfg := testServerConfig()
	router := testRouter(nil)
	srv := NewServer(cfg, router, nil, discardLogger())

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// The MCP endpoint expects POST with JSON-RPC, but a basic GET should
	// not 404 — the StreamableHTTPServer handles the method logic.
	resp, err := http.Get(ts.URL + mcpEndpointPath)
	require.NoError(t, err)
	defer resp.Body.Close()

	// StreamableHTTPServer returns something (not 404 from mux).
	assert.NotEqual(t, http.StatusNotFound, resp.StatusCode,
		"MCP endpoint should be registered in the mux")
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testServerConfig returns a Config suitable for testing.
func testServerConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Name:      DefaultServerName,
			HTTPAddr:  DefaultHTTPAddr,
			LogLevel:  LogLevelInfo,
			LogFormat: LogFormatJSON,
		},
		Router: testRouterConfig(),
		Sources: map[string]PluginConfig{
			SourceArXiv: {Enabled: true, RateLimit: testRateLimitRPS, RateLimitBurst: testRateLimitBurst},
		},
	}
}
