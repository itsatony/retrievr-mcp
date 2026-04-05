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
	"time"
)

// ---------------------------------------------------------------------------
// HuggingFace plugin identity constants
// ---------------------------------------------------------------------------

const (
	hfPluginID          = "huggingface"
	hfPluginName        = "HuggingFace"
	hfPluginDescription = "AI community hub with papers, models, and datasets — papers linked to ArXiv, 1M+ models, 100K+ datasets"
)

// ---------------------------------------------------------------------------
// HuggingFace API base URL and path constants
// ---------------------------------------------------------------------------

const (
	hfDefaultBaseURL         = "https://huggingface.co"
	hfAPIPapersSearchPath    = "/api/papers/search"
	hfAPIPaperGetPath        = "/api/papers/"
	hfAPIPaperMarkdownPath   = "/papers/"
	hfAPIModelsPath          = "/api/models"
	hfAPIModelsSlashPath     = "/api/models/"
	hfAPIDatasetsPath        = "/api/datasets"
	hfAPIDatasetsSlashPath   = "/api/datasets/"
	hfMaxResponseBytes       = 10 << 20 // 10 MB upper bound
	hfMaxResultsPerPage      = 100
	hfLinkedModelsLimit      = 10
	hfContentTypesInitialCap = 3
)

// ---------------------------------------------------------------------------
// HuggingFace API parameter name constants
// ---------------------------------------------------------------------------

const (
	hfParamQuery     = "q"
	hfParamSearch    = "search"
	hfParamLimit     = "limit"
	hfParamSkip      = "skip"
	hfParamOffset    = "offset"
	hfParamSort      = "sort"
	hfParamDirection = "direction"
	hfParamFilter    = "filter"
)

// ---------------------------------------------------------------------------
// HuggingFace API sort value constants
// ---------------------------------------------------------------------------

const (
	hfSortDownloads    = "downloads"
	hfSortCreatedAt    = "createdAt"
	hfSortLastModified = "lastModified"
	hfDirectionDesc    = "-1"
	hfDirectionAsc     = "1"
)

// ---------------------------------------------------------------------------
// HuggingFace config extra key constants
// ---------------------------------------------------------------------------

const (
	hfExtraKeyIncludeModels   = "include_models"
	hfExtraKeyIncludeDatasets = "include_datasets"
	hfExtraKeyIncludePapers   = "include_papers"
	hfExtraValueTrue          = "true"
)

// ---------------------------------------------------------------------------
// HuggingFace HTTP constants
// ---------------------------------------------------------------------------

const (
	hfAuthHeader       = "Authorization"
	hfAuthBearerPrefix = "Bearer "
	hfHTTPStatusErrFmt = "status %d"
)

// ---------------------------------------------------------------------------
// HuggingFace ID sub-type prefix constants
// ---------------------------------------------------------------------------

const (
	hfSubTypePaper   = "paper/"
	hfSubTypeModel   = "model/"
	hfSubTypeDataset = "dataset/"
)

// ---------------------------------------------------------------------------
// HuggingFace URL prefix constants
// ---------------------------------------------------------------------------

const (
	hfPaperURLPrefix   = "https://huggingface.co/papers/"
	hfModelURLPrefix   = "https://huggingface.co/"
	hfDatasetURLPrefix = "https://huggingface.co/datasets/"
	hfPaperMDSuffix    = ".md"
)

// ---------------------------------------------------------------------------
// HuggingFace metadata key constants
// ---------------------------------------------------------------------------

const (
	hfMetaKeyUpvotes      = "hf_upvotes"
	hfMetaKeyNumComments  = "hf_num_comments"
	hfMetaKeyPipelineTag  = "hf_pipeline_tag"
	hfMetaKeyLibraryName  = "hf_library_name"
	hfMetaKeyDownloads    = "hf_downloads"
	hfMetaKeyLikes        = "hf_likes"
	hfMetaKeyTags         = "hf_tags"
	hfMetaKeyPrivate      = "hf_private"
	hfMetaKeyLinkedModels = "hf_linked_models"
	hfMetaKeyAuthor       = "hf_author"
)

