# Plugin Development Guide

This guide walks through every step required to add a new source plugin to retrievr-mcp. Follow each step in order; the checklist at the end verifies that nothing was missed.

---

## Overview

A plugin is a Go struct that implements the `SourcePlugin` interface defined in `internal/rtv.plugin.go`. Once registered, the plugin is automatically available through `rtv_search`, `rtv_get`, and `rtv_list_sources` — no changes to the MCP tool layer are needed.

The full interface:

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

All files in `internal/` belong to `package internal`. There are no sub-packages.

---

## Prerequisites

- Go 1.25
- Familiarity with the target source's API (authentication model, search/retrieval endpoints, response format)
- Understanding of rate limits imposed by the upstream source

---

## Step 1: Add Source ID

Open `internal/rtv.types.go`.

**1a. Add a source ID constant** in the `Source*` constant block:

```go
const (
    SourceArXiv       = "arxiv"
    SourcePubMed      = "pubmed"
    SourceS2          = "s2"
    SourceOpenAlex    = "openalex"
    SourceHuggingFace = "huggingface"
    SourceEuropePMC   = "europmc"
    SourceMySource    = "mysource"   // <-- add here
)
```

Use a lowercase, URL-safe string. This value is the stable public identifier — it appears in API requests, config files, Publication IDs, and log output.

**1b. Add the constant to `validSourceIDs`:**

```go
var validSourceIDs = map[string]bool{
    SourceArXiv:       true,
    SourcePubMed:      true,
    SourceS2:          true,
    SourceOpenAlex:    true,
    SourceHuggingFace: true,
    SourceEuropePMC:   true,
    SourceMySource:    true,   // <-- add here
}
```

**1c. Increment `SourceCount`:**

```go
const SourceCount = 7   // was 6
```

`AllSourceIDs()` is derived from `validSourceIDs` at runtime and requires no change.

---

## Step 2: Add Error Constants

Open `internal/rtv.errors.go`. Add a new sentinel section following the pattern established by every existing plugin. Place it after the last plugin block (currently HuggingFace):

```go
// ---------------------------------------------------------------------------
// Sentinel errors — plugin: MySource
// ---------------------------------------------------------------------------

const (
    ErrMsgMySourceJSONParse   = "failed to parse mysource json response"
    ErrMsgMySourceHTTPRequest = "mysource http request failed"
    ErrMsgMySourceNotFound    = "mysource item not found"
    ErrMsgMySourceEmptyQuery  = "search query is empty"
)

var (
    ErrMySourceJSONParse   = errors.New(ErrMsgMySourceJSONParse)
    ErrMySourceHTTPRequest = errors.New(ErrMsgMySourceHTTPRequest)
    ErrMySourceNotFound    = errors.New(ErrMsgMySourceNotFound)
    ErrMySourceEmptyQuery  = errors.New(ErrMsgMySourceEmptyQuery)
)
```

Rules:
- Every `ErrMsg*` constant is a plain string (no format verbs).
- Every `Err*` sentinel is constructed with `errors.New(ErrMsg*)`.
- Wrap sentinel errors with `fmt.Errorf("%w: %w", ErrMySourceHTTPRequest, err)` inside plugin methods — never format the sentinel strings directly.
- Add more specific constants if the source has additional distinct failure modes (e.g., `ErrMsgMySourceAuthFailed`).

---

## Step 3: Create the Plugin File

Create `internal/rtv.plugin.mysource.go`. The file must be `package internal`.

### Structure

#### 3a. Constants section

Every string that appears more than once, or that comes from an external spec, must be a named constant. No magic strings.

```go
// ---------------------------------------------------------------------------
// MySource plugin identity constants
// ---------------------------------------------------------------------------

const (
    mySourcePluginID          = "mysource"
    mySourcePluginName        = "MySource"
    mySourcePluginDescription = "Short description for LLM context"
)

// ---------------------------------------------------------------------------
// MySource API constants
// ---------------------------------------------------------------------------

const (
    mySourceDefaultBaseURL    = "https://api.mysource.example/v1"
    mySourceSearchPath        = "/search"
    mySourceGetPath           = "/works"
    mySourceMaxResultsPerPage = 100
    mySourceCategoriesHint    = ""           // leave empty if not applicable
)

// ---------------------------------------------------------------------------
// MySource API parameter name constants
// ---------------------------------------------------------------------------

const (
    mySourceParamQuery  = "q"
    mySourceParamLimit  = "limit"
    mySourceParamOffset = "offset"
    // ... all query param names
)

// ---------------------------------------------------------------------------
// MySource HTTP constants
// ---------------------------------------------------------------------------

const (
    mySourceHTTPStatusErrFmt = "status %d"
    mySourceMaxResponseBytes = 10 << 20   // 10 MB — matches the codebase-wide limit
    mySourceAuthHeader       = "Authorization"
    mySourceAuthPrefix       = "Bearer "
)
```

