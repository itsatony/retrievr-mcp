package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// bioRxiv plugin identity constants
// ---------------------------------------------------------------------------

const (
	biorxivPluginID          = "biorxiv"
	biorxivPluginName        = "bioRxiv/medRxiv"
	biorxivPluginDescription = "Preprint servers for biology and health sciences — " +
		"supports date-range browsing and DOI retrieval (no keyword search)"
)

// ---------------------------------------------------------------------------
// bioRxiv API base URL and path constants
// ---------------------------------------------------------------------------

const (
	biorxivDefaultBaseURL    = "https://api.biorxiv.org"
	biorxivDetailsPath       = "/details"
	biorxivPubsPath          = "/pubs"
	biorxivMaxResultsPerPage = 100
	biorxivMaxResponseBytes  = 10 << 20 // 10 MB
)

// ---------------------------------------------------------------------------
// bioRxiv server name constants
// ---------------------------------------------------------------------------

const (
	biorxivServerBiorxiv = "biorxiv"
	biorxivServerMedrxiv = "medrxiv"
)

// ---------------------------------------------------------------------------
// bioRxiv config extra key constants
// ---------------------------------------------------------------------------

const (
	biorxivExtraKeyServers = "servers"
	biorxivDefaultServers  = "biorxiv,medrxiv"
	biorxivServerSeparator = ","
)

// ---------------------------------------------------------------------------
// bioRxiv URL path constants
// ---------------------------------------------------------------------------

const (
	biorxivPathSeparator = "/"
	biorxivGetSuffix     = "/na/json"
	biorxivDetailsSuffix = "/json"
	biorxivDefaultDateTo = "2099-12-31"
	biorxivCursorStart   = "0"
)

// ---------------------------------------------------------------------------
// bioRxiv HTTP error format
// ---------------------------------------------------------------------------

const biorxivHTTPStatusErrFmt = "status %d"

// ---------------------------------------------------------------------------
// bioRxiv URL constants
// ---------------------------------------------------------------------------

const (
	biorxivURLPrefix      = "https://www."
	biorxivURLContentPath = ".org/content/"
)

// ---------------------------------------------------------------------------
// bioRxiv author separator
// ---------------------------------------------------------------------------

const biorxivAuthorSeparator = ";"

// ---------------------------------------------------------------------------
// bioRxiv metadata key constants
// ---------------------------------------------------------------------------

const (
	biorxivMetaKeyServer       = "biorxiv_server"
	biorxivMetaKeyCategory     = "biorxiv_category"
	biorxivMetaKeyVersion      = "biorxiv_version"
	biorxivMetaKeyPublishedDOI = "biorxiv_published_doi"
)

// ---------------------------------------------------------------------------
// bioRxiv categories hint
// ---------------------------------------------------------------------------

const biorxivCategoriesHint = "Neuroscience, Bioinformatics, Genomics, Immunology, " +
	"Microbiology, Cell Biology, Epidemiology, Molecular Biology, " +
	"Biochemistry, Genetics, Physiology, Pharmacology"

// ---------------------------------------------------------------------------
// bioRxiv response structs
// ---------------------------------------------------------------------------

type biorxivResponse struct {
	Collection []biorxivArticle `json:"collection"`
	Messages   []biorxivMsg     `json:"messages"`
}

type biorxivArticle struct {
	DOI                 string `json:"biorxiv_doi"`
	Title               string `json:"preprint_title"`
	Authors             string `json:"preprint_authors"` // semicolon-separated
	Abstract            string `json:"preprint_abstract"`
	Date                string `json:"preprint_date"`
	Category            string `json:"preprint_category"`
	Version             string `json:"version"`
	Server              string `json:"server"`
	PublishedDOI        string `json:"published_doi"`
	PublishedJournal    string `json:"published_journal"`
	PublishedDate       string `json:"published_date"`
	CorrespondingAuthor string `json:"preprint_author_corresponding"`
	Institution         string `json:"preprint_author_corresponding_institution"`
}

