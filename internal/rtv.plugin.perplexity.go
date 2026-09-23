package internal

import (
	"bytes"
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
// Perplexity Sonar synthesized-web provider — Cycle 3 Wave-2.
//
// API: https://docs.perplexity.ai/api-reference/chat-completions
//   POST https://api.perplexity.ai/chat/completions
//   Headers: Authorization: Bearer <key>, content-type: application/json
//   Body: { model, messages: [...], return_citations: true }
//   Response: chat-completion shape with citations[] + choices[0].message.content
//
// Mapping per plan §6.1.1: one synthesized answer per query → primary
// Result with Kind=KindWeb, LLMContext=<answer>; citations are emitted as
// separate Result entries (sparse — Perplexity doesn't surface per-
// citation titles, just URLs).
//
// Residency: US-resident. Blocked under eu_strict.
//
// Latency note: Sonar's median is 5–13s — much slower than Brave/Linkup.
// Default per-source timeout (15s) is intentionally higher than other web
// providers; bump via sources.perplexity.timeout when running batch.
// ---------------------------------------------------------------------------

const (
	perplexityPluginID          = SourcePerplexity
	perplexityPluginName        = "Perplexity Sonar"
	perplexityPluginDescription = "Synthesized web answer + inline citations. Maps to LLMContext on a primary KindWeb Result; citations follow as sparse-shape entries. Slow (~5-13s); US-resident; blocked under eu_strict."

	perplexityDefaultBaseURL = "https://api.perplexity.ai"
	// perplexityAgentPath is the Agent API (retrievr-mcp#2). ⛔ Perplexity retires the Sonar
	// Chat Completions surface (`POST /chat/completions`) on 2026-09-27; this is its successor.
	perplexityAgentPath       = "/v1/agent"
	perplexityAuthHeader      = "Authorization"
	perplexityAuthScheme      = "Bearer "
	perplexityContentTypeJSON = "application/json"

	// perplexityDefaultModel is the Agent API's name for Sonar. ⛔ A bare `sonar` is refused there
	// (400 `model "sonar" is not supported`); see agentModel for the migration of old configs.
	perplexityDefaultModel = "perplexity/sonar"
	perplexityModelPrefix  = "perplexity/"
	// perplexityToolWebSearch is REQUIRED for this plugin to be a search source at all (measured
	// 2026-09-23): without it the Agent API answers from model memory with NO search results and
	// NO citations — a confident, unsourced answer from a plugin whose whole job is sourcing.
	perplexityToolWebSearch      = "web_search"
	perplexityToolChoiceRequired = "required"
	perplexityOutputMessage      = "message"
	perplexityOutputText         = "output_text"
	perplexityOutputResults      = "search_results"
	perplexityDefaultRPS         = 1.0

	perplexityCategoriesHint = "synthesized web answer + citations; latency ~5-13s; not recommended in fan-out under tight ctx deadlines"
)

// Extra-key constants.
const (
	perplexityExtraModel = "model" // perplexity/sonar (a bare legacy name is prefixed, see agentModel)
)

// ---------------------------------------------------------------------------
// Perplexity wire types
// ---------------------------------------------------------------------------

// The Agent API wire shapes, MEASURED against api.perplexity.ai on 2026-09-23 (retrievr-mcp#2).
//
// ⛔ THE REQUEST DECODES STRICTLY upstream — an unknown field is a 400 — so it carries exactly
// model, input and tools, and nothing "harmless" like the old return_citations.

type perplexityAgentRequest struct {
	Model string                 `json:"model"`
	Input []perplexityAgentInput `json:"input"`
	Tools []perplexityAgentTool  `json:"tools"`
	// ToolChoice is "required", and it is load-bearing (measured 2026-09-23): with web_search only
	// OFFERED, the model decides — it searched for a news question and answered a conceptual one
	// ("encoder-only vs decoder-only transformers") from memory, with no sources. A search plugin
	// must search every time.
	ToolChoice string `json:"tool_choice"`
}

type perplexityAgentInput struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type perplexityAgentTool struct {
	Type string `json:"type"`
}

// perplexityAgentResponse keeps only what the plugin reads. `output` is a list of typed items:
// a `message` (the synthesized answer, as `output_text` parts) and a `search_results` item (the
// sources, each with a title, url, snippet and date — richer than the old bare citation URLs).
type perplexityAgentResponse struct {
	ID     string                  `json:"id"`
	Model  string                  `json:"model"`
	Status string                  `json:"status"`
	Output []perplexityAgentOutput `json:"output"`
}

type perplexityAgentOutput struct {
	Type    string                  `json:"type"`
	Content []perplexityAgentPart   `json:"content,omitempty"`
	Results []perplexityAgentResult `json:"results,omitempty"`
}

type perplexityAgentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type perplexityAgentResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Date    string `json:"date"`
}

// agentModel migrates a legacy model name. ⚠ Every deployed config says `sonar`, and a bare name
// is refused by the Agent API, so an un-updated config would otherwise go dark on 2026-09-27 with
// a 400 per call. A name that already carries a vendor prefix is used as written.
func agentModel(m string) string {
	m = strings.TrimSpace(m)
	if m == "" {
		return perplexityDefaultModel
	}
	if strings.Contains(m, "/") {
		return m
	}
	return perplexityModelPrefix + m
}

// answerAndResults splits an Agent API response into the synthesized answer and its sources.
func (r *perplexityAgentResponse) answerAndResults() (string, []perplexityAgentResult) {
	var parts []string
	var results []perplexityAgentResult
	for _, o := range r.Output {
		switch o.Type {
		case perplexityOutputMessage:
			for _, c := range o.Content {
				if c.Type == perplexityOutputText && c.Text != "" {
					parts = append(parts, c.Text)
				}
			}
		case perplexityOutputResults:
			results = append(results, o.Results...)
		}
	}
	return strings.Join(parts, ""), results
}

