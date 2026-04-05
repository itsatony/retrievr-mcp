package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// EuropePMC plugin identity constants
// ---------------------------------------------------------------------------

const (
	emcPluginID          = "europmc"
	emcPluginName        = "Europe PMC"
	emcPluginDescription = "Open access biomedical literature database with 40M+ publications, free access, no authentication required"
)

// ---------------------------------------------------------------------------
// EuropePMC API constants
// ---------------------------------------------------------------------------

const (
	emcDefaultBaseURL    = "https://www.ebi.ac.uk/europepmc/webservices/rest/"
	emcSearchPath        = "search"
	emcMaxResultsPerPage = 1000
	emcMaxResponseBytes  = 10 << 20 // 10 MB upper bound
)

// ---------------------------------------------------------------------------
// EuropePMC API parameter name constants
// ---------------------------------------------------------------------------

const (
	emcParamQuery      = "query"
	emcParamFormat     = "format"
	emcParamPageSize   = "pageSize"
	emcParamCursorMark = "cursorMark"
	emcParamSort       = "sort"
	emcParamPage       = "page"
)

// ---------------------------------------------------------------------------
// EuropePMC API parameter value constants
// ---------------------------------------------------------------------------

const (
	emcFormatJSON      = "json"
	emcCursorMarkStart = "*"
)

// ---------------------------------------------------------------------------
// EuropePMC sort parameter constants
// ---------------------------------------------------------------------------

const (
	emcSortRelevance    = ""               // default — no sort param needed
	emcSortDateDesc     = "FIRST_PDATE desc"
	emcSortDateAsc      = "FIRST_PDATE asc"
	emcSortCitationsDesc = "CITED desc"
)

// ---------------------------------------------------------------------------
// EuropePMC query field tag constants
// ---------------------------------------------------------------------------

const (
	emcFieldTitle  = "TITLE:"
	emcFieldAuthor = "AUTH:"
	emcFieldDate   = "FIRST_PDATE:"
	emcFieldExtID  = "EXT_ID:"
)

// ---------------------------------------------------------------------------
// EuropePMC query building constants
// ---------------------------------------------------------------------------

const (
	emcQueryAND          = " AND "
	emcQueryPartsInitCap = 4
	emcQueryQuote        = "\""
)

// ---------------------------------------------------------------------------
// EuropePMC date format constants
// ---------------------------------------------------------------------------

const (
	emcYearOnlyLength = 4
	emcYearStartPad   = "-01-01"
	emcYearEndPad     = "-12-31"
	emcDateRangeTo    = " TO "
	emcDateRangeOpen  = "["
	emcDateRangeClose = "]"
)

// ---------------------------------------------------------------------------
// EuropePMC HTTP constants
// ---------------------------------------------------------------------------

const emcHTTPStatusErrFmt = "status %d"

// ---------------------------------------------------------------------------
// EuropePMC metadata key constants
// ---------------------------------------------------------------------------

const (
	emcMetaKeyPMID       = "emc_pmid"
	emcMetaKeyPMCID      = "emc_pmcid"
	emcMetaKeySource     = "emc_source"
	emcMetaKeyJournal    = "emc_journal"
	emcMetaKeyJournalVol = "emc_journal_volume"
	emcMetaKeyJournalIss = "emc_journal_issue"
	emcMetaKeyIsOA       = "emc_is_open_access"
	emcMetaKeyMeSH       = "emc_mesh_terms"
)

// ---------------------------------------------------------------------------
// EuropePMC BibTeX constants
// ---------------------------------------------------------------------------

const emcBibTeXTemplate = `@article{%s,
  title   = {%s},
  author  = {%s},
  year    = {%s},
  journal = {%s},
  doi     = {%s},
  pmid    = {%s},
  url     = {%s}
}`

const (
	emcBibTeXAuthorSeparator = " and "
	emcBibTeXKeyPrefix       = "EPMC-"
)

// ---------------------------------------------------------------------------
// EuropePMC URL constants
// ---------------------------------------------------------------------------

const (
	emcAbsURLPrefix    = "https://europepmc.org/article/"
	emcFullTextXMLPath = "/fullTextXML"
)

// ---------------------------------------------------------------------------
// EuropePMC open access indicator
// ---------------------------------------------------------------------------

const emcOpenAccessYes = "Y"

// ---------------------------------------------------------------------------
// EuropePMC categories hint
// ---------------------------------------------------------------------------

const emcCategoriesHint = "Medicine, Biology, Biochemistry, Genetics, Pharmacology, Neuroscience, Immunology, Microbiology, Public Health, Molecular Biology"

