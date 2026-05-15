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
// OpenAIRE OpenScience aggregator — v5 cycle 2 / v2.9.0.
//
// API: GET https://api.openaire.eu/graph/v1/researchProducts
//   Query params:
//     search       free-text query
//     page         1-indexed page
//     pageSize     results per page (max 100)
//     fromPublicationDate / toPublicationDate  YYYY-MM-DD bounds
//     sortBy       e.g. "publicationDate DESC"
//
// We use the OpenAIRE Graph API (v1) rather than the legacy
// /search/publications endpoint: same data, JSON-native, agent-friendly.
// The legacy endpoint wraps everything in OAI-XML semantics inside JSON,
// which adds zero value over the Graph API for the cross-source dedup
// the router already does on DOI.
//
// Response (relevant subset):
//   {
//     "header": { "numFound": int, "queryTime": int, "page": int, ... },
//     "results": [
//       {
//         "id": "openaire:dedup_xyz...",
//         "mainTitle": string,
//         "descriptions": [string],
//         "authors": [ { "fullName": string, "name": string, "surname": string,
//                        "pid": { "id": { "scheme": "orcid", "value": string } } } ],
//         "pids": [ { "scheme": "doi", "value": string } ],
//         "publicationDate": "YYYY-MM-DD",
//         "language": { "code": "eng", "label": "English" },
//         "bestAccessRight": { "code": "c_abf2", "label": "Open Access" },
//         "type": "publication" | "dataset" | "software" | "other",
//         "publisher": string,
//         "subjects": [ { "value": string } ]
//       }
//     ]
//   }
//
// Free with optional public-data token (passed via Authorization: Bearer).
// EU-funded research aggregator hosted by the OpenAIRE consortium (Greece).
//
// Residency: EU (Athena Research and Innovation Center).
// ---------------------------------------------------------------------------

const (
	openaire_PluginID          = SourceOpenAIRE
	openaire_PluginName        = "OpenAIRE"
	openaire_PluginDescription = "Search OpenAIRE (EU, Greece) — EU-funded research aggregator with publications, datasets, software, and project outputs across 50+ European countries. Returns DOIs for cross-source dedup. Free; optional public-data token via PluginConfig.APIKey or per-call credentials.openaire."

	openaire_DefaultBaseURL = "https://api.openaire.eu/graph/v1"
	openaire_SearchPath     = "/researchProducts"
	openaire_DefaultLimit   = 25
	openaire_MaxLimitCap    = 100
	openaire_DefaultRPS     = 2.0
	openaire_DefaultTimeout = 15 * time.Second

	openaire_IDPrefix = "openaire:"

	openaire_ParamSearch   = "search"
	openaire_ParamPage     = "page"
	openaire_ParamPageSize = "pageSize"
	openaire_ParamFromDate = "fromPublicationDate"
	openaire_ParamToDate   = "toPublicationDate"
	openaire_ParamSortBy   = "sortBy"

	openaire_SortPublishedDesc = "publicationDate DESC"
	openaire_SortPublishedAsc  = "publicationDate ASC"
	openaire_SortRelevance     = "relevance DESC"

	openaire_PIDSchemeDOI = "doi"

	openaire_CategoriesHint = "OpenAIRE 'type' field: publication, dataset, software, other. Subjects (keywords) returned per-record but not filterable at search time."

	openaire_MetaKeyType        = "openaire_type"
	openaire_MetaKeyAccessLabel = "openaire_access_label"
	openaire_MetaKeyPublisher   = "openaire_publisher"
	openaire_MetaKeySubjects    = "openaire_subjects"
)

// ---------------------------------------------------------------------------
// OpenAIRE wire types
// ---------------------------------------------------------------------------

type openaireSearchResponse struct {
	Header  openaireHeader   `json:"header"`
	Results []openaireResult `json:"results"`
}

