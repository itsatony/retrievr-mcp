package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// crates.io package-registry provider — v5 cycle 4 / v2.11.0.
//
// API: GET https://crates.io/api/v1/crates?q=<q>&page=<n>&per_page=<n>
//   Params:
//     q             free-text query
//     page          1-indexed page number
//     per_page      1..100 (default 10)
//     sort          "relevance" | "downloads" | "recent-downloads" |
//                   "new" | "recent-updates"
//     category      slug filter (e.g. "asynchronous", "web-programming")
//
// Response:
//   { "crates": [ { "id","name","max_version","description","homepage",
//                   "repository","documentation","downloads",
//                   "recent_downloads","categories":[...],"keywords":[...],
//                   "created_at","updated_at" } ],
//     "meta": { "total": int } }
//
// Free, public. Honor Rust Foundation crawler policy:
//   - Custom User-Agent including a contact email is required
//   - Rate-limit to ~1 req/s
//
// Residency: US (Rust Foundation, US-based non-profit).
// ---------------------------------------------------------------------------

const (
	cratesPluginID          = SourceCrates
	cratesPluginName        = "crates.io"
	cratesPluginDescription = "Search the crates.io Rust package registry. Free, public. Returns name, version, description, repository, downloads, categories, keywords. Rust Foundation policy mandates a contact User-Agent and ~1 req/s rate limit. Cross-registry dedup keyed on '<ecosystem>:<name>'."

	cratesDefaultBaseURL = "https://crates.io"
	cratesSearchPath     = "/api/v1/crates"
	cratesDefaultLimit   = 10
	cratesMaxLimitCap    = 100
	cratesDefaultRPS     = 1.0
	cratesDefaultTimeout = 15 * time.Second

	cratesIDPrefix        = "crates:"
	cratesEcosystemID     = "crates"
	cratesCratePageURLFmt = "https://crates.io/crates/%s"

	cratesParamQ        = "q"
	cratesParamPage     = "page"
	cratesParamPerPage  = "per_page"
	cratesParamSort     = "sort"
	cratesParamCategory = "category"

	cratesSortRelevance     = "relevance"
	cratesSortRecentUpdates = "recent-updates"

	cratesCategoriesHint = "crates.io category slugs (lowercase, hyphenated): asynchronous, web-programming, command-line-utilities, parser-implementations, etc. Pass via filters.categories[0] — a single category filter per request."

	cratesExtraUserAgent   = "user_agent"
	cratesDefaultUserAgent = "retrievr-mcp/2.11 (+https://github.com/itsatony/retrievr-mcp)"
	cratesMetaKeyDocsURL   = "crates_docs_url"
)

// ---------------------------------------------------------------------------
// crates.io wire types
// ---------------------------------------------------------------------------

type cratesSearchResponse struct {
	Crates []cratesCrate `json:"crates"`
	Meta   cratesMeta    `json:"meta"`
}

type cratesMeta struct {
	Total int `json:"total"`
}

