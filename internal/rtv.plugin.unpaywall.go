package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Unpaywall enrichment provider — Cycle 2 Wave-1 (free / no auth).
//
// API: https://api.unpaywall.org/v2/{doi}?email=<email>
//   GET — no auth, but a contact email is REQUIRED in the query string.
//   Response: { doi, is_oa, best_oa_location: { url, url_for_pdf, license } }
//
// Cycle 2 ships Unpaywall as both:
//   (a) A regular SourcePlugin (Search returns empty; Get takes a DOI).
//   (b) A post-merge enrichment hook the Router calls directly to fill in
//       PaperData.PDFURL on results that have a DOI but no upstream PDF.
//
// Toggle (b) via enrichment.unpaywall.enabled: true in YAML config.
// Cycle 3 generalizes this into a typed Enrichment interface that other
// providers (Firecrawl scrape, future re-rankers) plug into.
//
// Residency: public-research-infrastructure (OurResearch, US — bibliographic
// public-good metadata only). Admitted under eu_strict only with the
// IncludePublicResearch opt-in.
// ---------------------------------------------------------------------------

// Identity / config constants.
const (
	unpaywallPluginID          = SourceUnpaywall
	unpaywallPluginName        = "Unpaywall"
	unpaywallPluginDescription = "DOI → open-access PDF resolver. Used as a post-merge enrichment hook on paper results with a DOI but no upstream PDF link."

	unpaywallDefaultBaseURL = "https://api.unpaywall.org"
	unpaywallV2PathPrefix   = "/v2/"
	unpaywallEmailParam     = "email"

	unpaywallDefaultRPS = 5.0

	// Per Unpaywall guidelines, requests without a contact email are
	// throttled or blocked. We require email in PluginConfig.Extra.
	unpaywallExtraEmail = "email"
)

// ---------------------------------------------------------------------------
// Unpaywall wire types
// ---------------------------------------------------------------------------

type unpaywallResponse struct {
	DOI            string                `json:"doi"`
	Title          string                `json:"title"`
	IsOA           bool                  `json:"is_oa"`
	BestOALocation *unpaywallOALocation  `json:"best_oa_location,omitempty"`
	OALocations    []unpaywallOALocation `json:"oa_locations,omitempty"`
	Year           int                   `json:"year"`
	JournalName    string                `json:"journal_name,omitempty"`
	PublishedDate  string                `json:"published_date,omitempty"`
	Authors        []unpaywallAuthor     `json:"z_authors,omitempty"`
}

type unpaywallOALocation struct {
	URL       string `json:"url,omitempty"`
	URLForPDF string `json:"url_for_pdf,omitempty"`
	License   string `json:"license,omitempty"`
	HostType  string `json:"host_type,omitempty"`
	Version   string `json:"version,omitempty"`
}

type unpaywallAuthor struct {
	Family string `json:"family,omitempty"`
	Given  string `json:"given,omitempty"`
}

// ---------------------------------------------------------------------------
// UnpaywallPlugin
// ---------------------------------------------------------------------------

// UnpaywallPlugin implements SourcePlugin for the Unpaywall enrichment API.
type UnpaywallPlugin struct {
	baseURL    string
	email      string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID / Name / Description.
func (p *UnpaywallPlugin) ID() string                  { return unpaywallPluginID }
func (p *UnpaywallPlugin) Name() string                { return unpaywallPluginName }
func (p *UnpaywallPlugin) Description() string         { return unpaywallPluginDescription }
func (p *UnpaywallPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }
func (p *UnpaywallPlugin) NativeFormat() ContentFormat { return FormatJSON }
func (p *UnpaywallPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities — Search-via-fan-out is unsupported; Get is the only path.
func (p *UnpaywallPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    false,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: true,
		SupportsPagination:       false,
		MaxResultsPerQuery:       1,
		CategoriesHint:           "DOI lookup only — used as post-merge enrichment hook",
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentPrimarySource},
		Kinds:                    []ResultKind{KindPaper},
	}
}

