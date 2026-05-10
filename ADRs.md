# Architectural Decision Records — retrievr-mcp

This document records the key architectural decisions made during the design and implementation of retrievr-mcp. Each record captures the context that drove a decision, what was decided, and the resulting trade-offs. All records have status **Accepted**.

---

## ADR-001: Flat Package Structure

**Status:** Accepted

### Context

All Go source lives in `internal/` as a single `package internal` with no sub-packages. Files are named with an `rtv.` prefix and organized by concern (e.g., `rtv.router.go`, `rtv.types.go`, `rtv.plugin.arxiv.go`). The `internal/` directory boundary enforces that no external package can import these types.

### Decision

A single flat package was chosen to avoid import cycles and keep all shared types in one namespace without indirection. Go's `internal/` convention enforces the external-access boundary without requiring sub-package visibility rules.

### Consequences

- All types and functions are visible to all code within the package, which simplifies cross-cutting concerns such as caching, rate limiting, and deduplication.
- No risk of circular imports between, for example, plugin code and router code.
- File count grows linearly as sources are added; at six sources the directory holds roughly twenty files, which remains navigable.
- Lack of sub-packages means the compiler cannot enforce internal module boundaries; discipline is maintained by naming convention alone.

---

## ADR-002: Three Tools Only (`rtv_search`, `rtv_get`, `rtv_list_sources`)

**Status:** Accepted

### Context

The server exposes exactly three MCP tools: `rtv_search`, `rtv_get`, and `rtv_list_sources`. Alternative designs would expose one tool per source (ArXiv, PubMed, Semantic Scholar, OpenAlex, HuggingFace, Europe PMC), yielding eighteen or more tools as sources grow.

### Decision

Fewer, more powerful tools are easier for LLM agents to discover and use correctly. Source selection is handled via a `sources` parameter within `rtv_search` and `rtv_get`, not via separate tool names. Tool constants are defined as `ToolNameSearch = "rtv_search"`, `ToolNameGet = "rtv_get"`, and `ToolNameListSources = "rtv_list_sources"`.

### Consequences

- An agent must learn three tools rather than a tool per source; adding sources does not increase the tool surface area.
- The agent must understand the `sources` parameter to target specific sources, which adds one degree of indirection compared to dedicated per-source tools.
- `rtv_list_sources` provides a runtime discovery mechanism so agents can enumerate available sources without hardcoding them.

---

## ADR-003: Lazy Depth — Lightweight Search, Explicit Get for Full Content

**Status:** Accepted

### Context

Academic source APIs charge differently for metadata versus full-content retrieval. Returning full text, references, and citations for every search result would be slow and wasteful when an agent is browsing or filtering results.

### Decision

`rtv_search` returns lightweight results: title, authors, publication date, and abstract. Full text, references, citations, and related works are only fetched when explicitly requested via `rtv_get` with the appropriate `include` fields (`full_text`, `references`, `citations`, `related`, `metadata`).

### Consequences

- Search latency is lower because only metadata endpoints are called.
- Agents that only need to identify relevant papers pay no cost for full-content retrieval.
- Workflows that need deep content require two steps: search to identify candidates, then get for each paper of interest.
- The `include` field list in `rtv_get` gives agents fine-grained control over what is fetched per call.

---

## ADR-004: Format Passthrough with Selective Normalization

**Status:** Accepted

### Context

Sources return content in different native formats: ArXiv returns XML, PubMed returns XML, Semantic Scholar returns JSON, HuggingFace returns Markdown. Lossy conversion between these formats discards structure and nuance. Content format constants (`FormatJSON`, `FormatXML`, `FormatMarkdown`, `FormatBibTeX`, `FormatNative`) are defined in `rtv.types.go`.

### Decision

Content is returned in its native source format by default. A `content_format` field on every result tells the agent exactly what format it received. No forced conversion is performed. This is the primary rule; ADR-007 documents the narrow exception for genuinely unusable raw formats.

### Consequences

- Zero information loss from format conversion.
- Agents must handle multiple formats and use `content_format` to dispatch appropriately.
- The `content_format` field is a stable contract that allows format-aware processing pipelines without parsing heuristics.
- Adding a new source does not require writing a converter; the source's native format is surfaced directly.

