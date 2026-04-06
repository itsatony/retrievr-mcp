# retrievr-mcp Tool Reference

## 1. Overview

retrievr-mcp exposes three MCP tools that give agents unified access to academic publications, AI research, models, and datasets across ten upstream sources (ArXiv, PubMed, Semantic Scholar, OpenAlex, HuggingFace, Europe PMC, CrossRef, DBLP, NASA ADS, bioRxiv/medRxiv).

The server speaks the MCP protocol (spec 2025-11-25) over **Streamable HTTP** on path `/mcp`, port **8099**. All tool inputs and outputs are JSON-encoded. Tool results are returned as MCP text content (a JSON string). Error responses follow the structured `MCPError` format described in section 5.

Available tools:

| Tool | Purpose |
|---|---|
| `rtv_search` | Fan-out search across one or more sources, returns merged and deduplicated results |
| `rtv_get` | Retrieve a single publication by its prefixed ID |
| `rtv_list_sources` | List all configured sources with their capabilities and rate-limit status |

### Source summary

| Source ID | Name | Content types | Auth required | Rate limit | Coverage |
|---|---|---|---|---|---|
| `arxiv` | ArXiv | papers | no | 3 req/s | Open-access preprints for physics, math, CS, and more |
| `pubmed` | PubMed | papers | optional API key | 10 req/s (3 without key) | 36M+ biomedical literature citations |
| `s2` | Semantic Scholar | papers | optional API key | 1 req/s (10 with key) | AI-powered discovery with citation graph |
| `openalex` | OpenAlex | papers | optional API key | 10 req/s | 250M+ scholarly works with open metadata |
| `huggingface` | HuggingFace | models, datasets | optional token | 10 req/s | ML models and datasets hub |
| `europmc` | Europe PMC | papers | no | 10 req/s | 40M+ biomedical and life sciences records |
| `crossref` | CrossRef | papers | no | 10 req/s polite pool | DOI-centric metadata for 150M+ works |
| `dblp` | DBLP | papers | no | 5 req/s | CS bibliography with 7M+ publications (no abstracts) |
| `ads` | NASA ADS | papers | API key required | 5000/day | Astronomy/astrophysics 16M+ records |
| `biorxiv` | bioRxiv/medRxiv | papers | no | 5 req/s | Biology/health preprints (date-range search only, no keyword search) |

---

## 2. rtv_search

Search academic publications across multiple sources. Results are merged, cross-source deduplicated by DOI or ArXiv ID, re-sorted, and truncated to the requested limit. Sources that fail partially are reported in `sources_failed` without blocking results from working sources.

### Parameters

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | string | yes | — | Search query string |
| `sources` | array of string | no | server-configured sources | List of source IDs to search. Valid values: `arxiv`, `pubmed`, `s2`, `openalex`, `huggingface`, `europmc`, `crossref`, `dblp`, `ads`, `biorxiv` |
| `content_type` | string | no | `paper` | Type of content to search for. Enum: `paper`, `model`, `dataset`, `any` |
| `sort` | string | no | `relevance` | Sort order for results. Enum: `relevance`, `date_desc`, `date_asc`, `citations` |
| `limit` | number | no | `10` | Maximum number of results to return (1–100) |
| `offset` | number | no | `0` | Number of results to skip for pagination |
| `filters` | object | no | — | Optional filters to narrow search results (see sub-fields below) |
| `credentials` | object | no | — | Optional per-call API credentials that override server defaults (see sub-fields below) |

### filters sub-fields

| Field | Type | Description |
|---|---|---|
| `title` | string | Filter by title substring |
| `authors` | array of string | Filter by author names |
| `date_from` | string | Earliest publication date, format `YYYY-MM-DD` or `YYYY` |
| `date_to` | string | Latest publication date, format `YYYY-MM-DD` or `YYYY` |
| `categories` | array of string | Filter by subject categories (source-specific identifiers) |
| `open_access` | boolean | When `true`, restrict to open-access publications only |
| `min_citations` | integer | Minimum citation count threshold |

