# `pkg/retrievr` Library Guide

retrievr ships as both an MCP server (`cmd/retrievr-mcp`) and an
importable Go library (`pkg/retrievr`). The library lets in-process
consumers (liz, nexus, your own services) call retrievr without paying
the MCP HTTP/JSON overhead.

## Quick-start

```go
package main

import (
    "context"
    "encoding/json"
    "log/slog"
    "os"

    rtv "github.com/itsatony/retrievr-mcp/pkg/retrievr"
)

func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

    client, cleanup, err := rtv.NewClientFromConfig("configs/retrievr-mcp.yaml", logger)
    if err != nil { panic(err) }
    defer cleanup()

    ctx := rtv.WithCredentials(context.Background(), map[string]string{
        "exa":   os.Getenv("EXAAI"),
        "brave": os.Getenv("BRAVE_SEARCH"),
    })

    result, err := client.SearchV2(ctx, rtv.SearchParams{
        Query:  "transformer attention mechanisms",
        Limit:  10,
        Intent: rtv.IntentDeepResearch,
    }, nil)
    if err != nil { panic(err) }

    enc := json.NewEncoder(os.Stdout)
    enc.SetIndent("", "  ")
    _ = enc.Encode(result)
}
```

## Worked examples

### 1. Mixed-kind search returning paper + web + code

```go
result, err := client.SearchV2(ctx, rtv.SearchParams{
    Query: "sparse attention production",
    Limit: 10,
}, []string{"arxiv", "exa", "github"}) // explicit sources

for _, r := range result.Results {
    switch r.Kind {
    case rtv.KindPaper:
        fmt.Printf("PAPER  %s  %s\n", r.Paper.DOI, r.Title)
    case rtv.KindWeb:
        fmt.Printf("WEB    %s  %s\n", r.Domain, r.Title)
    case rtv.KindCode:
        fmt.Printf("CODE   %s★%d  %s\n", r.Code.Repo, *r.Code.Stars, r.Title)
    }
}
```

### 2. EU-strict deployment

`NewClientFromConfig` reads the top-level `eu_mode` block from YAML:

```yaml
eu_mode:
  mode: "eu_strict"
  include_public_research: true   # admit ArXiv, OpenAlex, etc.
```

Then in code:

```go
result, err := client.SearchV2(ctx, params, []string{"linkup", "exa"})
if errors.Is(err, rtv.ErrEUModeProviderConflict) {
    // exa is non-EU; refuse rather than silently drop
    fmt.Println("eu_strict refused: exa is US-resident")
    return
}
// On success, result.SourcesSkipped will list any providers gated out;
// result.AuditRef correlates this call to the audit log
```

### 3. Exact-window news query (v2.22.0)

```go
// Sub-day freshness window. PublishedAfter / PublishedBefore are RFC3339
// and have exclusive boundaries. The router pushes the precise timestamp
// into NewsAPI / GDELT / HackerNews / YouTube; for Brave, Exa, and other
// coarse-precision sources it downcasts to a day floor upstream and
// post-filters merged results against each hit's published_at.
result, err := client.SearchV2(ctx, rtv.SearchParams{
    Query:  "AI Act enforcement",
    Intent: rtv.IntentNews,
    Filters: rtv.SearchFilters{
        PublishedAfter:  "2026-05-23T08:00:00Z",
        PublishedBefore: "2026-05-23T18:00:00Z",
    },
}, nil)
if err != nil { panic(err) }

// Discover per-source handling: rtv_list_sources surfaces a tri-state
// supports_published_after_filter ("native" | "coarse+postfilter" | "none").
// Sources with "none" emit no usable per-hit timestamp; their results
// pass through unfiltered unless filters.StrictPublishedAt is true.
```

### 4. Intent-driven fallback

```go
// quick_lookup tries the web chain (brave + exa) and falls through to
// linkup + wikipedia if the primary set yields zero results.
result, err := client.SearchV2(ctx, rtv.SearchParams{
    Query:  "what is RAG",
    Intent: rtv.IntentQuickLookup,
}, nil) // Sources nil so Intent drives resolution

if result.FallbackWalked {
    fmt.Println("primary chain yielded zero; fallback walked")
}
```

### 5. Streaming search

`Client.Stream` is useful for slow providers (Perplexity Sonar median
5-13s) where progressive rendering beats waiting for a full merge.

