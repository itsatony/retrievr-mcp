package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testConfigYAML = `
server:
  name: "retrievr-mcp"
  http_addr: ":8099"
  log_level: "info"
  log_format: "json"
router:
  default_sources: ["arxiv"]
  per_source_timeout: "10s"
sources:
  arxiv:
    enabled: true
    timeout: "15s"
`

const testVersionsYAML = `version: "0.1.0"
`

func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "retrievr-mcp")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")

	// Build from the cmd/retrievr-mcp directory context.
	cmd = exec.Command("go", "build", "-o", binary, "./cmd/retrievr-mcp")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build failed: %s", string(out))
	return binary
}

func setupTestFiles(t *testing.T) (configPath, versionsPath, workDir string) {
	t.Helper()
	dir := t.TempDir()

	configPath = filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(testConfigYAML), 0o644)
	require.NoError(t, err)

	versionsPath = filepath.Join(dir, "versions.yaml")
	err = os.WriteFile(versionsPath, []byte(testVersionsYAML), 0o644)
	require.NoError(t, err)

	return configPath, versionsPath, dir
}

func TestMainBinaryValidConfig(t *testing.T) {
	binary := buildBinary(t)
	configPath, _, workDir := setupTestFiles(t)

	cmd := exec.Command(binary, "--config", configPath)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "binary exited with error: %s", string(out))

	// Verify output contains JSON log lines.
	output := string(out)
	assert.Contains(t, output, "retrievr-mcp")
	assert.Contains(t, output, logMsgStartup)

	// Verify at least one line is valid JSON (structured logging).
	lines := splitNonEmpty(output)
	require.NotEmpty(t, lines)

	var logEntry map[string]any
	err = json.Unmarshal([]byte(lines[0]), &logEntry)
	require.NoError(t, err, "first log line should be valid JSON: %s", lines[0])
	assert.Contains(t, logEntry, "msg")
}

func TestMainBinaryMissingConfig(t *testing.T) {
	binary := buildBinary(t)
	_, _, workDir := setupTestFiles(t) // provides versions.yaml in workDir

	cmd := exec.Command(binary, "--config", "/nonexistent/config.yaml")
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "expected non-zero exit for missing config")
	assert.Contains(t, string(out), "failed to load config")
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

func splitNonEmpty(s string) []string {
	var result []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			if i > start {
				result = append(result, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		result = append(result, s[start:])
	}
	return result
}
