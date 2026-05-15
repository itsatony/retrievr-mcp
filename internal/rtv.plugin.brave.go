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
// Brave Search web/news provider — Cycle 2 Wave-1.
//
// API: https://api.search.brave.com/res/v1/web/search
//   Method: GET
//   Headers: X-Subscription-Token: <key>, Accept: application/json
//   Query params: q, count, country, search_lang, ui_lang, freshness, ...
//   Response: { type, web: { results: [...] }, news: { results: [...] }, ... }
//
// Residency: US (Brave Software is US-based; index is global). Blocked under
// eu_strict; admissible in eu_preferred and off.
//
// Free tier soft cap: 1 req/s. Operators with a Pro subscription bump
// sources.brave.rate_limit.
// ---------------------------------------------------------------------------

// Identity / config constants.
const (
	bravePluginID          = SourceBrave
	bravePluginName        = "Brave Search"
	bravePluginDescription = "Independent 35B+ page web index from Brave Software. Strong privacy posture, fast (median <1s). US-resident; blocked under eu_strict."

	braveDefaultBaseURL  = "https://api.search.brave.com"
	braveSearchPath      = "/res/v1/web/search"
	braveImageSearchPath = "/res/v1/images/search" // v3 cycle 4 / v2.5.0
	braveAuthHeader      = "X-Subscription-Token"
	braveAcceptHeader    = "Accept"
	braveAcceptJSON      = "application/json"
	braveAcceptEncHeader = "Accept-Encoding"
	braveAcceptEncIdent  = "identity" // disable gzip — Go's http.Client decodes opaquely otherwise

	braveDefaultCount = 10
	braveMaxCount     = 20 // Brave caps web search at 20 per request
	braveDefaultRPS   = 1.0

	braveCategoriesHint = "general web, news, blogs"
)

// Extra-key constants (PluginConfig.Extra).
const (
	braveExtraCountry    = "country"     // ISO 3166 alpha-2; default ALL
	braveExtraSearchLang = "search_lang" // ISO 639-1; default not set
	braveExtraSafesearch = "safesearch"  // off | moderate | strict
)

// Outbound query-param name constants. Extracted in v2.7.0 from prior
// string-literal usage to comply with the "no magic strings" code rule.
//
// Note: Brave Search Web API does NOT expose dedicated `include_domains` /
// `exclude_domains` query params (those were a v2.7.0 mis-spec validated
// against live API in v2.7.1). Instead, domain scoping is performed via
// inline `site:` operators in the query string itself — the same SERP
// syntax Google/DuckDuckGo accept. The original constants are removed
// because they were dead code.
const (
	braveParamQ          = "q"
	braveParamCount      = "count"
	braveParamOffset     = "offset"
	braveParamCountry    = "country"
	braveParamSearchLang = "search_lang"
	braveParamSafesearch = "safesearch"
	braveParamFreshness  = "freshness"
)

// Inline SERP operators Brave honors inside the q parameter.
const (
	braveQueryOperatorSite        = "site:"
	braveQueryOperatorExcludeSite = "-site:"
	braveQueryOperatorOR          = "OR"
)

// Freshness bucket constants — Brave's documented relative-time tokens.
const (
	braveFreshnessDay   = "pd"
	braveFreshnessWeek  = "pw"
	braveFreshnessMonth = "pm"
	braveFreshnessYear  = "py"
)

// Age thresholds used to map a DateFrom value to the nearest freshness
// bucket. The cut-offs match Brave's documented bucket semantics: results
// produced inside the last 24h, 7d, 31d, or 365d respectively.
const (
	braveAgeDay   = 24 * time.Hour
	braveAgeWeek  = 7 * 24 * time.Hour
	braveAgeMonth = 31 * 24 * time.Hour
	braveAgeYear  = 365 * 24 * time.Hour
)