---

## ADR-005: Exact-Match Deduplication Only

**Status:** Accepted

### Context

Multiple sources frequently index the same paper. Cross-source deduplication is necessary to avoid returning the same paper multiple times. Fuzzy matching on title or author names risks false positives, which are more harmful in academic literature than surviving duplicates.

### Decision

Deduplication uses exact match on DOI and ArXiv ID only. The first occurrence in deterministic source-alphabetical order is kept as the primary record. Duplicate occurrences contribute their source ID to the `also_found_in` field and their citation count metadata is merged. Transitive cross-identifier dedup (e.g., linking a DOI match to a later ArXiv ID) is explicitly out of scope. The router fetches `limit * 2` results per source (`dedupHeadroomMultiplier = 2`) to provide headroom after deduplication.

### Consequences

- Deduplication has zero false positives: only records sharing an identical DOI or ArXiv ID string are merged.
- Papers that exist in multiple sources but lack a shared identifier will appear as separate results.
- `also_found_in` gives agents visibility into cross-source presence without discarding that signal.
- The 2x headroom factor means each source is queried for twice the requested limit, which increases upstream API load slightly.

---

## ADR-006: Per-Call Credentials with Three-Level Resolution

**Status:** Accepted

### Context

Different callers of the same server instance may hold different API keys with different rate quotas. Running one server instance per tenant would multiply infrastructure costs. Credentials are hashed for use as rate-limit bucket keys via `CredentialHash` in `rtv.credentials.go`, using SHA-256 truncated to 16 hex characters.

### Decision

Every tool call accepts an optional `credentials` map that overrides server-level defaults for that call only. Resolution order is: per-call credentials > server config defaults > anonymous (`__anonymous__`). Rate limit buckets are keyed per `(sourceID, credentialHash)` pair, so different callers' quotas are tracked independently.

### Consequences

- A single server instance can serve multiple tenants with different API keys and quotas without interference.
- Per-credential rate limit buckets (TTL-evicted after 15 minutes of inactivity) prevent one tenant's burst usage from consuming another tenant's quota.
- Per-call credential passing adds a small serialization overhead to each request.
- Sources that do not accept credentials ignore the field; `sourcesAcceptingCredentials` in `rtv.router.go` identifies which sources accept them.

---

## ADR-007: Selective Normalization as a Pragmatic Exception to Format Passthrough

**Status:** Accepted

### Context

ADR-004 establishes format passthrough as the default policy. Two specific cases produce raw API output that is genuinely unusable by agents without transformation: OpenAlex returns abstracts as an inverted index (a map of word to position list), and BibTeX entries are not returned by any source API but are useful to agents for citation workflows.

### Decision

Simple, reliable, information-preserving normalizations are permitted where the raw format is unusable. The OpenAlex inverted abstract index is reconstructed to plaintext by `reconstructAbstract` in `rtv.plugin.openalex.go`. BibTeX is assembled from normalized publication metadata in `rtv.bibtex.go` as a metadata assembly operation, not a content conversion. Both normalizations are deterministic and lossless for the data they handle.

### Consequences

- Agents receive a usable plaintext abstract from OpenAlex rather than a JSON structure they would have to reconstruct themselves.
- BibTeX output is available as an explicit format option without any source API natively providing it.
- The normalization exception must remain narrow: every addition requires justification that the raw format is genuinely unusable, and that the normalization is simple and reliable.
- The `content_format` field accurately reflects the output format after normalization.

---

## ADR-008: Round-Robin Interleaving for Relevance-Sorted Multi-Source Results

**Status:** Accepted

### Context

When results from multiple sources are merged, naive concatenation or score-based ranking would bias results toward whichever source returns the most or highest-scored results. Per-source relevance scores are not comparable across sources; each source's own ranking is internally meaningful but cross-source comparison is not.

### Decision

When `sort=relevance`, results are reordered by `roundRobinInterleave` in `rtv.router.go`: the first result from each source, then the second from each source, and so on. Source groups are ordered alphabetically by source ID for determinism. Within each source group, the original per-source ranking order is preserved.

