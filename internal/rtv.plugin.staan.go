package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Staan web search — v2.29.0.
//
// EU web search on a proprietary European index (European Search Perspective
// SAS, Paris). Expected to become the default web search provider because it
// is EU-hosted end to end and a DPA is in place.
//
// API (read 2026-10-18 from https://docs.staan.ai/introduction,
// https://docs.staan.ai/docs/web-search, https://docs.staan.ai/docs/web-for-ai,
// https://docs.staan.ai/api/search and https://docs.staan.ai/api/~schemas):
//
//   POST https://api.staan.ai/v2/search/web        (201 on success)
//   GET  https://api.staan.ai/v2/search/web?q=...  (200 on success)
//   Header: Authorization: Bearer <key>
//
//   Body (SearchWebRequestBody):
//     q               string, required, max 400 chars, accepts site:/-site:
//     market          enum, default fr-FR — documented values fr-fr, de-de,
//                     en-us, en-gb, en-fr, en-ca, en-au, en-in (+4 the docs
//                     collapse behind "show 4 more" and never list)
//     count           number, default 10 — "fixed at 10" per the guide
//     offset          number, default 0 — 0, 10, 20 or 30 (max 30)
//     extra_snippets  bool, default false — scored chunks, triggers rerank
//     full_content    "markdown" | "html" — full page body, triggers rerank
//     max_snippets    number 1–10, default 3
//     min_score       number 0–1, default 0.10
//     include_domains string[] max 10, POST only, exclusive with exclude
//     exclude_domains string[] max 10, POST only — extras DROPPED silently
//
//   Response (SearchWebResponse):
//     { search_id, query: {q, market, count, offset, altered_query?},
//       web: { results: [ { title, url, snippet, display_url, hostname,
//              favicon_url?, thumbnail?, published_date?,
//              extra_snippets?: [{chunk, score}],
//              full_content?: {text, format, length} } ] } }
//
// Rate limit: 20 requests/second (documented). Free tier: 1,000 requests a
// month; paid from €2 per 1,000 requests.
//
// ⚠ WHAT THE DOCS DO NOT SAY, AND HOW THIS PLUGIN DEFENDS AGAINST IT:
//   - No error status codes, error body, or quota/credit error is documented.
//     The live API answers an unauthenticated call with HTTP 401 and
//     {"message":"Unauthorized","statusCode":401} (observed 2026-10-18, not
//     documented). Mapping: 401/403 → credential_invalid; 402 → quota
//     exhausted; 429 → rate_limited UNLESS the body names a quota/credit/plan
//     limit, then quota_exhausted (permanent, not retried); any status whose
//     body names a quota/credit limit → quota_exhausted; 400/413/422 →
//     invalid_request; everything else → upstream_error.
//   - No freshness/date, language, country or safe-search parameter exists.
//     `market` is the only locale knob, so no news intent and no date filter.
//   - The 4 undocumented market values are not guessed: an unmapped language
//     falls back to the configured market.
//
// Residency: EU. Hosted on OVHcloud in France (privacy policy §3.4; FAQ
// "entire infrastructure is hosted in Europe with OVHcloud"). DPAStatus is
// signed — operator ruling 2026-10-19: full EU compliance, DPA in place;
// vaik8s#1331.
// ---------------------------------------------------------------------------

