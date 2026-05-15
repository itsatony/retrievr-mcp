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
// Dimensions.ai scholarly provider — v6 cycle 3 / v2.16.0.
//
// Auth: two-step.
//   1. POST /api/auth  (body: {"key":"<api-key>"})
//      → {"token":"<bearer-token>"}
//   2. Subsequent /api/dsl requests carry  Authorization: <bearer-token>
//      (note: Dimensions does NOT use the literal "Bearer " prefix).
//
// Search: POST /api/dsl
//   Content-Type: text/plain
//   Body: raw DSL query, e.g.
//     search publications for "neural network"
//     return publications [id+title+doi+authors+year+abstract+times_cited+journal]
//     limit 25
//
// Response (subset):
//   { "_stats": {"total_count": int},
//     "publications": [
//       { "id":"pub.1...",
//         "title":"...",
//         "doi":"10.x/y",
//         "authors":[{"first_name","last_name","corresponding"}],
//         "year": int,
//         "abstract":"...",
//         "times_cited": int,
//         "journal":{"id":"jour.123","title":"Nature"},
//         "publisher":"Springer Nature" } ] }
//
// Free for academic users; paid commercial. Per-call credential:
// `dimensions`. Refuses to start without a key.
// Residency: US/AU mixed (Digital Science). Covered-by-SCC.
// ---------------------------------------------------------------------------

const (
	dimensionsPluginID          = SourceDimensions
	dimensionsPluginName        = "Dimensions.ai"
	dimensionsPluginDescription = "Search Dimensions.ai (app.dimensions.ai) scholarly database via DSL. Free for academic users; paid commercial. Requires API key (per-call credentials.dimensions). Returns publications with DOI, authors, citation count, journal — covers the citation-graph depth not available in OA-only sources."

	dimensionsDefaultBaseURL = "https://app.dimensions.ai"
	dimensionsAuthPath       = "/api/auth"
	dimensionsDSLPath        = "/api/dsl"
	dimensionsDefaultLimit   = 25
	dimensionsMaxLimitCap    = 100
	dimensionsDefaultRPS     = 2.0
	dimensionsDefaultTimeout = 30 * time.Second
	dimensionsTokenLifetime  = 25 * time.Minute // Dimensions tokens nominally last 30m

	dimensionsIDPrefix = "dimensions:"

	dimensionsHeaderAuth     = "Authorization"
	dimensionsContentType    = "text/plain"
	dimensionsCategoriesHint = "Dimensions DSL category facets: filters.categories[*] are folded into a 'where category_for.name in [...]' clause. Pass codes/labels matching the Dimensions categorization vocabulary (FOR / SDG / HRCS)."

	dimensionsMetaKeyJournal    = "dimensions_journal"
	dimensionsMetaKeyPublisher  = "dimensions_publisher"
	dimensionsMetaKeyTimesCited = "dimensions_times_cited"
)

// ---------------------------------------------------------------------------
// Dimensions wire types
// ---------------------------------------------------------------------------

type dimensionsAuthRequest struct {
	Key string `json:"key"`
}

type dimensionsAuthResponse struct {
	Token string `json:"token"`
}

type dimensionsSearchResponse struct {
	Stats        dimensionsStats         `json:"_stats,omitempty"`
	Publications []dimensionsPublication `json:"publications,omitempty"`
}

type dimensionsStats struct {
	TotalCount int `json:"total_count,omitempty"`
}

type dimensionsPublication struct {
	ID         string             `json:"id,omitempty"`
	Title      string             `json:"title,omitempty"`
	DOI        string             `json:"doi,omitempty"`
	Authors    []dimensionsAuthor `json:"authors,omitempty"`
	Year       int                `json:"year,omitempty"`
	Abstract   string             `json:"abstract,omitempty"`
	TimesCited int                `json:"times_cited,omitempty"`
	Journal    *dimensionsJournal `json:"journal,omitempty"`
	Publisher  string             `json:"publisher,omitempty"`
}

type dimensionsAuthor struct {
	FirstName     string `json:"first_name,omitempty"`
	LastName      string `json:"last_name,omitempty"`
	Corresponding bool   `json:"corresponding,omitempty"`
	ORCID         string `json:"orcid,omitempty"`
}

type dimensionsJournal struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
}