#### 3b. Response struct definitions

Define private structs that mirror the upstream JSON (or XML) response. Keep them private (lowercase) and scoped to this file.

```go
type mySourceSearchResponse struct {
    Total   int              `json:"total"`
    Results []mySourceItem   `json:"results"`
}

type mySourceItem struct {
    ID      string          `json:"id"`
    Title   string          `json:"title"`
    // ...
}
```

#### 3c. Plugin struct

```go
// MySourcePlugin implements SourcePlugin for MySource.
// Thread-safe for concurrent use after Initialize.
type MySourcePlugin struct {
    baseURL    string
    apiKey     string
    httpClient *http.Client
    enabled    bool
    rateLimit  float64

    mu        sync.RWMutex   // protects health state below
    healthy   bool
    lastError string
}
```

#### 3d. Identity methods

```go
func (p *MySourcePlugin) ID() string          { return mySourcePluginID }
func (p *MySourcePlugin) Name() string        { return mySourcePluginName }
func (p *MySourcePlugin) Description() string { return mySourcePluginDescription }

func (p *MySourcePlugin) ContentTypes() []ContentType {
    return []ContentType{ContentTypePaper}  // adjust to match the source
}

func (p *MySourcePlugin) NativeFormat() ContentFormat { return FormatJSON }

func (p *MySourcePlugin) AvailableFormats() []ContentFormat {
    return []ContentFormat{FormatJSON, FormatBibTeX}
}
```

#### 3e. Capabilities method

Fill in only what the upstream API actually supports. Be conservative — returning `true` for a capability the source cannot satisfy causes runtime errors.

```go
func (p *MySourcePlugin) Capabilities() SourceCapabilities {
    return SourceCapabilities{
        SupportsFullText:         false,
        SupportsCitations:        false,
        SupportsDateFilter:       true,
        SupportsAuthorFilter:     false,
        SupportsCategoryFilter:   false,
        SupportsSortRelevance:    true,
        SupportsSortDate:         true,
        SupportsSortCitations:    false,
        SupportsOpenAccessFilter: false,
        SupportsPagination:       true,
        MaxResultsPerQuery:       mySourceMaxResultsPerPage,
        CategoriesHint:           mySourceCategoriesHint,
        NativeFormat:             FormatJSON,
        AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
    }
}
```

#### 3f. Initialize method

`Initialize` is called exactly once at startup. It must:
- Apply `cfg.BaseURL` if set, otherwise fall back to the default constant.
- Apply `cfg.Timeout.Duration` if non-zero, otherwise use `DefaultPluginTimeout` (defined in `internal/rtv.config.go` as `10 * time.Second`).
- Store `cfg.Enabled` and `cfg.RateLimit`.
- Set `p.healthy = true`.

```go
func (p *MySourcePlugin) Initialize(_ context.Context, cfg PluginConfig) error {
    p.enabled = cfg.Enabled
    p.rateLimit = cfg.RateLimit
    p.apiKey = cfg.APIKey

    p.baseURL = cfg.BaseURL
    if p.baseURL == "" {
        p.baseURL = mySourceDefaultBaseURL
    }

    timeout := cfg.Timeout.Duration
    if timeout == 0 {
        timeout = DefaultPluginTimeout
    }

    p.httpClient = &http.Client{Timeout: timeout}
    p.healthy = true

    return nil
}
```

If the source requires extra config values (e.g., `email`, `tool`), read them from `cfg.Extra`:

```go
p.email = cfg.Extra["email"]
```

#### 3g. Health method

