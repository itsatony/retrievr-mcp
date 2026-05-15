# Search Filter Reference

`rtv_search` accepts a structured `filters` object that narrows results across
the 28 source plugins. Filters are **declared once, honored per provider**:
each plugin decides whether to apply a filter natively (server-side), via a
sanctioned post-fetch step (Mastodon language only), or ignore it entirely
when the upstream API has no analogue.

To discover at runtime which provider honors which filter, call
`rtv_list_sources`. Each entry exposes three v2.7.0 capability booleans:

- `supports_domain_filter`
- `supports_channel_filter`
- `supports_language_filter`

…alongside the existing flags (`supports_date_filter`,
`supports_open_access_filter`, etc.).

---

## Per-provider capability matrix

| Provider                | DomainInc/Exc | Channels        | Subreddits | Language          | Date         |
| ----------------------- | ------------- | --------------- | ---------- | ----------------- | ------------ |
| `brave` (web/images)    | ✅ wired      | —               | —          | ✅ `search_lang`  | ✅ freshness |
| `exa`                   | ✅ wired      | —               | —          | —                 | ✅ wired     |
| `youtube`               | —             | ✅ `channelId`  | —          | ✅ `relevanceLanguage` | ✅ wired |
| `scrapingdog_youtube`   | —             | ✅ `channel:` qualifier | —    | ✅ `language`     | —            |
| `reddit`                | —             | —               | ✅ `/r/{sub}/search` | —     | deferred     |
| `mastodon`              | —             | —               | —          | ⚠️ post-filter on `Status.language` | deferred |
| `bluesky`               | —             | —               | —          | ✅ `lang` param   | —            |
| `europeana`             | —             | —               | —          | ✅ `lang` param   | —            |
| All other 20 providers  | —             | —               | —          | —                 | varies (academic providers wire dates natively) |

Legend:
- ✅ wired — supported by the upstream API and forwarded by retrievr.
- ⚠️ post-filter — applied client-side after fetch; documented exception.
- — — no support; the filter is silently ignored, never post-filtered.

---

## Filter reference

### `include_domains` / `exclude_domains` (`[]string`)

Restrict or drop results by registered domain name. Entries must be the
bare host (`kubernetes.io`), not URLs (`https://kubernetes.io/`); invalid
entries return a typed `ErrInvalidDomainList`.

Honored by `brave` (comma-joined onto `include_domains` /
`exclude_domains`) and `exa` (`includeDomains` / `excludeDomains` JSON
body fields). Other providers ignore.

**Example**

```json
{
  "query": "service mesh",
  "filters": {
    "include_domains": ["kubernetes.io", "istio.io"],
    "exclude_domains": ["reddit.com"]
  }
}
```

### `channels` (`[]string`)

Restrict results to specific provider-native channel identifiers. Capped at
5 channels per call (`ErrTooManyChannels` returned above the limit). Multi-
channel requests fan out one upstream call per channel and merge by video
ID.

- **YouTube**: pass channel IDs (start with `UC`). `@handle` resolution is
  NOT performed in v2.7.0 — resolve the ID upstream first if you have a
  handle.
- **Scrapingdog YouTube**: composes `channel:<id>` into the q string.

**Example**

```json
{
  "query": "kubernetes operators",
  "sources": ["youtube"],
  "filters": {
    "channels": ["UCdngmbVKX1Tgre699-XLlUA"]
  }
}
```

### `subreddits` (`[]string`)

Restrict Reddit results to specific subreddits (without the `r/` prefix).
Capped at 5 per call (`ErrTooManySubreddits`). Multi-subreddit requests
fan out one `/r/<sub>/search` call per subreddit and merge by URL.

**Example**

```json
{
  "query": "compiler optimization",
  "sources": ["reddit"],
  "filters": {
    "subreddits": ["golang", "rust"]
  }
}
```

### `language` (`string`, BCP-47)

Filter results to a single primary language. retrievr extracts the first
BCP-47 subtag (e.g. `de-DE` → `de`) and forwards to providers that accept
a language code. Providers that do not natively support a language param
**ignore the filter** — they do not post-filter, with one exception:

- **Mastodon** has no server-side language param on `/api/v2/search` but
  the response includes a reliable `Status.language` field. retrievr
  applies a client-side post-filter using BCP-47 prefix matching. Posts
  with an empty `language` field pass through (**fail-open**) to avoid
  dropping records whose language metadata is missing.

Honored server-side by `brave`, `youtube`, `scrapingdog_youtube`,
`bluesky`, `europeana`.

**Example**

```json
{
  "query": "klimawandel",
  "sources": ["mastodon", "bluesky"],
  "filters": {
    "language": "de"
  }
}
```

### `date_from` / `date_to` (`string`, `YYYY-MM-DD` or `YYYY`)

Already supported in v2.6.0 for all academic providers, Exa, and YouTube.
v2.7.0 fixes the **Brave defect**: previously `supports_date_filter` was
advertised but `doSearch()` never read the filter. Now mapped:

- `date_from` only → nearest freshness bucket (`pd` / `pw` / `pm` / `py`)
  derived from the age vs `time.Now()`. Older than 1 year drops the
  param (Brave has no "older than a year" bucket).
- `date_from` + `date_to` → custom range `YYYY-MM-DDtoYYYY-MM-DD`. On
  HTTP 422 (Brave rejects the range syntax for some queries), retrievr
  retries once with the closest bucket derived from `date_from`.

### Other filters

The following v2.6.0 filters are unchanged in v2.7.0:

- `title` — title fragment match (provider-dependent).
- `authors` — author name list.
- `categories` — discipline/category tags (academic providers).
- `open_access` — open-access only (pubmed, unpaywall).
- `min_citations` — minimum citation count (S2, EuropePMC).

---

## Error semantics

| Condition                                         | Error                       |
| ------------------------------------------------- | --------------------------- |
| `include_domains` / `exclude_domains` malformed   | `ErrInvalidDomainList`      |
| `channels` exceeds plugin's fan-out cap (5)       | `ErrTooManyChannels`        |
| `subreddits` exceeds plugin's fan-out cap (5)     | `ErrTooManySubreddits`      |
| `language` value is not interpretable as BCP-47   | `ErrInvalidLanguageTag` (currently unused — first-subtag extraction is forgiving; future strict mode reserved) |

---

## Deferred filters (v2.8.0+)

The following axes were surveyed in `project_plan/retrievr_v4.md` but not
implemented in v2.7.0:

- Safe-search per call (currently config-only on YouTube + Brave).
- Place radius / bounding box (Nominatim, TomTom, Photon).
- Sort-order extensions (YouTube `viewCount`, GitHub `forks`/`updated`).
- Mastodon date filter via `min_id`/`max_id` cursor pagination.
- Peer-review / preprint status flag.
- YouTube `@handle` → `channelId` resolution.