### Consequences

- Every active source contributes to the top results proportionally, regardless of how many total results it returned.
- Ordering is deterministic for a given set of sources and results, which aids reproducibility.
- Per-source relevance ranking is respected within each source's contribution.
- If one source returns fewer results than others, its slots in later rounds are simply absent; no padding or re-ranking is performed.

---

## ADR-010: Plugin Registry Pattern

**Status:** Accepted

### Context

The original `main.go` contained six near-identical plugin initialization blocks — one per source (ArXiv, S2, OpenAlex, PubMed, EuropePMC, HuggingFace). Each block checked config enablement, created a zero-value plugin struct, called `Initialize`, handled errors, and inserted into a map. Adding a new source required copy-pasting another block.

### Decision

A plugin registry (`rtv.registry.go`) centralizes plugin creation via a `PluginFactories()` function that returns a `map[string]PluginFactory`. The `InitializePlugins(cfg, logger)` function iterates enabled sources, looks up the factory, creates the plugin, and initializes it. `main.go` calls this single function instead of six repetitive blocks.

### Consequences

- Adding a new source requires adding one line to `PluginFactories()` and implementing the plugin. No changes to `main.go`.
- Plugin initialization order is non-deterministic (map iteration), which is correct since plugins are independent.
- Unknown source IDs in config are silently skipped, consistent with the original behavior where only known sources had explicit blocks.
- The registry is unit-testable in isolation, improving coverage of the initialization path.

---

## ADR-011: bioRxiv Date-Range-Only Search

**Status:** Accepted

### Context

The bioRxiv/medRxiv API provides date-range browsing and DOI-based retrieval, but has no keyword search endpoint. Adding bioRxiv as a source plugin requires handling this limitation transparently.

### Decision

bioRxiv's `Search()` method requires a `date_from` filter. Without it, the plugin returns `ErrBiorxivDateRequired`. bioRxiv is NOT included in `default_sources` to prevent failures on keyword-only searches. Users opt in explicitly by adding `"biorxiv"` to the sources list. `Get()` works by DOI and tries all configured servers (biorxiv, medrxiv) in sequence.

### Consequences

- Users must explicitly request bioRxiv and provide date filters for search.
- Get-by-DOI works transparently across both biorxiv and medrxiv servers.
- The router's partial failure handling gracefully reports bioRxiv failures without blocking other sources.

---

## ADR-012: Environment Variable API Key Overrides

**Status:** Accepted

### Context

K8s deployments inject secrets via environment variables, but retrievr-mcp reads API keys from YAML config files. The sysop team needs a way to override config-file API keys with environment variables without modifying the config.

### Decision

After YAML config parsing and validation, `applyEnvOverrides()` checks for `RETRIEVR_{UPPER_SOURCE_ID}_API_KEY` environment variables (e.g., `RETRIEVR_S2_API_KEY`, `RETRIEVR_ADS_API_KEY`). If set, the env var value overwrites the corresponding source's `api_key` in the parsed config.

### Consequences

- K8s secrets map directly to env vars with a clear naming convention.
- No custom YAML processing or template engine needed.
- Env vars take precedence over YAML values — operators can override without touching config files.
- Only API keys are overridable (not arbitrary config fields), limiting the attack surface.

---

## ADR-013: Public `pkg/retrievr` Library Surface

**Status:** Accepted (cycle 1, v1.5.0)

### Context

ADR-001 codified a flat `internal/` package structure that, by Go's
visibility rules, is unreachable from any module outside `retrievr-mcp/`.
That worked while retrievr was MCP-only — every consumer reached it through
the HTTP transport. As liz and (eventually) nexus moved toward
in-process composition, the MCP hop became a measurable tax: per-call JSON
marshal/unmarshal, TCP round-trip, and a translation layer that obscured
typed errors and credential flow.

### Decision

