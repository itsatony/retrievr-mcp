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
)

// ---------------------------------------------------------------------------
// ADS plugin identity constants
// ---------------------------------------------------------------------------

const (
	adsPluginID          = "ads"
	adsPluginName        = "NASA ADS"
	adsPluginDescription = "NASA Astrophysics Data System — 16M+ records in astronomy, " +
		"astrophysics, planetary science, and related physics (API key required)"
)

// ---------------------------------------------------------------------------
// ADS API base URL and path constants
// ---------------------------------------------------------------------------

const (
	adsDefaultBaseURL    = "https://api.adsabs.harvard.edu/v1"
	adsSearchPath        = "/search/query"
	adsMaxResultsPerPage = 200
	adsMaxResponseBytes  = 10 << 20 // 10 MB
)

// ---------------------------------------------------------------------------
// ADS API parameter constants
// ---------------------------------------------------------------------------

const (
	adsParamQuery  = "q"
	adsParamFields = "fl"
	adsParamRows   = "rows"
	adsParamStart  = "start"
	adsParamSort   = "sort"
)

// ---------------------------------------------------------------------------
// ADS default field list
// ---------------------------------------------------------------------------

const (
	adsDefaultFields = "bibcode,title,author,abstract,pubdate,doi," +
		"citation_count,year,pub,volume,issue,page,identifier,aff,orcid_pub"
)

// ---------------------------------------------------------------------------
// ADS auth constants
// ---------------------------------------------------------------------------

const (
	adsAuthHeader = "Authorization"
	adsAuthPrefix = "Bearer "
)

// ---------------------------------------------------------------------------
// ADS sort value constants
// ---------------------------------------------------------------------------

const (
	adsSortRelevance     = "score desc"
	adsSortDateDesc      = "date desc"
	adsSortDateAsc       = "date asc"
	adsSortCitationsDesc = "citation_count desc"
)

// ---------------------------------------------------------------------------
// ADS date filter constants
// ---------------------------------------------------------------------------

const (
	adsDateFilterPrefix = "pubdate:["
	adsDateFilterTo     = " TO "
	adsDateFilterSuffix = "]"
	adsDateWildcard     = "*"
)

// ---------------------------------------------------------------------------
// ADS URL and query constants
// ---------------------------------------------------------------------------

const (
	adsAbsURLPrefix       = "https://ui.adsabs.harvard.edu/abs/"
	adsQueryBibcodePrefix = "bibcode:"
	adsGetRowCount        = 1
	adsEmptyFieldMarker   = "-"
	adsQuerySeparator     = " "
)

// ---------------------------------------------------------------------------
// ADS HTTP error format
// ---------------------------------------------------------------------------

const adsHTTPStatusErrFmt = "status %d"

// ---------------------------------------------------------------------------
// ADS pubdate cleanup constants
// ---------------------------------------------------------------------------

const (
	adsPubdateZeroSuffix = "-00"
	adsPubdateMinLen     = 4
)

// ---------------------------------------------------------------------------
// ADS ORCID URL prefix (to strip from identifiers)
// ---------------------------------------------------------------------------

const adsOrcidPrefix = "https://orcid.org/"

// ---------------------------------------------------------------------------
// ADS metadata key constants
// ---------------------------------------------------------------------------

const (
	adsMetaKeyJournal     = "ads_journal"
	adsMetaKeyBibcode     = "ads_bibcode"
	adsMetaKeyVolume      = "ads_volume"
	adsMetaKeyIssue       = "ads_issue"
	adsMetaKeyPage        = "ads_page"
	adsMetaKeyIdentifiers = "ads_identifiers"
	adsMetaKeyYear        = "ads_year"
)

// ---------------------------------------------------------------------------
// ADS categories hint
// ---------------------------------------------------------------------------

const adsCategoriesHint = "Astronomy, Astrophysics, Planetary Science, " +
	"Solar Physics, Space Science, Cosmology, High-Energy Physics, " +
	"Instrumentation, General Physics"

// ---------------------------------------------------------------------------
// ADS response structs
// ---------------------------------------------------------------------------

type adsSearchResponse struct {
	Response adsResponseBody `json:"response"`
}

type adsResponseBody struct {
	NumFound int      `json:"numFound"`
	Start    int      `json:"start"`
	Docs     []adsDoc `json:"docs"`
}

type adsDoc struct {
	Bibcode       string   `json:"bibcode"`
	Title         []string `json:"title"`
	Author        []string `json:"author"`
	Abstract      string   `json:"abstract"`
	Pubdate       string   `json:"pubdate"`
	DOI           []string `json:"doi"`
	CitationCount int      `json:"citation_count"`
	Year          string   `json:"year"`
	Pub           string   `json:"pub"`
	Volume        string   `json:"volume"`
	Issue         string   `json:"issue"`
	Page          []string `json:"page"`
	Identifier    []string `json:"identifier"`
	Aff           []string `json:"aff"`
	OrcidPub      []string `json:"orcid_pub"`
}