// ---------------------------------------------------------------------------
// DimensionsPlugin
// ---------------------------------------------------------------------------

// DimensionsPlugin implements SourcePlugin for the Dimensions.ai DSL
// search API. Thread-safe after Initialize; token state guarded by mu.
type DimensionsPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu          sync.RWMutex
	healthy     bool
	lastError   string
	accessToken string
	tokenExpiry time.Time
}

// ID returns "dimensions".
func (p *DimensionsPlugin) ID() string { return dimensionsPluginID }

// Name returns the human-readable label.
func (p *DimensionsPlugin) Name() string { return dimensionsPluginName }

// Description returns the LLM-facing one-liner.
func (p *DimensionsPlugin) Description() string { return dimensionsPluginDescription }

// ContentTypes — paper.
func (p *DimensionsPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *DimensionsPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON + BibTeX (assembled centrally).
func (p *DimensionsPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// Capabilities reports Dimensions' filter/sort surface.
func (p *DimensionsPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        true,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    true,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       dimensionsMaxLimitCap,
		CategoriesHint:           dimensionsCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource},
		Kinds:                    []ResultKind{KindPaper},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *DimensionsPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = dimensionsDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = dimensionsDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = dimensionsDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *DimensionsPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Dimensions DSL query.
func (p *DimensionsPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = dimensionsDefaultLimit
	}
	if limit > dimensionsMaxLimitCap {
		limit = dimensionsMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceDimensions, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: dimensions requires an API key", ErrCredentialRequired)
	}

	token, err := p.ensureToken(ctx, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}

	resp, err := p.doSearch(ctx, params, limit, token)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Publications))
	for i := range resp.Publications {
		pubs = append(pubs, dimensionsPubToPublication(&resp.Publications[i]))
	}
	return &SearchResult{
		Total:   resp.Stats.TotalCount,
		Results: pubs,
		HasMore: resp.Stats.TotalCount > params.Offset+len(pubs),
	}, nil
}

