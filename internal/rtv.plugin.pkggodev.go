package internal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// pkg.go.dev package-registry provider — v5 cycle 4 / v2.11.0.
//
// pkg.go.dev does not expose a free-text-search JSON API. The user-facing
// HTML search page at https://pkg.go.dev/search?q=<q>&m=package is the
// only entry point. This plugin extracts the rendered SearchSnippet
// blocks with stable, well-marked CSS classes — fragile relative to a
// JSON API but adequate for the cycle's gate. Risk tracked in
// retrievr_v5.md §12.
//
// Get path is left as a follow-on; package metadata via pkg.go.dev
// requires HTML parsing of the package page, which is more fragile than
// search. Use deps.dev (api.deps.dev/v3) for richer cross-system
// lookups in a future cycle.
//
// Free, no auth required.
// Residency: US (Google, Mountain View).
// ---------------------------------------------------------------------------

const (
	pkggoPluginID          = SourcePkgGoDev
	pkggoPluginName        = "pkg.go.dev"
	pkggoPluginDescription = "Search the Go module index (pkg.go.dev). Free, no auth required. Returns package import path, version, synopsis, import-count, license. NOTE: result extraction parses the HTML search page — pkg.go.dev has no free-text-search JSON API. Cross-registry dedup keyed on '<ecosystem>:<importPath>'."

	pkggoDefaultBaseURL = "https://pkg.go.dev"
	pkggoSearchPath     = "/search"
	pkggoDefaultLimit   = 20
	pkggoMaxLimitCap    = 50
	pkggoDefaultRPS     = 2.0
	pkggoDefaultTimeout = 15 * time.Second

	pkggoIDPrefix    = "pkggodev:"
	pkggoEcosystemID = "pkggodev"
	pkggoPagePathFmt = "https://pkg.go.dev/%s"

	pkggoParamQ    = "q"
	pkggoParamMode = "m"
	pkggoModePkg   = "package"

	pkggoCategoriesHint = "pkg.go.dev has no category filter. The synopsis text is the only structured signal beyond name."

	pkggoMetaKeyLicense    = "pkggodev_license"
	pkggoMetaKeyImportedBy = "pkggodev_imported_by"
	pkggoMetaKeyImportPath = "pkggodev_import_path"
)

// ---------------------------------------------------------------------------
// HTML parsing regexes
// ---------------------------------------------------------------------------

// pkggoSnippetRegex captures each rendered SearchSnippet block. The
// (?s) flag lets `.` match newlines.
var pkggoSnippetRegex = regexp.MustCompile(`(?s)<div class="SearchSnippet[^"]*"[^>]*>(.*?)</div>\s*</div>`)

// pkggoTitleRegex extracts the import path from the snippet title link.
// pkg.go.dev wraps the import path in <a data-test-id="snippet-title" href="/<path>">
var pkggoTitleRegex = regexp.MustCompile(`<a[^>]*data-test-id="snippet-title"[^>]*href="/([^"]+)"`)

var pkggoNameRegex = regexp.MustCompile(`data-test-id="snippet-title-name"[^>]*>([^<]+)</span>`)
var pkggoSynopsisRegex = regexp.MustCompile(`(?s)data-test-id="snippet-synopsis"[^>]*>(.*?)</p>`)
var pkggoLicenseRegex = regexp.MustCompile(`data-test-id="snippet-license"[^>]*>([^<]+)</span>`)
var pkggoImportedByRegex = regexp.MustCompile(`data-test-id="snippet-importedby"[^>]*>Imported by\s*([0-9,]+)`)
var pkggoPublishedRegex = regexp.MustCompile(`data-test-id="snippet-published"[^>]*>([^<]+)</span>`)

// ---------------------------------------------------------------------------
// PkgGoDevPlugin
// ---------------------------------------------------------------------------

// PkgGoDevPlugin implements SourcePlugin for the pkg.go.dev search page.
// Thread-safe after Initialize.
type PkgGoDevPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "pkggodev".
func (p *PkgGoDevPlugin) ID() string { return pkggoPluginID }

// Name returns the human-readable label.
func (p *PkgGoDevPlugin) Name() string { return pkggoPluginName }

