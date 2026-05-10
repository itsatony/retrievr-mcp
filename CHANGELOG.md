# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [1.6.0] - 2026-05-10

Cycle 2 of the v2 multi-cycle plan (`project_plan/retrievr_v2.md`). Headline:
**Wave-1 providers + EU-GDPR mode + v2 result shape**. Source count grows
from 10 → 17 (Exa, Brave, Linkup, Firecrawl, GitHub, Wikipedia, Unpaywall).
EU-mode platform with all 6 audit hooks ships behind the existing public
API. v2 fat-struct `Result` shape opt-in via MCP `compat: "v2"` arg or
`Client.SearchV2()`; default v1 wire format unchanged for byte-stable
backward compat.

### Added — Wave-1 providers

- **Exa.ai** (`exa`) — neural + keyword web/news search. POST /search with
  `x-api-key`. `Kinds: [web, news]`, `QueryIntents: [quick_lookup, deep_research]`.
  US-resident; blocked under `eu_strict`.
- **Brave Search** (`brave`) — independent 35B+ page index. GET
  /res/v1/web/search with `X-Subscription-Token`. Merges web + news sections
  with `kind` override per result. US-resident; blocked under `eu_strict`.
- **Linkup** (`linkup`) — **EU-resident** web search (Linkup SAS, France)
  with signed DPA. POST /v1/search with Bearer auth. The headline EU-strict
  primary web provider — only Wave-1 source admitted under `eu_strict`.
- **Firecrawl** (`firecrawl`) — web search + per-URL markdown extraction.
  POST /v1/search with Bearer. Cycle-3 will activate the post-merge
  enrichment hook (toggle in config: `enrichment.firecrawl.enabled`).
  US-resident; blocked under `eu_strict`.
- **GitHub Code Search** (`github`) — public repository search via
  GET /search/repositories with PAT. `Kinds: [code]`,
  `QueryIntents: [code_provenance]`. Maps repo metadata (stars, forks,
  language, topics, license, last_commit) into `CodeData`.
- **Wikipedia** (`wikipedia`) — encyclopedia search via the public
  MediaWiki API. Free / no auth (polite User-Agent required).
  `Kinds: [encyclopedia]`, `QueryIntents: [reference, quick_lookup]`.
  Public-research-infrastructure tier; admitted under `eu_strict` only with
  `IncludePublicResearch=true`.
- **Unpaywall** (`unpaywall`) — DOI → OA PDF resolver, wired as a
  **post-merge enrichment hook**. When paper results have a DOI but no
  upstream PDF link, the Router consults Unpaywall to fill `PDFURL` +
  `License` + `OpenAccess`. Toggle via `enrichment.unpaywall` and the
  `Router.WithUnpaywallEnrichment(*UnpaywallPlugin)` option.

### Added — EU-mode platform (all 6 audit hooks per plan §3.7)

- **Hook #1 — Provider residency tags.** `SourcePlugin.Residency() ResidencyTag`
  is now part of the interface; every plugin declares region (EU /
  UK-adequacy / US / public-research-infrastructure / unknown), DPA status
  (signed / covered-by-scc / n/a / unknown), subprocessor URL, and
  last-verified date. Surfaced in `rtv_list_sources`.
- **Hook #2 — Mode gate pre-fanout.** Configurable via
  `Router.WithEUMode(mode, includePublicResearch)`. In `eu_strict` mode,
  non-EU providers are filtered out before fan-out and surface in
  `MergedSearchResult.SourcesSkipped` with `reason: "eu_strict_mode"`.
- **Hook #3 — Outbound query audit log.** Every `Router.Search` call emits
  an `AuditEvent` with `audit_ref`, mode, intent, hashed query (sha256:16,
  default), invoked/skipped/failed providers, fallback flags. Default sink
  routes to slog.Info; opt-in plaintext query via
  `WithAuditLogQueryPlaintext(true)`.