// Brave custom-range syntax for the freshness param: two ISO dates joined
// by the literal separator below. Documented in the Brave Search API
// reference. Less battle-tested than the bucket tokens; on a 422 response
// the doSearch path retries once with the nearest bucket derived from
// DateFrom (see OQ-4 in retrievr_v4.md).
const (
	braveFreshnessRangeSep    = "to"
	braveFilterDateLayout     = "2006-01-02" // mirrors time.DateOnly; named for intent
	braveFilterYearOnlyLayout = "2006"
)

// ---------------------------------------------------------------------------
// Brave wire types
// ---------------------------------------------------------------------------

type braveSearchResponse struct {
	Type string            `json:"type"`
	Web  *braveWebSection  `json:"web,omitempty"`
	News *braveNewsSection `json:"news,omitempty"`
}

type braveWebSection struct {
	Results []braveResult `json:"results"`
}

type braveNewsSection struct {
	Results []braveResult `json:"results"`
}

type braveResult struct {
	Type          string        `json:"type"`
	Subtype       string        `json:"subtype,omitempty"`
	Title         string        `json:"title"`
	URL           string        `json:"url"`
	Description   string        `json:"description,omitempty"`
	PageAge       string        `json:"page_age,omitempty"`
	Language      string        `json:"language,omitempty"`
	ExtraSnippets []string      `json:"extra_snippets,omitempty"`
	MetaURL       *braveMetaURL `json:"meta_url,omitempty"`
	Profile       *braveProfile `json:"profile,omitempty"`
}

type braveMetaURL struct {
	Hostname string `json:"hostname,omitempty"`
	Favicon  string `json:"favicon,omitempty"`
	Path     string `json:"path,omitempty"`
}

type braveProfile struct {
	Name     string `json:"name,omitempty"`
	LongName string `json:"long_name,omitempty"`
	URL      string `json:"url,omitempty"`
}

// ---------------------------------------------------------------------------
// BravePlugin
// ---------------------------------------------------------------------------

// BravePlugin implements SourcePlugin for Brave Search.
// Thread-safe for concurrent use after Initialize.
type BravePlugin struct {
	baseURL        string
	apiKey         string
	defaultCountry string
	searchLang     string
	safesearch     string
	httpClient     *http.Client
	enabled        bool
	rateLimit      float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "brave".
func (p *BravePlugin) ID() string { return bravePluginID }

// Name returns the human-readable label.
func (p *BravePlugin) Name() string { return bravePluginName }

// Description returns a one-liner for LLM tool listing.
func (p *BravePlugin) Description() string { return bravePluginDescription }

// ContentTypes — Brave covers general web + image SERP (v3 cycle 4 added
// the images endpoint). The web/news path is the default; image dispatch
// happens when params.ContentType == ContentTypeImage.
func (p *BravePlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeAny, ContentTypeImage}
}

// NativeFormat — JSON.
func (p *BravePlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *BravePlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatMarkdown}
}

// Capabilities reports Brave-specific filtering + sorting support.
//
// v2.7.0: SupportsDateFilter is now real (freshness bucket + custom range);
// SupportsDomainFilter (include_domains / exclude_domains, comma-joined);
// SupportsLanguageFilter (search_lang, first BCP-47 subtag).
func (p *BravePlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     true,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true,
		SupportsPagination:       true, // via offset+count
		MaxResultsPerQuery:       braveMaxCount,
		CategoriesHint:           braveCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatMarkdown},
		QueryIntents:             []Intent{IntentQuickLookup, IntentNews},
		Kinds:                    []ResultKind{KindWeb, KindNews, KindImage},
	}
}

