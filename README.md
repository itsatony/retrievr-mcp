# retrievr-mcp

![Go](https://img.shields.io/badge/Go-1.25.5-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/License-MIT-blue)
![MCP](https://img.shields.io/badge/MCP-2025--11--25-purple)

An MCP server that gives LLM agents unified access to academic publications, AI research, models, and datasets. Six source APIs — ArXiv, PubMed, Semantic Scholar, OpenAlex, HuggingFace, and Europe PMC — behind three tools.

## What it does

- **Searches** fan out to all requested sources concurrently
- **Results** are merged, deduplicated by DOI/ArXiv ID, and interleaved round-robin across sources
- **Each result** includes title, authors, date, abstract, URL, DOI, and source-specific metadata
- **rtv_get** retrieves full details for a single publication, including BibTeX, references, and citations
- **Per-call credentials** let each caller supply their own API keys

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
| `content_type` | string | no | `"paper"` | `paper`, `model`, `dataset`, or `any` |
| `sort` | string | no | `"relevance"` | `relevance`, `date_desc`, `date_asc`, or `citations` |
| `limit` | number | no | `10` | Max results (1–100) |
| `offset` | number | no | `0` | Pagination offset |
| `filters` | object | no | — | `title`, `authors`, `date_from`, `date_to`, `categories`, `open_access`, `min_citations` |
| `credentials` | object | no | — | Per-call API keys: `pubmed_api_key`, `s2_api_key`, `openalex_api_key`, `hf_token` |

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

| Source | ID | Content | Auth | Rate Limit | Key Features |
|--------|----|---------|------|------------|--------------|
| ArXiv | `arxiv` | papers | No | 0.33 req/s | 2M+ open-access preprints |
| Semantic Scholar | `s2` | papers | Optional | 1 req/s (100 with key) | Citation graph, references |
| OpenAlex | `openalex` | papers | Optional | 10 req/s | 250M+ works, open metadata |
| PubMed | `pubmed` | papers | Optional | 3 req/s (10 with key) | 35M+ biomedical articles |
| Europe PMC | `europmc` | papers | No | 10 req/s | 40M+ biomedical, full text |
| HuggingFace | `huggingface` | papers, models, datasets | Optional | 10 req/s | 1M+ models, 100K+ datasets |

All sources work without API keys. Keys raise rate limits and unlock additional features.

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