All filter fields are optional. Omit any field that should not be applied. Not every source supports every filter; unsupported filters are silently ignored by that source.

> **Note on bioRxiv:** bioRxiv search requires a `date_from` filter. It does not support keyword search. Use other sources for keyword discovery, then bioRxiv for direct preprint retrieval by DOI.

### credentials sub-fields

| Field | Type | Applies to source |
|---|---|---|
| `pubmed_api_key` | string | `pubmed` |
| `s2_api_key` | string | `s2` |
| `openalex_api_key` | string | `openalex` |
| `hf_token` | string | `huggingface` |
| `ads_api_key` | string | `ads` |

Per-call credentials take precedence over server-configured defaults. Resolution order: per-call value > server config value > anonymous (unauthenticated).

### Example MCP request

```json
{
  "method": "tools/call",
  "params": {
    "name": "rtv_search",
    "arguments": {
      "query": "large language model alignment",
      "sources": ["arxiv", "s2"],
      "content_type": "paper",
      "sort": "date_desc",
      "limit": 5,
      "offset": 0,
      "filters": {
        "date_from": "2024-01-01",
        "open_access": true,
        "min_citations": 10
      }
    }
  }
}
```

### Example response

The tool result content is a JSON-encoded `MergedSearchResult` object.

```json
{
  "total_results": 142,
  "results": [
    {
      "id": "arxiv:2405.10234",
      "source": "arxiv",
      "also_found_in": ["s2"],
      "content_type": "paper",
      "title": "Alignment of Large Language Models: A Survey",
      "authors": [
        { "name": "Jane Smith", "affiliation": "MIT" },
        { "name": "Bob Lee" }
      ],
      "published": "2024-05-16",
      "updated": "2024-05-20",
      "abstract": "We survey recent techniques for aligning large language models...",
      "url": "https://arxiv.org/abs/2405.10234",
      "pdf_url": "https://arxiv.org/pdf/2405.10234",
      "doi": "10.48550/arXiv.2405.10234",
      "arxiv_id": "2405.10234",
      "categories": ["cs.AI", "cs.CL"],
      "citation_count": 47,
      "license": "CC BY 4.0"
    }
  ],
  "sources_queried": ["arxiv", "s2"],
  "sources_failed": [],
  "has_more": true
}
```

### MergedSearchResult fields

| Field | Type | Description |
|---|---|---|
| `total_results` | integer | Estimated total matching results across all queried sources |
| `results` | array of Publication | Merged, deduplicated, sorted result list (see Publication shape below) |
| `sources_queried` | array of string | Source IDs that were contacted |
| `sources_failed` | array of string | Source IDs that returned an error (partial failure) |
| `has_more` | boolean | Whether additional results exist beyond the current page |

### Publication object fields

| Field | Type | Always present | Description |
|---|---|---|---|
| `id` | string | yes | Prefixed identifier, e.g. `arxiv:2405.10234` |
| `source` | string | yes | Primary source ID |
| `also_found_in` | array of string | no | Other sources that returned the same item (by DOI or ArXiv ID) |
| `content_type` | string | yes | One of `paper`, `model`, `dataset`, `any` |
| `title` | string | yes | Publication title |
| `authors` | array of Author | yes | Author list |
| `published` | string | yes | Publication date, `YYYY-MM-DD` |
| `updated` | string | no | Last update date, `YYYY-MM-DD` |
| `abstract` | string | no | Abstract text |
| `url` | string | yes | Canonical URL |
| `pdf_url` | string | no | Direct PDF URL |
| `doi` | string | no | DOI |
| `arxiv_id` | string | no | ArXiv identifier used for cross-source deduplication |
| `categories` | array of string | no | Subject categories |
| `citation_count` | integer | no | Citation count (absent when unknown) |
| `full_text` | FullTextContent | no | Full text content (only when explicitly requested via `rtv_get`) |
| `references` | array of Reference | no | Reference list |
| `citations` | array of Reference | no | Citing works |
| `related` | array of Reference | no | Related works |
| `license` | string | no | License identifier |
| `source_metadata` | object | no | Raw source-specific metadata |

