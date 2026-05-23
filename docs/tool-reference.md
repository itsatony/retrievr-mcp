# retrievr-mcp Tool Reference

## 1. Overview

retrievr-mcp exposes **three MCP tools** that give agents unified access to
academic publications, AI research, models, datasets, web/news/social/code/
place/image/post/package/patent/audio content, and structured facts.

The catalog spans **61 source plugins** across five masterplans (v1 academic,
v2 web/enrichment, v3 multimodal, v5 KnowledgeCommons, v6 GeoExpansion +
Premium). The canonical count is `SourceCount` in
`internal/rtv.types.go`.

- **Transport**: Streamable HTTP on path `/mcp`, port **8099**.
- **MCP spec**: 2025-11-25.
- **Wire**: All tool inputs/outputs are JSON-encoded. Results are returned
  as MCP text content (a JSON string). Errors follow the structured
  `MCPError` shape (section 6).

| Tool | Purpose |
|---|---|
| `rtv_search` | Fan-out search across one or more sources, merged + deduplicated results |
| `rtv_get` | Retrieve a single result by its prefixed ID |
| `rtv_list_sources` | List every registered source with its full capability surface |

Response shape: `rtv_search` returns a `MergedSearchResultV2` (fat-struct
`Result` with a `kind` discriminator + per-kind data blocks). `compat:"v1"`
was sunset in v2.0.0 and returns `RTV_COMPAT_V1_SUNSET`.

---

## 2. Source Catalog

Per-source key/free status reflects `Capabilities.RequiresCredential`. A
"free" source either works fully anonymously or accepts an optional key for
higher quota. A "key" source refuses to start without a credential and
emits `ErrCredentialRequired`. Use `rtv_list_sources` for the live truth.

### v1 — Academic core (10)

| ID | Name | Content types | Key/Free | Notes |
|---|---|---|---|---|
| `arxiv` | ArXiv | paper | free | Open-access preprints (physics, math, CS). |
| `pubmed` | PubMed | paper | free (key boosts quota) | Biomedical citations, MeSH terms. |
| `s2` | Semantic Scholar | paper | free (key boosts quota) | Citation graph, influence metrics. |
| `openalex` | OpenAlex | paper | free (key boosts quota) | 250M+ scholarly works, inverted-index abstract normalized to plaintext. |
| `huggingface` | HuggingFace | model, dataset | free (key boosts quota) | ML models + datasets hub. |
| `europmc` | Europe PMC | paper | free | Life-sciences full-text + OA. |
| `crossref` | CrossRef | paper | free | DOI-centric metadata (150M+ works). |
| `dblp` | DBLP | paper | free | CS bibliography (no abstracts). |
| `ads` | NASA ADS | paper | key required | Astronomy / astrophysics 16M+ records. |
| `biorxiv` | bioRxiv / medRxiv | paper | free | Date-range only — `date_from` REQUIRED. |

### v2 — Web / Enrichment (8)

| ID | Name | Content types | Key/Free | Notes |
|---|---|---|---|---|
| `exa` | Exa | any | key | Neural web search; honors `include_domains` / `exclude_domains`. |
| `brave` | Brave Search | any, image | key | Web + image; freshness buckets; `site:` rewrites for domain filters. |
| `linkup` | Linkup | any | key | Premium web retrieval. |
| `firecrawl` | Firecrawl | any | key | Page scrape → markdown native. |
| `github` | GitHub | any | free (key boosts quota) | Code/repo search. |
| `wikipedia` | Wikipedia | any | free | Encyclopedia summaries. |
| `unpaywall` | Unpaywall | paper | free | OA enrichment (DOI → PDF/license). Wired post-merge by router. |
| `perplexity` | Perplexity | any | key | LLM-grounded web answers. |

### v3 — Multimodal (10)

| ID | Name | Content types | Key/Free | Notes |
|---|---|---|---|---|
| `youtube` | YouTube Data API | video | key | Native `channelId` + `relevanceLanguage`. |
| `scrapingdog_youtube` | Scrapingdog YouTube | video | key | SERP-style YouTube; `channel:` qualifier. |
| `photon` | Photon | place | free | OSM geocoder. |
| `tomtom` | TomTom | place | key | POI / geocoder. |
| `nominatim` | Nominatim | place | free | OSM geocoder; rate-limited. |
| `wikimedia` | Wikimedia Commons | image | free | Free-licensed media. |
| `europeana` | Europeana | image | key | EU cultural heritage; language filter. |
| `mastodon` | Mastodon | post | free | Public Fediverse posts; language post-filter (only sanctioned post-filter). |
| `bluesky` | Bluesky | post | free | AT-Proto firehose search; native `lang`. |
| `reddit` | Reddit | post | key | Per-subreddit search (≤5 subs/call). |