```go
func (p *MySourcePlugin) Health(_ context.Context) SourceHealth {
    p.mu.RLock()
    defer p.mu.RUnlock()

    return SourceHealth{
        Enabled:   p.enabled,
        Healthy:   p.healthy,
        RateLimit: p.rateLimit,
        LastError: p.lastError,
    }
}
```

#### 3h. Search method

```go
func (p *MySourcePlugin) Search(ctx context.Context, params SearchParams, creds *CallCredentials) (*SearchResult, error) {
    if params.Query == "" {
        return nil, ErrMySourceEmptyQuery
    }

    apiKey := ""
    if creds != nil {
        apiKey = creds.ResolveForSource(mySourcePluginID, p.apiKey)
    } else {
        apiKey = p.apiKey
    }

    reqURL := buildMySourceSearchURL(p.baseURL, params)

    data, err := p.doRequest(ctx, reqURL, apiKey)
    if err != nil {
        p.recordError(err)
        return nil, fmt.Errorf("%w: %w", ErrSearchFailed, err)
    }

    p.recordSuccess()

    var resp mySourceSearchResponse
    if err := json.Unmarshal(data, &resp); err != nil {
        return nil, fmt.Errorf("%w: %w", ErrMySourceJSONParse, err)
    }

    pubs := make([]Publication, 0, len(resp.Results))
    for i := range resp.Results {
        pubs = append(pubs, mapMySourceItemToPublication(&resp.Results[i]))
    }

    hasMore := (params.Offset + len(pubs)) < resp.Total

    return &SearchResult{
        Total:   resp.Total,
        Results: pubs,
        HasMore: hasMore,
    }, nil
}
```

#### 3i. Get method

The `id` parameter arrives without the source prefix (the router strips `"mysource:"` before calling `Get`).

```go
func (p *MySourcePlugin) Get(ctx context.Context, id string, _ []IncludeField, format ContentFormat, creds *CallCredentials) (*Publication, error) {
    apiKey := ""
    if creds != nil {
        apiKey = creds.ResolveForSource(mySourcePluginID, p.apiKey)
    } else {
        apiKey = p.apiKey
    }

    reqURL := p.baseURL + mySourceGetPath + "/" + url.PathEscape(id)

    data, err := p.doRequest(ctx, reqURL, apiKey)
    if err != nil {
        p.recordError(err)
        return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
    }

    var item mySourceItem
    if err := json.Unmarshal(data, &item); err != nil {
        return nil, fmt.Errorf("%w: %w", ErrMySourceJSONParse, err)
    }

    if item.ID == "" {
        return nil, fmt.Errorf("%w: %s", ErrMySourceNotFound, id)
    }

    p.recordSuccess()

    pub := mapMySourceItemToPublication(&item)

    // Handle non-native format requests here if needed.
    _ = format

    return &pub, nil
}
```

#### 3j. HTTP request helper

Use `io.LimitReader` capped at `mySourceMaxResponseBytes` (10 MB) to prevent unbounded memory growth. Always drain and discard the body on non-200 responses so the HTTP connection can be reused.

```go
func (p *MySourcePlugin) doRequest(ctx context.Context, reqURL string, apiKey string) ([]byte, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
    if err != nil {
        return nil, fmt.Errorf("%w: %w", ErrMySourceHTTPRequest, err)
    }

    if apiKey != "" {
        req.Header.Set(mySourceAuthHeader, mySourceAuthPrefix+apiKey)
    }

    resp, err := p.httpClient.Do(req)
    if err != nil {
        if ctx.Err() != nil {
            return nil, fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
        }
        return nil, fmt.Errorf("%w: %w", ErrMySourceHTTPRequest, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        _, _ = io.Copy(io.Discard, resp.Body)
        return nil, fmt.Errorf("%w: "+mySourceHTTPStatusErrFmt, ErrMySourceHTTPRequest, resp.StatusCode)
    }

    limitedBody := io.LimitReader(resp.Body, mySourceMaxResponseBytes)
    body, err := io.ReadAll(limitedBody)
    if err != nil {
        return nil, fmt.Errorf("%w: %w", ErrMySourceHTTPRequest, err)
    }

    return body, nil
}
```

#### 3k. Health state helpers

