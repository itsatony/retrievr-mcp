package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Stack Exchange Q&A provider — v5 cycle 1 / v2.8.0.
//
// API: GET https://api.stackexchange.com/2.3/search/advanced
//   Params:
//     q            free-text query
//     site         "stackoverflow" | "serverfault" | "askubuntu" | ...
//                  (required; this plugin defaults to "stackoverflow" but
//                  the operator can override via extra.default_site, and
//                  a per-call hint can land via Filters.Extra["site"].)
//     tagged       semicolon-joined tag list (maps to SearchFilters.Categories)
//     fromdate     unix epoch seconds (SearchFilters.DateFrom)
//     todate       unix epoch seconds (SearchFilters.DateTo)
//     order        "desc" | "asc"
//     sort         "relevance" | "activity" | "votes" | "creation"
//     pagesize     1..100
//     key          API key (free, registered; lifts daily quota 300→10k)
//     access_token oauth access token (extends key to authenticated 10k)
//
// Response is gzip-encoded by default; net/http handles gzip transparently
// when "Accept-Encoding" is unset (Go default). Body shape:
//   { "items": [ { "question_id", "title", "body", "owner": {...},
//                  "tags": [...], "score", "answer_count", "is_answered",
//                  "accepted_answer_id", "creation_date", "link" } ],
//     "has_more": bool, "quota_max": int, "quota_remaining": int,
//     "backoff": int }
//
// "backoff" is the server signalling we must wait N seconds before the
// next request. We honor it by returning ErrRateLimitExceeded so the
// router rate-limit middleware backs off; the operator should set a
// conservative rate_limit in config to avoid hitting it.
//
// Residency: US-resident (Stack Exchange Inc., NYC). Content is licensed
// CC-BY-SA so admissible under eu_preferred with attribution; blocked
// under eu_strict.
// ---------------------------------------------------------------------------

const (
	stackExchangePluginID          = SourceStackExchange
	stackExchangePluginName        = "Stack Exchange"
	stackExchangePluginDescription = "Search Q&A across the Stack Exchange network (Stack Overflow, Server Fault, Ask Ubuntu, Math Overflow, and 170+ other sites). Free — anonymous access caps at 300/day/IP; configure a free API key for 10k/day. CC-BY-SA content. Per-call credential: stackexchange. Default site is stackoverflow; override per-deployment via extra.default_site."

	stackExchangeDefaultBaseURL = "https://api.stackexchange.com"
	stackExchangeSearchPath     = "/2.3/search/advanced"

	stackExchangeDefaultSite    = "stackoverflow"
	stackExchangeDefaultLimit   = 25
	stackExchangeMaxLimitCap    = 100
	stackExchangeDefaultRPS     = 0.5 // anonymous 300/day ≈ 0.0035 rps; 0.5 rps stays well within 10k/day
	stackExchangeDefaultTimeout = 10 * time.Second

	stackExchangeQueryParamQ        = "q"
	stackExchangeQueryParamSite     = "site"
	stackExchangeQueryParamTagged   = "tagged"
	stackExchangeQueryParamFromDate = "fromdate"
	stackExchangeQueryParamToDate   = "todate"
	stackExchangeQueryParamPageSize = "pagesize"
	stackExchangeQueryParamOrder    = "order"
	stackExchangeQueryParamSort     = "sort"
	stackExchangeQueryParamKey      = "key"
	stackExchangeQueryParamFilter   = "filter"

	// "withbody" is a built-in Stack Exchange filter that includes the
	// question body in the response (default omits it). Saves an extra
	// round-trip for snippet generation. See:
	// https://api.stackexchange.com/docs/filters
	stackExchangeFilterWithBody = "withbody"

	stackExchangeSortRelevance = "relevance"
	stackExchangeSortCreation  = "creation"
	stackExchangeOrderDesc     = "desc"
	stackExchangeOrderAsc      = "asc"

	stackExchangeAcceptHeader = "Accept"
	stackExchangeAcceptJSON   = "application/json"

	stackExchangeTagJoinSep = ";"
	stackExchangeIDPrefix   = "stackexchange:"

	// Extra config keys.
	stackExchangeExtraDefaultSite = "default_site"

	stackExchangeCategoriesHint = "Stack Exchange Q&A across 170+ sites; filters.categories map to SE tags (joined with ';'). filters.date_from/date_to honoured via fromdate/todate (unix seconds). Default site is configured per-deployment via extra.default_site (defaults to stackoverflow)."

	// stackExchangeErrorNameThrottleSubstr is the case-insensitive
	// substring that flags an in-band 200-OK throttle envelope (e.g.
	// error_name="throttle_violation"). We surface it as a typed rate-
	// limit error so the middleware backs off rather than re-firing.
	stackExchangeErrorNameThrottleSubstr = "throttle"
)

// ---------------------------------------------------------------------------
// Stack Exchange wire types
// ---------------------------------------------------------------------------

