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
// EUR-Lex provider — v5 cycle 5 / v2.12.0.
//
// EUR-Lex publishes EU regulations, directives, decisions, and CJEU case
// law. Its first-class web service is SOAP-only (registration required);
// no clean JSON API exists. The HTML search page is the only free
// machine-readable path:
//
//   GET https://eur-lex.europa.eu/search.html?text=<q>&type=quick
//   Accept: text/html
//
// The plugin extracts result entries by hunting for CELEX identifiers in
// anchor hrefs (`/legal-content/EN/TXT/?uri=CELEX:NNNNNNNN`) plus the
// anchor text as title. The CELEX number becomes the citation_code and
// dedup key.
//
// Fragility is documented in retrievr_v5.md §12. The integration test
// gate catches structural breakage early.
//
// Free, no auth required.
// Residency: EU (Publications Office of the EU, Luxembourg).
// ---------------------------------------------------------------------------

const (
	eurLexPluginID          = SourceEURLex
	eurLexPluginName        = "EUR-Lex"
	eurLexPluginDescription = "Search EUR-Lex (EU regulations, directives, decisions, CJEU case law) via the public HTML search page. Free, no auth. CELEX identifier serves as the dedup citation code. EU-resident (Luxembourg). Emits paper-typed results with Result.Kind = KindLaw."

	eurLexDefaultBaseURL = "https://eur-lex.europa.eu"
	eurLexSearchPath     = "/search.html"
	eurLexDefaultLimit   = 20
	eurLexMaxLimitCap    = 50
	eurLexDefaultRPS     = 2.0
	eurLexDefaultTimeout = 20 * time.Second

	eurLexIDPrefix = "eurlex:"

	eurLexCategoriesHint = "EUR-Lex 'sector' codes: 1=treaties, 2=international agreements, 3=legislation, 4=international, 5=preparatory, 6=case law, 7=national implementing, 8=national case law, 9=parliamentary. Pass via filters.categories[0]."

	eurLexParamText = "text"
	eurLexParamType = "type"
	eurLexParamLang = "lang"
	eurLexTypeQuick = "quick"
)

// ---------------------------------------------------------------------------
// HTML parsing regexes
// ---------------------------------------------------------------------------

// eurLexAnchorCELEXRegex matches anchor tags whose href carries a CELEX
// identifier. Capture group 1 is the CELEX number, group 2 is the anchor
// text (used as title).
var eurLexAnchorCELEXRegex = regexp.MustCompile(`(?s)<a[^>]+href="[^"]*CELEX:([0-9A-Z]+)"[^>]*>(.*?)</a>`)

// eurLexCELEXRegex matches CELEX identifiers in any URL context (used as
// fallback when the anchor variant doesn't catch a result).
var eurLexCELEXRegex = regexp.MustCompile(`CELEX:([0-9A-Z]+)`)

// eurLexTagStripRegex removes simple inline tags so the anchor body
// becomes plaintext.
var eurLexTagStripRegex = regexp.MustCompile(`<[^>]+>`)

// ---------------------------------------------------------------------------
// EURLexPlugin
// ---------------------------------------------------------------------------

