# Search Filter Reference

`rtv_search` accepts a structured `filters` object that narrows results
across all **61 source plugins**. Filters are **declared once, honored per
provider**: each plugin decides whether to apply a filter natively
(server-side), via a sanctioned post-fetch step (Mastodon `language`
only), or ignore it entirely when the upstream API has no analogue.

Plugins MUST NOT post-filter silently. The sanctioned post-filters are:

- Mastodon `language` (flagged by `SupportsLanguageFilter`).
- The v2.22.0 router-level `published_after` / `published_before` window:
  applied to merged results when the per-source
  `supports_published_after_filter` is `"coarse+postfilter"`. Native
  push-down sources are also subject to the window for safety but the
  upstream filter has already done most of the trimming.

## Runtime discovery

Call `rtv_list_sources` and read the per-source capability booleans to
learn what each provider honors:

- `supports_date_filter`
- `supports_author_filter`
- `supports_category_filter`
- `supports_open_access_filter`
- `supports_domain_filter`
- `supports_channel_filter`
- `supports_language_filter`
- `supports_sort_relevance` / `supports_sort_date` / `supports_sort_citations`
- `supports_pagination`
- `supports_published_after_filter` (tri-state): `"native"` |
  `"coarse+postfilter"` | `"none"` — see `published_after` below.

`max_results_per_query` and `categories_hint` complete the picture.

---

## Filters

### `title` (string)

Title-only substring match. Honored natively by `arxiv`, `pubmed`,
`europmc`. Other scholarly providers (`s2`, `openalex`, `crossref`, ...)
fold title terms into the main query — there is no separate title-only
mode.

### `authors` (`[]string`)

Author-only filter. Honored natively by `arxiv`, `pubmed`, `europmc`.
Other scholarly providers fold author terms into the main query.

### `date_from` / `date_to` (string, `YYYY-MM-DD` or `YYYY`)

Restrict by publication date. Semantics vary per provider:

| Provider | Semantics |
|---|---|
| `arxiv`, `pubmed`, `europmc`, `crossref`, `s2`, `openalex`, `dblp`, `ads`, `core`, `openaire`, `zenodo`, `datacite`, `iascholar` | Native date range. |
| `biorxiv` | **`date_from` is REQUIRED.** bioRxiv API has no keyword search — date-window is the entry point. |
| `brave` | `date_from` only → nearest freshness bucket (`pd`/`pw`/`pm`/`py`) derived from age vs `time.Now()`. Older than 1 year drops the param. Combined range → custom `YYYY-MM-DDtoYYYY-MM-DD`; on HTTP 422 retries with the closest bucket. |
| `stackexchange` | `fromdate` / `todate` (unix seconds). |
| `hackernews` | `numericFilters=created_at_i…`. |
| `dimensions`, `lens` | **Year-floor** (full date truncated to year). |
| `gdelt`, `newsapi`, `serpapi`, `serpapinews` | Native date range. |
| `youtube`, `exa` | Native date range. |
| `wayback` | Snapshot timestamp range. |
| All other providers | Filter is silently ignored. |

### `published_after` / `published_before` (string, RFC3339)  *(v2.22.0)*

Sub-day freshness window. Complements `date_from` / `date_to` — when both
are set, `published_after` / `published_before` win.

**Boundaries are exclusive.** "After T" means strictly later than T; "before
T" means strictly earlier. Validated as `time.RFC3339` at the router
boundary; malformed input returns `ErrInvalidPublishedAt` before any
fan-out.

Per-source handling, surfaced at runtime via
`supports_published_after_filter`:

| Tri-state | Providers | Behavior |
|---|---|---|
| `"native"` | `newsapi`, `gdelt`, `hackernews`, `youtube` | Upstream API accepts sub-day precision; the plugin pushes the precise timestamp through. |
| `"coarse+postfilter"` | `brave`, `exa`, `firecrawl`, `serpapinews`, `bluesky`, `mastodon`, `reddit`, `scrapingdog_youtube` | Plugin downcasts to a `YYYY-MM-DD` floor (UTC) so the upstream query is at least as inclusive as the client window; the router then trims merged results using each hit's `SourceMetadata["published_at"]`. |
| `"none"` (the zero / unset value, omitted from JSON) | All scholarly + structured + places + packages providers | Source emits no usable timestamp. Hits pass through unfiltered by default and are dropped only under `strict_published_at`. |

### `strict_published_at` (bool)  *(v2.22.0)*

When `true`, merged hits with missing or unparseable
`SourceMetadata["published_at"]` are **dropped** by the router post-filter.
Default is `false` — unknown timestamps are kept so providers without
timestamp metadata (encyclopedias, packages, scholarly identifiers,
places) are not silently excluded from a news-window query.

### `categories` (`[]string`)

`categories` is **overloaded** across six+ semantics. Read each provider's
`SourceInfo.categories_hint` for the accepted vocabulary.

