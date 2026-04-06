package internal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadVersionFromFile(t *testing.T) {
	t.Run("valid_versions_yaml", func(t *testing.T) {
		ResetVersionForTesting()
		dir := t.TempDir()
		path := filepath.Join(dir, "versions.yaml")
		err := os.WriteFile(path, []byte("version: \"1.2.3\"\n"), 0o644)
		require.NoError(t, err)

		err = LoadVersion(path)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3", GetVersion())
	})

	t.Run("skips_if_already_set_via_ldflags", func(t *testing.T) {
		SetVersionForTesting("0.5.0", VersionUnknown, VersionUnknown)
		t.Cleanup(ResetVersionForTesting)
		err := LoadVersion("nonexistent.yaml")
		require.NoError(t, err)
		assert.Equal(t, "0.5.0", GetVersion())
	})

	t.Run("missing_file", func(t *testing.T) {
		ResetVersionForTesting()
		err := LoadVersion("/nonexistent/path/versions.yaml")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrVersionLoad)
	})

	t.Run("invalid_yaml", func(t *testing.T) {
		ResetVersionForTesting()
		dir := t.TempDir()
		path := filepath.Join(dir, "versions.yaml")
		err := os.WriteFile(path, []byte("{{invalid yaml"), 0o644)
		require.NoError(t, err)

		err = LoadVersion(path)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrVersionLoad)
	})

	t.Run("empty_version_field", func(t *testing.T) {
		ResetVersionForTesting()
		dir := t.TempDir()
		path := filepath.Join(dir, "versions.yaml")
		err := os.WriteFile(path, []byte("version: \"\"\n"), 0o644)
		require.NoError(t, err)

		err = LoadVersion(path)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrVersionLoad)
		assert.Contains(t, err.Error(), "empty")
	})

	t.Run("only_first_call_takes_effect", func(t *testing.T) {
		ResetVersionForTesting()
		dir := t.TempDir()

		path1 := filepath.Join(dir, "v1.yaml")
		err := os.WriteFile(path1, []byte("version: \"1.0.0\"\n"), 0o644)
		require.NoError(t, err)

		path2 := filepath.Join(dir, "v2.yaml")
		err = os.WriteFile(path2, []byte("version: \"2.0.0\"\n"), 0o644)
		require.NoError(t, err)

		err = LoadVersion(path1)
		require.NoError(t, err)
		assert.Equal(t, "1.0.0", GetVersion())

		// Second call should be a no-op.
		err = LoadVersion(path2)
		require.NoError(t, err)
		assert.Equal(t, "1.0.0", GetVersion())
	})
}

func TestGetVersionInfo(t *testing.T) {
	SetVersionForTesting("1.0.0", "abc123", "2024-01-01")
	t.Cleanup(ResetVersionForTesting)

	info := GetVersionInfo()

	assert.Equal(t, "1.0.0", info[LogKeyVersion])
	assert.Equal(t, "abc123", info[LogKeyGitCommit])
	assert.Equal(t, "2024-01-01", info[LogKeyBuildDate])
}