### v5 — KnowledgeCommons (19)

| ID | Name | Content types | Key/Free | Notes |
|---|---|---|---|---|
| `stackexchange` | Stack Exchange | post (Q&A) | free | Unix-seconds date filter; `categories` → `tagged`. |
| `hackernews` | Hacker News (Algolia) | post (Q&A) | free | `numericFilters` date range. |
| `zenodo` | Zenodo | paper, dataset | free | CERN OA repository; native `open_access`. |
| `core` | CORE | paper | free | OA aggregator. |
| `openaire` | OpenAIRE | paper, dataset | free | EU OA aggregator. |
| `wikidata` | Wikidata | any | free | Structured KG; SPARQL-backed; language. |
| `datacite` | DataCite | dataset, paper | free | Dataset DOIs. |
| `orcid` | ORCID | paper | free | Researcher-centric works lookup. |
| `npm` | npm | package | free | JS/TS registry. |
| `pypi` | PyPI | package | free | Python registry. |
| `crates` | crates.io | package | free | Rust registry. |
| `pkggodev` | pkg.go.dev | package | free | Go modules. |
| `googlepatents` | Google Patents | patent | free | Unofficial xhr endpoint. |
| `epoops` | EPO OPS | patent | key | EPO Open Patent Services. |
| `courtlistener` | CourtListener | paper (law) | free | US case law; `citation_code` dedup. |
| `eurlex` | EUR-Lex | paper (law) | free | EU regulations/directives; CELEX; language. |
| `gdelt` | GDELT | any | free | Global news monitoring; domain filters; language. |
| `iascholar` | IA Scholar | paper | free | Internet Archive scholarly index. |
| `wayback` | Wayback Machine | any | free | Web archive snapshots. |

### v6 — GeoExpansion + Premium (14)

| ID | Name | Content types | Key/Free | Notes |
|---|---|---|---|---|
| `googleplaces` | Google Places | place | key | POI search; native `language`, region. |
| `osmoverpass` | OSM Overpass | place | free | Overpass QL gated (server-side validation). |
| `here` | HERE | place | key | POI/geocoder; native `language`. |
| `listennotes` | Listen Notes | audio | key | Podcast episodes/shows. |
| `itunes` | Apple iTunes | audio | free | Podcast catalog (no key). |
| `dimensions` | Dimensions.ai | paper | key | Premium citation graph; year-floor dates. |
| `lens` | Lens.org | paper | key | Premium citation graph; year-floor dates. |
| `kagi` | Kagi | any | key | Paid web search; domain + language. |
| `mojeek` | Mojeek | any | key | Independent crawler; domain + language. |
| `serpapi` | SerpAPI (Google) | any | key | Google SERP proxy; country via `categories[0]`. |
| `wolframalpha` | Wolfram Alpha | any | key | Structured computation. |
| `kgapi` | Google Knowledge Graph | any | key | Entity KG lookup; language. |
| `newsapi` | NewsAPI | any | key | Premium news; domain + language. |
| `serpapinews` | SerpAPI News | any | key | Google News SERP; country + language. |

---

## 3. rtv_search

Search across any subset of the catalog. Results are fanned-out
concurrently, merged, cross-source deduplicated (per ContentType family),
re-sorted, and truncated. Partial failures are graceful — working sources
return results while failed sources land in `sources_failed`.

### Parameters

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | string | yes | — | Search query string. |
| `sources` | array[string] | no | server-configured `DefaultSources` | Explicit source IDs. Overrides `intent`. |
| `content_type` | string | no | `paper` | One of `paper`, `model`, `dataset`, `video`, `place`, `image`, `post`, `package`, `patent`, `audio`, `any`. |
| `sort` | string | no | `relevance` | One of `relevance`, `date_desc`, `date_asc`, `citations`. |
| `limit` | number | no | `10` | 1–100. |
| `offset` | number | no | `0` | Pagination skip count. |
| `filters` | object | no | — | See section 3.2 and `docs/filter-reference.md`. |
| `credentials` | object | no | — | Per-call overrides (section 3.3). |
| `intent` | string | no | — | One of `deep_research`, `quick_lookup`, `primary_source`, `code_provenance`, `news`, `reference`. Drives source selection + fallback chains. |
| `compat` | string | no | `v2` | `"v1"` returns `RTV_COMPAT_V1_SUNSET`. |