// Residency — US-resident.
func (*BravePlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize wires the plugin from PluginConfig. Uses NewEgressClient
// (Hook #4) for outbound HTTP hygiene.
func (p *BravePlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = braveDefaultRPS
	}
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = braveDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	p.defaultCountry = stringFromExtra(cfg.Extra, braveExtraCountry, "")
	p.searchLang = stringFromExtra(cfg.Extra, braveExtraSearchLang, "")
	p.safesearch = stringFromExtra(cfg.Extra, braveExtraSafesearch, "moderate")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *BravePlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Brave web/news OR image search depending on
// params.ContentType. ContentTypeImage dispatches to the images endpoint;
// everything else falls through to the web+news endpoint.
// Credentials read from ctx.
func (p *BravePlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	apiKey := CredentialFor(ctx, bravePluginID, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: brave requires an API key", ErrCredentialRequired)
	}

	count := params.Limit
	if count <= 0 {
		count = braveDefaultCount
	}
	if count > braveMaxCount {
		count = braveMaxCount
	}

	if err := ValidateDomainList(params.Filters.IncludeDomains); err != nil {
		return nil, fmt.Errorf("brave: include_domains: %w", err)
	}
	if err := ValidateDomainList(params.Filters.ExcludeDomains); err != nil {
		return nil, fmt.Errorf("brave: exclude_domains: %w", err)
	}
	if err := ValidateLanguageTag(params.Filters.Language); err != nil {
		return nil, fmt.Errorf("brave: language: %w", err)
	}

	if params.ContentType == ContentTypeImage {
		return p.searchImages(ctx, params, count, apiKey)
	}

	resp, err := p.doSearch(ctx, params, count, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := p.mergeBraveSections(resp)
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: len(pubs) >= count, // Brave doesn't surface a total; assume more on full page
	}, nil
}

// ---------------------------------------------------------------------------
// Brave image search (v3 cycle 4 / v2.5.0)
// ---------------------------------------------------------------------------

type braveImageSearchResponse struct {
	Type    string             `json:"type"`
	Results []braveImageResult `json:"results,omitempty"`
}

type braveImageResult struct {
	Type        string               `json:"type"`
	Title       string               `json:"title"`
	URL         string               `json:"url"` // page hosting the image
	Source      string               `json:"source,omitempty"`
	PageFetched string               `json:"page_fetched,omitempty"`
	Thumbnail   braveImageThumb      `json:"thumbnail,omitempty"`
	Properties  braveImageProperties `json:"properties,omitempty"`
	MetaURL     *braveMetaURL        `json:"meta_url,omitempty"`
	Confidence  string               `json:"confidence,omitempty"`
}

type braveImageThumb struct {
	Src      string `json:"src,omitempty"`
	Original string `json:"original,omitempty"`
}

type braveImageProperties struct {
	URL    string `json:"url,omitempty"` // direct media URL
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	Format string `json:"format,omitempty"`
}

// searchImages hits /res/v1/images/search and maps results to Publication.
func (p *BravePlugin) searchImages(ctx context.Context, params SearchParams, count int, apiKey string) (*SearchResult, error) {
	resp, err := p.doImageSearch(ctx, params, count, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Results))
	for _, r := range resp.Results {
		pub := braveImageResultToPublication(r)
		if pub.MediaURL == "" {
			continue
		}
		pubs = append(pubs, pub)
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: len(pubs) >= count,
	}, nil
}

func (p *BravePlugin) doImageSearch(ctx context.Context, params SearchParams, count int, apiKey string) (*braveImageSearchResponse, error) {
	q := url.Values{}
	q.Set(braveParamQ, params.Query)
	q.Set(braveParamCount, fmt.Sprintf("%d", count))
	if p.defaultCountry != "" {
		q.Set(braveParamCountry, p.defaultCountry)
	}
	if lang := p.resolveSearchLang(params.Filters.Language); lang != "" {
		q.Set(braveParamSearchLang, lang)
	}
	if p.safesearch != "" {
		q.Set(braveParamSafesearch, p.safesearch)
	}

	reqURL := p.baseURL + braveImageSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("brave_image: build request: %w", err)
	}
	req.Header.Set(braveAuthHeader, apiKey)
	req.Header.Set(braveAcceptHeader, braveAcceptJSON)
	req.Header.Set(braveAcceptEncHeader, braveAcceptEncIdent)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave_image: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: brave images returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: brave images", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("brave_image: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp braveImageSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("brave_image: decode response: %w", err)
	}
	return &resp, nil
}

