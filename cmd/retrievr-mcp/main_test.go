package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testConfigYAMLTemplate = `
server:
  name: "retrievr-mcp"
  http_addr: ":%d"
  log_level: "info"
  log_format: "json"
router:
  default_sources: ["arxiv"]
  per_source_timeout: "10s"
sources:
  arxiv:
    enabled: true
    timeout: "15s"
    rate_limit: 10.0
    rate_limit_burst: 5
`

const (
	testExpectedVersion = "1.0.0"
	testVersionsYAML    = `version: "1.0.0"
`
)

const (
	healthPollInterval = 100 * time.Millisecond
	healthPollTimeout  = 5 * time.Second
)

func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "retrievr-mcp")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/retrievr-mcp")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build failed: %s", string(out))
	return binary
}

// findFreePort returns an available TCP port.
func findFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

func setupTestFiles(t *testing.T, port int) (configPath, workDir string) {
	t.Helper()
	dir := t.TempDir()

	configYAML := fmt.Sprintf(testConfigYAMLTemplate, port)
	configPath = filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(configYAML), 0o644)
	require.NoError(t, err)

	versionsPath := filepath.Join(dir, "versions.yaml")
	err = os.WriteFile(versionsPath, []byte(testVersionsYAML), 0o644)
	require.NoError(t, err)

	return configPath, dir
}

// pollHealth polls the /health endpoint until it gets a 200 response or times out.
func pollHealth(t *testing.T, port int) {
	t.Helper()
	url := fmt.Sprintf("http://localhost:%d/health", port)
	deadline := time.Now().Add(healthPollTimeout)

	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(healthPollInterval)
	}
	t.Fatalf("server did not become healthy within %s", healthPollTimeout)
}

func TestMainBinaryStartsAndServesHealth(t *testing.T) {
	binary := buildBinary(t)
	port := findFreePort(t)
	configPath, workDir := setupTestFiles(t, port)

	cmd := exec.Command(binary, "--config", configPath)
	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())

	// Ensure the process is cleaned up.
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	})

	// Wait for server to be ready.
	pollHealth(t, port)

	// Verify /health response.
	healthURL := fmt.Sprintf("http://localhost:%d/health", port)
	resp, err := http.Get(healthURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var health map[string]string
	err = json.NewDecoder(resp.Body).Decode(&health)
	require.NoError(t, err)
	assert.Equal(t, "ok", health["status"])
	assert.Equal(t, testExpectedVersion, health["version"])

	// Verify /version response.
	versionURL := fmt.Sprintf("http://localhost:%d/version", port)
	resp2, err := http.Get(versionURL)
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	// Send SIGTERM and verify clean shutdown.
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	err = cmd.Wait()
	assert.NoError(t, err, "server should exit cleanly after SIGTERM")
}

func TestMainBinaryMissingConfig(t *testing.T) {
	binary := buildBinary(t)
	// Provide a valid versions.yaml so the binary gets past version loading.
	dir := t.TempDir()
	versionsPath := filepath.Join(dir, "versions.yaml")
	err := os.WriteFile(versionsPath, []byte(testVersionsYAML), 0o644)
	require.NoError(t, err)

	cmd := exec.Command(binary, "--config", "/nonexistent/config.yaml")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "expected non-zero exit for missing config")
	assert.Contains(t, string(out), logMsgConfigFail)
}

func TestMainBinaryHelp(t *testing.T) {
	binary := buildBinary(t)

	cmd := exec.Command(binary, "--help")
	out, err := cmd.CombinedOutput()
	// --help causes flag.Parse to exit with status 0 in some Go versions,
	// or status 2 in others. Just check it mentions the flag.
	_ = err
	assert.Contains(t, string(out), flagNameConfig)
}