type openaireHeader struct {
	NumFound int `json:"numFound"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}

type openaireResult struct {
	ID              string            `json:"id"`
	MainTitle       string            `json:"mainTitle,omitempty"`
	Descriptions    []string          `json:"descriptions,omitempty"`
	Authors         []openaireAuthor  `json:"authors,omitempty"`
	PIDs            []openairePID     `json:"pids,omitempty"`
	PublicationDate string            `json:"publicationDate,omitempty"`
	Language        openaireLabeled   `json:"language,omitempty"`
	BestAccessRight openaireLabeled   `json:"bestAccessRight,omitempty"`
	Type            string            `json:"type,omitempty"`
	Publisher       string            `json:"publisher,omitempty"`
	Subjects        []openaireSubject `json:"subjects,omitempty"`
}

type openaireAuthor struct {
	FullName string             `json:"fullName,omitempty"`
	Name     string             `json:"name,omitempty"`
	Surname  string             `json:"surname,omitempty"`
	PID      *openaireAuthorPID `json:"pid,omitempty"`
}

type openaireAuthorPID struct {
	ID openairePID `json:"id"`
}

type openairePID struct {
	Scheme string `json:"scheme,omitempty"`
	Value  string `json:"value,omitempty"`
}

type openaireLabeled struct {
	Code  string `json:"code,omitempty"`
	Label string `json:"label,omitempty"`
}

type openaireSubject struct {
	Value string `json:"value,omitempty"`
}

// ---------------------------------------------------------------------------
// OpenAIREPlugin
// ---------------------------------------------------------------------------

// OpenAIREPlugin implements SourcePlugin for the OpenAIRE Graph API.
// Thread-safe after Initialize.
type OpenAIREPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "openaire".
func (p *OpenAIREPlugin) ID() string { return openaire_PluginID }

// Name returns the human-readable label.
func (p *OpenAIREPlugin) Name() string { return openaire_PluginName }

// Description returns the LLM-facing one-liner.
func (p *OpenAIREPlugin) Description() string { return openaire_PluginDescription }

// ContentTypes — OpenAIRE catalogs papers, datasets, and software; we
// surface paper + dataset to align with the rest of the academic chain.
func (p *OpenAIREPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper, ContentTypeDataset}
}

// NativeFormat — JSON.
func (p *OpenAIREPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only; BibTeX assembled centrally.
func (p *OpenAIREPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// Capabilities reports OpenAIRE's filter/sort surface.
func (p *OpenAIREPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       openaire_MaxLimitCap,
		CategoriesHint:           openaire_CategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource, IntentReference},
		Kinds:                    []ResultKind{KindPaper, KindDataset},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *OpenAIREPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = openaire_DefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = openaire_DefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = openaire_DefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *OpenAIREPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes an OpenAIRE /researchProducts query.
func (p *OpenAIREPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = openaire_DefaultLimit
	}
	if limit > openaire_MaxLimitCap {
		limit = openaire_MaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceOpenAIRE, p.apiKey)
	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Results))
	for i := range resp.Results {
		pubs = append(pubs, openaireResultToPublication(&resp.Results[i]))
	}
	return &SearchResult{
		Total:   resp.Header.NumFound,
		Results: pubs,
		HasMore: resp.Header.NumFound > params.Offset+len(pubs),
	}, nil
}

// Get is not wired in cycle 2 — OpenAIRE IDs are opaque dedup-record
// identifiers (e.g. "openaire____::abc123") and DOI-based fetch lands
// via Crossref/Zenodo on the same DOI.
func (p *OpenAIREPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: openaire Get is not wired in cycle 2", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *OpenAIREPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*openaireSearchResponse, error) {
	q := url.Values{}
	q.Set(openaire_ParamSearch, params.Query)
	q.Set(openaire_ParamPageSize, strconv.Itoa(limit))
	page := 1
	if params.Offset > 0 && limit > 0 && params.Offset%limit == 0 {
		page = params.Offset/limit + 1
	}
	q.Set(openaire_ParamPage, strconv.Itoa(page))

	switch params.Sort {
	case SortDateDesc:
		q.Set(openaire_ParamSortBy, openaire_SortPublishedDesc)
	case SortDateAsc:
		q.Set(openaire_ParamSortBy, openaire_SortPublishedAsc)
	case SortRelevance:
		q.Set(openaire_ParamSortBy, openaire_SortRelevance)
	}

	if d := strings.TrimSpace(params.Filters.DateFrom); d != "" {
		q.Set(openaire_ParamFromDate, openaireNormalizeDate(d, true))
	}
	if d := strings.TrimSpace(params.Filters.DateTo); d != "" {
		q.Set(openaire_ParamToDate, openaireNormalizeDate(d, false))
	}

	reqURL := p.baseURL + openaire_SearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("openaire: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openaire: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: openaire", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: openaire", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("openaire: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp openaireSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("openaire: decode response: %w", err)
	}
	return &resp, nil
}

// openaireNormalizeDate accepts YYYY or YYYY-MM-DD and returns a
// YYYY-MM-DD value for OpenAIRE's date-range params. A bare year is
// expanded to Jan 1 (lower) or Dec 31 (upper).
func openaireNormalizeDate(in string, lowerBound bool) string {
	switch len(in) {
	case 4:
		if lowerBound {
			return in + "-01-01"
		}
		return in + "-12-31"
	case 10:
		return in
	}
	return in
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func openaireResultToPublication(r *openaireResult) Publication {
	rawID := r.ID

	// Authors: prefer fullName; fall back to "surname, name".
	authors := make([]Author, 0, len(r.Authors))
	for _, a := range r.Authors {
		name := strings.TrimSpace(a.FullName)
		if name == "" {
			parts := []string{}
			if a.Surname != "" {
				parts = append(parts, a.Surname)
			}
			if a.Name != "" {
				parts = append(parts, a.Name)
			}
			name = strings.Join(parts, ", ")
		}
		if name == "" {
			continue
		}
		orcid := ""
		if a.PID != nil && strings.EqualFold(a.PID.ID.Scheme, "orcid") {
			orcid = a.PID.ID.Value
		}
		authors = append(authors, Author{Name: name, ORCID: orcid})
	}

	// DOI from PIDs.
	doi := ""
	for _, p := range r.PIDs {
		if strings.EqualFold(p.Scheme, openaire_PIDSchemeDOI) && p.Value != "" {
			doi = strings.TrimSpace(p.Value)
			break
		}
	}

	abstract := ""
	if len(r.Descriptions) > 0 {
		abstract = stripXMLTags(r.Descriptions[0])
	}

	ct := ContentTypePaper
	switch strings.ToLower(r.Type) {
	case "dataset":
		ct = ContentTypeDataset
	}

	subjects := make([]string, 0, len(r.Subjects))
	for _, s := range r.Subjects {
		if s.Value != "" {
			subjects = append(subjects, s.Value)
		}
	}

	meta := map[string]any{}
	if r.Type != "" {
		meta[openaire_MetaKeyType] = r.Type
	}
	if r.BestAccessRight.Label != "" {
		meta[openaire_MetaKeyAccessLabel] = r.BestAccessRight.Label
	}
	if r.Publisher != "" {
		meta[openaire_MetaKeyPublisher] = r.Publisher
	}
	if len(subjects) > 0 {
		meta[openaire_MetaKeySubjects] = subjects
	}

	displayURL := ""
	if doi != "" {
		displayURL = "https://doi.org/" + doi
	}

	return Publication{
		ID:             openaire_IDPrefix + rawID,
		Source:         SourceOpenAIRE,
		ContentType:    ct,
		Title:          strings.TrimSpace(r.MainTitle),
		Abstract:       abstract,
		URL:            displayURL,
		DOI:            doi,
		Published:      r.PublicationDate,
		Authors:        authors,
		Categories:     subjects,
		Language:       r.Language.Code,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *OpenAIREPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *OpenAIREPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