type biorxivMsg struct {
	Status string `json:"status"`
	Total  string `json:"total"`
	Count  string `json:"count"`
}

// ---------------------------------------------------------------------------
// BioRxivPlugin struct
// ---------------------------------------------------------------------------

// BioRxivPlugin implements SourcePlugin for bioRxiv and medRxiv preprint servers.
// Search requires a date_from filter (no keyword search API available).
// Get retrieves preprints by DOI. Thread-safe via sync.RWMutex.
type BioRxivPlugin struct {
	baseURL    string
	servers    []string // e.g., ["biorxiv", "medrxiv"]
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

func (p *BioRxivPlugin) ID() string                  { return biorxivPluginID }
func (p *BioRxivPlugin) Name() string                { return biorxivPluginName }
func (p *BioRxivPlugin) Description() string         { return biorxivPluginDescription }
func (p *BioRxivPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }
func (p *BioRxivPlugin) NativeFormat() ContentFormat { return FormatJSON }
func (p *BioRxivPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatBibTeX}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Capabilities
// ---------------------------------------------------------------------------

func (p *BioRxivPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    false,
		SupportsSortDate:         true,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       biorxivMaxResultsPerPage,
		CategoriesHint:           biorxivCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatBibTeX},
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Initialize
// ---------------------------------------------------------------------------

func (p *BioRxivPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = biorxivDefaultBaseURL
	}

	// Parse configured servers (default: biorxiv,medrxiv).
	serverStr := biorxivDefaultServers
	if cfg.Extra != nil {
		if s, ok := cfg.Extra[biorxivExtraKeyServers]; ok && s != "" {
			serverStr = s
		}
	}
	p.servers = strings.Split(serverStr, biorxivServerSeparator)

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

func (p *BioRxivPlugin) Health(_ context.Context) SourceHealth {
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

// Search browses preprints by date range. Requires params.Filters.DateFrom.
// Returns ErrBiorxivDateRequired if no date_from filter is provided.
func (p *BioRxivPlugin) Search(ctx context.Context, params SearchParams, _ *CallCredentials) (*SearchResult, error) {
	if params.Filters.DateFrom == "" {
		return nil, ErrBiorxivDateRequired
	}

	dateTo := params.Filters.DateTo
	if dateTo == "" {
		dateTo = biorxivDefaultDateTo
	}

	// Query each configured server and merge results.
	var allArticles []biorxivArticle
	var totalCount int

	for _, server := range p.servers {
		reqURL := buildBiorxivDetailsURL(p.baseURL, server, params.Filters.DateFrom, dateTo)

		var response biorxivResponse
		if err := p.doRequest(ctx, reqURL, &response); err != nil {
			// Partial failure: log but continue with other servers.
			p.recordError(err)
			continue
		}

		allArticles = append(allArticles, response.Collection...)

		if len(response.Messages) > 0 {
			if t, err := strconv.Atoi(response.Messages[0].Total); err == nil {
				totalCount += t
			}
		}
	}

	p.recordSuccess()

	// Apply limit.
	limit := params.Limit
	if limit <= 0 || limit > biorxivMaxResultsPerPage {
		limit = biorxivMaxResultsPerPage
	}
	if len(allArticles) > limit {
		allArticles = allArticles[:limit]
	}

	pubs := make([]Publication, 0, len(allArticles))
	for i := range allArticles {
		pubs = append(pubs, mapBiorxivArticleToPublication(&allArticles[i]))
	}

	return &SearchResult{
		Total:   totalCount,
		Results: pubs,
		HasMore: totalCount > len(pubs),
	}, nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Get
// ---------------------------------------------------------------------------

// Get retrieves a single preprint by DOI. Tries each configured server.
func (p *BioRxivPlugin) Get(ctx context.Context, id string, _ []IncludeField, format ContentFormat, _ *CallCredentials) (*Publication, error) {
	// Try each configured server for the DOI.
	for _, server := range p.servers {
		reqURL := buildBiorxivGetURL(p.baseURL, server, id)

		var response biorxivResponse
		if err := p.doRequest(ctx, reqURL, &response); err != nil {
			continue // try next server
		}

		if len(response.Collection) > 0 {
			p.recordSuccess()
			pub := mapBiorxivArticleToPublication(&response.Collection[0])

			if format != FormatNative && format != FormatJSON {
				if err := convertBiorxivFormat(&pub, format); err != nil {
					return nil, err
				}
			}

			return &pub, nil
		}
	}

	p.recordError(ErrBiorxivNotFound)
	return nil, fmt.Errorf("%w: %s", ErrBiorxivNotFound, id)
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

func (p *BioRxivPlugin) doRequest(ctx context.Context, reqURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBiorxivHTTPRequest, err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return fmt.Errorf("%w: %w", ErrBiorxivHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrBiorxivNotFound
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%w: "+biorxivHTTPStatusErrFmt, ErrBiorxivHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(biorxivMaxResponseBytes))
	if err := json.NewDecoder(limitedBody).Decode(target); err != nil {
		return fmt.Errorf("%w: %w", ErrBiorxivJSONParse, err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Health state helpers
// ---------------------------------------------------------------------------

func (p *BioRxivPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *BioRxivPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	p.lastError = err.Error()
}

// ---------------------------------------------------------------------------
// URL building
// ---------------------------------------------------------------------------

func buildBiorxivDetailsURL(baseURL, server, dateFrom, dateTo string) string {
	// Format: /details/{server}/{from}/{to}/{cursor}/json
	return baseURL + biorxivDetailsPath +
		biorxivPathSeparator + server +
		biorxivPathSeparator + dateFrom +
		biorxivPathSeparator + dateTo +
		biorxivPathSeparator + biorxivCursorStart +
		biorxivDetailsSuffix
}

func buildBiorxivGetURL(baseURL, server, doi string) string {
	// Format: /pubs/{server}/{DOI}/na/json
	return baseURL + biorxivPubsPath +
		biorxivPathSeparator + server +
		biorxivPathSeparator + doi +
		biorxivGetSuffix
}

// ---------------------------------------------------------------------------
// Response mapping
// ---------------------------------------------------------------------------

func mapBiorxivArticleToPublication(article *biorxivArticle) Publication {
	pub := Publication{
		ID:          SourceBioRxiv + prefixedIDSeparator + article.DOI,
		Source:      SourceBioRxiv,
		ContentType: ContentTypePaper,
		Title:       article.Title,
		Abstract:    article.Abstract,
		Published:   article.Date,
		DOI:         article.DOI,
	}

	// URL.
	server := article.Server
	if server == "" {
		server = biorxivServerBiorxiv
	}
	pub.URL = biorxivURLPrefix + server + biorxivURLContentPath + article.DOI

	// Authors — semicolon-separated.
	pub.Authors = parseBiorxivAuthors(article.Authors)

	// Source metadata.
	metadata := make(map[string]any)
	metadata[biorxivMetaKeyServer] = server
	if article.Category != "" {
		metadata[biorxivMetaKeyCategory] = article.Category
	}
	if article.Version != "" {
		metadata[biorxivMetaKeyVersion] = article.Version
	}
	if article.PublishedDOI != "" {
		metadata[biorxivMetaKeyPublishedDOI] = article.PublishedDOI
	}
	pub.SourceMetadata = metadata

	return pub
}

func parseBiorxivAuthors(authorStr string) []Author {
	if authorStr == "" {
		return nil
	}

	parts := strings.Split(authorStr, biorxivAuthorSeparator)
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

func convertBiorxivFormat(_ *Publication, format ContentFormat) error {
	switch format {
	case FormatJSON:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrFormatUnsupported, format)
	}
}
