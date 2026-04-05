package internal

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// ArXiv plugin identity constants
// ---------------------------------------------------------------------------

const (
	arxivPluginID          = "arxiv"
	arxivPluginName        = "ArXiv"
	arxivPluginDescription = "Open-access preprint server for physics, mathematics, computer science, and more"
)

// ---------------------------------------------------------------------------
// ArXiv API constants
// ---------------------------------------------------------------------------

const (
	arxivDefaultBaseURL    = "http://export.arxiv.org/api/query"
	arxivPDFURLPrefix      = "https://arxiv.org/pdf/"
	arxivAbsURLPrefix      = "https://arxiv.org/abs/"
	arxivMaxResultsPerPage = 2000
	arxivCategoriesHint    = "cs.AI, cs.CL, cs.LG, cs.CV, math.*, physics.*, stat.ML, q-bio.*"
)

// ---------------------------------------------------------------------------
// ArXiv query field prefix constants
// ---------------------------------------------------------------------------

const (
	arxivFieldAll      = "all:"
	arxivFieldTitle    = "ti:"
	arxivFieldAuthor   = "au:"
	arxivFieldCategory = "cat:"
)

// ---------------------------------------------------------------------------
// ArXiv API parameter name constants
// ---------------------------------------------------------------------------

const (
	arxivParamSearchQuery = "search_query"
	arxivParamStart       = "start"
	arxivParamMaxResults  = "max_results"
	arxivParamSortBy      = "sortBy"
	arxivParamSortOrder   = "sortOrder"
	arxivParamIDList      = "id_list"
)

// ---------------------------------------------------------------------------
// ArXiv sort constants
// ---------------------------------------------------------------------------

const (
	arxivSortRelevance  = "relevance"
	arxivSortSubmitted  = "submittedDate"
	arxivSortOrderAsc   = "ascending"
	arxivSortOrderDesc  = "descending"
)

// ---------------------------------------------------------------------------
// ArXiv date format constants
// ---------------------------------------------------------------------------

const (
	arxivDateSubmittedPrefix = "submittedDate:["
	arxivDateSubmittedSuffix = "]"
	arxivDateTimeMinSuffix   = "0000"
	arxivDateTimeMaxSuffix   = "2359"
	arxivDateSeparator       = " TO "
	arxivDateWildcard        = "*"

	// Go reference time layouts for date conversion.
	arxivDateInputLayout     = "2006-01-02" // YYYY-MM-DD input
	arxivDateOutputFormat    = "20060102"    // YYYYMMDD output for ArXiv
	arxivPublishedDateLayout = "2006-01-02"  // YYYY-MM-DD for Publication.Published

	arxivYearOnlyLength = 4
	arxivYearStartPad   = "0101" // January 1st for year-only date_from
	arxivYearEndPad     = "1231" // December 31st for year-only date_to
)

// ---------------------------------------------------------------------------
// ArXiv query operator constants
// ---------------------------------------------------------------------------

const (
	arxivQueryAND = "+AND+"
)

// ---------------------------------------------------------------------------
// ArXiv ID extraction constants
// ---------------------------------------------------------------------------

const (
	arxivIDURLPrefix   = "http://arxiv.org/abs/"
	arxivVersionPrefix = "v"
)

// ---------------------------------------------------------------------------
// ArXiv metadata key constants
// ---------------------------------------------------------------------------

const (
	arxivMetaKeyComment         = "arxiv_comment"
	arxivMetaKeyJournalRef      = "arxiv_journal_ref"
	arxivMetaKeyPrimaryCategory = "arxiv_primary_category"
)

// ---------------------------------------------------------------------------
// ArXiv BibTeX constants
// ---------------------------------------------------------------------------

const arxivBibTeXTemplate = `@article{%s,
  title         = {%s},
  author        = {%s},
  year          = {%s},
  eprint        = {%s},
  archivePrefix = {arXiv},
  primaryClass  = {%s},
  url           = {%s}
}`