// ---------------------------------------------------------------------------
// EuropePMC pagination constants
// ---------------------------------------------------------------------------

const (
	emcFirstPage      = 1
	emcDefaultPerPage = 25
)

// ---------------------------------------------------------------------------
// EuropePMC author parsing constants
// ---------------------------------------------------------------------------

const (
	emcAuthorSeparator  = ", "
	emcAuthorTrailingDot = "."
)

// ---------------------------------------------------------------------------
// EuropePMC JSON response struct definitions
// ---------------------------------------------------------------------------

// emcSearchResponse represents the top-level search response from Europe PMC.
type emcSearchResponse struct {
	Version    string        `json:"version"`
	HitCount   int           `json:"hitCount"`
	NextCursor string        `json:"nextCursorMark"`
	ResultList emcResultList `json:"resultList"`
}

// emcResultList wraps the array of search results.
type emcResultList struct {
	Results []emcResult `json:"result"`
}

// emcResult represents a single publication in the Europe PMC search response.
type emcResult struct {
	ID                   string              `json:"id"`
	Source               string              `json:"source"`
	PMID                 string              `json:"pmid"`
	PMCID                string              `json:"pmcid"`
	DOI                  string              `json:"doi"`
	Title                string              `json:"title"`
	AuthorString         string              `json:"authorString"`
	AbstractText         string              `json:"abstractText"`
	FirstPublicationDate string              `json:"firstPublicationDate"`
	IsOpenAccess         string              `json:"isOpenAccess"`
	CitedByCount         int                 `json:"citedByCount"`
	JournalInfo          *emcJournalInfo     `json:"journalInfo"`
	MeshHeadingList      *emcMeshList        `json:"meshHeadingList"`
	FullTextURLList      *emcFullTextURLList `json:"fullTextUrlList"`
}

// emcJournalInfo wraps journal metadata.
type emcJournalInfo struct {
	Journal emcJournal `json:"journal"`
	Volume  string     `json:"volume"`
	Issue   string     `json:"issue"`
}

// emcJournal represents journal identity information.
type emcJournal struct {
	Title string `json:"title"`
	ISSN  string `json:"issn"`
}

// emcMeshList wraps the MeSH heading array.
type emcMeshList struct {
	MeshHeading []emcMeshHeading `json:"meshHeading"`
}

// emcMeshHeading represents a single MeSH descriptor.
type emcMeshHeading struct {
	DescriptorName string `json:"descriptorName"`
}

// emcFullTextURLList wraps the array of full text URLs.
type emcFullTextURLList struct {
	FullTextURL []emcFullTextURL `json:"fullTextUrl"`
}

// emcFullTextURL represents a single full text URL entry.
type emcFullTextURL struct {
	URL           string `json:"url"`
	Availability  string `json:"availabilityCode"`
	DocumentStyle string `json:"documentStyle"`
	Site          string `json:"site"`
}

// ---------------------------------------------------------------------------
// EuropePMCPlugin struct
// ---------------------------------------------------------------------------

// EuropePMCPlugin implements SourcePlugin for the Europe PMC REST API.
// Thread-safe for concurrent use after Initialize.
type EuropePMCPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex // protects health state below
	healthy   bool
	lastError string
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: identity methods
// ---------------------------------------------------------------------------

// ID returns the unique source identifier.
func (p *EuropePMCPlugin) ID() string { return emcPluginID }

// Name returns a human-readable name.
func (p *EuropePMCPlugin) Name() string { return emcPluginName }

// Description returns a short description for LLM context.
func (p *EuropePMCPlugin) Description() string { return emcPluginDescription }

