package internal

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
)

// ---------------------------------------------------------------------------
// DBLP plugin identity constants
// ---------------------------------------------------------------------------

const (
	dblpPluginID          = "dblp"
	dblpPluginName        = "DBLP"
	dblpPluginDescription = "Computer science bibliography with 7M+ publications from conferences, journals, and workshops"
)

// ---------------------------------------------------------------------------
// DBLP API constants
// ---------------------------------------------------------------------------

const (
	dblpDefaultBaseURL    = "https://dblp.org"
	dblpSearchPath        = "/search/publ/api"
	dblpMaxResultsPerPage = 1000
	dblpMaxResponseBytes  = 10 << 20 // 10 MB upper bound
)

// ---------------------------------------------------------------------------
// DBLP query parameter constants
// ---------------------------------------------------------------------------

const (
	dblpParamQuery  = "q"
	dblpParamFormat = "format"
	dblpParamHits   = "h"
	dblpParamFirst  = "f"
	dblpFormatJSON  = "json"
)

// ---------------------------------------------------------------------------
// DBLP get key prefix
// ---------------------------------------------------------------------------

// dblpRecPath is the direct record lookup endpoint (XML only, JSON not supported).
const dblpRecPath = "/rec/"

// dblpRecXMLSuffix is appended to the key for XML format.
const dblpRecXMLSuffix = ".xml"

// ---------------------------------------------------------------------------
// DBLP HTTP constants
// ---------------------------------------------------------------------------

const dblpHTTPStatusErrFmt = "status %d"

// ---------------------------------------------------------------------------
// DBLP metadata key constants
// ---------------------------------------------------------------------------

const (
	dblpMetaKeyVenue   = "dblp_venue"
	dblpMetaKeyType    = "dblp_type"
	dblpMetaKeyKey     = "dblp_key"
	dblpMetaKeyEE      = "dblp_electronic_edition"
	dblpMetaKeyDBLPURL = "dblp_url"
)

// ---------------------------------------------------------------------------
// DBLP categories hint
// ---------------------------------------------------------------------------

const dblpCategoriesHint = "Computer Science — conferences (NeurIPS, ICML, ACL, CVPR), journals (JACM, TPAMI), workshops, preprints"

// ---------------------------------------------------------------------------
// DBLP pagination constants
// ---------------------------------------------------------------------------

const (
	dblpFirstOffset = 0
)

// ---------------------------------------------------------------------------
// DBLP JSON response struct definitions
// ---------------------------------------------------------------------------

// dblpRecXMLResponse represents the XML response from /rec/{key}.xml.
// The root <dblp> element contains one article/inproceedings/etc child.
type dblpRecXMLResponse struct {
	XMLName       xml.Name         `xml:"dblp"`
	Article       *dblpRecXMLEntry `xml:"article"`
	InProceedings *dblpRecXMLEntry `xml:"inproceedings"`
	Proceedings   *dblpRecXMLEntry `xml:"proceedings"`
	Book          *dblpRecXMLEntry `xml:"book"`
	Incollection  *dblpRecXMLEntry `xml:"incollection"`
	PhdThesis     *dblpRecXMLEntry `xml:"phdthesis"`
	MastersThesis *dblpRecXMLEntry `xml:"mastersthesis"`
}

// dblpRecXMLEntry holds the fields common to all DBLP record types.
type dblpRecXMLEntry struct {
	Key       string   `xml:"key,attr"`
	Title     string   `xml:"title"`
	Authors   []string `xml:"author"`
	Year      string   `xml:"year"`
	Journal   string   `xml:"journal"`
	Booktitle string   `xml:"booktitle"`
	Volume    string   `xml:"volume"`
	Pages     string   `xml:"pages"`
	EE        string   `xml:"ee"`
	URL       string   `xml:"url"`
	DOI       string   `xml:"doi"`
}