| Semantic | Providers |
|---|---|
| Subject taxonomy (arxiv-style codes) | `arxiv`, `ads`, `europmc`, `dimensions`, `lens` |
| Tags | `stackexchange` (→ `tagged`) |
| Keywords | `npm` |
| Resource type | `zenodo`, `datacite`, `openaire` |
| Country code (first entry only) | `serpapi` (`gl`), `serpapinews` (`cr`) |
| POI / podcast category (first entry only) | `googleplaces`, `here`, `osmoverpass`, `itunes`, `listennotes` |

Other providers ignore.

### `open_access` (bool)

Currently honored natively **only by `zenodo`**. Other providers ignore.

**Workaround**: use `intent=primary_source` to bias the source set toward
OA-friendly providers (`europmc`, `openalex`). The router also runs
post-merge Unpaywall enrichment for paper results with a DOI but no
upstream PDF, populating `pdf_url` / `license` regardless of this filter.

### `min_citations` (int)

**Reserved for a future release.** Currently NOT wired by any provider —
the field accepts a value but no plugin honors it. Future wiring is
planned for `s2`, `openalex`, `europmc`.

### `include_domains` / `exclude_domains` (`[]string`)

Restrict or drop results by registered domain name. Entries must be bare
hosts (`kubernetes.io`), not URLs (`https://kubernetes.io/`). Invalid
entries return `ErrInvalidDomainList` (shape-checked: no scheme, no path,
no whitespace, must contain a dot).

Honored by:

| Provider | Mechanism |
|---|---|
| `brave` | Inline `site:` / `-site:` rewritten into the query string (no native request param). Multi-include OR-ed. |
| `exa` | `includeDomains` / `excludeDomains` JSON body fields. |
| `gdelt` | Native domain filter. |
| `kagi` | Native domain filter. |
| `mojeek` | Native domain filter. |
| `serpapi` | Native `as_sitesearch` / SERP operators. |
| `newsapi` | `domains` / `excludeDomains` params. |

Other providers ignore.

### `channels` (`[]string`)

Restrict to specific provider-native channel identifiers. Capped at 5
(`ErrTooManyChannels`). Multi-channel requests fan-out one upstream call
per channel and merge by video ID.

| Provider | Accepts |
|---|---|
| `youtube` | Channel IDs starting with `UC`. `@handle` resolution is NOT performed — resolve upstream first. |
| `scrapingdog_youtube` | Composes `channel:<id>` into the q string. |

### `subreddits` (`[]string`)

Reddit only. Subreddit names without the `r/` prefix. Capped at 5
(`ErrTooManySubreddits`). Multi-subreddit requests fan-out one
`/r/<sub>/search` call per subreddit and merge by URL.

### `language` (string, BCP-47)

Filter to a single primary language. retrievr extracts the first BCP-47
subtag (e.g. `de-DE` → `de`) and forwards to providers that accept a
language code.

Server-side honoring:

| Provider | Param |
|---|---|
| `brave` | `search_lang` |
| `youtube` | `relevanceLanguage` |
| `scrapingdog_youtube` | `language` |
| `bluesky` | `lang` |
| `europeana` | `lang` |
| `serpapi` | `hl` |
| `serpapinews` | `hl` |
| `kagi` | `language` |
| `mojeek` | `language` |
| `newsapi` | `language` |
| `gdelt` | `sourcelang` |
| `eurlex` | language code |
| `kgapi` | `languages` |
| `wikidata` | SPARQL language filter |
| `here` | `lang` |
| `googleplaces` | `languageCode` |
| `listennotes` | `language` |

Sanctioned post-filter exception:

- **`mastodon`** — has no server-side language param on `/api/v2/search`
  but the response carries a reliable `Status.language` field. retrievr
  applies a client-side post-filter using BCP-47 prefix matching. Posts
  with an empty `language` field pass through (**fail-open**) to avoid
  dropping records whose metadata is missing.

Other providers ignore.

---

## Error semantics

| Condition | Error |
|---|---|
| `include_domains` / `exclude_domains` malformed | `ErrInvalidDomainList` |
| `channels` exceeds plugin cap (5) | `ErrTooManyChannels` |
| `subreddits` exceeds plugin cap (5) | `ErrTooManySubreddits` |
| `language` value is not interpretable as BCP-47 | `ErrInvalidLanguageTag` (currently unused — first-subtag extraction is forgiving; future strict mode reserved) |
| `date_from` / `date_to` not parseable | `invalid date format, expected YYYY-MM-DD or YYYY` |
| `biorxiv` request without `date_from` | `search failed` (bioRxiv requires the date window) |

---

## Deferred / not-yet-wired axes

- **`min_citations`** — accepted by the API but no provider honors it
  today. Planned for `s2`, `openalex`, `europmc`.
- Safe-search per call (currently config-only on YouTube + Brave).
- Place radius / bounding-box filter (Nominatim, TomTom, Photon).
- Sort-order extensions (YouTube `viewCount`, GitHub `forks` / `updated`).
- Mastodon date filter via `min_id` / `max_id` cursor pagination.
- Peer-review / preprint-status flag.
- YouTube `@handle` → `channelId` resolution.
- Native `open_access` on `europmc` / `openalex` / `core` / `openaire`
  (currently zenodo-only).