// EURLexPlugin implements SourcePlugin for the EUR-Lex public search
// page. Thread-safe after Initialize.
type EURLexPlugin struct {
	baseURL    string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "eurlex".
func (p *EURLexPlugin) ID() string { return eurLexPluginID }

// Name returns the human-readable label.
func (p *EURLexPlugin) Name() string { return eurLexPluginName }

// Description returns the LLM-facing one-liner.
func (p *EURLexPlugin) Description() string { return eurLexPluginDescription }

// ContentTypes — paper (law results carry the KindLaw discriminator).
func (p *EURLexPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — HTML upstream, JSON shape exposed to callers.
func (p *EURLexPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *EURLexPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports EUR-Lex's filter/sort surface.
func (p *EURLexPlugin) Capabilities() SourceCapabilities {
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
		SupportsLanguageFilter:   true,
		SupportsPagination:       false,
		MaxResultsPerQuery:       eurLexMaxLimitCap,
		CategoriesHint:           eurLexCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource, IntentReference},
		Kinds:                    []ResultKind{KindLaw},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *EURLexPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = eurLexDefaultRPS
	}

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = eurLexDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = eurLexDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *EURLexPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes an EUR-Lex HTML search query.
func (p *EURLexPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = eurLexDefaultLimit
	}
	if limit > eurLexMaxLimitCap {
		limit = eurLexMaxLimitCap
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

// Get is not wired in cycle 5 — EUR-Lex document pages are HTML; the
// CELEX-keyed search result already carries the dedup identifier and a
// stable URL.
func (p *EURLexPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: eurlex Get is not wired in cycle 5", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *EURLexPlugin) doSearch(ctx context.Context, params SearchParams, limit int) ([]Publication, error) {
	q := url.Values{}
	q.Set(eurLexParamText, params.Query)
	q.Set(eurLexParamType, eurLexTypeQuick)
	if lang := strings.TrimSpace(params.Filters.Language); lang != "" {
		q.Set(eurLexParamLang, strings.ToLower(lang))
	}

	reqURL := p.baseURL + eurLexSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("eurlex: build request: %w", err)
	}
	req.Header.Set("Accept", "text/html")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("eurlex: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: eurlex", ErrRateLimitExceeded)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("eurlex: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("eurlex: read body: %w", err)
	}
	lang := strings.ToUpper(strings.TrimSpace(params.Filters.Language))
	if lang == "" {
		lang = "EN"
	}
	return eurLexParseSearchHTML(string(body), limit, lang), nil
}

// eurLexParseSearchHTML extracts up to `limit` CELEX-keyed entries from
// the rendered search page. Duplicates of the same CELEX (the same
// document is often linked multiple times on the page — title, badge,
// PDF link) are collapsed to a single result.
func eurLexParseSearchHTML(html string, limit int, lang string) []Publication {
	hits := make([]Publication, 0, limit)
	seen := make(map[string]bool)

	add := func(celex, title string) {
		if celex == "" || seen[celex] {
			return
		}
		seen[celex] = true
		hits = append(hits, eurLexCELEXToPublication(celex, title, lang))
	}

	matches := eurLexAnchorCELEXRegex.FindAllStringSubmatch(html, -1)
	for _, m := range matches {
		if len(hits) >= limit {
			break
		}
		celex := strings.TrimSpace(m[1])
		title := htmlUnescapeBasic(strings.TrimSpace(eurLexTagStripRegex.ReplaceAllString(m[2], " ")))
		title = strings.Join(strings.Fields(title), " ")
		if len(title) > 280 {
			title = title[:280] + "…"
		}
		add(celex, title)
	}

	// Fallback: pure CELEX scan if anchors didn't yield enough hits.
	if len(hits) < limit {
		cmatches := eurLexCELEXRegex.FindAllStringSubmatch(html, -1)
		for _, m := range cmatches {
			if len(hits) >= limit {
				break
			}
			add(strings.TrimSpace(m[1]), "")
		}
	}
	return hits
}

// eurLexCELEXToPublication builds a Publication for a CELEX number plus
// optional title. The display URL uses EUR-Lex's stable language-tagged
// content path.
func eurLexCELEXToPublication(celex, title, lang string) Publication {
	if title == "" {
		title = celex
	}
	displayURL := fmt.Sprintf("https://eur-lex.europa.eu/legal-content/%s/TXT/?uri=CELEX:%s", lang, celex)

	citationCode := celex // CELEX doubles as the law-family dedup key.

	meta := map[string]any{
		MetaKeyCitationCode:  citationCode,
		smetaLawCitationCode: citationCode,
		smetaLawCelex:        celex,
		smetaLawJurisdiction: "EU",
	}

	return Publication{
		ID:             eurLexIDPrefix + celex,
		Source:         SourceEURLex,
		ContentType:    ContentTypePaper,
		Title:          title,
		URL:            displayURL,
		Language:       strings.ToLower(lang),
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *EURLexPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *EURLexPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
