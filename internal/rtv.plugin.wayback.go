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
// Wayback Machine resolver — v5 cycle 6 / v2.13.0.
//
// API: GET https://archive.org/wayback/available?url=<url>&timestamp=<ts>
//   Params:
//     url        full or bare URL to look up
//     timestamp  YYYYMMDDhhmmss (any prefix length accepted; closest
//                snapshot to the given timestamp is returned)
//
// Response:
//   { "url": "...", "archived_snapshots": {
//       "closest": { "available": true, "url": "...", "timestamp": "...",
//                    "status": "200" } } }
//
// This plugin is a GET-only RESOLVER. Search() returns a single
// degenerate result explaining the resolver semantics so agents
// discover the Get path; the real work happens in Get(id) where the
// id is "<url>" or "<url>:<YYYYMMDD>".
//
// Free, no auth required.
// Residency: US (Internet Archive non-profit, San Francisco).
// ---------------------------------------------------------------------------

const (
	waybackPluginID          = SourceWayback
	waybackPluginName        = "Wayback Machine"
	waybackPluginDescription = "Resolve a URL to its closest Internet Archive Wayback snapshot. GET-only: pass the URL (and optional YYYYMMDD timestamp) as the rtv_get id like 'wayback:https://example.com' or 'wayback:https://example.com:20230114'. Search returns a usage hint, not real results."

	waybackDefaultBaseURL = "https://archive.org"
	waybackAvailablePath  = "/wayback/available"
	waybackDefaultRPS     = 5.0
	waybackDefaultTimeout = 15 * time.Second

	waybackIDPrefix = "wayback:"

	waybackParamURL       = "url"
	waybackParamTimestamp = "timestamp"

	waybackHintTitle = "Wayback Machine is a resolver, not a search index"
	waybackHintBody  = "Pass an id like 'wayback:https://example.com' or 'wayback:https://example.com:20230114' to rtv_get. Returns the closest archived snapshot URL and timestamp."

	waybackMetaKeyOriginal   = "wayback_original_url"
	waybackMetaKeyTimestamp  = "wayback_timestamp"
	waybackMetaKeyHTTPStatus = "wayback_http_status"
)

// ---------------------------------------------------------------------------
// Wayback wire types
// ---------------------------------------------------------------------------

type waybackAvailableResponse struct {
	URL               string           `json:"url,omitempty"`
	ArchivedSnapshots waybackSnapshots `json:"archived_snapshots,omitempty"`
}

type waybackSnapshots struct {
	Closest *waybackSnapshot `json:"closest,omitempty"`
}

type waybackSnapshot struct {
	Available bool   `json:"available,omitempty"`
	URL       string `json:"url,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Status    string `json:"status,omitempty"`
}

// ---------------------------------------------------------------------------
// WaybackPlugin
// ---------------------------------------------------------------------------

// WaybackPlugin implements SourcePlugin as a resolver — Search returns a
// usage hint; Get does the actual snapshot lookup. Thread-safe after
// Initialize.
type WaybackPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "wayback".
func (p *WaybackPlugin) ID() string { return waybackPluginID }

// Name returns the human-readable label.
func (p *WaybackPlugin) Name() string { return waybackPluginName }

// Description returns the LLM-facing one-liner.
func (p *WaybackPlugin) Description() string { return waybackPluginDescription }

// ContentTypes — paper (the snapshot URL lives in the paper-family
// metadata layer; KindWeb at the v2 layer).
func (p *WaybackPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *WaybackPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *WaybackPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Wayback's surface (which is intentionally minimal).
func (p *WaybackPlugin) Capabilities() SourceCapabilities {
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
		MaxResultsPerQuery:       1,
		CategoriesHint:           "",
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentReference},
		Kinds:                    []ResultKind{KindWeb},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *WaybackPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = waybackDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = waybackDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = waybackDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *WaybackPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search returns a single Publication that explains the resolver
// semantics. Real lookups go through Get(id).
func (p *WaybackPlugin) Search(_ context.Context, _ SearchParams) (*SearchResult, error) {
	hint := Publication{
		ID:          waybackIDPrefix + "_hint",
		Source:      SourceWayback,
		ContentType: ContentTypePaper,
		Title:       waybackHintTitle,
		Abstract:    waybackHintBody,
		URL:         "https://web.archive.org",
	}
	return &SearchResult{Total: 1, Results: []Publication{hint}, HasMore: false}, nil
}

// Get resolves a URL (optionally a URL:timestamp pair) to its closest
// Wayback snapshot. The id format is "<url>" or "<url>:<YYYYMMDD>" with
// the wayback: prefix already stripped by the router.
func (p *WaybackPlugin) Get(ctx context.Context, id string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	target, ts := waybackSplitIDArg(id)
	if target == "" {
		return nil, fmt.Errorf("%w: wayback Get requires a url id, got %q", ErrInvalidID, id)
	}

	q := url.Values{}
	q.Set(waybackParamURL, target)
	if ts != "" {
		q.Set(waybackParamTimestamp, ts)
	}

	reqURL := p.baseURL + waybackAvailablePath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("wayback: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("wayback: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: wayback", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("wayback: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp waybackAvailableResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("wayback: decode response: %w", err)
	}
	p.recordSuccess()

	snap := resp.ArchivedSnapshots.Closest
	if snap == nil || !snap.Available || snap.URL == "" {
		return nil, fmt.Errorf("%w: wayback has no snapshot for %s", ErrGetFailed, target)
	}

	pub := Publication{
		ID:          waybackIDPrefix + target,
		Source:      SourceWayback,
		ContentType: ContentTypePaper,
		Title:       target,
		URL:         snap.URL,
		Published:   waybackTimestampToDate(snap.Timestamp),
		SourceMetadata: map[string]any{
			waybackMetaKeyOriginal:   target,
			waybackMetaKeyTimestamp:  snap.Timestamp,
			waybackMetaKeyHTTPStatus: snap.Status,
		},
	}
	return &pub, nil
}

// waybackSplitIDArg parses "<url>" or "<url>:<YYYYMMDD>" into its parts.
// Only the LAST colon-trailing segment that looks like a YYYYMMDD
// (8+ digits, no slash) is treated as a timestamp; this preserves
// "https://" colons embedded in the url part.
func waybackSplitIDArg(id string) (target, ts string) {
	idx := strings.LastIndex(id, ":")
	if idx == -1 {
		return id, ""
	}
	tail := id[idx+1:]
	if isAllDigits(tail) && len(tail) >= 8 {
		return id[:idx], tail
	}
	return id, ""
}

// isAllDigits reports whether s is non-empty and consists solely of
// ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// waybackTimestampToDate converts the YYYYMMDDhhmmss timestamp to
// YYYY-MM-DD for the Publication.Published field.
func waybackTimestampToDate(ts string) string {
	if len(ts) < 8 {
		return ""
	}
	return ts[:4] + "-" + ts[4:6] + "-" + ts[6:8]
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *WaybackPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *WaybackPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
