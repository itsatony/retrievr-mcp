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
