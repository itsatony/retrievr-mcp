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
// Wikidata structured-knowledge provider — v5 cycle 3 / v2.10.0.
//
// API: GET https://www.wikidata.org/w/api.php
//   Action: wbsearchentities (entity lookup by free-text label).
//   Params:
//     action    "wbsearchentities"
//     search    free-text query (label, alias, or partial match)
//     language  BCP-47 short code, e.g. "en", "de"
//     format    "json"
//     type      "item" (default) — could also be "property" or "lexeme"
//     limit     1..50 (Wikidata's hard cap on this action)
//     continue  offset for pagination
//
// Response shape (relevant fields):
//   {
//     "searchinfo": { "search": "Brandenburg Gate" },
//     "search": [
//       {
//         "id": "Q82425",
//         "pageid": int,
//         "concepturi": "http://www.wikidata.org/entity/Q82425",
//         "url": "//www.wikidata.org/wiki/Q82425",
//         "label": "Brandenburg Gate",
//         "description": "neoclassical triumphal arch in Berlin, Germany",
//         "match": { "type": "label", "language": "en", "text": "..." },
//         "aliases": [ "BG", "Brandenburger Tor" ]
//       }
//     ],
//     "success": 1
//   }
//
// SPARQL endpoint (https://query.wikidata.org/sparql) is OUT of scope for
// the cycle-3 ship. The text-path is the 95% case for agents; SPARQL is a
// power-user feature deferred to a follow-up cycle so this plugin's
// integration tests stay deterministic and short.
//
// Residency: public-research-infrastructure (Wikimedia Foundation,
// non-profit, US-incorporated but globally mirrored).
// ---------------------------------------------------------------------------

const (
	wikidataPluginID          = SourceWikidata
	wikidataPluginName        = "Wikidata"
	wikidataPluginDescription = "Search Wikidata for structured-fact entities (QIDs, labels, descriptions, aliases). Uses the wbsearchentities text-lookup endpoint by default — power users can pass raw SPARQL via filters.extra['sparql'] in a future cycle. Free; throttled to ~5s response window. Public-research-infrastructure."

	wikidataDefaultBaseURL = "https://www.wikidata.org"
	wikidataAPIPath        = "/w/api.php"
	wikidataDefaultLimit   = 10
	wikidataMaxLimitCap    = 50
	wikidataDefaultRPS     = 2.0
	wikidataDefaultTimeout = 10 * time.Second

	wikidataIDPrefix        = "wikidata:"
	wikidataDefaultLanguage = "en"

	wikidataParamAction   = "action"
	wikidataParamSearch   = "search"
	wikidataParamLanguage = "language"
	wikidataParamFormat   = "format"
	wikidataParamType     = "type"
	wikidataParamLimit    = "limit"
	wikidataParamContinue = "continue"

	wikidataActionSearch = "wbsearchentities"
	wikidataFormatJSON   = "json"
	wikidataTypeItem     = "item"

	wikidataCategoriesHint = "Wikidata entity types: item (default), property (P-codes), lexeme. Only 'item' is exposed in cycle 3; properties + lexemes reserved for future."

	wikidataMetaKeyQID         = "wikidata_qid"
	wikidataMetaKeyAliases     = "wikidata_aliases"
	wikidataMetaKeyDescription = "wikidata_description"
	wikidataMetaKeyConceptURI  = "wikidata_concept_uri"
)

// ---------------------------------------------------------------------------
// Wikidata wire types
// ---------------------------------------------------------------------------

type wikidataSearchResponse struct {
	SearchInfo wikidataSearchInfo    `json:"searchinfo,omitempty"`
	Search     []wikidataSearchHit   `json:"search,omitempty"`
	Success    int                   `json:"success,omitempty"`
	SearchCont *wikidataContinueInfo `json:"search-continue,omitempty"`
}

type wikidataSearchInfo struct {
	Search string `json:"search,omitempty"`
}

type wikidataContinueInfo struct {
	Continue int `json:"continue,omitempty"`
}