// entry returns the first non-nil entry from the XML response.
func (r *dblpRecXMLResponse) entry() *dblpRecXMLEntry {
	switch {
	case r.Article != nil:
		return r.Article
	case r.InProceedings != nil:
		return r.InProceedings
	case r.Proceedings != nil:
		return r.Proceedings
	case r.Book != nil:
		return r.Book
	case r.Incollection != nil:
		return r.Incollection
	case r.PhdThesis != nil:
		return r.PhdThesis
	case r.MastersThesis != nil:
		return r.MastersThesis
	default:
		return nil
	}
}

// dblpSearchResponse represents the top-level search response from the DBLP API.
type dblpSearchResponse struct {
	Result dblpResult `json:"result"`
}

// dblpResult represents the result wrapper in a DBLP response.
type dblpResult struct {
	Hits dblpHits `json:"hits"`
}

// dblpHits represents the hits section of a DBLP response.
// Note: @total is a string in the DBLP API, not an integer.
type dblpHits struct {
	Total string    `json:"@total"`
	Hit   []dblpHit `json:"hit"`
}

// dblpHit represents a single hit in the DBLP search results.
type dblpHit struct {
	Info dblpInfo `json:"info"`
}

// dblpInfo represents the info section of a DBLP hit.
type dblpInfo struct {
	Key     string       `json:"key"`
	Title   string       `json:"title"`
	Authors *dblpAuthors `json:"authors"`
	Venue   string       `json:"venue"`
	Year    string       `json:"year"`
	Type    string       `json:"type"`
	DOI     string       `json:"doi"`
	EE      string       `json:"ee"`
	URL     string       `json:"url"`
}

// dblpAuthor represents a single author in a DBLP response.
type dblpAuthor struct {
	Text string `json:"text"`
	PID  string `json:"@pid"`
}

// dblpAuthors handles the polymorphic author field in DBLP responses.
// The "author" field can be a single JSON object or an array of objects.
type dblpAuthors struct {
	Author []dblpAuthor
}

