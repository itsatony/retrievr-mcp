package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// GitHub Code Search provider — Cycle 2 Wave-1.
//
// API: https://docs.github.com/en/rest/search/search#search-repositories
//   GET https://api.github.com/search/repositories
//   Headers: Authorization: Bearer <PAT>, Accept: application/vnd.github+json
//   Query: q=<query> [+language:go] [+stars:>100] ...
//   Response: { total_count, incomplete_results, items: [{full_name, html_url, ...}] }
//
// Cycle 2 ships repository search (the practical "find code about X"
// pattern). /search/code is reserved for cycle 3 — it requires explicit
// org:/repo:/path: scoping that doesn't fit a generic search-by-keyword
// plugin shape.
//
// Residency: US (Microsoft/GitHub). Blocked under eu_strict.
// ---------------------------------------------------------------------------

// Identity / config constants.
const (
	githubPluginID          = SourceGitHub
	githubPluginName        = "GitHub Code Search"
	githubPluginDescription = "Public repository search ranked by stars/relevance. Maps to IntentCodeProvenance. US-resident; blocked under eu_strict."

	githubDefaultBaseURL   = "https://api.github.com"
	githubReposSearchPath  = "/search/repositories"
	githubAuthHeader       = "Authorization"
	githubAuthScheme       = "Bearer "
	githubAcceptHeader     = "Accept"
	githubAcceptValue      = "application/vnd.github+json"
	githubAPIVersionHeader = "X-GitHub-Api-Version"
	githubAPIVersionValue  = "2022-11-28"

	githubDefaultLimit = 10
	githubMaxLimit     = 100
	githubDefaultRPS   = 0.5 // ~30 req/min PAT-authenticated search ceiling

	githubCategoriesHint = "public repositories ranked by stars; cycle-3 adds /search/code with org:/repo: scoping"
)

// Extra-key constants.
const (
	githubExtraSort  = "sort"  // stars | forks | help-wanted-issues | updated
	githubExtraOrder = "order" // desc | asc
)

// ---------------------------------------------------------------------------
// GitHub wire types
// ---------------------------------------------------------------------------

type githubReposResponse struct {
	TotalCount        int              `json:"total_count"`
	IncompleteResults bool             `json:"incomplete_results"`
	Items             []githubRepoItem `json:"items"`
	Message           string           `json:"message,omitempty"` // populated on errors
}

type githubRepoItem struct {
	ID              int64          `json:"id"`
	NodeID          string         `json:"node_id"`
	Name            string         `json:"name"`
	FullName        string         `json:"full_name"`
	HTMLURL         string         `json:"html_url"`
	Description     string         `json:"description,omitempty"`
	Language        string         `json:"language,omitempty"`
	StargazersCount int            `json:"stargazers_count"`
	ForksCount      int            `json:"forks_count"`
	Topics          []string       `json:"topics,omitempty"`
	License         *githubLicense `json:"license,omitempty"`
	DefaultBranch   string         `json:"default_branch,omitempty"`
	PushedAt        string         `json:"pushed_at,omitempty"`
	Owner           *githubOwner   `json:"owner,omitempty"`
}

type githubLicense struct {
	Key    string `json:"key"`
	SPDXID string `json:"spdx_id"`
	Name   string `json:"name"`
}

type githubOwner struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// ---------------------------------------------------------------------------
// GitHubPlugin
// ---------------------------------------------------------------------------

// GitHubPlugin implements SourcePlugin for GitHub repository search.
// Thread-safe for concurrent use after Initialize.
type GitHubPlugin struct {
	baseURL    string
	apiKey     string
	sort       string
	order      string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID / Name / Description.
func (p *GitHubPlugin) ID() string          { return githubPluginID }
func (p *GitHubPlugin) Name() string        { return githubPluginName }
func (p *GitHubPlugin) Description() string { return githubPluginDescription }

// ContentTypes — code maps to ContentTypeAny in this codebase (no
// dedicated ContentTypeCode). Capabilities.Kinds carries the actual signal.
func (p *GitHubPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypeAny} }

// NativeFormat / AvailableFormats.
func (p *GitHubPlugin) NativeFormat() ContentFormat { return FormatJSON }
func (p *GitHubPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatMarkdown}
}

// Capabilities reports GitHub's filtering + sorting support.
func (p *GitHubPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     true, // user: qualifier in query
		SupportsCategoryFilter:   true, // language: qualifier
		SupportsSortRelevance:    true,
		SupportsSortDate:         true, // sort=updated
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       githubMaxLimit,
		CategoriesHint:           githubCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatMarkdown},
		QueryIntents:             []Intent{IntentCodeProvenance},
		Kinds:                    []ResultKind{KindCode},
	}
}