// braveImageResultToPublication maps one Brave image result to a Publication.
// Brave doesn't surface license info on the images SERP — License is left
// empty so downstream consumers know the legal status is unverified.
func braveImageResultToPublication(r braveImageResult) Publication {
	mediaURL := r.Properties.URL
	if mediaURL == "" {
		mediaURL = r.Thumbnail.Original
	}
	thumb := r.Thumbnail.Src
	if thumb == "" {
		thumb = r.Thumbnail.Original
	}

	pub := Publication{
		ID:           fmt.Sprintf("%s:image:%s", SourceBrave, hashURL(mediaURL)),
		Source:       SourceBrave,
		ContentType:  ContentTypeImage,
		Title:        r.Title,
		URL:          r.URL, // hosting page, not the image
		ThumbnailURL: thumb,
		MediaURL:     mediaURL,
		MediaMime:    braveImageFormatToMime(r.Properties.Format),
		SourceMetadata: map[string]any{
			smetaSourcePage: r.URL,
			smetaWidth:      r.Properties.Width,
			smetaHeight:     r.Properties.Height,
		},
	}
	if r.Source != "" {
		pub.SourceMetadata[smetaSiteName] = r.Source
	}
	return pub
}

// braveImageFormatToMime maps Brave's `format` string to a MIME type.
func braveImageFormatToMime(format string) string {
	switch strings.ToLower(format) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	}
	return ""
}