### 3.1 intent — declarative source selection

Six intents are wired in `DefaultFallbackConfig()` (`internal/rtv.router.go`).
Unregistered or unconfigured sources are silently dropped (chain degrades to
whatever is enabled in the running tenant). Explicit `sources` overrides
`intent`.

| Intent | Primary | Fallback |
|---|---|---|
| `deep_research` | `s2`, `openalex` | `arxiv`, `crossref`, `europmc`, `pubmed`, `dblp`, `ads`, `core`, `openaire` |
| `primary_source` | `europmc`, `openalex` | `crossref`, `s2`, `arxiv`, `pubmed`, `core`, `openaire`, `zenodo` |
| `quick_lookup` | `kagi`, `mojeek`, `serpapi` | `brave`, `exa`, `linkup`, `wikipedia` |
| `code_provenance` | `npm`, `pypi`, `crates`, `pkggodev` | `github`, `arxiv`, `dblp`, `s2` |
| `news` | `newsapi`, `serpapinews` | `gdelt`, `brave`, `exa`, `wikipedia` |
| `reference` | `wolframalpha`, `kgapi` | `wikidata`, `wikipedia` |

Fallback walking triggers when the primary set yields zero hits or every
primary source fails. The walk stops at the first fallback source returning
≥1 result. `MergedSearchResult.FallbackWalked` reports whether this happened.

### 3.2 filters sub-fields

| Field | Type | Semantics |
|---|---|---|
| `title` | string | Title-only match. Honored by `arxiv`, `pubmed`, `europmc`; others fold into the main query. |
| `authors` | []string | Author-only filter. Honored by `arxiv`, `pubmed`, `europmc`. |
| `date_from` / `date_to` | string | `YYYY-MM-DD` or `YYYY`. Brave maps to freshness buckets (`pd`/`pw`/`pm`/`py`); StackExchange / HackerNews convert to unix seconds; Dimensions / Lens floor to year; **bioRxiv REQUIRES `date_from`**. |
| `published_after` / `published_before` | string | **v2.22.0.** RFC3339 (e.g. `2026-05-23T08:00:00Z`). Sub-day freshness window with **exclusive** boundaries. Wins over `date_from`/`date_to`. Native push-down: `newsapi`, `gdelt`, `hackernews`, `youtube`. Coarse-precision push-down + router post-filter against `SourceMetadata["published_at"]`: `brave`, `exa`, `firecrawl`, `serpapinews`, `bluesky`, `mastodon`, `reddit`, `scrapingdog_youtube`. Surfaced at runtime via `rtv_list_sources.supports_published_after_filter`. Malformed input rejected before fan-out. |
| `strict_published_at` | bool | **v2.22.0.** When `true`, router drops merged hits whose `published_at` is missing or unparseable. Default `false`. |
| `categories` | []string | Semantics vary by source — see filter-reference.md. |
| `open_access` | bool | Native only on `zenodo`. Use `intent=primary_source` for OA-biased scholarly retrieval. |
| `min_citations` | int | **Reserved for a future release.** Not wired by any provider today. |
| `include_domains` / `exclude_domains` | []string | Bare domains (no scheme). Honored by `brave`, `exa`, `gdelt`, `kagi`, `mojeek`, `serpapi`, `newsapi`. |
| `channels` | []string | YouTube channel IDs (≤5). Honored by `youtube`, `scrapingdog_youtube`. |
| `subreddits` | []string | Subreddit names (≤5). Honored by `reddit`. |
| `language` | string | BCP-47 (`en`, `de`, `fr-CA`). Honored by `brave`, `youtube`, `scrapingdog_youtube`, `bluesky`, `europeana`, `mastodon` (post-fetch), `serpapi`, `serpapinews`, `kagi`, `mojeek`, `newsapi`, `gdelt`, `eurlex`, `kgapi`, `wikidata`, `here`, `googleplaces`, `listennotes`. |

Plugins that do not natively support a filter MUST ignore it. Sanctioned
post-filters: Mastodon `language`, and the v2.22.0 router-level
`published_after` / `published_before` window (applied after dedup +
enrichment, before sort + truncate). For the runtime matrix query
`rtv_list_sources` and read the `supports_*` booleans, including the
tri-state `supports_published_after_filter`.

### 3.3 credentials sub-fields

Object with optional string fields. Each plugin reads only its own key;
unknown keys are ignored. Per-credential rate-limit buckets keep tenants
isolated. Resolution order: per-call > server config > anonymous.