const arxivBibTeXAuthorSeparator = " and "

// ---------------------------------------------------------------------------
// ArXiv HTTP constants
// ---------------------------------------------------------------------------

const (
	arxivGetMaxResults     = 1
	arxivHTTPStatusErrFmt  = "status %d"
	arxivQueryPartsInitCap = 4 // capacity hint: query + title + author + category
)

// ---------------------------------------------------------------------------
// ArXiv error message constants
// ---------------------------------------------------------------------------

const (
	ErrMsgArxivXMLParse    = "failed to parse arxiv xml response"
	ErrMsgArxivHTTPRequest = "arxiv http request failed"
	ErrMsgArxivNotFound    = "arxiv entry not found"
	ErrMsgArxivEmptyQuery  = "search query is empty"
)

// ---------------------------------------------------------------------------
// ArXiv sentinel errors
// ---------------------------------------------------------------------------

var (
	ErrArxivXMLParse    = fmt.Errorf("%s", ErrMsgArxivXMLParse)
	ErrArxivHTTPRequest = fmt.Errorf("%s", ErrMsgArxivHTTPRequest)
	ErrArxivNotFound    = fmt.Errorf("%s", ErrMsgArxivNotFound)
	ErrArxivEmptyQuery  = fmt.Errorf("%s", ErrMsgArxivEmptyQuery)
)

// ---------------------------------------------------------------------------
// ArXiv XML struct definitions (Atom 1.0 feed)
// Namespaces: Atom="http://www.w3.org/2005/Atom",
// OpenSearch="http://a9.com/-/spec/opensearch/1.1/",
// ArXiv="http://arxiv.org/schemas/atom"
// ---------------------------------------------------------------------------

// arxivFeed represents the top-level Atom feed returned by the ArXiv API.
type arxivFeed struct {
	XMLName      xml.Name     `xml:"http://www.w3.org/2005/Atom feed"`
	TotalResults int          `xml:"http://a9.com/-/spec/opensearch/1.1/ totalResults"`
	StartIndex   int          `xml:"http://a9.com/-/spec/opensearch/1.1/ startIndex"`
	ItemsPerPage int          `xml:"http://a9.com/-/spec/opensearch/1.1/ itemsPerPage"`
	Entries      []arxivEntry `xml:"http://www.w3.org/2005/Atom entry"`
}

// arxivEntry represents a single Atom entry in the ArXiv feed.
type arxivEntry struct {
	ID              string          `xml:"http://www.w3.org/2005/Atom id"`
	Title           string          `xml:"http://www.w3.org/2005/Atom title"`
	Summary         string          `xml:"http://www.w3.org/2005/Atom summary"`
	Published       string          `xml:"http://www.w3.org/2005/Atom published"`
	Updated         string          `xml:"http://www.w3.org/2005/Atom updated"`
	Authors         []arxivAuthor   `xml:"http://www.w3.org/2005/Atom author"`
	Links           []arxivLink     `xml:"http://www.w3.org/2005/Atom link"`
	Categories      []arxivCategory `xml:"http://www.w3.org/2005/Atom category"`
	DOI             string          `xml:"http://arxiv.org/schemas/atom doi"`
	Comment         string          `xml:"http://arxiv.org/schemas/atom comment"`
	JournalRef      string          `xml:"http://arxiv.org/schemas/atom journal_ref"`
	PrimaryCategory arxivCategory   `xml:"http://arxiv.org/schemas/atom primary_category"`
}

// arxivAuthor represents an author element in the Atom feed.
type arxivAuthor struct {
	Name        string `xml:"http://www.w3.org/2005/Atom name"`
	Affiliation string `xml:"http://arxiv.org/schemas/atom affiliation"`
}

// arxivLink represents a link element with attributes.
type arxivLink struct {
	Href  string `xml:"href,attr"`
	Rel   string `xml:"rel,attr"`
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr"`
}

// arxivCategory represents a category element with attributes.
type arxivCategory struct {
	Term   string `xml:"term,attr"`
	Scheme string `xml:"scheme,attr"`
}