```go
ch, err := client.Stream(ctx, rtv.SearchParams{
    Query: "compare encoder-only vs decoder-only transformers",
}, []string{"perplexity", "exa", "wikipedia"})
if err != nil { panic(err) }

for ev := range ch {
    if ev.Err != nil {
        fmt.Printf("[%s] error: %v\n", ev.Source, ev.Err)
        continue
    }
    for _, pub := range ev.Result.Results {
        fmt.Printf("[%s] %s — %s\n", ev.Source, pub.ID, pub.Title)
    }
}
```

Streaming trade-offs: no cross-source dedup, no fallback walk. Use
`Search`/`SearchV2` when you need either.

### 6. Per-call credential injection

```go
ctx := rtv.WithCredentials(context.Background(), map[string]string{
    "exa":      tenantA.ExaKey,
    "github":   tenantA.GithubPAT,
    "linkup":   tenantA.LinkupKey,
})
result, err := client.SearchV2(ctx, params, nil)
```

Per-call credentials override server-default keys for that call only.
Different tenants get independent rate-limit buckets keyed on
`(sourceID, sha256(credential))`.

### 7. Auditing every call

By default, every Search emits an `AuditEvent` to slog with:
- `audit_ref` (also returned in `result.AuditRef`)
- `query_hash` (sha256:16 of the raw query, no plaintext)
- `mode`, `intent`, `providers_invoked / skipped / failed`

Plug in a custom sink to forward events to your observability pipeline:

```go
// You'll need direct Router construction for now; the higher-level
// NewClientFromConfig will gain WithAuditSink option in v2.1.
import "github.com/itsatony/retrievr-mcp/internal" // not normally importable cross-module

myCustomSink := /* your AuditSink impl */

router := internal.NewRouter(...,
    internal.WithAuditSink(myCustomSink),
    internal.WithAuditLogQueryPlaintext(false),
)
```

(For external modules, use `retrievr.NewSlogAuditSink(myLogger)` for now;
v2.1.0 will expose `Router.WithAuditSink` directly via `Client` options.)

## Public API surface

```go
// Construction
func NewClientFromConfig(configPath string, logger *slog.Logger) (*Client, cleanup func(), err error)
func NewClientFromRouter(router *internal.Router, opts ...ClientOption) *Client  // escape hatch

// Search
func (c *Client) Search(ctx, params, sources) (*MergedSearchResult, error)        // v1 shape (Publication)
func (c *Client) SearchV2(ctx, params, sources) (*MergedSearchResultV2, error)    // v2 shape (Result)
func (c *Client) Stream(ctx, params, sources) (<-chan StreamEvent, error)         // progressive
func (c *Client) Get(ctx, prefixedID, include, format) (*Publication, error)
func (c *Client) ListSources(ctx) []SourceInfo

// Credentials
func WithCredentials(ctx, map[string]string) context.Context
func CredentialsFromContext(ctx) map[string]string
func CredentialFor(ctx, sourceID) string

// Types (all aliases of internal types)
type Result, MergedSearchResult, MergedSearchResultV2, SearchParams, ...
type AuditEvent, AuditSink, ...
type ResultKind (KindPaper, KindWeb, ...)
type Intent (IntentDeepResearch, IntentQuickLookup, ...)
type EUMode (EUModeOff, EUModePreferred, EUModeStrict)
type Region, DPAStatus, ResidencyTag, FallbackChain, ...

// Sentinel errors
var ErrEUModeProviderConflict, ErrCompatV1Sunset, ErrFallbackExhausted, ...
```

## Versioning + stability

- v2.0.0 is the first major release with a stable public surface.
- `pkg/retrievr` aliases internal types — alias targets may evolve,
  but field-level changes are governed by semver.
- The MCP `rtv_search` wire format is **v2 by default** in v2.0.0;
  explicit `compat: "v1"` returns `RTV_COMPAT_V1_SUNSET`.

## See also

- `docs/architecture.md` — internals
- `docs/eu-mode.md` — EU-GDPR mode reference
- `docs/intents.md` — per-intent fallback chains
- `docs/residency.md` — provider residency table
- `docs/plugin-guide.md` — write your own SourcePlugin
- `cmd/retrievr-cli/` — reference consumer (~500 LoC, stdlib-only)
- `~/code/liz/internal/retrievr/` — second-module integration spike