// Identity / config constants.
const (
	staanPluginID          = SourceStaan
	staanPluginName        = "Staan"
	staanPluginDescription = "EU web search on a proprietary European index (Staan / European Search Perspective, Paris; hosted on OVHcloud France). Paid; 1,000 free requests/month, then €2 per 1,000. 10 results per page, up to 4 pages. Per-call credential: staan."

	staanDefaultBaseURL = "https://api.staan.ai"
	staanSearchPath     = "/v2/search/web"
	staanAuthHeader     = "Authorization"
	staanAuthScheme     = "Bearer "
	staanContentType    = "application/json"

	staanPageSize      = 10   // documented: count is fixed at 10
	staanMaxOffset     = 30   // documented: offset 0, 10, 20, 30
	staanMaxQueryChars = 400  // documented: max query length
	staanMaxDomains    = 10   // documented: include/exclude max 10 each
	staanDefaultRPS    = 10.0 // documented ceiling is 20 req/s; stay at half
	staanDefaultMarket = "en-us"

	staanIDPrefix = "staan:"

	staanFullContentMarkdown = "markdown"
	staanFullContentHTML     = "html"

	staanCategoriesHint = "general web on an EU index; no category, date or freshness filter. Locale via `market` (fr-fr, de-de, en-us, en-gb, en-fr, en-ca, en-au, en-in); `language` picks the matching market when one is documented."

	// staanSubprocessorURL is where Staan lists its sub-processors (privacy
	// policy §3.4). Staan publishes no standalone DPA URL; the signed DPA is
	// held by the operator (ruling 2026-10-19, vaik8s#1331).
	staanSubprocessorURL = "https://staan.ai/privacy-policy"
)

// staanResidencyVerifiedAt is when Staan's residency posture was last
// verified: EU hosting read from its own documentation (FAQ + privacy policy)
// on 2026-10-18; the signed DPA confirmed by operator ruling 2026-10-19
// (vaik8s#1331).
var staanResidencyVerifiedAt = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

// SourceMetadata keys specific to Staan.
const (
	staanMetaDisplayURL    = "staan_display_url"
	staanMetaFaviconURL    = "staan_favicon_url"
	staanMetaExtraSnippets = "staan_extra_snippets"
	staanMetaSearchID      = "staan_search_id"
	staanMetaAlteredQuery  = "staan_altered_query"
	staanMetaMarket        = "staan_market"
	staanMetaChunk         = "chunk"
	staanMetaScore         = "score"
)

// Extra-key constants (PluginConfig.Extra).
const (
	staanExtraMarket        = "market"         // default market, e.g. de-de
	staanExtraExtraSnippets = "extra_snippets" // "true" | "false"
	staanExtraFullContent   = "full_content"   // "" | markdown | html
	staanExtraMaxSnippets   = "max_snippets"   // 1–10
	staanExtraMinScore      = "min_score"      // 0–1
)

// staanQuotaMarkers are lower-case substrings that, in an error body, mean a
// quota/credit/plan limit rather than a short-window throttle. The error
// vocabulary is undocumented, so this is deliberately broad.
var staanQuotaMarkers = []string{"quota", "credit", "monthly", "plan limit", "exhausted", "payment required"}

// staanMarkets maps a BCP-47 language (full tag first, then primary subtag)
// to a DOCUMENTED Staan market. Only documented values appear here.
var staanMarkets = map[string]string{
	"fr":    "fr-fr",
	"fr-fr": "fr-fr",
	"de":    "de-de",
	"de-de": "de-de",
	"en":    "en-us",
	"en-us": "en-us",
	"en-gb": "en-gb",
	"en-fr": "en-fr",
	"en-ca": "en-ca",
	"en-au": "en-au",
	"en-in": "en-in",
}

// ---------------------------------------------------------------------------
// Staan wire types
// ---------------------------------------------------------------------------

