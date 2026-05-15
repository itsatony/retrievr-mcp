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
// DataCite structured-knowledge provider — v5 cycle 3 / v2.10.0.
//
// API: GET https://api.datacite.org/dois?query=<q>
//   Params:
//     query             free-text query (Lucene tolerated; full-text search
//                       across title, abstract, creator, etc.)
//     page[size]        1..1000 (default 25)
//     page[number]      1-indexed pagination
//     sort              "-created" | "created" | "title" | "-relevance" | ...
//     resource-type-id  filter on type (e.g. "dataset", "publication-article")
//     registered        date range: "2020-01-01,2024-12-31"
//
// Response (JSON:API):
//   {
//     "data": [
//       {
//         "id": "10.x/y",
//         "type": "dois",
//         "attributes": {
//           "doi": "10.x/y",
//           "titles": [{"title": "..."}],
//           "creators": [
//             { "name": "Doe, Jane", "affiliation": [{"name":"University X"}],
//               "nameIdentifiers": [{"nameIdentifier":"0000-...","nameIdentifierScheme":"ORCID"}] }
//           ],
//           "publisher": "Zenodo" | "...",
//           "publicationYear": int,
//           "types": { "resourceType": "Dataset",
//                      "resourceTypeGeneral": "Dataset" },
//           "descriptions": [
//             { "description": "...", "descriptionType": "Abstract" }
//           ],
//           "registered": "YYYY-MM-DDThh:mm:ssZ",
//           "url": "https://..."
//         }
//       }
//     ],
//     "meta": { "total": int, "totalPages": int, "page": int }
//   }
//
// Free, no auth required. Specialized in dataset / research-output DOIs.
//
// Residency: EU (DataCite e.V., Hannover DE).
// ---------------------------------------------------------------------------

const (
	dataciteePluginID          = SourceDataCite
	dataciteePluginName        = "DataCite"
	dataciteePluginDescription = "Search DataCite (api.datacite.org) — DOI registry specialized in research datasets, software, and outputs. Complements CrossRef's publication-DOI focus. Free, no auth required. EU-hosted (Hannover, DE)."

	dataciteDefaultBaseURL = "https://api.datacite.org"
	dataciteSearchPath     = "/dois"
	dataciteDefaultLimit   = 25
	dataciteMaxLimitCap    = 100
	dataciteDefaultRPS     = 5.0
	dataciteDefaultTimeout = 15 * time.Second

	dataciteIDPrefix = "datacite:"

	dataciteParamQuery       = "query"
	dataciteParamPageSize    = "page[size]"
	dataciteParamPageNumber  = "page[number]"
	dataciteParamSort        = "sort"
	dataciteParamRegistered  = "registered"
	dataciteParamResourceTID = "resource-type-id"

	dataciteSortCreatedDesc   = "-created"
	dataciteSortCreatedAsc    = "created"
	dataciteSortRelevanceDesc = "-relevance"

	dataciteCategoriesHint = "DataCite resourceTypeGeneral values: Dataset, Software, Text, Image, Audiovisual, Collection, Event, InteractiveResource, Model, PhysicalObject, Service, Sound, Workflow, Other. Use filters.categories[0] to filter on a single resource-type-id."

	dataciteMetaKeyResourceTypeGeneral = "datacite_resource_type_general"
	dataciteMetaKeyResourceType        = "datacite_resource_type"
	dataciteMetaKeyPublisher           = "datacite_publisher"
	dataciteMetaKeyRegistered          = "datacite_registered"
)

// ---------------------------------------------------------------------------
// DataCite wire types
// ---------------------------------------------------------------------------

type dataciteSearchResponse struct {
	Data []dataciteRecord `json:"data"`
	Meta dataciteMeta     `json:"meta"`
}

type dataciteMeta struct {
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
	Page       int `json:"page"`
}

type dataciteRecord struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Attributes dataciteAttributes `json:"attributes"`
}