// ---------------------------------------------------------------------------
// PerplexityPlugin
// ---------------------------------------------------------------------------

// PerplexityPlugin implements SourcePlugin for Perplexity Sonar.
type PerplexityPlugin struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

func (p *PerplexityPlugin) ID() string                  { return perplexityPluginID }
func (p *PerplexityPlugin) Name() string                { return perplexityPluginName }
func (p *PerplexityPlugin) Description() string         { return perplexityPluginDescription }
func (p *PerplexityPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypeAny} }
func (p *PerplexityPlugin) NativeFormat() ContentFormat { return FormatJSON }
func (p *PerplexityPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatMarkdown}
}

// Capabilities.
func (p *PerplexityPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         true, // synthesized answer is the "full text"
		SupportsCitations:        true,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       false,
		MaxResultsPerQuery:       1, // one synthesized answer; citations supplemental
		CategoriesHint:           perplexityCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatMarkdown},
		QueryIntents:             []Intent{IntentQuickLookup, IntentDeepResearch},
		Kinds:                    []ResultKind{KindWeb},
		RequiresCredential:       true,
	}
}

// Residency — US.
func (*PerplexityPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize.
func (p *PerplexityPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = perplexityDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = perplexityDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.model = agentModel(stringFromExtra(cfg.Extra, perplexityExtraModel, perplexityDefaultModel))

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		// Bump default — Sonar is slow.
		timeout = DefaultPluginTimeout * 2
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health.
func (p *PerplexityPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{Enabled: p.enabled, Healthy: p.healthy, RateLimit: p.rateLimit, LastError: p.lastError}
}

// Search calls /chat/completions and packages the response into one
// primary Publication (the synthesized answer) plus per-citation
// Publications. Credentials read from ctx.
func (p *PerplexityPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, perplexityPluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: perplexity requires an API key", ErrCredentialRequired)
	}

	body := perplexityAgentRequest{
		Model:      p.model,
		Input:      []perplexityAgentInput{{Type: perplexityOutputMessage, Role: "user", Content: params.Query}},
		Tools:      []perplexityAgentTool{{Type: perplexityToolWebSearch}},
		ToolChoice: perplexityToolChoiceRequired,
	}

	resp, err := p.doSearch(ctx, body, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	answer, sources := resp.answerAndResults()
	if answer == "" && len(sources) == 0 {
		return &SearchResult{Total: 0, Results: nil, HasMore: false}, nil
	}
	pubs := make([]Publication, 0, 1+len(sources))

	// Primary: synthesized answer with LLMContext=answer.
	primary := Publication{
		ID:          fmt.Sprintf("%s:%s", perplexityPluginID, resp.ID),
		Source:      perplexityPluginID,
		ContentType: ContentTypeAny,
		Title:       fmt.Sprintf("Perplexity synthesized answer: %s", truncateForTitle(params.Query)),
		Abstract:    answer,
	}
	if len(sources) > 0 {
		primary.URL = sources[0].URL
	}
	primary.SourceMetadata = map[string]any{
		smetaSnippet:  truncateSnippet(answer),
		"llm_context": answer,
		"model":       resp.Model,
		// ⚠ Stated, not implied: an answer with no sources was not grounded in a search, and a
		// consumer must be able to tell that apart from a sourced one.
		"grounded": len(sources) > 0,
	}
	pubs = append(pubs, primary)

	// Each source as a follow-up Publication. The Agent API names each source's title and date,
	// which the old citation list did not — the host stays the fallback title.
	for i, src := range sources {
		host := hostFromURL(src.URL)
		title := strings.TrimSpace(src.Title)
		if title == "" {
			title = host
		}
		if title == "" {
			title = fmt.Sprintf("Citation %d", i+1)
		}
		meta := map[string]any{smetaDomain: host}
		if src.Snippet != "" {
			meta[smetaSnippet] = truncateSnippet(src.Snippet)
		}
		pubs = append(pubs, Publication{
			ID:             fmt.Sprintf("%s:%s/cit/%d", perplexityPluginID, resp.ID, i+1),
			Source:         perplexityPluginID,
			ContentType:    ContentTypeAny,
			Title:          title,
			URL:            src.URL,
			Published:      src.Date,
			SourceMetadata: meta,
		})
	}

	return &SearchResult{Total: len(pubs), Results: pubs, HasMore: false}, nil
}

// Get is not supported — Perplexity has no per-result-ID retrieval API.
func (p *PerplexityPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, NewGetUnsupportedError("perplexity has no per-result Get API")
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *PerplexityPlugin) doSearch(ctx context.Context, body perplexityAgentRequest, apiKey string) (*perplexityAgentResponse, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("perplexity: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+perplexityAgentPath, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("perplexity: build request: %w", err)
	}
	req.Header.Set(perplexityAuthHeader, perplexityAuthScheme+apiKey)
	req.Header.Set("Content-Type", perplexityContentTypeJSON)
	req.Header.Set("Accept", perplexityContentTypeJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perplexity: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: perplexity returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: perplexity", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("perplexity: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp perplexityAgentResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("perplexity: decode response: %w", err)
	}
	return &resp, nil
}

// hostFromURL extracts the hostname from a URL. Empty when input is invalid.
func hostFromURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// truncateForTitle keeps Perplexity's primary-result title compact when the
// caller's query is verbose.
func truncateForTitle(s string) string {
	const maxLen = 80
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *PerplexityPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *PerplexityPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = sanitizeHealthError(err)
	}
}