// ---------------------------------------------------------------------------
// HuggingFace BibTeX constants
// ---------------------------------------------------------------------------

const hfBibTeXTemplate = `@misc{%s,
  title  = {%s},
  author = {%s},
  year   = {%s},
  url    = {%s}
}`

const (
	hfBibTeXAuthorSeparator = " and "
	hfBibTeXKeyPrefix       = "HF-"
)

// ---------------------------------------------------------------------------
// HuggingFace categories hint
// ---------------------------------------------------------------------------

const hfCategoriesHint = "text-generation, text-classification, question-answering, summarization, translation, image-classification, object-detection, text-to-image, automatic-speech-recognition"

// ---------------------------------------------------------------------------
// HuggingFace date format constants
// ---------------------------------------------------------------------------

const (
	hfDateLayout       = "2006-01-02T15:04:05.000Z"
	hfDateOutputLayout = "2006-01-02"
	hfYearOnlyLength   = 4
)

// ---------------------------------------------------------------------------
// HuggingFace ArXiv filter prefix
// ---------------------------------------------------------------------------

const hfArxivFilterPrefix = "arxiv:"

// ---------------------------------------------------------------------------
// HuggingFace JSON response struct definitions — Papers
// ---------------------------------------------------------------------------

// hfPaperSearchResult is the outer wrapper in the papers search array.
type hfPaperSearchResult struct {
	Paper       hfPaper `json:"paper"`
	PublishedAt string  `json:"publishedAt"`
	Title       string  `json:"title"`
	Summary     string  `json:"summary"`
	NumComments int     `json:"numComments"`
}

// hfPaper is the inner paper object with core metadata.
type hfPaper struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary"`
	PublishedAt string     `json:"publishedAt"`
	Upvotes     int        `json:"upvotes"`
	Authors     []hfAuthor `json:"authors"`
}

