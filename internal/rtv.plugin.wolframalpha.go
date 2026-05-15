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
	"time"
)

// ---------------------------------------------------------------------------
// Wolfram Alpha — v6 cycle 5 / v2.18.0.
//
// API: GET https://api.wolframalpha.com/v2/query
//   Params:
//     input          free-text question
//     appid          required (free dev tier: 2k/mo)
//     output         "json"
//     format         "plaintext" (default) | "image" | "minput"
//     reinterpret    "true" — lets Wolfram retry on parse failure
//
// Response (subset):
//   { "queryresult": {
//       "success": bool,
//       "error": bool,
//       "numpods": int,
//       "pods": [
//         { "title":"Input", "id":"Input", "scanner":"Identity",
//           "subpods":[{"plaintext":"..."}] },
//         { "title":"Result", "id":"Result",
//           "subpods":[{"plaintext":"6.28318..."}] } ] } }
//
// Each pod becomes a Publication; the user usually wants the "Result"
// pod, so it's surfaced first when present.
//
// Free dev tier: 2k queries/mo. Per-call credential: `wolframalpha`.
// Refuses to start without a key.
// Residency: US (Wolfram Research, Champaign IL).
// ---------------------------------------------------------------------------

const (
	wolframAlphaPluginID          = SourceWolframAlpha
	wolframAlphaPluginName        = "Wolfram Alpha"
	wolframAlphaPluginDescription = "Query Wolfram Alpha (api.wolframalpha.com) for computed answers, structured facts, formulas, and unit conversions. Free dev tier 2k/mo; paid above. Each result pod becomes a Publication with KindFact at the v2 layer. Per-call credential: wolframalpha."

	wolframAlphaDefaultBaseURL = "https://api.wolframalpha.com"
	wolframAlphaQueryPath      = "/v2/query"
	wolframAlphaDefaultLimit   = 10
	wolframAlphaMaxLimitCap    = 50
	wolframAlphaDefaultRPS     = 2.0
	wolframAlphaDefaultTimeout = 30 * time.Second

	wolframAlphaIDPrefix = "wolframalpha:"

	wolframAlphaParamInput       = "input"
	wolframAlphaParamAppID       = "appid"
	wolframAlphaParamOutput      = "output"
	wolframAlphaParamFormat      = "format"
	wolframAlphaParamReinterpret = "reinterpret"

	wolframAlphaOutputJSON      = "json"
	wolframAlphaFormatPlaintext = "plaintext"

	wolframAlphaCategoriesHint = "Wolfram Alpha has no native category filter — the query language itself is the interface. Use phrasings like 'integrate x^2 from 0 to 1' or 'population of Berlin'."

	wolframAlphaMetaKeyScanner = "wolframalpha_scanner"
	wolframAlphaMetaKeyPodID   = "wolframalpha_pod_id"
)

// ---------------------------------------------------------------------------
// Wolfram Alpha wire types
// ---------------------------------------------------------------------------

type wolframAlphaResponse struct {
	QueryResult wolframAlphaQueryResult `json:"queryresult"`
}

type wolframAlphaQueryResult struct {
	Success bool              `json:"success,omitempty"`
	Error   any               `json:"error,omitempty"` // can be bool or object
	NumPods int               `json:"numpods,omitempty"`
	Pods    []wolframAlphaPod `json:"pods,omitempty"`
}

type wolframAlphaPod struct {
	Title   string               `json:"title,omitempty"`
	ID      string               `json:"id,omitempty"`
	Scanner string               `json:"scanner,omitempty"`
	Primary bool                 `json:"primary,omitempty"`
	Subpods []wolframAlphaSubpod `json:"subpods,omitempty"`
}

type wolframAlphaSubpod struct {
	Title     string `json:"title,omitempty"`
	Plaintext string `json:"plaintext,omitempty"`
}

// ---------------------------------------------------------------------------
// WolframAlphaPlugin
// ---------------------------------------------------------------------------

// WolframAlphaPlugin implements SourcePlugin for the Wolfram Alpha v2
// query API. Thread-safe after Initialize.
type WolframAlphaPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "wolframalpha".
func (p *WolframAlphaPlugin) ID() string { return wolframAlphaPluginID }

