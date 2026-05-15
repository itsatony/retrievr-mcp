package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Firecrawl web search + extraction provider — Cycle 2 Wave-1.
//
// API: https://docs.firecrawl.dev/api-reference/endpoint/search
//   POST https://api.firecrawl.dev/v1/search
//   Headers: Authorization: Bearer <key>, content-type: application/json
//   Body: { query, limit, scrapeOptions: { formats: ["markdown"] } }
//   Response: { success, data: [ { url, title, description, markdown, metadata } ] }
//
// Cycle 2 ships search mode (mode "a" per plan §5.1.3). The enrichment
// hook (mode "b" — per-URL scrape via /v1/scrape for thin web snippets)
// is reserved for cycle 3; the toggle (`enrichment.firecrawl.enabled`)
// is wired in config (task #8) but the post-merge call site lands later.
//
// Residency: US (Firecrawl is a US company). Blocked under eu_strict.
// ---------------------------------------------------------------------------

// Identity / config constants.
const (
	firecrawlPluginID          = SourceFirecrawl
	firecrawlPluginName        = "Firecrawl"
	firecrawlPluginDescription = "Web search + per-URL markdown extraction. Pairs with rtv_get for primary-source bodies. US-resident; blocked under eu_strict."

	firecrawlDefaultBaseURL  = "https://api.firecrawl.dev"
	firecrawlSearchPath      = "/v1/search"
	firecrawlScrapePath      = "/v1/scrape"
	firecrawlAuthHeader      = "Authorization"
	firecrawlAuthScheme      = "Bearer "
	firecrawlContentTypeJSON = "application/json"

	firecrawlDefaultLimit = 10
	firecrawlMaxLimit     = 50
	firecrawlDefaultRPS   = 2.0

	firecrawlCategoriesHint = "general web with optional markdown extraction; thin snippets enriched via /v1/scrape (cycle-3)"
)

// Extra-key constants (PluginConfig.Extra).
const (
	firecrawlExtraIncludeMarkdown = "include_markdown" // "true" pulls per-result markdown
)

// ---------------------------------------------------------------------------
// Firecrawl wire types
// ---------------------------------------------------------------------------

type firecrawlSearchRequest struct {
	Query         string                  `json:"query"`
	Limit         int                     `json:"limit,omitempty"`
	ScrapeOptions *firecrawlScrapeOptions `json:"scrapeOptions,omitempty"`
}

type firecrawlScrapeOptions struct {
	Formats []string `json:"formats,omitempty"` // markdown | html | rawHtml | links
}

type firecrawlSearchResponse struct {
	Success bool              `json:"success"`
	Data    []firecrawlResult `json:"data"`
	Warning string            `json:"warning,omitempty"`
	Error   string            `json:"error,omitempty"`
}

type firecrawlResult struct {
	URL         string         `json:"url"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Markdown    string         `json:"markdown,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ---------------------------------------------------------------------------
// FirecrawlPlugin
// ---------------------------------------------------------------------------

// FirecrawlPlugin implements SourcePlugin for Firecrawl web search.
// Thread-safe for concurrent use after Initialize.
type FirecrawlPlugin struct {
	baseURL         string
	apiKey          string
	includeMarkdown bool
	httpClient      *http.Client
	enabled         bool
	rateLimit       float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "firecrawl".
func (p *FirecrawlPlugin) ID() string { return firecrawlPluginID }

// Name returns the human-readable label.
func (p *FirecrawlPlugin) Name() string { return firecrawlPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *FirecrawlPlugin) Description() string { return firecrawlPluginDescription }

// ContentTypes — Firecrawl covers general web; mapped to ContentTypeAny.
func (p *FirecrawlPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeAny}
}

// NativeFormat — JSON.
func (p *FirecrawlPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON + markdown (when scrapeOptions.formats=markdown).
func (p *FirecrawlPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatMarkdown}
}

// Capabilities reports Firecrawl's filtering + sorting support.
func (p *FirecrawlPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         true, // via scrapeOptions.formats=markdown
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       false,
		MaxResultsPerQuery:       firecrawlMaxLimit,
		CategoriesHint:           firecrawlCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatMarkdown},
		QueryIntents:             []Intent{IntentDeepResearch, IntentQuickLookup},
		Kinds:                    []ResultKind{KindWeb},
		RequiresCredential:       true,
	}
}