- **Hook #4 — Outbound HTTP hygiene.** New `internal.NewEgressClient(timeout)`
  builds an `*http.Client` with neutral User-Agent (`retrievr/<version> (+repo)`),
  no Referer / X-Forwarded-For / X-Real-IP / Forwarded headers, and no
  cookie jar. All Wave-1 plugins use it; cycle-3 migrates the 10 cycle-1
  scholarly plugins.
- **Hook #5 — Refusal path.** `Router.Search` rejects calls with
  `eu_strict + explicit non-EU sources` upfront with
  `*EUModeProviderConflictError` (satisfies `errors.Is(err, ErrEUModeProviderConflict)`).
  Structured `Requested` / `Blocked` / `Mode` fields let callers render
  remediation messages without parsing strings.
- **Hook #6 — Config drift guard.** `VerifyProvidersSnapshot` computes
  SHA256 of `providers.yaml` and compares to a checked-in signature file.
  Mismatch warns by default; `Strict: true` upgrades to fatal
  (`ErrConfigDriftDetected`). No-op when files unset.

### Added — v2 result shape

- **`Result` fat struct** with `Kind` discriminator (paper / model / dataset /
  web / news / code / encyclopedia) + per-kind data blocks (`PaperData`,
  `WebData`, `CodeData`, etc.). Lives in `internal/rtv.result.go`; aliased
  from `pkg/retrievr/result.go`.
- **`Client.SearchV2(ctx, params, sources)`** returns `*MergedSearchResultV2`
  with `[]Result`. Internally wraps `Search` and runs `PublicationsToResults`.
- **`Router.toResult(p, rank)`** converter — rank-based score
  (`1 / (1 + rank)`), domain auto-derived from URL, snippet auto-truncated
  from Abstract for non-paper kinds, provenance tagged from plugin's
  `Residency()`. 17 SourceMetadata keys for plugins to populate kind-
  specific data.
- **MCP `rtv_search` `compat` arg.** Default `"v1"` keeps the cycle-1 wire
  shape byte-stable. Opt in to `"v2"` for the new fat-struct response.
- **MCP `rtv_search` `intent` arg.** Drives Router source selection +
  fallback chains; values match the `Intent` enum.

### Added — `rtv_list_sources` revamp

`SourceInfo` gains `Kinds`, `QueryIntents`, `Region`, `DPAStatus`,
`SubprocessorURL`, `FreeTier`, `RequiresKey`. LLM agents and operators
can now pick sources by intent + jurisdiction without enumerating booleans.
Updated `ToolDescSearch` to a 30-second LLM-readable description covering
intent / kind / eu_mode / compat semantics.

### Added — config blocks (top-level)