Carve a public `pkg/retrievr` package whose surface is mostly type aliases
into `internal/`. External consumers import `pkg/retrievr` directly; the
MCP server (`cmd/retrievr-mcp`) and a new `cmd/retrievr-cli` are now thin
wrappers around the same `*Client`. Cycle 1 escape hatch:
`pkg/retrievr.NewClientFromConfig(configPath, logger)` does the full
bootstrap (config → plugins → rate limits → cache → router → client) so
external callers don't need to reach into `internal/`. Cycle 2 replaces
this with a richer `NewClient(opts ...ClientOption)` that takes the config
struct directly and exposes middleware / EU-mode / reranker / fallback
hooks via functional options.

### Consequences

- liz, nexus, and any other Go consumer can import retrievr's surface
  without the MCP hop. The liz integration spike at
  `liz/internal/retrievr/` validates this end-to-end with a live ArXiv
  search.
- The `internal/` package keeps its single-package layout (ADR-001 still
  stands); `pkg/retrievr` is a thin adapter, not a competing
  implementation. Cycle 2 may extract specific subpackages
  (`pkg/retrievr/internal/{rate,cache,dedup,fanout}`) when the middleware
  refactor lands.
- The MCP server still embeds the full upstream surface (no separate
  binary needed). LoC budget on `cmd/retrievr-mcp/` is enforced via CI in
  cycle 2 to keep the wrapper thin (≤300 LoC).
- Type aliases in `pkg/retrievr` mean cycle-2 evolution of `Publication`
  to the fat-struct `Result` is a coordinated change in two places (the
  `internal` definition + the `pkg/retrievr` re-export), not a breaking
  surface migration. MCP-side flattening shim handles the wire-format
  transition.

---

## ADR-014: Context-Based Credentials, Plugin Signature Without `creds`

**Status:** Accepted (cycle 1, v1.5.0)

### Context

Until v1.4.x, `SourcePlugin.Search` and `Get` carried a typed
`creds *CallCredentials` parameter. That worked for five well-known
sources (PubMed, S2, OpenAlex, HuggingFace, ADS) but locked the surface
to a fixed credential schema; adding Wave-1 providers (Exa, Brave,
Linkup, Firecrawl, GitHub) would have meant adding fields to
`CallCredentials` for every new source — leaking provider knowledge into
the upstream type and forcing every caller to change.

### Decision

Drop `creds` from the `SourcePlugin` interface. Credentials flow through
`context.Context` instead, via two helpers:

- `retrievr.WithCredentials(ctx, map[string]string)` — public,
  source-ID-keyed map. The intended path for cycle 2+ providers and
  external Go consumers.
- `internal.WithCallCredentials(ctx, *CallCredentials)` — legacy typed
  shape, populated by Router from MCP wrapper input during cycle 1.

`internal.CredentialFor(ctx, sourceID, fallback)` resolves both, preferring
the map when present. Diverges from the typical "don't put values in
context" Go advice because credentials are exactly the cross-cutting,
request-scoped, opaque-to-most-code-paths case context values were
designed for (`*http.Request` carries auth the same way).

### Consequences

- New providers add a single map key; no upstream-type churn.
- Plugin signatures shrank by one parameter, eliminating ~40 trivial
  argument forwards across the 10 existing plugins.
- The MCP wrapper's JSON unmarshal + ctx attachment is the single
  translation point. Direct Go callers skip it entirely.
- Tests that previously passed `*CallCredentials` directly now thread
  through ctx — adds one line per call site but makes the credential
  flow explicit. ~280 lines of test churn for 13 files.
- Cycle 2 retires the legacy typed surface entirely; cycle 1 keeps both
  to avoid a breaking change for the MCP wrapper.

---

## ADR-015: Plugin-Invocation Middleware Chain — Retry Above Rate-Limit

**Status:** Accepted (cycle 1, v1.5.0)

### Context

`Router.searchOneSource` hard-coded the per-source resilience pipeline:
rate-limit → timeout → plugin. That was fine for fan-out across stable
providers, but transient upstream failures (HTTP 429, network blips)
surfaced as immediate errors with no retry. Adding more layers (cache,
fallback, metrics, EU-mode gate) would have meant editing one monolithic
function repeatedly.

### Decision

