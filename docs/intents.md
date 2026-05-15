# Intents and Per-Intent Fallback Chains

retrievr's `Intent` enum lets callers declare *what* they're trying to do
and lets the Router pick the right primary source set + fallback chain.
Set via `SearchParams.Intent` or the MCP `intent` field.

## The six intents

All six intents ship with a wired default chain (since v2.20.0). Chains
are filtered through the live plugin registry — paid plugins that lack a
configured key are silently dropped from the chain, so the effective set
matches what the tenant actually has. The canonical chain definitions
live in `DefaultFallbackConfig` (internal/rtv.router.go).

| Intent             | Primary set                                  | Fallback set                                                              | Purpose                                                |
| ------------------ | -------------------------------------------- | ------------------------------------------------------------------------- | ------------------------------------------------------ |
| `deep_research`    | `s2`, `openalex`                             | `arxiv`, `crossref`, `europmc`, `pubmed`, `dblp`, `ads`, `core`, `openaire` | Scholarly evidence-gathering, broad coverage          |
| `primary_source`   | `europmc`, `openalex`                        | `crossref`, `s2`, `arxiv`, `pubmed`, `core`, `openaire`, `zenodo`         | OA-biased scholarly retrieval. Unpaywall enrichment runs post-merge when configured |
| `quick_lookup`     | `kagi`, `mojeek`, `serpapi`                  | `brave`, `exa`, `linkup`, `wikipedia`                                     | Fast web answer; premium first, free fallback         |
| `code_provenance`  | `npm`, `pypi`, `crates`, `pkggodev`          | `github`, `arxiv`, `dblp`, `s2`                                           | Package registries → repo → CS literature              |
| `news`             | `newsapi`, `serpapinews`                     | `gdelt`, `brave`, `exa`, `wikipedia`                                      | Premium news APIs → open monitoring → web              |
| `reference`        | `wolframalpha`, `kgapi`                      | `wikidata`, `wikipedia`                                                   | Structured facts → knowledge graphs → encyclopedia     |

> When `Intent` is empty, Router falls through to `RouterConfig.DefaultSources`
> (no fallback walk). Callers passing an explicit `sources: [...]` array
> short-circuit Intent resolution entirely — the array wins.

## Three-level source resolution

```
Router.Search precedence:
  (1) explicit `sources` arg (caller knows best)            — no fallback walk
  (2) params.Intent set                                     — chain-based primary + fallback
  (3) default                                               — Router.defaultSources, no fallback
```

When primary fan-out yields zero merged results (or all-fail), Router
walks the fallback list **sequentially**, short-circuiting on the first
hit. Walked sources contribute to `MergedSearchResult.SourcesQueried`,
`MergedSearchResult.FallbackWalked` is set to `true`, and the audit event
records the same flag.

## Configuring custom chains

```yaml
router:
  fallback:
    chains:
      academic:
        primary:  ["s2", "openalex"]
        fallback: ["arxiv", "crossref", "europmc", "pubmed"]
      web:
        primary:  ["exa", "brave"]
        fallback: ["linkup", "wikipedia"]
      code:
        primary:  ["github"]
        fallback: ["s2"]
    intent_to_chain:
      deep_research:    "academic"
      primary_source:   "academic"
      quick_lookup:     "web"
      news:             "web"
      code_provenance:  "code"
      reference:        "web"
```

`resolveFallbackConfig` does NOT merge user config with defaults — supply
a complete `RouterFallbackConfig` or rely on `DefaultFallbackConfig`
wholesale. This avoids surprising defaults surviving when an operator
intentionally removes an intent.

## Intent + EU-mode

The EU-mode gate runs **after** intent resolution. Under `eu_strict`,
the gate filters the chain's primary + fallback sets — non-EU providers
in the chain are simply skipped. Practical implications:

- `code_provenance` under `eu_strict` will lose GitHub (US) and most of
  the package registries (US). The chain effectively becomes empty
  unless you opt in via `IncludePublicResearch=true` (admits npm/PyPI
  as PRI tier).
- `news` under `eu_strict` loses NewsAPI / SerpAPI News (both US). GDELT
  is a US project but the underlying corpus is global; treat as US.
- `quick_lookup` under `eu_strict` keeps Mojeek (EU-resident) but drops
  Kagi / SerpAPI / Brave / Exa.
- `reference` under `eu_strict` is essentially Wikidata + Wikipedia.

For regulated deployments where you want **fail-safe** rather than
**silent-drop**, use explicit `sources` instead of `Intent`:
`eu_strict` + an explicit non-EU source surfaces
`ErrEUModeProviderConflict` rather than silently switching providers.

## When NOT to use Intent

- The caller knows exactly which sources to query → pass `sources: [...]`
  explicitly. The Intent is ignored and no fallback walk happens.
- The caller wants a hard guarantee about which providers are touched →
  same as above. `eu_strict` + explicit non-EU sources surfaces
  `ErrEUModeProviderConflict` rather than silently switching providers.

## Streaming + intents

`Router.Stream` uses Intent for source resolution but does **not** walk
the fallback chain (streaming can't make incremental decisions without
buffering). If the primary fan-out yields zero results in stream mode,
the channel closes empty. Callers needing fallback semantics should use
`Search` instead.
