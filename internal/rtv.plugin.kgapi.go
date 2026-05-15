package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Google Knowledge Graph Search API — v6 cycle 5 / v2.18.0.
//
// API: GET https://kgsearch.googleapis.com/v1/entities:search
//   Params:
//     query    free-text query
//     key      required — same Google Cloud project key works for KG and
//              Maps/Places (cycle 1) when both APIs are enabled
//     limit    1..500
//     types    Schema.org @type filter (e.g. "Person", "Place", "Book")
//     languages BCP-47 language hint
//     prefix   true to enable prefix-match (autocomplete style)
//
// Response (JSON-LD ItemList):
//   { "@context": {...}, "@type": "ItemList",
//     "itemListElement": [
//       { "@type": "EntitySearchResult",
//         "result": {
//           "@id":"kg:/m/0...",
//           "name":"Brandenburg Gate",
//           "@type":["Place","Thing"],
//           "description":"Monument in Berlin",
//           "image":{"contentUrl":"..."},
//           "detailedDescription":{
//             "articleBody":"...",
//             "url":"https://en.wikipedia.org/wiki/Brandenburg_Gate",
//             "license":"..."},
//           "url":"..." },
//         "resultScore": float } ] }
//
// Free with a Google Cloud API key; quota 100k requests/day per key.
// Per-call credential: `kgapi`.
// Residency: US (Google LLC).
// ---------------------------------------------------------------------------

const (
	kgapiPluginID          = SourceKGAPI
	kgapiPluginName        = "Google Knowledge Graph"
	kgapiPluginDescription = "Search the Google Knowledge Graph for structured entities (people, places, books, organizations). Free with a Google Cloud API key (100k/day quota). Returns entity name, description, image, Wikipedia article body, types. Per-call credential: kgapi. Same Google Cloud key can be shared with Google Places (cycle 1) when both APIs are enabled."

	kgapiDefaultBaseURL = "https://kgsearch.googleapis.com"
	kgapiSearchPath     = "/v1/entities:search"
	kgapiDefaultLimit   = 10
	kgapiMaxLimitCap    = 100
	kgapiDefaultRPS     = 5.0
	kgapiDefaultTimeout = 15 * time.Second

	kgapiIDPrefix = "kgapi:"

	kgapiParamQuery     = "query"
	kgapiParamKey       = "key"
	kgapiParamLimit     = "limit"
	kgapiParamTypes     = "types"
	kgapiParamLanguages = "languages"

	kgapiCategoriesHint = "Knowledge Graph Schema.org @type filter via filters.categories[*] — e.g. 'Person', 'Place', 'Book', 'Organization', 'Movie', 'MusicGroup'. Sent as comma-joined 'types' param."

	kgapiMetaKeyTypes      = "kgapi_types"
	kgapiMetaKeyKGID       = "kgapi_kgid"
	kgapiMetaKeyScore      = "kgapi_score"
	kgapiMetaKeyArticleURL = "kgapi_article_url"
)

// ---------------------------------------------------------------------------
// KG API wire types
// ---------------------------------------------------------------------------

type kgapiSearchResponse struct {
	ItemListElement []kgapiElement `json:"itemListElement,omitempty"`
}

type kgapiElement struct {
	Type        string      `json:"@type,omitempty"`
	Result      kgapiResult `json:"result,omitempty"`
	ResultScore float64     `json:"resultScore,omitempty"`
}

type kgapiResult struct {
	ID                  string             `json:"@id,omitempty"`
	Name                string             `json:"name,omitempty"`
	Types               []string           `json:"@type,omitempty"`
	Description         string             `json:"description,omitempty"`
	Image               *kgapiImage        `json:"image,omitempty"`
	DetailedDescription *kgapiDetailedDesc `json:"detailedDescription,omitempty"`
	URL                 string             `json:"url,omitempty"`
}

type kgapiImage struct {
	ContentURL string `json:"contentUrl,omitempty"`
}

type kgapiDetailedDesc struct {
	ArticleBody string `json:"articleBody,omitempty"`
	URL         string `json:"url,omitempty"`
	License     string `json:"license,omitempty"`
}

// ---------------------------------------------------------------------------
// KGAPIPlugin
// ---------------------------------------------------------------------------

// KGAPIPlugin implements SourcePlugin for the Google Knowledge Graph
// Search API. Thread-safe after Initialize.
type KGAPIPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "kgapi".
func (p *KGAPIPlugin) ID() string { return kgapiPluginID }

