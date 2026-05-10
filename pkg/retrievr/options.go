package retrievr

import "log/slog"

// ClientOption configures a Client at construction time. The full functional-
// option set is filled in across cycles 1-3 of the v1.5.0 → v2.0.0 plan; the
// signatures below are the stable surface from cycle 1 onward.
type ClientOption func(*Client)

// WithLogger sets the slog logger Client uses for diagnostic output.
// A nil logger falls back to a discard handler.
func WithLogger(l *slog.Logger) ClientOption {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithEUMode configures jurisdictional gating.
//
// includePublicResearch is honored only when mode == EUModeStrict; it allows
// public-research-infrastructure providers (ArXiv, OpenAlex, Wikipedia, etc.)
// to participate alongside strictly-EU providers.
//
// Cycle-1 status: the option is recorded on the Client but the gate is
// stubbed (admits everything). Full enforcement lands in cycle 2.
func WithEUMode(mode EUMode, includePublicResearch bool) ClientOption {
	return func(c *Client) {
		c.euMode = mode
		c.euIncludePublicResearch = includePublicResearch
	}
}

// WithAuditSink installs an AuditSink. When unset, Client uses NoopAuditSink.
func WithAuditSink(s AuditSink) ClientOption {
	return func(c *Client) {
		if s != nil {
			c.auditSink = s
		}
	}
}

// WithFallbackChains configures the per-intent primary/fallback source
// resolution. Cycle 1 task #4 wires this into resolveSources.
func WithFallbackChains(primary, fallback map[Intent][]string) ClientOption {
	return func(c *Client) {
		c.primaryChains = primary
		c.fallbackChains = fallback
	}
}

// WithIncludeRaw causes Result.Raw to be populated with each provider's raw
// JSON response. Off by default (raw payloads are bulky).
func WithIncludeRaw(include bool) ClientOption {
	return func(c *Client) {
		c.includeRaw = include
	}
}
