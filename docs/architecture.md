# Architecture — retrievr-mcp v2.0.0

retrievr is a multi-source retrieval library + MCP server. **18 providers**
fan out across 7 result kinds (paper, model, dataset, web, news, code,
encyclopedia) behind a small public surface (`pkg/retrievr.Client`).

## Layered structure

```
cmd/retrievr-mcp/        thin MCP wrapper (~250 LoC)
cmd/retrievr-cli/        thin CLI wrapper (~500 LoC)
pkg/retrievr/            public Go library — type aliases + Client + ClientOption
pkg/retrievr/plugin/     SourcePlugin alias + middleware/registry/residency
internal/                canonical implementation (single flat package; see ADR-001)
  rtv.router.go            fan-out, merge, dedup, audit, EU-mode integration
  rtv.pluginchain.go       middleware chain (retry → rate-limit → timeout)
  rtv.eumode.go            gate (Hook #2) + refusal (Hook #5)
  rtv.audit.go             AuditEvent + sinks (Hook #3)
  rtv.httpx.go             egress client (Hook #4)
  rtv.snapshot.go          providers.yaml drift guard (Hook #6)
  rtv.residency.go         Region / DPAStatus / ResidencyTag (Hook #1)
  rtv.result.go            Result fat-struct + per-kind data blocks
  rtv.result_convert.go    Publication → Result converter
  rtv.stream.go            progressive search (Router.Stream)
  rtv.plugin.<source>.go   18 provider implementations
```

## Request flow — `Client.Search` (v2 default)

```
caller → Client.Search(ctx, params, sources)
  ↓ ctx-creds mirror (map → internal.PerCallCredsMap)
  ↓ Router.Search
    1. validateEUModeSources    — Hook #5 refusal if eu_strict + non-EU sources[]
    2. resolveSources/Intent    — explicit > intent-chain > defaults
    3. applyEUGate              — Hook #2 filter, populate SourcesSkipped
    4. cache lookup             — emit audit event on hit, return early
    5. fan-out goroutines       → each runs through pluginchain:
                                    retry → rate-limit → timeout → plugin.Search
    6. classify per-source
    7. fallback walk            — only when primary all-fail or zero-results
    8. dedup (DOI / ArXiv ID)
    9. unpaywall enrichment     — fill PDFURL on papers with DOI but no PDF
    10. sort + truncate to limit
    11. emitAuditEvent          — Hook #3
    12. PublicationsToResults   — Publication → Result conversion
  ↓ MergedSearchResultV2 with Kind discriminator + per-kind data blocks
```

## Plugin invocation chain (per-source)

```
                            ┌───────────────────────────────┐
                            │ retry (3 attempts, eq-jitter, │
                            │  honors ctx deadline)         │
                            └───────────┬───────────────────┘
                                        │
                            ┌───────────▼───────────────────┐
                            │ rate-limit (token bucket per  │
                            │  (sourceID, credKey))         │
                            └───────────┬───────────────────┘
                                        │
                            ┌───────────▼───────────────────┐
                            │ per-attempt timeout           │
                            └───────────┬───────────────────┘
                                        │
                            ┌───────────▼───────────────────┐
                            │ plugin.Search                 │
                            └───────────────────────────────┘
```

Order: retry **above** rate-limit so each attempt consumes its own bucket
token (matches liz DC-145; ADR-015). Cycle-3 may add cache + fallback as
explicit middleware — currently those run at Router level above the chain.

## Fan-out and dedup

- Per-source results are collected concurrently into `[]sourceResult`.
- Sorted by source ID for deterministic dedup primary selection.
- Exact-match dedup on DOI + ArXiv ID only (ADR-005). The first source-
  alphabetical occurrence wins; duplicates contribute to `also_found_in`
  and merge their citation count metadata.
- Sort + truncate happen post-dedup. Truncation respects `params.Limit`.

## Audit pipeline

Every `Search` (and every `Stream`) emits a single `AuditEvent` after the
final return path. Default sink is slog at Info level. Events carry:

- `audit_ref` — `evt_aud_<16hex>` correlation ID surfaced back in the
  `MergedSearchResult.AuditRef` field
- `mode`, `intent` — request-level routing config
- `query_hash` — sha256:16 of the query (DSGVO Art. 5(1)(c) data minimization)
- `query_plaintext` — empty by default; opt in via `WithAuditLogQueryPlaintext(true)`
- `providers_invoked`, `providers_skipped`, `providers_failed` — gating + per-source outcome
- `fallback_walked`, `eu_fallback_used`, `cache_hit` — flow flags

## Streaming flow — `Client.Stream`

Trade-offs vs. Search:

- Per-source `StreamEvent`s emitted as plugins return — no merge wait
- No cross-source dedup (would require buffering)
- No fallback walk (incremental decisions can't lookahead)
- EU-mode gate, refusal path, audit event, HTTP hygiene all still apply
- Not exposed via MCP (MCP doesn't stream tool results); CLI exposes via `--stream`

## See also

- `docs/eu-mode.md` — EU-GDPR mode reference (3 states, 6 hooks)
- `docs/intents.md` — intent-driven source selection + fallback chains
- `docs/residency.md` — per-provider residency table
- `docs/library-guide.md` — `pkg/retrievr` API + worked examples
- `docs/plugin-guide.md` — write a new `SourcePlugin`
- `ADRs.md` — accepted architectural decisions (currently 022)
