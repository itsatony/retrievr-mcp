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
// npm package-registry provider — v5 cycle 4 / v2.11.0.
//
// API: GET https://registry.npmjs.org/-/v1/search?text=<q>&size=<n>&from=<n>
//   Params:
//     text         free-text query (Lucene-style; supports qualifiers like
//                  "keywords:cli author:doe" but free text is the 95% case)
//     size         1..250 (default 20)
//     from         0-indexed offset
//     quality      weight for quality score (default 0.65)
//     popularity   weight for popularity score (default 0.98)
//     maintenance  weight for maintenance score (default 0.5)
//
// Response (subset):
//   { "objects": [
//       { "package": { "name", "version", "description", "keywords": [...],
//                       "date": "...", "links": { "npm","homepage","repository" },
//                       "author": {"name"}, "publisher": {"username"} },
//         "score": { "final": float, "detail": {...} },
//         "searchScore": float } ],
//     "total": int }
//
// Free, no auth required.
//
// Residency: US (npm Inc., a subsidiary of GitHub / Microsoft, US-based).
// ---------------------------------------------------------------------------

const (
	npmPluginID          = SourceNPM
	npmPluginName        = "npm"
	npmPluginDescription = "Search the npm package registry (registry.npmjs.org). Free, no auth required. Returns name, version, description, keywords, repository URL, weekly-download-derived popularity score. Cross-registry dedup keyed on '<ecosystem>:<name>'."

	npmDefaultBaseURL = "https://registry.npmjs.org"
	npmSearchPath     = "/-/v1/search"
	npmDefaultLimit   = 20
	npmMaxLimitCap    = 250
	npmDefaultRPS     = 5.0
	npmDefaultTimeout = 10 * time.Second

	npmIDPrefix    = "npm:"
	npmEcosystemID = "npm"

	npmParamText = "text"
	npmParamSize = "size"
	npmParamFrom = "from"

	npmPackagePageURLTemplate = "https://www.npmjs.com/package/%s"

	npmCategoriesHint = "npm packages: filters.categories[*] map to free-text keywords filters via 'keywords:<kw>' tokens appended to text. Pass an explicit Lucene qualifier in the query (e.g. 'cli keywords:tool') for finer control."
)

// ---------------------------------------------------------------------------
// npm wire types
// ---------------------------------------------------------------------------

type npmSearchResponse struct {
	Objects []npmSearchObject `json:"objects"`
	Total   int               `json:"total"`
}

type npmSearchObject struct {
	Package     npmPackage `json:"package"`
	Score       npmScore   `json:"score"`
	SearchScore float64    `json:"searchScore"`
}

type npmPackage struct {
	Name        string    `json:"name,omitempty"`
	Version     string    `json:"version,omitempty"`
	Description string    `json:"description,omitempty"`
	Keywords    []string  `json:"keywords,omitempty"`
	Date        string    `json:"date,omitempty"`
	Links       npmLinks  `json:"links,omitempty"`
	Author      npmAuthor `json:"author,omitempty"`
	Publisher   npmAuthor `json:"publisher,omitempty"`
}

type npmLinks struct {
	NPM        string `json:"npm,omitempty"`
	Homepage   string `json:"homepage,omitempty"`
	Repository string `json:"repository,omitempty"`
}

type npmAuthor struct {
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
}

type npmScore struct {
	Final  float64       `json:"final,omitempty"`
	Detail npmScoreParts `json:"detail,omitempty"`
}

type npmScoreParts struct {
	Quality     float64 `json:"quality,omitempty"`
	Popularity  float64 `json:"popularity,omitempty"`
	Maintenance float64 `json:"maintenance,omitempty"`
}

// ---------------------------------------------------------------------------
// NPMPlugin
// ---------------------------------------------------------------------------

// NPMPlugin implements SourcePlugin for the npm public registry.
// Thread-safe after Initialize.
type NPMPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "npm".
func (p *NPMPlugin) ID() string { return npmPluginID }

// Name returns the human-readable label.
func (p *NPMPlugin) Name() string { return npmPluginName }

// Description returns the LLM-facing one-liner.
func (p *NPMPlugin) Description() string { return npmPluginDescription }

// ContentTypes — package.
func (p *NPMPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePackage} }

// NativeFormat — JSON.
func (p *NPMPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *NPMPlugin) AvailableFormats() []ContentFormat { return []ContentFormat{FormatJSON} }