// ---------------------------------------------------------------------------
// ArXivPlugin struct
// ---------------------------------------------------------------------------

// ArXivPlugin implements SourcePlugin for the ArXiv preprint server.
// Thread-safe for concurrent use after Initialize.
type ArXivPlugin struct {
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
func (p *ArXivPlugin) ID() string { return arxivPluginID }

// Name returns a human-readable name.
func (p *ArXivPlugin) Name() string { return arxivPluginName }

// Description returns a short description for LLM context.
func (p *ArXivPlugin) Description() string { return arxivPluginDescription }

// ContentTypes returns the types of content this source provides.
func (p *ArXivPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat returns the default content format.
func (p *ArXivPlugin) NativeFormat() ContentFormat { return FormatXML }

// AvailableFormats returns all formats this source can provide.
func (p *ArXivPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatXML, FormatJSON, FormatBibTeX}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Capabilities
// ---------------------------------------------------------------------------

// Capabilities reports what filtering, sorting, and features ArXiv supports.
func (p *ArXivPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     true,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       arxivMaxResultsPerPage,
		CategoriesHint:           arxivCategoriesHint,
		NativeFormat:             FormatXML,
		AvailableFormats:         []ContentFormat{FormatXML, FormatJSON, FormatBibTeX},
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Initialize
// ---------------------------------------------------------------------------

// Initialize sets up the ArXiv plugin with the given configuration.
// Called once at startup.
func (p *ArXivPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = arxivDefaultBaseURL
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
func (p *ArXivPlugin) Health(_ context.Context) SourceHealth {
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

// Search executes a search query against the ArXiv API.
func (p *ArXivPlugin) Search(ctx context.Context, params SearchParams, _ *CallCredentials) (*SearchResult, error) {
	query, err := buildArxivQuery(params)
	if err != nil {
		return nil, err
	}

	reqURL := buildArxivSearchURL(p.baseURL, query, params.Offset, params.Limit, params.Sort)

	feed, err := p.doRequest(ctx, reqURL)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}

	p.recordSuccess()

	pubs := make([]Publication, 0, len(feed.Entries))
	for i := range feed.Entries {
		pubs = append(pubs, mapArxivEntryToPublication(&feed.Entries[i]))
	}

	hasMore := (feed.StartIndex + len(feed.Entries)) < feed.TotalResults

	return &SearchResult{
		Total:   feed.TotalResults,
		Results: pubs,
		HasMore: hasMore,
	}, nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Get
// ---------------------------------------------------------------------------

// Get retrieves a single publication by its ArXiv ID.
func (p *ArXivPlugin) Get(ctx context.Context, id string, _ []IncludeField, format ContentFormat, _ *CallCredentials) (*Publication, error) {
	reqURL := buildArxivGetURL(p.baseURL, id)

	feed, err := p.doRequest(ctx, reqURL)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	if len(feed.Entries) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrArxivNotFound, id)
	}

	p.recordSuccess()

	pub := mapArxivEntryToPublication(&feed.Entries[0])

	// Apply format conversion if not native XML.
	if format != FormatNative && format != FormatXML {
		if err := convertArxivFormat(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

// doRequest executes an HTTP GET and decodes the XML response.
func (p *ArXivPlugin) doRequest(ctx context.Context, reqURL string) (*arxivFeed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrArxivHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return nil, fmt.Errorf("%w: %w", ErrArxivHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: "+arxivHTTPStatusErrFmt, ErrArxivHTTPRequest, resp.StatusCode)
	}

	var feed arxivFeed
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrArxivXMLParse, err)
	}

	return &feed, nil
}

// ---------------------------------------------------------------------------
// Health state helpers
// ---------------------------------------------------------------------------

func (p *ArXivPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *ArXivPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	p.lastError = err.Error()
}

// ---------------------------------------------------------------------------
// Query building
// ---------------------------------------------------------------------------

// buildArxivQuery translates SearchParams into an ArXiv query string.
func buildArxivQuery(params SearchParams) (string, error) {
	parts := make([]string, 0, arxivQueryPartsInitCap)

	if params.Query != "" {
		parts = append(parts, arxivFieldAll+params.Query)
	}

	if params.Filters.Title != "" {
		parts = append(parts, arxivFieldTitle+params.Filters.Title)
	}

	for _, author := range params.Filters.Authors {
		parts = append(parts, arxivFieldAuthor+author)
	}

	for _, cat := range params.Filters.Categories {
		parts = append(parts, arxivFieldCategory+cat)
	}

	if len(parts) == 0 {
		return "", ErrArxivEmptyQuery
	}

	query := strings.Join(parts, arxivQueryAND)

	dateFilter := buildArxivDateFilter(params.Filters.DateFrom, params.Filters.DateTo)
	if dateFilter != "" {
		query += arxivQueryAND + dateFilter
	}

	return query, nil
}

// buildArxivDateFilter constructs the submittedDate range filter.
func buildArxivDateFilter(dateFrom, dateTo string) string {
	if dateFrom == "" && dateTo == "" {
		return ""
	}

	from := arxivDateWildcard
	to := arxivDateWildcard

	if dateFrom != "" {
		from = convertDateToArxivFormat(dateFrom, false) + arxivDateTimeMinSuffix
	}
	if dateTo != "" {
		to = convertDateToArxivFormat(dateTo, true) + arxivDateTimeMaxSuffix
	}

	return arxivDateSubmittedPrefix + from + arxivDateSeparator + to + arxivDateSubmittedSuffix
}

// convertDateToArxivFormat converts YYYY-MM-DD or YYYY to YYYYMMDD format.
// For year-only input, pads with 0101 (start) or 1231 (end) depending on isEndDate.
func convertDateToArxivFormat(date string, isEndDate bool) string {
	if len(date) == arxivYearOnlyLength {
		if isEndDate {
			return date + arxivYearEndPad
		}
		return date + arxivYearStartPad
	}

	t, err := time.Parse(arxivDateInputLayout, date)
	if err != nil {
		return date // return as-is if unparseable
	}
	return t.Format(arxivDateOutputFormat)
}

// buildArxivSearchURL assembles the full search URL with query parameters.
func buildArxivSearchURL(baseURL, query string, offset, limit int, sort SortOrder) string {
	params := url.Values{}
	params.Set(arxivParamSearchQuery, query)
	params.Set(arxivParamStart, strconv.Itoa(offset))
	params.Set(arxivParamMaxResults, strconv.Itoa(limit))

	sortBy, sortOrder := mapArxivSortOrder(sort)
	if sortBy != "" {
		params.Set(arxivParamSortBy, sortBy)
		if sortOrder != "" {
			params.Set(arxivParamSortOrder, sortOrder)
		}
	}

	return baseURL + "?" + params.Encode()
}

// buildArxivGetURL assembles the URL for fetching a single entry by ID.
func buildArxivGetURL(baseURL, id string) string {
	params := url.Values{}
	params.Set(arxivParamIDList, id)
	params.Set(arxivParamMaxResults, strconv.Itoa(arxivGetMaxResults))
	return baseURL + "?" + params.Encode()
}

// mapArxivSortOrder translates our SortOrder to ArXiv sort parameters.
func mapArxivSortOrder(sort SortOrder) (sortBy, sortOrder string) {
	switch sort {
	case SortRelevance:
		return arxivSortRelevance, ""
	case SortDateDesc:
		return arxivSortSubmitted, arxivSortOrderDesc
	case SortDateAsc:
		return arxivSortSubmitted, arxivSortOrderAsc
	default:
		return "", ""
	}
}

// ---------------------------------------------------------------------------
// XML to Publication mapping
// ---------------------------------------------------------------------------

// mapArxivEntryToPublication converts an ArXiv Atom entry to a Publication.
func mapArxivEntryToPublication(entry *arxivEntry) Publication {
	arxivID := extractArxivID(entry.ID)

	pub := Publication{
		ID:          SourceArXiv + prefixedIDSeparator + arxivID,
		Source:      SourceArXiv,
		ContentType: ContentTypePaper,
		Title:       cleanArxivText(entry.Title),
		Abstract:    cleanArxivText(entry.Summary),
		ArXivID:     arxivID,
		URL:         arxivAbsURLPrefix + arxivID,
		PDFURL:      arxivPDFURLPrefix + arxivID,
		Authors:     mapArxivAuthors(entry.Authors),
		Categories:  mapArxivCategories(entry.Categories),
		Published:   parseArxivDate(entry.Published),
		Updated:     parseArxivDate(entry.Updated),
	}

	if entry.DOI != "" {
		pub.DOI = entry.DOI
	}

	metadata := make(map[string]any)
	if entry.Comment != "" {
		metadata[arxivMetaKeyComment] = entry.Comment
	}
	if entry.JournalRef != "" {
		metadata[arxivMetaKeyJournalRef] = entry.JournalRef
	}
	if entry.PrimaryCategory.Term != "" {
		metadata[arxivMetaKeyPrimaryCategory] = entry.PrimaryCategory.Term
	}
	if len(metadata) > 0 {
		pub.SourceMetadata = metadata
	}

	return pub
}

// extractArxivID extracts the clean ArXiv ID from an Atom <id> URL.
// Handles both new-style (2401.12345v1) and old-style (hep-th/9901001v1).
func extractArxivID(atomID string) string {
	id := strings.TrimPrefix(atomID, arxivIDURLPrefix)

	// Strip version suffix (e.g., "v1", "v2").
	if idx := strings.LastIndex(id, arxivVersionPrefix); idx > 0 {
		suffix := id[idx+1:]
		if _, err := strconv.Atoi(suffix); err == nil {
			id = id[:idx]
		}
	}

	return id
}

// cleanArxivText normalizes whitespace and newlines in ArXiv text fields.
func cleanArxivText(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

// parseArxivDate converts an RFC3339 date string to YYYY-MM-DD format.
func parseArxivDate(raw string) string {
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.Format(arxivPublishedDateLayout)
}

// mapArxivAuthors converts ArXiv Atom authors to the unified Author type.
func mapArxivAuthors(authors []arxivAuthor) []Author {
	result := make([]Author, len(authors))
	for i, a := range authors {
		result[i] = Author{
			Name:        a.Name,
			Affiliation: a.Affiliation,
		}
	}
	return result
}

// mapArxivCategories extracts category terms from ArXiv Atom categories.
func mapArxivCategories(cats []arxivCategory) []string {
	result := make([]string, len(cats))
	for i, c := range cats {
		result[i] = c.Term
	}
	return result
}

// ---------------------------------------------------------------------------
// Format conversion
// ---------------------------------------------------------------------------

// convertArxivFormat applies format conversion on a Publication.
func convertArxivFormat(pub *Publication, format ContentFormat) error {
	switch format {
	case FormatJSON:
		return nil // Publication is natively JSON-serializable
	case FormatBibTeX:
		bibtex := assembleBibTeX(pub)
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

// assembleBibTeX creates a BibTeX entry from Publication metadata.
func assembleBibTeX(pub *Publication) string {
	authorNames := make([]string, len(pub.Authors))
	for i, a := range pub.Authors {
		authorNames[i] = a.Name
	}

	year := ""
	if len(pub.Published) >= arxivYearOnlyLength {
		year = pub.Published[:arxivYearOnlyLength]
	}

	primaryCat := ""
	if len(pub.Categories) > 0 {
		primaryCat = pub.Categories[0]
	}

	return fmt.Sprintf(arxivBibTeXTemplate,
		pub.ArXivID,
		pub.Title,
		strings.Join(authorNames, arxivBibTeXAuthorSeparator),
		year,
		pub.ArXivID,
		primaryCat,
		pub.URL,
	)
}
