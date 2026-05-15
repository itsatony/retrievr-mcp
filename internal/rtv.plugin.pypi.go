package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// PyPI package-registry provider — v5 cycle 4 / v2.11.0.
//
// PyPI retired its XML-RPC `search` method in 2021 (abuse mitigation). The
// official free-text search lives only on the website's HTML page:
//   https://pypi.org/search/?q=<q>&page=<n>
//
// Search path strategy: GET the HTML search page and regex-extract the
// `<a class="package-snippet">` blocks for name, version, release date,
// and short description. The HTML structure is stable enough for this
// purpose (it's the same shape the `pip search` UX once consumed); on
// breakage the integration test catches it within the cycle's gate.
//
// Get path: GET https://pypi.org/pypi/<name>/json — official JSON
// endpoint, fully stable, returns full package metadata.
//
// Free, no auth required.
// Residency: US (Python Software Foundation, US-based non-profit).
// ---------------------------------------------------------------------------

const (
	pypiPluginID          = SourcePyPI
	pypiPluginName        = "PyPI"
	pypiPluginDescription = "Search the Python Package Index (pypi.org). Free, no auth required. Search returns lightweight hits (name, version, summary) parsed from the official HTML search page; Get returns the full JSON metadata for a known package. Cross-registry dedup keyed on '<ecosystem>:<name>'."

	pypiDefaultBaseURL = "https://pypi.org"
	pypiSearchPath     = "/search/"
	pypiJSONPathPrefix = "/pypi/"
	pypiJSONPathSuffix = "/json"
	pypiDefaultLimit   = 20
	pypiMaxLimitCap    = 100
	pypiDefaultRPS     = 5.0
	pypiDefaultTimeout = 15 * time.Second

	pypiIDPrefix      = "pypi:"
	pypiEcosystemID   = "pypi"
	pypiProjectURLFmt = "https://pypi.org/project/%s/"

	pypiParamQ    = "q"
	pypiParamPage = "page"

	pypiCategoriesHint = "PyPI search has no native category filter; filters.categories[*] are accepted but currently ignored. Pass classifier-style terms in the q field (e.g. \"web framework\")."

	pypiMetaKeyLicense = "pypi_license"
	pypiMetaKeySummary = "pypi_summary"
	pypiMetaKeyAuthor  = "pypi_author"
)

// ---------------------------------------------------------------------------
// PyPI HTML parser
// ---------------------------------------------------------------------------

// pypiSnippetRegex extracts each <a class="package-snippet"> block's body
// in one shot. The non-greedy group captures everything between the
// opening anchor and the closing </a>.
var pypiSnippetRegex = regexp.MustCompile(`(?s)<a class="package-snippet"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)

var pypiNameRegex = regexp.MustCompile(`<span class="package-snippet__name">([^<]+)</span>`)
var pypiVersionRegex = regexp.MustCompile(`<span class="package-snippet__version">([^<]+)</span>`)
var pypiReleasedRegex = regexp.MustCompile(`<time[^>]*datetime="([^"]+)"`)
var pypiDescriptionRegex = regexp.MustCompile(`<p class="package-snippet__description">([^<]*)</p>`)

// ---------------------------------------------------------------------------
// PyPI Get JSON shapes
// ---------------------------------------------------------------------------

type pypiPackageEnvelope struct {
	Info pypiInfo `json:"info"`
}

type pypiInfo struct {
	Name        string `json:"name,omitempty"`
	Version     string `json:"version,omitempty"`
	Summary     string `json:"summary,omitempty"`
	HomePage    string `json:"home_page,omitempty"`
	License     string `json:"license,omitempty"`
	Author      string `json:"author,omitempty"`
	AuthorEmail string `json:"author_email,omitempty"`
	ProjectURL  string `json:"project_url,omitempty"`
	PackageURL  string `json:"package_url,omitempty"`
	Description string `json:"description,omitempty"`
	Keywords    string `json:"keywords,omitempty"`
}

// ---------------------------------------------------------------------------
// PyPIPlugin
// ---------------------------------------------------------------------------

// PyPIPlugin implements SourcePlugin for the public PyPI registry.
// Thread-safe after Initialize.
type PyPIPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "pypi".
func (p *PyPIPlugin) ID() string { return pypiPluginID }

// Name returns the human-readable label.
func (p *PyPIPlugin) Name() string { return pypiPluginName }

// Description returns the LLM-facing one-liner.
func (p *PyPIPlugin) Description() string { return pypiPluginDescription }

// ContentTypes — package.
func (p *PyPIPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePackage} }

// NativeFormat — JSON for Get; lightweight metadata for Search.
func (p *PyPIPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *PyPIPlugin) AvailableFormats() []ContentFormat { return []ContentFormat{FormatJSON} }

// Capabilities reports PyPI's filter/sort surface.
func (p *PyPIPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false, // categories accepted but ignored — documented in hint
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       pypiMaxLimitCap,
		CategoriesHint:           pypiCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentCodeProvenance, IntentQuickLookup},
		Kinds:                    []ResultKind{KindCode},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *PyPIPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = pypiDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = pypiDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = pypiDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *PyPIPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a PyPI HTML-search query and parses the result blocks.