type wikidataSearchHit struct {
	ID          string   `json:"id"`
	PageID      int      `json:"pageid,omitempty"`
	ConceptURI  string   `json:"concepturi,omitempty"`
	URL         string   `json:"url,omitempty"`
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

// ---------------------------------------------------------------------------
// WikidataPlugin
// ---------------------------------------------------------------------------

// WikidataPlugin implements SourcePlugin for the Wikidata wbsearchentities
// endpoint. Thread-safe after Initialize.
type WikidataPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "wikidata".
func (p *WikidataPlugin) ID() string { return wikidataPluginID }

// Name returns the human-readable label.
func (p *WikidataPlugin) Name() string { return wikidataPluginName }

// Description returns the LLM-facing one-liner.
func (p *WikidataPlugin) Description() string { return wikidataPluginDescription }

// ContentTypes — Wikidata surfaces structured items as papers (KindFact
// at the v2 layer); we keep ContentTypePaper to align with the existing
// router family.
func (p *WikidataPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat — JSON.
func (p *WikidataPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *WikidataPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports Wikidata's filter/sort surface.
func (p *WikidataPlugin) Capabilities() SourceCapabilities {
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
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true,
		SupportsPagination:       true,
		MaxResultsPerQuery:       wikidataMaxLimitCap,
		CategoriesHint:           wikidataCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentReference, IntentQuickLookup},
		Kinds:                    []ResultKind{KindFact},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *WikidataPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = wikidataDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = wikidataDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = wikidataDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *WikidataPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a Wikidata wbsearchentities query.
func (p *WikidataPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = wikidataDefaultLimit
	}
	if limit > wikidataMaxLimitCap {
		limit = wikidataMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Search))
	for i := range resp.Search {
		pubs = append(pubs, wikidataHitToPublication(&resp.Search[i]))
	}
	return &SearchResult{
		Total:   len(pubs), // wbsearchentities doesn't return a total — surface the page count
		Results: pubs,
		HasMore: resp.SearchCont != nil,
	}, nil
}

// Get is not wired in cycle 3 — QIDs can be fetched via wbgetentities in
// a future cycle when the SPARQL path lands.
func (p *WikidataPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: wikidata Get is not wired in cycle 3", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *WikidataPlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*wikidataSearchResponse, error) {
	q := url.Values{}
	q.Set(wikidataParamAction, wikidataActionSearch)
	q.Set(wikidataParamSearch, params.Query)
	q.Set(wikidataParamFormat, wikidataFormatJSON)
	q.Set(wikidataParamType, wikidataTypeItem)
	q.Set(wikidataParamLimit, strconv.Itoa(limit))

	lang := strings.TrimSpace(params.Filters.Language)
	if lang == "" {
		lang = wikidataDefaultLanguage
	}
	q.Set(wikidataParamLanguage, lang)

	if params.Offset > 0 {
		q.Set(wikidataParamContinue, strconv.Itoa(params.Offset))
	}

	reqURL := p.baseURL + wikidataAPIPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("wikidata: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wikidata: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: wikidata", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("wikidata: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp wikidataSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("wikidata: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func wikidataHitToPublication(hit *wikidataSearchHit) Publication {
	displayURL := hit.URL
	if displayURL != "" && strings.HasPrefix(displayURL, "//") {
		displayURL = "https:" + displayURL
	}
	if displayURL == "" {
		displayURL = "https://www.wikidata.org/wiki/" + hit.ID
	}

	title := strings.TrimSpace(hit.Label)
	if title == "" {
		title = hit.ID
	}

	meta := map[string]any{
		wikidataMetaKeyQID: hit.ID,
	}
	if hit.Description != "" {
		meta[wikidataMetaKeyDescription] = hit.Description
	}
	if len(hit.Aliases) > 0 {
		meta[wikidataMetaKeyAliases] = hit.Aliases
	}
	if hit.ConceptURI != "" {
		meta[wikidataMetaKeyConceptURI] = hit.ConceptURI
	}

	return Publication{
		ID:             wikidataIDPrefix + hit.ID,
		Source:         SourceWikidata,
		ContentType:    ContentTypePaper,
		Title:          title,
		Abstract:       hit.Description,
		URL:            displayURL,
		Categories:     hit.Aliases,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *WikidataPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *WikidataPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
