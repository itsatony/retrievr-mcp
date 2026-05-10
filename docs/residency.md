# Provider Residency Table

Source of truth: `internal/rtv.plugin_residency.go` (cycle-1 plugins) +
each Wave-1/2 plugin's `Residency()` method. Surfaced at runtime via
`rtv_list_sources` (`Region`, `DPAStatus`, `SubprocessorURL`,
`LastVerifiedAt` fields per `SourceInfo`).

## Cycle-3 (v2.0.0) snapshot

| Source | Region | DPA | Notes |
|---|---|---|---|
| **DBLP** | EU | n/a | Schloss Dagstuhl, Germany — admitted under eu_strict |
| **Europe PMC** | UK-adequacy | n/a | EBI, Hinxton — admitted under eu_strict (UK adequacy decision) |
| **Linkup** | EU | signed | Linkup SAS, France — primary EU web provider; signed DPA |
| ArXiv | public-research-infrastructure | n/a | Cornell, US — eu_strict opt-in via `IncludePublicResearch=true` |
| OpenAlex | public-research-infrastructure | n/a | OurResearch, US — same opt-in |
| CrossRef | public-research-infrastructure | n/a | same opt-in |
| Semantic Scholar | public-research-infrastructure | n/a | AI2, US — same opt-in |
| PubMed | public-research-infrastructure | n/a | NLM, US — same opt-in |
| Wikipedia | public-research-infrastructure | n/a | WMF, US/global — same opt-in |
| Unpaywall | public-research-infrastructure | n/a | OurResearch, US — same opt-in (enrichment hook) |
| HuggingFace | US | unknown | Blocked under eu_strict |
| NASA ADS | US | unknown | Harvard CfA — blocked under eu_strict |
| bioRxiv | US | unknown | CSHL — blocked under eu_strict |
| Exa | US | unknown | Blocked under eu_strict |
| Brave Search | US | unknown | Blocked under eu_strict (verify Pro tier separately) |
| Firecrawl | US | unknown | Blocked under eu_strict |
| GitHub | US | unknown | Blocked under eu_strict |
| Perplexity | US | unknown | Blocked under eu_strict |

## What "admitted under eu_strict" means

The EU-mode gate (Hook #2) admits providers whose `Region` is one of:

- `RegionEU` (EU/EEA member states)
- `RegionUKAdequacy` (UK, currently covered by EU adequacy decision)
- `RegionPublicResearch` IF `IncludePublicResearch=true` is set

Everything else is filtered out pre-fanout with
`reason: "eu_strict_mode"` in `MergedSearchResult.SourcesSkipped`.

## Verification policy

`ResidencyTag.LastVerifiedAt` is set per-provider at registration. The
canonical date for cycle-1 scholarly providers lives in
`internal.residencyVerifiedAt`. Cycle-3 ships the policy:

- Bump `residencyVerifiedAt` per quarterly residency audit.
- CI warns when any provider's `LastVerifiedAt` is older than 90 days
  (cycle-3 deferred — currently warn-only at boot; cycle-4 promotes to
  CI gate).
- CI hard-fails when older than 180 days (cycle-4).

## How to verify a provider's residency claim

1. Read the provider's published Data Processing Addendum / Privacy Policy.
2. Check the Region declaration in the provider's DPA against
   the value in `Residency()`.
3. If they match — ok. If they diverge — open a PR updating the plugin's
   `Residency()` and bumping `residencyVerifiedAt`.

retrievr does NOT independently verify upstream routing claims — the
ResidencyTag reflects what the provider says about itself. Adopters with
strict compliance requirements should track the `SubprocessorURL` for
material changes.

## Adding a new provider

When adding a Wave-2/3 provider:

1. Implement `Residency() ResidencyTag` returning the correct Region +
   DPAStatus.
2. Set `LastVerifiedAt: residencyVerifiedAt` (or a per-plugin date if
   verification cadence diverges).
3. Add the provider to this table.
4. If the residency is anything other than `RegionUS / DPAUnknown`,
   include the supporting documentation reference (DPA URL, etc.) in
   the PR description.
