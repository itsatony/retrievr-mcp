# retrievr-mcp

![Go](https://img.shields.io/badge/Go-1.25.5-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/License-MIT-blue)
![MCP](https://img.shields.io/badge/MCP-2025--11--25-purple)

An MCP server **and** importable Go library that gives LLM agents unified access to academic papers, AI models, datasets, web pages, news, code repos, packages, patents, court decisions, regulations, encyclopedia articles, multimedia, social posts, places, podcasts, and structured facts. **61 source plugins** behind three tools (`rtv_search`, `rtv_get`, `rtv_list_sources`):

- **v1 Scholarly** (10): ArXiv, PubMed, Semantic Scholar, OpenAlex, HuggingFace, Europe PMC, CrossRef, DBLP, NASA ADS, bioRxiv
- **v2 Web / enrichment** (8): Exa, Brave, Linkup (**EU-resident**), Firecrawl, GitHub, Wikipedia, Unpaywall (enrichment), Perplexity
- **v3 Multimodal** (10): YouTube + Scrapingdog YouTube; Photon / TomTom / Nominatim (geo); Wikimedia / Europeana (images); Mastodon / Bluesky / Reddit (social)
- **v5 Knowledge Commons** (19): StackExchange + HackerNews (Q&A); Zenodo / CORE / OpenAIRE (OA repositories); Wikidata / DataCite / ORCID (structured); npm / PyPI / crates / pkg.go.dev (packages); Google Patents / EPO OPS / CourtListener / EUR-Lex (patents + law); GDELT / IA Scholar / Wayback (archives)
- **v6 GeoExpansion + Premium** (14): Google Places / OSM Overpass / HERE (POI); Listen Notes / iTunes (podcasts); Dimensions / Lens (premium scholarly); Kagi / Mojeek / SerpAPI (paid web); Wolfram Alpha / Google KG (facts); NewsAPI / SerpAPI News (premium news)

Paid plugins set `RequiresCredential=true` and refuse to start without a key — `rtv_list_sources` advertises `requires_key` and `free_tier` so agents can filter the catalog by reachability.

Plus **EU-GDPR mode** (`eu_strict` + audit hooks), **v2 Result shape** with kind discriminator (paper/web/code/place/news/model/dataset/post/package/patent/audio/encyclopedia), **six wired intent chains** (deep_research, primary_source, quick_lookup, code_provenance, news, reference), **per-credential rate-limit isolation**, and **streaming search** for slow providers.

📚 **[Library Guide](docs/library-guide.md)** · **[Architecture](docs/architecture.md)** · **[EU Mode](docs/eu-mode.md)** · **[Intents](docs/intents.md)** · **[Residency](docs/residency.md)** · **[Plugin Guide](docs/plugin-guide.md)**

## What it does

- **Searches** fan out to all requested sources concurrently
- **Results** are merged, deduplicated by DOI/ArXiv ID, and interleaved round-robin across sources
- **Each result** includes title, authors, date, abstract, URL, DOI, and source-specific metadata
- **rtv_get** retrieves full details for a single publication, including BibTeX, references, and citations
- **Per-call credentials** let each caller supply their own API keys
- **Importable Go library** (`pkg/retrievr`) so in-process consumers (liz, nexus) skip the MCP HTTP hop entirely

## Two ways to use retrievr

### As an MCP server (`cmd/retrievr-mcp`)

The default. Spin up the daemon, register with an MCP-aware host (Claude Code, etc.), and call `rtv_search` / `rtv_get` / `rtv_list_sources` over HTTP. See **Quick Start** below.

### As an importable Go library (`pkg/retrievr`)

For Go consumers running on the same host, direct import skips JSON serialization and the TCP round-trip. See [`pkg/retrievr/bootstrap.go`](pkg/retrievr/bootstrap.go):

```go
import (
    "context"
    rtv "github.com/itsatony/retrievr-mcp/pkg/retrievr"
)

client, cleanup, err := rtv.NewClientFromConfig("configs/retrievr-mcp.yaml", logger)
if err != nil { /* ... */ }
defer cleanup()

ctx := rtv.WithCredentials(context.Background(), map[string]string{
    "s2": os.Getenv("RETRIEVR_S2_API_KEY"),
})
result, err := client.Search(ctx, rtv.SearchParams{
    Query:  "transformer attention",
    Limit:  10,
    Intent: rtv.IntentDeepResearch,
}, nil)
```

### As a CLI (`cmd/retrievr-cli`)

```bash
go build -o retrievr-cli ./cmd/retrievr-cli
./retrievr-cli search --sources=arxiv,s2 --limit=5 "transformer attention"
./retrievr-cli get arxiv:2401.12345
./retrievr-cli sources --format=json
```

The CLI reads per-call API keys from `RETRIEVR_<SOURCEID>_API_KEY` env vars (e.g. `RETRIEVR_S2_API_KEY`).

## Contributing

Local CI sim mirrors `.github/workflows/ci.yaml` step-for-step:

```bash
make ci              # full pipeline: tidy, build, vet, gofmt, golangci-lint, race tests, ≥80% coverage
make ci-fast         # quick iteration (skips lint + race + coverage)
make install-hooks   # auto-run `make ci` before every git push
```

Run `make help` for the full target list. The pre-push hook is the recommended path — it has caught every CI failure since v1.5.0 (gofmt drift + lint warnings + EU-mode wiring gaps) in under 2 minutes locally vs. waiting for CI to fail and re-pushing.

## Quick Start

### Connect to Claude Code

```bash
# Build
go build -o retrievr-mcp ./cmd/retrievr-mcp

# Start
./retrievr-mcp --config configs/retrievr-mcp.yaml

# Register with Claude Code (in another terminal)
claude mcp add --transport http retrievr http://localhost:8099/mcp
```

Restart Claude Code. The tools `rtv_search`, `rtv_get`, and `rtv_list_sources` are now available.

### Docker

```bash
docker build -t retrievr-mcp .
docker run -p 8099:8099 retrievr-mcp
```

The MCP endpoint is at `http://localhost:8099/mcp`. Health check at `/health`.

## Tools

### rtv_search

Search academic publications across multiple sources. Returns merged, deduplicated results.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | yes | — | Search query |
| `sources` | string[] | no | all enabled | Source IDs to search (e.g., `["arxiv", "s2"]`) |
| `content_type` | string | no | `"paper"` | `paper`, `model`, `dataset`, `video`, `place`, `image`, `post`, `package`, `patent`, `audio`, or `any` |
| `sort` | string | no | `"relevance"` | `relevance`, `date_desc`, `date_asc`, or `citations`. Per-source sort support is in `rtv_list_sources` (`supports_sort_*`) |
| `limit` | number | no | `10` | Max results (1–100) |
| `offset` | number | no | `0` | Pagination offset |
| `intent` | string | no | — | `deep_research`, `primary_source`, `quick_lookup`, `code_provenance`, `news`, `reference`. Picks the right source set + fallback chain — preferred over `sources` for most cases. See [`docs/intents.md`](docs/intents.md). |
| `filters` | object | no | — | `title`, `authors`, `date_from`, `date_to`, `categories`, `open_access`, `min_citations`, `include_domains`, `exclude_domains`, `channels`, `subreddits`, `language`. Per-provider honoring matrix in [`docs/filter-reference.md`](docs/filter-reference.md). |
| `credentials` | object | no | — | Per-call API keys (override server defaults): `pubmed_api_key`, `s2_api_key`, `openalex_api_key`, `hf_token`, `ads_api_key` |

**Example:**

```json
{
  "query": "transformer attention mechanisms",
  "sources": ["arxiv", "openalex"],
  "sort": "date_desc",
  "limit": 5,
  "filters": { "date_from": "2024-01-01" }
}
```

**Response:**

```json
{
  "total_results": 5,
  "results": [
    {
      "id": "arxiv:2401.12345",
      "source": "arxiv",
      "also_found_in": ["openalex"],
      "content_type": "paper",
      "title": "Efficient Attention Mechanisms for Transformers",
      "authors": [{"name": "J. Smith"}],
      "published": "2024-01-15",
      "abstract": "We propose...",
      "url": "https://arxiv.org/abs/2401.12345",
      "pdf_url": "https://arxiv.org/pdf/2401.12345",
      "doi": "10.1234/example",
      "citation_count": 42
    }
  ],
  "sources_queried": ["arxiv", "openalex"],
  "sources_failed": [],
  "has_more": true
}
```

### rtv_get

Retrieve a single publication by its prefixed ID. Returns full metadata and optionally BibTeX, references, citations, or full text.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `id` | string | yes | — | Prefixed ID (e.g., `"arxiv:2401.12345"`, `"s2:abc123"`, `"pubmed:12345678"`) |
| `include` | string[] | no | — | `abstract`, `full_text`, `references`, `citations`, `related`, `metadata` |
| `format` | string | no | `"native"` | `native`, `json`, `xml`, `markdown`, or `bibtex` |
| `credentials` | object | no | — | Per-call API keys |

**Example — get BibTeX:**

```json
{
  "id": "openalex:W4366341216",
  "format": "bibtex",
  "include": ["abstract"]
}
```

### rtv_list_sources

List all available sources with capabilities, rate limits, and supported content types. No parameters.

## Sources

61 plugins across five tiers. The full per-source capability surface (rate limits, supported filters, supported sort orders, residency, format support, intents served) is available at runtime via `rtv_list_sources`. The tables below are an abbreviated map; for the canonical detail see [`docs/tool-reference.md`](docs/tool-reference.md).

### v1 — Scholarly (10)

| Source | ID | Content | Key | Notes |
|--------|----|---------|-----|-------|
| ArXiv | `arxiv` | paper | optional | 2M+ OA preprints (physics, math, CS) |
| PubMed | `pubmed` | paper | optional | 36M+ biomedical citations |
| Semantic Scholar | `s2` | paper | optional | citation graph, references |
| OpenAlex | `openalex` | paper | optional | 250M+ works, open metadata |
| HuggingFace | `huggingface` | model, dataset | optional | 1M+ models, 100K+ datasets |
| Europe PMC | `europmc` | paper | none | 40M+ biomedical, full text (**EU-resident**) |
| CrossRef | `crossref` | paper | none (polite pool with `mailto`) | 150M+ DOI metadata |
| DBLP | `dblp` | paper | none | 7M+ CS publications (**EU-resident**) |
| NASA ADS | `ads` | paper | **required for use** | 16M+ astronomy/astrophysics |
| bioRxiv | `biorxiv` | paper | none | preprints, **date-range search only** |

### v2 — Web / enrichment (8)

| Source | ID | Content | Key | Notes |
|--------|----|---------|-----|-------|
| Exa | `exa` | web | **required** | neural + keyword web search |
| Brave | `brave` | web, image | **required** | independent web index, image search |
| Linkup | `linkup` | web | **required** | **EU-resident** web answers |
| Firecrawl | `firecrawl` | web | **required** | markdown extraction from arbitrary URLs |
| GitHub | `github` | code | **required** | repos + code, optional token raises 60→5000 req/h |
| Wikipedia | `wikipedia` | encyclopedia | none | extract + summary |
| Unpaywall | `unpaywall` | paper enrichment | **required** (email) | post-merge OA PDF / license enrichment |
| Perplexity | `perplexity` | web (synthesized) | **required** | Sonar online API |

### v3 — Multimodal (10)

| Source | ID | Content | Key | Notes |
|--------|----|---------|-----|-------|
| YouTube | `youtube` | video | **required** | Data API v3, channel filters |
| Scrapingdog YouTube | `scrapingdog_youtube` | video | **required** | scraping-based alt |
| Photon | `photon` | place | none | Komoot OSM geocoder |
| TomTom | `tomtom` | place | **required** | commercial POI |
| Nominatim | `nominatim` | place | none | OSM reference geocoder |
| Wikimedia | `wikimedia` | image | none | Commons + Wikipedia images |
| Europeana | `europeana` | image | **required** | EU cultural heritage |
| Mastodon | `mastodon` | post | none | Federated; post-fetch language filter |
| Bluesky | `bluesky` | post | **required** | AT Protocol; needs app password |
| Reddit | `reddit` | post | **required** | OAuth2 client credentials |

### v5 — Knowledge Commons (19)

| Source | ID | Content | Key | Notes |
|--------|----|---------|-----|-------|
| Stack Exchange | `stackexchange` | post (Q&A) | none | tags via `categories` |
| Hacker News | `hackernews` | post | none | Algolia HN search |
| Zenodo | `zenodo` | paper, dataset | none | CERN OA repository; honors `open_access` |
| CORE | `core` | paper | **required** | aggregated OA papers |
| OpenAIRE | `openaire` | paper, dataset | none | EU OA research graph |
| Wikidata | `wikidata` | reference | none | SPARQL-backed structured facts |
| DataCite | `datacite` | dataset | none | DOI metadata for research outputs |
| ORCID | `orcid` | reference (people) | none | researcher disambiguation |
| npm | `npm` | package | none | JavaScript registry |
| PyPI | `pypi` | package | none | Python packages |
| crates | `crates` | package | none | Rust packages |
| pkg.go.dev | `pkggodev` | package | none | Go modules |
| Google Patents | `googlepatents` | patent | none | scraping-based |
| EPO OPS | `epoops` | patent | **required** | OAuth2 consumer key/secret |
| CourtListener | `courtlistener` | paper (law) | none | US court decisions |
| EUR-Lex | `eurlex` | paper (law) | none | EU legislation |
| GDELT | `gdelt` | web (news) | none | global news monitoring |
| IA Scholar | `iascholar` | paper | none | Internet Archive scholarly index |
| Wayback | `wayback` | web | none | Internet Archive Wayback Machine |

### v6 — GeoExpansion + Premium (14)

| Source | ID | Content | Key | Notes |
|--------|----|---------|-----|-------|
| Google Places | `googleplaces` | place | **required** | Places API (New) |
| OSM Overpass | `osmoverpass` | place | none | raw QL behind opt-in `extra.allow_raw_ql` |
| HERE | `here` | place | **required** | Discover API |
| Listen Notes | `listennotes` | audio | **required** | podcast search |
| iTunes | `itunes` | audio | none | Apple podcast directory |
| Dimensions | `dimensions` | paper | **required** | premium citation graph |
| Lens.org | `lens` | paper, patent | **required** | premium scholarly + patents |
| Kagi | `kagi` | web | **required** | premium ad-free search |
| Mojeek | `mojeek` | web | **required** | **EU-resident** independent index |
| SerpAPI | `serpapi` | web | **required** | Google SERP via SerpAPI |
| Wolfram Alpha | `wolframalpha` | reference | **required** | computational facts |
| Google KG | `kgapi` | reference | **required** | Knowledge Graph entity lookup |
| NewsAPI | `newsapi` | web (news) | **required** | premium news aggregator |
| SerpAPI News | `serpapinews` | web (news) | **required** | Google News via SerpAPI |

Use `rtv_list_sources` for the runtime catalog (capabilities, residency, free-tier flag).

### Environment variable overrides

API keys can be injected via environment variables, useful for K8s secrets:

```
RETRIEVR_S2_API_KEY=...
RETRIEVR_PUBMED_API_KEY=...
RETRIEVR_OPENALEX_API_KEY=...
RETRIEVR_HUGGINGFACE_API_KEY=...
RETRIEVR_ADS_API_KEY=...
```

Env vars override the corresponding `api_key` value in the YAML config.

## Configuration

Config is loaded from YAML. Default: `configs/retrievr-mcp.yaml`. Override: `--config <path>`. Local dev: `configs/retrievr-mcp.local.yaml` (gitignored).

```yaml
server:
  name: "retrievr-mcp"
  http_addr: ":8099"
  log_level: "info"             # debug, info, warn, error
  log_format: "json"            # json, text

router:
  default_sources: ["arxiv", "s2", "openalex", "pubmed", "huggingface", "europmc"]
  per_source_timeout: "10s"
  dedup_enabled: true
  cache_enabled: true
  cache_ttl: "5m"
  cache_max_entries: 1000

sources:
  arxiv:
    enabled: true
    base_url: "http://export.arxiv.org/api/query"
    timeout: "15s"
    rate_limit: 0.33            # ArXiv asks for max 1 req/3s
    rate_limit_burst: 1

  s2:
    enabled: true
    api_key: ""                 # Semantic Scholar API key (optional)
    timeout: "10s"
    rate_limit: 1.0
    rate_limit_burst: 3

  openalex:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      mailto: "you@example.com" # Enters polite pool for higher limits

  pubmed:
    enabled: true
    api_key: ""                 # NCBI API key (optional, raises limit to 10 req/s)
    timeout: "10s"
    rate_limit: 3.0
    rate_limit_burst: 3
    extra:
      tool: "retrievr-mcp"     # Required by NCBI E-utilities
      email: "you@example.com"

  europmc:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5

  huggingface:
    enabled: true
    api_key: ""                 # HuggingFace token (optional)
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      include_models: "true"
      include_datasets: "true"
      include_papers: "true"
```

## Architecture

All source code lives in `internal/` as a flat single package (`package internal`). Files are named `rtv.<concern>.go`.

### Request flow

```
rtv_search
  |
  v
Fan-out to N sources concurrently (requests limit*2 per source)
  |
  v
Per-source timeout (default 10s)
  |
  v
Merge results, deduplicate by DOI/ArXiv ID
  |
  v
Round-robin interleave by source-rank position
  |
  v
Truncate to requested limit
  |
  v
Return results + sources_queried + sources_failed
```

### Plugin system

Each source implements the `SourcePlugin` interface defined in `rtv.plugin.go`. Adding a new source requires:

1. Create `internal/rtv.plugin.<source>.go` implementing the interface
2. Add source ID constant to `rtv.types.go`
3. Add factory entry to `PluginFactories()` in `rtv.registry.go`
4. Add config block to `configs/retrievr-mcp.yaml`

See [docs/plugin-guide.md](docs/plugin-guide.md) for the full guide.

### Credential resolution

1. Per-call credentials (passed in tool request)
2. Server config credentials (from YAML)
3. Anonymous (no credentials)

## Observability

| Endpoint | Description |
|----------|-------------|
| `/health` | `{"status":"ok","version":"..."}` |
| `/version` | Full version info |
| `/metrics` | Prometheus metrics |
| `/mcp` | MCP Streamable HTTP endpoint |

**Prometheus metrics** (namespace `rtv_`):

| Metric | Type | Description |
|--------|------|-------------|
| `rtv_search_total` | counter | Searches by source and status |
| `rtv_search_duration_seconds` | histogram | Search latency |
| `rtv_get_total` | counter | Gets by source and status |
| `rtv_rate_limit_waits_total` | counter | Rate limit waits by source |
| `rtv_cache_hits_total` | counter | Cache hits |
| `rtv_cache_misses_total` | counter | Cache misses |

## Development

```bash
go build -o retrievr-mcp ./cmd/retrievr-mcp    # Build
go test -race ./...                              # Unit tests
go test -race -tags integration ./...            # Integration tests (live APIs)
golangci-lint run ./...                          # Lint
```

### Dependencies

| Module | Purpose |
|--------|---------|
| [mcp-go](https://github.com/mark3labs/mcp-go) | MCP protocol SDK |
| [yaml.v3](https://pkg.go.dev/gopkg.in/yaml.v3) | Config loading |
| [validator](https://github.com/go-playground/validator) | Config validation |
| [x/time/rate](https://pkg.go.dev/golang.org/x/time/rate) | Token-bucket rate limiting |
| [client_golang](https://github.com/prometheus/client_golang) | Prometheus metrics |
| [testify](https://github.com/stretchr/testify) | Test assertions |

## License

MIT — see [LICENSE](LICENSE).