type stackExchangeSearchResponse struct {
	Items          []stackExchangeQuestion `json:"items,omitempty"`
	HasMore        bool                    `json:"has_more,omitempty"`
	QuotaMax       int                     `json:"quota_max,omitempty"`
	QuotaRemaining int                     `json:"quota_remaining,omitempty"`
	Backoff        int                     `json:"backoff,omitempty"`
	ErrorID        int                     `json:"error_id,omitempty"`
	ErrorName      string                  `json:"error_name,omitempty"`
	ErrorMessage   string                  `json:"error_message,omitempty"`
}

type stackExchangeQuestion struct {
	QuestionID       int64              `json:"question_id"`
	Title            string             `json:"title"`
	Body             string             `json:"body,omitempty"`
	Link             string             `json:"link"`
	Tags             []string           `json:"tags,omitempty"`
	Score            int                `json:"score"`
	AnswerCount      int                `json:"answer_count"`
	IsAnswered       bool               `json:"is_answered"`
	AcceptedAnswerID int64              `json:"accepted_answer_id,omitempty"`
	CreationDate     int64              `json:"creation_date"`
	LastActivityDate int64              `json:"last_activity_date,omitempty"`
	Owner            stackExchangeOwner `json:"owner"`
	ContentLicense   string             `json:"content_license,omitempty"`
}

