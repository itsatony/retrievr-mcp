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

	perplexityDefaultBaseURL  = "https://api.perplexity.ai"
	perplexityCompletionsPath = "/chat/completions"
	perplexityAuthHeader      = "Authorization"
	perplexityAuthScheme      = "Bearer "
	perplexityContentTypeJSON = "application/json"

	perplexityDefaultModel = "sonar"
	perplexityDefaultRPS   = 1.0

	perplexityCategoriesHint = "synthesized web answer + citations; latency ~5-13s; not recommended in fan-out under tight ctx deadlines"
)

// Extra-key constants.
const (
	perplexityExtraModel = "model" // sonar | sonar-pro | sonar-reasoning
)

// ---------------------------------------------------------------------------
// Perplexity wire types
// ---------------------------------------------------------------------------

type perplexityChatRequest struct {
	Model           string              `json:"model"`
	Messages        []perplexityMessage `json:"messages"`
	ReturnCitations bool                `json:"return_citations,omitempty"`
}

type perplexityMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type perplexityChatResponse struct {
	ID        string             `json:"id"`
	Model     string             `json:"model"`
	Citations []string           `json:"citations,omitempty"`
	Choices   []perplexityChoice `json:"choices"`
}

type perplexityChoice struct {
	Index        int               `json:"index"`
	Message      perplexityMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
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

	p.model = stringFromExtra(cfg.Extra, perplexityExtraModel, perplexityDefaultModel)

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

	body := perplexityChatRequest{
		Model:           p.model,
		Messages:        []perplexityMessage{{Role: "user", Content: params.Query}},
		ReturnCitations: true,
	}

	resp, err := p.doSearch(ctx, body, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	if len(resp.Choices) == 0 {
		return &SearchResult{Total: 0, Results: nil, HasMore: false}, nil
	}

	answer := resp.Choices[0].Message.Content
	pubs := make([]Publication, 0, 1+len(resp.Citations))

	// Primary: synthesized answer with LLMContext=answer.
	primary := Publication{
		ID:          fmt.Sprintf("%s:%s", perplexityPluginID, resp.ID),
		Source:      perplexityPluginID,
		ContentType: ContentTypeAny,
		Title:       fmt.Sprintf("Perplexity synthesized answer: %s", truncateForTitle(params.Query)),
		Abstract:    answer,
	}
	if len(resp.Citations) > 0 {
		primary.URL = resp.Citations[0]
	}
	primary.SourceMetadata = map[string]any{
		smetaSnippet:  truncateSnippet(answer),
		"llm_context": answer,
		"model":       resp.Model,
	}
	pubs = append(pubs, primary)

	// Each citation as a sparse follow-up Publication. Title is derived
	// from the URL hostname since Perplexity doesn't surface per-citation
	// titles.
	for i, citationURL := range resp.Citations {
		host := hostFromURL(citationURL)
		title := host
		if title == "" {
			title = fmt.Sprintf("Citation %d", i+1)
		}
		pubs = append(pubs, Publication{
			ID:          fmt.Sprintf("%s:%s/cit/%d", perplexityPluginID, resp.ID, i+1),
			Source:      perplexityPluginID,
			ContentType: ContentTypeAny,
			Title:       title,
			URL:         citationURL,
			SourceMetadata: map[string]any{
				smetaDomain: host,
			},
		})
	}

	return &SearchResult{Total: len(pubs), Results: pubs, HasMore: false}, nil
}

// Get is not supported — Perplexity has no per-result-ID retrieval API.
func (p *PerplexityPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: perplexity has no per-result Get API", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *PerplexityPlugin) doSearch(ctx context.Context, body perplexityChatRequest, apiKey string) (*perplexityChatResponse, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("perplexity: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+perplexityCompletionsPath, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("perplexity: build request: %w", err)
	}
	req.Header.Set(perplexityAuthHeader, perplexityAuthScheme+apiKey)
	req.Header.Set("Content-Type", perplexityContentTypeJSON)
	req.Header.Set("Accept", perplexityContentTypeJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perplexity: http: %w", err)
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

	var resp perplexityChatResponse
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
		p.lastError = err.Error()
	}
}