// Description returns the LLM-facing one-liner.
func (p *PkgGoDevPlugin) Description() string { return pkggoPluginDescription }

// ContentTypes — package.
func (p *PkgGoDevPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePackage} }

// NativeFormat — HTML upstream; JSON shape exposed to callers.
func (p *PkgGoDevPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *PkgGoDevPlugin) AvailableFormats() []ContentFormat { return []ContentFormat{FormatJSON} }

// Capabilities reports pkg.go.dev's filter/sort surface (essentially
// none beyond relevance).
func (p *PkgGoDevPlugin) Capabilities() SourceCapabilities {
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
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       false,
		MaxResultsPerQuery:       pkggoMaxLimitCap,
		CategoriesHint:           pkggoCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentCodeProvenance, IntentQuickLookup},
		Kinds:                    []ResultKind{KindCode},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *PkgGoDevPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = pkggoDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = pkggoDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = pkggoDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *PkgGoDevPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes a pkg.go.dev HTML search query.
func (p *PkgGoDevPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = pkggoDefaultLimit
	}
	if limit > pkggoMaxLimitCap {
		limit = pkggoMaxLimitCap
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
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 4 — pkg.go.dev's package page is also
// HTML-only and adds an additional fragility surface we don't need for
// the cycle's gate. deps.dev would be the right Get backend; deferred.
func (p *PkgGoDevPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: pkggodev Get is not wired in cycle 4", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *PkgGoDevPlugin) doSearch(ctx context.Context, params SearchParams, limit int) ([]Publication, error) {
	q := url.Values{}
	q.Set(pkggoParamQ, params.Query)
	q.Set(pkggoParamMode, pkggoModePkg)

	reqURL := p.baseURL + pkggoSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("pkggodev: build request: %w", err)
	}
	req.Header.Set("Accept", "text/html")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pkggodev: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: pkggodev", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("pkggodev: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("pkggodev: read body: %w", err)
	}
	return pkggoParseSearchHTML(string(body), limit), nil
}

// pkggoParseSearchHTML extracts up to `limit` package snippets from the
// rendered pkg.go.dev search page. Each snippet becomes a lightweight
// Publication keyed on the package import path.
func pkggoParseSearchHTML(html string, limit int) []Publication {
	matches := pkggoSnippetRegex.FindAllStringSubmatch(html, -1)
	if len(matches) > limit {
		matches = matches[:limit]
	}

	hits := make([]Publication, 0, len(matches))
	for _, m := range matches {
		body := m[1]

		importPath := firstSubmatch(pkggoTitleRegex, body)
		if importPath == "" {
			continue
		}
		importPath = strings.TrimSpace(importPath)

		name := firstSubmatch(pkggoNameRegex, body)
		if name == "" {
			// Fall back to last path segment.
			parts := strings.Split(importPath, "/")
			name = parts[len(parts)-1]
		}

		synopsis := htmlUnescapeBasic(strings.TrimSpace(firstSubmatch(pkggoSynopsisRegex, body)))
		license := firstSubmatch(pkggoLicenseRegex, body)
		importedBy := firstSubmatch(pkggoImportedByRegex, body)
		publishedRaw := firstSubmatch(pkggoPublishedRegex, body)

		meta := map[string]any{
			MetaKeyPackageID:       pkggoEcosystemID + ":" + importPath,
			smetaPackageEcosystem:  pkggoEcosystemID,
			smetaPackageName:       name,
			pkggoMetaKeyImportPath: importPath,
		}
		if license != "" {
			meta[pkggoMetaKeyLicense] = license
		}
		if importedBy != "" {
			meta[pkggoMetaKeyImportedBy] = strings.ReplaceAll(importedBy, ",", "")
		}

		hits = append(hits, Publication{
			ID:             pkggoIDPrefix + importPath,
			Source:         SourcePkgGoDev,
			ContentType:    ContentTypePackage,
			Title:          importPath,
			Abstract:       synopsis,
			URL:            fmt.Sprintf(pkggoPagePathFmt, importPath),
			Published:      publishedRaw,
			License:        license,
			SourceMetadata: meta,
		})
	}
	return hits
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *PkgGoDevPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *PkgGoDevPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
