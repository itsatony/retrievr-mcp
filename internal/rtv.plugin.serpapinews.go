package internal

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// SerpAPI Google News — v6 cycle 6 / v2.19.0.
//
// Same underlying SerpAPI account / API key as the cycle-4 web plugin,
// just with `engine=google_news`. We reuse the cycle-4 plugin's
// `doSearch(engine)` transport + `serpapiOrganicToPublication` wire
// mapping so the news engine inherits all the auth, error-mapping, and
// retry behavior at zero cost.
//
// Per-call credential: `serpapi` (deliberately shared with the cycle-4
// SourceSerpAPI plugin since SerpAPI keys are account-scoped, not
// engine-scoped). The credential layer's per-credential rate-limit
// bucket already isolates per-tenant spend.
//
// Residency: US (SerpAPI Inc.).
// ---------------------------------------------------------------------------

const (
	serpapinewsPluginID          = SourceSerpAPINews
	serpapinewsPluginName        = "SerpAPI (Google News)"
	serpapinewsPluginDescription = "Search Google News via SerpAPI's engine=google_news endpoint. Same SerpAPI API key as the cycle-4 web plugin (serpapi). Paid. Returns news articles with title, snippet, source, displayed_link, date."

	serpapinewsIDPrefix   = "serpapinews:"
	serpapinewsEngine     = "google_news"
	serpapinewsRPSDefault = 2.0
	serpapinewsTimeout    = 20 * time.Second

	serpapinewsCategoriesHint = "SerpAPI Google News: filters.categories[0] → 'gl' country code (us, de, gb, ...). Pass filters.language → 'hl' BCP-47. Domain include/exclude → site:/-site: tokens in q."
)

// ---------------------------------------------------------------------------
// SerpAPINewsPlugin
// ---------------------------------------------------------------------------

// SerpAPINewsPlugin implements SourcePlugin for the SerpAPI Google News
// engine. Composes the cycle-4 SerpAPIPlugin's transport so both
// plugins share auth + wire types + error mapping. Thread-safe after
// Initialize.
type SerpAPINewsPlugin struct {
	// The embedded SerpAPIPlugin owns the http client, baseURL, apiKey,
	// and health state. We expose a different SourcePlugin identity
	// while reusing the transport.
	inner *SerpAPIPlugin
}

// ID returns "serpapinews".
func (p *SerpAPINewsPlugin) ID() string { return serpapinewsPluginID }

// Name returns the human-readable label.
func (p *SerpAPINewsPlugin) Name() string { return serpapinewsPluginName }

// Description returns the LLM-facing one-liner.
func (p *SerpAPINewsPlugin) Description() string { return serpapinewsPluginDescription }

// ContentTypes — paper (news).
func (p *SerpAPINewsPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePaper} }

// NativeFormat — JSON.
func (p *SerpAPINewsPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *SerpAPINewsPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports SerpAPI Google News' filter/sort surface.
func (p *SerpAPINewsPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true, // gl (country) via filters.categories[0]
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     true,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   true,
		SupportsPagination:       false,
		MaxResultsPerQuery:       serpapiMaxLimitCap,
		CategoriesHint:           serpapinewsCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentNews, IntentQuickLookup},
		Kinds:                    []ResultKind{KindNews},
		RequiresCredential:       true,
	}
}

// Initialize wires the plugin from PluginConfig. Constructs the inner
// SerpAPIPlugin which provides the shared transport.
func (p *SerpAPINewsPlugin) Initialize(ctx context.Context, cfg PluginConfig) error {
	if p.inner == nil {
		p.inner = &SerpAPIPlugin{}
	}
	// Default the timeout to the news-engine value if the caller didn't
	// configure one explicitly.
	if cfg.Timeout.Duration == 0 {
		cfg.Timeout = Duration{Duration: serpapinewsTimeout}
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = serpapinewsRPSDefault
	}
	return p.inner.Initialize(ctx, cfg)
}

// Health reports current status (delegates to the inner SerpAPI plugin).
func (p *SerpAPINewsPlugin) Health(ctx context.Context) SourceHealth {
	return p.inner.Health(ctx)
}

// Search executes a SerpAPI Google News query via the shared transport.
func (p *SerpAPINewsPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	if p.inner == nil {
		return nil, fmt.Errorf("serpapinews: not initialized")
	}

	limit := params.Limit
	if limit <= 0 {
		limit = serpapiDefaultLimit
	}
	if limit > serpapiMaxLimitCap {
		limit = serpapiMaxLimitCap
	}

	// Resolve the shared SerpAPI credential. We re-use the
	// `serpapi` credential key on purpose — SerpAPI keys are
	// account-scoped, not engine-scoped, so a single key covers both
	// web and news.
	apiKey := CredentialFor(ctx, SourceSerpAPI, p.inner.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: serpapinews requires a serpapi API key (per-call: credentials.serpapi)", ErrCredentialRequired)
	}

	resp, err := p.inner.doSearch(ctx, params, limit, apiKey, serpapinewsEngine)
	if err != nil {
		// inner.doSearch does NOT touch health state — the cycle-4
		// outer Search is what calls recordSuccess/recordError. Mirror
		// that here so a news-only outage moves the inner plugin's
		// Health() indicator (Health() delegates to inner).
		p.inner.recordError(err)
		return nil, wrapSerpAPINewsError(err)
	}
	p.inner.recordSuccess()

	pubs := make([]Publication, 0, len(resp.OrganicResults))
	for i := range resp.OrganicResults {
		pubs = append(pubs, serpapiOrganicToPublication(&resp.OrganicResults[i], SourceSerpAPINews, serpapinewsIDPrefix))
	}
	return &SearchResult{
		Total:   int(resp.SearchInformation.TotalResults),
		Results: pubs,
		HasMore: false,
	}, nil
}

// Get is not wired in cycle 6.
func (p *SerpAPINewsPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: serpapinews Get is not wired in cycle 6", ErrFormatUnsupported)
}

// wrapSerpAPINewsError prepends "serpapinews: " to inner SerpAPI plugin
// errors so log readers can tell which engine produced the failure.
// Sentinel errors stay reachable via errors.Is because the original
// chain is preserved with %w.
func wrapSerpAPINewsError(err error) error {
	if err == nil {
		return nil
	}
	// Skip the prefix if the message already mentions serpapinews
	// (defensive — keeps re-wrapping idempotent).
	if strings.Contains(err.Error(), "serpapinews") {
		return err
	}
	return fmt.Errorf("serpapinews: %w", err)
}