// Name returns the human-readable label.
func (p *WolframAlphaPlugin) Name() string { return wolframAlphaPluginName }

// Description returns the LLM-facing one-liner.
func (p *WolframAlphaPlugin) Description() string { return wolframAlphaPluginDescription }

// ContentTypes — paper (each pod is surfaced with KindFact at the v2
// layer).
func (p *WolframAlphaPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *WolframAlphaPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *WolframAlphaPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Wolfram Alpha's filter/sort surface.
func (p *WolframAlphaPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    false,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       false,
		MaxResultsPerQuery:       wolframAlphaMaxLimitCap,
		CategoriesHint:           wolframAlphaCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentReference},
		Kinds:                    []ResultKind{KindFact},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *WolframAlphaPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = wolframAlphaDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = wolframAlphaDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = wolframAlphaDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *WolframAlphaPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Wolfram Alpha /v2/query call. Each result pod
// becomes one Publication, with the "primary" / "Result" pod surfaced
// first when present.
func (p *WolframAlphaPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = wolframAlphaDefaultLimit
	}
	if limit > wolframAlphaMaxLimitCap {
		limit = wolframAlphaMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceWolframAlpha, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: wolframalpha requires an API key", ErrCredentialRequired)
	}

	resp, err := p.doSearch(ctx, params, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pods := reorderPrimaryFirst(resp.QueryResult.Pods)
	pubs := make([]Publication, 0, len(pods))
	for i := range pods {
		if pub, ok := wolframAlphaPodToPublication(&pods[i], params.Query); ok {
			pubs = append(pubs, pub)
		}
		if len(pubs) >= limit {
			break
		}
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 5.
func (p *WolframAlphaPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: wolframalpha Get is not wired in cycle 5", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *WolframAlphaPlugin) doSearch(ctx context.Context, params SearchParams, apiKey string) (*wolframAlphaResponse, error) {
	q := url.Values{}
	q.Set(wolframAlphaParamInput, params.Query)
	q.Set(wolframAlphaParamAppID, apiKey)
	q.Set(wolframAlphaParamOutput, wolframAlphaOutputJSON)
	q.Set(wolframAlphaParamFormat, wolframAlphaFormatPlaintext)
	q.Set(wolframAlphaParamReinterpret, "true")

	reqURL := p.baseURL + wolframAlphaQueryPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("wolframalpha: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wolframalpha: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: wolframalpha", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: wolframalpha", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("wolframalpha: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp wolframAlphaResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("wolframalpha: decode response: %w", err)
	}
	return &resp, nil
}

// reorderPrimaryFirst hoists the "primary" pod (typically id="Result")
// to the front of the slice without mutating the input.
func reorderPrimaryFirst(in []wolframAlphaPod) []wolframAlphaPod {
	if len(in) <= 1 {
		return in
	}
	primaryIdx := -1
	for i := range in {
		if in[i].Primary || in[i].ID == "Result" {
			primaryIdx = i
			break
		}
	}
	if primaryIdx <= 0 {
		return in
	}
	out := make([]wolframAlphaPod, 0, len(in))
	out = append(out, in[primaryIdx])
	out = append(out, in[:primaryIdx]...)
	out = append(out, in[primaryIdx+1:]...)
	return out
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func wolframAlphaPodToPublication(pod *wolframAlphaPod, query string) (Publication, bool) {
	body := ""
	for _, sp := range pod.Subpods {
		if v := strings.TrimSpace(sp.Plaintext); v != "" {
			if body != "" {
				body += "\n"
			}
			body += v
		}
	}
	if body == "" {
		return Publication{}, false
	}

	meta := map[string]any{
		wolframAlphaMetaKeyPodID: pod.ID,
	}
	if pod.Scanner != "" {
		meta[wolframAlphaMetaKeyScanner] = pod.Scanner
	}

	return Publication{
		ID:             wolframAlphaIDPrefix + pod.ID,
		Source:         SourceWolframAlpha,
		ContentType:    ContentTypePaper,
		Title:          strings.TrimSpace(pod.Title),
		Abstract:       body,
		URL:            "https://www.wolframalpha.com/input?i=" + url.QueryEscape(query),
		SourceMetadata: meta,
	}, true
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *WolframAlphaPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *WolframAlphaPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