// Residency — US.
func (*GitHubPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *GitHubPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = githubDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = githubDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.sort = stringFromExtra(cfg.Extra, githubExtraSort, "stars")
	p.order = stringFromExtra(cfg.Extra, githubExtraOrder, "desc")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *GitHubPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a GitHub repository search.
func (p *GitHubPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, githubPluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: github requires a personal access token", ErrCredentialRequired)
	}

	limit := params.Limit
	if limit <= 0 {
		limit = githubDefaultLimit
	}
	if limit > githubMaxLimit {
		limit = githubMaxLimit
	}

	resp, err := p.doSearch(ctx, params.Query, limit, params.Offset, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	if resp.Message != "" {
		// GitHub returns a 200 with an error message in some cases (e.g.
		// validation issues on /search/code). Surface those as plugin errors.
		return nil, fmt.Errorf("github: api error: %s", resp.Message)
	}

	pubs := make([]Publication, 0, len(resp.Items))
	for _, r := range resp.Items {
		pubs = append(pubs, githubRepoToPublication(r))
	}

	return &SearchResult{
		Total:   resp.TotalCount,
		Results: pubs,
		HasMore: resp.TotalCount > len(pubs),
	}, nil
}

// Get fetches a single repository by full_name (e.g. "owner/repo").
// Cycle-2 minimum-viable path; the prefixed ID format is "github:<full_name>".
func (p *GitHubPlugin) Get(ctx context.Context, fullName string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	apiKey := CredentialFor(ctx, githubPluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: github requires a personal access token", ErrCredentialRequired)
	}
	if fullName == "" || !strings.Contains(fullName, "/") {
		return nil, fmt.Errorf("%w: github Get id must be 'owner/repo'", ErrInvalidID)
	}

	reqURL := p.baseURL + "/repos/" + fullName
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set(githubAuthHeader, githubAuthScheme+apiKey)
	req.Header.Set(githubAcceptHeader, githubAcceptValue)
	req.Header.Set(githubAPIVersionHeader, githubAPIVersionValue)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: github returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	}
	if httpResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrSourceNotFound, fullName)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("github: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var item githubRepoItem
	if err := json.NewDecoder(httpResp.Body).Decode(&item); err != nil {
		return nil, fmt.Errorf("github: decode: %w", err)
	}
	pub := githubRepoToPublication(item)
	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *GitHubPlugin) doSearch(ctx context.Context, query string, perPage, page int, apiKey string) (*githubReposResponse, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("per_page", fmt.Sprintf("%d", perPage))
	if page > 0 {
		q.Set("page", fmt.Sprintf("%d", page))
	}
	if p.sort != "" {
		q.Set("sort", p.sort)
	}
	if p.order != "" {
		q.Set("order", p.order)
	}

	reqURL := p.baseURL + githubReposSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set(githubAuthHeader, githubAuthScheme+apiKey)
	req.Header.Set(githubAcceptHeader, githubAcceptValue)
	req.Header.Set(githubAPIVersionHeader, githubAPIVersionValue)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: github returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: github", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("github: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp githubReposResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("github: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Result mapping
// ---------------------------------------------------------------------------

// githubRepoToPublication maps one GitHub repo into a Publication, stuffing
// code-specific data into SourceMetadata for the converter to unpack into
// Result.Code.
func githubRepoToPublication(r githubRepoItem) Publication {
	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceGitHub, r.FullName),
		Source:      SourceGitHub,
		ContentType: ContentTypeAny,
		Title:       r.FullName,
		URL:         r.HTMLURL,
		Abstract:    r.Description,
	}
	if r.Owner != nil && r.Owner.Login != "" {
		pub.Authors = []Author{{Name: r.Owner.Login}}
	}
	if r.PushedAt != "" {
		// Strip time component for the canonical YYYY-MM-DD shape.
		if len(r.PushedAt) >= 10 {
			pub.Updated = r.PushedAt[:10]
		} else {
			pub.Updated = r.PushedAt
		}
	}
	if r.License != nil && r.License.SPDXID != "" {
		pub.License = r.License.SPDXID
	}

	meta := map[string]any{
		smetaKindOverride: string(KindCode),
		smetaRepo:         r.FullName,
		smetaStars:        r.StargazersCount,
		smetaForks:        r.ForksCount,
	}
	if r.Description != "" {
		meta[smetaSnippet] = truncateSnippet(r.Description)
	}
	if r.Language != "" {
		meta[smetaCodeLang] = r.Language
		// Surface as Result.Language too — useful for filtering at the merge layer.
		meta[smetaLanguage] = r.Language
	}
	if len(r.Topics) > 0 {
		meta[smetaTopics] = r.Topics
	}
	if r.PushedAt != "" {
		meta[smetaLastCommit] = r.PushedAt
	}
	pub.SourceMetadata = meta
	return pub
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *GitHubPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *GitHubPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