Extract a closure-based middleware chain — `pluginOp = func(ctx) error`
with `pluginMW = func(pluginOp) pluginOp`. Three middleware
constructors land in cycle 1: `withTimeout`, `withRateLimit`, `withRetry`.
The fixed order is **outermost → innermost**: retry → rate-limit →
timeout → plugin. Retry sits ABOVE rate-limit so each attempt acquires
its own bucket token (matches liz DC-145 — putting retry below would
burn tokens that the inner code shouldn't have been issued).

`withRetry` implements equal-jitter exponential backoff: `delay ∈ [0, base
× growth^(attempt-1)]`, capped at `MaxDelay`. Default 3 attempts, 250ms
base, 8s cap. Predicate-based — `context.Canceled` / `DeadlineExceeded`
are never retried. Cycle 2 narrows the predicate when plugins start
wrapping upstream HTTP errors with typed `RetryableError{RetryAfter}`.

### Consequences

- Per-source resilience is now configurable via `RouterConfig.Retry` YAML;
  zero values inherit `DefaultRetryConfig`.
- Cycle 2's planned cache, fallback (extracted from the current Router
  level), and metrics middleware drop in cleanly — no more
  `searchOneSource` surgery.
- `closure-of-error` shape (rather than generic `Handler[T]`) keeps the
  surface small. Search and Get callers capture their result types via
  closure and reuse the same chain machinery without parallel generic
  instantiations. Cycle 2 reconsiders if a fallback middleware needs
  per-result-type hooks.
- 15 dedicated unit tests lock the contract: chain ordering,
  context-cancellation short-circuit, equal-jitter bounds, geometric
  growth + MaxDelay cap, RouterRetryConfig zero-value substitution.

---

## ADR-016: Per-Intent Fallback Chains, Three-Level Source Resolution

**Status:** Accepted (cycle 1, v1.5.0)

### Context

ADR-002's "fewer, more powerful tools" principle already lets callers
target sources via a `sources` parameter, but assumes the caller knows
which sources to ask. As wave-1 providers expand the source mix to web /
code / encyclopedia (cycle 2), most callers — especially LLM agents —
won't have that knowledge a priori. Naive "fan out to every enabled
source" wastes rate-limit budget on irrelevant providers and dilutes
ranking. We need a way for the caller to declare *intent* and let
Router pick.

### Decision

Add an `Intent` enum to `SearchParams` with values `deep_research`,
`quick_lookup`, `primary_source`, `code_provenance`, `news`, `reference`.
Add a `RouterFallbackConfig` mapping intents → primary source set + ordered
fallback list. Source resolution becomes three-level, in this precedence:

1. Explicit `sources` arg → use directly. No fallback walk; the caller is
   being prescriptive.
2. `params.Intent` set → look up chain. Fan out across primary set; if
   primary returns zero merged results (or all-fail), walk fallback list
   sequentially, short-circuiting on first hit.
3. Default → `Router.defaultSources`. No fallback walk.

Cycle 1 default config: only the `academic` chain
(primary `[s2, openalex]`, fallback `[arxiv, crossref, europmc, pubmed]`)
is meaningfully populated, mapped to `IntentDeepResearch` and
`IntentPrimarySource`. Wave-1 (cycle 2) adds web/code/news/reference
chains when the new providers land.

### Consequences

- LLM agents can pick intent without enumerating sources; existing direct-
  source callers are unaffected (empty `Intent` = legacy behavior).
- Fallback walk is short-circuit-on-first-hit by design — adding more
  fallback sources never amplifies cost when the first is healthy.
- Cycle 2's EU-mode gate (planned ADR-017) composes naturally with the
  chain: filter the primary + fallback lists pre-fanout, surface skipped
  sources in the response.
- 9 dedicated unit tests lock the resolution precedence + walk semantics.

---

## ADR-017: `cmd/retrievr-cli` as the Importable-Surface Validator

**Status:** Accepted (cycle 1, v1.5.0)

### Context

ADR-013 added the public `pkg/retrievr` library surface, but a Go module
that only ships an MCP-server `main` package + a public library still
needs evidence that the public surface actually works for an external
consumer. liz's adapter is one validator (ADR carryover from the spike),
but liz lives in another repo; we wanted an in-tree binary that exercises
the same import path with no special compile-time machinery.

### Decision

