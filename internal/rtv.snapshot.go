package internal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// ---------------------------------------------------------------------------
// Config drift guard — EU-mode Hook #6 (plan §3.7).
//
// At NewClient/Server time we compute SHA256 of the providers manifest
// (typically configs/providers.yaml — a checked-in declaration of every
// active provider's residency tag) and compare it to a checked-in
// signature file (configs/providers.snapshot.sig — plain hex string).
//
// Mismatch = someone changed the active provider set without updating the
// signature, which would silently relax the EU-mode posture. We:
//   - log a warning + emit an audit event (default behavior)
//   - return ErrConfigDriftDetected (when SnapshotConfig.Strict=true)
//
// The hook is a no-op when SnapshotConfig.ProvidersFile or SignatureFile
// is empty (operators who haven't adopted the manifest pattern yet stay
// unaffected).
// ---------------------------------------------------------------------------

// VerifyProvidersSnapshot performs the drift check. Returns nil when:
//   - cfg.ProvidersFile or cfg.SignatureFile is empty (hook disabled), or
//   - the computed SHA256 matches the trimmed contents of the signature file.
//
// Returns ErrConfigDriftDetected (wrapped with detail) when there's a
// mismatch AND cfg.Strict=true. When Strict=false, logs a warning and
// returns nil so boot proceeds.
//
// On any IO error reading either file, returns the error unconditionally —
// a missing snapshot file is a configuration mistake, not a drift event.
func VerifyProvidersSnapshot(cfg SnapshotConfig, logger *slog.Logger) error {
	if cfg.ProvidersFile == "" || cfg.SignatureFile == "" {
		return nil // hook disabled
	}

	manifestBytes, err := os.ReadFile(cfg.ProvidersFile)
	if err != nil {
		return fmt.Errorf("retrievr snapshot: read providers file %q: %w", cfg.ProvidersFile, err)
	}

	sigBytes, err := os.ReadFile(cfg.SignatureFile)
	if err != nil {
		return fmt.Errorf("retrievr snapshot: read signature file %q: %w", cfg.SignatureFile, err)
	}

	expected := strings.TrimSpace(string(sigBytes))
	got := computeSHA256Hex(manifestBytes)

	if got == expected {
		if logger != nil {
			logger.Debug("retrievr snapshot verified",
				slog.String("providers_file", cfg.ProvidersFile),
				slog.String("sha256", got),
			)
		}
		return nil
	}

	// Drift detected.
	if logger != nil {
		level := slog.LevelWarn
		if cfg.Strict {
			level = slog.LevelError
		}
		logger.LogAttrs(context.TODO(), level, "retrievr snapshot drift detected",
			slog.String("providers_file", cfg.ProvidersFile),
			slog.String("expected", expected),
			slog.String("got", got),
			slog.Bool("strict", cfg.Strict),
		)
	}

	if cfg.Strict {
		return fmt.Errorf("%w: providers_file=%q expected=%s got=%s",
			ErrConfigDriftDetected, cfg.ProvidersFile, expected, got)
	}
	return nil
}

// computeSHA256Hex returns the lowercase hex SHA256 of b.
func computeSHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