// ---------------------------------------------------------------------------
// ADSPlugin struct
// ---------------------------------------------------------------------------

// ADSPlugin implements SourcePlugin for the NASA Astrophysics Data System.
// Thread-safe: health state protected by sync.RWMutex.
type ADSPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: identity methods
// ---------------------------------------------------------------------------

func (p *ADSPlugin) ID() string                  { return adsPluginID }
func (p *ADSPlugin) Name() string                { return adsPluginName }
func (p *ADSPlugin) Description() string         { return adsPluginDescription }
func (p *ADSPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }
func (p *ADSPlugin) NativeFormat() ContentFormat { return FormatJSON }
func (p *ADSPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Capabilities
// ---------------------------------------------------------------------------

func (p *ADSPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    true,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       adsMaxResultsPerPage,
		CategoriesHint:           adsCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Initialize
// ---------------------------------------------------------------------------

func (p *ADSPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = adsDefaultBaseURL
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

func (p *ADSPlugin) Health(_ context.Context) SourceHealth {
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

func (p *ADSPlugin) Search(ctx context.Context, params SearchParams, creds *CallCredentials) (*SearchResult, error) {
	if params.Query == "" {
		return nil, ErrADSEmptyQuery
	}

	apiKey := resolveADSAPIKey(creds, p.apiKey)
	reqURL := buildADSSearchURL(p.baseURL, params)

	var response adsSearchResponse
	if err := p.doRequest(ctx, reqURL, apiKey, &response); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}

	p.recordSuccess()

	pubs := make([]Publication, 0, len(response.Response.Docs))
	for i := range response.Response.Docs {
		pubs = append(pubs, mapADSDocToPublication(&response.Response.Docs[i]))
	}

	hasMore := response.Response.Start+len(response.Response.Docs) < response.Response.NumFound

	return &SearchResult{
		Total:   response.Response.NumFound,
		Results: pubs,
		HasMore: hasMore,
	}, nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Get
// ---------------------------------------------------------------------------

func (p *ADSPlugin) Get(ctx context.Context, id string, _ []IncludeField, format ContentFormat, creds *CallCredentials) (*Publication, error) {
	apiKey := resolveADSAPIKey(creds, p.apiKey)
	reqURL := buildADSGetURL(p.baseURL, id)

	var response adsSearchResponse
	if err := p.doRequest(ctx, reqURL, apiKey, &response); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	if len(response.Response.Docs) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrADSNotFound, id)
	}

	p.recordSuccess()

	pub := mapADSDocToPublication(&response.Response.Docs[0])

	if format != FormatNative && format != FormatJSON {
		if err := convertADSFormat(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

func (p *ADSPlugin) doRequest(ctx context.Context, reqURL, apiKey string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrADSHTTPRequest, err)
	}

	if apiKey != "" {
		req.Header.Set(adsAuthHeader, adsAuthPrefix+apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return fmt.Errorf("%w: %w", ErrADSHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrADSNotFound
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%w: "+adsHTTPStatusErrFmt, ErrADSHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(adsMaxResponseBytes))
	if err := json.NewDecoder(limitedBody).Decode(target); err != nil {
		return fmt.Errorf("%w: %w", ErrADSJSONParse, err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Health state helpers
// ---------------------------------------------------------------------------

func (p *ADSPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *ADSPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	p.lastError = err.Error()
}

// ---------------------------------------------------------------------------
// Credential resolution
// ---------------------------------------------------------------------------

func resolveADSAPIKey(creds *CallCredentials, serverDefault string) string {
	if creds != nil {
		return creds.ResolveForSource(SourceADS, serverDefault)
	}
	return serverDefault
}

// ---------------------------------------------------------------------------
// URL building
// ---------------------------------------------------------------------------

func buildADSSearchURL(baseURL string, params SearchParams) string {
	qParams := url.Values{}

	// Build the query string, potentially with date filter embedded.
	query := params.Query
	if params.Filters.DateFrom != "" || params.Filters.DateTo != "" {
		dateFilter := buildADSDateFilter(params.Filters.DateFrom, params.Filters.DateTo)
		query = query + adsQuerySeparator + dateFilter
	}
	qParams.Set(adsParamQuery, query)

	qParams.Set(adsParamFields, adsDefaultFields)

	rows := params.Limit
	if rows <= 0 || rows > adsMaxResultsPerPage {
		rows = adsMaxResultsPerPage
	}
	qParams.Set(adsParamRows, strconv.Itoa(rows))

	offset := params.Offset
	if offset < 0 {
		offset = 0
	}
	qParams.Set(adsParamStart, strconv.Itoa(offset))

	sortStr := mapADSSortOrder(params.Sort)
	if sortStr != "" {
		qParams.Set(adsParamSort, sortStr)
	}

	return baseURL + adsSearchPath + "?" + qParams.Encode()
}

func buildADSGetURL(baseURL, bibcode string) string {
	qParams := url.Values{}
	qParams.Set(adsParamQuery, adsQueryBibcodePrefix+bibcode)
	qParams.Set(adsParamFields, adsDefaultFields)
	qParams.Set(adsParamRows, strconv.Itoa(adsGetRowCount))

	return baseURL + adsSearchPath + "?" + qParams.Encode()
}

func buildADSDateFilter(from, to string) string {
	fromVal := adsDateWildcard
	toVal := adsDateWildcard

	if from != "" {
		fromVal = from
	}
	if to != "" {
		toVal = to
	}

	return adsDateFilterPrefix + fromVal + adsDateFilterTo + toVal + adsDateFilterSuffix
}

func mapADSSortOrder(sort SortOrder) string {
	switch sort {
	case SortRelevance:
		return adsSortRelevance
	case SortDateDesc:
		return adsSortDateDesc
	case SortDateAsc:
		return adsSortDateAsc
	case SortCitations:
		return adsSortCitationsDesc
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Response mapping
// ---------------------------------------------------------------------------

func mapADSDocToPublication(doc *adsDoc) Publication {
	pub := Publication{
		ID:          SourceADS + prefixedIDSeparator + doc.Bibcode,
		Source:      SourceADS,
		ContentType: ContentTypePaper,
		Abstract:    doc.Abstract,
	}

	// Title — array, take first.
	if len(doc.Title) > 0 {
		pub.Title = doc.Title[0]
	}

	// DOI — array, take first.
	if len(doc.DOI) > 0 {
		pub.DOI = doc.DOI[0]
	}

	// Published — clean pubdate (strip trailing -00).
	pub.Published = cleanADSPubdate(doc.Pubdate)

	// Citation count.
	citCount := doc.CitationCount
	pub.CitationCount = &citCount

	// URL — construct from bibcode.
	pub.URL = adsAbsURLPrefix + doc.Bibcode

	// Authors — zip author[], aff[], orcid_pub[] parallel arrays.
	pub.Authors = mapADSAuthors(doc.Author, doc.Aff, doc.OrcidPub)

	// Source metadata.
	metadata := make(map[string]any)
	metadata[adsMetaKeyBibcode] = doc.Bibcode
	metadata[adsMetaKeyYear] = doc.Year

	if doc.Pub != "" {
		metadata[adsMetaKeyJournal] = doc.Pub
	}
	if doc.Volume != "" {
		metadata[adsMetaKeyVolume] = doc.Volume
	}
	if doc.Issue != "" {
		metadata[adsMetaKeyIssue] = doc.Issue
	}
	if len(doc.Page) > 0 {
		metadata[adsMetaKeyPage] = doc.Page[0]
	}
	if len(doc.Identifier) > 0 {
		metadata[adsMetaKeyIdentifiers] = doc.Identifier
	}

	pub.SourceMetadata = metadata

	return pub
}

func mapADSAuthors(names, affs, orcids []string) []Author {
	authors := make([]Author, len(names))
	for i, name := range names {
		authors[i] = Author{Name: name}

		if i < len(affs) && affs[i] != "" && affs[i] != adsEmptyFieldMarker {
			authors[i].Affiliation = affs[i]
		}

		if i < len(orcids) && orcids[i] != "" && orcids[i] != adsEmptyFieldMarker {
			authors[i].ORCID = strings.TrimPrefix(orcids[i], adsOrcidPrefix)
		}
	}
	return authors
}

func cleanADSPubdate(pubdate string) string {
	if len(pubdate) < adsPubdateMinLen {
		return pubdate
	}
	// Strip trailing "-00" segments (e.g., "2024-01-00" → "2024-01").
	for strings.HasSuffix(pubdate, adsPubdateZeroSuffix) {
		pubdate = pubdate[:len(pubdate)-len(adsPubdateZeroSuffix)]
	}
	return pubdate
}

// ---------------------------------------------------------------------------
// Format conversion
// ---------------------------------------------------------------------------

func convertADSFormat(_ *Publication, format ContentFormat) error {
	switch format {
	case FormatJSON:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrFormatUnsupported, format)
	}
}