type staanSearchRequest struct {
	Q              string   `json:"q"`
	Market         string   `json:"market,omitempty"`
	Offset         int      `json:"offset,omitempty"`
	ExtraSnippets  bool     `json:"extra_snippets,omitempty"`
	FullContent    string   `json:"full_content,omitempty"`
	MaxSnippets    int      `json:"max_snippets,omitempty"`
	MinScore       *float64 `json:"min_score,omitempty"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
}

type staanSearchResponse struct {
	SearchID string        `json:"search_id"`
	Query    staanQuery    `json:"query"`
	Web      staanWebBlock `json:"web"`
}

type staanQuery struct {
	Q            string `json:"q"`
	AlteredQuery string `json:"altered_query,omitempty"`
	Market       string `json:"market"`
	Count        int    `json:"count"`
	Offset       int    `json:"offset"`
}

type staanWebBlock struct {
	Results []staanResult `json:"results"`
}

type staanResult struct {
	Title         string              `json:"title"`
	URL           string              `json:"url"`
	Snippet       string              `json:"snippet"`
	DisplayURL    string              `json:"display_url"`
	Hostname      string              `json:"hostname"`
	FaviconURL    string              `json:"favicon_url,omitempty"`
	Thumbnail     string              `json:"thumbnail,omitempty"`
	PublishedDate string              `json:"published_date,omitempty"`
	ExtraSnippets []staanChunk        `json:"extra_snippets,omitempty"`
	FullContent   *staanFullContentWS `json:"full_content,omitempty"`
}

type staanChunk struct {
	Chunk string  `json:"chunk"`
	Score float64 `json:"score"`
}

type staanFullContentWS struct {
	Text   string `json:"text"`
	Format string `json:"format"`
	Length int    `json:"length"`
}

// ---------------------------------------------------------------------------
// StaanPlugin
// ---------------------------------------------------------------------------

// StaanPlugin implements SourcePlugin for the Staan web search API.
// Thread-safe for concurrent use after Initialize.
type StaanPlugin struct {
	baseURL       string
	apiKey        string
	market        string
	extraSnippets bool
	fullContent   string
	maxSnippets   int
	minScore      *float64
	httpClient    *http.Client
	enabled       bool
	rateLimit     float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "staan".
func (p *StaanPlugin) ID() string { return staanPluginID }

// Name returns the human-readable label.
func (p *StaanPlugin) Name() string { return staanPluginName }

// Description returns a one-liner for LLM tool listing.
func (p *StaanPlugin) Description() string { return staanPluginDescription }

// ContentTypes — general web.
func (p *StaanPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypeAny} }

// NativeFormat — JSON.
func (p *StaanPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON + markdown.
func (p *StaanPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatMarkdown}
}

// Capabilities reports Staan's documented surface. No date/freshness filter
// exists, so there is no news intent. SupportsFullText is false because page
// bodies are opt-in per deployment (extra.full_content), not per call.
func (p *StaanPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     true,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true, // via market (documented markets only)
		SupportsPagination:       true, // offset 0–30 in pages of 10
		MaxResultsPerQuery:       staanPageSize,
		CategoriesHint:           staanCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatMarkdown},
		QueryIntents:             []Intent{IntentQuickLookup, IntentReference, IntentDeepResearch},
		Kinds:                    []ResultKind{KindWeb},
		RequiresCredential:       true,
	}
}

// Residency — EU (OVHcloud France), DPA signed. Operator ruling 2026-10-19:
// full EU compliance, DPA in place; vaik8s#1331.
func (*StaanPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:          RegionEU,
		DPAStatus:       DPASigned,
		SubprocessorURL: staanSubprocessorURL,
		LastVerifiedAt:  staanResidencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *StaanPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = staanDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = staanDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.market = strings.ToLower(strings.TrimSpace(stringFromExtra(cfg.Extra, staanExtraMarket, staanDefaultMarket)))
	p.extraSnippets = strings.EqualFold(stringFromExtra(cfg.Extra, staanExtraExtraSnippets, "false"), "true")

	switch fc := strings.ToLower(strings.TrimSpace(stringFromExtra(cfg.Extra, staanExtraFullContent, ""))); fc {
	case "", staanFullContentMarkdown, staanFullContentHTML:
		p.fullContent = fc
	default:
		return fmt.Errorf("%w: staan extra.full_content must be markdown or html, got %q", ErrInvalidInput, fc)
	}

	if raw := stringFromExtra(cfg.Extra, staanExtraMaxSnippets, ""); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 10 {
			return fmt.Errorf("%w: staan extra.max_snippets must be 1-10, got %q", ErrInvalidInput, raw)
		}
		p.maxSnippets = n
	}
	if raw := stringFromExtra(cfg.Extra, staanExtraMinScore, ""); raw != "" {
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil || f < 0 || f > 1 {
			return fmt.Errorf("%w: staan extra.min_score must be 0-1, got %q", ErrInvalidInput, raw)
		}
		p.minScore = &f
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
func (p *StaanPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes one Staan page. Staan pages are fixed at 10 results at
// offsets 0/10/20/30; a caller offset that is not a multiple of 10 is served
// from the enclosing page with the leading rows dropped, and an offset past
// the last page (39) returns an empty, final result.
func (p *StaanPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, staanPluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: staan requires an API key", ErrCredentialRequired)
	}

	body, skip, err := p.buildRequest(params)
	if err != nil {
		return nil, err
	}
	if body == nil {
		return &SearchResult{Results: []Publication{}}, nil
	}

	resp, err := p.doSearch(ctx, *body, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	raw := resp.Web.Results
	pageFull := len(raw) >= staanPageSize
	if skip >= len(raw) {
		raw = nil
	} else {
		raw = raw[skip:]
	}

	pubs := make([]Publication, 0, len(raw))
	for i := range raw {
		if strings.TrimSpace(raw[i].URL) == "" {
			continue
		}
		pubs = append(pubs, staanResultToPublication(&raw[i], resp))
	}

	if params.Limit > 0 && len(pubs) > params.Limit {
		pubs = pubs[:params.Limit]
	}

	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: pageFull && body.Offset+staanPageSize <= staanMaxOffset,
	}, nil
}

// Get is not supported — Staan has no fetch-by-id endpoint (full_content is
// a search-time enrichment keyed by query, not by result).
func (p *StaanPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, NewGetUnsupportedError("staan has no per-result Get API")
}

// buildRequest validates params against the documented limits and returns
// the wire body plus the number of leading rows to drop. A nil body means
// the offset is past the last page.
func (p *StaanPlugin) buildRequest(params SearchParams) (*staanSearchRequest, int, error) {
	q := strings.TrimSpace(params.Query)
	if q == "" {
		return nil, 0, fmt.Errorf("%w: staan: empty query", ErrInvalidInput)
	}
	if n := len([]rune(q)); n > staanMaxQueryChars {
		return nil, 0, fmt.Errorf("%w: staan: query is %d characters, max %d", ErrInvalidInput, n, staanMaxQueryChars)
	}

	inc, exc := params.Filters.IncludeDomains, params.Filters.ExcludeDomains
	if err := ValidateDomainList(inc); err != nil {
		return nil, 0, fmt.Errorf("staan: include_domains: %w", err)
	}
	if err := ValidateDomainList(exc); err != nil {
		return nil, 0, fmt.Errorf("staan: exclude_domains: %w", err)
	}
	if len(inc) > 0 && len(exc) > 0 {
		return nil, 0, fmt.Errorf("%w: staan: include_domains and exclude_domains are mutually exclusive", ErrInvalidDomainList)
	}
	// Staan silently DROPS domains past 10; refuse rather than search a
	// narrower set than the caller asked for.
	if len(inc) > staanMaxDomains || len(exc) > staanMaxDomains {
		return nil, 0, fmt.Errorf("%w: staan accepts at most %d domains", ErrInvalidDomainList, staanMaxDomains)
	}
	if err := ValidateLanguageTag(params.Filters.Language); err != nil {
		return nil, 0, fmt.Errorf("staan: language: %w", err)
	}

	offset := params.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= staanMaxOffset+staanPageSize {
		return nil, 0, nil
	}
	pageOffset := (offset / staanPageSize) * staanPageSize

	body := &staanSearchRequest{
		Q:              q,
		Market:         p.resolveMarket(params.Filters.Language),
		Offset:         pageOffset,
		ExtraSnippets:  p.extraSnippets,
		FullContent:    p.fullContent,
		IncludeDomains: inc,
		ExcludeDomains: exc,
	}
	if p.extraSnippets {
		body.MaxSnippets = p.maxSnippets
		body.MinScore = p.minScore
	}
	return body, offset - pageOffset, nil
}

// resolveMarket picks the market for a BCP-47 language: full tag first, then
// primary subtag, then the configured default. Only documented markets are
// ever produced from a language.
func (p *StaanPlugin) resolveMarket(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == "" {
		return p.market
	}
	if m, ok := staanMarkets[l]; ok {
		return m
	}
	if i := strings.IndexAny(l, "-_"); i > 0 {
		if m, ok := staanMarkets[l[:i]]; ok {
			return m
		}
	}
	return p.market
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *StaanPlugin) doSearch(ctx context.Context, body staanSearchRequest, apiKey string) (*staanSearchResponse, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("staan: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+staanSearchPath, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("staan: build request: %w", err)
	}
	req.Header.Set(staanAuthHeader, staanAuthScheme+apiKey)
	req.Header.Set("Content-Type", staanContentType)
	req.Header.Set("Accept", staanContentType)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("staan: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		return nil, staanStatusError(httpResp.StatusCode, string(buf))
	}

	var resp staanSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("staan: decode response: %w", err)
	}
	return &resp, nil
}

// staanStatusError maps a non-2xx answer onto the typed error vocabulary.
// Staan documents no error codes; see the file header for the rationale.
func staanStatusError(status int, body string) error {
	if staanBodyNamesQuota(body) || status == http.StatusPaymentRequired {
		return &QuotaExhaustedError{Source: staanPluginID, Reason: fmt.Sprintf("http_%d", status)}
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: staan returned %d", ErrCredentialInvalid, status)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: staan", ErrRateLimitExceeded)
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: staan returned %d: %s", ErrInvalidInput, status, truncateForError(body))
	}
	return fmt.Errorf("staan: status=%d body=%s", status, truncateForError(body))
}

func staanBodyNamesQuota(body string) bool {
	b := strings.ToLower(body)
	for _, m := range staanQuotaMarkers {
		if strings.Contains(b, m) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Result mapping
// ---------------------------------------------------------------------------

func staanResultToPublication(r *staanResult, resp *staanSearchResponse) Publication {
	pub := Publication{
		ID:           staanIDPrefix + hashURL(r.URL),
		Source:       SourceStaan,
		ContentType:  ContentTypeAny,
		Title:        strings.TrimSpace(r.Title),
		URL:          r.URL,
		Abstract:     strings.TrimSpace(r.Snippet),
		ThumbnailURL: r.Thumbnail,
	}

	meta := map[string]any{}
	if pub.Abstract != "" {
		meta[smetaSnippet] = truncateSnippet(pub.Abstract)
	}
	if r.Hostname != "" {
		meta[smetaDomain] = r.Hostname
		meta[smetaSiteName] = r.Hostname
	}
	if r.PublishedDate != "" {
		meta[smetaPublishedAt] = r.PublishedDate
		if len(r.PublishedDate) >= 10 {
			pub.Published = r.PublishedDate[:10]
		}
	}
	if r.DisplayURL != "" {
		meta[staanMetaDisplayURL] = r.DisplayURL
	}
	if r.FaviconURL != "" {
		meta[staanMetaFaviconURL] = r.FaviconURL
	}
	if len(r.ExtraSnippets) > 0 {
		chunks := make([]map[string]any, 0, len(r.ExtraSnippets))
		for _, c := range r.ExtraSnippets {
			chunks = append(chunks, map[string]any{staanMetaChunk: c.Chunk, staanMetaScore: c.Score})
		}
		meta[staanMetaExtraSnippets] = chunks
	}
	if resp != nil {
		if resp.SearchID != "" {
			meta[staanMetaSearchID] = resp.SearchID
		}
		if resp.Query.AlteredQuery != "" {
			meta[staanMetaAlteredQuery] = resp.Query.AlteredQuery
		}
		if resp.Query.Market != "" {
			meta[staanMetaMarket] = resp.Query.Market
		}
	}
	// Documented: "Always check length > 0 before using text" — a failed
	// page fetch returns text "" and length 0.
	if fc := r.FullContent; fc != nil && fc.Length > 0 && fc.Text != "" {
		format := FormatNative // html has no ContentFormat of its own
		if fc.Format == staanFullContentMarkdown {
			format = FormatMarkdown
		}
		pub.FullText = &FullTextContent{
			Content:       fc.Text,
			ContentFormat: format,
			ContentLength: len(fc.Text),
		}
	}
	pub.SourceMetadata = meta
	return pub
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *StaanPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *StaanPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = sanitizeHealthError(err)
	}
}