**Author object:**

| Field | Type | Description |
|---|---|---|
| `name` | string | Author full name |
| `affiliation` | string | Institutional affiliation (when available) |
| `orcid` | string | ORCID identifier (when available) |

**Reference object:**

| Field | Type | Description |
|---|---|---|
| `id` | string | Prefixed ID of the referenced work (when available) |
| `title` | string | Title of the referenced work |
| `year` | integer | Publication year (when available) |

---

## 3. rtv_get

Retrieve a single publication by its prefixed ID. Returns full metadata and, optionally, additional content such as abstract, full text, references, citations, or related works.

### Parameters

| Name | Type | Required | Default | Description |
|---|---|---|---|---|
| `id` | string | yes | — | Prefixed publication ID, e.g. `arxiv:2401.12345`, `s2:abc123`, `pubmed:38123456`, `crossref:10.1038/s41586-024-07487-w`, `dblp:journals/corr/abs-2401-12345`, `ads:2024ApJ...123..456A`, `biorxiv:10.1101/2024.01.15.575123` |
| `include` | array of string | no | `["abstract"]` | Additional data to include. Enum values: `abstract`, `full_text`, `references`, `citations`, `related`, `metadata` |
| `format` | string | no | `native` | Desired content format. Enum: `native`, `json`, `xml`, `markdown`, `bibtex` |
| `credentials` | object | no | — | Optional per-call API credentials (same sub-fields as `rtv_search`) |

The `format` parameter controls the format of any returned `full_text` content. `native` returns the source's own format without conversion. `bibtex` assembles a BibTeX entry from the publication metadata (available for all sources).

Not all sources support every `include` value or `format`. Unsupported combinations return an error with message `"requested format not supported by this source"` or `"full text not available for this publication"`.

### Example MCP request

```json
{
  "method": "tools/call",
  "params": {
    "name": "rtv_get",
    "arguments": {
      "id": "arxiv:2401.12345",
      "include": ["abstract", "references"],
      "format": "native"
    }
  }
}
```

### Example response

The tool result content is a JSON-encoded `Publication` object.

```json
{
  "id": "arxiv:2401.12345",
  "source": "arxiv",
  "content_type": "paper",
  "title": "Attention Is All You Need: Revisited",
  "authors": [
    { "name": "Alice Chen", "affiliation": "Stanford University" }
  ],
  "published": "2024-01-15",
  "abstract": "This paper revisits the transformer architecture...",
  "url": "https://arxiv.org/abs/2401.12345",
  "pdf_url": "https://arxiv.org/pdf/2401.12345",
  "doi": "10.48550/arXiv.2401.12345",
  "arxiv_id": "2401.12345",
  "categories": ["cs.LG", "cs.CL"],
  "citation_count": 312,
  "references": [
    { "id": "arxiv:1706.03762", "title": "Attention Is All You Need", "year": 2017 }
  ]
}
```

**FullTextContent object** (present when `full_text` is included and available):

| Field | Type | Description |
|---|---|---|
| `content` | string | The content body in the requested format |
| `content_format` | string | The actual format of the returned content (`native`, `json`, `xml`, `markdown`, or `bibtex`) |
| `content_length` | integer | Length of `content` in bytes |
| `truncated` | boolean | Whether the content was truncated due to size limits |

---

## 4. rtv_list_sources

List all available publication sources with their capabilities, rate-limit status, and supported features.

### Parameters

This tool takes no parameters.

### Example MCP request

```json
{
  "method": "tools/call",
  "params": {
    "name": "rtv_list_sources",
    "arguments": {}
  }
}
```

### Example response

The tool result content is a JSON-encoded array of `SourceInfo` objects.