Add `cmd/retrievr-cli` — a stdlib-only thin wrapper (search / get /
sources subcommands) that imports `pkg/retrievr` and `internal` only for
the bootstrap helpers shared with `cmd/retrievr-mcp`. ~500 LoC total,
zero new module dependencies. Per-call API keys read from
`RETRIEVR_<SOURCEID>_API_KEY` env vars and threaded through
`retrievr.WithCredentials(ctx, …)` — exercising the new map-based
credential surface end-to-end.

### Consequences

- A user can run retrievr against a live API without spinning up the MCP
  server: `retrievr-cli search --sources=arxiv "transformer attention"`
  works on a fresh checkout.
- CI gains a third build target whose breakage signals public-surface
  regression independently from the MCP server's own evolution.
- Cycle 2 may add `--stream` (paired with `Client.Stream()`) and
  `--eu-mode=strict` flags as the upstream surface gains those features.

---

## ADR-018: Three-State EU-GDPR Mode (off / eu_preferred / eu_strict)

**Status:** Accepted (cycle 2, v1.6.0)

### Context

Customers in regulated markets need a verifiable assertion that retrievr's
fan-out only touches EU-resident providers. A binary "EU-only" toggle is
either too restrictive (forces operators to choose between EU compliance
and useful coverage of OA scholarly metadata hosted by US non-profits) or
too permissive (relies on operator-set-and-forget config that's invisible
to a downstream auditor).

### Decision

Three-state enum — `off | eu_preferred | eu_strict` — with an orthogonal
`include_public_research` opt-in for the strict mode. `eu_strict` admits
only EU-resident or UK-adequacy providers by default; the opt-in widens
admission to public-research-infrastructure providers (ArXiv, OpenAlex,
CrossRef, Semantic Scholar, PubMed, Wikipedia, Unpaywall) which are
US-hosted but scientifically public-good metadata. `eu_preferred` admits
everyone but tries EU first at the result-level (cycle-3 enhancement;
cycle-2 admits everyone in `eu_preferred`). `off` is no-op.

### Consequences

- Auditable contract: the gate's admission decision is recorded per call
  in `MergedSearchResult.SourcesSkipped` and the `AuditEvent`, so a
  downstream auditor can reconstruct mode + skipped-providers from logs.
- Customers in regulated markets get a single config knob with predictable
  semantics. Customers without that constraint pay no cost (default `off`).
- The `include_public_research` flag is documented as a known relaxation
  — not a hidden default — so adopters explicitly accept the cross-border
  scientific-metadata flow when they enable it.

---

## ADR-019: Centralized Residency Source-of-Truth in `internal`

**Status:** Accepted (cycle 2, v1.6.0)

### Context

Cycle 1 introduced `ResidencyTag` in `pkg/retrievr/plugin/residency.go` as
a forward-compatible stub. Cycle 2's EU-mode gate (Hook #2) needs to read
residency from the `SourcePlugin` interface, but that interface lives in
`internal` — and Go's `internal` visibility rule prevents `internal` from
importing `pkg/retrievr/plugin` (which already imports `internal` for the
SourcePlugin alias).

### Decision

Move residency types — `Region`, `DPAStatus`, `ResidencyTag` — into
`internal/rtv.residency.go` as the canonical source of truth. The public
`pkg/retrievr/plugin/residency.go` and `pkg/retrievr/eumode.go` re-export
via type aliases. A single `residencyVerifiedAt` constant in
`internal/rtv.plugin_residency.go` lets a quarterly residency audit bump
one date variable rather than touching every plugin.

### Consequences

- `SourcePlugin.Residency()` reads typed enums (not strings), eliminating
  a class of typos at provider authoring time.
- External Go consumers of `pkg/retrievr` see the same types via aliases
  — no surface change.
- A single grep for `residencyVerifiedAt` answers "when was the residency
  table last reviewed" without parsing per-file dates.

---

## ADR-020: Router-Side Publication→Result Conversion (vs. Plugin-Side Emission)

**Status:** Accepted (cycle 2, v1.6.0)

### Context

