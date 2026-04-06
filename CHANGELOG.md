# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

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
