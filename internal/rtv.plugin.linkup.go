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
// Linkup web search provider — Cycle 2 Wave-1.
//
// API: https://docs.linkup.so/pages/documentation/api-reference/search
//   POST https://api.linkup.so/v1/search
//   Headers: Authorization: Bearer <key>, content-type: application/json
//   Body: { q, depth, outputType, includeImages }
//   Response (outputType=searchResults):
//     { results: [ { type, name, url, content } ] }
//
// Residency: **EU** (Linkup SAS, France) with a signed DPA. Primary admit
// in eu_strict mode and the headline EU-resident web provider. This is the
// only Wave-1 provider not blocked by the eu_strict gate.
// ---------------------------------------------------------------------------

// Identity / config constants.
const (
	linkupPluginID          = SourceLinkup
	linkupPluginName        = "Linkup"
	linkupPluginDescription = "EU-resident web search (Linkup SAS, France) with signed DPA. Primary admit under eu_strict mode."

	linkupDefaultBaseURL  = "https://api.linkup.so"
	linkupSearchPath      = "/v1/search"
	linkupAuthHeader      = "Authorization"
	linkupAuthScheme      = "Bearer "
	linkupContentTypeJSON = "application/json"

	linkupDefaultDepth      = "standard" // standard | deep
	linkupDefaultOutputType = "searchResults"
	linkupDefaultRPS        = 5.0

	linkupCategoriesHint = "general web (EU-resident); preferred under eu_strict mode"

	linkupSubprocessorURL = "https://www.linkup.so/legal/dpa"
)

// Extra-key constants (PluginConfig.Extra).
const (
	linkupExtraDepth          = "depth"           // standard | deep
	linkupExtraOutputType     = "output_type"     // searchResults (cycle 2 only)
	linkupExtraIncludeImages  = "include_images"  // "true" | "false"
	linkupExtraIncludeDomains = "include_domains" // comma-sep
)

// ---------------------------------------------------------------------------
// Linkup wire types
// ---------------------------------------------------------------------------

type linkupSearchRequest struct {
	Q              string   `json:"q"`
	Depth          string   `json:"depth,omitempty"`
	OutputType     string   `json:"outputType,omitempty"`
	IncludeImages  bool     `json:"includeImages,omitempty"`
	IncludeDomains []string `json:"includeDomains,omitempty"`
	ExcludeDomains []string `json:"excludeDomains,omitempty"`
}

type linkupSearchResponse struct {
	Results []linkupResult `json:"results,omitempty"`
}

type linkupResult struct {
	Type    string `json:"type"`              // "text" | "image" — we only consume "text"
	Name    string `json:"name"`              // title
	URL     string `json:"url"`
	Content string `json:"content,omitempty"` // snippet / extract
	Snippet string `json:"snippet,omitempty"` // legacy field name
}

// ---------------------------------------------------------------------------
// LinkupPlugin
// ---------------------------------------------------------------------------

// LinkupPlugin implements SourcePlugin for Linkup web search.
// Thread-safe for concurrent use after Initialize.
type LinkupPlugin struct {
	baseURL        string
	apiKey         string
	depth          string
	outputType     string
	includeImages  bool
	includeDomains []string
	httpClient     *http.Client
	enabled        bool
	rateLimit      float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "linkup".
func (p *LinkupPlugin) ID() string { return linkupPluginID }

// Name returns the human-readable label.
func (p *LinkupPlugin) Name() string { return linkupPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *LinkupPlugin) Description() string { return linkupPluginDescription }

// ContentTypes — Linkup covers general web; mapped to ContentTypeAny.
func (p *LinkupPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeAny}
}

// NativeFormat — JSON.
func (p *LinkupPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON + markdown.
func (p *LinkupPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatMarkdown}
}

// Capabilities reports Linkup's filtering + sorting support.
func (p *LinkupPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         true, // Linkup returns content per result
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       false,
		MaxResultsPerQuery:       20,
		CategoriesHint:           linkupCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatMarkdown},
		QueryIntents:             []Intent{IntentQuickLookup, IntentDeepResearch},
		Kinds:                    []ResultKind{KindWeb},
	}
}

// Residency — **EU-resident** (Linkup SAS, France) with signed DPA. The
// only Wave-1 provider admitted under eu_strict.
func (*LinkupPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:          RegionEU,
		DPAStatus:       DPASigned,
		SubprocessorURL: linkupSubprocessorURL,
		LastVerifiedAt:  residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *LinkupPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = linkupDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = linkupDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.depth = stringFromExtra(cfg.Extra, linkupExtraDepth, linkupDefaultDepth)
	p.outputType = stringFromExtra(cfg.Extra, linkupExtraOutputType, linkupDefaultOutputType)
	p.includeImages = strings.EqualFold(stringFromExtra(cfg.Extra, linkupExtraIncludeImages, "false"), "true")

	if raw := stringFromExtra(cfg.Extra, linkupExtraIncludeDomains, ""); raw != "" {
		parts := strings.Split(raw, ",")
		for _, v := range parts {
			if v = strings.TrimSpace(v); v != "" {
				p.includeDomains = append(p.includeDomains, v)
			}
		}
	}

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *LinkupPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Linkup search. Credentials read from ctx.
func (p *LinkupPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, linkupPluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: linkup requires an API key", ErrCredentialRequired)
	}

	body := linkupSearchRequest{
		Q:              params.Query,
		Depth:          p.depth,
		OutputType:     p.outputType,
		IncludeImages:  p.includeImages,
		IncludeDomains: p.includeDomains,
	}

	resp, err := p.doSearch(ctx, body, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.Type != "text" {
			continue // skip image/binary results
		}
		pubs = append(pubs, linkupResultToPublication(r))
	}

	limit := params.Limit
	if limit > 0 && len(pubs) > limit {
		pubs = pubs[:limit]
	}

	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not supported — Linkup has no per-result-ID retrieval API.
func (p *LinkupPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: linkup has no per-result Get API", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *LinkupPlugin) doSearch(ctx context.Context, body linkupSearchRequest, apiKey string) (*linkupSearchResponse, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("linkup: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+linkupSearchPath, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("linkup: build request: %w", err)
	}
	req.Header.Set(linkupAuthHeader, linkupAuthScheme+apiKey)
	req.Header.Set("Content-Type", linkupContentTypeJSON)
	req.Header.Set("Accept", linkupContentTypeJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linkup: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: linkup returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: linkup", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("linkup: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp linkupSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("linkup: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Result mapping
// ---------------------------------------------------------------------------

// linkupResultToPublication maps one Linkup result into a Publication. Snippet
// derived from `content` (preferred) or `snippet` (legacy field name).
func linkupResultToPublication(r linkupResult) Publication {
	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceLinkup, hashURL(r.URL)),
		Source:      SourceLinkup,
		ContentType: ContentTypeAny,
		Title:       r.Name,
		URL:         r.URL,
	}

	body := r.Content
	if body == "" {
		body = r.Snippet
	}
	if body != "" {
		pub.Abstract = body
	}

	meta := map[string]any{}
	if body != "" {
		meta[smetaSnippet] = truncateSnippet(body)
	}
	if len(meta) > 0 {
		pub.SourceMetadata = meta
	}
	return pub
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *LinkupPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *LinkupPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