func (p *PyPIPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = pypiDefaultLimit
	}
	if limit > pypiMaxLimitCap {
		limit = pypiMaxLimitCap
	}

	hits, err := p.doSearch(ctx, params, limit)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	return &SearchResult{
		Total:   len(hits),
		Results: hits,
		HasMore: len(hits) == limit,
	}, nil
}

// Get retrieves the JSON envelope for a known package name.
func (p *PyPIPlugin) Get(ctx context.Context, id string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	reqURL := p.baseURL + pypiJSONPathPrefix + url.PathEscape(id) + pypiJSONPathSuffix
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("pypi: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("pypi: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: pypi package %s", ErrGetFailed, id)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("pypi: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var env pypiPackageEnvelope
	if err := json.NewDecoder(httpResp.Body).Decode(&env); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("pypi: decode response: %w", err)
	}
	p.recordSuccess()
	pub := pypiInfoToPublication(&env.Info)
	return &pub, nil
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *PyPIPlugin) doSearch(ctx context.Context, params SearchParams, limit int) ([]Publication, error) {
	q := url.Values{}
	q.Set(pypiParamQ, params.Query)
	if params.Offset > 0 && limit > 0 && params.Offset%limit == 0 {
		q.Set(pypiParamPage, strconv.Itoa(params.Offset/limit+1))
	}

	reqURL := p.baseURL + pypiSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("pypi: build request: %w", err)
	}
	req.Header.Set("Accept", "text/html")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pypi: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: pypi", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("pypi: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("pypi: read body: %w", err)
	}
	return pypiParseSearchHTML(string(body), limit), nil
}

// pypiParseSearchHTML extracts up to `limit` package snippets from the
// PyPI search page HTML. Each snippet is converted to a lightweight
// Publication. The shape is intentionally minimal — callers needing full
// metadata can follow up with Get().
func pypiParseSearchHTML(html string, limit int) []Publication {
	matches := pypiSnippetRegex.FindAllStringSubmatch(html, -1)
	if len(matches) > limit {
		matches = matches[:limit]
	}

	hits := make([]Publication, 0, len(matches))
	for _, m := range matches {
		body := m[2]
		name := firstSubmatch(pypiNameRegex, body)
		if name == "" {
			continue
		}
		version := firstSubmatch(pypiVersionRegex, body)
		released := firstSubmatch(pypiReleasedRegex, body)
		desc := htmlUnescapeBasic(firstSubmatch(pypiDescriptionRegex, body))

		published := released
		if len(published) >= 10 {
			published = published[:10]
		}

		meta := map[string]any{
			MetaKeyPackageID:      pypiEcosystemID + ":" + name,
			smetaPackageEcosystem: pypiEcosystemID,
			smetaPackageName:      name,
		}
		if version != "" {
			meta[smetaPackageVersion] = version
		}

		hits = append(hits, Publication{
			ID:             pypiIDPrefix + name,
			Source:         SourcePyPI,
			ContentType:    ContentTypePackage,
			Title:          name,
			Abstract:       desc,
			URL:            fmt.Sprintf(pypiProjectURLFmt, name),
			Published:      published,
			SourceMetadata: meta,
		})
	}
	return hits
}

// firstSubmatch returns the first capture group of re against s, or "".
func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// htmlUnescapeBasic handles the handful of entities the PyPI search page
// commonly emits. We avoid pulling html/template's full escaper for what
// is a one-line need; missing entities round-trip as literals.
func htmlUnescapeBasic(s string) string {
	r := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
	)
	return r.Replace(s)
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func pypiInfoToPublication(info *pypiInfo) Publication {
	name := info.Name
	authors := []Author{}
	if info.Author != "" {
		authors = append(authors, Author{Name: info.Author})
	}

	displayURL := info.PackageURL
	if displayURL == "" {
		displayURL = fmt.Sprintf(pypiProjectURLFmt, name)
	}

	keywords := []string{}
	if info.Keywords != "" {
		for _, kw := range strings.Split(info.Keywords, ",") {
			if k := strings.TrimSpace(kw); k != "" {
				keywords = append(keywords, k)
			}
		}
	}

	meta := map[string]any{
		MetaKeyPackageID:      pypiEcosystemID + ":" + name,
		smetaPackageEcosystem: pypiEcosystemID,
		smetaPackageName:      name,
	}
	if info.Version != "" {
		meta[smetaPackageVersion] = info.Version
	}
	if info.HomePage != "" {
		meta[smetaPackageHomeURL] = info.HomePage
	}
	if info.License != "" {
		meta[pypiMetaKeyLicense] = info.License
	}
	if info.Author != "" {
		meta[pypiMetaKeyAuthor] = info.Author
	}
	if len(keywords) > 0 {
		meta[smetaPackageKeywords] = keywords
	}
	if info.Summary != "" {
		meta[pypiMetaKeySummary] = info.Summary
	}

	return Publication{
		ID:             pypiIDPrefix + name,
		Source:         SourcePyPI,
		ContentType:    ContentTypePackage,
		Title:          name,
		Abstract:       info.Summary,
		URL:            displayURL,
		Authors:        authors,
		Categories:     keywords,
		License:        info.License,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *PyPIPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *PyPIPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