// Name returns the human-readable label.
func (p *KGAPIPlugin) Name() string { return kgapiPluginName }

// Description returns the LLM-facing one-liner.
func (p *KGAPIPlugin) Description() string { return kgapiPluginDescription }

// ContentTypes — paper (entities surface as paper-typed with KindFact).
func (p *KGAPIPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *KGAPIPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *KGAPIPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports KG API's filter/sort surface.
func (p *KGAPIPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true,
		SupportsPagination:       false,
		MaxResultsPerQuery:       kgapiMaxLimitCap,
		CategoriesHint:           kgapiCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentQuickLookup, IntentReference},
		Kinds:                    []ResultKind{KindFact},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *KGAPIPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = kgapiDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = kgapiDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = kgapiDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *KGAPIPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Knowledge Graph /v1/entities:search query.
func (p *KGAPIPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = kgapiDefaultLimit
	}
	if limit > kgapiMaxLimitCap {
		limit = kgapiMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceKGAPI, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: kgapi requires an API key", ErrCredentialRequired)
	}

	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.ItemListElement))
	for i := range resp.ItemListElement {
		pubs = append(pubs, kgapiElementToPublication(&resp.ItemListElement[i]))
	}
	return &SearchResult{
		Total:   len(pubs),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 5.
func (p *KGAPIPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: kgapi Get is not wired in cycle 5", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *KGAPIPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*kgapiSearchResponse, error) {
	q := url.Values{}
	q.Set(kgapiParamQuery, params.Query)
	q.Set(kgapiParamKey, apiKey)
	q.Set(kgapiParamLimit, strconv.Itoa(limit))

	if len(params.Filters.Categories) > 0 {
		clean := make([]string, 0, len(params.Filters.Categories))
		for _, c := range params.Filters.Categories {
			if v := strings.TrimSpace(c); v != "" {
				clean = append(clean, v)
			}
		}
		if len(clean) > 0 {
			q.Set(kgapiParamTypes, strings.Join(clean, ","))
		}
	}
	if lang := strings.TrimSpace(params.Filters.Language); lang != "" {
		q.Set(kgapiParamLanguages, lang)
	}

	reqURL := p.baseURL + kgapiSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("kgapi: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kgapi: http: %w", redactURLErr(err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: kgapi", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: kgapi", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("kgapi: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp kgapiSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("kgapi: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func kgapiElementToPublication(el *kgapiElement) Publication {
	r := el.Result

	// KG IDs are "kg:/m/abc123" — strip the leading "kg:" so the
	// retrievr id formats consistently with the other plugins.
	rawID := strings.TrimPrefix(r.ID, "kg:")

	title := strings.TrimSpace(r.Name)
	if title == "" {
		title = rawID
	}

	abstract := strings.TrimSpace(r.Description)
	if r.DetailedDescription != nil && r.DetailedDescription.ArticleBody != "" {
		if abstract != "" {
			abstract += "\n"
		}
		abstract += stripXMLTags(r.DetailedDescription.ArticleBody)
	}

	displayURL := r.URL
	if displayURL == "" && r.DetailedDescription != nil {
		displayURL = r.DetailedDescription.URL
	}

	thumb := ""
	if r.Image != nil {
		thumb = r.Image.ContentURL
	}

	meta := map[string]any{
		kgapiMetaKeyKGID: rawID,
	}
	if len(r.Types) > 0 {
		meta[kgapiMetaKeyTypes] = r.Types
	}
	if el.ResultScore != 0 {
		meta[kgapiMetaKeyScore] = el.ResultScore
	}
	if r.DetailedDescription != nil && r.DetailedDescription.URL != "" {
		meta[kgapiMetaKeyArticleURL] = r.DetailedDescription.URL
	}

	license := ""
	if r.DetailedDescription != nil {
		license = r.DetailedDescription.License
	}

	return Publication{
		ID:             kgapiIDPrefix + rawID,
		Source:         SourceKGAPI,
		ContentType:    ContentTypePaper,
		Title:          title,
		Abstract:       abstract,
		URL:            displayURL,
		Categories:     r.Types,
		License:        license,
		ThumbnailURL:   thumb,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *KGAPIPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *KGAPIPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = sanitizeHealthError(err)
	}
}