// ContentTypes returns the types of content this source provides.
func (p *EuropePMCPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat returns the default content format.
func (p *EuropePMCPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats returns all formats this source can provide.
func (p *EuropePMCPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Capabilities
// ---------------------------------------------------------------------------

// Capabilities reports what filtering, sorting, and features Europe PMC supports.
func (p *EuropePMCPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         true,
		SupportsCitations:        true,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     true,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    true,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       emcMaxResultsPerPage,
		CategoriesHint:           emcCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Initialize
// ---------------------------------------------------------------------------

// Initialize sets up the Europe PMC plugin with the given configuration.
// Called once at startup. No API key required — Europe PMC is a free resource.
func (p *EuropePMCPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = emcDefaultBaseURL
	}

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}

	p.httpClient = &http.Client{Timeout: timeout}
	p.healthy = true

	return nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Health
// ---------------------------------------------------------------------------

// Health returns current health and rate-limit status.
func (p *EuropePMCPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Search
// ---------------------------------------------------------------------------

// Search executes a search query against the Europe PMC REST API.
// Single-phase workflow: search → JSON results → map to publications.
func (p *EuropePMCPlugin) Search(ctx context.Context, params SearchParams, _ *CallCredentials) (*SearchResult, error) {
	query := buildEMCSearchQuery(params)
	if query == "" {
		return nil, ErrEuropePMCEmptyQuery
	}

	reqURL := buildEMCSearchURL(p.baseURL, query, params)

	var response emcSearchResponse
	if err := p.doRequest(ctx, reqURL, &response); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}

	p.recordSuccess()

	pubs := make([]Publication, 0, len(response.ResultList.Results))
	for i := range response.ResultList.Results {
		pubs = append(pubs, mapEMCResultToPublication(&response.ResultList.Results[i]))
	}

	hasMore := (params.Offset + len(pubs)) < response.HitCount

	return &SearchResult{
		Total:   response.HitCount,
		Results: pubs,
		HasMore: hasMore,
	}, nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Get
// ---------------------------------------------------------------------------

// Get retrieves a single publication by its Europe PMC identifier.
func (p *EuropePMCPlugin) Get(ctx context.Context, id string, include []IncludeField, format ContentFormat, _ *CallCredentials) (*Publication, error) {
	reqURL := buildEMCGetURL(p.baseURL, id)

	var response emcSearchResponse
	if err := p.doRequest(ctx, reqURL, &response); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	if len(response.ResultList.Results) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrEuropePMCNotFound, id)
	}

	p.recordSuccess()
	pub := mapEMCResultToPublication(&response.ResultList.Results[0])

	// Fetch full text XML if requested and article is open access (non-fatal on failure).
	if slices.Contains(include, IncludeFullText) {
		result := &response.ResultList.Results[0]
		if result.IsOpenAccess == emcOpenAccessYes {
			source := result.Source
			articleID := result.ID
			fullText, fetchErr := p.fetchFullText(ctx, source, articleID)
			if fetchErr == nil && fullText != "" {
				pub.FullText = &FullTextContent{
					Content:       fullText,
					ContentFormat: FormatXML,
					ContentLength: len(fullText),
					Truncated:     false,
				}
			}
		}
	}

	// Apply format conversion if not native JSON.
	if format != FormatNative && format != FormatJSON {
		if err := convertEMCFormat(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// ---------------------------------------------------------------------------
// Full text fetching
// ---------------------------------------------------------------------------

// fetchFullText retrieves the JATS XML full text for an open-access article.
func (p *EuropePMCPlugin) fetchFullText(ctx context.Context, source, id string) (string, error) {
	reqURL := buildEMCFullTextURL(p.baseURL, source, id)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrEuropePMCHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return "", fmt.Errorf("%w: %w", ErrEuropePMCHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("%w: "+emcHTTPStatusErrFmt, ErrEuropePMCHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(emcMaxResponseBytes))
	body, err := io.ReadAll(limitedBody)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrEuropePMCHTTPRequest, err)
	}

	return string(body), nil
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

// doRequest executes an HTTP GET and decodes the JSON response into the target.
func (p *EuropePMCPlugin) doRequest(ctx context.Context, reqURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEuropePMCHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return fmt.Errorf("%w: %w", ErrEuropePMCHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrEuropePMCNotFound
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%w: "+emcHTTPStatusErrFmt, ErrEuropePMCHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, emcMaxResponseBytes)
	if err := json.NewDecoder(limitedBody).Decode(target); err != nil {
		return fmt.Errorf("%w: %w", ErrEuropePMCJSONParse, err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Health state helpers
// ---------------------------------------------------------------------------

func (p *EuropePMCPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *EuropePMCPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	p.lastError = err.Error()
}

// ---------------------------------------------------------------------------
// URL / query building
// ---------------------------------------------------------------------------

// buildEMCSearchQuery assembles a Europe PMC query string with field tags.
// Combines the free-text query with optional title, author, date, and MeSH filters.
func buildEMCSearchQuery(params SearchParams) string {
	parts := make([]string, 0, emcQueryPartsInitCap)

	if params.Query != "" {
		parts = append(parts, params.Query)
	}

	if params.Filters.Title != "" {
		parts = append(parts, emcFieldTitle+emcQueryQuote+params.Filters.Title+emcQueryQuote)
	}

	for _, author := range params.Filters.Authors {
		if author != "" {
			parts = append(parts, emcFieldAuthor+emcQueryQuote+author+emcQueryQuote)
		}
	}

	for _, cat := range params.Filters.Categories {
		if cat != "" {
			parts = append(parts, emcQueryQuote+cat+emcQueryQuote)
		}
	}

	if params.Filters.DateFrom != "" || params.Filters.DateTo != "" {
		dateRange := buildEMCDateRange(params.Filters.DateFrom, params.Filters.DateTo)
		if dateRange != "" {
			parts = append(parts, dateRange)
		}
	}

	return strings.Join(parts, emcQueryAND)
}

// buildEMCDateRange constructs a FIRST_PDATE range clause for Europe PMC.
// Format: (FIRST_PDATE:[2024-01-01 TO 2024-12-31])
func buildEMCDateRange(dateFrom, dateTo string) string {
	from := dateFrom
	to := dateTo

	if from == "" && to == "" {
		return ""
	}

	// Pad year-only dates.
	if from != "" && len(from) == emcYearOnlyLength {
		from += emcYearStartPad
	}
	if to != "" && len(to) == emcYearOnlyLength {
		to += emcYearEndPad
	}

	// If only one end is specified, use wildcard for the other.
	if from == "" {
		from = "*"
	}
	if to == "" {
		to = "*"
	}

	return "(" + emcFieldDate + emcDateRangeOpen + from + emcDateRangeTo + to + emcDateRangeClose + ")"
}

// buildEMCSearchURL assembles the full search URL with query parameters.
func buildEMCSearchURL(baseURL, query string, params SearchParams) string {
	qp := url.Values{}
	qp.Set(emcParamQuery, query)
	qp.Set(emcParamFormat, emcFormatJSON)

	pageSize := params.Limit
	if pageSize <= 0 {
		pageSize = emcDefaultPerPage
	}
	if pageSize > emcMaxResultsPerPage {
		pageSize = emcMaxResultsPerPage
	}
	qp.Set(emcParamPageSize, strconv.Itoa(pageSize))

	// Map offset to page number.
	page := emcFirstPage
	if params.Offset > 0 && pageSize > 0 {
		page = (params.Offset / pageSize) + emcFirstPage
	}
	qp.Set(emcParamPage, strconv.Itoa(page))

	qp.Set(emcParamCursorMark, emcCursorMarkStart)

	sortVal := mapEMCSortOrder(params.Sort)
	if sortVal != "" {
		qp.Set(emcParamSort, sortVal)
	}

	return baseURL + emcSearchPath + "?" + qp.Encode()
}

// buildEMCGetURL assembles the URL for fetching a single article by its external ID.
func buildEMCGetURL(baseURL, id string) string {
	qp := url.Values{}
	qp.Set(emcParamQuery, emcFieldExtID+id)
	qp.Set(emcParamFormat, emcFormatJSON)
	qp.Set(emcParamPageSize, strconv.Itoa(emcFirstPage))

	return baseURL + emcSearchPath + "?" + qp.Encode()
}

// buildEMCFullTextURL assembles the URL for retrieving full text XML.
// Format: {baseURL}{source}/{id}/fullTextXML
func buildEMCFullTextURL(baseURL, source, id string) string {
	return baseURL + source + "/" + id + emcFullTextXMLPath
}

// mapEMCSortOrder converts a SortOrder to a Europe PMC sort parameter value.
func mapEMCSortOrder(sort SortOrder) string {
	switch sort {
	case SortRelevance:
		return emcSortRelevance
	case SortDateDesc:
		return emcSortDateDesc
	case SortDateAsc:
		return emcSortDateAsc
	case SortCitations:
		return emcSortCitationsDesc
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Response mapping
// ---------------------------------------------------------------------------

// mapEMCResultToPublication converts an Europe PMC result to the unified Publication type.
func mapEMCResultToPublication(result *emcResult) Publication {
	// Use PMID as the canonical ID when available, otherwise use the source-specific ID.
	canonicalID := result.PMID
	if canonicalID == "" {
		canonicalID = result.ID
	}

	pub := Publication{
		ID:          SourceEuropePMC + prefixedIDSeparator + canonicalID,
		Source:      SourceEuropePMC,
		ContentType: ContentTypePaper,
		Title:       result.Title,
		Abstract:    result.AbstractText,
		DOI:         result.DOI,
		Published:   result.FirstPublicationDate,
		Authors:     parseEMCAuthors(result.AuthorString),
	}

	// Build URL.
	pub.URL = emcAbsURLPrefix + result.Source + "/" + result.ID

	// Citation count.
	citationCount := result.CitedByCount
	pub.CitationCount = &citationCount

	// MeSH terms as categories.
	if result.MeshHeadingList != nil {
		categories := make([]string, 0, len(result.MeshHeadingList.MeshHeading))
		for _, mh := range result.MeshHeadingList.MeshHeading {
			if mh.DescriptorName != "" {
				categories = append(categories, mh.DescriptorName)
			}
		}
		pub.Categories = categories
	}

	// PDF URL from full text URL list.
	if result.FullTextURLList != nil {
		for _, ftURL := range result.FullTextURLList.FullTextURL {
			if ftURL.URL != "" {
				pub.PDFURL = ftURL.URL
				break
			}
		}
	}

	// Source metadata.
	metadata := make(map[string]any)

	if result.PMID != "" {
		metadata[emcMetaKeyPMID] = result.PMID
	}
	if result.PMCID != "" {
		metadata[emcMetaKeyPMCID] = result.PMCID
	}
	if result.Source != "" {
		metadata[emcMetaKeySource] = result.Source
	}
	if result.IsOpenAccess != "" {
		metadata[emcMetaKeyIsOA] = result.IsOpenAccess
	}
	if result.JournalInfo != nil {
		if result.JournalInfo.Journal.Title != "" {
			metadata[emcMetaKeyJournal] = result.JournalInfo.Journal.Title
		}
		if result.JournalInfo.Volume != "" {
			metadata[emcMetaKeyJournalVol] = result.JournalInfo.Volume
		}
		if result.JournalInfo.Issue != "" {
			metadata[emcMetaKeyJournalIss] = result.JournalInfo.Issue
		}
	}
	if result.MeshHeadingList != nil && len(result.MeshHeadingList.MeshHeading) > 0 {
		terms := make([]string, 0, len(result.MeshHeadingList.MeshHeading))
		for _, mh := range result.MeshHeadingList.MeshHeading {
			if mh.DescriptorName != "" {
				terms = append(terms, mh.DescriptorName)
			}
		}
		metadata[emcMetaKeyMeSH] = terms
	}

	if len(metadata) > 0 {
		pub.SourceMetadata = metadata
	}

	return pub
}

// parseEMCAuthors splits the Europe PMC author string into individual Author structs.
// Europe PMC returns authors as "Smith J, Chen W, Doe JA." format.
func parseEMCAuthors(authorString string) []Author {
	if authorString == "" {
		return nil
	}

	// Trim trailing period.
	trimmed := strings.TrimRight(authorString, emcAuthorTrailingDot)
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return nil
	}

	parts := strings.Split(trimmed, emcAuthorSeparator)
	authors := make([]Author, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name != "" {
			authors = append(authors, Author{Name: name})
		}
	}

	return authors
}

// ---------------------------------------------------------------------------
// Format conversion
// ---------------------------------------------------------------------------

// convertEMCFormat converts the publication to the requested format.
func convertEMCFormat(pub *Publication, format ContentFormat) error {
	switch format {
	case FormatBibTeX:
		bibtex := assembleEMCBibTeX(pub)
		pub.FullText = &FullTextContent{
			Content:       bibtex,
			ContentFormat: FormatBibTeX,
			ContentLength: len(bibtex),
			Truncated:     false,
		}
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrFormatUnsupported, format)
	}
}

// assembleEMCBibTeX assembles a BibTeX entry from publication metadata.
func assembleEMCBibTeX(pub *Publication) string {
	// Extract year from published date.
	year := ""
	if len(pub.Published) >= emcYearOnlyLength {
		year = pub.Published[:emcYearOnlyLength]
	}

	// Build author string.
	authorNames := make([]string, 0, len(pub.Authors))
	for _, a := range pub.Authors {
		authorNames = append(authorNames, a.Name)
	}
	authorStr := strings.Join(authorNames, emcBibTeXAuthorSeparator)

	// Extract journal from metadata.
	journal := ""
	if pub.SourceMetadata != nil {
		if j, ok := pub.SourceMetadata[emcMetaKeyJournal]; ok {
			if js, ok := j.(string); ok {
				journal = js
			}
		}
	}

	// Extract PMID for the key.
	pmid := ""
	if pub.SourceMetadata != nil {
		if p, ok := pub.SourceMetadata[emcMetaKeyPMID]; ok {
			if ps, ok := p.(string); ok {
				pmid = ps
			}
		}
	}

	key := emcBibTeXKeyPrefix + pmid

	return fmt.Sprintf(emcBibTeXTemplate,
		key,
		pub.Title,
		authorStr,
		year,
		journal,
		pub.DOI,
		pmid,
		pub.URL,
	)
}