type dataciteAttributes struct {
	DOI             string                `json:"doi,omitempty"`
	Titles          []dataciteTitle       `json:"titles,omitempty"`
	Creators        []dataciteCreator     `json:"creators,omitempty"`
	Publisher       string                `json:"publisher,omitempty"`
	PublicationYear int                   `json:"publicationYear,omitempty"`
	Types           dataciteTypes         `json:"types,omitempty"`
	Descriptions    []dataciteDescription `json:"descriptions,omitempty"`
	Registered      string                `json:"registered,omitempty"`
	URL             string                `json:"url,omitempty"`
	Language        string                `json:"language,omitempty"`
}

type dataciteTitle struct {
	Title string `json:"title,omitempty"`
}

type dataciteCreator struct {
	Name            string                   `json:"name,omitempty"`
	Affiliation     []dataciteAffiliation    `json:"affiliation,omitempty"`
	NameIdentifiers []dataciteNameIdentifier `json:"nameIdentifiers,omitempty"`
}

type dataciteAffiliation struct {
	Name string `json:"name,omitempty"`
}

type dataciteNameIdentifier struct {
	NameIdentifier       string `json:"nameIdentifier,omitempty"`
	NameIdentifierScheme string `json:"nameIdentifierScheme,omitempty"`
}

type dataciteTypes struct {
	ResourceType        string `json:"resourceType,omitempty"`
	ResourceTypeGeneral string `json:"resourceTypeGeneral,omitempty"`
}

type dataciteDescription struct {
	Description     string `json:"description,omitempty"`
	DescriptionType string `json:"descriptionType,omitempty"`
}

// ---------------------------------------------------------------------------
// DataCitePlugin
// ---------------------------------------------------------------------------

// DataCitePlugin implements SourcePlugin for the DataCite DOI registry.
// Thread-safe after Initialize.
type DataCitePlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "datacite".
func (p *DataCitePlugin) ID() string { return dataciteePluginID }

// Name returns the human-readable label.
func (p *DataCitePlugin) Name() string { return dataciteePluginName }

// Description returns the LLM-facing one-liner.
func (p *DataCitePlugin) Description() string { return dataciteePluginDescription }

// ContentTypes — DataCite is dataset-first, but also covers publications,
// software, models. We surface dataset + paper so the existing router
// routes correctly.
func (p *DataCitePlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeDataset, ContentTypePaper}
}

// NativeFormat — JSON.
func (p *DataCitePlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON + BibTeX (assembled centrally).
func (p *DataCitePlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// Capabilities reports DataCite's filter/sort surface.
func (p *DataCitePlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       dataciteMaxLimitCap,
		CategoriesHint:           dataciteCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource, IntentReference},
		Kinds:                    []ResultKind{KindDataset, KindPaper},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *DataCitePlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = dataciteDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = dataciteDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = dataciteDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *DataCitePlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a DataCite /dois query.
func (p *DataCitePlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = dataciteDefaultLimit
	}
	if limit > dataciteMaxLimitCap {
		limit = dataciteMaxLimitCap
	}

	resp, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.Data))
	for i := range resp.Data {
		pubs = append(pubs, dataciteRecordToPublication(&resp.Data[i]))
	}
	return &SearchResult{
		Total:   resp.Meta.Total,
		Results: pubs,
		HasMore: resp.Meta.Total > params.Offset+len(pubs),
	}, nil
}

