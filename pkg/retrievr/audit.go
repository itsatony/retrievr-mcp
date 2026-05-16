package retrievr

import (
	"log/slog"

	"github.com/itsatony/retrievr-mcp/v2/internal"
)

// ---------------------------------------------------------------------------
// Audit re-exports — Cycle 2 task #9 promoted these from public-only stubs
// to the canonical internal definitions used by the Router. This file
// preserves the cycle-1 import path for external Go consumers.
// ---------------------------------------------------------------------------

// AuditEvent captures a single retrieval call for compliance/observability.
type AuditEvent = internal.AuditEvent

// AuditSink consumes AuditEvents. Implementations must be safe for
// concurrent use.
type AuditSink = internal.AuditSink

// NoopAuditSink returns an AuditSink that discards all events. Useful for
// tests and for callers who want to disable audit emission entirely.
func NoopAuditSink() AuditSink { return internal.NoopAuditSink() }

// NewSlogAuditSink returns an AuditSink backed by a slog logger. Cycle-2
// default sink for retrievr.Client. Pass a nil logger to use slog.Default().
func NewSlogAuditSink(logger *slog.Logger) AuditSink {
	return internal.NewSlogAuditSink(logger)
}
