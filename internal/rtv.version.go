package internal

import (
	"fmt"
	"os"
	"runtime/debug"

	"gopkg.in/yaml.v3"
)

// Build-time variables — set via ldflags:
//
//	go build -ldflags "-X github.com/itsatony/retrievr-mcp/internal.Version=0.1.0"
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

// Log field key constants for version info.
const (
	LogKeyVersion   = "version"
	LogKeyGitCommit = "git_commit"
	LogKeyBuildDate = "build_date"
	LogKeyGoVersion = "go_version"
)

// versionsFile is the YAML structure of versions.yaml.
type versionsFile struct {
	Version string `yaml:"version"`
}

// LoadVersion reads version information from a YAML file and sets the
// package-level Version variable. If the version was already set via
// ldflags (i.e., not "dev"), the file is not read.
func LoadVersion(path string) error {
	if Version != "dev" {
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
		return fmt.Errorf("%w: version field is empty", ErrVersionLoad)
	}

	Version = vf.Version

	// Attempt to populate git commit from build info if not set via ldflags.
	if GitCommit == "unknown" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" && setting.Value != "" {
					GitCommit = setting.Value
				}
			}
		}
	}

	return nil
}

// VersionInfo returns version information as a string map, suitable for
// logging attributes or the /version endpoint.
func VersionInfo() map[string]string {
	return map[string]string{
		LogKeyVersion:   Version,
		LogKeyGitCommit: GitCommit,
		LogKeyBuildDate: BuildDate,
	}
}