// Get retrieves a single DOI record. The id is the DOI itself (prefix
// already stripped).
func (p *DataCitePlugin) Get(ctx context.Context, id string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	reqURL := p.baseURL + dataciteSearchPath + "/" + url.PathEscape(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("datacite: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.api+json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("datacite: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: datacite doi %s", ErrGetFailed, id)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("datacite: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var envelope struct {
		Data dataciteRecord `json:"data"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&envelope); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("datacite: decode response: %w", err)
	}
	p.recordSuccess()
	pub := dataciteRecordToPublication(&envelope.Data)
	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *DataCitePlugin) doSearch(ctx context.Context, params SearchParams, limit int) (*dataciteSearchResponse, error) {
	q := url.Values{}
	q.Set(dataciteParamQuery, params.Query)
	q.Set(dataciteParamPageSize, strconv.Itoa(limit))

	page := 1
	if params.Offset > 0 && limit > 0 && params.Offset%limit == 0 {
		page = params.Offset/limit + 1
	}
	q.Set(dataciteParamPageNumber, strconv.Itoa(page))

	switch params.Sort {
	case SortDateDesc:
		q.Set(dataciteParamSort, dataciteSortCreatedDesc)
	case SortDateAsc:
		q.Set(dataciteParamSort, dataciteSortCreatedAsc)
	case SortRelevance:
		q.Set(dataciteParamSort, dataciteSortRelevanceDesc)
	}

	if len(params.Filters.Categories) > 0 {
		if rt := strings.TrimSpace(params.Filters.Categories[0]); rt != "" {
			q.Set(dataciteParamResourceTID, strings.ToLower(rt))
		}
	}

	from := strings.TrimSpace(params.Filters.DateFrom)
	to := strings.TrimSpace(params.Filters.DateTo)
	if from != "" || to != "" {
		q.Set(dataciteParamRegistered, dataciteRegisteredRange(from, to))
	}

	reqURL := p.baseURL + dataciteSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("datacite: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.api+json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("datacite: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: datacite", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("datacite: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp dataciteSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("datacite: decode response: %w", err)
	}
	return &resp, nil
}

// dataciteRegisteredRange folds DateFrom/DateTo into the comma-separated
// range syntax DataCite uses for its `registered` param. Bare years are
// expanded to Jan 1 / Dec 31 just like Zenodo/OpenAIRE.
func dataciteRegisteredRange(from, to string) string {
	if from == "" {
		from = "*"
	} else {
		from = openaireNormalizeDate(from, true)
	}
	if to == "" {
		to = "*"
	} else {
		to = openaireNormalizeDate(to, false)
	}
	return from + "," + to
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func dataciteRecordToPublication(r *dataciteRecord) Publication {
	attr := r.Attributes

	title := ""
	if len(attr.Titles) > 0 {
		title = strings.TrimSpace(attr.Titles[0].Title)
	}

	abstract := ""
	for _, d := range attr.Descriptions {
		if strings.EqualFold(d.DescriptionType, "Abstract") && d.Description != "" {
			abstract = stripXMLTags(d.Description)
			break
		}
	}
	if abstract == "" && len(attr.Descriptions) > 0 {
		abstract = stripXMLTags(attr.Descriptions[0].Description)
	}

	authors := make([]Author, 0, len(attr.Creators))
	for _, c := range attr.Creators {
		if c.Name == "" {
			continue
		}
		aff := ""
		if len(c.Affiliation) > 0 {
			aff = c.Affiliation[0].Name
		}
		orcid := ""
		for _, ni := range c.NameIdentifiers {
			if strings.EqualFold(ni.NameIdentifierScheme, "ORCID") {
				orcid = ni.NameIdentifier
				break
			}
		}
		authors = append(authors, Author{Name: c.Name, Affiliation: aff, ORCID: orcid})
	}

	doi := strings.TrimSpace(attr.DOI)
	if doi == "" {
		doi = r.ID
	}

	displayURL := attr.URL
	if displayURL == "" && doi != "" {
		displayURL = "https://doi.org/" + doi
	}

	published := ""
	if attr.PublicationYear > 0 {
		published = strconv.Itoa(attr.PublicationYear)
	}

	ct := ContentTypePaper
	switch strings.ToLower(attr.Types.ResourceTypeGeneral) {
	case "dataset":
		ct = ContentTypeDataset
	}

	meta := map[string]any{}
	if attr.Types.ResourceTypeGeneral != "" {
		meta[dataciteMetaKeyResourceTypeGeneral] = attr.Types.ResourceTypeGeneral
	}
	if attr.Types.ResourceType != "" {
		meta[dataciteMetaKeyResourceType] = attr.Types.ResourceType
	}
	if attr.Publisher != "" {
		meta[dataciteMetaKeyPublisher] = attr.Publisher
	}
	if attr.Registered != "" {
		meta[dataciteMetaKeyRegistered] = attr.Registered
	}

	return Publication{
		ID:             dataciteIDPrefix + r.ID,
		Source:         SourceDataCite,
		ContentType:    ct,
		Title:          title,
		Abstract:       abstract,
		URL:            displayURL,
		DOI:            doi,
		Published:      published,
		Authors:        authors,
		Language:       attr.Language,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *DataCitePlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *DataCitePlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