// Get is not wired in cycle 3 — single-publication DSL lookup via
// `search publications where id="pub.X" return publications` is a
// trivial future-cycle addition.
func (p *DimensionsPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: dimensions Get is not wired in cycle 3", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// Auth + HTTP transport
// ---------------------------------------------------------------------------

// ensureToken returns a cached bearer token if still valid, otherwise
// requests a fresh one from /api/auth using the configured key.
func (p *DimensionsPlugin) ensureToken(ctx context.Context, apiKey string) (string, error) {
	p.mu.RLock()
	cached := p.accessToken
	expiry := p.tokenExpiry
	p.mu.RUnlock()
	if cached != "" && time.Now().Before(expiry) {
		return cached, nil
	}

	body, err := json.Marshal(dimensionsAuthRequest{Key: apiKey})
	if err != nil {
		return "", fmt.Errorf("dimensions: encode auth body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+dimensionsAuthPath, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("dimensions: build auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dimensions: auth http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%w: dimensions", ErrCredentialInvalid)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return "", fmt.Errorf("dimensions: auth status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var ar dimensionsAuthResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&ar); err != nil {
		return "", fmt.Errorf("dimensions: decode auth: %w", err)
	}
	if ar.Token == "" {
		return "", fmt.Errorf("%w: dimensions empty token", ErrCredentialInvalid)
	}

	p.mu.Lock()
	p.accessToken = ar.Token
	p.tokenExpiry = time.Now().Add(dimensionsTokenLifetime)
	p.mu.Unlock()
	return ar.Token, nil
}

func (p *DimensionsPlugin) doSearch(ctx context.Context, params SearchParams, limit int, token string) (*dimensionsSearchResponse, error) {
	dsl := dimensionsBuildDSL(params, limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+dimensionsDSLPath, strings.NewReader(dsl))
	if err != nil {
		return nil, fmt.Errorf("dimensions: build dsl request: %w", err)
	}
	req.Header.Set("Content-Type", dimensionsContentType)
	req.Header.Set("Accept", "application/json")
	// Dimensions wants the bare token, not "Bearer <token>".
	req.Header.Set(dimensionsHeaderAuth, token)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dimensions: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: dimensions", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		// Force a re-auth on next call.
		p.mu.Lock()
		p.accessToken = ""
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: dimensions", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("dimensions: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp dimensionsSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("dimensions: decode response: %w", err)
	}
	return &resp, nil
}

// dimensionsBuildDSL synthesizes a DSL query string from SearchParams.
// We escape embedded double-quotes in the user query to keep the DSL
// well-formed; any other DSL injection vectors are out-of-band since
// the API key is per-tenant.
func dimensionsBuildDSL(params SearchParams, limit int) string {
	q := strings.ReplaceAll(strings.TrimSpace(params.Query), `"`, `\"`)

	var b strings.Builder
	b.WriteString(`search publications for "`)
	b.WriteString(q)
	b.WriteString(`"`)

	// WHERE clause for year + category filters.
	whereParts := []string{}
	if y := yearFromDateString(params.Filters.DateFrom); y != 0 {
		whereParts = append(whereParts, "year>="+strconv.Itoa(y))
	}
	if y := yearFromDateString(params.Filters.DateTo); y != 0 {
		whereParts = append(whereParts, "year<="+strconv.Itoa(y))
	}
	if len(params.Filters.Categories) > 0 {
		labels := []string{}
		for _, c := range params.Filters.Categories {
			if v := strings.TrimSpace(c); v != "" {
				labels = append(labels, `"`+strings.ReplaceAll(v, `"`, `\"`)+`"`)
			}
		}
		if len(labels) > 0 {
			whereParts = append(whereParts, "category_for.name in ["+strings.Join(labels, ",")+"]")
		}
	}
	if len(whereParts) > 0 {
		b.WriteString(" where ")
		b.WriteString(strings.Join(whereParts, " and "))
	}

	b.WriteString(` return publications[id+title+doi+authors+year+abstract+times_cited+journal+publisher]`)

	switch params.Sort {
	case SortDateDesc:
		b.WriteString(" sort by year desc")
	case SortDateAsc:
		b.WriteString(" sort by year asc")
	case SortCitations:
		b.WriteString(" sort by times_cited desc")
	}

	b.WriteString(" limit ")
	b.WriteString(strconv.Itoa(limit))
	if params.Offset > 0 {
		b.WriteString(" skip ")
		b.WriteString(strconv.Itoa(params.Offset))
	}
	return b.String()
}

// yearFromDateString extracts the YYYY prefix from a YYYY or YYYY-MM-DD
// string; returns 0 if neither form parses.
func yearFromDateString(s string) int {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return 0
	}
	y, err := strconv.Atoi(s[:4])
	if err != nil || y < 1000 {
		return 0
	}
	return y
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func dimensionsPubToPublication(pub *dimensionsPublication) Publication {
	authors := make([]Author, 0, len(pub.Authors))
	for _, a := range pub.Authors {
		name := strings.TrimSpace(strings.TrimSpace(a.LastName + ", " + a.FirstName))
		name = strings.TrimSuffix(strings.TrimPrefix(name, ", "), ", ")
		if name == "" {
			continue
		}
		authors = append(authors, Author{Name: name, ORCID: a.ORCID})
	}

	published := ""
	if pub.Year > 0 {
		published = strconv.Itoa(pub.Year)
	}

	citation := pub.TimesCited
	var citPtr *int
	if citation != 0 {
		citPtr = &citation
	}

	meta := map[string]any{}
	if pub.Journal != nil && pub.Journal.Title != "" {
		meta[dimensionsMetaKeyJournal] = pub.Journal.Title
	}
	if pub.Publisher != "" {
		meta[dimensionsMetaKeyPublisher] = pub.Publisher
	}
	if pub.TimesCited != 0 {
		meta[dimensionsMetaKeyTimesCited] = pub.TimesCited
	}

	displayURL := ""
	if pub.DOI != "" {
		displayURL = "https://doi.org/" + pub.DOI
	} else {
		displayURL = "https://app.dimensions.ai/details/publication/" + pub.ID
	}

	return Publication{
		ID:             dimensionsIDPrefix + pub.ID,
		Source:         SourceDimensions,
		ContentType:    ContentTypePaper,
		Title:          strings.TrimSpace(pub.Title),
		Abstract:       stripXMLTags(pub.Abstract),
		URL:            displayURL,
		DOI:            pub.DOI,
		Published:      published,
		Authors:        authors,
		CitationCount:  citPtr,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *DimensionsPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *DimensionsPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
