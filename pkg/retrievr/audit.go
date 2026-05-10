package retrievr

import (
	"context"
	"time"
)

// AuditEvent captures a single retrieval call for compliance/observability.
// Emitted by Client through the configured AuditSink. Cycle 2 wires this
// into the EU-mode hook chain (hook #3 — outbound query audit log).
type AuditEvent struct {
	// AuditRef is a short, sortable identifier surfaced back to the caller
	// in MergedSearchResult so they can correlate logs to responses.
	AuditRef string `json:"audit_ref"`

	// Mode is the EUMode active for this call.
	Mode EUMode `json:"mode"`

	// Intent is the resolved intent for this call.
	Intent Intent `json:"intent,omitempty"`

	// QueryHash is the sha256(query) prefix (16 hex). Plaintext is omitted by
	// default; opt in via audit.log_query_plaintext.
	QueryHash string `json:"query_hash"`

	// QueryPlaintext is set only when the audit config opts in. Default empty.
	QueryPlaintext string `json:"query_plaintext,omitempty"`

	// ProvidersInvoked are the source IDs actually called.
	ProvidersInvoked []string `json:"providers_invoked"`

	// ProvidersSkipped are sources excluded by eu_mode + reason.
	ProvidersSkipped []SkipNote `json:"providers_skipped,omitempty"`

	// ProvidersFailed are sources that returned errors.
	ProvidersFailed []string `json:"providers_failed,omitempty"`

	// FallbackWalked is true when the fallback chain was walked beyond the
	// primary set (i.e., primary set yielded zero results or all failed).
	FallbackWalked bool `json:"fallback_walked,omitempty"`

	// EUFallbackUsed is true when eu_preferred fell back to a non-EU provider.
	EUFallbackUsed bool `json:"eu_fallback_used,omitempty"`

	// CacheHit is true when the response came from the cache and no provider
	// was invoked.
	CacheHit bool `json:"cache_hit,omitempty"`

	// Ts is the wall-clock time at which the call started.
	Ts time.Time `json:"ts"`
}

// SkipNote records why a provider was excluded from a fan-out.
type SkipNote struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// AuditSink consumes AuditEvents. The default Client uses a slog-backed sink;
// cycle 3 may add filesystem (rotating JSONL) and NATS sinks.
type AuditSink interface {
	Emit(ctx context.Context, evt AuditEvent)
}

// noopAuditSink is the zero-config default — drops events on the floor.
type noopAuditSink struct{}

// Emit implements AuditSink.
func (noopAuditSink) Emit(_ context.Context, _ AuditEvent) {}

// NoopAuditSink returns an AuditSink that discards all events. Useful for
// tests and for callers who want to disable audit emission entirely.
func NoopAuditSink() AuditSink { return noopAuditSink{} }
