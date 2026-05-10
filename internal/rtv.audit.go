package internal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"
)

// ---------------------------------------------------------------------------
// EU-mode audit log — Hook #3 (plan §3.7).
//
// Every Router.Search call emits an AuditEvent through the configured sink.
// Default sink writes JSON to slog.Info. Cycle 2 hashes the query by default
// (sha256 of UTF-8 bytes, first 16 hex chars) to satisfy DSGVO Art. 5(1)(c)
// data minimization; opt-in plaintext via AuditConfig.LogQueryPlaintext for
// debugging environments where the operator owns the query stream.
// ---------------------------------------------------------------------------

const (
	// auditQueryHashHexLen is the truncated length of the query hash in
	// AuditEvent.QueryHash. 16 hex chars = 64 bits of entropy — enough to
	// distinguish queries in a logging stream without surfacing the raw
	// query content.
	auditQueryHashHexLen = 16

	// auditRefBytes is the random-bytes count for AuditEvent.AuditRef
	// (8 bytes → 16 hex chars). The audit_ref is surfaced in the API
	// response so callers can correlate logs to requests.
	auditRefBytes = 8

	// auditRefPrefix is prepended to every audit ref to make it
	// grep-friendly and self-describing in mixed log streams.
	auditRefPrefix = "evt_aud_"

	// AuditSinkSlog routes events to the Router's logger at Info level.
	// Default sink for cycle 2; "file" and "nats" sinks deferred to v2.1.0.
	AuditSinkSlog = "slog"

	// auditLogMsg is the slog message under which audit events are written.
	auditLogMsg = "retrievr audit event"

	// Skip-reason constants for ProvidersSkipped entries.
	skipReasonEUStrict          = "eu_strict_mode"
	skipReasonEUPreferredFilter = "eu_preferred_filter"
)

// AuditEvent captures a single retrieval call for compliance/observability.
// Surfaced via AuditSink after every Router.Search; AuditRef returns to the
// caller in MergedSearchResult.
type AuditEvent struct {
	AuditRef         string     `json:"audit_ref"`
	Mode             string     `json:"mode,omitempty"`
	Intent           string     `json:"intent,omitempty"`
	QueryHash        string     `json:"query_hash"`
	QueryPlaintext   string     `json:"query_plaintext,omitempty"`
	ProvidersInvoked []string   `json:"providers_invoked"`
	ProvidersSkipped []SkipNote `json:"providers_skipped,omitempty"`
	ProvidersFailed  []string   `json:"providers_failed,omitempty"`
	FallbackWalked   bool       `json:"fallback_walked,omitempty"`
	EUFallbackUsed   bool       `json:"eu_fallback_used,omitempty"`
	CacheHit         bool       `json:"cache_hit,omitempty"`
	Ts               time.Time  `json:"ts"`
}

// SkipNote records why a provider was excluded from a fan-out. Exposed in
// AuditEvent and in MergedSearchResult.ProvidersSkipped so the caller can
// observe gate decisions without parsing logs.
type SkipNote struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// AuditSink consumes AuditEvents. Implementations must be safe for
// concurrent use — Router emits from its Search goroutine; downstream sinks
// may serialize internally.
type AuditSink interface {
	Emit(ctx context.Context, evt AuditEvent)
}

// noopAuditSink discards every event. Used when AuditConfig.Enabled=false
// or when no sink is configured.
type noopAuditSink struct{}

// Emit implements AuditSink.
func (noopAuditSink) Emit(_ context.Context, _ AuditEvent) {}

// NoopAuditSink returns an AuditSink that discards events.
func NoopAuditSink() AuditSink { return noopAuditSink{} }

// slogAuditSink writes events to a slog logger at Info level. The event is
// flattened into key/value pairs so log-aggregators can index by field
// without parsing nested JSON.
type slogAuditSink struct {
	logger *slog.Logger
}

// NewSlogAuditSink constructs an AuditSink backed by the given logger.
// Nil logger falls through to the default slog handler.
func NewSlogAuditSink(logger *slog.Logger) AuditSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &slogAuditSink{logger: logger}
}

// Emit implements AuditSink. Builds attrs incrementally so optional fields
// don't appear when zero (keeps the log line compact when there's nothing
// special to report).
func (s *slogAuditSink) Emit(ctx context.Context, evt AuditEvent) {
	attrs := []slog.Attr{
		slog.String("audit_ref", evt.AuditRef),
		slog.String("query_hash", evt.QueryHash),
		slog.Time("ts", evt.Ts),
	}
	if evt.Mode != "" {
		attrs = append(attrs, slog.String("mode", evt.Mode))
	}
	if evt.Intent != "" {
		attrs = append(attrs, slog.String("intent", evt.Intent))
	}
	if evt.QueryPlaintext != "" {
		attrs = append(attrs, slog.String("query", evt.QueryPlaintext))
	}
	if len(evt.ProvidersInvoked) > 0 {
		attrs = append(attrs, slog.Any("providers_invoked", evt.ProvidersInvoked))
	}
	if len(evt.ProvidersSkipped) > 0 {
		attrs = append(attrs, slog.Any("providers_skipped", evt.ProvidersSkipped))
	}
	if len(evt.ProvidersFailed) > 0 {
		attrs = append(attrs, slog.Any("providers_failed", evt.ProvidersFailed))
	}
	if evt.FallbackWalked {
		attrs = append(attrs, slog.Bool("fallback_walked", true))
	}
	if evt.EUFallbackUsed {
		attrs = append(attrs, slog.Bool("eu_fallback_used", true))
	}
	if evt.CacheHit {
		attrs = append(attrs, slog.Bool("cache_hit", true))
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, auditLogMsg, attrs...)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// hashQuery returns the first auditQueryHashHexLen hex chars of sha256(query).
// Empty query yields the empty string (skipping is preferable to recording
// a hash of nothing).
func hashQuery(query string) string {
	if query == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(query))
	full := hex.EncodeToString(sum[:])
	return full[:auditQueryHashHexLen]
}

// generateAuditRef returns a fresh "evt_aud_<hex>" identifier suitable for
// correlating an AuditEvent to API responses and downstream log streams.
// Uses crypto/rand for collision resistance under concurrent emissions.
func generateAuditRef() string {
	b := make([]byte, auditRefBytes)
	if _, err := rand.Read(b); err != nil {
		// Fallback: time-based ref. Not collision-safe under high
		// concurrency but better than panicking when /dev/urandom dies.
		return auditRefPrefix + fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return auditRefPrefix + hex.EncodeToString(b)
}

// ResolveAuditSink returns an AuditSink derived from cfg + a fallback
// logger. Returns NoopAuditSink when cfg.Enabled=false.
//
// Exported because cmd/retrievr-mcp/main.go and pkg/retrievr.NewClientFromConfig
// both consume it to wire the operator's `audit:` YAML block onto the Router.
func ResolveAuditSink(cfg AuditConfig, logger *slog.Logger) AuditSink {
	if !cfg.Enabled {
		return NoopAuditSink()
	}
	switch cfg.Sink {
	case "", AuditSinkSlog:
		return NewSlogAuditSink(logger)
	default:
		// Unknown sink — log the misconfiguration via the provided logger
		// and degrade to slog. Hard-fail would be unfriendly to operators
		// who have a typo in YAML.
		if logger != nil {
			logger.Warn("retrievr audit: unknown sink, falling back to slog",
				slog.String("sink", cfg.Sink),
			)
		}
		return NewSlogAuditSink(logger)
	}
}
