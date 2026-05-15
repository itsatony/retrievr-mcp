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
	"time"
)

// ---------------------------------------------------------------------------
// Exa.ai web/news search plugin — Cycle 2 Wave-1.
//
// API: https://docs.exa.ai/reference/search
//   POST https://api.exa.ai/search
//   Headers: x-api-key: <key>, content-type: application/json
//   Body: { query, numResults, type, category?, contents?, ... }
//   Response: { requestId, results: [ {id, title, url, publishedDate, author, score, text} ] }
//
// Residency: US (Exa is a US company; index is global). Blocked under
// eu_strict; admissible in eu_preferred and off.
//
// Capabilities.Kinds = [KindWeb, KindNews] — the converter picks KindWeb
// as the default; per-result kind override via SourceMetadata["kind"].
// ---------------------------------------------------------------------------

// Identity / config constants.
const (
	exaPluginID          = SourceExa
	exaPluginName        = "Exa.ai"
	exaPluginDescription = "Neural + keyword web search with snippet extraction. Strong on technical / research-adjacent web content (US-resident; blocked under eu_strict)."

	exaDefaultBaseURL  = "https://api.exa.ai"
	exaSearchPath      = "/search"
	exaContentsPath    = "/contents"
	exaAuthHeader      = "x-api-key"
	exaContentTypeJSON = "application/json"

	exaDefaultNumResults = 10
	exaMaxNumResults     = 100

	// Default search "type" — "auto" lets Exa pick neural vs keyword per
	// query. Plugin extras can override via PluginConfig.Extra["default_type"].
	exaDefaultSearchType = "auto"
	exaTypeNeural        = "neural"
	exaTypeKeyword       = "keyword"
	exaTypeAuto          = "auto"

	// Default Exa rate-limit budget. Exa's published soft cap is 5 req/s
	// on the free tier, 10 req/s on Pro. We default to 5 to stay polite;
	// operators bump via sources.exa.rate_limit.
	exaDefaultRPS = 5.0

	exaPluginCategoriesHint = "general web, news, blogs, technical writing, papers (via includeDomains arxiv/etc.)"
)

// Extra-key constants (PluginConfig.Extra).
const (
	exaExtraDefaultType = "default_type" // "auto" | "neural" | "keyword"
	exaExtraIncludeText = "include_text" // "true" to fetch contents.text inline
	exaExtraCategory    = "default_category"
)

// ---------------------------------------------------------------------------
// Exa wire types
// ---------------------------------------------------------------------------

type exaSearchRequest struct {
	Query          string       `json:"query"`
	NumResults     int          `json:"numResults,omitempty"`
	Type           string       `json:"type,omitempty"`
	Category       string       `json:"category,omitempty"`
	Contents       *exaContents `json:"contents,omitempty"`
	StartDate      string       `json:"startPublishedDate,omitempty"`
	EndDate        string       `json:"endPublishedDate,omitempty"`
	IncludeDomains []string     `json:"includeDomains,omitempty"`
	ExcludeDomains []string     `json:"excludeDomains,omitempty"`
}

type exaContents struct {
	Text       *exaTextOpts      `json:"text,omitempty"`
	Highlights *exaHighlightOpts `json:"highlights,omitempty"`
}

type exaTextOpts struct {
	MaxCharacters int  `json:"maxCharacters,omitempty"`
	IncludeHTML   bool `json:"includeHtmlTags,omitempty"`
}

type exaHighlightOpts struct {
	NumSentences        int    `json:"numSentences,omitempty"`
	HighlightsPerResult int    `json:"highlightsPerResult,omitempty"`
	Query               string `json:"query,omitempty"`
}

type exaSearchResponse struct {
	RequestID string       `json:"requestId"`
	Results   []exaResult  `json:"results"`
	AutoDate  *exaAutoDate `json:"autopromptString,omitempty"`
}

type exaAutoDate struct{}

type exaResult struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	URL           string  `json:"url"`
	PublishedDate string  `json:"publishedDate,omitempty"`
	Author        string  `json:"author,omitempty"`
	Score         float64 `json:"score,omitempty"`
	Text          string  `json:"text,omitempty"`
	Summary       string  `json:"summary,omitempty"`
}

// ---------------------------------------------------------------------------
// ExaPlugin struct
// ---------------------------------------------------------------------------

// ExaPlugin implements SourcePlugin for Exa.ai web search.
// Thread-safe for concurrent use after Initialize.
type ExaPlugin struct {
	baseURL         string
	apiKey          string // server-default fallback when CredentialFor returns ""
	defaultType     string
	defaultCategory string
	includeText     bool
	httpClient      *http.Client
	enabled         bool
	rateLimit       float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "exa".
func (p *ExaPlugin) ID() string { return exaPluginID }

// Name returns the human-readable label.
func (p *ExaPlugin) Name() string { return exaPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *ExaPlugin) Description() string { return exaPluginDescription }

// ContentTypes — Exa surfaces general web (mapped to ContentTypeAny so it
// participates in mixed-type rtv_search calls without forcing paper).
func (p *ExaPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeAny}
}

// NativeFormat — Exa returns JSON.
func (p *ExaPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only at the plugin level; markdown rendering
// happens in the converter / consumer.
func (p *ExaPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatMarkdown}
}

// Capabilities reports Exa-specific filtering + sorting support.
func (p *ExaPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         true, // via contents.text
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     true, // includeDomains / excludeDomains
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false, // Exa has no language param
		SupportsPagination:       false, // Exa returns top-N only
		MaxResultsPerQuery:       exaMaxNumResults,
		CategoriesHint:           exaPluginCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatMarkdown},
		QueryIntents:             []Intent{IntentQuickLookup, IntentDeepResearch},
		Kinds:                    []ResultKind{KindWeb, KindNews},
	}
}