// Capabilities reports npm's filter/sort surface.
func (p *NPMPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       npmMaxLimitCap,
		CategoriesHint:           npmCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentCodeProvenance, IntentQuickLookup},
		Kinds:                    []ResultKind{KindCode},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *NPMPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = npmDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = npmDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = npmDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *NPMPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes an npm /-/v1/search query.
func (p *NPMPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = npmDefaultLimit
	}
	if limit > npmMaxLimitCap {
		limit = npmMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Objects))
	for i := range resp.Objects {
		pubs = append(pubs, npmObjectToPublication(&resp.Objects[i]))
	}
	return &SearchResult{
		Total:   resp.Total,
		Results: pubs,
		HasMore: resp.Total > params.Offset+len(pubs),
	}, nil
}

// Get retrieves the latest version metadata for a package by name. The
// `id` is the raw package name (without prefix). Returns a Publication
// keyed identically to a search hit so callers can dedup.
func (p *NPMPlugin) Get(ctx context.Context, id string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	reqURL := p.baseURL + "/" + url.PathEscape(id) + "/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("npm: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("npm: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: npm package %s", ErrGetFailed, id)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("npm: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	// /latest returns a packument with the same shape as npmPackage
	// plus extra fields we ignore.
	var pkg npmPackage
	if err := json.NewDecoder(httpResp.Body).Decode(&pkg); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("npm: decode response: %w", err)
	}
	p.recordSuccess()
	obj := npmSearchObject{Package: pkg}
	pub := npmObjectToPublication(&obj)
	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *NPMPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*npmSearchResponse, error) {
	q := url.Values{}
	q.Set(npmParamText, npmBuildQuery(params))
	q.Set(npmParamSize, strconv.Itoa(limit))
	if params.Offset > 0 {
		q.Set(npmParamFrom, strconv.Itoa(params.Offset))
	}

	reqURL := p.baseURL + npmSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("npm: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("npm: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: npm", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("npm: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp npmSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("npm: decode response: %w", err)
	}
	return &resp, nil
}

// npmBuildQuery folds free-text query plus filters.categories into the
// npm text parameter using "keywords:" qualifiers per the registry's
// Lucene-style mini-syntax.
func npmBuildQuery(params SearchParams) string {
	parts := []string{}
	if q := strings.TrimSpace(params.Query); q != "" {
		parts = append(parts, q)
	}
	for _, kw := range params.Filters.Categories {
		if k := strings.TrimSpace(kw); k != "" {
			parts = append(parts, "keywords:"+k)
		}
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func npmObjectToPublication(obj *npmSearchObject) Publication {
	pkg := obj.Package
	name := strings.TrimSpace(pkg.Name)

	displayURL := pkg.Links.NPM
	if displayURL == "" {
		displayURL = fmt.Sprintf(npmPackagePageURLTemplate, name)
	}

	authors := []Author{}
	if pkg.Author.Name != "" {
		authors = append(authors, Author{Name: pkg.Author.Name})
	} else if pkg.Publisher.Username != "" {
		authors = append(authors, Author{Name: pkg.Publisher.Username})
	}

	published := pkg.Date
	if len(published) >= 10 {
		published = published[:10]
	}

	meta := map[string]any{
		MetaKeyPackageID:      npmEcosystemID + ":" + name,
		smetaPackageEcosystem: npmEcosystemID,
		smetaPackageName:      name,
	}
	if pkg.Version != "" {
		meta[smetaPackageVersion] = pkg.Version
	}
	if pkg.Links.Repository != "" {
		meta[smetaPackageRepoURL] = pkg.Links.Repository
	}
	if pkg.Links.Homepage != "" {
		meta[smetaPackageHomeURL] = pkg.Links.Homepage
	}
	if len(pkg.Keywords) > 0 {
		meta[smetaPackageKeywords] = pkg.Keywords
	}
	if obj.Score.Final != 0 {
		meta[smetaPackageScore] = obj.Score.Final
	}

	return Publication{
		ID:             npmIDPrefix + name,
		Source:         SourceNPM,
		ContentType:    ContentTypePackage,
		Title:          name,
		Abstract:       pkg.Description,
		URL:            displayURL,
		Published:      published,
		Authors:        authors,
		Categories:     pkg.Keywords,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *NPMPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *NPMPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