```go
func (p *MySourcePlugin) recordSuccess() {
    p.mu.Lock()
    defer p.mu.Unlock()
    p.healthy = true
    p.lastError = ""
}

func (p *MySourcePlugin) recordError(err error) {
    p.mu.Lock()
    defer p.mu.Unlock()
    p.healthy = false
    p.lastError = err.Error()
}
```

#### 3l. Mapping function

Map the source's native response struct to the unified `Publication` type. Set `ID` with the `"mysource:"` prefix. Use `YYYY-MM-DD` for `Published` and `Updated`. Set `Source` to `mySourcePluginID`. Only populate fields the source actually provides.

```go
func mapMySourceItemToPublication(item *mySourceItem) Publication {
    return Publication{
        ID:          mySourcePluginID + ":" + item.ID,
        Source:      mySourcePluginID,
        ContentType: ContentTypePaper,
        Title:       strings.TrimSpace(item.Title),
        // ... map remaining fields
    }
}
```

---

## Step 4: Create the Test File

Create `internal/rtv.plugin.mysource_test.go` in `package internal`.

### 4a. Contract test

Every plugin must pass the generic contract suite. This is non-negotiable.

```go
func TestMySourcePluginContract(t *testing.T) {
    plugin := &MySourcePlugin{}
    err := plugin.Initialize(context.Background(), PluginConfig{
        Enabled:   true,
        BaseURL:   "http://unused.test",
        Timeout:   Duration{Duration: 5 * time.Second},
        RateLimit: 1.0,
    })
    require.NoError(t, err)
    PluginContractTest(t, plugin)
}
```

`PluginContractTest` is defined in `internal/rtv.plugin_contract_test.go` and verifies:
- `ID()` is non-empty and present in `validSourceIDs`
- `Name()` and `Description()` are non-empty
- `ContentTypes()` is non-empty and contains only valid `ContentType` constants
- `NativeFormat()` is non-empty and appears in `AvailableFormats()`
- `AvailableFormats()` is non-empty
- `Health()` returns `Enabled: true` after `Initialize`
- `Capabilities().MaxResultsPerQuery >= 1` when `SupportsPagination` is true

### 4b. Unit test pattern using httptest.Server

All unit tests mock the upstream HTTP API with `net/http/httptest`. Never make live network calls in unit tests.

```go
func TestMySourceSearch(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name        string
        params      SearchParams
        handlerFunc http.HandlerFunc
        wantCount   int
        wantErr     error
    }{
        {
            name: "basic_search",
            params: SearchParams{
                Query: "neural networks",
                Limit: 10,
            },
            handlerFunc: func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Type", "application/json")
                // write a minimal valid JSON response
                _, _ = w.Write([]byte(`{"total":1,"results":[{"id":"123","title":"Test"}]}`))
            },
            wantCount: 1,
        },
        {
            name: "empty_query",
            params: SearchParams{
                Query: "",
                Limit: 10,
            },
            handlerFunc: nil, // never reached
            wantErr:     ErrMySourceEmptyQuery,
        },
        {
            name: "http_error",
            params: SearchParams{Query: "test", Limit: 10},
            handlerFunc: func(w http.ResponseWriter, _ *http.Request) {
                w.WriteHeader(http.StatusInternalServerError)
            },
            wantErr: ErrSearchFailed,
        },
        {
            name: "context_cancelled",
            params: SearchParams{Query: "test", Limit: 10},
            handlerFunc: func(w http.ResponseWriter, _ *http.Request) {
                time.Sleep(200 * time.Millisecond)
                w.WriteHeader(http.StatusOK)
            },
            wantErr: ErrSearchFailed,
        },
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()

            var plugin *MySourcePlugin
            if tc.handlerFunc != nil {
                srv := httptest.NewServer(tc.handlerFunc)
                t.Cleanup(srv.Close)
                plugin = newMySourceTestPlugin(t, srv.URL)
            } else {
                plugin = newMySourceTestPlugin(t, "http://unused.test")
            }

            ctx := context.Background()
            if tc.name == "context_cancelled" {
                var cancel context.CancelFunc
                ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
                defer cancel()
            }

            result, err := plugin.Search(ctx, tc.params, nil)

            if tc.wantErr != nil {
                require.Error(t, err)
                assert.ErrorIs(t, err, tc.wantErr)
                assert.Nil(t, result)
                return
            }

            require.NoError(t, err)
            require.NotNil(t, result)
            assert.Len(t, result.Results, tc.wantCount)
        })
    }
}
```