```json
[
  {
    "id": "arxiv",
    "name": "ArXiv",
    "description": "Open-access preprint server for physics, mathematics, computer science, and related fields.",
    "enabled": true,
    "content_types": ["paper"],
    "native_format": "xml",
    "available_formats": ["xml", "json", "markdown", "bibtex"],
    "supports_full_text": false,
    "supports_citations": false,
    "supports_date_filter": true,
    "supports_author_filter": true,
    "supports_category_filter": true,
    "rate_limit": {
      "requests_per_second": 3.0,
      "remaining": 2.8
    },
    "categories_hint": "cs.AI, cs.CL, cs.LG, math.*, physics.*",
    "accepts_credentials": false
  },
  {
    "id": "s2",
    "name": "Semantic Scholar",
    "description": "AI-powered research discovery with citation graph and influence metrics.",
    "enabled": true,
    "content_types": ["paper"],
    "native_format": "json",
    "available_formats": ["json", "markdown", "bibtex"],
    "supports_full_text": false,
    "supports_citations": true,
    "supports_date_filter": true,
    "supports_author_filter": true,
    "supports_category_filter": false,
    "rate_limit": {
      "requests_per_second": 1.0,
      "remaining": 0.9
    },
    "accepts_credentials": true
  }
]
```

### SourceInfo fields

| Field | Type | Description |
|---|---|---|
| `id` | string | Source identifier used in `sources` and `id` prefix fields |
| `name` | string | Human-readable source name |
| `description` | string | Short description of the source |
| `enabled` | boolean | Whether the source is active in the current server configuration |
| `content_types` | array of string | Content types this source provides (`paper`, `model`, `dataset`) |
| `native_format` | string | Format the source returns data in natively |
| `available_formats` | array of string | All formats this source can serve (including via conversion) |
| `supports_full_text` | boolean | Whether the source can return full text content |
| `supports_citations` | boolean | Whether the source provides citation data |
| `supports_date_filter` | boolean | Whether date range filters are honored |
| `supports_author_filter` | boolean | Whether author filters are honored |
| `supports_category_filter` | boolean | Whether category filters are honored |
| `rate_limit` | RateLimitInfo | Current rate-limit configuration and token availability |
| `categories_hint` | string | Example or documentation of valid category values (when applicable) |
| `accepts_credentials` | boolean | Whether this source accepts per-call API credentials |

**RateLimitInfo fields:**

| Field | Type | Description |
|---|---|---|
| `requests_per_second` | number | Configured token-bucket refill rate |
| `remaining` | number | Available tokens at the time of the request |

---

## 5. Error Format

All tool errors are returned as MCP error result content with a JSON-encoded body:

```json
{
  "error": "<error message>",
  "source": "<source id, if applicable>",
  "detail": "<additional context, if available>"
}
```

The `source` and `detail` fields are omitted when empty.

### Common error messages

| Message | When it occurs |
|---|---|
| `"invalid tool input"` | A required parameter is missing or has an invalid value |
| `"source not found"` | The source ID prefix in an `rtv_get` `id` is not a known source |
| `"source is disabled"` | The requested source exists but is disabled in the server config |
| `"invalid publication id format"` | The `id` parameter does not match the expected `source:rawid` prefix format |
| `"search failed"` | A source-level search request failed |
| `"retrieval failed"` | A source-level get request failed |
| `"rate limit exceeded"` | The source's rate limit was reached and the request was not served |
| `"upstream source timeout"` | The upstream source did not respond within the configured timeout |
| `"invalid date format, expected YYYY-MM-DD or YYYY"` | A date filter field contains an unparseable value |
| `"all sources failed"` | Every requested source returned an error (no partial results) |
| `"full text not available for this publication"` | The source does not provide full text for this item |
| `"requested format not supported by this source"` | The `format` value is not supported by the source handling the request |
| `"provided credential was rejected by upstream source"` | An API key or token in `credentials` was refused by the upstream API |
| `"this source requires credentials for the requested operation"` | The source requires an API key that was not provided |
| `"failed to marshal response"` | Internal serialization error |

### Example error response

```json
{
  "error": "rate limit exceeded",
  "source": "s2",
  "detail": "rate limit exceeded: retry after 1s"
}
```

```json
{
  "error": "invalid publication id format",
  "detail": "invalid publication id format: missing source prefix in \"2401.12345\""
}
```