// UnmarshalJSON implements custom JSON unmarshaling for the polymorphic author field.
// DBLP can return authors as either a single object or an array.
func (a *dblpAuthors) UnmarshalJSON(data []byte) error {
	// Try array first (direct array of authors).
	var arr []dblpAuthor
	if err := json.Unmarshal(data, &arr); err == nil {
		a.Author = arr
		return nil
	}

	// Try wrapper object: {"author": ...}
	var wrapper struct {
		Author json.RawMessage `json:"author"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}

	// Try array inside wrapper.
	if err := json.Unmarshal(wrapper.Author, &arr); err == nil {
		a.Author = arr
		return nil
	}

	// Try single object inside wrapper.
	var single dblpAuthor
	if err := json.Unmarshal(wrapper.Author, &single); err != nil {
		return err
	}
	a.Author = []dblpAuthor{single}
	return nil
}

// ---------------------------------------------------------------------------
// DBLPPlugin struct
// ---------------------------------------------------------------------------

// DBLPPlugin implements SourcePlugin for DBLP.
// Thread-safe for concurrent use after Initialize.
type DBLPPlugin struct {
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
func (p *DBLPPlugin) ID() string { return dblpPluginID }

// Name returns a human-readable name.
func (p *DBLPPlugin) Name() string { return dblpPluginName }

// Description returns a short description for LLM context.
func (p *DBLPPlugin) Description() string { return dblpPluginDescription }

// ContentTypes returns the types of content this source provides.
func (p *DBLPPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat returns the default content format.
func (p *DBLPPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats returns all formats this source can provide.
func (p *DBLPPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Capabilities
// ---------------------------------------------------------------------------

// Capabilities reports what filtering, sorting, and features DBLP supports.
func (p *DBLPPlugin) Capabilities() SourceCapabilities {
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
		SupportsPagination:       true,
		MaxResultsPerQuery:       dblpMaxResultsPerPage,
		CategoriesHint:           dblpCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Initialize
// ---------------------------------------------------------------------------

// Initialize sets up the DBLP plugin with the given configuration.
// Called once at startup.
func (p *DBLPPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = dblpDefaultBaseURL
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
func (p *DBLPPlugin) Health(_ context.Context) SourceHealth {
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

// Search executes a search query against the DBLP publication search API.
func (p *DBLPPlugin) Search(ctx context.Context, params SearchParams, _ *CallCredentials) (*SearchResult, error) {
	if params.Query == "" {
		return nil, ErrDBLPEmptyQuery
	}

	reqURL := buildDBLPSearchURL(p.baseURL, params)

	var response dblpSearchResponse
	if err := p.doRequest(ctx, reqURL, &response); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}

	p.recordSuccess()

	total, _ := strconv.Atoi(response.Result.Hits.Total)

	pubs := make([]Publication, 0, len(response.Result.Hits.Hit))
	for i := range response.Result.Hits.Hit {
		pubs = append(pubs, mapDBLPHitToPublication(&response.Result.Hits.Hit[i].Info))
	}

	hasMore := total > params.Offset+len(pubs)

	return &SearchResult{
		Total:   total,
		Results: pubs,
		HasMore: hasMore,
	}, nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Get
// ---------------------------------------------------------------------------

// Get retrieves a single publication by its DBLP key using the direct /rec/ endpoint.
// The ID will already have the source prefix stripped (e.g., "journals/corr/abs-2401-12345").
func (p *DBLPPlugin) Get(ctx context.Context, id string, _ []IncludeField, format ContentFormat, _ *CallCredentials) (*Publication, error) {
	reqURL := buildDBLPGetURL(p.baseURL, id)

	var xmlResp dblpRecXMLResponse
	if err := p.doXMLRequest(ctx, reqURL, &xmlResp); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	entry := xmlResp.entry()
	if entry == nil {
		return nil, fmt.Errorf("%w: %s", ErrDBLPNotFound, id)
	}

	p.recordSuccess()

	pub := mapDBLPXMLEntryToPublication(entry)

	if format != FormatNative && format != FormatJSON {
		if err := convertDBLPFormat(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

// doRequest executes an HTTP GET and decodes the JSON response into the target.
func (p *DBLPPlugin) doRequest(ctx context.Context, reqURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDBLPHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return fmt.Errorf("%w: %w", ErrDBLPHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrDBLPNotFound
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%w: "+dblpHTTPStatusErrFmt, ErrDBLPHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(dblpMaxResponseBytes))
	if err := json.NewDecoder(limitedBody).Decode(target); err != nil {
		return fmt.Errorf("%w: %w", ErrDBLPJSONParse, err)
	}

	return nil
}

// doXMLRequest executes an HTTP GET and decodes the XML response.
// Used for the /rec/{key}.xml endpoint (DBLP does not support JSON for direct records).
func (p *DBLPPlugin) doXMLRequest(ctx context.Context, reqURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDBLPHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return fmt.Errorf("%w: %w", ErrDBLPHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrDBLPNotFound
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%w: "+dblpHTTPStatusErrFmt, ErrDBLPHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(dblpMaxResponseBytes))
	decoder := xml.NewDecoder(limitedBody)
	// DBLP returns US-ASCII encoding declaration. Go's xml package requires a
	// CharsetReader for non-UTF-8 encodings. US-ASCII is a subset of UTF-8,
	// so we can safely pass through the reader unchanged.
	decoder.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		return input, nil
	}
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %w", ErrDBLPJSONParse, err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Health state helpers
// ---------------------------------------------------------------------------

func (p *DBLPPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *DBLPPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	p.lastError = err.Error()
}

// ---------------------------------------------------------------------------
// URL / query building
// ---------------------------------------------------------------------------

// buildDBLPSearchURL assembles the full search URL with query parameters.
func buildDBLPSearchURL(baseURL string, params SearchParams) string {
	qParams := url.Values{}
	qParams.Set(dblpParamQuery, params.Query)
	qParams.Set(dblpParamFormat, dblpFormatJSON)

	limit := params.Limit
	if limit <= 0 || limit > dblpMaxResultsPerPage {
		limit = dblpMaxResultsPerPage
	}
	qParams.Set(dblpParamHits, strconv.Itoa(limit))

	offset := params.Offset
	if offset < dblpFirstOffset {
		offset = dblpFirstOffset
	}
	if offset > dblpFirstOffset {
		qParams.Set(dblpParamFirst, strconv.Itoa(offset))
	}

	return baseURL + dblpSearchPath + "?" + qParams.Encode()
}

// buildDBLPGetURL assembles the direct record URL for fetching by key.
// Uses /rec/{key}.xml since DBLP does not support JSON for direct record access.
func buildDBLPGetURL(baseURL, key string) string {
	return baseURL + dblpRecPath + key + dblpRecXMLSuffix
}

// ---------------------------------------------------------------------------
// Response mapping
// ---------------------------------------------------------------------------

// mapDBLPHitToPublication converts a DBLP hit info to the unified Publication type.
func mapDBLPHitToPublication(info *dblpInfo) Publication {
	pub := Publication{
		ID:          SourceDBLP + prefixedIDSeparator + info.Key,
		Source:      SourceDBLP,
		ContentType: ContentTypePaper,
		Title:       info.Title,
		URL:         info.EE,
		Authors:     mapDBLPAuthors(info.Authors),
		Published:   info.Year,
		DOI:         info.DOI,
	}

	// Source metadata.
	metadata := make(map[string]any)

	if info.Venue != "" {
		metadata[dblpMetaKeyVenue] = info.Venue
	}
	if info.Type != "" {
		metadata[dblpMetaKeyType] = info.Type
	}
	if info.Key != "" {
		metadata[dblpMetaKeyKey] = info.Key
	}
	if info.EE != "" {
		metadata[dblpMetaKeyEE] = info.EE
	}
	if info.URL != "" {
		metadata[dblpMetaKeyDBLPURL] = info.URL
	}

	if len(metadata) > 0 {
		pub.SourceMetadata = metadata
	}

	return pub
}

// mapDBLPAuthors converts DBLP authors to the unified Author type.
func mapDBLPAuthors(authors *dblpAuthors) []Author {
	if authors == nil || len(authors.Author) == 0 {
		return nil
	}

	result := make([]Author, len(authors.Author))
	for i, a := range authors.Author {
		result[i] = Author{Name: a.Text}
	}
	return result
}

// mapDBLPXMLEntryToPublication converts a DBLP XML record to Publication.
func mapDBLPXMLEntryToPublication(entry *dblpRecXMLEntry) Publication {
	pub := Publication{
		ID:          SourceDBLP + prefixedIDSeparator + entry.Key,
		Source:      SourceDBLP,
		ContentType: ContentTypePaper,
		Title:       entry.Title,
		Published:   entry.Year,
		DOI:         entry.DOI,
		URL:         entry.EE,
	}

	authors := make([]Author, 0, len(entry.Authors))
	for _, name := range entry.Authors {
		if name != "" {
			authors = append(authors, Author{Name: name})
		}
	}
	pub.Authors = authors

	metadata := make(map[string]any)
	metadata[dblpMetaKeyKey] = entry.Key

	venue := entry.Journal
	if venue == "" {
		venue = entry.Booktitle
	}
	if venue != "" {
		metadata[dblpMetaKeyVenue] = venue
	}

	if entry.EE != "" {
		metadata[dblpMetaKeyEE] = entry.EE
	}

	dblpURL := dblpDefaultBaseURL + dblpRecPath + entry.Key
	metadata[dblpMetaKeyDBLPURL] = dblpURL

	pub.SourceMetadata = metadata

	return pub
}

// ---------------------------------------------------------------------------
// Format conversion
// ---------------------------------------------------------------------------

// convertDBLPFormat applies format conversion on a Publication.
func convertDBLPFormat(_ *Publication, format ContentFormat) error {
	switch format {
	case FormatJSON:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrFormatUnsupported, format)
	}
}