### 4c. Get tests

```go
func TestMySourceGet(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name        string
        id          string
        handlerFunc http.HandlerFunc
        wantErr     error
    }{
        {
            name: "valid_id",
            id:   "123",
            handlerFunc: func(w http.ResponseWriter, _ *http.Request) {
                w.Header().Set("Content-Type", "application/json")
                _, _ = w.Write([]byte(`{"id":"123","title":"Test Paper"}`))
            },
        },
        {
            name: "not_found",
            id:   "nonexistent",
            handlerFunc: func(w http.ResponseWriter, _ *http.Request) {
                w.WriteHeader(http.StatusNotFound)
            },
            wantErr: ErrGetFailed,
        },
        {
            name: "http_error",
            id:   "123",
            handlerFunc: func(w http.ResponseWriter, _ *http.Request) {
                w.WriteHeader(http.StatusInternalServerError)
            },
            wantErr: ErrGetFailed,
        },
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()

            srv := httptest.NewServer(tc.handlerFunc)
            t.Cleanup(srv.Close)
            plugin := newMySourceTestPlugin(t, srv.URL)

            pub, err := plugin.Get(context.Background(), tc.id, nil, FormatNative, nil)

            if tc.wantErr != nil {
                require.Error(t, err)
                assert.ErrorIs(t, err, tc.wantErr)
                assert.Nil(t, pub)
                return
            }

            require.NoError(t, err)
            require.NotNil(t, pub)
        })
    }
}
```

### 4d. Helper constructor

```go
func newMySourceTestPlugin(t *testing.T, baseURL string) *MySourcePlugin {
    t.Helper()
    plugin := &MySourcePlugin{}
    err := plugin.Initialize(context.Background(), PluginConfig{
        Enabled:   true,
        BaseURL:   baseURL,
        Timeout:   Duration{Duration: 5 * time.Second},
        RateLimit: 1.0,
    })
    require.NoError(t, err)
    return plugin
}
```

---

## Step 5: Register the Plugin

Open `cmd/retrievr-mcp/main.go`. Add an `if` block following the exact pattern used by every existing plugin. Add it after the last plugin block (currently HuggingFace, ending at line 163):

```go
if myCfg, ok := cfg.Sources[internal.SourceMySource]; ok && myCfg.Enabled {
    myPlugin := &internal.MySourcePlugin{}
    if err := myPlugin.Initialize(context.Background(), myCfg); err != nil {
        logger.Error(logMsgPluginInitFail,
            slog.String(internal.LogKeySource, internal.SourceMySource),
            slog.String(internal.LogKeyError, err.Error()),
        )
        return exitCodeStartup
    }
    plugins[internal.SourceMySource] = myPlugin
}
```

The rate limit manager, credential resolver, and router pick up the plugin automatically from the `plugins` map — no other changes are needed in `main.go`.

---

## Step 6: Add Config Block

Open `configs/retrievr-mcp.yaml`. Add the new source under the `sources:` key. Also add the source ID to `router.default_sources` if it should be queried by default.

```yaml
router:
  default_sources: ["arxiv", "s2", "openalex", "pubmed", "huggingface", "europmc", "mysource"]

sources:
  mysource:
    enabled: true
    api_key: ""          # omit if the source is unauthenticated
    base_url: ""         # omit to use the plugin default
    timeout: "10s"
    rate_limit: 5.0      # requests per second — check upstream docs
    rate_limit_burst: 3
    extra:               # only if the plugin reads cfg.Extra
      email: "contact@example.com"
```

`PluginConfig.Extra` is `map[string]string`. Any key/value pairs under `extra:` are passed through to `Initialize` without interpretation by the framework.

For a local development override, create `configs/retrievr-mcp.local.yaml` (already gitignored) with only the fields that differ.

---

## Step 7: (Optional) Credential Support

If the upstream source supports per-call API keys (i.e., different callers should be able to supply their own credentials), complete these three additional changes.

### 7a. Add a field to `CallCredentials` in `internal/rtv.types.go`