// Get is not supported — Brave Search has no per-result-ID retrieval API.
func (p *BravePlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: brave has no per-result Get API", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

// resolveSearchLang returns the per-call language (first BCP-47 subtag) if
// set, otherwise the operator-configured default. Empty string means "do not
// send the search_lang param".
func (p *BravePlugin) resolveSearchLang(filterLang string) string {
	if filterLang != "" {
		return BCP47FirstSubtag(filterLang)
	}
	return p.searchLang
}

// braveFreshnessFromDate maps a SearchFilters date range to Brave's
// freshness parameter:
//   - DateFrom + DateTo set: custom range "YYYY-MM-DDtoYYYY-MM-DD". Both
//     endpoints MUST parse as a calendar date — year-only or malformed
//     inputs return "" and the caller proceeds without a freshness param
//     (Brave would reject e.g. "2026to2026-05-15" with 422 anyway).
//   - DateFrom only: nearest bucket (pd/pw/pm/py) based on age vs `now`.
//     Future-dated `DateFrom` (negative age) returns "". Older than
//     braveAgeYear returns "" (Brave has no "older than 1y" bucket).
//   - Neither set: returns "".
//
// Invalid date strings return "" and no error — the caller chose to omit
// the filter; producing an error here would break searches whose date hint
// is malformed at the source.
func braveFreshnessFromDate(filters SearchFilters, now time.Time) string {
	if filters.DateFrom == "" && filters.DateTo == "" {
		return ""
	}
	if filters.DateFrom != "" && filters.DateTo != "" {
		// Both endpoints must be full calendar dates — Brave's custom range
		// syntax does not accept year-only segments. Reject mismatched
		// granularity by returning "" so the bucket path can still try.
		fromDate, okFrom := parseFilterDateCalendar(filters.DateFrom)
		toDate, okTo := parseFilterDateCalendar(filters.DateTo)
		if !okFrom || !okTo {
			return ""
		}
		return fromDate + braveFreshnessRangeSep + toDate
	}
	if filters.DateFrom == "" {
		return ""
	}
	from, ok := parseFilterDateStart(filters.DateFrom)
	if !ok {
		return ""
	}
	age := now.Sub(from)
	if age < 0 {
		return "" // future date — no meaningful "past N" bucket
	}
	switch {
	case age <= braveAgeDay:
		return braveFreshnessDay
	case age <= braveAgeWeek:
		return braveFreshnessWeek
	case age <= braveAgeMonth:
		return braveFreshnessMonth
	case age <= braveAgeYear:
		return braveFreshnessYear
	}
	return ""
}

// parseFilterDateStart parses a filter date ("YYYY-MM-DD" or "YYYY") as the
// start-of-day in UTC. Returns false on any parse error.
func parseFilterDateStart(s string) (time.Time, bool) {
	if len(s) == 4 {
		t, err := time.Parse(braveFilterYearOnlyLayout, s)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	}
	t, err := time.Parse(braveFilterDateLayout, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// parseFilterDateCalendar requires a full YYYY-MM-DD calendar date.
// Year-only inputs are rejected (returns "", false). Returns the canonical
// "YYYY-MM-DD" string on success so the custom-range syntax is always
// well-formed regardless of accidental input quirks.
func parseFilterDateCalendar(s string) (string, bool) {
	t, err := time.Parse(braveFilterDateLayout, s)
	if err != nil {
		return "", false
	}
	return t.Format(braveFilterDateLayout), true
}

func (p *BravePlugin) buildSearchQuery(params SearchParams, count int, freshness string) url.Values {
	q := url.Values{}
	q.Set(braveParamQ, braveComposeQuery(params.Query, params.Filters.IncludeDomains, params.Filters.ExcludeDomains))
	q.Set(braveParamCount, fmt.Sprintf("%d", count))
	if params.Offset > 0 {
		q.Set(braveParamOffset, fmt.Sprintf("%d", params.Offset))
	}
	if p.defaultCountry != "" {
		q.Set(braveParamCountry, p.defaultCountry)
	}
	if lang := p.resolveSearchLang(params.Filters.Language); lang != "" {
		q.Set(braveParamSearchLang, lang)
	}
	if p.safesearch != "" {
		q.Set(braveParamSafesearch, p.safesearch)
	}
	if freshness != "" {
		q.Set(braveParamFreshness, freshness)
	}
	return q
}

// braveComposeQuery appends inline `site:` and `-site:` SERP operators
// to the user's query so Brave restricts results to / excludes the named
// domains. Multiple include domains are OR-ed (parenthesized) per
// Brave's documented boolean syntax. This is the only mechanism Brave
// Search Web API exposes for domain scoping — there is no
// `include_domains` request parameter, despite older retrievr code
// assuming one.
func braveComposeQuery(query string, includeDomains, excludeDomains []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(query))
	if len(includeDomains) == 1 {
		b.WriteByte(' ')
		b.WriteString(braveQueryOperatorSite)
		b.WriteString(includeDomains[0])
	} else if len(includeDomains) > 1 {
		parts := make([]string, 0, len(includeDomains))
		for _, d := range includeDomains {
			parts = append(parts, braveQueryOperatorSite+d)
		}
		b.WriteString(" (")
		b.WriteString(strings.Join(parts, " "+braveQueryOperatorOR+" "))
		b.WriteByte(')')
	}
	for _, d := range excludeDomains {
		b.WriteByte(' ')
		b.WriteString(braveQueryOperatorExcludeSite)
		b.WriteString(d)
	}
	return b.String()
}

func (p *BravePlugin) doSearch(ctx context.Context, params SearchParams, count int, apiKey string) (*braveSearchResponse, error) {
	freshness := braveFreshnessFromDate(params.Filters, time.Now())
	resp, status, err := p.executeSearch(ctx, p.buildSearchQuery(params, count, freshness), apiKey)
	if err == nil {
		return resp, nil
	}
	// Custom-range freshness syntax is less battle-tested than bucket tokens;
	// when Brave rejects with 422 AND we sent a range AND DateFrom is set,
	// retry once with the nearest bucket derived from DateFrom alone
	// (retrievr_v4.md OQ-4). All three predicates must hold: a plain bucket
	// failure or a 422 from a different cause (bad q, bad lang) must not
	// rewrite the freshness param and resubmit.
	if isBraveRangeRetryable(status, freshness, params.Filters) {
		bucket := braveFreshnessFromDate(SearchFilters{DateFrom: params.Filters.DateFrom}, time.Now())
		if bucket != "" && bucket != freshness {
			resp, _, err = p.executeSearch(ctx, p.buildSearchQuery(params, count, bucket), apiKey)
			if err == nil {
				return resp, nil
			}
		}
	}
	return nil, err
}

// isBraveRangeRetryable reports whether the doSearch path should retry a
// 422 by collapsing a custom-range freshness to its closest bucket. Three
// predicates must all hold so we never retry an unrelated 422.
func isBraveRangeRetryable(status int, freshness string, filters SearchFilters) bool {
	if status != http.StatusUnprocessableEntity {
		return false
	}
	if filters.DateFrom == "" {
		return false
	}
	return strings.Contains(freshness, braveFreshnessRangeSep) && strings.Count(freshness, "-") >= 4
}

// executeSearch performs a single HTTP request against the web/news
// endpoint. Returns the decoded body, the HTTP status (for retry decisions),
// and any error.
func (p *BravePlugin) executeSearch(ctx context.Context, q url.Values, apiKey string) (*braveSearchResponse, int, error) {
	reqURL := p.baseURL + braveSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("brave: build request: %w", err)
	}
	req.Header.Set(braveAuthHeader, apiKey)
	req.Header.Set(braveAcceptHeader, braveAcceptJSON)
	req.Header.Set(braveAcceptEncHeader, braveAcceptEncIdent)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("brave: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return nil, httpResp.StatusCode, fmt.Errorf("%w: brave returned %d", ErrCredentialInvalid, httpResp.StatusCode)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, httpResp.StatusCode, fmt.Errorf("%w: brave", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, httpResp.StatusCode, fmt.Errorf("brave: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp braveSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, httpResp.StatusCode, fmt.Errorf("brave: decode response: %w", err)
	}
	return &resp, httpResp.StatusCode, nil
}

// mergeBraveSections collects web + news results into a single Publication
// slice, tagging news entries with smetaKindOverride so the converter
// produces Result.Kind=KindNews while web entries default to KindWeb (the
// first entry of Capabilities().Kinds).
func (p *BravePlugin) mergeBraveSections(resp *braveSearchResponse) []Publication {
	if resp == nil {
		return nil
	}
	var out []Publication
	if resp.Web != nil {
		for _, r := range resp.Web.Results {
			out = append(out, braveResultToPublication(r, KindWeb))
		}
	}
	if resp.News != nil {
		for _, r := range resp.News.Results {
			out = append(out, braveResultToPublication(r, KindNews))
		}
	}
	return out
}

// braveResultToPublication maps one Brave result into a Publication.
// Snippet, domain, language, page_age, favicon are stuffed into
// SourceMetadata for the converter to unpack into Result.Web / Result.News.
func braveResultToPublication(r braveResult, kind ResultKind) Publication {
	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceBrave, hashURL(r.URL)),
		Source:      SourceBrave,
		ContentType: ContentTypeAny,
		Title:       r.Title,
		Abstract:    r.Description,
		URL:         r.URL,
	}

	meta := map[string]any{}
	if string(kind) != string(KindWeb) {
		meta[smetaKindOverride] = string(kind)
	}
	if r.Description != "" {
		meta[smetaSnippet] = truncateSnippet(r.Description)
	}
	if r.Language != "" {
		meta[smetaLanguage] = r.Language
	}
	if r.PageAge != "" {
		meta[smetaPublishedAt] = r.PageAge
	}
	if r.MetaURL != nil {
		if r.MetaURL.Hostname != "" {
			meta[smetaDomain] = r.MetaURL.Hostname
			meta[smetaSiteName] = r.MetaURL.Hostname
		}
	}
	// extra_snippets concatenated — gives the LLM more context per result.
	if len(r.ExtraSnippets) > 0 {
		extras := strings.Join(r.ExtraSnippets, " · ")
		if pub.Abstract == "" {
			pub.Abstract = extras
		} else {
			pub.Abstract = pub.Abstract + "\n\n" + extras
		}
	}
	if len(meta) > 0 {
		pub.SourceMetadata = meta
	}
	return pub
}

// hashURL returns a short stable identifier for a URL — used as the per-
// result ID since Brave doesn't surface stable IDs of its own. We re-use
// the audit hashing helper (sha256:16) for consistency.
func hashURL(u string) string {
	if u == "" {
		return "noid"
	}
	return hashQuery(u)
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *BravePlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *BravePlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
