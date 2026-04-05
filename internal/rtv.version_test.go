package internal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadVersionFromFile(t *testing.T) {
	// Reset to "dev" before each test to ensure LoadVersion reads the file.
	originalVersion := Version
	t.Cleanup(func() { Version = originalVersion })

	t.Run("valid_versions_yaml", func(t *testing.T) {
		Version = "dev"
		dir := t.TempDir()
		path := filepath.Join(dir, "versions.yaml")
		err := os.WriteFile(path, []byte("version: \"1.2.3\"\n"), 0o644)
		require.NoError(t, err)

		err = LoadVersion(path)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3", Version)
	})

	t.Run("skips_if_already_set_via_ldflags", func(t *testing.T) {
		Version = "0.5.0"
		err := LoadVersion("nonexistent.yaml")
		require.NoError(t, err)
		assert.Equal(t, "0.5.0", Version)
	})

	t.Run("missing_file", func(t *testing.T) {
		Version = "dev"
		err := LoadVersion("/nonexistent/path/versions.yaml")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrVersionLoad)
	})

	t.Run("invalid_yaml", func(t *testing.T) {
		Version = "dev"
		dir := t.TempDir()
		path := filepath.Join(dir, "versions.yaml")
		err := os.WriteFile(path, []byte("{{invalid yaml"), 0o644)
		require.NoError(t, err)

		err = LoadVersion(path)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrVersionLoad)
	})

	t.Run("empty_version_field", func(t *testing.T) {
		Version = "dev"
		dir := t.TempDir()
		path := filepath.Join(dir, "versions.yaml")
		err := os.WriteFile(path, []byte("version: \"\"\n"), 0o644)
		require.NoError(t, err)

		err = LoadVersion(path)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrVersionLoad)
		assert.Contains(t, err.Error(), "empty")
	})
}

func TestVersionInfo(t *testing.T) {
	originalVersion := Version
	originalCommit := GitCommit
	originalDate := BuildDate
	t.Cleanup(func() {
		Version = originalVersion
		GitCommit = originalCommit
		BuildDate = originalDate
	})

	Version = "1.0.0"
	GitCommit = "abc123"
	BuildDate = "2024-01-01"

	info := VersionInfo()

	assert.Equal(t, "1.0.0", info[LogKeyVersion])
	assert.Equal(t, "abc123", info[LogKeyGitCommit])
	assert.Equal(t, "2024-01-01", info[LogKeyBuildDate])
}