// hfAuthor represents a paper author.
type hfAuthor struct {
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// HuggingFace JSON response struct definitions — Models
// ---------------------------------------------------------------------------

// hfModel represents a single model in the HuggingFace models API response.
type hfModel struct {
	ID          string   `json:"id"`
	ModelID     string   `json:"modelId"`
	Likes       int      `json:"likes"`
	Downloads   int      `json:"downloads"`
	PipelineTag string   `json:"pipeline_tag"`
	LibraryName string   `json:"library_name"`
	Tags        []string `json:"tags"`
	CreatedAt   string   `json:"createdAt"`
	Private     bool     `json:"private"`
}

// ---------------------------------------------------------------------------
// HuggingFace JSON response struct definitions — Datasets
// ---------------------------------------------------------------------------

// hfDataset represents a single dataset in the HuggingFace datasets API response.
type hfDataset struct {
	ID          string   `json:"id"`
	Author      string   `json:"author"`
	Likes       int      `json:"likes"`
	Downloads   int      `json:"downloads"`
	Tags        []string `json:"tags"`
	CreatedAt   string   `json:"createdAt"`
	Description string   `json:"description"`
	Private     bool     `json:"private"`
}

// ---------------------------------------------------------------------------
// HuggingFacePlugin struct
// ---------------------------------------------------------------------------

// HuggingFacePlugin implements SourcePlugin for the HuggingFace Hub API.
// Supports three sub-sources: papers, models, and datasets.
// Thread-safe for concurrent use after Initialize.
type HuggingFacePlugin struct {
	baseURL         string
	apiKey          string // server-level HF token from config
	httpClient      *http.Client
	enabled         bool
	rateLimit       float64
	includePapers   bool
	includeModels   bool
	includeDatasets bool

	mu        sync.RWMutex // protects health state below
	healthy   bool
	lastError string
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: identity methods
// ---------------------------------------------------------------------------

// ID returns the unique source identifier.
func (p *HuggingFacePlugin) ID() string { return hfPluginID }

// Name returns a human-readable name.
func (p *HuggingFacePlugin) Name() string { return hfPluginName }

// Description returns a short description for LLM context.
func (p *HuggingFacePlugin) Description() string { return hfPluginDescription }

// ContentTypes returns the types of content this source provides,
// based on which sub-sources are enabled in config.
func (p *HuggingFacePlugin) ContentTypes() []ContentType {
	types := make([]ContentType, 0, hfContentTypesInitialCap)
	if p.includePapers {
		types = append(types, ContentTypePaper)
	}
	if p.includeModels {
		types = append(types, ContentTypeModel)
	}
	if p.includeDatasets {
		types = append(types, ContentTypeDataset)
	}
	return types
}

// NativeFormat returns the default content format.
func (p *HuggingFacePlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats returns all formats this source can provide.
func (p *HuggingFacePlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON, FormatMarkdown, FormatBibTeX}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Capabilities
// ---------------------------------------------------------------------------

// Capabilities reports what filtering, sorting, and features HuggingFace supports.
func (p *HuggingFacePlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         true,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         true,
		SupportsSortCitations:    true,
		SupportsOpenAccessFilter: false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       hfMaxResultsPerPage,
		CategoriesHint:           hfCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON, FormatMarkdown, FormatBibTeX},
	}
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Initialize
// ---------------------------------------------------------------------------

// Initialize sets up the HuggingFace plugin with the given configuration.
// Reads include_papers/include_models/include_datasets from config extras.
func (p *HuggingFacePlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = hfDefaultBaseURL
	}

	// Default: all sub-sources enabled unless explicitly disabled.
	p.includePapers = true
	p.includeModels = true
	p.includeDatasets = true
	if cfg.Extra != nil {
		if v, ok := cfg.Extra[hfExtraKeyIncludePapers]; ok && v != hfExtraValueTrue {
			p.includePapers = false
		}
		if v, ok := cfg.Extra[hfExtraKeyIncludeModels]; ok && v != hfExtraValueTrue {
			p.includeModels = false
		}
		if v, ok := cfg.Extra[hfExtraKeyIncludeDatasets]; ok && v != hfExtraValueTrue {
			p.includeDatasets = false
		}
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
func (p *HuggingFacePlugin) Health(_ context.Context) SourceHealth {
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

// Search executes a search query against the HuggingFace Hub API.
// Routes to papers, models, and/or datasets sub-APIs based on content_type.
// When multiple sub-APIs are queried, calls are made concurrently.
func (p *HuggingFacePlugin) Search(ctx context.Context, params SearchParams, creds *CallCredentials) (*SearchResult, error) {
	if params.Query == "" {
		return nil, ErrHFEmptyQuery
	}

	token := resolveHFToken(creds, p.apiKey)

	// Determine which sub-APIs to query based on content_type AND config.
	// Empty content_type is treated as ContentTypePaper (the default from tools.go).
	ct := params.ContentType
	wantPapers := p.includePapers && (ct == ContentTypePaper || ct == ContentTypeAny || ct == "")
	wantModels := p.includeModels && (ct == ContentTypeModel || ct == ContentTypeAny)
	wantDatasets := p.includeDatasets && (ct == ContentTypeDataset || ct == ContentTypeAny)

	// Fan out to sub-APIs concurrently.
	type subResult struct {
		pubs []Publication
		err  error
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make([]subResult, 0, hfContentTypesInitialCap)

	searchFn := func(fn func(context.Context, SearchParams, string) ([]Publication, error)) {
		defer wg.Done()
		pubs, err := fn(ctx, params, token)
		mu.Lock()
		results = append(results, subResult{pubs: pubs, err: err})
		mu.Unlock()
	}

	if wantPapers {
		wg.Add(1)
		go searchFn(p.searchPapers)
	}
	if wantModels {
		wg.Add(1)
		go searchFn(p.searchModels)
	}
	if wantDatasets {
		wg.Add(1)
		go searchFn(p.searchDatasets)
	}

	wg.Wait()

	// Merge results: partial success (at least one sub-API succeeded) returns results.
	var allPubs []Publication
	var firstErr error
	for _, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
		} else {
			allPubs = append(allPubs, r.pubs...)
		}
	}

	// If ALL sub-searches failed, report error.
	if len(allPubs) == 0 && firstErr != nil {
		p.recordError(firstErr)
		return nil, fmt.Errorf("%w: %w", ErrSearchFailed, firstErr)
	}

	p.recordSuccess()

	return &SearchResult{
		Total:   len(allPubs),
		Results: allPubs,
		HasMore: len(allPubs) >= params.Limit,
	}, nil
}

// ---------------------------------------------------------------------------
// Search sub-methods
// ---------------------------------------------------------------------------

// searchPapers searches the HuggingFace papers API.
func (p *HuggingFacePlugin) searchPapers(ctx context.Context, params SearchParams, token string) ([]Publication, error) {
	reqURL := buildHFPapersSearchURL(p.baseURL, params)

	var results []hfPaperSearchResult
	if err := p.doRequest(ctx, reqURL, token, &results); err != nil {
		return nil, err
	}

	pubs := make([]Publication, 0, len(results))
	for i := range results {
		pubs = append(pubs, mapHFPaperToPublication(&results[i]))
	}

	return pubs, nil
}

// searchModels searches the HuggingFace models API.
func (p *HuggingFacePlugin) searchModels(ctx context.Context, params SearchParams, token string) ([]Publication, error) {
	reqURL := buildHFModelsSearchURL(p.baseURL, params)

	var results []hfModel
	if err := p.doRequest(ctx, reqURL, token, &results); err != nil {
		return nil, err
	}

	pubs := make([]Publication, 0, len(results))
	for i := range results {
		pubs = append(pubs, mapHFModelToPublication(&results[i]))
	}

	return pubs, nil
}

// searchDatasets searches the HuggingFace datasets API.
func (p *HuggingFacePlugin) searchDatasets(ctx context.Context, params SearchParams, token string) ([]Publication, error) {
	reqURL := buildHFDatasetsSearchURL(p.baseURL, params)

	var results []hfDataset
	if err := p.doRequest(ctx, reqURL, token, &results); err != nil {
		return nil, err
	}

	pubs := make([]Publication, 0, len(results))
	for i := range results {
		pubs = append(pubs, mapHFDatasetToPublication(&results[i]))
	}

	return pubs, nil
}

// ---------------------------------------------------------------------------
// SourcePlugin interface: Get
// ---------------------------------------------------------------------------

// Get retrieves a single item by its HuggingFace identifier.
// The rawID contains a sub-type prefix: paper/<arxivID>, model/<org/name>, dataset/<org/name>.
func (p *HuggingFacePlugin) Get(ctx context.Context, rawID string, include []IncludeField, format ContentFormat, creds *CallCredentials) (*Publication, error) {
	token := resolveHFToken(creds, p.apiKey)

	switch {
	case strings.HasPrefix(rawID, hfSubTypePaper):
		paperID := strings.TrimPrefix(rawID, hfSubTypePaper)
		return p.getPaper(ctx, paperID, include, format, token)
	case strings.HasPrefix(rawID, hfSubTypeModel):
		modelID := strings.TrimPrefix(rawID, hfSubTypeModel)
		return p.getModel(ctx, modelID, include, format, token)
	case strings.HasPrefix(rawID, hfSubTypeDataset):
		datasetID := strings.TrimPrefix(rawID, hfSubTypeDataset)
		return p.getDataset(ctx, datasetID, include, format, token)
	default:
		return nil, fmt.Errorf("%w: %s", ErrHFNotFound, rawID)
	}
}

// ---------------------------------------------------------------------------
// Get sub-methods
// ---------------------------------------------------------------------------

// getPaper retrieves a single paper by ArXiv ID.
func (p *HuggingFacePlugin) getPaper(ctx context.Context, arxivID string, include []IncludeField, format ContentFormat, token string) (*Publication, error) {
	reqURL := buildHFPaperGetURL(p.baseURL, arxivID)

	var paper hfPaper
	if err := p.doRequest(ctx, reqURL, token, &paper); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	p.recordSuccess()

	// Build a wrapper for the mapper (Get returns hfPaper directly, not wrapped).
	wrapper := &hfPaperSearchResult{
		Paper:       paper,
		PublishedAt: paper.PublishedAt,
		Title:       paper.Title,
		Summary:     paper.Summary,
	}
	pub := mapHFPaperToPublication(wrapper)

	// Fetch full text markdown if requested (non-fatal on failure).
	if slices.Contains(include, IncludeFullText) {
		mdURL := buildHFPaperMarkdownURL(p.baseURL, arxivID)
		content, fetchErr := p.doRequestRaw(ctx, mdURL, token)
		if fetchErr == nil && content != "" {
			pub.FullText = &FullTextContent{
				Content:       content,
				ContentFormat: FormatMarkdown,
				ContentLength: len(content),
				Truncated:     false,
			}
		}
	}

	// Fetch linked models if requested (non-fatal on failure).
	if slices.Contains(include, IncludeRelated) {
		linkedURL := buildHFPaperLinkedModelsURL(p.baseURL, arxivID)
		var linkedModels []hfModel
		if fetchErr := p.doRequest(ctx, linkedURL, token, &linkedModels); fetchErr == nil && len(linkedModels) > 0 {
			related := make([]Reference, 0, len(linkedModels))
			modelIDs := make([]string, 0, len(linkedModels))
			for i := range linkedModels {
				related = append(related, Reference{
					ID:    SourceHuggingFace + prefixedIDSeparator + hfSubTypeModel + linkedModels[i].ID,
					Title: linkedModels[i].ID,
				})
				modelIDs = append(modelIDs, linkedModels[i].ID)
			}
			pub.Related = related
			if pub.SourceMetadata == nil {
				pub.SourceMetadata = make(map[string]any)
			}
			pub.SourceMetadata[hfMetaKeyLinkedModels] = modelIDs
		}
	}

	// Apply format conversion if not native JSON.
	if format != FormatNative && format != FormatJSON {
		if err := convertHFFormat(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// getModel retrieves a single model by its org/name ID.
func (p *HuggingFacePlugin) getModel(ctx context.Context, modelID string, _ []IncludeField, format ContentFormat, token string) (*Publication, error) {
	reqURL := buildHFModelGetURL(p.baseURL, modelID)

	var model hfModel
	if err := p.doRequest(ctx, reqURL, token, &model); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	p.recordSuccess()
	pub := mapHFModelToPublication(&model)

	// Apply format conversion if not native JSON.
	if format != FormatNative && format != FormatJSON {
		if err := convertHFFormat(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// getDataset retrieves a single dataset by its org/name ID.
func (p *HuggingFacePlugin) getDataset(ctx context.Context, datasetID string, _ []IncludeField, format ContentFormat, token string) (*Publication, error) {
	reqURL := buildHFDatasetGetURL(p.baseURL, datasetID)

	var dataset hfDataset
	if err := p.doRequest(ctx, reqURL, token, &dataset); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
	}

	p.recordSuccess()
	pub := mapHFDatasetToPublication(&dataset)

	// Apply format conversion if not native JSON.
	if format != FormatNative && format != FormatJSON {
		if err := convertHFFormat(&pub, format); err != nil {
			return nil, err
		}
	}

	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP request helpers
// ---------------------------------------------------------------------------

// doRequest executes an HTTP GET and decodes the JSON response into the target.
func (p *HuggingFacePlugin) doRequest(ctx context.Context, reqURL, token string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHFHTTPRequest, err)
	}

	if token != "" {
		req.Header.Set(hfAuthHeader, hfAuthBearerPrefix+token)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return fmt.Errorf("%w: %w", ErrHFHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrHFNotFound
	}

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%w: "+hfHTTPStatusErrFmt, ErrHFHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(hfMaxResponseBytes))
	if err := json.NewDecoder(limitedBody).Decode(target); err != nil {
		return fmt.Errorf("%w: %w", ErrHFJSONParse, err)
	}

	return nil
}

// doRequestRaw executes an HTTP GET and returns the raw response body as a string.
// Used for fetching paper markdown content.
func (p *HuggingFacePlugin) doRequestRaw(ctx context.Context, reqURL, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrHFHTTPRequest, err)
	}

	if token != "" {
		req.Header.Set(hfAuthHeader, hfAuthBearerPrefix+token)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: %w", ErrUpstreamTimeout, ctx.Err())
		}
		return "", fmt.Errorf("%w: %w", ErrHFHTTPRequest, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("%w: "+hfHTTPStatusErrFmt, ErrHFHTTPRequest, resp.StatusCode)
	}

	limitedBody := io.LimitReader(resp.Body, int64(hfMaxResponseBytes))
	body, err := io.ReadAll(limitedBody)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrHFHTTPRequest, err)
	}

	return string(body), nil
}

// ---------------------------------------------------------------------------
// Credential resolution helper
// ---------------------------------------------------------------------------

// resolveHFToken extracts the effective HF token from per-call credentials
// and server default, following the three-level resolution chain.
func resolveHFToken(creds *CallCredentials, serverDefault string) string {
	if creds != nil {
		return creds.ResolveForSource(SourceHuggingFace, serverDefault)
	}
	return serverDefault
}

// ---------------------------------------------------------------------------
// Health state helpers
// ---------------------------------------------------------------------------

func (p *HuggingFacePlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *HuggingFacePlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	p.lastError = err.Error()
}

// ---------------------------------------------------------------------------
// URL / query building — Papers
// ---------------------------------------------------------------------------

// buildHFPapersSearchURL assembles the papers search URL.
func buildHFPapersSearchURL(baseURL string, params SearchParams) string {
	qp := url.Values{}
	qp.Set(hfParamQuery, params.Query)

	limit := params.Limit
	if limit <= 0 || limit > hfMaxResultsPerPage {
		limit = hfMaxResultsPerPage
	}
	qp.Set(hfParamLimit, strconv.Itoa(limit))

	if params.Offset > 0 {
		qp.Set(hfParamSkip, strconv.Itoa(params.Offset))
	}

	return baseURL + hfAPIPapersSearchPath + "?" + qp.Encode()
}

// buildHFPaperGetURL assembles the URL for fetching a single paper by ArXiv ID.
func buildHFPaperGetURL(baseURL, arxivID string) string {
	return baseURL + hfAPIPaperGetPath + arxivID
}

// buildHFPaperMarkdownURL assembles the URL for fetching paper markdown content.
func buildHFPaperMarkdownURL(baseURL, arxivID string) string {
	return baseURL + hfAPIPaperMarkdownPath + arxivID + hfPaperMDSuffix
}

// buildHFPaperLinkedModelsURL assembles the URL for fetching models linked to a paper.
func buildHFPaperLinkedModelsURL(baseURL, arxivID string) string {
	qp := url.Values{}
	qp.Set(hfParamFilter, hfArxivFilterPrefix+arxivID)
	qp.Set(hfParamLimit, strconv.Itoa(hfLinkedModelsLimit))

	return baseURL + hfAPIModelsPath + "?" + qp.Encode()
}

// ---------------------------------------------------------------------------
// URL / query building — Models
// ---------------------------------------------------------------------------

// buildHFModelsSearchURL assembles the models search URL.
func buildHFModelsSearchURL(baseURL string, params SearchParams) string {
	qp := url.Values{}
	qp.Set(hfParamSearch, params.Query)

	limit := params.Limit
	if limit <= 0 || limit > hfMaxResultsPerPage {
		limit = hfMaxResultsPerPage
	}
	qp.Set(hfParamLimit, strconv.Itoa(limit))

	if params.Offset > 0 {
		qp.Set(hfParamOffset, strconv.Itoa(params.Offset))
	}

	sortField, direction := mapHFModelSortOrder(params.Sort)
	if sortField != "" {
		qp.Set(hfParamSort, sortField)
		qp.Set(hfParamDirection, direction)
	}

	// Categories from filters map to the filter parameter (tags).
	if len(params.Filters.Categories) > 0 {
		qp.Set(hfParamFilter, strings.Join(params.Filters.Categories, ","))
	}

	return baseURL + hfAPIModelsPath + "?" + qp.Encode()
}

// buildHFModelGetURL assembles the URL for fetching a single model by ID.
func buildHFModelGetURL(baseURL, modelID string) string {
	return baseURL + hfAPIModelsSlashPath + modelID
}

// mapHFModelSortOrder converts a SortOrder to HuggingFace models sort parameters.
func mapHFModelSortOrder(sort SortOrder) (field, direction string) {
	switch sort {
	case SortDateDesc:
		return hfSortCreatedAt, hfDirectionDesc
	case SortDateAsc:
		return hfSortCreatedAt, hfDirectionAsc
	case SortCitations:
		return hfSortDownloads, hfDirectionDesc
	default:
		return "", ""
	}
}

// ---------------------------------------------------------------------------
// URL / query building — Datasets
// ---------------------------------------------------------------------------

// buildHFDatasetsSearchURL assembles the datasets search URL.
func buildHFDatasetsSearchURL(baseURL string, params SearchParams) string {
	qp := url.Values{}
	qp.Set(hfParamSearch, params.Query)

	limit := params.Limit
	if limit <= 0 || limit > hfMaxResultsPerPage {
		limit = hfMaxResultsPerPage
	}
	qp.Set(hfParamLimit, strconv.Itoa(limit))

	if params.Offset > 0 {
		qp.Set(hfParamOffset, strconv.Itoa(params.Offset))
	}

	sortField, direction := mapHFDatasetSortOrder(params.Sort)
	if sortField != "" {
		qp.Set(hfParamSort, sortField)
		qp.Set(hfParamDirection, direction)
	}

	// Categories from filters map to the filter parameter (tags).
	if len(params.Filters.Categories) > 0 {
		qp.Set(hfParamFilter, strings.Join(params.Filters.Categories, ","))
	}

	return baseURL + hfAPIDatasetsPath + "?" + qp.Encode()
}

// buildHFDatasetGetURL assembles the URL for fetching a single dataset by ID.
func buildHFDatasetGetURL(baseURL, datasetID string) string {
	return baseURL + hfAPIDatasetsSlashPath + datasetID
}

// mapHFDatasetSortOrder converts a SortOrder to HuggingFace datasets sort parameters.
func mapHFDatasetSortOrder(sort SortOrder) (field, direction string) {
	switch sort {
	case SortDateDesc:
		return hfSortLastModified, hfDirectionDesc
	case SortDateAsc:
		return hfSortLastModified, hfDirectionAsc
	case SortCitations:
		return hfSortDownloads, hfDirectionDesc
	default:
		return "", ""
	}
}

// ---------------------------------------------------------------------------
// Response mapping — Papers
// ---------------------------------------------------------------------------

// mapHFPaperToPublication converts a HuggingFace paper search result
// to the unified Publication type.
func mapHFPaperToPublication(wrapper *hfPaperSearchResult) Publication {
	paper := &wrapper.Paper

	pub := Publication{
		ID:          SourceHuggingFace + prefixedIDSeparator + hfSubTypePaper + paper.ID,
		Source:      SourceHuggingFace,
		ContentType: ContentTypePaper,
		Title:       paper.Title,
		Abstract:    paper.Summary,
		ArXivID:     paper.ID,
		URL:         hfPaperURLPrefix + paper.ID,
		Published:   parseHFDate(paper.PublishedAt),
		Authors:     mapHFAuthors(paper.Authors),
	}

	// Source metadata.
	metadata := make(map[string]any)
	metadata[hfMetaKeyUpvotes] = paper.Upvotes
	metadata[hfMetaKeyNumComments] = wrapper.NumComments
	pub.SourceMetadata = metadata

	return pub
}

// mapHFAuthors converts HuggingFace authors to the unified Author type.
func mapHFAuthors(authors []hfAuthor) []Author {
	if len(authors) == 0 {
		return nil
	}

	result := make([]Author, 0, len(authors))
	for _, a := range authors {
		if a.Name != "" {
			result = append(result, Author{Name: a.Name})
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Response mapping — Models
// ---------------------------------------------------------------------------

// mapHFModelToPublication converts a HuggingFace model to the unified Publication type.
func mapHFModelToPublication(model *hfModel) Publication {
	pub := Publication{
		ID:          SourceHuggingFace + prefixedIDSeparator + hfSubTypeModel + model.ID,
		Source:      SourceHuggingFace,
		ContentType: ContentTypeModel,
		Title:       model.ID,
		URL:         hfModelURLPrefix + model.ID,
		Published:   parseHFDate(model.CreatedAt),
		Categories:  model.Tags,
	}

	// Source metadata.
	metadata := make(map[string]any)
	metadata[hfMetaKeyDownloads] = model.Downloads
	metadata[hfMetaKeyLikes] = model.Likes
	metadata[hfMetaKeyPrivate] = model.Private
	if model.PipelineTag != "" {
		metadata[hfMetaKeyPipelineTag] = model.PipelineTag
	}
	if model.LibraryName != "" {
		metadata[hfMetaKeyLibraryName] = model.LibraryName
	}
	if len(model.Tags) > 0 {
		metadata[hfMetaKeyTags] = model.Tags
	}
	pub.SourceMetadata = metadata

	return pub
}

// ---------------------------------------------------------------------------
// Response mapping — Datasets
// ---------------------------------------------------------------------------

// mapHFDatasetToPublication converts a HuggingFace dataset to the unified Publication type.
func mapHFDatasetToPublication(dataset *hfDataset) Publication {
	pub := Publication{
		ID:          SourceHuggingFace + prefixedIDSeparator + hfSubTypeDataset + dataset.ID,
		Source:      SourceHuggingFace,
		ContentType: ContentTypeDataset,
		Title:       dataset.ID,
		Abstract:    dataset.Description,
		URL:         hfDatasetURLPrefix + dataset.ID,
		Published:   parseHFDate(dataset.CreatedAt),
		Categories:  dataset.Tags,
	}

	// Source metadata.
	metadata := make(map[string]any)
	metadata[hfMetaKeyDownloads] = dataset.Downloads
	metadata[hfMetaKeyLikes] = dataset.Likes
	metadata[hfMetaKeyPrivate] = dataset.Private
	if dataset.Author != "" {
		metadata[hfMetaKeyAuthor] = dataset.Author
	}
	if len(dataset.Tags) > 0 {
		metadata[hfMetaKeyTags] = dataset.Tags
	}
	pub.SourceMetadata = metadata

	return pub
}

// ---------------------------------------------------------------------------
// Date parsing
// ---------------------------------------------------------------------------

// parseHFDate converts a HuggingFace ISO 8601 timestamp to YYYY-MM-DD format.
// Returns an empty string if the date cannot be parsed.
func parseHFDate(isoDate string) string {
	if isoDate == "" {
		return ""
	}

	t, err := time.Parse(hfDateLayout, isoDate)
	if err != nil {
		return ""
	}

	return t.Format(hfDateOutputLayout)
}

// ---------------------------------------------------------------------------
// Format conversion
// ---------------------------------------------------------------------------

// convertHFFormat converts the publication to the requested format.
func convertHFFormat(pub *Publication, format ContentFormat) error {
	switch format {
	case FormatBibTeX:
		bibtex := assembleHFBibTeX(pub)
		pub.FullText = &FullTextContent{
			Content:       bibtex,
			ContentFormat: FormatBibTeX,
			ContentLength: len(bibtex),
			Truncated:     false,
		}
		return nil
	case FormatMarkdown:
		// Markdown is only available if already fetched for papers.
		if pub.FullText != nil && pub.FullText.ContentFormat == FormatMarkdown {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrFormatUnsupported, format)
	default:
		return fmt.Errorf("%w: %s", ErrFormatUnsupported, format)
	}
}

// assembleHFBibTeX assembles a BibTeX entry from publication metadata.
func assembleHFBibTeX(pub *Publication) string {
	year := ""
	if len(pub.Published) >= hfYearOnlyLength {
		year = pub.Published[:hfYearOnlyLength]
	}

	authorNames := make([]string, 0, len(pub.Authors))
	for _, a := range pub.Authors {
		authorNames = append(authorNames, a.Name)
	}
	authorStr := strings.Join(authorNames, hfBibTeXAuthorSeparator)

	// Extract a short key from the ID (strip source prefix).
	key := hfBibTeXKeyPrefix
	if idx := strings.Index(pub.ID, prefixedIDSeparator); idx >= 0 {
		key += pub.ID[idx+len(prefixedIDSeparator):]
	}

	return fmt.Sprintf(hfBibTeXTemplate,
		key,
		pub.Title,
		authorStr,
		year,
		pub.URL,
	)
}
