# EU-GDPR Mode

retrievr v1.6.0+ ships a three-state jurisdictional gate plus six audit
hooks per plan §3.7 / ADR-018. The contract is auditable, predictable,
and orthogonal to the rest of the search surface.

## Three states

| Mode | Behavior |
|---|---|
| `off` | No-op. Every registered provider participates. Default. |
| `eu_preferred` | Same admission rules as `off` in cycle-2; the result-level "prefer EU" semantic is reserved for v1.7+. Cycle-2 emits an audit-warn when EU sources alone yield zero results. |
| `eu_strict` | Only EU-resident or UK-adequacy providers admitted. Optional `IncludePublicResearch` flag widens admission to public-research-infrastructure providers (ArXiv, OpenAlex, CrossRef, Semantic Scholar, PubMed, Wikipedia, Unpaywall). |

## Six audit hooks

### Hook #1 — Provider residency tags

Every `SourcePlugin` returns a `ResidencyTag` with `Region`, `DPAStatus`,
`SubprocessorURL`, and `LastVerifiedAt`. CI warns when `LastVerifiedAt`
is older than 90 days and (cycle-3) hard-fails at 180. See
`docs/residency.md` for the full provider table.

### Hook #2 — Mode gate pre-fanout

`internal.applyEUGate(candidates, plugins, mode, includePublicResearch)`
filters the resolved source set before any plugin invocation. Skipped
sources surface in `MergedSearchResult.SourcesSkipped` with
`reason: "eu_strict_mode"` so the caller can render UI hints / compliance
reports without parsing logs.

### Hook #3 — Outbound query audit log

Every `Router.Search` (and `Router.Stream`) emits an `AuditEvent` through
the configured `AuditSink`. Default sink writes JSON to slog.Info. The
event carries:

- `audit_ref` (`evt_aud_<16hex>`) — surfaced in `MergedSearchResult.AuditRef`
  for response-to-log correlation
- `mode`, `intent` — routing config snapshot
- `query_hash` — sha256:16 of the raw query (DSGVO Art. 5(1)(c) data minimization)
- `query_plaintext` — empty by default; opt-in via `WithAuditLogQueryPlaintext(true)`
- `providers_invoked` / `providers_skipped` / `providers_failed`
- `fallback_walked`, `eu_fallback_used`, `cache_hit` flags

Configured via top-level `audit:` YAML block:

```yaml
audit:
  enabled: true                  # default true
  log_query_plaintext: false     # default false; flip only in operator-owned environments
  sink: "slog"                   # slog | file | nats (file/nats deferred to v2.1)
```

### Hook #4 — Outbound HTTP hygiene

`internal.NewEgressClient(timeout)` returns an `*http.Client` whose
custom `RoundTripper` enforces:

- Neutral User-Agent: `retrievr/<version> (+repo URL)`
- Strips `Referer`, `X-Forwarded-For`, `X-Real-IP`, `Forwarded` headers on every outbound request
- No cookie jar (cross-tenant correlation hazard)
- Conservative connection pool (4 idle conns/host, 90s idle timeout, 10s dial+TLS handshake)

All 18 v2.0.0 plugins use it. Plugins MAY override the User-Agent
(Wikipedia requires a polite UA with contact email).

### Hook #5 — Refusal path

`Router.Search` runs `validateEUModeSources` upfront. When `eu_strict` +
explicit `Sources` arg includes a non-EU provider, returns
`*EUModeProviderConflictError` (satisfies `errors.Is(err, ErrEUModeProviderConflict)`).
Structured fields:

```go
type EUModeProviderConflictError struct {
    Mode      string   // "eu_strict"
    Requested []string // sources the caller asked for
    Blocked   []string // subset of Requested rejected by the gate
}
```

### Hook #6 — Config drift guard

`VerifyProvidersSnapshot(SnapshotConfig, *slog.Logger)` computes SHA256
of a checked-in `providers.yaml` (declaration of every active provider's
residency tag) and compares to a checked-in signature file. Mismatch
warns by default; `Strict: true` upgrades to fatal (`ErrConfigDriftDetected`).

```yaml
snapshot:
  providers_file: "configs/providers.yaml"
  signature_file: "configs/providers.snapshot.sig"
  strict: false                  # warn vs fatal on drift
```

## Wiring it together

```go
import (
    "log/slog"
    "github.com/itsatony/retrievr-mcp/pkg/retrievr"
)

logger := slog.Default()

client, cleanup, err := retrievr.NewClientFromConfig("configs/retrievr-mcp.yaml", logger)
if err != nil { /* ... */ }
defer cleanup()

// Cycle-2 NewClientFromConfig automatically reads top-level eu_mode/audit/
// snapshot blocks from the YAML config. To override per-call, the Router
// inside the Client honors WithEUMode/WithAuditSink at construction time.
```

## What v1.7+ may add

- `eu_preferred` result-level fallback: try EU set first, walk to non-EU
  only when EU set yields zero merged results. Currently equivalent to `off`.
- Per-call `EUMode` override on `SearchParams` (currently config-only).
- File + NATS audit sinks (currently slog-only).
- `LastVerifiedAt` 90-day CI gate enforcement.

## Caveats

- `eu_strict + IncludePublicResearch=true` admits providers that are US-hosted
  but provide bibliographic public-good metadata. Operators should explicitly
  opt in based on their compliance posture — this isn't a default-on relaxation.
- "EU-resident" is a vendor claim. retrievr enforces the gate against the
  ResidencyTag the plugin declares; verifying that the upstream provider
  actually routes data through EU infrastructure is the operator's responsibility.
- Cycle-2 + Cycle-3 ship without rerankers — Mixedbread (EU-resident
  reranker) was originally planned but cut. If a rerank stage is added
  later, the EU-mode docs will track which rerankers are admissible
  under each mode.