```go
type CallCredentials struct {
    PubMedAPIKey    string `json:"pubmed_api_key,omitempty"`
    S2APIKey        string `json:"s2_api_key,omitempty"`
    OpenAlexAPIKey  string `json:"openalex_api_key,omitempty"`
    HFToken         string `json:"hf_token,omitempty"`
    MySourceAPIKey  string `json:"mysource_api_key,omitempty"`   // <-- add here
}
```

### 7b. Add a `case` to `ResolveForSource` in `internal/rtv.types.go`

```go
func (c *CallCredentials) ResolveForSource(sourceID string, serverDefault string) string {
    if c == nil {
        return serverDefault
    }

    var perCall string
    switch sourceID {
    case SourcePubMed:
        perCall = c.PubMedAPIKey
    case SourceS2:
        perCall = c.S2APIKey
    case SourceOpenAlex:
        perCall = c.OpenAlexAPIKey
    case SourceHuggingFace:
        perCall = c.HFToken
    case SourceMySource:
        perCall = c.MySourceAPIKey   // <-- add here
    }

    if perCall != "" {
        return perCall
    }
    return serverDefault
}
```

### 7c. Add the source to `sourcesAcceptingCredentials` in `internal/rtv.router.go`

```go
var sourcesAcceptingCredentials = map[string]bool{
    SourcePubMed:      true,
    SourceS2:          true,
    SourceOpenAlex:    true,
    SourceHuggingFace: true,
    SourceMySource:    true,   // <-- add here
}
```

This map controls whether the `AcceptsCredentials` field in `rtv_list_sources` responses is `true` for this source, and whether the router passes per-call credentials through to the plugin.

---

## Checklist

Use this list to verify the implementation is complete before opening a pull request.

### Code

- [ ] `internal/rtv.types.go`: `SourceMySource` constant added
- [ ] `internal/rtv.types.go`: `validSourceIDs` map updated
- [ ] `internal/rtv.types.go`: `SourceCount` incremented
- [ ] `internal/rtv.errors.go`: `ErrMsgMySource*` constants added
- [ ] `internal/rtv.errors.go`: `ErrMySource*` sentinel variables added
- [ ] `internal/rtv.plugin.mysource.go`: file created, `package internal`
- [ ] All string literals in the plugin file are named constants — no magic strings
- [ ] `io.LimitReader` used in `doRequest` with a 10 MB cap
- [ ] Non-200 HTTP responses drain the body with `io.Copy(io.Discard, resp.Body)`
- [ ] `ctx.Err()` checked after failed `httpClient.Do` to distinguish timeout from network error
- [ ] `recordSuccess()` / `recordError()` called appropriately
- [ ] `Publication.ID` uses the `"mysource:"` prefix
- [ ] `Publication.Source` set to `mySourcePluginID`
- [ ] `Published` and `Updated` in `YYYY-MM-DD` format
- [ ] `cmd/retrievr-mcp/main.go`: plugin registration `if` block added
- [ ] `configs/retrievr-mcp.yaml`: source config block added
- [ ] `configs/retrievr-mcp.yaml`: source ID added to `router.default_sources`

### Tests

- [ ] `internal/rtv.plugin.mysource_test.go`: file created, `package internal`
- [ ] `TestMySourcePluginContract` calls `PluginContractTest(t, plugin)` and passes
- [ ] Search: normal query returns mapped `[]Publication`
- [ ] Search: empty query returns `ErrMySourceEmptyQuery`
- [ ] Search: HTTP 5xx returns error wrapping `ErrSearchFailed`
- [ ] Search: context cancellation returns error wrapping `ErrSearchFailed`
- [ ] Get: valid ID returns `*Publication`
- [ ] Get: not-found response returns error wrapping `ErrGetFailed` (or `ErrMySourceNotFound`)
- [ ] Get: HTTP 5xx returns error wrapping `ErrGetFailed`
- [ ] All tests pass with `-race`: `go test -race ./internal/...`

### Optional (credential support)

- [ ] `CallCredentials.MySourceAPIKey` field added in `rtv.types.go`
- [ ] `ResolveForSource` case added in `rtv.types.go`
- [ ] `sourcesAcceptingCredentials` entry added in `rtv.router.go`
- [ ] Plugin `Search` and `Get` call `creds.ResolveForSource(mySourcePluginID, p.apiKey)`
