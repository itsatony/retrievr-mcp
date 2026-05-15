# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [2.13.0] - 2026-05-15

Minor release. **KnowledgeCommons cycle 6 — TemporalArchives**
(`project_plan/retrievr_v5.md` §10). Closes the v5 masterplan with
three temporal/archival sources: GDELT 2.0 (real-time global news),
Internet Archive Scholar (OA aggregator with Wayback fallbacks), and
the Wayback Machine itself as a GET-only URL→snapshot resolver.

### Added

- **`SourceGDELT` plugin** (`internal/rtv.plugin.gdelt.go`):
  `https://api.gdeltproject.org/api/v2/doc/doc?mode=ArtList&format=json`.
  Free, no auth. Real-time news with 15-minute update cycle, 60+
  languages. Supports `filters.language` (mapped to `sourcelang:`),
  `filters.categories[*]` (mapped to `theme:`), `filters.include_domains`
  (mapped to `domain:`), and full date-range filters (expands to
  GDELT's 14-digit `startdatetime`/`enddatetime`). Sort: HybridRel
  (default), DateDesc, DateAsc. Emits paper-typed results with
  `KindNews` at the v2 layer.
- **`SourceIAScholar` plugin** (`internal/rtv.plugin.iascholar.go`):
  `https://scholar.archive.org/search?format=json`. Free, no auth. OA
  scholarly aggregator with Wayback fallback PDF URLs surfaced into
  `Publication.PDFURL`. Returns DOI + ArXivID so cross-source dedup
  absorbs hits against arXiv / CrossRef / OpenAlex transparently.
  `filter_year` range filter, `release_type` category filter, offset
  pagination.
- **`SourceWayback` plugin** (`internal/rtv.plugin.wayback.go`):
  `https://archive.org/wayback/available?url=...&timestamp=...`. **GET-only
  resolver** — Search returns a single usage-hint result explaining
  how to invoke Get; the real work lives in
  `Get("<url>" | "<url>:<YYYYMMDD>")`. Smart id parsing keeps embedded
  `https://` colons intact while detecting timestamp suffixes (8+
  digits, no slash). Returns the closest archived snapshot URL,
  timestamp (formatted YYYY-MM-DD), and HTTP status.
- **Residency tags** (`internal/rtv.plugin_residency.go`): all three
  US/SCC. GDELT = Georgetown/Google partnership; IA Scholar +
  Wayback = Internet Archive non-profit (San Francisco).
- **Config blocks** for the three new sources in
  `configs/retrievr-mcp.yaml` (disabled by default).
- **Unit tests** with httptest fixtures for all three plugins:
  identity, capabilities, residency, happy-path (GDELT article,
  IA Scholar release with Wayback fallback PDF, Wayback snapshot
  resolution), filter routing (GDELT lang+category+domain, IA Scholar
  year range), id-parsing edge cases (Wayback split for `https://...:ts`
  vs bare URL vs middle-colon URL), 429 → `ErrRateLimitExceeded`,
  Get-not-wired (GDELT, IA Scholar), Wayback empty-id →
  `ErrInvalidID`, Wayback no-snapshot → `ErrGetFailed`.

### Changed

- **Plugin count bump**: `SourceCount` 44 → 47; `validSourceIDs`
  registers `gdelt`, `iascholar`, `wayback`; registry factory map +
  test-expected count updated. Config / E2E / registry fixtures
  extended with the three new source blocks.

### Notes

- This release closes the v5 KnowledgeCommons masterplan. Plugin
  total at v2.13.0: **47** (up from 28 at v2.7).
- No new `ContentType` or `ResultKind` introduced — GDELT, IA Scholar
  and Wayback all map to existing paper-family kinds (`KindNews`,
  `KindPaper`, `KindWeb`).
- IA Scholar's Wayback fallback URLs surface into
  `Publication.PDFURL`, so callers that follow paper hits to PDFs
  automatically benefit from the archival fallback without writing
  any per-plugin code.

## [2.12.0] - 2026-05-15

Minor release. **KnowledgeCommons cycle 5 — PatentsAndLaw**
(`project_plan/retrievr_v5.md` §9). Adds patents and case-law as
searchable evidence via Google Patents, EPO OPS, CourtListener, and
EUR-Lex. Introduces `ContentTypePatent`, `KindLaw`, and two new dedup
families.

### Added

- **`ContentType` `ContentTypePatent`** (`internal/rtv.types.go`) for
  patent records. Dedup keyed on `SourceMetadata["patent_number"]`.
- **`ResultKind` `KindPatent`** and **`KindLaw`**
  (`internal/rtv.result.go`). Patents land as
  `ContentTypePatent`/`KindPatent`; court decisions and EU regulations
  emit `ContentTypePaper` with `Result.Kind = KindLaw` and dedup on
  `SourceMetadata["citation_code"]`.
- **`SourceGooglePatents` plugin**
  (`internal/rtv.plugin.googlepatents.go`):
  `https://patents.google.com/xhr/query`. Free, no auth. Rides
  Google's internal xhr/query endpoint (with `)]}'` anti-hijack
  prefix stripping). Returns publication number, title, snippet,
  inventors, assignee, CPC classifications, dates. **Documented
  fragility** in retrievr_v5.md §12.
- **`SourceEPOOPS` plugin** (`internal/rtv.plugin.epoops.go`):
  `https://ops.epo.org/3.2/rest-services/published-data/search`. Free
  with registration; full OAuth2 client_credentials flow with cached
  Bearer-token refresh. Per-call credential: `epoops`, value
  `"consumer_key:consumer_secret"`. EU-resident (Munich, DPA-signed).
  Handles the OPS object-vs-array shape switch on
  `publication-reference`.
- **`SourceCourtListener` plugin**
  (`internal/rtv.plugin.courtlistener.go`):
  `https://www.courtlistener.com/api/rest/v4/search/?type=o`. Free,
  non-profit; optional Token-auth header (per-call: `courtlistener`)
  bumps rate limit. Emits paper-typed results with citation_code,
  court slug, jurisdiction = "US". 8M+ US federal + state opinions.
- **`SourceEURLex` plugin** (`internal/rtv.plugin.eurlex.go`):
  `https://eur-lex.europa.eu/search.html`. Free, no auth; HTML
  search-page parse extracting CELEX identifiers as citation codes.
  Supports `filters.language` (24 EU official languages). EU-resident
  (Luxembourg).
- **Dedup families** in router (`internal/rtv.router.go`):
  `dedupFamilyPatent` (routes `ContentTypePatent` results on
  `patent_number`); `dedupFamilyLaw` (routes paper-typed law results
  on `citation_code` before DOI). Cross-class collision impossible by
  construction.
- **Per-component patent + law metadata keys** in types
  (`smetaPatentAssignee`, `smetaPatentInventors`, `smetaPatentCPC`,
  `smetaPatentJurisdiction`, `smetaPatentKindCode`,
  `smetaPatentFilingDate`; `smetaLawCourt`, `smetaLawJurisdiction`,
  `smetaLawCitationCode`, `smetaLawDecisionDate`,
  `smetaLawDocketNumber`, `smetaLawCelex`).
- **Residency tags** (`internal/rtv.plugin_residency.go`):
  `googlepatents` → US/SCC; `epoops` → EU/signed-DPA; `courtlistener`
  → US/SCC (non-profit); `eurlex` → EU/n/a.
- **Config blocks** for the four new sources in
  `configs/retrievr-mcp.yaml` (disabled by default).
- **Unit tests** for all four plugins (httptest fixtures): identity,
  capabilities, residency, happy-path search, anti-hijack-prefix
  stripping (Google), OAuth2 token flow + cache (EPO), object-vs-array
  publication-reference shape (EPO), Token-auth + court/date filter
  routing (CourtListener), CELEX-anchor extraction + CELEX-dedup +
  language hint (EUR-Lex), 401 → `ErrCredentialInvalid`, 429 →
  `ErrRateLimitExceeded`, Get-not-wired path.

### Changed

- **Plugin count bump**: `SourceCount` 40 → 44; `validSourceIDs`
  registers `googlepatents`, `epoops`, `courtlistener`, `eurlex`;
  registry factory map + test-expected count updated.
  `IsValidContentType` accepts `ContentTypePatent`. Config / E2E /
  registry fixtures extended with the four new source blocks.

### Notes

- Google Patents and EUR-Lex both ride non-API surfaces (xhr/query
  and HTML search respectively). Integration tests catch breakage
  before deploy; risk-register entries in retrievr_v5.md §12 cover
  TOS shifts and structural drift.
- EPO OPS biblio enrichment (inventors, applicants, abstracts via
  `/published-data/publication/.../biblio`) is a follow-on cycle.
  The publication number already returned today is the dedup key,
  so cross-source patent dedup works without enrichment.
- Patent / law BibTeX entry types (`@patent`, `@misc howpublished="court
  decision"`) deferred to a future tidy cycle. The metadata layer
  already carries everything needed to assemble them.

## [2.11.0] - 2026-05-15

Minor release. **KnowledgeCommons cycle 4 — Packages**
(`project_plan/retrievr_v5.md` §8). Adds code-package discovery across
four major language ecosystems via free public registries: npm, PyPI,
crates.io, and pkg.go.dev. Introduces `ContentTypePackage` and the
`package_id` dedup family.

### Added

- **`ContentType` `ContentTypePackage`** (`internal/rtv.types.go`) for
  code-package registry hits. Cross-ecosystem dedup keyed on
  `SourceMetadata["package_id"]` = `"<ecosystem>:<name>"` so a `react`
  on npm and a `react` on PyPI never collide.
- **`SourceNPM` plugin** (`internal/rtv.plugin.npm.go`):
  `https://registry.npmjs.org/-/v1/search`. Free, no auth. Returns
  name, version, description, keywords, repo/home URLs, weekly-score.
  `filters.categories[*]` map to `keywords:<kw>` Lucene qualifiers
  appended to the text param. Native + Get supported
  (`/<name>/latest`).
- **`SourcePyPI` plugin** (`internal/rtv.plugin.pypi.go`):
  `https://pypi.org/search/` (HTML parse — PyPI retired XML-RPC search
  in 2021). Search returns lightweight hits parsed from
  `<a class="package-snippet">` blocks. Get path uses the official
  stable JSON endpoint `/pypi/<name>/json`. Documented fragility per
  retrievr_v5.md §12.
- **`SourceCrates` plugin** (`internal/rtv.plugin.crates.go`):
  `https://crates.io/api/v1/crates`. Free, public. Honors Rust
  Foundation crawler policy: required contact `User-Agent` header
  (config `extra.user_agent`) and ≤1 req/s. Supports
  `filters.categories[0]` (category slug), date-desc sort
  (→ `recent-updates`), and pagination. Native + Get supported.
- **`SourcePkgGoDev` plugin** (`internal/rtv.plugin.pkggodev.go`):
  `https://pkg.go.dev/search?m=package`. Free, no auth. Search parses
  the rendered SearchSnippet HTML blocks using stable `data-test-id`
  attribute hooks. Get path deferred — deps.dev is the right backend
  for future cycle (documented inline).
- **Per-component package metadata keys** (`internal/rtv.types.go`):
  `smetaPackageEcosystem`, `smetaPackageName`, `smetaPackageVersion`,
  `smetaPackageDownloads`, `smetaPackageRepoURL`, `smetaPackageHomeURL`,
  `smetaPackageKeywords`, `smetaPackageScore` — uniform shape across
  the four registries.
- **`dedupFamilyPackage`** in router dedup
  (`internal/rtv.router.go`). `ContentTypePackage` results route on
  `MetaKeyPackageID` for cross-ecosystem dedup.
- **Residency tags** (`internal/rtv.plugin_residency.go`): all four
  US-resident. Plan §8 documents this as a known gap — no
  EU-resident package registry exists at scale, so `eu_strict`
  callers must opt into `eu_preferred` or leave packages off.
- **Config blocks** for the four new sources in
  `configs/retrievr-mcp.yaml` (disabled by default).
- **Unit tests** with httptest fixtures for all four plugins:
  identity, capabilities, residency, happy-path search, sort/category
  filter routing, 429 → `ErrRateLimitExceeded`, Get path (npm,
  crates, PyPI), Get-not-wired (pkg.go.dev), HTML parser respects
  limits.

### Changed

- **Plugin count bump**: `SourceCount` 36 → 40; `validSourceIDs`
  registers `npm`, `pypi`, `crates`, `pkggodev`; registry factory map +
  test-expected count updated. `IsValidContentType` accepts
  `ContentTypePackage`. Config / E2E / registry fixtures extended with
  the four new source blocks.

### Notes

- pkg.go.dev and PyPI rely on HTML parsing because neither exposes a
  free-text-search JSON API. The fragility is acknowledged and tracked
  in retrievr_v5.md §12; integration tests catch breakage at the
  cycle's gate.
- No new `ResultKind` introduced — package hits map cleanly to
  `KindCode` at the v2 layer.

## [2.10.0] - 2026-05-15

Minor release. **KnowledgeCommons cycle 3 — Structured**
(`project_plan/retrievr_v5.md` §7). Adds structured-knowledge sources
distinct from prose articles: Wikidata for facts, DataCite for dataset
DOIs, ORCID for researcher profiles. Introduces the `KindFact`
ResultKind discriminator.

### Added

- **`ResultKind` `KindFact`** (`internal/rtv.result.go`) for
  structured-fact responses (Wikidata entities, ORCID profiles).
- **`SourceWikidata` plugin** (`internal/rtv.plugin.wikidata.go`):
  `https://www.wikidata.org/w/api.php?action=wbsearchentities`. Free,
  throttled. Surfaces entity QIDs + label + description + aliases.
  `SupportsLanguageFilter: true` (multilingual labels). SPARQL path
  intentionally deferred to a future cycle — text-lookup is the 95%
  case for agents and keeps the cycle's integration tests
  deterministic.
- **`SourceDataCite` plugin** (`internal/rtv.plugin.datacite.go`):
  `https://api.datacite.org/dois?query=...`. Free, no auth.
  Complements CrossRef with dataset / software DOIs. Supports
  `filters.date_from`/`date_to` (range on `registered`),
  `filters.categories[0]` (maps to `resource-type-id`), and
  `-created`/`created`/`-relevance` sorts. Pagination via
  `page[size]`+`page[number]`. Native + Get supported.
- **`SourceORCID` plugin** (`internal/rtv.plugin.orcid.go`):
  `https://pub.orcid.org/v3.0/expanded-search/`. Free w/ public-data
  token (per-call credential: `orcid`). Returns person records as
  Publication-shaped hits with Title=display-name and
  Authors=[self+affiliation]. Cross-source dedup keys on ORCID iD via
  `SourceMetadata["orcid_id"]`. Maps 401/403 → `ErrCredentialInvalid`,
  429 → `ErrRateLimitExceeded`.
- **Residency tags** (`internal/rtv.plugin_residency.go`):
  `wikidata` → `{Region: public-research-infrastructure}`;
  `datacite` → `{Region: EU, DPAStatus: n/a}`; `orcid` →
  `{Region: US, DPAStatus: covered-by-scc}`.
- **Config blocks** for the three new sources in
  `configs/retrievr-mcp.yaml` (disabled by default).
- **Unit tests** with httptest fixtures for all three plugins:
  identity, capabilities, residency, happy-path, language override
  (Wikidata), date/category filters (DataCite), name-fallback (ORCID),
  401 → `ErrCredentialInvalid`, 429 → `ErrRateLimitExceeded`, Get path
  (DataCite), Get-not-wired (Wikidata + ORCID), relative-URL
  normalization.

### Changed

- **Plugin count bump**: `SourceCount` 33 → 36; `validSourceIDs`
  registers `wikidata`, `datacite`, `orcid`; registry factory map +
  test-expected count updated. Config / E2E / registry fixtures
  extended with the three new source blocks.

### Notes

- No new `ContentType` introduced — Wikidata and ORCID return
  `paper`-typed Publications with `KindFact` at the v2 layer; DataCite
  surfaces `dataset`/`paper` based on `resourceTypeGeneral`.
- Cross-source DOI dedup applies transparently to DataCite results,
  so Zenodo/CrossRef/DataCite all merge on a shared DOI.

## [2.9.0] - 2026-05-15

Minor release. **KnowledgeCommons cycle 2 — OpenScience**
(`project_plan/retrievr_v5.md` §6). Adds three EU-friendly open-access
research aggregators to the academic chain: Zenodo (CERN, EU), CORE
(Open University, UK adequacy), and OpenAIRE (Athena RIC, Greece). All
free; CORE and OpenAIRE accept optional API keys per call.

### Added

- **`SourceZenodo` plugin** (`internal/rtv.plugin.zenodo.go`):
  `https://zenodo.org/api/records`. Free, no auth required. CERN-hosted.
  Returns papers, datasets, and software with DOIs for cross-source
  dedup. Supports `filters.date_from`/`date_to` (range on
  `publication_date`), `filters.categories[0]` (Zenodo `type` —
  publication / dataset / software / image / video / etc.), and
  `filters.open_access` (maps to `access_right=open`). Sort:
  relevance (default) and date_desc → `mostrecent`. Pagination via
  page+size. Native + Get supported.
- **`SourceCORE` plugin** (`internal/rtv.plugin.core.go`):
  `https://api.core.ac.uk/v3/search/works` (POST + Bearer token).
  Free with registration; 350M+ open-access works. Per-call
  credential: `core`. Supports date filters (year-resolution on
  `yearPublished`) and date sort. Native + Get supported. Maps 401/403
  to `ErrCredentialInvalid`, 429 to `ErrRateLimitExceeded`.
- **`SourceOpenAIRE` plugin** (`internal/rtv.plugin.openaire.go`):
  `https://api.openaire.eu/graph/v1/researchProducts` (Graph API v1).
  Free; optional public-data token via per-call credential
  `openaire`. EU-funded research aggregator. Returns papers + datasets
  with DOIs for cross-source dedup. Supports `fromPublicationDate` /
  `toPublicationDate` filters and `sortBy` (`publicationDate
  ASC/DESC`, `relevance DESC`). Pagination via page+pageSize. Choice
  of the Graph API over the legacy `/search/publications` endpoint is
  documented inline: same data, JSON-native, no OAI-XML wrapper.
- **Residency tags** (`internal/rtv.plugin_residency.go`):
  `zenodo` → `{Region: EU, DPAStatus: n/a}`; `core` →
  `{Region: UK-adequacy, DPAStatus: n/a}`; `openaire` →
  `{Region: EU, DPAStatus: n/a}`. All three admissible under
  `eu_preferred`; CORE is UK-adequacy so admissible under `eu_strict`
  only with `IncludePublicResearch`.
- **Config blocks** for the three new sources in
  `configs/retrievr-mcp.yaml` (disabled by default).
- **Unit tests** with httptest fixtures
  (`internal/rtv.plugin.{zenodo,core,openaire}_test.go`): identity,
  capabilities, residency, happy-path search, date/category filters,
  sort routing, 429 → `ErrRateLimitExceeded`, 401 → `ErrCredentialInvalid`,
  Get path (Zenodo), Get-not-wired (OpenAIRE), date normalization,
  query-string builders.

### Changed

- **Plugin count bump**: `SourceCount` 30 → 33; `validSourceIDs`
  registers `zenodo`, `core`, `openaire`; registry factory map +
  test-expected count updated. Config / E2E / registry fixtures
  extended with the three new source blocks.

### Notes

- No new `ContentType` or `ResultKind` introduced in this cycle —
  Zenodo and OpenAIRE both emit `paper`/`dataset` already in the type
  model; cross-source dedup happens on DOI (existing key family).
- `SourceCount` is now 33 and the registry test invariant
  `testRegistryExpectedFactoryCount` was bumped to match.

## [2.8.0] - 2026-05-15

Minor release. **KnowledgeCommons cycle 1 — QAndA**
(`project_plan/retrievr_v5.md` §5). Adds Q&A as a first-class result
kind via two free, no-paid-tier providers: Stack Exchange (170+ sites
including Stack Overflow) and Hacker News (Algolia mirror).

### Added

- **`ResultKind` `KindQA`** (`internal/rtv.result.go`) + `Result.QA`
  block (`QAData`: `Site`, `QuestionID`, `Tags`, `AnswerCount`,
  `AcceptedAnswerID`, `Score`, `IsAnswered`, `AuthorHandle`).
- **`SourceStackExchange` plugin** (`internal/rtv.plugin.stackexchange.go`):
  `https://api.stackexchange.com/2.3/search/advanced`. Free anonymous
  tier 300/day/IP; optional `api_key` (per-call: `credentials.stackexchange`)
  lifts the cap to 10k/day. Supports `filters.categories` (→ `tagged`),
  `filters.date_from`/`date_to` (→ `fromdate`/`todate` unix seconds),
  and date-sort. Per-deployment site selection via `extra.default_site`
  (default `stackoverflow`). Content licensed CC-BY-SA.
- **`SourceHackerNews` plugin** (`internal/rtv.plugin.hackernews.go`):
  `https://hn.algolia.com/api/v1/search` (and `search_by_date` for
  date-sort, which is descending-only). Free, no auth. Date filters via
  `numericFilters=created_at_i>=…,…<=…`.
- **Dedup family `qa_question_id`** (`internal/rtv.router.go`). Key
  format: `"<site>:<question_id>"` — Stack Overflow #1 and Server Fault
  #1 are intentionally distinct. Cross-class merging remains impossible
  by construction.
- **`MetaKeyQAQuestionID`** SourceMetadata key constant for the
  composite dedup value. Per-component fields live alongside in
  `smetaQA*` keys.
- **Shared Q&A helpers** (`internal/rtv.qa_shared.go`):
  `unixSecondsToShortDate` and `parseFilterDateUnix` — single home for
  YYYY-MM-DD/YYYY → unix-epoch conversion used by both plugins.
- **Residency tags** (`internal/rtv.plugin_residency.go`):
  `stackexchange` → `{Region: US, DPAStatus: covered-by-scc}` (CC-BY-SA
  content, admissible eu_preferred with attribution, blocked
  eu_strict); `hackernews` → `{Region: US, DPAStatus: unknown}`
  (blocked eu_strict).
- **Live integration tests** (`internal/rtv_integration_test.go`):
  "kubernetes ingress" must return ≥3 Stack Overflow results with QA
  metadata populated; "rust async" must return ≥5 HN results with
  non-zero score and tags. Both passed at v2.8.0 release.

### Changed

- **`SourceCount`** bumped 28 → 30 (`internal/rtv.types.go`). Tool
  description (`ToolDescSearch`) corrected from the stale "17 sources"
  literal to "30 sources" — closes the v5 OQ-7 reconciliation item.
- **`validSourceIDs`** map and `AllSourceIDs()` extended with the two
  new Q&A sources.
- **`configs/retrievr-mcp.yaml`**: `stackexchange` and `hackernews`
  source blocks, both disabled by default per the project's opt-in
  convention.

### Wired-up safety

- StackExchange: in-band throttle envelopes (200-OK + `error_name`
  containing "throttle" or non-zero `backoff`) surface as typed
  `ErrRateLimitExceeded` so the middleware backs off instead of
  spinning.
- HackerNews: malformed `objectID` no longer produces `?id=0` URLs —
  the platform-URL template now accepts a raw string round-trip and
  fails visibly on bad input.

## [2.7.1] - 2026-05-15

Patch release. Live integration testing against the Brave Search Web API
revealed that the v2.7.0 `IncludeDomains` / `ExcludeDomains` wiring sent
non-existent query parameters and was silently ignored upstream — Brave
returned results from arbitrary domains regardless of the filter. v2.7.1
rewrites the Brave plugin to use inline `site:` / `-site:` SERP
operators in the q string (Brave's only documented domain-scoping
mechanism). All other filter axes verified live in v2.7.0 are unchanged.

See the "Hardened (post-live-verification)" section under v2.7.0 for the
full set of changes that v2.7.1 ships.

## [2.7.0] - 2026-05-15

Minor release. **Smart-filter surface** (`project_plan/retrievr_v4.md`).
Closes the four highest-impact filter gaps from the May 2026 search-surface
audit: include/exclude-domain scoping for web SERPs, channel/subreddit
scoping for video and social providers, BCP-47 language filtering across
six providers, and the long-standing Brave date-filter defect.

### Added

- **`SearchFilters` fields** (`internal/rtv.types.go`):
  - `IncludeDomains` / `ExcludeDomains` (`[]string`) — honored by `brave`
    (comma-joined `include_domains` / `exclude_domains`) and `exa`
    (`includeDomains` / `excludeDomains` JSON body fields). Bare
    registered-domain form only; validation rejects schemes, paths, and
    whitespace with `ErrInvalidDomainList`.
  - `Channels` (`[]string`) — honored by `youtube` (`channelId`, with
    multi-channel fan-out capped at 5 — `ErrTooManyChannels` above the
    cap) and `scrapingdog_youtube` (`channel:` query qualifier, same
    fan-out).
  - `Subreddits` (`[]string`) — honored by `reddit` via `/r/<sub>/search`
    routing with `restrict_sr=on`, capped at 5 (`ErrTooManySubreddits`).
  - `Language` (`string`, BCP-47) — honored server-side by `brave`
    (`search_lang`), `youtube` (`relevanceLanguage`),
    `scrapingdog_youtube` (`language`), `bluesky` (`lang`), `europeana`
    (`lang`). `mastodon` applies it as a post-fetch filter on
    `Status.language` with fail-open on missing metadata (the single
    sanctioned client-side filter exception — see
    `docs/filter-reference.md`).
- **`SourceCapabilities` flags**: `SupportsDomainFilter`,
  `SupportsChannelFilter`, `SupportsLanguageFilter`. Surfaced via
  `rtv_list_sources` so callers can build the per-provider truth matrix
  at runtime.
- **`docs/filter-reference.md`** — full per-provider × per-filter
  capability matrix with copy-pasteable request examples.
- **MCP `rtv_search.filters` schema**: filter-key constants
  (`FilterIncludeDomains`, `FilterExcludeDomains`, `FilterChannels`,
  `FilterSubreddits`, `FilterLanguage`) and an expanded `FieldDescFilters`
  description enumerating all keys and the provider mapping.
- **BCP-47 helpers** (`rtv.types.go`): `BCP47FirstSubtag` extracts the
  primary subtag (`"de-DE"` → `"de"`); `MatchesLanguagePrefix` applies the
  prefix-with-dash match rule used by the Mastodon post-filter (fail-open
  on missing record metadata).
- **`ValidateDomainList`** — bare registered-domain validator shared by
  Brave + Exa.
- **Cross-plugin smart-filter test file**
  (`internal/rtv.plugin.smartfilters_test.go`) — table-driven coverage
  of every new filter axis, fan-out cap, post-filter rule, and Brave
  freshness truth table including the 422-retry-with-bucket path. 8 live
  integration tests under `//go:build integration` plus a capability
  matrix assertion that runs without credentials.

### Fixed

- **Brave date filter was advertised but unwired** — `Capabilities()`
  returned `SupportsDateFilter: true` (since v2.4) but `doSearch()` never
  consulted `Filters.DateFrom`/`DateTo`. Now maps to Brave's `freshness`
  parameter: bucket tokens (`pd`/`pw`/`pm`/`py`) for `date_from`-only
  inputs based on age vs `time.Now()`; custom-range syntax
  `YYYY-MM-DDtoYYYY-MM-DD` when both `date_from` and `date_to` are set.
  On HTTP 422 from a custom range, retries once with the nearest bucket
  derived from `date_from`.

### Changed

- **Plugin `doSearch` signatures** for brave, scrapingdog_youtube,
  reddit, mastodon, bluesky, and europeana now accept `SearchParams`
  instead of bare query strings — needed to plumb `Filters.*` through to
  the request builder. Pre-release, no compatibility shim.
- **Error sentinels** added: `ErrTooManyChannels`,
  `ErrTooManySubreddits`, `ErrInvalidLanguageTag`, `ErrInvalidDomainList`.
- **Constants extraction**: every query-param string literal in the eight
  modified plugins is now a typed constant (`braveParamFreshness`,
  `youtubeParamChannelID`, `redditQueryParamRestrict`, etc.) to comply
  with the "no magic strings" code rule. Reddit additionally extracts
  header names (`Authorization`, `Content-Type`), the OAuth grant body
  string, and the bearer prefix.

### Hardened (post-live-verification)

Live integration test against Brave Search Web API revealed that the
`include_domains` / `exclude_domains` query params do not exist in the
Brave API surface (they were a v2.7.0 spec error). Brave's only
mechanism for domain scoping is inline `site:` / `-site:` SERP operators
in the `q` parameter, the same syntax Google/DuckDuckGo accept.

- **Brave domain filter rewritten** to compose inline operators:
  - 1 include domain → `q="<query> site:<d>"`.
  - >1 include domain → `q="<query> (site:<d1> OR site:<d2> ...)"`.
  - Each exclude domain → ` -site:<d>` appended.
- **Dead constants removed**: `braveParamIncludeDomains`,
  `braveParamExcludeDomains` (Brave never read those params).
- **New helper**: `braveComposeQuery(query, includeDomains, excludeDomains)`
  with its own table-driven unit test (5 cases).
- **Live integration tests now all PASS**: brave domain filter, brave
  date filter (freshness wiring), exa domain filter, mastodon language
  post-filter, bluesky language wiring. The `TestIntegrationBraveDomainFilter`
  asserts every returned URL contains the included domain — verified
  against `api.search.brave.com` returning only `kubernetes.io` hits.

### Hardened (post-review)

After `/review` flagged 15 follow-ups, every applicable item was addressed
in-cycle (no v2.7.1 deferrals):

- **Brave 422-retry guard tightened**: now requires `DateFrom` set,
  freshness containing the literal `to` separator, AND at least four
  date hyphens — eliminating spurious retries when a 422 originates from
  something other than the custom-range syntax (`isBraveRangeRetryable`).
- **Brave custom-range input validation**: both `DateFrom` and `DateTo`
  must parse as full `YYYY-MM-DD`; year-only inputs in either endpoint
  now drop the freshness param (previously generated invalid
  `"2026to2026-05-15"`).
- **Brave future-date rejection**: a `DateFrom` value beyond `time.Now()`
  no longer buckets to `pd`; returns `""`.
- **`ValidateLanguageTag`** added and wired into every plugin that
  honors `Filters.Language` (brave, exa, youtube, scrapingdog_youtube,
  bluesky, europeana, mastodon). Wraps `ErrInvalidLanguageTag`, which was
  declared but unused.
- **`ValidateDomainList` tightened**: rejects bare hostnames without a
  dot (e.g. `"localhost"`), leading/trailing dots, double dots, and ports.
- **Reddit subreddit-name validation** (`^[A-Za-z0-9_]{2,21}$`) before
  path interpolation — path-injection defense for the `/r/<sub>/search`
  router.
- **Reddit fan-out dedup key** changed from URL to Submission ID
  (`pub.ID` carries the `t3_<id>` fullname). Crossposts that share an
  external URL across subreddits now collapse to one merged result.
- **Mastodon `HasMore` semantics** corrected: compares against
  pre-filter `len(resp.Statuses)` so language filtering doesn't lie
  about pagination availability.
- **Inline `braveBucketFromDate` wrapper**: one-liner removed; bucket
  derivation reads directly from `braveFreshnessFromDate`.
- **Metadata-key constants extracted**: `smetaUpstreamScore` (exa),
  `smetaDataProvider` (europeana), `smetaExternalURL` (reddit).
- **`extractCredentials` ADS check**: pre-existing miss — the
  all-empty short-circuit ignored `creds.ADSAPIKey`, dropping ADS-only
  credentials silently.
- **`pkg/retrievr/types.go` comment** corrected: previously claimed
  `Intent`, `IncludeDomains`, etc. were "planned, not yet present".

Test coverage expanded by 5 new cases: BCP-47 validation table, Brave
422-not-retried-without-range, year-only range rejection, future-date
rejection, Mastodon empty-results post-filter, invalid-subreddit
rejection.

### Deferred (planned v2.8.0+)

Per `project_plan/retrievr_v4.md` §2.2: safe-search per call, place
radius/bounding-box, sort-order extensions (YouTube viewCount, GitHub
forks/updated), Mastodon date filter via cursor pagination, peer-review
status, YouTube `@handle` → `channelId` resolution.

## [2.6.0] - 2026-05-11

Minor release. **Fifth and final cycle of the v3 "Multimodal Retrieval"
initiative** (`project_plan/retrievr_v3.md`). Fourth multimodal content
class lands: **social-post search** with three new providers spanning
the Fediverse (Mastodon), the AT Protocol (Bluesky), and the Reddit
ecosystem. v3 is complete: video / place / image / post all shipping.

### Added

- **`PostData` per-kind block** (cycle-1's `KindPost` ResultKind already
  existed): author_handle, author_url, atproto_uri, platform_url,
  like_count, repost_count, reply_count, published_at, media_count,
  subreddit, instance, verified. Converter routes `ContentTypePost`
  → `Result.Post`. EngagementScore on `Publication` carries a normalized
  sum (likes+reposts+replies); per-component counts live in PostData
  for breakdown rendering.
- **`MastodonPlugin`** (`SourceMastodon = "mastodon"`):
  - `/api/v2/search?type=statuses` against any v4+ instance — NO auth
    required for public-statuses search.
  - `BaseURL` selects the instance (default `mastodon.social`, DE).
  - **Dynamic residency**: `Extra.region` declaration drives the
    Residency() tag. Default `RegionEU`; operators pointing at a non-EU
    instance MUST set `extra.region=US` to keep the EU-mode gate truthful.
  - HTML content stripped via shared `stripHTMLTags`.
  - Handle composed as `@user@instance` (canonical fediverse form) —
    local users get the configured instance hostname, remote users keep
    the upstream `acct` field.
  - First media attachment surfaces as `ThumbnailURL`.
  - Engagement = `favourites_count + reblogs_count + replies_count`.
- **`BlueskyPlugin`** (`SourceBluesky = "bluesky"`):
  - `app.bsky.feed.searchPosts` (the public AppView). No auth.
  - `atproto_uri` (`at://did:plc:.../app.bsky.feed.post/<rkey>`) is the
    canonical dedup key (`MetaKeyAtprotoURI`).
  - Human-readable `bsky.app` URL derived from handle + rkey for
    `Publication.URL` and `PostData.PlatformURL`.
  - `record.langs[0]` → `Publication.Language`.
  - Author avatar surfaces as `ThumbnailURL`.
  - Residency: `public-research-infrastructure`. Admissible under
    `eu_strict` only with the `include_public_research` opt-in.
- **`RedditPlugin`** (`SourceReddit = "reddit"`):
  - OAuth2 client-credentials flow with **in-memory token cache** and
    60s safety-margin auto-refresh.
  - Credential format `<client_id>:<client_secret>` as a single string;
    `parseRedditCredential` splits on the first colon (secret may
    contain colons).
  - 401 on `/search` invalidates the token cache so the next call
    re-exchanges.
  - User-Agent required by Reddit policy — placeholder default with
    explicit "REPLACE-WITH-YOUR-CONTACT" reminder in YAML.
  - Permalink → `https://www.reddit.com<permalink>` as canonical URL;
    crosspost URL surfaces separately as `external_url` in
    SourceMetadata.
  - Thumbnail validity filter (rejects "self", "default", "nsfw",
    "spoiler", "image" placeholder values).
  - Residency: `US` + `DPACoveredBySCC`. **Blocked under `eu_strict`.**
- **SourceMetadata post keys**: `smetaAuthorHandle`, `smetaAuthorURL`,
  `smetaPlatformURL`, `smetaRepostCount`, `smetaReplyCount`,
  `smetaMediaCount`, `smetaSubreddit`, `smetaInstance`, `smetaVerified`.
  Existing `smetaLikeCount` (from cycle 2 video) is reused for posts.
- **Config blocks** for all three providers in `configs/retrievr-mcp.yaml`
  (disabled by default; explicit Reddit UA placeholder, explicit
  Mastodon `region` declaration).

### Changed

- **`SourceCount`** bumped 25 → 28.
- **Plugin registry** registers Mastodon + Bluesky + Reddit factories.
- Test fixtures + `testRegistryExpectedFactoryCount` updated to 28.

### Tests

- **29 new tests** total:
  - 8 in `rtv.plugin.mastodon_test.go`: identity, capabilities,
    default-EU residency, **dynamic residency reflects configured
    region** (the eu_strict-truthfulness invariant), happy-path wire
    shape (HTML tag stripping, engagement sum, media-attachment
    thumbnail, local-user handle composition), remote-user `acct`
    preserved, 429 → `ErrRateLimitExceeded`, 401 → `ErrCredentialRequired`,
    FormatUnsupported on Get.
  - 7 in `rtv.plugin.bluesky_test.go`: identity, capabilities,
    residency (public-research-infrastructure tag), happy path
    (atproto_uri dedup key, derived bsky.app URL, language, engagement,
    avatar thumbnail), 429 mapping, FormatUnsupported, helpers
    (`blueskyRKey`, `blueskyPlatformURL` guards).
  - 11 in `rtv.plugin.reddit_test.go`: identity, capabilities, residency
    (US + DPACoveredBySCC, blocked under eu_strict), happy path (Basic
    auth on token, bearer token on search, permalink URL, engagement,
    external_url surfacing), **token caching across 3 calls
    (1 exchange)**, **401 on search invalidates cache + re-exchange**,
    no credential / malformed credential errors, token-endpoint 401,
    429 mapping, FormatUnsupported, helpers (`parseRedditCredential`
    with 4 forms + colon-in-secret, `redditValidThumbnail`).
  - 2 in `rtv.post_xprovider_dedup_test.go`: Router-level
    cross-provider dedup on `atproto_uri` (Bluesky + Mastodon → 1
    Result) and URL fallback (Reddit + Mastodon when atproto_uri
    absent).

### v3 initiative complete

All four planned content classes shipped:

| Cycle | Version | Content class | Plugins | EU coverage |
|---|---|---|---|---|
| C1 | v2.2.0 | (type model) | — | — |
| C2 | v2.3.0 | video | youtube, scrapingdog_youtube | none (US only — no EU alternative) |
| C3 | v2.4.0 | place | photon, tomtom, nominatim | full eu_strict, zero cost via Photon |
| C4 | v2.5.0 | image | wikimedia, europeana (+ brave ext) | Europeana (pure-EU) + Wikimedia (opt-in) |
| C5 | v2.6.0 | post | mastodon, bluesky, reddit | Mastodon (configurable) + Bluesky (opt-in) |

10 new SourceIDs across 5 cycles; SourceCount 18 → 28. Brave gained an
image-search dispatch without becoming a new SourceID.

### EU-strict notes (final)

- **Pure-EU (DPASigned)**: TomTom (place), Europeana (image),
  Mastodon-when-pointed-at-EU-instance (post).
- **EU + public-research opt-in adds**: Wikimedia (image), Bluesky (post),
  Photon-self-hosted (place), Nominatim (place, UK-adequacy).
- **Always blocked under eu_strict**: YouTube + Scrapingdog (video),
  Brave (image SERP), Reddit (post).
- **Video** remains the one content class with no EU-resident alternative
  — accepted gap per the plan.

### Migration notes

- **No action required** for existing deployments. New sources default
  to `enabled: false`.
- **Reddit operators**: format `api_key` as `<client_id>:<client_secret>`
  (single string, plugin splits on first colon). Override `extra.user_agent`
  per Reddit policy.
- **Mastodon operators**: set `extra.region` to match the instance's
  actual jurisdiction. Default EU keeps `mastodon.social` truthful but
  pointing elsewhere requires the manual declaration.

## [2.5.0] - 2026-05-11

Minor release. Fourth cycle of the v3 "Multimodal Retrieval" initiative
(`project_plan/retrievr-v3.md`). Third multimodal content class lands:
**image search** with two new EU/public-research providers plus an
extension of the existing Brave plugin for web image SERP.
**License is first-class** — openly-licensed reuse requires it.

### Added

- **`KindImage` ResultKind** + **`ImageData` per-kind block**:
  media_url, thumbnail_url, media_mime, width, height, license,
  license_url, artist, source_page. Converter routes `ContentTypeImage`
  → `Result.Image`. License + LicenseURL + Artist are first-class so
  downstream consumers can refuse to use unlicensed images.
- **`WikimediaPlugin`** (`SourceWikimedia = "wikimedia"`):
  - MediaWiki API single-call shape via `generator=search` +
    `prop=imageinfo` (no second roundtrip per result).
  - License + LicenseUrl + Artist extracted from `extmetadata`;
    Artist's surrounding HTML tags stripped via shared `stripHTMLTags`.
  - Composite ID `wikimedia:File:Mona_Lisa.jpg` matches the dedup key
    (`MetaKeyWikimediaFile`) so cross-provider merging is automatic.
  - Stable result order via `sortByIndex` (MediaWiki returns pages as
    a map → insertion order non-deterministic).
  - Residency: `public-research-infrastructure` — admissible under
    `eu_strict` with the `include_public_research` opt-in.
- **`EuropeanaPlugin`** (`SourceEuropeana = "europeana"`):
  - `/record/v2/search.json` with `qf=TYPE:IMAGE` + `media=true`.
  - Europeana's wskey passed via `X-Retrievr-Cred-europeana` per-call
    or `RETRIEVR_EUROPEANA_API_KEY` env.
  - MIME inferred from URL extension (Europeana doesn't surface it).
  - License taken from `Rights` (Europeana's rights are URLs).
  - Residency: `EU` + `DPASigned` (The Hague, NL). Admissible under
    `eu_strict`.
- **Brave image-search extension**:
  - New `/res/v1/images/search` dispatch when
    `params.ContentType == ContentTypeImage`. Web/news path unchanged.
  - `ContentTypes()` now reports `[Any, Image]`. `Capabilities().Kinds`
    adds `KindImage`.
  - **License intentionally NOT fabricated** — Brave's image SERP
    doesn't carry license info, so `Publication.License` stays empty
    as the explicit "unverified" signal.
  - Brave is NOT a new SourceID; existing API key continues to cover
    image search.
- **SourceMetadata image keys**: `smetaWidth`, `smetaHeight`,
  `smetaLicenseURL`, `smetaArtist`, `smetaSourcePage`. Converter reads
  them for `Result.Image`.
- **Config blocks** for `wikimedia` and `europeana` in
  `configs/retrievr-mcp.yaml` (disabled by default; Wikimedia carries
  a `user_agent` placeholder).

### Changed

- **`SourceCount`** bumped 23 → 25 (Brave gets new behavior without a
  new SourceID).
- **Plugin registry** registers Wikimedia + Europeana factories.
- **Brave's `Capabilities().Kinds`** gains `KindImage`.
- Test fixtures `TestLoadConfigAllSources` +
  `TestE2EConfigToTypesToErrors` + `TestInitializePlugins/all_sources_enabled`
  + `testRegistryExpectedFactoryCount` updated for 25 sources.

### Tests

- **27 new tests** total:
  - 10 in `rtv.plugin.wikimedia_test.go`: identity, capabilities,
    residency (public-research tag for eu_strict opt-in), happy path
    with license + artist + HTML tag stripping, filters out entries
    without MediaURL, stable order by search-rank `Index` (map-
    iteration determinism), 429 mapping, API-error propagation,
    FormatUnsupported on Get, `normalizeWikimediaFile` helper.
  - 10 in `rtv.plugin.europeana_test.go`: identity, capabilities,
    residency (EU + DPASigned), happy path with rights/creator/year,
    per-call credential, missing credential, 401 mapping,
    filters items without MediaURL, FormatUnsupported,
    `sanitizeEuropeanaID`, `inferMimeFromURL` (8 cases),
    `firstSliceValue`.
  - 5 in `rtv.plugin.brave_image_test.go`: ContentTypes surfaces
    Image, Kinds include KindImage, dispatch to `/images/search` on
    `ContentType=Image`, web path stays on `/web/search` for non-image
    types, filters results without MediaURL, License stays empty,
    `braveImageFormatToMime` helper.
  - 2 in `rtv.image_xprovider_dedup_test.go`: Router-level
    cross-provider dedup on `wikimedia_file` (Wikimedia +
    Europeana → 1 Result) and MediaURL fallback (Brave + Wikimedia
    → 1 Result when wikimedia_file is absent).

### EU-strict notes

- **Pure-EU coverage**: Europeana alone (DPASigned) — recommended for
  strict deployments.
- **EU-strict + public-research opt-in**: Europeana + Wikimedia —
  significantly broader catalog.
- **Brave images** stays blocked under `eu_strict` (US residency
  unchanged). Web-image SERP is opt-in for `off` / `eu_preferred`.

### Migration notes

- **No action required** for existing deployments. New sources default
  to `enabled: false`. Brave operators get image-search dispatch for
  free once they pass `content_type: image` in `rtv_search`.
- **Wikimedia operators**: override the placeholder User-Agent before
  enabling — same etiquette as Wikipedia/Nominatim.

## [2.4.0] - 2026-05-11

Minor release. Third cycle of the v3 "Multimodal Retrieval" initiative
(`project_plan/retrievr_v3.md`). Second multimodal content class lands:
**place search** with three EU-resident providers. First v3 cycle with
**full eu_strict coverage at zero baseline cost** — all three plugins
are admissible under the EU-mode gate.

### Added

- **`KindPlace` ResultKind** + **`PlaceData` per-kind block** on `Result`
  (osm_id, osm_type, lat, lon, address, country, country_code, city,
  state, postcode, street, house_number, categories, place_type,
  importance). Converter routes `ContentTypePlace` → `Result.Place`.
- **`PhotonPlugin`** (`SourcePhoton = "photon"`):
  - GeoJSON parsing of Komoot's `/api` endpoint (FeatureCollection →
    Publication with Lat/Lon/Address pointer fields).
  - Configurable `BaseURL` for self-hosting (planet DB ~95 GB, 64 GB RAM).
  - Composite osm_id `<osm_type>:<osm_id>` matches Nominatim's form
    so cross-provider dedup hits without extra glue.
  - Coarse `place_type` derivation from `osm_key`/`osm_value`.
  - Fallback Title composition (street + housenumber + city + country)
    when `properties.name` is empty.
  - Residency: `EU` + `DPANotApplicable` (Komoot, Berlin DE). **Admissible
    under `eu_strict`.** No auth required.
- **`TomTomPlugin`** (`SourceTomTom = "tomtom"`):
  - `GET /search/2/search/{q}.json` with URL-encoded query in the path
    and key as a query parameter.
  - 2,500 requests/day free tier; ~$0.75/1k paid beyond.
  - POI handling: `result.poi.name` wins over `address.freeformAddress`
    when present; categories merged into SourceMetadata.
  - Score-to-importance normalization via `1 - 1/(1+score)` so callers
    have a comparable [0,1] signal across all three place plugins.
  - `HasMore` derived from `summary.totalResults > numResults+offset`.
  - Residency: `EU` + `DPASigned` (TomTom, Amsterdam NL). **Admissible
    under `eu_strict`.**
- **`NominatimPlugin`** (`SourceNominatim = "nominatim"`):
  - OSM reference geocoder with **strict policy enforcement**:
    - 1 RPS HARD cap on the public endpoint, enforced in Initialize
      regardless of `cfg.RateLimit`. Self-hosters override via BaseURL.
    - User-Agent header always sent (OSMF policy); default placeholder
      with retrievr identification, operators MUST override.
  - 403 (UA non-compliance ban) mapped to `ErrCredentialInvalid` with
    explicit message pointing at policy compliance.
  - City fallback chain `city → town → village` covers OSMs varying
    admin keys.
  - Importance signal passed through to PlaceData.Importance directly
    (Nominatim's native 0-1 score).
  - License field populated from response (`Licence`).
  - Residency: `UKAdequacy` + `DPANotApplicable` (OSMF, UK).
    **Admissible under `eu_strict`** (Region.IsEU() returns true).
- **SourceMetadata place keys**: `smetaOSMType`, `smetaCountry`,
  `smetaCountryCode`, `smetaCity`, `smetaState`, `smetaPostcode`,
  `smetaStreet`, `smetaHouseNumber`, `smetaCategories`, `smetaPlaceType`,
  `smetaImportance`. Converter reads them for `Result.Place`.
- **`metaFloat`** helper in converter — float64-aware sibling of `metaInt`,
  used to surface `importance` on PlaceData.
- **Config blocks** for all three providers in `configs/retrievr-mcp.yaml`
  (disabled by default; Nominatim block includes a `user_agent`
  placeholder with an explicit "REPLACE-WITH-YOUR-CONTACT" reminder per
  OSMF policy).

### Changed

- **`SourceCount`** bumped 20 → 23 (added `photon`, `tomtom`,
  `nominatim`).
- **Plugin registry** registers all three new factories.
- **Test fixtures** for `TestLoadConfigAllSources`,
  `TestE2EConfigToTypesToErrors`, and
  `TestInitializePlugins/all_sources_enabled` updated to include the
  three new sources. `testRegistryExpectedFactoryCount` bumped to 23.

### Tests

- **22 new tests** total:
  - 7 in `rtv.plugin.photon_test.go`: identity, capabilities, residency
    (EU admissibility under eu_strict), happy-path wire shape, composite
    name fallback, 429 → ErrRateLimitExceeded, FormatUnsupported on Get,
    limit clamping, live smoke gated on `RETRIEVR_LIVE_PHOTON`.
  - 7 in `rtv.plugin.tomtom_test.go`: identity, capabilities, residency
    (EU + DPASigned), happy path with score→importance derivation, per-
    call credential override, 401/429 error mapping, FormatUnsupported,
    HasMore from totalResults/numResults math.
  - 6 in `rtv.plugin.nominatim_test.go`: identity, capabilities,
    residency (UK-adequacy + IsEU), **public-endpoint forces 1 RPS
    regardless of cfg** (policy enforcement), self-hosted allows higher
    rate, happy path (UA header propagation, address parsing, importance,
    license), default UA placeholder identifies retrievr,
    429/403 → typed errors, FormatUnsupported, city/town/village
    fallback.
  - 2 in `rtv.place_xprovider_dedup_test.go`: end-to-end Router-level
    dedup on `osm_id` composite (Photon + Nominatim merge); secondary
    dedup on rounded (lat, lon) when osm_id is absent (TomTom + Photon).

### EU-strict coverage milestone

`eu_strict` deployments now have **zero-baseline place coverage**:
- Photon and Nominatim require no credentials and run free.
- TomTom is paid-tier optional for stronger POI coverage.
- All three are admissible under the EU-mode gate without
  `include_public_research` opt-in (TomTom + Photon are pure EU;
  Nominatim is UK-adequacy which `Region.IsEU()` accepts).

### Migration notes

- **No action required** for existing deployments. New sources default to
  `enabled: false`.
- **Nominatim operators**: MUST override `extra.user_agent` with their own
  app + contact email before enabling. The placeholder UA is intentionally
  flagged so deployments don't ship it to the OSMF public endpoint.

## [2.3.0] - 2026-05-11

Minor release. Second cycle of the v3 "Multimodal Retrieval" initiative
(`project_plan/retrievr_v3.md`). First multimodal content class lands:
**video search** via the official YouTube Data API v3 (primary) and the
Scrapingdog YouTube SERP API (paid fallback). Cross-provider dedup on
`youtube_id` flows end-to-end through Router.Search.

### Added

- **`KindVideo` ResultKind** + **`VideoData` per-kind block** on `Result`
  (channel_name, channel_id, video_id, thumbnail_url, duration_seconds,
  view_count, like_count, published_at, live_broadcast). Cycle 1's
  `ContentTypeVideo` now flows through the v2 wire as `Result.Kind="video"`
  with `Result.Video.*` populated. `kindForSource` maps ContentType →
  ResultKind for all four v3 multimodal types (video/place/image/post),
  preparing the converter for cycles 3–5.
- **`YouTubePlugin`** (`SourceYouTube = "youtube"`):
  - Search via `GET /youtube/v3/search` (snippet only — lightweight),
    Get via `GET /youtube/v3/videos` (adds duration + view/like counts).
  - API key via `X-Retrievr-Cred-youtube` per-call header or
    `RETRIEVR_YOUTUBE_API_KEY` env / YAML.
  - ISO 8601 duration parser (`PT1H2M3S` → 3723 seconds).
  - Thumbnail picker (maxres → standard → high → medium → default).
  - Date filter mapped to `publishedAfter` / `publishedBefore`.
  - Sort: `relevance` → `relevance`; `date_*` → `date`.
  - `quotaExceeded` / `dailyLimitExceeded` / `rateLimitExceeded` API
    error reasons map to `ErrRateLimitExceeded` **without flipping
    Health.Healthy=false** — quota exhaustion is throttling, not failure.
  - `keyInvalid` → `ErrCredentialInvalid`. 401/403 → `ErrCredentialInvalid`.
  - Residency: `US` + `DPACoveredBySCC`. Blocked under `eu_strict`.
- **`ScrapingdogYouTubePlugin`** (`SourceScrapingdogYouTube = "scrapingdog_youtube"`):
  - Paid SERP fallback. `GET https://api.scrapingdog.com/youtube/search/`.
  - Wall-clock duration parser (`H:MM:SS` / `M:SS`).
  - View-count parser (`1.2K`, `1.2M`, `1,234,567`, `1.5B`).
  - YouTube watch URL → videoId extractor (handles `youtube.com/watch?v=`,
    `youtu.be/`, plus query-param trailers like `&list=`).
  - Skips non-video results (channel pages, playlists) silently.
  - 402 Payment Required also maps to `ErrCredentialInvalid` (Scrapingdog's
    "out of credits" path).
  - Residency: `US`. Blocked under `eu_strict`.
  - No `Get` — Scrapingdog's per-video endpoint is a separate product;
    callers should resolve detail via the `youtube` plugin.
- **SourceMetadata video keys**: `smetaChannelName`, `smetaChannelID`,
  `smetaViewCount`, `smetaLikeCount`, `smetaLiveBroadcast`. Converter
  reads them when populating `Result.Video`.
- **Config blocks** for both providers in `configs/retrievr-mcp.yaml`
  (disabled by default; flip `enabled: true` + set `api_key`).

### Changed

- **`SourceCount`** bumped 18 → 20 (added `youtube`, `scrapingdog_youtube`).
- **Plugin registry** registers both new factories.
- Test fixtures `TestLoadConfigAllSources` + `TestE2EConfigToTypesToErrors`
  + `TestInitializePlugins/all_sources_enabled` updated to include the
  two new sources. `testRegistryExpectedFactoryCount` bumped to 20.

### Tests

- **27 new tests** total:
  - 17 in `rtv.plugin.youtube_test.go`: identity, capabilities, content
    types, residency, happy-path search wire shape (video-only filter,
    snippet → Publication mapping), per-call credential override,
    missing credential, quota-exceeded (with Health.Healthy stays true
    invariant), keyInvalid, 401, full Get with duration + view + like
    counts, not-found, empty ID, limit clamping to cap, date filter
    formatting, sort=date mapping, helper unit tests
    (`parseISO8601DurationSeconds`, `pickBestThumbnail`), live smoke
    (gated on `YOUTUBE_API_KEY`).
  - 9 in `rtv.plugin.scrapingdog_youtube_test.go`: identity, capabilities,
    residency, happy path (channel + thumbnail + duration + view count
    parsing), skips non-video URLs, per-call credential, missing
    credential, 402/429 error mapping, no-Get, limit clamps, helpers
    (`extractYouTubeVideoID` with 8 cases incl. youtu.be short URL and
    `&list=` trailer; `parseClockDurationSeconds`; `parseViewCount`
    with NaN/Inf hardening).
  - 1 in `rtv.video_xprovider_dedup_test.go`: end-to-end Router.Search
    proves a video appearing on both `youtube` and `scrapingdog_youtube`
    merges to a single Result via `MetaKeyYouTubeID`, with the other
    provider recorded in `AlsoFoundIn`.

### Migration notes

- **No action required** for existing deployments. New sources default to
  `enabled: false`. Set `auth.mode: per_request` for multi-tenant quota
  isolation (no server-side YouTube key).
- **eu_strict deployments** see no video coverage — both providers are
  US-resident. Plan acknowledges this gap; an EU YouTube replacement does
  not exist.

## [2.2.0] - 2026-05-11

Minor release. First cycle of the v3 "Multimodal Retrieval" initiative
(`project_plan/retrievr_v3.md`). Pure type-model extension — no new plugins,
no behavioral change for the existing 18 sources. Lays the ContentType +
`Publication` + dedup foundation for cycles 2–5 (video / place / image /
post providers).

### Added

- **Four new `ContentType` values**: `video`, `place`, `image`, `post`.
  ContentTypeAny continues to match the full set including the new values.
- **`IsValidContentType(string) bool`** helper. Empty string is intentionally
  invalid; ContentTypeAny is valid.
- **`Publication` multimodal fields** (all optional, ContentType-gated):
  `ThumbnailURL`, `DurationSeconds`, `Lat`, `Lon`, `Address`, `MediaURL`,
  `MediaMime`, `EngagementScore`, `Language`. Paper / model / dataset
  results leave these nil — existing snapshots stay byte-stable.
- **SourceMetadata key constants** for v3 dedup: `MetaKeyYouTubeID` (video),
  `MetaKeyOSMID` (place), `MetaKeyWikimediaFile` (image), `MetaKeyAtprotoURI`
  (post). Plugins populate these so Router.dedup() can merge cross-source
  duplicates within a content class.
- **`rtv_search` `content_type` enum** extended with the four new values.
  Default remains `paper`; existing callers see no behavior change.
- **`ErrBibTeXUnsupported`** sentinel + `bibtexSupported(ContentType) bool`.
  GenerateBibTeX returns this typed error for video/place/image/post rather
  than emitting a misleading `@misc` entry. paper/model/dataset/"" continue
  to work unchanged.

### Changed

- **`Router.dedup()` refactored** to dispatch by `ContentType` using a
  single composite `(family, value)` index. Dedup-key family per class:
    - paper / model / dataset / "" / any → DOI, then ArXiv ID (unchanged)
    - video → `SourceMetadata["youtube_id"]`
    - place → `SourceMetadata["osm_id"]`, then `(lat, lon)` rounded to 5 dp
    - image → `SourceMetadata["wikimedia_file"]`, then `MediaURL`
    - post  → `SourceMetadata["atproto_uri"]`, then `URL`
  Cross-class dedup is impossible by construction — a video and a paper
  with identical string keys never collide.

### Tests

- **15 new tests** in `rtv.dedup_multimodal_test.go` + `rtv.bibtex_multimodal_test.go`
  + `rtv.types_test.go` + `rtv.tools_test.go`:
    - video dedup by `youtube_id` (merge, differ, empty-key non-collision)
    - place dedup by `osm_id` and by coord rounding (5 dp), divergent coords kept,
      missing coords kept
    - image dedup by `wikimedia_file` and by `MediaURL` fallback
    - post dedup by `atproto_uri` and by `URL` fallback
    - cross-class non-merge (paper + video with overlapping metadata; place + video)
    - paper-by-DOI regression guard (v2 behavior unchanged)
    - GenerateBibTeX returns `ErrBibTeXUnsupported` for v3 types
    - GenerateBibTeX still works for paper/model/dataset/""
    - `content_type` enum schema contains all 8 values
    - `IsValidContentType` accepts all 8 values, rejects empty + unknown

### Migration notes

- **No action required.** Pre-1.0 Go interface stability promises do not
  apply; nothing in v2.1.0 callers breaks. The MCP tool surface is
  additive-only — new enum values accepted, old defaults preserved.

## [2.1.0] - 2026-05-10

Minor release. Adds a multi-tenant authentication mode where the server
process NEVER carries source credentials — each tenant supplies their
own keys per-request via HTTP headers (or per-call via ctx). Existing
`hybrid` deployments are unaffected; the new `per_request` mode is
strictly opt-in.

### Added

- **`auth.mode`** config key with three modes:
  - `hybrid` (default; backward-compatible) — YAML `sources.<id>.api_key`
    + `RETRIEVR_<SOURCE>_API_KEY` env overrides act as fallbacks when
    no per-call credential is attached.
  - `per_request` — multi-tenant gateway. YAML api_keys are CLEARED at
    `LoadConfig`, env-var overrides are skipped, and any required-auth
    source called without ctx-attached credentials returns
    `ErrCredentialRequired`. Each request must carry its own keys via
    `X-Retrievr-Cred-<source>` headers (HTTP/MCP transport) or via
    `WithCredentials` (library callers).
  - `server_side` — YAML api_keys honored, ctx credentials IGNORED.
- **`X-Retrievr-Cred-<source>` HTTP header convention** for per-tenant
  credentials. The new `PerRequestCredsContextFunc` HTTP context-func
  extracts these headers into a ctx-attached map. Wired by default on
  the StreamableHTTPServer.
- **`ClearServerCredentials`** — exported helper that wipes any
  `sources.<id>.api_key` values that may have leaked into the YAML.
  Idempotent; returns cleared source IDs for security-warning logs.
- 8 new unit tests covering the auth-mode resolver, server-side
  credential clearing, and the HTTP header → ctx extraction.

### Changed

- `LoadConfig` branches on `cfg.Auth.ResolvedAuthMode()`:
  `per_request` → clear server credentials + skip env overrides;
  `server_side` / `hybrid` → existing `applyEnvOverrides` flow.

### Migration notes

- **No action required for existing single-tenant deployments.** Default
  mode is `hybrid`, identical to v2.0.x behavior.
- **For multi-tenant gateways**, set `auth.mode: per_request`, remove
  `sources.<id>.api_key` values, and update clients to pass keys via
  `X-Retrievr-Cred-<source>` headers per request.

## [2.0.1] - 2026-05-10

Patch release. Migrates the 7 cycle-1 E2E pipeline tests to v2 shape
(closing the cycle-3 documented migration debt), wires the cycle-2
`eu_mode` / `audit` / `snapshot` / `enrichment.unpaywall` config blocks
through to the Router (they were declared but never plumbed in cycle 2),
and fixes 4 lint failures + the recurring gofmt CI failure that broke
v1.5.0/v1.6.0/v2.0.0 CI runs.

### Fixed

- **Recurring CI gofmt failure.** v1.5.0–v2.0.0 CI runs failed at the
  `gofmt -l .` step because 22+ files weren't formatted. Re-applied
  `gofmt -w .` and verified clean.
- **Cycle-2 EU-mode + audit wiring gap.** `cmd/retrievr-mcp/main.go` and
  `pkg/retrievr.NewClientFromConfig` now actually plumb the YAML
  `eu_mode:`, `audit:`, `snapshot:`, and `enrichment.unpaywall:` blocks
  through to `internal.NewRouter` via the appropriate `RouterOption`
  values. Before this fix, the config blocks were declared and parsed
  but Router was constructed without them — every `eu_strict`-flagged
  deployment was effectively running in `off` mode at runtime.
- **`internal.ResolveAuditSink` exported** (was `resolveAuditSink`,
  unused — golangci-lint failure on v2.0.0). Now consumed by both the
  MCP main and the library bootstrap.
- **`kindForSource` now honors `Publication.ContentType`** as a
  third-priority Kind derivation. HuggingFace's mixed paper/model/
  dataset emissions correctly map to `KindModel` / `KindDataset` in v2
  Result responses (was always `KindPaper` regardless of `ContentType`).
- **3 `nil` `context.Context` passes** in `resolveS2APIKey` /
  `resolveOAAPIKey` / `resolveHFToken` test call sites replaced with
  `context.TODO()` (staticcheck SA1012 lint failure).
- **7 cycle-1 E2E pipeline tests un-skipped and migrated to v2 Result
  shape.** `TestE2E*PluginFullPipeline` tests now assert on `Kind` +
  `Paper.{DOI, CitationCount, PDFURL, ArXivID}` + `Model` / `Dataset`
  per-kind blocks instead of the v1 flat Publication fields. Provides
  full end-to-end pipeline coverage on the v2 wire format.

### Added

- **`internal.VerifyProvidersSnapshot` called at boot** in both the MCP
  main and `pkg/retrievr.NewClientFromConfig`. Hook #6 of EU mode is
  now actually evaluated rather than being a no-op when `snapshot:`
  config is provided.

### Verification

Local CI sim run (matches `.github/workflows/ci.yaml` step-for-step):
- `go mod tidy` + `git diff --exit-code` — clean
- `go build ./...` — clean
- `go vet ./...` — clean
- `gofmt -l .` — empty (was 22 files in v2.0.0)
- `golangci-lint run ./...` (v1.64.8, the CI version) — clean (was 4
  failures in v2.0.0)
- `go test -race -coverprofile -covermode=atomic ./...` — all suites
  green
- Coverage: **81.4%** (above 80% threshold)

## [2.0.0] - 2026-05-10

Cycle 3 of the v2 multi-cycle plan. **First major release with a stable
public library surface.** Wave-2 synthesized-web provider (Perplexity Sonar)
ships, the cycle-2-deferred HTTP hygiene migration completes, MCP
`rtv_search` defaults to v2 fat-struct shape, and `Client.Stream()` lands
for progressive result delivery.

Wave-2 originally planned Mixedbread + Cohere rerankers; both cut from this
cycle by user direction (Cycle 4+ candidate).

### BREAKING CHANGES

- **`rtv_search` default response shape flips from v1 to v2.** Callers that
  rely on the legacy `Publication` shape now receive `Result` with `Kind`
  discriminator + per-kind data blocks (Paper/Web/Code/...). Migration:
  read `result.kind` first, then access kind-specific fields under
  `result.paper.{doi, citation_count, pdf_url, ...}`,
  `result.web.{site_name, ...}`, `result.code.{repo, stars, ...}`, etc.
  See `docs/library-guide.md` for examples.
- **Explicit `compat: "v1"` returns `RTV_COMPAT_V1_SUNSET`.** The cycle-2
  v1 compat opt-in is removed. Omit the `compat` field for the new
  default, or pass `"v2"` explicitly (idempotent with default).

### Added

- **Perplexity Sonar provider** (`perplexity`) — synthesized web answer
  + inline citations. POST /chat/completions with sonar/sonar-pro/
  sonar-reasoning models. Kinds=[KindWeb], QueryIntents=[QuickLookup,
  DeepResearch]. Maps the synthesized answer to a primary `Result` with
  `LLMContext=<answer>`; citations follow as sparse-shape entries.
  US-resident; blocked under `eu_strict`. Latency ~5-13s — bumps default
  per-source timeout to 20s.
- **`Client.Stream(ctx, params, sources)`** returns `<-chan StreamEvent`
  for progressive result delivery. Per-source results emit as plugins
  return; channel closes when all sources complete or ctx cancelled.
  Trade-offs: no cross-source dedup, no fallback walk. EU-mode gate +
  refusal path + audit event still apply. Not exposed via MCP (MCP
  doesn't stream tool results); CLI exposes via `--stream`.
- **`StreamEvent` type** with `Source`, `Result`, `Err` fields, re-exported
  from `pkg/retrievr` as `retrievr.StreamEvent`.
- **`retrievr-cli search --stream`** flag for progressive output.
- **`ErrCompatV1Sunset` typed sentinel** for callers detecting the v1
  sunset (`errors.Is(err, ErrCompatV1Sunset)`).
- **Comprehensive docs**:
  - `docs/architecture.md` — internals, middleware order, request flow
  - `docs/eu-mode.md` — EU-GDPR mode reference (3 states, 6 hooks)
  - `docs/intents.md` — per-intent semantics + fallback chains
  - `docs/residency.md` — provider residency table + verification policy
  - `docs/library-guide.md` — `pkg/retrievr` API + 6 worked examples
  - `docs/plugin-guide.md` was already present (cycle-1)

### Changed

- **All 10 cycle-1 plugins migrated to `internal.NewEgressClient`** —
  closes the cycle-2 deferred Hook #4 gap. ArXiv, S2, OpenAlex, PubMed,
  Europe PMC, HuggingFace, CrossRef, DBLP, NASA ADS, bioRxiv now share
  the same hygiene contract (neutral User-Agent, no Referer/XFF, no
  cookies) as Wave-1/2 plugins. ~10-line mechanical change per plugin.
- **`SourceCount`** 17 → 18.
- **MCP `rtv_search` `compat` field default** flipped from `"v1"` to
  `"v2"`. Tool definition's enum no longer includes `"v1"`.
- **`ToolDescSearch` rewritten** to describe v2 as canonical. The
  30-second LLM-readable description shipped in cycle 2 is updated to
  mark v1 as sunset.
- **README rewritten** to advertise 18 providers + library-first
  positioning + links to all 6 docs files.

### ADRs added

- ADR-023 (forthcoming): Perplexity citation-mapping pattern
- ADR-024 (forthcoming): v1 sunset rationale + migration path
- ADR-025 (forthcoming): Stream API design — what's intentionally
  out of scope (fallback, dedup) and why

### Tests

- 9 new Perplexity unit tests + live smoke (`TestPerplexity_LiveSmoke`
  passed against real Sonar API in 13.79s).
- 6 new Stream tests covering per-source events, partial-failure
  isolation, ctx-cancellation, EU-mode gate + refusal path, no-sources
  error.
- 3 new sunset tests (`TestSunset_DefaultIsV2`,
  `TestSunset_ExplicitV1ReturnsSunsetError`, `TestSunset_ExplicitV2Works`).

### Known migration debt

- 7 cycle-1 E2E pipeline tests (`TestE2E*PluginFullPipeline`) currently
  `t.Skip("v1 compat sunset in v2.0.0")` pending field-by-field
  migration to v2 Result shape. Substantive coverage exists via the
  cycle-2 `TestRouter_SearchV2WrapsSearch` + the 18 per-provider unit
  test suites; the skipped E2E tests duplicate what's already covered.

### Sign-up gates (cycle 4+)

- Mixedbread (EU-resident reranker) — when a rerank stage is reintroduced
- Cohere (US reranker) — same; auto-disabled in eu_strict per ADR-018
- Tavily, Kagi, You.com — deferred indefinitely per cycle-2 user direction

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