// Residency — US-resident.
func (*FirecrawlPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *FirecrawlPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = firecrawlDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = firecrawlDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.includeMarkdown = strings.EqualFold(stringFromExtra(cfg.Extra, firecrawlExtraIncludeMarkdown, "false"), "true")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *FirecrawlPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Firecrawl /v1/search call. Credentials from ctx.
func (p *FirecrawlPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, firecrawlPluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: firecrawl requires an API key", ErrCredentialRequired)
	}

	limit := params.Limit
	if limit <= 0 {
		limit = firecrawlDefaultLimit
	}
	if limit > firecrawlMaxLimit {
		limit = firecrawlMaxLimit
	}

	body := firecrawlSearchRequest{Query: params.Query, Limit: limit}
	if p.includeMarkdown {
		body.ScrapeOptions = &firecrawlScrapeOptions{Formats: []string{"markdown"}}
	}

	resp, err := p.doSearch(ctx, body, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	if !resp.Success && resp.Error != "" {
		return nil, fmt.Errorf("firecrawl: api error: %s", resp.Error)
	}

	pubs := make([]Publication, 0, len(resp.Data))
	for _, r := range resp.Data {
		pubs = append(pubs, firecrawlResultToPublication(r))
	}

	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get fetches a single URL via /v1/scrape and returns it as a Publication.
// The prefixedID is the URL itself (Firecrawl doesn't have stable
// non-URL IDs); callers pass it as "firecrawl:https://..." per the
// Router.Get contract, and the URL has already been hex-decoded by
// ParsePrefixedID — but firecrawl's hashURL ID format means we round-trip
// with the URL recovered from raw input is not feasible. Cycle-2 keeps
// Get unimplemented; the proper path is the enrichment hook that takes a
// URL directly. Reserve for cycle 3.
func (p *FirecrawlPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: firecrawl Get reserved for cycle-3 enrichment hook", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *FirecrawlPlugin) doSearch(ctx context.Context, body firecrawlSearchRequest, apiKey string) (*firecrawlSearchResponse, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("firecrawl: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+firecrawlSearchPath, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("firecrawl: build request: %w", err)
	}
	req.Header.Set(firecrawlAuthHeader, firecrawlAuthScheme+apiKey)
	req.Header.Set("Content-Type", firecrawlContentTypeJSON)
	req.Header.Set("Accept", firecrawlContentTypeJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("firecrawl: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: firecrawl returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: firecrawl", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("firecrawl: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp firecrawlSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("firecrawl: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Result mapping
// ---------------------------------------------------------------------------

// firecrawlResultToPublication maps one Firecrawl result into a Publication.
// Snippet derives from `description`; the full markdown body (when present)
// goes into Abstract so consumers can choose snippet vs full extract.
func firecrawlResultToPublication(r firecrawlResult) Publication {
	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceFirecrawl, hashURL(r.URL)),
		Source:      SourceFirecrawl,
		ContentType: ContentTypeAny,
		Title:       r.Title,
		URL:         r.URL,
	}

	switch {
	case r.Markdown != "":
		pub.Abstract = r.Markdown
	case r.Description != "":
		pub.Abstract = r.Description
	}

	meta := map[string]any{}
	if r.Description != "" {
		meta[smetaSnippet] = truncateSnippet(r.Description)
	} else if r.Markdown != "" {
		meta[smetaSnippet] = truncateSnippet(r.Markdown)
	}
	if r.Metadata != nil {
		if lang, ok := r.Metadata["language"].(string); ok && lang != "" {
			meta[smetaLanguage] = lang
		}
		if title, ok := r.Metadata["title"].(string); ok && pub.Title == "" {
			pub.Title = title
		}
		if pubTime, ok := r.Metadata["publishedTime"].(string); ok && pubTime != "" {
			meta[smetaPublishedAt] = pubTime
		}
	}
	if len(meta) > 0 {
		pub.SourceMetadata = meta
	}
	return pub
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *FirecrawlPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *FirecrawlPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = sanitizeHealthError(err)
	}
}
