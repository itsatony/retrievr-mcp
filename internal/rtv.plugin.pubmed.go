package internal

import (
	"context"
	"encoding/xml"
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
// PubMed plugin identity constants
// ---------------------------------------------------------------------------

const (
	pmPluginID          = "pubmed"
	pmPluginName        = "PubMed"
	pmPluginDescription = "Biomedical literature database from the U.S. National Library of Medicine with 35M+ citations"
)

// ---------------------------------------------------------------------------
// PubMed API constants
// ---------------------------------------------------------------------------

const (
	pmDefaultBaseURL    = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/"
	pmESearchPath       = "esearch.fcgi"
	pmEFetchPath        = "efetch.fcgi"
	pmMaxResultsPerPage = 10000
	pmMaxResponseBytes  = 10 << 20 // 10 MB upper bound
)

// ---------------------------------------------------------------------------
// PubMed API parameter name constants
// ---------------------------------------------------------------------------

const (
	pmParamDB         = "db"
	pmParamTerm       = "term"
	pmParamRetStart   = "retstart"
	pmParamRetMax     = "retmax"
	pmParamSort       = "sort"
	pmParamMinDate    = "mindate"
	pmParamMaxDate    = "maxdate"
	pmParamDateType   = "datetype"
	pmParamRetType    = "rettype"
	pmParamRetMode    = "retmode"
	pmParamTool       = "tool"
	pmParamEmail      = "email"
	pmParamAPIKey     = "api_key"
	pmParamUseHistory = "usehistory"
	pmParamWebEnv     = "WebEnv"
	pmParamQueryKey   = "query_key"
	pmParamID         = "id"
)

// ---------------------------------------------------------------------------
// PubMed API parameter value constants
// ---------------------------------------------------------------------------

const (
	pmDBPubMed      = "pubmed"
	pmDBPMC         = "pmc"
	pmRetTypeXML    = "xml"
	pmRetModeXML    = "xml"
	pmHistoryY      = "y"
	pmDateTypePDAT  = "pdat"
	pmSortRelevance = "relevance"
	pmSortPubDate   = "pub+date"
)

// ---------------------------------------------------------------------------
// PubMed query field tag constants
// ---------------------------------------------------------------------------

const (
	pmFieldTitle  = "[Title]"
	pmFieldAuthor = "[Author]"
	pmFieldMeSH   = "[MeSH Terms]"
)

// ---------------------------------------------------------------------------
// PubMed date format constants
// ---------------------------------------------------------------------------

const (
	pmDateSeparator     = "/"
	pmISODateSeparator  = "-"
	pmYearOnlyLength    = 4
	pmYearStartPad      = "/01/01"
	pmYearEndPad        = "/12/31"
	pmDayZeroPad        = "0"
	pmSingleDigitDayLen = 1
)

// ---------------------------------------------------------------------------
// PubMed query building constants
// ---------------------------------------------------------------------------

const (
	pmQueryAND          = " AND "
	pmQueryPartsInitCap = 4
	pmQueryQuote        = "\""
)

// ---------------------------------------------------------------------------
// PubMed HTTP constants
// ---------------------------------------------------------------------------

const (
	pmHTTPStatusErrFmt = "status %d"
)

// ---------------------------------------------------------------------------
// PubMed metadata key constants
// ---------------------------------------------------------------------------

const (
	pmMetaKeyPMID     = "pubmed_pmid"
	pmMetaKeyPMCID    = "pubmed_pmcid"
	pmMetaKeyJournal  = "pubmed_journal"
	pmMetaKeyVolume   = "pubmed_volume"
	pmMetaKeyIssue    = "pubmed_issue"
	pmMetaKeyMeSH     = "pubmed_mesh_terms"
	pmMetaKeyPubTypes = "pubmed_publication_types"
	pmMetaKeyLanguage = "pubmed_language"
	pmMetaKeyISSN     = "pubmed_issn"
)

// ---------------------------------------------------------------------------
// PubMed config extra key constants
// ---------------------------------------------------------------------------

const (
	pmExtraKeyTool  = "tool"
	pmExtraKeyEmail = "email"
)

// ---------------------------------------------------------------------------
// PubMed default constants
// ---------------------------------------------------------------------------

const (
	pmDefaultTool = "retrievr-mcp"
)

// ---------------------------------------------------------------------------
// PubMed URL prefix constants
// ---------------------------------------------------------------------------

const (
	pmAbsURLPrefix = "https://pubmed.ncbi.nlm.nih.gov/"
	pmPMCURLPrefix = "https://www.ncbi.nlm.nih.gov/pmc/articles/"
)

// ---------------------------------------------------------------------------
// PubMed ArticleID type constants
// ---------------------------------------------------------------------------

const (
	pmArticleIDTypePMC    = "pmc"
	pmArticleIDTypeDOI    = "doi"
	pmArticleIDTypePubmed = "pubmed"
)

// ---------------------------------------------------------------------------
// PubMed author name formatting constants
// ---------------------------------------------------------------------------

const (
	pmAuthorNameSeparator      = " "
	pmAbstractSectionSeparator = "\n"
)

// ---------------------------------------------------------------------------
// PubMed categories hint
// ---------------------------------------------------------------------------

const pmCategoriesHint = "Medicine, Biology, Biochemistry, Genetics, Pharmacology, Neuroscience, Immunology, Microbiology, Public Health, Nursing"

// ---------------------------------------------------------------------------
// PubMed month name map
// ---------------------------------------------------------------------------

var pmMonthNames = map[string]string{
	"Jan": "01", "Feb": "02", "Mar": "03", "Apr": "04",
	"May": "05", "Jun": "06", "Jul": "07", "Aug": "08",
	"Sep": "09", "Oct": "10", "Nov": "11", "Dec": "12",
}

// ---------------------------------------------------------------------------
// PubMed XML response structs — ESearch
// ---------------------------------------------------------------------------

// pmESearchResult represents the XML response from esearch.fcgi.
type pmESearchResult struct {
	XMLName  xml.Name `xml:"eSearchResult"`
	Count    int      `xml:"Count"`
	RetMax   int      `xml:"RetMax"`
	RetStart int      `xml:"RetStart"`
	QueryKey string   `xml:"QueryKey"`
	WebEnv   string   `xml:"WebEnv"`
	IDList   pmIDList `xml:"IdList"`
}

// pmIDList wraps the list of PMIDs from esearch.
type pmIDList struct {
	IDs []string `xml:"Id"`
}

// ---------------------------------------------------------------------------
// PubMed XML response structs — EFetch (PubmedArticleSet)
// ---------------------------------------------------------------------------

// pmArticleSet represents the top-level PubmedArticleSet XML response.
type pmArticleSet struct {
	XMLName  xml.Name    `xml:"PubmedArticleSet"`
	Articles []pmArticle `xml:"PubmedArticle"`
}

// pmArticle represents a single PubmedArticle.
type pmArticle struct {
	MedlineCitation pmMedlineCitation `xml:"MedlineCitation"`
	PubmedData      pmPubmedData      `xml:"PubmedData"`
}

// pmMedlineCitation contains the core article metadata.
type pmMedlineCitation struct {
	PMID            string            `xml:"PMID"`
	Article         pmArticleDetail   `xml:"Article"`
	MeshHeadingList pmMeshHeadingList `xml:"MeshHeadingList"`
}

// pmArticleDetail contains article-level fields.
type pmArticleDetail struct {
	Journal          pmJournal             `xml:"Journal"`
	ArticleTitle     string                `xml:"ArticleTitle"`
	Abstract         pmAbstract            `xml:"Abstract"`
	AuthorList       pmAuthorList          `xml:"AuthorList"`
	Language         string                `xml:"Language"`
	PublicationTypes pmPublicationTypeList `xml:"PublicationTypeList"`
	ELocationIDs     []pmELocationID       `xml:"ELocationID"`
}

// pmJournal contains journal metadata.
type pmJournal struct {
	ISSN         string         `xml:"ISSN"`
	JournalIssue pmJournalIssue `xml:"JournalIssue"`
	Title        string         `xml:"Title"`
}

// pmJournalIssue contains issue-level metadata.
type pmJournalIssue struct {
	Volume  string    `xml:"Volume"`
	Issue   string    `xml:"Issue"`
	PubDate pmPubDate `xml:"PubDate"`
}

// pmPubDate represents a publication date. PubMed dates can be structured
// (Year/Month/Day) or freeform (MedlineDate).
type pmPubDate struct {
	Year        string `xml:"Year"`
	Month       string `xml:"Month"`
	Day         string `xml:"Day"`
	MedlineDate string `xml:"MedlineDate"`
}

// pmAbstract wraps abstract text elements. PubMed supports structured
// abstracts with multiple labeled sections (BACKGROUND, METHODS, etc.).
type pmAbstract struct {
	Texts []string `xml:"AbstractText"`
}

// pmAuthorList wraps the list of authors.
type pmAuthorList struct {
	Authors []pmAuthor `xml:"Author"`
}

// pmAuthor represents a single author.
type pmAuthor struct {
	LastName        string              `xml:"LastName"`
	ForeName        string              `xml:"ForeName"`
	Initials        string              `xml:"Initials"`
	AffiliationInfo []pmAffiliationInfo `xml:"AffiliationInfo"`
}

// pmAffiliationInfo wraps an author's affiliation.
type pmAffiliationInfo struct {
	Affiliation string `xml:"Affiliation"`
}

// pmELocationID represents an electronic location identifier (e.g., DOI).
type pmELocationID struct {
	EIDType string `xml:"EIdType,attr"`
	Value   string `xml:",chardata"`
}

// pmPublicationTypeList wraps publication type entries.
type pmPublicationTypeList struct {
	Types []string `xml:"PublicationType"`
}

// pmMeshHeadingList wraps MeSH heading entries.
type pmMeshHeadingList struct {
	Headings []pmMeshHeading `xml:"MeshHeading"`
}

// pmMeshHeading represents a single MeSH heading.
type pmMeshHeading struct {
	DescriptorName string `xml:"DescriptorName"`
}

// pmPubmedData contains article identifier cross-references.
type pmPubmedData struct {
	ArticleIDList pmArticleIDList `xml:"ArticleIdList"`
}

// pmArticleIDList wraps the list of article identifiers.
type pmArticleIDList struct {
	ArticleIDs []pmArticleID `xml:"ArticleId"`
}

// pmArticleID represents a single article identifier with its type.
type pmArticleID struct {
	IDType string `xml:"IdType,attr"`
	Value  string `xml:",chardata"`
}

// ---------------------------------------------------------------------------
// PubMedPlugin struct
// ---------------------------------------------------------------------------

// PubMedPlugin implements SourcePlugin for the NCBI PubMed E-utilities API.
// Thread-safe for concurrent use after Initialize.
type PubMedPlugin struct {
	baseURL    string
	apiKey     string // server-level default from config
	toolName   string // NCBI tool parameter (required on every request)
	email      string // NCBI email parameter (required on every request)
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
func (p *PubMedPlugin) ID() string { return pmPluginID }

// Name returns a human-readable name.
func (p *PubMedPlugin) Name() string { return pmPluginName }

// Description returns a short description for LLM context.
func (p *PubMedPlugin) Description() string { return pmPluginDescription }

// ContentTypes returns the types of content this source provides.
func (p *PubMedPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat returns the default content format.
func (p *PubMedPlugin) NativeFormat() ContentFormat { return FormatXML }

// AvailableFormats returns all formats this source can provide.
func (p *PubMedPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatXML, FormatJSON, FormatBibTeX}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Capabilities
// ---------------------------------------------------------------------------

// Capabilities reports what filtering, sorting, and features PubMed supports.
func (p *PubMedPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         true,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     true,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       pmMaxResultsPerPage,
		CategoriesHint:           pmCategoriesHint,
		NativeFormat:             FormatXML,
		AvailableFormats:         []ContentFormat{FormatXML, FormatJSON, FormatBibTeX},
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Initialize
// ---------------------------------------------------------------------------

// Initialize sets up the PubMed plugin with the given configuration.
// Called once at startup.
func (p *PubMedPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = pmDefaultBaseURL
	}

	if cfg.Extra != nil {
		p.toolName = cfg.Extra[pmExtraKeyTool]
		p.email = cfg.Extra[pmExtraKeyEmail]
	}
	if p.toolName == "" {
		p.toolName = pmDefaultTool
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
func (p *PubMedPlugin) Health(_ context.Context) SourceHealth {
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
// SourcePlugin interface: Search (two-phase workflow)
// ---------------------------------------------------------------------------

// Search executes a two-phase search against the PubMed E-utilities API:
// 1. esearch.fcgi → PMIDs + count + WebEnv/QueryKey
// 2. efetch.fcgi  → PubmedArticle XML records
func (p *PubMedPlugin) Search(ctx context.Context, params SearchParams, creds *CallCredentials) (*SearchResult, error) {
	query := buildPMSearchQuery(params)
	if query == "" {
		return nil, ErrPubMedEmptyQuery
	}

	apiKey := resolvePMAPIKey(creds, p.apiKey)

	// Phase 1: esearch — get PMIDs and total count.
	esearchURL := buildPMESearchURL(p.baseURL, query, params, p.toolName, p.email, apiKey)
	esearchResult, err := p.doESearchRequest(ctx, esearchURL)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}

	if esearchResult.Count == 0 || len(esearchResult.IDList.IDs) == 0 {
		p.recordSuccess()
		return &SearchResult{Total: 0, Results: nil, HasMore: false}, nil
	}

	// Phase 2: efetch — retrieve full article records via WebEnv/QueryKey.
	efetchURL := buildPMEFetchURL(p.baseURL, esearchResult.WebEnv, esearchResult.QueryKey,
		params.Offset, params.Limit, p.toolName, p.email, apiKey)
	articleSet, err := p.doEFetchRequest(ctx, efetchURL)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}

	p.recordSuccess()

	pubs := make([]Publication, 0, len(articleSet.Articles))
	for i := range articleSet.Articles {
		pubs = append(pubs, mapPMArticleToPublication(&articleSet.Articles[i]))
	}

	hasMore := (params.Offset + len(pubs)) < esearchResult.Count

	return &SearchResult{
		Total:   esearchResult.Count,
		Results: pubs,
		HasMore: hasMore,
	}, nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Get
// ---------------------------------------------------------------------------

// Get retrieves a single publication by its PubMed ID (PMID).
func (p *PubMedPlugin) Get(ctx context.Context, id string, include []IncludeField, format ContentFormat, creds *CallCredentials) (*Publication, error) {
	apiKey := resolvePMAPIKey(creds, p.apiKey)

	efetchURL := buildPMGetURL(p.baseURL, id, p.toolName, p.email, apiKey)
	articleSet, err := p.doEFetchRequest(ctx, efetchURL)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	if len(articleSet.Articles) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrPubMedNotFound, id)
	}

	p.recordSuccess()
	pub := mapPMArticleToPublication(&articleSet.Articles[0])

	// Fetch PMC full text if requested and PMC ID is available (non-fatal on failure).
	if slices.Contains(include, IncludeFullText) {
		pmcID := extractPMCID(&articleSet.Articles[0])
		if pmcID != "" {
			fullText, fetchErr := p.fetchPMCFullText(ctx, pmcID, apiKey)
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

	// Apply format conversion if not native XML.
	if format != FormatNative && format != FormatXML {
		if err := convertPMFormat(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// ---------------------------------------------------------------------------
// PMC full text fetching
// ---------------------------------------------------------------------------

// fetchPMCFullText retrieves the JATS XML full text for a PMC article.
func (p *PubMedPlugin) fetchPMCFullText(ctx context.Context, pmcID, apiKey string) (string, error) {
	reqURL := buildPMCFullTextURL(p.baseURL, pmcID, p.toolName, p.email, apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrPubMedHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return "", fmt.Errorf("%w: %w", ErrPubMedHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("%w: "+pmHTTPStatusErrFmt, ErrPubMedHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(pmMaxResponseBytes))
	body, err := io.ReadAll(limitedBody)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrPubMedHTTPRequest, err)
	}

	return string(body), nil
}

// ---------------------------------------------------------------------------
// HTTP request helpers
// ---------------------------------------------------------------------------

// doESearchRequest executes an HTTP GET for esearch and decodes the XML response.
func (p *PubMedPlugin) doESearchRequest(ctx context.Context, reqURL string) (*pmESearchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPubMedHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return nil, fmt.Errorf("%w: %w", ErrPubMedHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("%w: "+pmHTTPStatusErrFmt, ErrPubMedHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(pmMaxResponseBytes))
	var result pmESearchResult
	if err := xml.NewDecoder(limitedBody).Decode(&result); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPubMedXMLParse, err)
	}

	return &result, nil
}

// doEFetchRequest executes an HTTP GET for efetch and decodes the XML response.
func (p *PubMedPlugin) doEFetchRequest(ctx context.Context, reqURL string) (*pmArticleSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPubMedHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return nil, fmt.Errorf("%w: %w", ErrPubMedHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, ErrPubMedNotFound
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("%w: "+pmHTTPStatusErrFmt, ErrPubMedHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(pmMaxResponseBytes))
	var result pmArticleSet
	if err := xml.NewDecoder(limitedBody).Decode(&result); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPubMedXMLParse, err)
	}

	return &result, nil
}

// ---------------------------------------------------------------------------
// Health state helpers
// ---------------------------------------------------------------------------

func (p *PubMedPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *PubMedPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	p.lastError = err.Error()
}

// ---------------------------------------------------------------------------
// Credential resolution helper
// ---------------------------------------------------------------------------

// resolvePMAPIKey extracts the effective API key from per-call credentials
// and server default, following the three-level resolution chain.
func resolvePMAPIKey(creds *CallCredentials, serverDefault string) string {
	if creds != nil {
		return creds.ResolveForSource(SourcePubMed, serverDefault)
	}
	return serverDefault
}

// ---------------------------------------------------------------------------
// URL / query building
// ---------------------------------------------------------------------------

// buildPMSearchQuery assembles a PubMed field-tagged query string.
// Combines the free-text query with optional title, author, and MeSH filters.
func buildPMSearchQuery(params SearchParams) string {
	parts := make([]string, 0, pmQueryPartsInitCap)

	if params.Query != "" {
		parts = append(parts, params.Query)
	}

	if params.Filters.Title != "" {
		parts = append(parts, pmQueryQuote+params.Filters.Title+pmQueryQuote+pmFieldTitle)
	}

	for _, author := range params.Filters.Authors {
		if author != "" {
			parts = append(parts, pmQueryQuote+author+pmQueryQuote+pmFieldAuthor)
		}
	}

	for _, cat := range params.Filters.Categories {
		if cat != "" {
			parts = append(parts, pmQueryQuote+cat+pmQueryQuote+pmFieldMeSH)
		}
	}

	return strings.Join(parts, pmQueryAND)
}

// buildPMESearchURL assembles the esearch URL with all required parameters.
func buildPMESearchURL(baseURL, query string, params SearchParams, tool, email, apiKey string) string {
	qp := url.Values{}
	qp.Set(pmParamDB, pmDBPubMed)
	qp.Set(pmParamTerm, query)
	qp.Set(pmParamRetStart, strconv.Itoa(params.Offset))
	qp.Set(pmParamRetMax, strconv.Itoa(params.Limit))
	qp.Set(pmParamUseHistory, pmHistoryY)
	qp.Set(pmParamTool, tool)
	qp.Set(pmParamEmail, email)

	sortVal := mapPMSortOrder(params.Sort)
	if sortVal != "" {
		qp.Set(pmParamSort, sortVal)
	}

	if params.Filters.DateFrom != "" || params.Filters.DateTo != "" {
		qp.Set(pmParamDateType, pmDateTypePDAT)
		if params.Filters.DateFrom != "" {
			qp.Set(pmParamMinDate, convertPMDate(params.Filters.DateFrom, false))
		}
		if params.Filters.DateTo != "" {
			qp.Set(pmParamMaxDate, convertPMDate(params.Filters.DateTo, true))
		}
	}

	if apiKey != "" {
		qp.Set(pmParamAPIKey, apiKey)
	}

	return baseURL + pmESearchPath + "?" + qp.Encode()
}

// buildPMEFetchURL assembles the efetch URL using WebEnv/QueryKey from esearch.
func buildPMEFetchURL(baseURL, webEnv, queryKey string, offset, limit int, tool, email, apiKey string) string {
	qp := url.Values{}
	qp.Set(pmParamDB, pmDBPubMed)
	qp.Set(pmParamWebEnv, webEnv)
	qp.Set(pmParamQueryKey, queryKey)
	qp.Set(pmParamRetStart, strconv.Itoa(offset))
	qp.Set(pmParamRetMax, strconv.Itoa(limit))
	qp.Set(pmParamRetType, pmRetTypeXML)
	qp.Set(pmParamRetMode, pmRetModeXML)
	qp.Set(pmParamTool, tool)
	qp.Set(pmParamEmail, email)

	if apiKey != "" {
		qp.Set(pmParamAPIKey, apiKey)
	}

	return baseURL + pmEFetchPath + "?" + qp.Encode()
}

// buildPMGetURL assembles the efetch URL for retrieving a single article by PMID.
func buildPMGetURL(baseURL, pmid, tool, email, apiKey string) string {
	qp := url.Values{}
	qp.Set(pmParamDB, pmDBPubMed)
	qp.Set(pmParamID, pmid)
	qp.Set(pmParamRetType, pmRetTypeXML)
	qp.Set(pmParamRetMode, pmRetModeXML)
	qp.Set(pmParamTool, tool)
	qp.Set(pmParamEmail, email)

	if apiKey != "" {
		qp.Set(pmParamAPIKey, apiKey)
	}

	return baseURL + pmEFetchPath + "?" + qp.Encode()
}

// buildPMCFullTextURL assembles the efetch URL for retrieving PMC full text.
func buildPMCFullTextURL(baseURL, pmcID, tool, email, apiKey string) string {
	qp := url.Values{}
	qp.Set(pmParamDB, pmDBPMC)
	qp.Set(pmParamID, pmcID)
	qp.Set(pmParamRetType, pmRetTypeXML)
	qp.Set(pmParamRetMode, pmRetModeXML)
	qp.Set(pmParamTool, tool)
	qp.Set(pmParamEmail, email)

	if apiKey != "" {
		qp.Set(pmParamAPIKey, apiKey)
	}

	return baseURL + pmEFetchPath + "?" + qp.Encode()
}

// convertPMDate converts a date from YYYY-MM-DD to PubMed's YYYY/MM/DD format.
// Year-only dates are padded to full dates.
func convertPMDate(date string, isEndDate bool) string {
	if len(date) == pmYearOnlyLength {
		if isEndDate {
			return date + pmYearEndPad
		}
		return date + pmYearStartPad
	}
	return strings.ReplaceAll(date, pmISODateSeparator, pmDateSeparator)
}

// mapPMSortOrder maps unified sort orders to PubMed sort parameter values.
func mapPMSortOrder(sort SortOrder) string {
	switch sort {
	case SortRelevance:
		return pmSortRelevance
	case SortDateDesc:
		return pmSortPubDate
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Response mapping
// ---------------------------------------------------------------------------

// mapPMArticleToPublication converts a PubMed article to the unified Publication type.
func mapPMArticleToPublication(article *pmArticle) Publication {
	pmid := article.MedlineCitation.PMID

	pub := Publication{
		ID:          SourcePubMed + prefixedIDSeparator + pmid,
		Source:      SourcePubMed,
		ContentType: ContentTypePaper,
		Title:       article.MedlineCitation.Article.ArticleTitle,
		Abstract:    assemblePMAbstract(article.MedlineCitation.Article.Abstract),
		URL:         pmAbsURLPrefix + pmid,
		Authors:     mapPMAuthors(article.MedlineCitation.Article.AuthorList),
		Published:   assemblePMDate(article.MedlineCitation.Article.Journal.JournalIssue.PubDate),
	}

	// DOI extraction: prefer ELocationID, fall back to ArticleIdList.
	doi := extractPMDOIFromELocation(article.MedlineCitation.Article.ELocationIDs)
	if doi == "" {
		doi = extractPMArticleID(&article.PubmedData, pmArticleIDTypeDOI)
	}
	pub.DOI = doi

	// MeSH terms as categories.
	meshTerms := extractPMMeSHTerms(article.MedlineCitation.MeshHeadingList)
	pub.Categories = meshTerms

	// PMC URL as PDF URL (PMC articles typically have full text).
	pmcID := extractPMArticleID(&article.PubmedData, pmArticleIDTypePMC)
	if pmcID != "" {
		pub.PDFURL = pmPMCURLPrefix + pmcID
	}

	// Source metadata.
	metadata := make(map[string]any)
	metadata[pmMetaKeyPMID] = pmid
	if pmcID != "" {
		metadata[pmMetaKeyPMCID] = pmcID
	}

	journal := article.MedlineCitation.Article.Journal
	if journal.Title != "" {
		metadata[pmMetaKeyJournal] = journal.Title
	}
	if journal.JournalIssue.Volume != "" {
		metadata[pmMetaKeyVolume] = journal.JournalIssue.Volume
	}
	if journal.JournalIssue.Issue != "" {
		metadata[pmMetaKeyIssue] = journal.JournalIssue.Issue
	}
	if journal.ISSN != "" {
		metadata[pmMetaKeyISSN] = journal.ISSN
	}

	// Pages from ELocationID pii or from journal (not directly available in XML struct,
	// but we track it if present in metadata).

	if len(meshTerms) > 0 {
		metadata[pmMetaKeyMeSH] = meshTerms
	}

	pubTypes := article.MedlineCitation.Article.PublicationTypes.Types
	if len(pubTypes) > 0 {
		metadata[pmMetaKeyPubTypes] = pubTypes
	}

	lang := article.MedlineCitation.Article.Language
	if lang != "" {
		metadata[pmMetaKeyLanguage] = lang
	}

	pub.SourceMetadata = metadata

	return pub
}

// assemblePMAbstract joins multiple abstract text sections with newlines.
func assemblePMAbstract(abstract pmAbstract) string {
	if len(abstract.Texts) == 0 {
		return ""
	}
	return strings.Join(abstract.Texts, pmAbstractSectionSeparator)
}

// mapPMAuthors converts PubMed authors to the unified Author type.
func mapPMAuthors(authorList pmAuthorList) []Author {
	result := make([]Author, 0, len(authorList.Authors))
	for _, a := range authorList.Authors {
		name := assemblePMAuthorName(a)
		if name == "" {
			continue
		}

		author := Author{Name: name}
		if len(a.AffiliationInfo) > 0 && a.AffiliationInfo[0].Affiliation != "" {
			author.Affiliation = a.AffiliationInfo[0].Affiliation
		}
		result = append(result, author)
	}
	return result
}

// assemblePMAuthorName creates "ForeName LastName" from author parts.
func assemblePMAuthorName(a pmAuthor) string {
	if a.ForeName != "" && a.LastName != "" {
		return a.ForeName + pmAuthorNameSeparator + a.LastName
	}
	if a.LastName != "" {
		return a.LastName
	}
	return ""
}

// assemblePMDate converts PubMed's structured date to YYYY-MM-DD.
func assemblePMDate(pubDate pmPubDate) string {
	// Prefer structured Year/Month/Day.
	if pubDate.Year != "" {
		month := pmMonthNames[pubDate.Month]
		if month == "" && pubDate.Month != "" {
			// Month might already be numeric.
			month = pubDate.Month
		}

		if month != "" && pubDate.Day != "" {
			day := pubDate.Day
			if len(day) == pmSingleDigitDayLen {
				day = pmDayZeroPad + day
			}
			return pubDate.Year + pmISODateSeparator + month + pmISODateSeparator + day
		}
		if month != "" {
			return pubDate.Year + pmISODateSeparator + month
		}
		return pubDate.Year
	}

	// Fallback: extract year from MedlineDate (e.g., "2024 Jan-Feb").
	if pubDate.MedlineDate != "" && len(pubDate.MedlineDate) >= pmYearOnlyLength {
		return pubDate.MedlineDate[:pmYearOnlyLength]
	}

	return ""
}

// ---------------------------------------------------------------------------
// ArticleID extraction helpers
// ---------------------------------------------------------------------------

// extractPMDOIFromELocation extracts the DOI from ELocationID elements.
func extractPMDOIFromELocation(elocations []pmELocationID) string {
	for _, eloc := range elocations {
		if eloc.EIDType == pmArticleIDTypeDOI {
			return eloc.Value
		}
	}
	return ""
}

// extractPMArticleID extracts an article ID of the given type from PubmedData.
func extractPMArticleID(data *pmPubmedData, idType string) string {
	for _, aid := range data.ArticleIDList.ArticleIDs {
		if aid.IDType == idType {
			return aid.Value
		}
	}
	return ""
}

// extractPMCID extracts the PMC ID from a PubMed article.
func extractPMCID(article *pmArticle) string {
	return extractPMArticleID(&article.PubmedData, pmArticleIDTypePMC)
}

// extractPMMeSHTerms collects descriptor names from MeSH headings.
func extractPMMeSHTerms(meshList pmMeshHeadingList) []string {
	if len(meshList.Headings) == 0 {
		return nil
	}
	terms := make([]string, 0, len(meshList.Headings))
	for _, h := range meshList.Headings {
		if h.DescriptorName != "" {
			terms = append(terms, h.DescriptorName)
		}
	}
	return terms
}

// ---------------------------------------------------------------------------
// Format conversion
// ---------------------------------------------------------------------------

// convertPMFormat applies format conversion on a Publication.
func convertPMFormat(pub *Publication, format ContentFormat) error {
	switch format {
	case FormatJSON:
		return nil // Publication is natively JSON-serializable
	default:
		return fmt.Errorf("%w: %s", ErrFormatUnsupported, format)
	}
}