| Field | Applies to |
|---|---|
| `pubmed_api_key` | `pubmed` |
| `s2_api_key` | `s2` |
| `openalex_api_key` | `openalex` |
| `hf_token` | `huggingface` |
| `ads_api_key` | `ads` |

Paid plugins (Brave, Exa, Kagi, NewsAPI, ...) read their key from server
config (`PluginConfig.APIKey`); per-call override of paid-tier keys is not
exposed via the `credentials` object.

### 3.4 Response — MergedSearchResultV2

| Field | Type | Description |
|---|---|---|
| `total_results` | int | Count after dedup + truncation. |
| `results` | []Result | Merged + deduplicated list. |
| `sources_queried` | []string | Source IDs actually contacted. |
| `sources_failed` | []string | Source IDs that errored. |
| `sources_skipped` | []SkipNote | Providers excluded by EU-mode gate, with structured reason. |
| `audit_ref` | string | `evt_aud_<16hex>` — correlate to retrievr's audit log. |
| `fallback_walked` | bool | True when the fallback chain walked past the primary set. |
| `eu_fallback_used` | bool | True when `EUModePreferred` fell back to a non-EU provider. |
| `has_more` | bool | True if more results exist beyond the page. |

### 3.5 Publication / Result fields

Each result carries a `kind` discriminator + a per-kind data block. The
underlying `Publication` envelope:

| Field | Type | Notes |
|---|---|---|
| `id` | string | Prefixed, e.g. `arxiv:2401.12345`. |
| `source` | string | Primary source. |
| `also_found_in` | []string | Other sources after dedup. |
| `content_type` | string | See enum in section 3. |
| `title` | string | |
| `authors` | []Author | `{name, affiliation?, orcid?}`. |
| `published` | string | `YYYY-MM-DD`. |
| `updated` | string | optional. |
| `abstract` | string | optional. |
| `url` | string | Canonical URL. |
| `pdf_url` | string | optional. |
| `doi` | string | optional. |
| `arxiv_id` | string | optional. |
| `categories` | []string | Source-specific vocabulary. |
| `citation_count` | int | Pointer — `nil` when unknown. |
| `full_text` | FullTextContent | Present when explicitly requested via `rtv_get`. |
| `references` / `citations` / `related` | []Reference | Present when included. |
| `license` | string | optional. |
| `thumbnail_url` | string | video / image / post preview. |
| `lat` / `lon` | float | place — WGS84. |
| `address` | string | place. |
| `duration_seconds` | int | video / audio. |
| `media_url` | string | image / video / audio direct media URL. |
| `media_mime` | string | MIME type of `media_url`. |
| `engagement_score` | int | post engagement (likes/score). |
| `language` | string | BCP-47 (record-level). |
| `source_metadata` | object | Source-specific key/value bag (e.g. `youtube_id`, `osm_id`, `package_id`, `patent_number`, `audio_id`, `qa_question_id`, `citation_code`). |

---

## 4. rtv_get

Fetch full details for a single result by its prefixed ID. The prefix
selects the plugin; the remainder is the source-native ID.

### ID examples

| Tier | Example IDs |
|---|---|
| v1 academic | `arxiv:2401.12345`, `pubmed:38123456`, `s2:abc123`, `openalex:W4366341216`, `crossref:10.1038/s41586-023-06600-9`, `dblp:journals/corr/abs-2401-12345`, `ads:2024ApJ...123..456A`, `biorxiv:10.1101/2024.01.15.575123` |
| v2 web | `github:owner/repo`, `wikipedia:Attention_(machine_learning)`, `firecrawl:https://example.com/page`, `unpaywall:10.1038/...` |
| v3 multimodal | `youtube:dQw4w9WgXcQ`, `nominatim:way/123`, `wikimedia:File:Example.jpg`, `mastodon:https://mastodon.social/@user/123`, `bluesky:at://did:plc:.../app.bsky.feed.post/...`, `reddit:t3_abc123` |
| v5 KnowledgeCommons | `npm:react`, `pypi:requests`, `crates:tokio`, `pkggodev:github.com/go-chi/chi`, `googlepatents:US20230123456A1`, `epoops:EP3456789A1`, `courtlistener:410-us-113`, `eurlex:32016R0679`, `zenodo:1234567`, `wikidata:Q42` |
| v6 premium | `googleplaces:ChIJ...`, `osmoverpass:node/1234567890`, `here:here:pds:place:...`, `listennotes:abc123`, `itunes:1234567890`, `dimensions:pub.1234567890`, `lens:123-456-789` |