The plan called for plugins to emit the v2 fat-struct `Result` directly.
That would have required updating every cycle-1 plugin's result builder
to set `Kind = KindPaper` + `Paper: &PaperData{...}` plus newly-required
core fields (Snippet, Domain, Language, Score) — substantial mechanical
churn across 10 well-tested plugins.

### Decision

Plugins continue to emit `Publication`. Wave-1 plugins stuff kind-specific
data (snippet, domain, stars, repo, language) into
`Publication.SourceMetadata` using documented keys. The Router runs a
**post-merge converter** (`Router.toResult`) that:

- Picks `Kind` from `Capabilities().Kinds[0]`, with a per-result override
  via `SourceMetadata["kind"]`.
- Auto-derives Domain from URL when not set, auto-truncates Snippet from
  Abstract for non-paper kinds, computes `Score = 1/(1+rank)` from
  per-source rank position, attaches `Provenance` from the plugin's
  `Residency()`.
- Populates the matching kind-specific data block; leaves others nil.

### Consequences

- Cycle-1 plugins remain byte-stable. Wave-1 plugins emit Publication +
  SourceMetadata — a tiny additional mapping layer per provider, vastly
  simpler than dual-shape emission.
- The converter centralises rank-based scoring + domain extraction +
  snippet truncation, so future cycles can refine these signals once
  rather than per-plugin.
- Cycle-3 may invert this when v1 sunsets and a typed plugin-side `Result`
  emission justifies the surface change. Until then, the converter is the
  single source of truth for Result shaping.

---

## ADR-021: MCP `compat` Field for v1↔v2 Wire Coexistence

**Status:** Accepted (cycle 2, v1.6.0)

### Context

ADR-013 promised v1 MCP consumers would stay byte-stable across cycle-1+2.
v2's fat-struct `Result` is a substantial wire-shape change. We can't
flip the default without breaking every existing rtv_search caller; we
can't gate v2 behind a flag day without losing the value of shipping it.

### Decision

The MCP `rtv_search` tool gains an opt-in `compat` field. `"v1"` (default)
preserves the legacy `Publication`-shaped response. `"v2"` returns
`MergedSearchResultV2` with `kind`-discriminated `Result`s. `Client.Search`
and `Client.SearchV2` mirror this dichotomy on the Go side. v2.0.0 will
sunset `compat: "v1"` with `RTV_COMPAT_V1_SUNSET` per plan §6.1.4.

### Consequences

- LLM agents adopt v2 incrementally — the Wave-1 mixed-kind results
  (paper + web + code) are dramatically more useful through the v2 surface,
  giving early adopters a clear migration incentive.
- v1.6.0 ships zero breaking changes to the MCP wire — every v1.5.0
  consumer continues to work without code changes.
- Documentation has to track both shapes. The trade-off is acceptable for
  one cycle; v2.0.0 retires the v1 shape and reduces docs surface.

---

## ADR-022: Post-Merge Enrichment as Inline Router Hook (Cycle-2 Minimum-Viable)

**Status:** Accepted (cycle 2, v1.6.0)

### Context

Unpaywall fills in OA PDF links on paper results that have a DOI but no
upstream PDF URL. It's not a search source (no keyword index) — only a
DOI lookup. Wiring it as a regular fan-out source would make zero sense;
wiring it as a generic "Enrichment" middleware would require typed
abstractions that don't exist yet.

### Decision

Cycle 2 ships a **direct Router method** —
`Router.enrichWithUnpaywall(ctx, pubs)` — invoked post-dedup in
`Router.Search`. Configuration via `Router.WithUnpaywallEnrichment(*UnpaywallPlugin)`
option. The Unpaywall plugin remains in the registry (so it surfaces in
`rtv_list_sources` and gets its residency tag honored by EU-mode) but its
`Search()` returns empty.

### Consequences

- v1.6.0 ships a working enrichment loop without a half-baked generic
  abstraction. The implementation is one method + one option + one
  optional plugin reference on Router.
- Cycle 3 promotes enrichment to a typed `Enrichment` middleware
  interface (Firecrawl scrape + future re-rankers will plug into it).
- The cycle-2 wiring is rapidly removable when the typed interface lands
  — no data-format churn, only plumbing churn.