// Residency — public-research-infrastructure.
func (*UnpaywallPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionPublicResearch,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Initialize.
func (p *UnpaywallPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = unpaywallDefaultRPS
	}
	p.email = stringFromExtra(cfg.Extra, unpaywallExtraEmail, "")

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = unpaywallDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = DefaultPluginTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health.
func (p *UnpaywallPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{Enabled: p.enabled, Healthy: p.healthy, RateLimit: p.rateLimit, LastError: p.lastError}
}

// Search is intentionally a no-op: Unpaywall has no keyword-search API,
// only DOI lookup. Returning an empty SearchResult lets Unpaywall sit in
// the registry without polluting fan-out fan-in.
func (p *UnpaywallPlugin) Search(_ context.Context, _ SearchParams) (*SearchResult, error) {
	return &SearchResult{Total: 0, Results: nil, HasMore: false}, nil
}

// Get takes a DOI and returns the corresponding Unpaywall record as a
// Publication with PDFURL + License + OpenAccess flag populated.
func (p *UnpaywallPlugin) Get(ctx context.Context, doi string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	if doi == "" {
		return nil, fmt.Errorf("%w: unpaywall Get requires a DOI", ErrInvalidID)
	}
	if p.email == "" {
		return nil, fmt.Errorf("%w: unpaywall requires enrichment.unpaywall.email or sources.unpaywall.extra.email", ErrCredentialRequired)
	}

	q := url.Values{}
	q.Set(unpaywallEmailParam, p.email)
	reqURL := p.baseURL + unpaywallV2PathPrefix + url.PathEscape(doi) + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("unpaywall: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("unpaywall: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: doi %q", ErrSourceNotFound, doi)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: unpaywall", ErrRateLimitExceeded)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("unpaywall: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp unpaywallResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		p.recordError(err)
		return nil, fmt.Errorf("unpaywall: decode response: %w", err)
	}
	p.recordSuccess()

	pub := unpaywallToPublication(resp)
	return &pub, nil
}

// EnrichPublication updates an existing Publication in place with Unpaywall
// data when the publication has a DOI but no PDFURL or OpenAccess flag.
// Returns true when something was filled in, false when nothing changed
// (allowing the Router to skip futile network calls in batches).
//
// Used by the Router post-merge enrichment loop (cycle 2 minimum-viable
// path; cycle 3 promotes this to a typed Enrichment middleware).
func (p *UnpaywallPlugin) EnrichPublication(ctx context.Context, pub *Publication) (bool, error) {
	if pub == nil || pub.DOI == "" {
		return false, nil
	}
	if pub.PDFURL != "" {
		return false, nil // already has a PDF link
	}

	got, err := p.Get(ctx, pub.DOI, nil, FormatNative)
	if err != nil {
		// Don't surface enrichment errors as fatal; just skip.
		return false, err
	}
	changed := false
	if got.PDFURL != "" && pub.PDFURL == "" {
		pub.PDFURL = got.PDFURL
		changed = true
	}
	if got.License != "" && pub.License == "" {
		pub.License = got.License
		changed = true
	}
	// Stamp open_access into SourceMetadata for the converter to pick up.
	if pub.SourceMetadata == nil {
		pub.SourceMetadata = map[string]any{}
	}
	if _, exists := pub.SourceMetadata["open_access"]; !exists {
		// Resolve from the Unpaywall response via the temp Publication's metadata.
		if v, ok := got.SourceMetadata["open_access"]; ok {
			pub.SourceMetadata["open_access"] = v
			changed = true
		}
	}
	return changed, nil
}

// ---------------------------------------------------------------------------
// Mapping
// ---------------------------------------------------------------------------

func unpaywallToPublication(r unpaywallResponse) Publication {
	pub := Publication{
		ID:          fmt.Sprintf("%s:%s", SourceUnpaywall, r.DOI),
		Source:      SourceUnpaywall,
		ContentType: ContentTypePaper,
		Title:       r.Title,
		DOI:         r.DOI,
		Published:   r.PublishedDate,
	}
	for _, a := range r.Authors {
		name := strings.TrimSpace(a.Given + " " + a.Family)
		if name != "" {
			pub.Authors = append(pub.Authors, Author{Name: name})
		}
	}
	if loc := r.BestOALocation; loc != nil {
		if loc.URLForPDF != "" {
			pub.PDFURL = loc.URLForPDF
		}
		if loc.URL != "" {
			pub.URL = loc.URL
		}
		if loc.License != "" {
			pub.License = loc.License
		}
	}
	if r.JournalName != "" {
		pub.SourceMetadata = map[string]any{"venue": r.JournalName}
	}
	if r.IsOA {
		if pub.SourceMetadata == nil {
			pub.SourceMetadata = map[string]any{}
		}
		pub.SourceMetadata["open_access"] = true
	}
	return pub
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *UnpaywallPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *UnpaywallPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