// Residency — Exa is US-resident. Blocked under eu_strict.
func (*ExaPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig. Cycle-2 Wave-1 plugins use
// NewEgressClient (Hook #4) by default rather than a vanilla *http.Client.
func (p *ExaPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = exaDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = exaDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.defaultType = stringFromExtra(cfg.Extra, exaExtraDefaultType, exaDefaultSearchType)
	p.defaultCategory = stringFromExtra(cfg.Extra, exaExtraCategory, "")
	p.includeText = strings.EqualFold(stringFromExtra(cfg.Extra, exaExtraIncludeText, "true"), "true")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status. Mirrors S2's pattern.
func (p *ExaPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes an Exa search. Credentials are read from ctx.
func (p *ExaPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, exaPluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: exa requires an API key", ErrCredentialRequired)
	}

	num := params.Limit
	if num <= 0 {
		num = exaDefaultNumResults
	}
	if num > exaMaxNumResults {
		num = exaMaxNumResults
	}

	if err := ValidateDomainList(params.Filters.IncludeDomains); err != nil {
		return nil, fmt.Errorf("exa: include_domains: %w", err)
	}
	if err := ValidateDomainList(params.Filters.ExcludeDomains); err != nil {
		return nil, fmt.Errorf("exa: exclude_domains: %w", err)
	}
	if err := ValidateLanguageTag(params.Filters.Language); err != nil {
		return nil, fmt.Errorf("exa: language: %w", err)
	}

	body := exaSearchRequest{
		Query:          params.Query,
		NumResults:     num,
		Type:           p.defaultType,
		Category:       p.defaultCategory,
		StartDate:      params.Filters.DateFrom,
		EndDate:        params.Filters.DateTo,
		IncludeDomains: params.Filters.IncludeDomains,
		ExcludeDomains: params.Filters.ExcludeDomains,
	}
	if p.includeText {
		body.Contents = &exaContents{Text: &exaTextOpts{MaxCharacters: 1000}}
	}

	resp, err := p.doSearch(ctx, body, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Results))
	for _, r := range resp.Results {
		pubs = append(pubs, exaResultToPublication(r))
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false, // Exa returns top-N; no pagination
	}, nil
}

// Get retrieves a single result by Exa ID via /contents. Cycle-2 minimum
// viable: returns NotImplemented for now — Exa's primary value is search,
// and rtv_get on a web result isn't a frequent caller pattern. Cycle-3
// can add this via /contents?ids=<id>.
func (p *ExaPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: exa does not support direct ID retrieval (cycle-2 limitation)", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *ExaPlugin) doSearch(ctx context.Context, body exaSearchRequest, apiKey string) (*exaSearchResponse, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("exa: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+exaSearchPath, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("exa: build request: %w", err)
	}
	req.Header.Set(exaAuthHeader, apiKey)
	req.Header.Set("Content-Type", exaContentTypeJSON)
	req.Header.Set("Accept", exaContentTypeJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exa: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: exa returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: exa", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("exa: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp exaSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("exa: decode response: %w", err)
	}
	return &resp, nil
}

// truncateForError keeps error logs from exploding when an upstream returns
// HTML. ~200-char cap matches what S2/OpenAlex use elsewhere.
func truncateForError(s string) string {
	const maxLen = 200
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// ---------------------------------------------------------------------------
// Result mapping
// ---------------------------------------------------------------------------

// exaResultToPublication maps one Exa result into a Publication, stuffing
// web-specific data (snippet, score, publishedAt, language) into
// SourceMetadata. The Router's Publication->Result converter picks Kind=Web
// from Capabilities and unpacks the metadata into Result.Web.
func exaResultToPublication(r exaResult) Publication {
	authors := make([]Author, 0, 1)
	if r.Author != "" {
		authors = append(authors, Author{Name: r.Author})
	}
	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceExa, r.ID),
		Source:      SourceExa,
		ContentType: ContentTypeAny,
		Title:       r.Title,
		Authors:     authors,
		Published:   normalizeExaDate(r.PublishedDate),
		Abstract:    r.Text, // populated when contents.text was requested
		URL:         r.URL,
	}

	meta := map[string]any{}
	if r.Text != "" {
		meta[smetaSnippet] = truncateSnippet(r.Text)
	}
	if r.Score > 0 {
		meta[smetaUpstreamScore] = r.Score
	}
	if r.PublishedDate != "" {
		meta[smetaPublishedAt] = r.PublishedDate
	}
	if len(meta) > 0 {
		pub.SourceMetadata = meta
	}
	return pub
}

// normalizeExaDate clamps Exa's ISO-8601 timestamp to a YYYY-MM-DD prefix
// so it matches the rest of retrievr's Publication.Published convention.
func normalizeExaDate(s string) string {
	if len(s) >= 10 {
		// ISO-8601 dates start with YYYY-MM-DD.
		_, err := time.Parse("2006-01-02", s[:10])
		if err == nil {
			return s[:10]
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Health helpers (mirror S2's pattern)
// ---------------------------------------------------------------------------

func (p *ExaPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *ExaPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}

// stringFromExtra reads a string from PluginConfig.Extra with a default.
func stringFromExtra(extra map[string]string, key, fallback string) string {
	if v, ok := extra[key]; ok && v != "" {
		return v
	}
	return fallback
}
