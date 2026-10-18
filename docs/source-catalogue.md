# Source catalogue

`docs/source-catalogue.json` is the machine-readable description of every
built-in retrievr source, for governance surfaces that must render the full
set without hardcoding it (obol's per-org source allow-list is the first
consumer). Added in v2.28.0.

- **Go:** `retrievr.Catalogue() []retrievr.CatalogueEntry` and
  `retrievr.CatalogueJSON() ([]byte, error)` in `pkg/retrievr`. Pure: no
  client, no config file, no credentials, no network.
- **CLI:** `retrievr-cli catalogue` prints the same JSON and needs no config.
- **File:** `make catalogue` regenerates `docs/source-catalogue.json`;
  `TestCommittedCatalogueMatchesCatalogue` fails when the committed file
  drifts from the code.

## One producer

`rtv_list_sources` / `Client.ListSources` and `Catalogue()` read every static
field through one function (`catalogueEntryFor`), which asks the plugin
itself. `TestListSourcesAgreesWithCatalogue` pins the agreement. The only fact
the catalogue owns is the governance `category` (no plugin declares one);
`TestCatalogueCategoryAgreesWithDeclaredKinds` checks it against what each
plugin says it emits.

`ListSources` additionally reports what only a running instance knows
(`enabled`, live rate-limit headroom) and is limited to the sources that
instance has enabled. One field can legitimately differ: an operator may
narrow huggingface's content types via `sources.huggingface.extra`; the
catalogue reports the source's capability.

## Document shape (`schema_version` "1")

```json
{
  "schema_version": "1",
  "categories": ["academic", "web", "code", "patents_law", "places", "images", "social", "media", "other"],
  "sources": [ { ...entry... } ]
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | string | Source ID, as accepted by `rtv_search.sources` and per-call credentials. Never changes. |
| `name` | string | Display name. |
| `description` | string | One-paragraph description (written for LLM context). |
| `category` | enum | Governance grouping, one of `categories`. |
| `content_types` | string[] | What the source serves (`paper`, `video`, `place`, ...). |
| `kinds` | string[] | v2 `ResultKind`s it emits. `[]` for the ten cycle-1 scholarly sources, which emit papers through the v1 conversion. |
| `region` | enum | Provider residency in the existing `ResidencyTag` vocabulary: `EU`, `UK-adequacy`, `US`, `global`, `public-research-infrastructure`, `unknown`. Reported, never re-mapped — see `docs/residency.md`. |
| `dpa_status` | enum | `signed`, `covered-by-scc`, `n/a`, `unknown` (omitted when undeclared). |
| `subprocessor_url` | string | Provider's sub-processor list (omitted when undeclared). |
| `residency_verified_at` | date | When a maintainer last verified the residency claim (omitted when never). |
| `requires_api_key` | bool | The plugin refuses to run without a key. ⚠ `false` does not promise the UPSTREAM answers without one: `ads` declares an optional key in the plugin while NASA ADS itself requires a token, and `core`/`orcid` describe registration-gated access. |
| `accepts_credentials` | bool | A per-call key is honored at all (every key-requiring source plus the optional-key ones). |
| `supports_get` | bool | `false` means `rtv_get` can never succeed; use the search result's `url`. |
| `intents` | string[] | Query intents the source declares itself fit for. `[]` for the cycle-1 scholarly sources, which are reached through the intent CHAINS (`docs/intents.md`) rather than per-source tags. |

Empty lists are rendered as `[]`, never `null`.

## Stability contract

- An entry's `id` never changes; a removed source is a major release.
- Fields are only ever added. A removed/renamed field or a changed type bumps
  `schema_version`.
- The closed sets (`category`, `region`, `kinds`, `intents`) gain values only
  in a minor release. A consumer must render an unknown value rather than
  reject the document.