type stackExchangeOwner struct {
	UserID      int64  `json:"user_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Link        string `json:"link,omitempty"`
}

// ---------------------------------------------------------------------------
// StackExchangePlugin
// ---------------------------------------------------------------------------

// StackExchangePlugin implements SourcePlugin for api.stackexchange.com.
// Thread-safe after Initialize.
type StackExchangePlugin struct {
	baseURL     string
	defaultSite string
	apiKey      string // optional; lifts quota from 300 → 10k/day
	httpClient  *http.Client
	enabled     bool
	rateLimit   float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "stackexchange".
func (p *StackExchangePlugin) ID() string { return stackExchangePluginID }

// Name returns the human-readable label.
func (p *StackExchangePlugin) Name() string { return stackExchangePluginName }

// Description returns a one-liner for LLM tool listing.
func (p *StackExchangePlugin) Description() string { return stackExchangePluginDescription }

// ContentTypes — Stack Exchange emits paper (with KindQA discriminator).
func (p *StackExchangePlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat — JSON.
func (p *StackExchangePlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *StackExchangePlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Stack Exchange's filter/sort surface.
func (p *StackExchangePlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true, // fromdate/todate (unix seconds)
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true, // tagged param
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       false, // not wired in cycle 1
		MaxResultsPerQuery:       stackExchangeMaxLimitCap,
		CategoriesHint:           stackExchangeCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentReference, IntentCodeProvenance},
		Kinds:                    []ResultKind{KindQA},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *StackExchangePlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = stackExchangeDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = stackExchangeDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.defaultSite = stringFromExtra(cfg.Extra, stackExchangeExtraDefaultSite, stackExchangeDefaultSite)
	p.apiKey = cfg.APIKey

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = stackExchangeDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *StackExchangePlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Stack Exchange search query.
func (p *StackExchangePlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = stackExchangeDefaultLimit
	}
	if limit > stackExchangeMaxLimitCap {
		limit = stackExchangeMaxLimitCap
	}

	site := p.defaultSite
	apiKey := CredentialFor(ctx, SourceStackExchange, p.apiKey)

	resp, err := p.doSearch(ctx, params, site, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Items))
	for _, q := range resp.Items {
		pubs = append(pubs, stackExchangeQuestionToPublication(q, site))
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: resp.HasMore,
	}, nil
}

// Get is not wired in cycle 1. The /questions/{ids} endpoint can fetch a
// single question by ID; deferred to a follow-on cycle if signal warrants.
func (p *StackExchangePlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: stackexchange Get is not wired in cycle 1", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *StackExchangePlugin) doSearch(ctx context.Context, params SearchParams, site string, limit int, apiKey string) (*stackExchangeSearchResponse, error) {
	q := url.Values{}
	q.Set(stackExchangeQueryParamQ, params.Query)
	q.Set(stackExchangeQueryParamSite, site)
	q.Set(stackExchangeQueryParamPageSize, strconv.Itoa(limit))
	q.Set(stackExchangeQueryParamFilter, stackExchangeFilterWithBody)

	if len(params.Filters.Categories) > 0 {
		q.Set(stackExchangeQueryParamTagged, strings.Join(params.Filters.Categories, stackExchangeTagJoinSep))
	}
	if from, ok := parseFilterDateUnix(params.Filters.DateFrom); ok {
		q.Set(stackExchangeQueryParamFromDate, strconv.FormatInt(from, 10))
	}
	if to, ok := parseFilterDateUnix(params.Filters.DateTo); ok {
		q.Set(stackExchangeQueryParamToDate, strconv.FormatInt(to, 10))
	}
	switch params.Sort {
	case SortDateDesc:
		q.Set(stackExchangeQueryParamSort, stackExchangeSortCreation)
		q.Set(stackExchangeQueryParamOrder, stackExchangeOrderDesc)
	case SortDateAsc:
		q.Set(stackExchangeQueryParamSort, stackExchangeSortCreation)
		q.Set(stackExchangeQueryParamOrder, stackExchangeOrderAsc)
	default:
		q.Set(stackExchangeQueryParamSort, stackExchangeSortRelevance)
		q.Set(stackExchangeQueryParamOrder, stackExchangeOrderDesc)
	}
	if apiKey != "" {
		q.Set(stackExchangeQueryParamKey, apiKey)
	}

	reqURL := p.baseURL + stackExchangeSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("stackexchange: build request: %w", err)
	}
	req.Header.Set(stackExchangeAcceptHeader, stackExchangeAcceptJSON)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stackexchange: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: stackexchange", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("stackexchange: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp stackExchangeSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("stackexchange: decode response: %w", err)
	}
	// The API returns 200 + an error envelope in some throttle paths;
	// surface that as a typed rate-limit error so the middleware backs off.
	if resp.ErrorID != 0 {
		if resp.Backoff > 0 || strings.Contains(strings.ToLower(resp.ErrorName), stackExchangeErrorNameThrottleSubstr) {
			return nil, fmt.Errorf("%w: stackexchange: %s", ErrRateLimitExceeded, resp.ErrorName)
		}
		return nil, fmt.Errorf("stackexchange: api error: id=%d name=%s msg=%s", resp.ErrorID, resp.ErrorName, resp.ErrorMessage)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func stackExchangeQuestionToPublication(q stackExchangeQuestion, site string) Publication {
	rawID := strconv.FormatInt(q.QuestionID, 10)
	dedupKey := site + ":" + rawID

	abstract := stackExchangeStripHTML(q.Body)
	title := html.UnescapeString(q.Title)
	published := unixSecondsToShortDate(q.CreationDate)
	updated := unixSecondsToShortDate(q.LastActivityDate)

	author := q.Owner.DisplayName
	authors := []Author{}
	if author != "" {
		authors = append(authors, Author{Name: html.UnescapeString(author)})
	}

	meta := map[string]any{
		MetaKeyQAQuestionID:  dedupKey,
		smetaQASite:          site,
		smetaQARawQuestionID: rawID,
		smetaQATags:          append([]string(nil), q.Tags...),
		smetaQAAnswerCount:   q.AnswerCount,
		smetaQAScore:         q.Score,
		smetaQAIsAnswered:    q.IsAnswered,
		smetaAuthorHandle:    author,
	}
	if q.AcceptedAnswerID != 0 {
		meta[smetaQAAcceptedAnswerID] = strconv.FormatInt(q.AcceptedAnswerID, 10)
	}
	if q.Owner.Link != "" {
		meta[smetaAuthorURL] = q.Owner.Link
	}

	pub := Publication{
		ID:             stackExchangeIDPrefix + dedupKey,
		Source:         SourceStackExchange,
		ContentType:    ContentTypePaper,
		Title:          title,
		Abstract:       abstract,
		URL:            q.Link,
		Published:      published,
		Updated:        updated,
		Authors:        authors,
		Categories:     append([]string(nil), q.Tags...),
		License:        stackExchangeNormalizeLicense(q.ContentLicense),
		SourceMetadata: meta,
	}
	return pub
}

// License-label constants. Stack Exchange has shipped four content_license
// codes over the years; the active one for new posts since 2018 is
// CC BY-SA 4.0. Keys (lower-cased + alt underscored form) map to a
// canonical human-readable label.
const (
	licenseLabelCCBYSA40 = "CC BY-SA 4.0"
	licenseLabelCCBYSA30 = "CC BY-SA 3.0"
	licenseLabelCCBYSA25 = "CC BY-SA 2.5"

	licenseRawCCBYSA40    = "cc by-sa 4.0"
	licenseRawCCBYSA40Alt = "cc_by_sa_4_0"
	licenseRawCCBYSA30    = "cc by-sa 3.0"
	licenseRawCCBYSA30Alt = "cc_by_sa_3_0"
	licenseRawCCBYSA25    = "cc by-sa 2.5"
	licenseRawCCBYSA25Alt = "cc_by_sa_2_5"
)

// stackExchangeNormalizeLicense maps the raw content_license code to a
// human-readable label.
func stackExchangeNormalizeLicense(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case licenseRawCCBYSA40, licenseRawCCBYSA40Alt:
		return licenseLabelCCBYSA40
	case licenseRawCCBYSA30, licenseRawCCBYSA30Alt:
		return licenseLabelCCBYSA30
	case licenseRawCCBYSA25, licenseRawCCBYSA25Alt:
		return licenseLabelCCBYSA25
	case "":
		return ""
	}
	return raw
}

// stackExchangeStripHTML removes HTML tags from a body and unescapes
// entities. Stack Exchange returns rendered HTML when filter=withbody;
// we keep things light here (no proper DOM parse) since the body is only
// used to build an Abstract / Snippet preview, never round-tripped back
// to a renderer.
func stackExchangeStripHTML(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	out := html.UnescapeString(b.String())
	// Collapse whitespace runs to keep abstracts compact.
	return strings.Join(strings.Fields(out), " ")
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *StackExchangePlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *StackExchangePlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