---

## ADR-023: Perplexity Citation-Mapping Pattern (Synthesized Answer + Sparse Citation Results)

**Status:** Accepted (cycle 3, v2.0.0)

### Context

Perplexity Sonar is fundamentally different from every other retrievr
provider — it returns *one synthesized answer* per query plus a list of
URLs cited during synthesis. Existing providers return ranked individual
results.

### Decision

Map one Sonar response to **one primary `Result`** with `Kind=KindWeb`,
`LLMContext=<full answer>`, `URL=<first citation>`, plus **N sparse
follow-up Results** — one per citation URL — with hostname-derived
titles. The primary result's `Snippet` is a truncated answer; the
citation results' titles fall back to hostnames since Sonar doesn't
surface per-citation titles.

### Consequences

- LLM agents asking `quick_lookup` queries get a synthesized answer in
  `LLMContext` ready for direct injection into a prompt, plus the
  citation URLs as separate results for per-source verification.
- The sparse-citation entries are intentionally minimal — better
  metadata would require a follow-up `rtv_get` per URL (cycle-4
  Firecrawl-enrichment opportunity).
- Sonar's high latency (~5-13s) makes it a poor fit for fan-out under
  tight ctx deadlines; the plugin defaults its per-source timeout to
  20s and is intentionally NOT in the default cycle-3 `quick_lookup`
  chain primary set.

---

## ADR-024: v1 Compat Sunset in v2.0.0

**Status:** Accepted (cycle 3, v2.0.0)

### Context

Cycle 2 introduced the v2 fat-struct `Result` shape behind an opt-in
`compat: "v2"` MCP arg. The plan (project_plan/retrievr_v2.md §6.1.4)
called for v2.0.0 to retire the v1 default.

### Decision

`rtv_search` default flips to v2. Explicit `compat: "v1"` returns
`RTV_COMPAT_V1_SUNSET` typed error pointing at the CHANGELOG migration
guide. The `Compat` enum in the tool definition no longer advertises
`"v1"` (clients still see it via the description's sunset note).

### Consequences

- LLM agents and direct MCP consumers must read `result.kind` before
  reaching for kind-specific fields. The 30-second tool description
  documents this.
- Existing MCP integration tests asserting on v1-shape fields are
  `t.Skip`'d pending field-by-field migration. The substantive contract
  is covered by the cycle-2 `TestRouter_SearchV2WrapsSearch` + per-
  provider tests.
- The `Client.Search` Go API still exists and returns the v1
  `*MergedSearchResult` shape for direct Go consumers that haven't
  migrated. v2.1.0 may deprecate it; v3.0.0 may remove it. v2.0.0 keeps
  it as a graceful migration path.

---

## ADR-025: Stream API — What's Intentionally Out of Scope

**Status:** Accepted (cycle 3, v2.0.0)

### Context

Slow providers (Perplexity Sonar at 5-13s, Firecrawl scrape at 3-8s)
make the merge-then-return contract painful for callers that want
progressive UI rendering. Plan §6.1.3 listed `Client.Stream()` as a
stretch goal.

### Decision

Ship `Router.Stream` + `Client.Stream` returning `<-chan StreamEvent`.
Per-source results emit as plugins return — no waiting for cross-source
merge. **Three things are intentionally out of scope for the streaming
path:**

1. **Cross-source dedup** — would require buffering the full set,
   defeating the streaming purpose.
2. **Fallback walk** — incremental decisions can't lookahead; the
   primary set's full result count isn't known until all primary
   sources finish.
3. **MCP exposure** — MCP tool results aren't streaming; only the CLI
   exposes via `--stream`.

The audit event still emits at channel close. EU-mode gate, refusal
path, and HTTP hygiene all still apply.

### Consequences

- Stream consumers see duplicate results across sources sharing a DOI
  or canonical URL. Document this in the `Client.Stream` godoc.
- Callers needing dedup or fallback must use `Search`/`SearchV2`.
- The pattern leaves room for a v2.1+ "stream-then-final-merge" mode
  where individual results stream first, followed by a terminal merged
  event. Not in v2.0.0 because no concrete consumer needs it yet.