- `eu_mode: { mode, include_public_research }` — gate configuration
- `audit: { enabled, log_query_plaintext, sink }` — Hook #3 controls
- `snapshot: { providers_file, signature_file, strict }` — Hook #6 inputs
- `enrichment: { unpaywall: {...}, firecrawl: {...} }` — post-merge hooks
- `sources: { exa, brave, linkup, firecrawl, github, wikipedia, unpaywall }` —
  7 new provider blocks (all `enabled: false` default; `wikipedia` enabled
  since it's keyless)

### Changed

- **`SourcePlugin` interface** gained `Residency() ResidencyTag` (breaking
  for any external implementor; pre-1.0 acceptable).
- **`SourceCapabilities`** gained `Kinds []ResultKind` (informational; cycle-1
  plugins return empty → converter defaults to `KindPaper`).
- **`MergedSearchResult`** gained `SourcesSkipped`, `AuditRef`,
  `FallbackWalked`, `EUFallbackUsed` (additive, JSON-omitempty preserves
  v1 callers' byte-stable response).
- **`NewRouter`** signature gained variadic `opts ...RouterOption` —
  existing 8-arg callers unaffected. New options: `WithEUMode`,
  `WithAuditSink`, `WithAuditLogQueryPlaintext`, `WithUnpaywallEnrichment`.
- **`SourceCount`** 10 → 17.

### Tests

- 14 new EU-mode conformance tests (`internal/rtv.eumode_test.go`) covering
  every hook end-to-end including HTTP hygiene round-trip + config-drift
  scenarios.
- 8 new converter tests (`internal/rtv.result_convert_test.go`) covering
  paper-default, web-via-SourceMetadata, code with stars, kind override,
  score decay, SearchV2 happy path, snippet truncation.
- 11 new Exa unit tests + live smoke (`TestExa_LiveSmoke`).
- 11 new Brave unit tests + live smoke (`TestBrave_LiveSmoke`).
- 13 new Linkup unit tests + **`TestEUMode_StrictAdmitsLinkupRefusesExa`**
  (the cycle-2 EU-mode end-to-end conformance test) + live smoke.
- 8 new Firecrawl unit tests + live smoke.
- 9 new GitHub unit tests + live smoke.
- 6 new Wikipedia unit tests + live smoke (keyless).
- 9 new Unpaywall unit tests including
  **`TestRouter_EnrichWithUnpaywall_Integration`** that proves the
  post-merge enrichment loop fills missing PDFURLs end-to-end.

### Sign-up gates (cycle 3 / Wave 2 prep)

Wave-2 (cycle 3) needs: Mixedbread (EU-resident reranker, headline EU-mode
companion), Perplexity Sonar (already in `~/code/.creds`), Cohere
(already; auto-disabled in `eu_strict`).

## [1.5.0] - 2026-05-10

Cycle 1 of the v2 multi-cycle plan (`project_plan/retrievr_v2.md`). Headline
goal: extract retrievr's retrieval logic as an importable Go library so liz,
nexus, and other in-process consumers no longer pay the MCP HTTP hop. Cycle
1 is **infrastructure-only** — no new providers, no breaking changes for
MCP consumers. Wave-1 providers (Exa, Brave, Linkup, Firecrawl, Unpaywall,
GitHub, Wikipedia) and the EU-GDPR mode arrive in v1.6.0 (cycle 2).

### Added
- **Public package `pkg/retrievr`** — importable surface with `Client`,
  `Search`, `Get`, `ListSources`, type aliases for every domain type, and
  the new credential / intent / EU-mode types. Cycle-1 escape hatch
  `NewClientFromConfig(configPath, logger)` lets external Go modules wire a
  Client end-to-end with one call. (Cycle 2 replaces this with a richer
  `NewClient(opts ...ClientOption)`.)
- **Context-based credentials.** New `retrievr.WithCredentials(ctx, map[string]string)`
  and `internal.WithCallCredentials(ctx, *CallCredentials)` carry per-call
  API keys keyed by source ID. The legacy `*CallCredentials` typed surface
  remains for the MCP wrapper during cycle 1.
- **Composable plugin-invocation middleware** (`internal/rtv.pluginchain.go`).
  Order outermost → innermost: retry → rate-limit → timeout → plugin.
  Equal-jitter exponential backoff (`RetryConfig`, `DefaultRetryConfig` —
  3 attempts, 250ms base, 8s cap). Each retry attempt acquires its own
  rate-limit token (matches liz DC-145).
- **Intent + per-intent fallback chains.** New `Intent` enum
  (`deep_research`, `quick_lookup`, `primary_source`, `code_provenance`,
  `news`, `reference`) on `SearchParams`. New `RouterFallbackConfig` maps
  intents → primary source set + ordered fallback list. When primary returns
  zero results (or all-fail), router walks the fallback list sequentially
  and short-circuits on the first hit. Cycle-1 default: `academic` chain
  (primary `[s2, openalex]`, fallback `[arxiv, crossref, europmc, pubmed]`)
  mapped to `IntentDeepResearch` and `IntentPrimarySource`.
- **`cmd/retrievr-cli`** — thin standalone CLI built on `pkg/retrievr.Client`.
  Subcommands: `search`, `get`, `sources`. Table or JSON output. Per-call
  API keys from `RETRIEVR_<SOURCEID>_API_KEY` env vars. Stdlib-only (no
  cobra).
- **Result fat-struct stub** (`pkg/retrievr/result.go`). Defines `Result`
  with `Kind` discriminator + per-kind data blocks (`PaperData`, `WebData`,
  `CodeData`, `NewsData`, `ModelData`, `DatasetData`, `EncyclopediaData`).
  Not yet emitted by plugins — plugins still produce `Publication` in cycle
  1; cycle 2 wires the new shape with a v1 `compat: "v1"` MCP shim.
- **EU-mode + audit-sink scaffolding** (`pkg/retrievr/eumode.go`,
  `audit.go`). `EUMode` enum (`off | eu_preferred | eu_strict`), `Region`
  classifications (EU, UK-adequacy, US, public-research-infrastructure,
  unknown), `AuditEvent` + `AuditSink` interface. Stubs only in cycle 1 —
  the gate, mode-filter, six audit hooks, and refusal path land in v1.6.0.
- **`SourceCapabilities.QueryIntents`** — informational field on every
  source's capabilities for intent-tag surfacing via `rtv_list_sources`.
- **Project plan** `project_plan/retrievr_v2.md` (~975 lines) covering
  cycles 1–3, package layout, middleware diagram, EU-mode hooks, risk
  register, sign-up checklist.

### Changed
- **`SourcePlugin` interface** — `Search(ctx, params)` and `Get(ctx, id, include, format)`.
  The `creds *CallCredentials` parameter is removed; plugins read
  credentials from ctx via `internal.CredentialFor(ctx, sourceID, fallback)`.
  All 10 providers (ArXiv, S2, OpenAlex, PubMed, Europe PMC, HuggingFace,
  CrossRef, DBLP, NASA ADS, bioRxiv) migrated. **Breaking** for any
  external Go consumer of the interface, but the package was effectively
  internal-only before this cycle.
- **`Router.searchOneSource` and `Router.Get`** rewritten to invoke plugins
  through the middleware chain. Plugin call latency unchanged in the happy
  path; transient errors now retry with backoff before bubbling up.
- **`RouterConfig.Retry`** (new YAML field, optional) — `RouterRetryConfig`
  with `max_attempts`, `base_delay`, `max_delay`, `jitter_fraction`. Zero
  values fall through to `DefaultRetryConfig`.
- **`RouterConfig.Fallback`** (new YAML field, optional) — `RouterFallbackConfig`
  with per-intent chain definitions. Zero values fall through to
  `DefaultFallbackConfig` (cycle-1 academic chain).
- **`Router.Search`** resolution precedence is now: explicit `Sources` arg
  → `params.Intent` chain lookup → `Router.defaultSources`. Behavior with
  empty `Intent` is byte-identical to v1.1.1.

### Fixed
- `CredentialFor(ctx, …)` is nil-ctx safe — returns the fallback when ctx is
  nil, rather than panicking on the value lookup.

### Tests
- `internal/rtv.pluginchain_test.go` — 15 tests covering chain ordering,
  per-attempt timeout, equal-jitter backoff math, retry-after-N-attempts,
  context-cancellation short-circuit, RouterRetryConfig zero-value
  substitution, transient-error predicate.
- `internal/rtv.fallback_test.go` — 9 tests covering chain primary
  resolution, fallback walk on zero-results / all-fail, no-walk when
  Sources explicit / Intent empty, `DefaultFallbackConfig` shape,
  `resolveFallbackConfig` zero-value substitution.
- `pkg/retrievr/smoke_test.go` — exercises every public identifier
  including the new credentials map, intent surface, and `DefaultFallbackConfig`.

## [1.1.1] - 2026-04-21

### Fixed
- **HTTP routing accepts both `/mcp` and `/mcp/`.** Go's `http.ServeMux` treats `mux.Handle("/mcp", …)` as an exact match and does not canonicalise a trailing slash — requests to `/mcp/` returned `404`. Reverse proxies that normalise empty remaining paths to `"/"` (notably Conduit's `singleJoiningSlash`) always forward as `.../mcp/`, so retrievr appeared dead behind a gateway even when it was perfectly healthy for direct `/mcp` callers. Registering the same `StreamableHTTPServer` handler against `/mcp/` as well closes the gap without changing upstream behavior. One-line change in `rtv.server.go`.

## [1.1.0] - 2026-04-06

### Added
- **CrossRef source plugin** — DOI-centric metadata for 150M+ scholarly works, JATS XML abstract stripping, date-parts conversion, polite pool via mailto
- **DBLP source plugin** — computer science bibliography with 7M+ publications, venue/conference metadata, custom author JSON unmarshaling
- **NASA ADS source plugin** — 16M+ astronomy/astrophysics records, API key auth, parallel array author/affiliation/ORCID mapping, Solr date filtering
- **bioRxiv/medRxiv source plugin** — preprint servers for biology/health sciences, date-range browsing (no keyword search), dual-server support, DOI retrieval
- **Environment variable API key overrides** — `RETRIEVR_{SOURCE}_API_KEY` env vars override YAML config, supports K8s secret injection
- Per-call credential support for NASA ADS (`ads_api_key`)

### Changed
- Source count expanded from 6 to 10
- Tool descriptions updated to list all 10 sources
- BibTeX journal key lookup now includes CrossRef and ADS metadata keys
- Default sources include crossref, dblp, ads (not biorxiv — requires date filter)

## [1.0.2] - 2026-04-06

### Added
- GitHub Actions CI workflow (build, vet, gofmt, golangci-lint, test -race, coverage >= 80%)

### Changed
- README rewritten for public release — fixed response field names, added Claude Code setup section, tighter structure
- MCP tool descriptions rewritten for LLM consumption — now mention concrete output fields
- Integration tests use OpenAlex+EuropePMC for multi-source test (S2 rate limits too aggressive without key)
- S2 integration test skips gracefully on 429/403 instead of failing

## [1.0.1] - 2026-04-06

### Added
- Plugin registry pattern (`rtv.registry.go`) — replaces 6 repetitive init blocks in main.go with data-driven factory map
- BibTeX journal field now checks all source-specific metadata keys (pubmed_journal, s2_journal, emc_journal, oa_venue, arxiv_journal_ref) with priority ordering
- Registry unit tests (`rtv.registry_test.go`) covering factories, initialization, disabled sources, unknown sources
- BibTeX cross-source journal tests covering all source keys and priority ordering

### Fixed
- `errors.Is()` used for `http.ErrServerClosed` comparison in server.go (was using direct equality)
- Dead code in `convertEMCFormat()` — added missing FormatJSON case
- Version test helpers (`SetVersionForTesting`/`ResetVersionForTesting`) protected with mutex against data races
- `TestE2EHuggingFace` race condition — removed erroneous `t.Parallel()` that conflicted with global state mutation
- Log/error constant mixing in router.go — separated `errDetailNoValidSources` from `logMsgNoValidSources`
- `io.LimitReader` int64 cast standardized across ArXiv, S2, and OpenAlex plugins
- `sort.Slice`/`sort.SliceStable` modernized to `slices.SortFunc`/`slices.SortStableFunc` (Go 1.21+)
- `sort.Strings` modernized to `slices.Sort` in router and cache
- Consistent `t.Cleanup(ResetVersionForTesting)` added across all version-mutating tests
- E2E test comment consistency for non-parallel tests

## [1.0.0] - 2026-04-05

### Added
- README.md with installation, configuration, and usage documentation
- LICENSE (MIT)
- ADRs.md documenting key architectural decisions
- docs/tool-reference.md — full reference for all three MCP tools
- docs/plugin-guide.md — guide for implementing new source plugins

### Changed
- Version bumped to 1.0.0

### Fixed
- BibTeX magic string constants extracted to named constants
- Rate limit metric semantics corrected
- Dockerfile `trimpath` flag added for reproducible builds
- Test coverage gaps from DC-11 code review

## [0.11.0] - 2026-03-29

### Added
- BibTeX generation from Publication metadata (`rtv.bibtex.go`), covering all sources
- Prometheus metrics (`rtv.metrics.go`) with a custom registry and nil-safe methods
- `/metrics` endpoint exposing Prometheus metrics
- Metrics: `rtv_search_total`, `rtv_search_duration_seconds`, `rtv_get_total`, `rtv_rate_limit_waits_total`, `rtv_cache_hits_total`, `rtv_cache_misses_total`
- Multi-stage Dockerfile: `golang:1.25-alpine` build → `alpine:3.21` runtime, non-root user, healthcheck
- Integration test suite (`//go:build integration`) for live API validation

## [0.10.0] - 2026-03-22

### Added
- HuggingFace plugin (`rtv.plugin.huggingface.go`) with three sub-sources: papers, models, datasets
- `content_type` routing to dispatch requests to the correct HuggingFace sub-source
- Cross-links between HuggingFace models/datasets and their associated papers

## [0.9.0] - 2026-03-15

### Added
- Europe PMC plugin (`rtv.plugin.europmc.go`) covering 40M+ biomedical publications
- REST/JSON search workflow with full-text XML retrieval support

## [0.8.0] - 2026-03-08

### Added
- PubMed plugin (`rtv.plugin.pubmed.go`) with two-phase XML workflow (ESearch + EFetch)
- MeSH term filtering support
- PMC full-text retrieval support

## [0.7.0] - 2026-03-01

### Added
- OpenAlex plugin (`rtv.plugin.openalex.go`) covering 250M+ scholarly works
- Inverted abstract index reconstruction to plaintext
- Polite pool support (mailto parameter in API requests)

## [0.6.0] - 2026-02-22

### Added
- Semantic Scholar plugin (`rtv.plugin.s2.go`) with citation and reference fetching
- Field selection for Semantic Scholar API requests
- Per-call API key support for Semantic Scholar

## [0.5.0] - 2026-02-15

### Added
- ArXiv plugin (`rtv.plugin.arxiv.go`) — first real source plugin
- ArXiv Atom XML API integration with search field mapping, date filtering, and pagination

## [0.4.0] - 2026-02-08

### Added
- MCP server with Streamable HTTP transport on `/mcp` (port 8099) (`rtv.server.go`)
- Three MCP tools: `rtv_search`, `rtv_get`, `rtv_list_sources` (`rtv.tools.go`)
- Request ID injection middleware (`rtv.middleware.go`)
- Per-tool logging middleware
- Graceful shutdown with configurable timeout

## [0.3.0] - 2026-02-01

### Added
- Source router with concurrent fan-out search across all requested plugins (`rtv.router.go`)
- Result merging with exact-match deduplication by DOI and ArXiv ID
- Round-robin interleaving for relevance sorting across sources
- Partial failure handling: working sources return results, failed sources reported in `sources_failed`
- Plugin contract test suite exercising every SourcePlugin implementation

## [0.2.0] - 2026-01-25

### Added
- Per-source token-bucket rate limiting (`rtv.ratelimit.go`) via `golang.org/x/time/rate`
- Per-credential rate limit buckets keyed by credential hash, TTL-evicted after 15 min inactivity
- Credential resolution (`rtv.credentials.go`) with priority order: per-call > server config > anonymous
- In-memory LRU cache with TTL (`rtv.cache.go`)

## [0.1.0] - 2026-01-18

### Added
- Go module scaffold (`go.mod`, `go.sum`)
- Unified types: `Publication`, `Author`, `SearchParams`, and related structs (`rtv.types.go`)
- `SourcePlugin` interface definition (`rtv.plugin.go`)
- YAML config loading with `go-playground/validator` struct validation (`rtv.config.go`)
- Sentinel error variables and constant error message strings (`rtv.errors.go`)
- Thread-safe version loading from `versions.yaml` or ldflags via `sync.Once` (`rtv.version.go`)
