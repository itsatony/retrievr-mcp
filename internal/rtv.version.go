package internal

import (
	"fmt"
	"os"
	"runtime/debug"
	"sync"

	"gopkg.in/yaml.v3"
)

// Version-related constants.
const (
	VersionDev           = "dev"
	VersionUnknown       = "unknown"
	buildInfoKeyRevision = "vcs.revision"

	errDetailVersionFieldEmpty = "version field is empty"
)

// Log field key constants for version info.
const (
	LogKeyVersion   = "version"
	LogKeyGitCommit = "git_commit"
	LogKeyBuildDate = "build_date"
	LogKeyGoVersion = "go_version"
)

// versionState holds the immutable version information set once at startup.
type versionState struct {
	Version   string
	GitCommit string
	BuildDate string
}

var (
	// versionMu protects versionOnce and currentVersion from concurrent
	// access. Writers (test helpers) take a write lock; readers (GetVersion,
	// GetVersionInfo) take a read lock. In production, currentVersion is
	// immutable after sync.Once, so the RLock is effectively free.
	versionMu      sync.RWMutex
	versionOnce    sync.Once
	currentVersion = versionState{
		Version:   VersionDev,
		GitCommit: VersionUnknown,
		BuildDate: VersionUnknown,
	}
)

// GetVersion returns the current version string. Thread-safe via RLock.
func GetVersion() string {
	versionMu.RLock()
	defer versionMu.RUnlock()
	return currentVersion.Version
}

// GetVersionInfo returns version information as a string map. Thread-safe via RLock.
func GetVersionInfo() map[string]string {
	versionMu.RLock()
	defer versionMu.RUnlock()
	return map[string]string{
		LogKeyVersion:   currentVersion.Version,
		LogKeyGitCommit: currentVersion.GitCommit,
		LogKeyBuildDate: currentVersion.BuildDate,
	}
}

// versionsFile is the YAML structure of versions.yaml.
type versionsFile struct {
	Version string `yaml:"version"`
}

// LoadVersion reads version information from a YAML file and sets the
// package-level version state. Safe to call multiple times — only the first
// call takes effect. If the version was already set via ldflags (i.e., not
// "dev"), the file is not read.
func LoadVersion(path string) error {
	var loadErr error
	versionOnce.Do(func() {
		loadErr = doLoadVersion(path)
	})
	return loadErr
}

func doLoadVersion(path string) error {
	if currentVersion.Version != VersionDev {
		return nil // already set via ldflags
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVersionLoad, err)
	}

	var vf versionsFile
	if err := yaml.Unmarshal(data, &vf); err != nil {
		return fmt.Errorf("%w: %w", ErrVersionLoad, err)
	}

	if vf.Version == "" {
		return fmt.Errorf("%w: %s", ErrVersionLoad, errDetailVersionFieldEmpty)
	}

	currentVersion.Version = vf.Version

	// Attempt to populate git commit from build info if not set via ldflags.
	if currentVersion.GitCommit == VersionUnknown {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == buildInfoKeyRevision && setting.Value != "" {
					currentVersion.GitCommit = setting.Value
				}
			}
		}
	}

	return nil
}

// ResetVersionForTesting resets the version state so LoadVersion can be called
// again. Only for use in tests. Protected by versionMu for race safety.
func ResetVersionForTesting() {
	versionMu.Lock()
	defer versionMu.Unlock()
	versionOnce = sync.Once{}
	currentVersion = versionState{
		Version:   VersionDev,
		GitCommit: VersionUnknown,
		BuildDate: VersionUnknown,
	}
}

// SetVersionForTesting sets version state directly. Only for use in tests.
// Protected by versionMu for race safety.
func SetVersionForTesting(version, gitCommit, buildDate string) {
	versionMu.Lock()
	defer versionMu.Unlock()
	versionOnce = sync.Once{}
	currentVersion = versionState{
		Version:   version,
		GitCommit: gitCommit,
		BuildDate: buildDate,
	}
	// Mark as done so LoadVersion won't override.
	versionOnce.Do(func() {})
}
