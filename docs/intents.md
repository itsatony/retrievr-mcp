# Intents and Per-Intent Fallback Chains

retrievr's `Intent` enum lets callers declare *what* they're trying to do
and lets the Router pick the right primary source set + fallback chain.
Set via `SearchParams.Intent` or the MCP `intent` field.

## The six intents

| Intent | Cycle-1 default chain | Cycle-2/3 chain (when Wave-1 enabled) |
|---|---|---|
| `deep_research` | s2, openalex → arxiv, crossref, europmc, pubmed | s2, openalex, exa, linkup → arxiv, crossref, europmc, pubmed, wikipedia |
| `quick_lookup` | (none — uses `defaultSources`) | brave, exa → linkup, wikipedia |
| `primary_source` | s2, openalex → arxiv, crossref, europmc, pubmed | same as deep_research; Unpaywall enrichment activates when configured |
| `code_provenance` | (none) | github → s2 |
| `news` | (none) | brave → exa |
| `reference` | (none) | wikipedia |

The cycle-2/3 chains assume the corresponding providers are enabled in
config. With everything disabled, the Router falls through to
`defaultSources` (legacy behavior).

## Three-level source resolution

```
Router.Search precedence:
  (1) explicit `sources` arg (caller knows best)            — no fallback walk
  (2) params.Intent set                                     — chain-based primary + fallback
  (3) default                                               — Router.defaultSources, no fallback
```

When primary fan-out yields zero merged results (or all-fail), Router
walks the fallback list **sequentially**, short-circuiting on the first
hit. Walked sources contribute to `MergedSearchResult.SourcesQueried` and
the audit event's `fallback_walked: true` flag.

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
        fallback: ["s2"]                    # CS papers fallback for code searches
    intent_to_chain:
      deep_research:    "academic"
      primary_source:   "academic"
      quick_lookup:     "web"
      news:             "web"
      code_provenance:  "code"
      reference:        "web"
```

Empty chains map to `defaultSources` automatically. `DefaultFallbackConfig`
ships sensible cycle-2 defaults that you can override or replace
wholesale via `RouterConfig.Fallback`.

## Intent + EU-mode

The EU-mode gate runs **after** intent resolution. Under `eu_strict`,
the gate filters the chain's primary + fallback sets — non-EU providers
in the chain are simply skipped. If your `code_provenance` chain only
contains GitHub (US), `eu_strict` will produce zero results from that
chain rather than walking to a non-EU fallback. Either:

1. Add an EU-resident code provider to the chain (Wave-3 candidate)
2. Use `IncludePublicResearch=true` to admit S2 (PRI tier) under eu_strict
3. Switch to `eu_preferred` so the gate doesn't filter at all

## When NOT to use Intent

- The caller knows exactly which sources to query → pass `sources: [...]`
  explicitly. The Intent will be ignored and no fallback walk happens.
- The caller wants a hard guarantee about which providers are touched →
  same as above. `eu_strict` + explicit non-EU sources surfaces
  `ErrEUModeProviderConflict` rather than silently switching providers.

## Streaming + intents

`Router.Stream` uses Intent for source resolution but does **not** walk
the fallback chain (streaming can't make incremental decisions without
buffering). If the primary fan-out yields zero results in stream mode,
the channel closes empty. Callers needing fallback semantics should use
`Search` instead.