### Parameters

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `id` | string | yes | — | Prefixed ID. |
| `include` | []string | no | `["abstract"]` | One or more of `abstract`, `full_text`, `references`, `citations`, `related`, `metadata`. Honored only where `SourceInfo.supports_full_text` / `supports_citations` is true. |
| `format` | string | no | `native` | One of `native`, `json`, `xml`, `markdown`, `bibtex`. |
| `credentials` | object | no | — | Same shape as `rtv_search` credentials. |

### Format notes

- `native` — source's native shape (XML for arxiv/europmc, JSON for most
  others, HTML for some web).
- `markdown` — native only for `firecrawl` + `brave`. Other sources may
  reject it.
- `bibtex` — **metadata assembly**, not lossy conversion. Works only on
  scholarly sources that emit enough bibliographic fields. The router
  fetches `native` then runs `GenerateBibTeX()` centrally.

---

## 5. rtv_list_sources

Returns one `SourceInfo` per registered plugin (sorted by ID).

### SourceInfo fields

| Field | Type | Description |
|---|---|---|
| `id` | string | Source identifier. |
| `name` | string | Human-readable name. |
| `description` | string | Short description. |
| `enabled` | bool | True when registered. |
| `content_types` | []string | Content types served. |
| `native_format` | string | Native response format. |
| `available_formats` | []string | Convertible formats. |
| `supports_full_text` | bool | `rtv_get include=full_text` honored. |
| `supports_citations` | bool | Citation graph available. |
| `supports_date_filter` | bool | `date_from` / `date_to` honored. |
| `supports_author_filter` | bool | `authors` filter honored. |
| `supports_category_filter` | bool | `categories` filter honored. |
| `supports_open_access_filter` | bool | `open_access` filter honored. |
| `supports_domain_filter` | bool | `include_domains` / `exclude_domains` honored. |
| `supports_channel_filter` | bool | `channels` filter honored. |
| `supports_language_filter` | bool | `language` filter honored. |
| `supports_sort_relevance` | bool | `sort=relevance` honored. |
| `supports_sort_date` | bool | `sort=date_desc/date_asc` honored. |
| `supports_sort_citations` | bool | `sort=citations` honored. |
| `supports_pagination` | bool | `offset` honored. |
| `max_results_per_query` | int | Upstream cap (0 if unbounded). |
| `rate_limit` | RateLimitInfo | `{requests_per_second, remaining}`. |
| `categories_hint` | string | Accepted category vocabulary hint. |
| `accepts_credentials` | bool | True when per-call key may be provided OR strictly required. |
| `kinds` | []ResultKind | Result kinds emitted (`paper`, `web`, `code`, `place`, ...). |
| `query_intents` | []Intent | Intents this provider is reasonable for. |
| `region` | Region | EU / US / public-research-infrastructure / ... |
| `dpa_status` | DPAStatus | `signed` / `covered-by-scc` / `n/a` / `unknown`. |
| `subprocessor_url` | string | DPA subprocessor link. |
| `free_tier` | bool | Works without a paid key. |
| `requires_key` | bool | Refuses to start without a credential. |

---

## 6. Error Format

All tool errors return as MCP error result content with a JSON body:

```json
{
  "error": "<error message>",
  "source": "<source id, if applicable>",
  "detail": "<additional context, if available>"
}
```

`source` and `detail` are omitted when empty.

### Common error messages

| Message | When |
|---|---|
| `invalid tool input` | Required parameter missing/invalid. |
| `source not found` | Unknown source prefix in `rtv_get` ID. |
| `source is disabled` | Source exists but is disabled in config. |
| `invalid publication id format` | ID does not match `source:rawid`. |
| `search failed` | Source-level search error. |
| `retrieval failed` | Source-level get error. |
| `rate limit exceeded` | Token bucket exhausted. |
| `upstream source timeout` | Per-source timeout exceeded. |
| `invalid date format, expected YYYY-MM-DD or YYYY` | Bad date filter. |
| `all sources failed` | Every requested source errored, no fallback hit. |
| `full text not available for this publication` | Source has no full text for this item. |
| `requested format not supported by this source` | Unsupported `format` for this source. |
| `provided credential was rejected by upstream source` | Upstream rejected the supplied key. |
| `this source requires credentials for the requested operation` | `RequiresCredential=true` plugin with no key. |
| `RTV_COMPAT_V1_SUNSET` | Caller passed `compat:"v1"`. |
| `invalid domain list` | `include_domains`/`exclude_domains` malformed. |
| `too many channels` | `channels` > 5. |
| `too many subreddits` | `subreddits` > 5. |
| `failed to marshal response` | Internal serialization error. |
