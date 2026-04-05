# retrievr-mcp

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/License-MIT-blue)
![MCP](https://img.shields.io/badge/MCP-2025--11--25-purple)

## Overview

retrievr-mcp is an MCP server ([Model Context Protocol](https://modelcontextprotocol.io/)) that gives LLM agents unified access to academic publications, AI research, models, and datasets. It abstracts six different source APIs -- ArXiv, PubMed, Semantic Scholar, OpenAlex, HuggingFace, and Europe PMC -- behind three MCP tools: `rtv_search`, `rtv_get`, and `rtv_list_sources`.

The server uses a plugin architecture where each source is an independent plugin implementing a common interface. Searches fan out to all requested sources concurrently, results are merged and deduplicated by DOI or ArXiv ID, and a round-robin interleaving strategy prevents bias toward any single source.

Per-call credentials allow multi-tenant usage where each caller can supply their own API keys, overriding server defaults. An in-memory LRU cache, per-source rate limiting with per-credential buckets, and Prometheus metrics round out the operational features.

## Features

- **6 academic sources** -- ArXiv, PubMed, Semantic Scholar, OpenAlex, HuggingFace, Europe PMC
- **3 MCP tools** -- `rtv_search`, `rtv_get`, `rtv_list_sources`
- **Cross-source deduplication** by exact DOI or ArXiv ID match
- **Per-call credentials** -- callers supply API keys per request for multi-tenant setups
- **Per-credential rate limit buckets** -- independent rate tracking per caller, TTL-evicted after 15 minutes of inactivity
- **In-memory LRU cache** with configurable TTL and max entries
- **BibTeX generation** from publication metadata across all sources
- **Prometheus metrics** -- counters, histograms, custom registry
- **Round-robin relevance sorting** -- interleaves results by source-rank position to avoid single-source bias
- **Graceful partial failure** -- working sources return results while failed sources are reported in `sources_failed`
- **Structured JSON logging** via `log/slog`

## Quick Start

### Docker

```bash
docker build -t retrievr-mcp .
docker run -p 8099:8099 retrievr-mcp
```

### Binary

```bash
go build -o retrievr-mcp ./cmd/retrievr-mcp
./retrievr-mcp --config configs/retrievr-mcp.yaml
```

The server listens on port **8099** by default. The MCP endpoint is available at `/mcp`.

## Configuration

Configuration is loaded from a YAML file. Default: `configs/retrievr-mcp.yaml`. Override with `--config <path>`. For local development, use `configs/retrievr-mcp.local.yaml` (gitignored).

```yaml
server:
  name: "retrievr-mcp"          # Server name reported in MCP handshake
  http_addr: ":8099"            # HTTP listen address (host:port)
  log_level: "info"             # Log level: debug, info, warn, error
  log_format: "json"            # Log format: json, text

router:
  default_sources:              # Sources used when caller does not specify
    - "arxiv"
    - "s2"
    - "openalex"
    - "pubmed"
    - "huggingface"
    - "europmc"
  per_source_timeout: "10s"     # Timeout for each source plugin per request
  dedup_enabled: true           # Enable cross-source dedup by DOI/ArXiv ID
  cache_enabled: true           # Enable in-memory LRU response cache
  cache_ttl: "5m"               # Cache entry time-to-live
  cache_max_entries: 1000       # Maximum cached entries

sources:
  arxiv:
    enabled: true
    base_url: "http://export.arxiv.org/api/query"
    timeout: "15s"              # HTTP client timeout for this source
    rate_limit: 0.33            # Requests per second (ArXiv asks for 1 req/3s)
    rate_limit_burst: 1         # Token bucket burst size

  pubmed:
    enabled: true
    api_key: ""                 # NCBI API key (optional, raises limit to 10 req/s)
    timeout: "10s"
    rate_limit: 3.0
    rate_limit_burst: 3
    extra:
      tool: "retrievr-mcp"     # Required by NCBI E-utilities
      email: "contact@example.com"

  s2:
    enabled: true
    api_key: ""                 # Semantic Scholar API key (optional)
    timeout: "10s"
    rate_limit: 1.0
    rate_limit_burst: 3

  openalex:
    enabled: true
    api_key: ""                 # OpenAlex API key (optional, for polite pool use mailto)
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      mailto: "contact@example.com"  # Enters the polite pool for higher limits

  huggingface:
    enabled: true
    api_key: ""                 # HuggingFace token (optional)
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
    extra:
      include_models: "true"    # Search HuggingFace models
      include_datasets: "true"  # Search HuggingFace datasets
      include_papers: "true"    # Search HuggingFace papers

  europmc:
    enabled: true
    timeout: "10s"
    rate_limit: 10.0
    rate_limit_burst: 5
```

## Tools

### rtv_search

Search academic publications across multiple sources. Returns merged, deduplicated results.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | yes | -- | Search query string |
| `sources` | string[] | no | server default | Source IDs to search (e.g., `["arxiv", "s2"]`) |
| `content_type` | string | no | `"paper"` | `paper`, `model`, `dataset`, or `any` |
| `sort` | string | no | `"relevance"` | `relevance`, `date_desc`, `date_asc`, or `citations` |
| `limit` | number | no | `10` | Max results (1-100) |
| `offset` | number | no | `0` | Pagination offset |
| `filters` | object | no | -- | Filters: `title`, `authors`, `date_from`, `date_to`, `categories`, `open_access`, `min_citations` |
| `credentials` | object | no | -- | Per-call API keys: `pubmed_api_key`, `s2_api_key`, `openalex_api_key`, `hf_token` |

**Example request:**

```json
{
  "query": "transformer attention mechanisms",
  "sources": ["arxiv", "s2"],
  "content_type": "paper",
  "sort": "relevance",
  "limit": 5,
  "filters": {
    "date_from": "2023-01-01"
  }
}
```

**Example response (abbreviated):**

```json
{
  "results": [
    {
      "id": "arxiv:2401.12345",
      "source": "arxiv",
      "also_found_in": ["s2"],
      "content_type": "paper",
      "title": "Efficient Attention Mechanisms for Transformers",
      "authors": [{"name": "J. Smith"}],
      "published": "2024-01-15",
      "abstract": "We propose...",
      "url": "https://arxiv.org/abs/2401.12345",
      "doi": "10.1234/example"
    }
  ],
  "total": 5,
  "sources_searched": ["arxiv", "s2"],
  "sources_failed": []
}
```

### rtv_get

Retrieve a single publication by its prefixed ID. Returns full metadata and optionally abstract, full text, references, citations, or related works.

**Parameters:**

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `id` | string | yes | -- | Prefixed ID (e.g., `"arxiv:2401.12345"`, `"s2:abc123"`) |
| `include` | string[] | no | `["abstract"]` | Data to include: `abstract`, `full_text`, `references`, `citations`, `related`, `metadata` |
| `format` | string | no | `"native"` | Output format: `native`, `json`, `xml`, `markdown`, `bibtex` |
| `credentials` | object | no | -- | Per-call API keys |

**Example request:**

```json
{
  "id": "arxiv:2401.12345",
  "include": ["abstract", "references"],
  "format": "bibtex"
}
```

### rtv_list_sources

List all available sources with their capabilities, rate limits, and supported features. Takes no parameters.

**Example response (abbreviated):**

```json
[
  {
    "id": "arxiv",
    "name": "ArXiv",
    "description": "...",
    "content_types": ["paper"],
    "native_format": "xml",
    "available_formats": ["native", "json", "xml", "markdown", "bibtex"]
  }
]
```

## Sources

| Source | ID | Content Types | Auth Required | Rate Limit (default) | Native Format | Notable Features |
|--------|----|---------------|---------------|---------------------|---------------|-----------------|
| ArXiv | `arxiv` | paper | No | 0.33 req/s | XML | 2M+ preprints, open access |
| Semantic Scholar | `s2` | paper | Optional API key | 1 req/s | JSON | Citation graph data |
| OpenAlex | `openalex` | paper | Optional (polite pool) | 10 req/s | JSON | 250M+ works, open metadata |
| PubMed | `pubmed` | paper | Optional API key | 3 req/s (10 with key) | XML | 35M+ biomedical articles |
| Europe PMC | `europmc` | paper | No | 10 req/s | JSON | 40M+ biomedical, full text access |
| HuggingFace | `huggingface` | paper, model, dataset | Optional token | 10 req/s | JSON | 1M+ models, datasets, papers |

## Architecture

All source code lives in `internal/` as a flat single package. Files follow the naming convention `rtv.<concern>.go` (e.g., `rtv.plugin.arxiv.go`, `rtv.router.go`, `rtv.server.go`).

### Plugin interface

Every source implements the `SourcePlugin` interface:

```go
type SourcePlugin interface {
    ID() string
    Name() string
    Description() string
    ContentTypes() []ContentType
    Capabilities() SourceCapabilities
    NativeFormat() ContentFormat
    AvailableFormats() []ContentFormat
    Search(ctx context.Context, params SearchParams, creds *CallCredentials) (*SearchResult, error)
    Get(ctx context.Context, id string, include []IncludeField, format ContentFormat, creds *CallCredentials) (*Publication, error)
    Initialize(ctx context.Context, cfg PluginConfig) error
    Health(ctx context.Context) SourceHealth
}
```

### Router fan-out flow

```
rtv_search request
       |
       v
  Fan-out to N plugins concurrently (limit * 2 per source)
       |
       v
  Per-plugin timeout (default 10s)
       |
       v
  Merge all results into single list
       |
       v
  Deduplicate by exact DOI / ArXiv ID
       |
       v
  Round-robin interleave by source-rank position
       |
       v
  Truncate to requested limit
       |
       v
  Return results + sources_searched + sources_failed
```

### Credential resolution order

1. Per-call credentials (passed in tool request)
2. Server config credentials (from YAML)
3. Anonymous (no credentials)

## Observability

### Endpoints

| Path | Description |
|------|-------------|
| `/health` | Health check, returns `{"status":"ok","version":"..."}` |
| `/version` | Full version information |
| `/metrics` | Prometheus metrics (custom registry) |
| `/mcp` | MCP Streamable HTTP endpoint |

### Prometheus metrics

All metrics use the `rtv_` namespace.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `rtv_search_total` | counter | `source`, `status` | Total search operations by source and status |
| `rtv_search_duration_seconds` | histogram | `source` | Search latency in seconds (buckets: 0.1 to 30s) |
| `rtv_get_total` | counter | `source`, `status` | Total get operations by source and status |
| `rtv_rate_limit_waits_total` | counter | `source` | Rate limit wait events by source |
| `rtv_cache_hits_total` | counter | -- | Cache hit count |
| `rtv_cache_misses_total` | counter | -- | Cache miss count |

### Logging

Structured JSON logging via `log/slog` to stdout. Configurable level (`debug`, `info`, `warn`, `error`) and format (`json`, `text`).

## Deployment

### Docker

```bash
docker build -t retrievr-mcp .
docker run -p 8099:8099 retrievr-mcp
```

The Docker image uses a multi-stage build (`golang:1.25-alpine` build stage, `alpine:3.21` runtime). It runs as a non-root user (`rtv`, UID 1000) and includes a healthcheck that polls `/health` every 30 seconds.

To use a custom config, mount it into the container:

```bash
docker run -p 8099:8099 -v /path/to/config.yaml:/app/configs/retrievr-mcp.yaml retrievr-mcp
```

### Port

The server listens on port **8099** by default. Change via `server.http_addr` in the config.

## Development

### Build

```bash
go build -o retrievr-mcp ./cmd/retrievr-mcp
```

### Test

```bash
# Unit tests (with race detector)
go test -race ./...

# Single package
go test -race ./internal/...

# Integration tests (requires live API access)
go test -race -tags integration ./...
```

### Lint

```bash
golangci-lint run ./...
```

### Adding a new source

To add a source plugin:

1. Create `internal/rtv.plugin.<source>.go` implementing the `SourcePlugin` interface.
2. Add the source ID constant to `rtv.types.go`.
3. Register the plugin in `cmd/retrievr-mcp/main.go`.
4. Add source config to `configs/retrievr-mcp.yaml`.

### Dependencies

| Module | Purpose |
|--------|---------|
| `github.com/mark3labs/mcp-go` | MCP protocol SDK |
| `gopkg.in/yaml.v3` | YAML config loading |
| `github.com/go-playground/validator/v10` | Config validation |
| `golang.org/x/time/rate` | Token-bucket rate limiting |
| `github.com/prometheus/client_golang` | Prometheus metrics |
| `github.com/stretchr/testify` | Test assertions |

## Contributing

1. Fork the repository.
2. Create a feature branch from `main`.
3. Implement changes following the flat package structure and `rtv.` file naming convention.
4. Write tests (table-driven, >80% coverage target, run with `-race`).
5. Run `go test -race ./...` and `golangci-lint run ./...`.
6. Open a pull request.

## License

MIT -- see [LICENSE](LICENSE).