type cratesCrate struct {
	ID            string   `json:"id,omitempty"`
	Name          string   `json:"name,omitempty"`
	MaxVersion    string   `json:"max_version,omitempty"`
	NewestVersion string   `json:"newest_version,omitempty"`
	Description   string   `json:"description,omitempty"`
	Homepage      string   `json:"homepage,omitempty"`
	Repository    string   `json:"repository,omitempty"`
	Documentation string   `json:"documentation,omitempty"`
	Downloads     int64    `json:"downloads,omitempty"`
	RecentDls     int64    `json:"recent_downloads,omitempty"`
	Categories    []string `json:"categories,omitempty"`
	Keywords      []string `json:"keywords,omitempty"`
	CreatedAt     string   `json:"created_at,omitempty"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
}

// ---------------------------------------------------------------------------
// CratesPlugin
// ---------------------------------------------------------------------------

// CratesPlugin implements SourcePlugin for the crates.io registry.
// Thread-safe after Initialize.
type CratesPlugin struct {
	baseURL    string
	userAgent  string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "crates".
func (p *CratesPlugin) ID() string { return cratesPluginID }

// Name returns the human-readable label.
func (p *CratesPlugin) Name() string { return cratesPluginName }

// Description returns the LLM-facing one-liner.
func (p *CratesPlugin) Description() string { return cratesPluginDescription }

// ContentTypes — package.
func (p *CratesPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePackage} }

// NativeFormat — JSON.
func (p *CratesPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *CratesPlugin) AvailableFormats() []ContentFormat { return []ContentFormat{FormatJSON} }

// Capabilities reports crates.io's filter/sort surface.
func (p *CratesPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true, // mapped to "new" / "recent-updates"
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       cratesMaxLimitCap,
		CategoriesHint:           cratesCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentCodeProvenance, IntentQuickLookup},
		Kinds:                    []ResultKind{KindCode},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *CratesPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = cratesDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = cratesDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	if cfg.Extra != nil {
		p.userAgent = strings.TrimSpace(cfg.Extra[cratesExtraUserAgent])
	}
	if p.userAgent == "" {
		p.userAgent = cratesDefaultUserAgent
	}

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = cratesDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *CratesPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a crates.io /api/v1/crates query.
func (p *CratesPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = cratesDefaultLimit
	}
	if limit > cratesMaxLimitCap {
		limit = cratesMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Crates))
	for i := range resp.Crates {
		pubs = append(pubs, cratesCrateToPublication(&resp.Crates[i]))
	}
	return &SearchResult{
		Total:   resp.Meta.Total,
		Results: pubs,
		HasMore: resp.Meta.Total > params.Offset+len(pubs),
	}, nil
}

// Get retrieves a single crate by name.
func (p *CratesPlugin) Get(ctx context.Context, id string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	reqURL := p.baseURL + cratesSearchPath + "/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("crates: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("crates: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: crates crate %s", ErrGetFailed, id)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("crates: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	// /api/v1/crates/<name> wraps a single crate under "crate".
	var env struct {
		Crate cratesCrate `json:"crate"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&env); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("crates: decode response: %w", err)
	}
	p.recordSuccess()
	pub := cratesCrateToPublication(&env.Crate)
	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *CratesPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*cratesSearchResponse, error) {
	q := url.Values{}
	q.Set(cratesParamQ, params.Query)
	q.Set(cratesParamPerPage, strconv.Itoa(limit))

	page := 1
	if params.Offset > 0 && limit > 0 && params.Offset%limit == 0 {
		page = params.Offset/limit + 1
	}
	q.Set(cratesParamPage, strconv.Itoa(page))

	switch params.Sort {
	case SortDateDesc:
		q.Set(cratesParamSort, cratesSortRecentUpdates)
	case SortDateAsc:
		// crates.io has no ascending-date sort; fall through to relevance.
		q.Set(cratesParamSort, cratesSortRelevance)
	case SortRelevance:
		q.Set(cratesParamSort, cratesSortRelevance)
	}

	if len(params.Filters.Categories) > 0 {
		if cat := strings.TrimSpace(params.Filters.Categories[0]); cat != "" {
			q.Set(cratesParamCategory, strings.ToLower(cat))
		}
	}

	reqURL := p.baseURL + cratesSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("crates: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("crates: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: crates", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("crates: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp cratesSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("crates: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func cratesCrateToPublication(c *cratesCrate) Publication {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = c.ID
	}

	published := c.UpdatedAt
	if len(published) >= 10 {
		published = published[:10]
	}

	version := c.MaxVersion
	if version == "" {
		version = c.NewestVersion
	}

	meta := map[string]any{
		MetaKeyPackageID:      cratesEcosystemID + ":" + name,
		smetaPackageEcosystem: cratesEcosystemID,
		smetaPackageName:      name,
	}
	if version != "" {
		meta[smetaPackageVersion] = version
	}
	if c.Repository != "" {
		meta[smetaPackageRepoURL] = c.Repository
	}
	if c.Homepage != "" {
		meta[smetaPackageHomeURL] = c.Homepage
	}
	if c.Documentation != "" {
		meta[cratesMetaKeyDocsURL] = c.Documentation
	}
	if c.Downloads != 0 {
		meta[smetaPackageDownloads] = c.Downloads
	}
	if len(c.Keywords) > 0 {
		meta[smetaPackageKeywords] = c.Keywords
	}

	categories := append([]string{}, c.Categories...)
	categories = append(categories, c.Keywords...)

	return Publication{
		ID:             cratesIDPrefix + name,
		Source:         SourceCrates,
		ContentType:    ContentTypePackage,
		Title:          name,
		Abstract:       c.Description,
		URL:            fmt.Sprintf(cratesCratePageURLFmt, name),
		Published:      published,
		Categories:     categories,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *CratesPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *CratesPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
