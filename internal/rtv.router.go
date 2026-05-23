package internal

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Router constants
// ---------------------------------------------------------------------------

const (
	// dedupHeadroomMultiplier is the factor applied to the requested limit
	// when querying individual sources to provide headroom for deduplication.
	dedupHeadroomMultiplier = 2

	// prefixedIDSeparator is the delimiter between source ID and raw ID
	// in prefixed publication identifiers (e.g., "arxiv:2401.12345").
	prefixedIDSeparator = ":"

	// Error detail strings for ParsePrefixedID and router operations.
	errDetailMissingSeparator = "missing separator in %q"
	errDetailEmptySource      = "empty source in %q"
	errDetailEmptyID          = "empty id in %q"
	errDetailUnknownSource    = "unknown source %q"
	errDetailAllSourcesFailed = "queried %d sources"
	errDetailNoValidSources   = "no valid sources in request"
)

// ---------------------------------------------------------------------------
// Router log message constants
// ---------------------------------------------------------------------------

const (
	logMsgSearchStart        = "search started"
	logMsgSearchComplete     = "search complete"
	logMsgSourceSearchFailed = "source search failed"
	logMsgGetStart           = "get started"
	logMsgGetComplete        = "get complete"
	logMsgListSources        = "list sources"
	logMsgCacheHit           = "cache hit"
	logMsgCacheMiss          = "cache miss"
	logMsgFallbackAttempt    = "fallback chain attempt"
)

// ---------------------------------------------------------------------------
// Credential acceptance
// ---------------------------------------------------------------------------

// sourcesAcceptingOptionalCredentials lists plugins that operate anonymously
// but accept an optional API key for higher quotas. Used by
// SourceAcceptsCredentials together with the capability-derived
// RequiresCredential flag — the latter captures fail-fast paid plugins
// across the v2-v6 catalog; this map captures the legacy
// optional-key surface (pubmed, s2, openalex, huggingface, ads).
//
// Adding a new optional-key plugin: register it here. Adding a new
// must-have-key plugin: set Capabilities.RequiresCredential=true on
// the plugin and don't touch this map.
var sourcesAcceptingOptionalCredentials = map[string]bool{
	SourcePubMed:      true,
	SourceS2:          true,
	SourceOpenAlex:    true,
	SourceHuggingFace: true,
	SourceADS:         true,
}

// SourceAcceptsCredentials returns true if the given source supports
// per-call credentials. Looks up the plugin factory by sourceID,
// instantiates it, reads Capabilities().RequiresCredential, and falls
// back to the legacy sourcesAcceptingOptionalCredentials allowlist for
// pre-RequiresCredential sources (pubmed, s2, openalex, hf, ads).
//
// The function is intentionally not cheap on a hot path because the
// only callers are introspection surfaces (rtv_list_sources, audit
// logs, docs generators). Cached lookup is not warranted today; if a
// hot caller appears, wrap with sync.Once-based memoization.
func SourceAcceptsCredentials(sourceID string) bool {
	if sourcesAcceptingOptionalCredentials[sourceID] {
		return true
	}
	factory, ok := PluginFactories()[sourceID]
	if !ok {
		return false
	}
	plugin := factory()
	return plugin.Capabilities().RequiresCredential
}

// sourceAcceptsCredentialsFromCaps is the preferred form. Returns true
// when the plugin either declares it strictly requires a key (paid
// plugin) or appears in the legacy optional-credential allowlist.
func sourceAcceptsCredentialsFromCaps(sourceID string, caps SourceCapabilities) bool {
	if caps.RequiresCredential {
		return true
	}
	return sourcesAcceptingOptionalCredentials[sourceID]
}

// ---------------------------------------------------------------------------
// Router struct
// ---------------------------------------------------------------------------

// Router dispatches searches to plugins concurrently, merges and deduplicates results.
// Thread-safe for concurrent use.
type Router struct {
	plugins        map[string]SourcePlugin
	defaultSources []string
	timeout        time.Duration
	dedupEnabled   bool
	serverDefaults map[string]string // sourceID → server API key
	cache          *Cache
	rateLimits     *SourceRateLimitManager
	credentials    *CredentialResolver
	metrics        *Metrics
	logger         *slog.Logger
	retry          RetryConfig
	fallback       RouterFallbackConfig

	// EU-mode (Cycle 2). Empty mode treated as "off"; gate is a no-op.
	// SearchParams may override per-call via the Mode field.
	euMode                  string
	euIncludePublicResearch bool
	auditSink               AuditSink
	auditLogQueryPlaintext  bool

	// Cycle 2 Wave-1 enrichment hook — Unpaywall is consulted post-merge
	// for paper results with a DOI but no upstream PDF link. nil = disabled.
	unpaywallEnrichment *UnpaywallPlugin
}

// DefaultFallbackConfig returns the default chains for each intent.
//
// All six intents declared in docs/intents.md are wired here. Unregistered
// sources (paid plugins missing a key, plugins disabled by config) are
// silently dropped by Router.filterRegistered — chains gracefully degrade
// to whatever subset is actually enabled in the running tenant.
//
// Operators wanting custom routing supply a complete RouterFallbackConfig
// via cfg.Fallback; resolveFallbackConfig refuses to merge partial overrides
// to avoid surprising defaults surviving when an intent is intentionally
// removed.
func DefaultFallbackConfig() RouterFallbackConfig {
	return RouterFallbackConfig{
		Chains: map[string]FallbackChain{
			// Scholarly retrieval — peer-reviewed + preprints.
			fallbackChainAcademic: {
				Primary:  []string{SourceS2, SourceOpenAlex},
				Fallback: []string{SourceArXiv, SourceCrossRef, SourceEuropePMC, SourcePubMed, SourceDBLP, SourceADS, SourceCORE, SourceOpenAIRE},
			},
			// Primary-source OA papers — same scholarly chain, biased
			// to providers that surface OA PDFs (europmc, openalex,
			// unpaywall enrichment runs post-merge regardless).
			fallbackChainPrimarySource: {
				Primary:  []string{SourceEuropePMC, SourceOpenAlex},
				Fallback: []string{SourceCrossRef, SourceS2, SourceArXiv, SourcePubMed, SourceCORE, SourceOpenAIRE, SourceZenodo},
			},
			// Fast web lookup — paid web search first, free fallback.
			fallbackChainQuickLookup: {
				Primary:  []string{SourceKagi, SourceMojeek, SourceSerpAPI},
				Fallback: []string{SourceBrave, SourceExa, SourceLinkup, SourceWikipedia},
			},
			// Code provenance — package registries first, then GitHub,
			// then CS literature.
			fallbackChainCodeProvenance: {
				Primary:  []string{SourceNPM, SourcePyPI, SourceCrates, SourcePkgGoDev},
				Fallback: []string{SourceGitHub, SourceArXiv, SourceDBLP, SourceS2},
			},
			// News — premium news APIs first, then open monitoring,
			// then web search as a last resort.
			fallbackChainNews: {
				Primary:  []string{SourceNewsAPI, SourceSerpAPINews},
				Fallback: []string{SourceGDELT, SourceBrave, SourceExa, SourceWikipedia},
			},
			// Structured facts + encyclopedia — structured engines
			// first, then knowledge graphs, then Wikipedia.
			fallbackChainReference: {
				Primary:  []string{SourceWolframAlpha, SourceKGAPI},
				Fallback: []string{SourceWikidata, SourceWikipedia},
			},
		},
		IntentToChain: map[string]string{
			string(IntentDeepResearch):   fallbackChainAcademic,
			string(IntentPrimarySource):  fallbackChainPrimarySource,
			string(IntentQuickLookup):    fallbackChainQuickLookup,
			string(IntentCodeProvenance): fallbackChainCodeProvenance,
			string(IntentNews):           fallbackChainNews,
			string(IntentReference):      fallbackChainReference,
		},
	}
}

const (
	fallbackChainAcademic       = "academic"
	fallbackChainPrimarySource  = "primary_source"
	fallbackChainQuickLookup    = "quick_lookup"
	fallbackChainCodeProvenance = "code_provenance"
	fallbackChainNews           = "news"
	fallbackChainReference      = "reference"
)

// RouterOption is a functional option used by NewRouter for cycle-2+
// additions (EU mode, audit sink) that don't fit the original positional
// signature. Existing 8-arg NewRouter callers don't break.
type RouterOption func(*Router)

// WithEUMode configures the router's jurisdictional gate (Hook #2 of EU
// mode). Empty mode is treated as off. includePublicResearch widens
// eu_strict to admit public-research-infrastructure providers (ArXiv,
// OpenAlex, Wikipedia, ...).
func WithEUMode(mode string, includePublicResearch bool) RouterOption {
	return func(r *Router) {
		r.euMode = mode
		r.euIncludePublicResearch = includePublicResearch
	}
}

// WithAuditSink installs an AuditSink. When unset, Router uses NoopAuditSink.
func WithAuditSink(sink AuditSink) RouterOption {
	return func(r *Router) {
		if sink != nil {
			r.auditSink = sink
		}
	}
}

// WithAuditLogQueryPlaintext, when true, opts in to recording the raw query
// in AuditEvent.QueryPlaintext alongside the always-present QueryHash.
// Default false (DSGVO Art. 5(1)(c) data minimization).
func WithAuditLogQueryPlaintext(v bool) RouterOption {
	return func(r *Router) {
		r.auditLogQueryPlaintext = v
	}
}

// WithUnpaywallEnrichment installs a configured *UnpaywallPlugin that the
// Router consults post-merge to fill PDFURL / License / OpenAccess on paper
// results carrying a DOI but no upstream PDF. Pass nil to disable.
//
// Cycle-2 minimum-viable wiring; cycle 3 promotes this to a typed
// Enrichment middleware that any provider can register against.
func WithUnpaywallEnrichment(plugin *UnpaywallPlugin) RouterOption {
	return func(r *Router) {
		r.unpaywallEnrichment = plugin
	}
}

// NewRouter creates a Router wired to the given plugins and infrastructure.
// The plugins map is defensively copied. A nil logger is replaced with a discard logger.
// The metrics parameter is optional (nil disables Prometheus instrumentation).
//
// Cycle-2 EU-mode + audit configuration goes through the variadic opts:
// pass WithEUMode / WithAuditSink / WithAuditLogQueryPlaintext as needed.
func NewRouter(
	cfg RouterConfig,
	plugins map[string]SourcePlugin,
	serverDefaults map[string]string,
	cache *Cache,
	rateLimits *SourceRateLimitManager,
	creds *CredentialResolver,
	metrics *Metrics,
	logger *slog.Logger,
	opts ...RouterOption,
) *Router {
	// Defensive copy of plugins map.
	pluginsCopy := make(map[string]SourcePlugin, len(plugins))
	maps.Copy(pluginsCopy, plugins)

	// Defensive copy of server defaults.
	defaults := make(map[string]string, len(serverDefaults))
	maps.Copy(defaults, serverDefaults)

	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}

	r := &Router{
		plugins:        pluginsCopy,
		defaultSources: cfg.DefaultSources,
		timeout:        cfg.PerSourceTimeout.Duration,
		dedupEnabled:   cfg.DedupEnabled,
		serverDefaults: defaults,
		cache:          cache,
		rateLimits:     rateLimits,
		credentials:    creds,
		metrics:        metrics,
		logger:         logger,
		retry:          resolveRetryConfig(cfg.Retry),
		fallback:       resolveFallbackConfig(cfg.Fallback),
		auditSink:      NoopAuditSink(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// resolveFallbackConfig returns the user-supplied config if it has any
// chains, otherwise falls back to DefaultFallbackConfig. We intentionally
// do NOT merge: callers wanting custom chains should supply a complete
// RouterFallbackConfig, not partial overrides (avoids surprising defaults
// surviving when the operator thinks they removed an intent).
func resolveFallbackConfig(c RouterFallbackConfig) RouterFallbackConfig {
	if len(c.Chains) == 0 && len(c.IntentToChain) == 0 {
		return DefaultFallbackConfig()
	}
	return c
}

// resolveRetryConfig folds RouterRetryConfig (YAML shape, zero values allowed)
// into the canonical RetryConfig, substituting defaults for any unset field.
func resolveRetryConfig(c RouterRetryConfig) RetryConfig {
	def := DefaultRetryConfig()
	out := RetryConfig{
		MaxAttempts:    c.MaxAttempts,
		BaseDelay:      c.BaseDelay.Duration,
		MaxDelay:       c.MaxDelay.Duration,
		JitterFraction: c.JitterFraction,
	}
	if out.MaxAttempts <= 0 {
		out.MaxAttempts = def.MaxAttempts
	}
	if out.BaseDelay <= 0 {
		out.BaseDelay = def.BaseDelay
	}
	if out.MaxDelay <= 0 {
		out.MaxDelay = def.MaxDelay
	}
	if c.JitterFraction == 0 {
		// Distinguish "explicit 0" from "unset" — RouterRetryConfig is YAML-
		// origin; zero from YAML is most likely "unset", so substitute the
		// equal-jitter default. Callers wanting deterministic backoff (no
		// jitter) should construct RetryConfig directly via NewRouter+test
		// harness, not via YAML config.
		out.JitterFraction = def.JitterFraction
	}
	return out
}

// ---------------------------------------------------------------------------
// ParsePrefixedID
// ---------------------------------------------------------------------------

// ParsePrefixedID splits a prefixed publication ID (e.g., "arxiv:2401.12345")
// into its source ID and raw ID components. Only the first colon is used as
// the separator, so IDs like "huggingface:paper/2401.12345" are handled correctly.
func ParsePrefixedID(prefixedID string) (sourceID, rawID string, err error) {
	var found bool
	sourceID, rawID, found = strings.Cut(prefixedID, prefixedIDSeparator)
	if !found {
		return "", "", fmt.Errorf("%w: "+errDetailMissingSeparator, ErrInvalidID, prefixedID)
	}

	if sourceID == "" {
		return "", "", fmt.Errorf("%w: "+errDetailEmptySource, ErrInvalidID, prefixedID)
	}
	if rawID == "" {
		return "", "", fmt.Errorf("%w: "+errDetailEmptyID, ErrInvalidID, prefixedID)
	}
	if !IsValidSourceID(sourceID) {
		return "", "", fmt.Errorf("%w: "+errDetailUnknownSource, ErrSourceNotFound, sourceID)
	}

	return sourceID, rawID, nil
}

// ---------------------------------------------------------------------------
// sourceResult — fan-out collection type
// ---------------------------------------------------------------------------

// sourceResult collects the outcome of a single source's search.
type sourceResult struct {
	sourceID string
	result   *SearchResult
	err      error
	duration time.Duration
}

// ---------------------------------------------------------------------------
// Router.Search
// ---------------------------------------------------------------------------

// Search fans out the query to the requested sources concurrently, merges,
// deduplicates, sorts, and truncates the results. Partial failures are
// handled gracefully — working sources return results while failed sources
// are reported in SourcesFailed. Returns ErrAllSourcesFailed only when
// every requested source fails.
func (r *Router) Search(
	ctx context.Context,
	params SearchParams,
	sources []string,
	creds *CallCredentials,
) (*MergedSearchResult, error) {
	start := time.Now()

	// Step 0a: Filter-shape validation. RFC3339 PublishedAfter / PublishedBefore
	// must parse; if both are set, after must be <= before. Reject malformed
	// input here so plugins downstream can trust the field shape (and avoid
	// silent downcast / silent zero-time bugs).
	if err := validatePublishedWindow(params.Filters); err != nil {
		return nil, err
	}

	// Step 0: EU-mode refusal path (Hook #5). Reject eu_strict + explicit
	// non-EU sources up-front rather than silently dropping them.
	if err := validateEUModeSources(sources, r.plugins, r.euMode, r.euIncludePublicResearch); err != nil {
		return nil, err
	}

	// Step 1: Resolve sources. Three precedence levels:
	//   (a) explicit `sources` arg — overrides everything; no fallback walking.
	//   (b) params.Intent set — chain-based primary set + ordered fallback list.
	//   (c) default — Router.defaultSources, no fallback walking.
	var resolved, fallbackList []string
	switch {
	case len(sources) > 0:
		resolved = r.resolveSources(sources)
	case params.Intent != "":
		resolved, fallbackList = r.resolveByIntent(params.Intent)
	default:
		resolved = r.resolveSources(nil)
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrSearchFailed, errDetailNoValidSources)
	}

	// Step 1.5: EU-mode gate (Hook #2). Filter resolved sources by jurisdiction.
	// In eu_strict, providers without EU residency (or public-research, when
	// the opt-in is set) are skipped with a structured reason. Skipped fallback
	// sources are filtered too — we don't want fallback to silently re-introduce
	// non-EU providers that the gate rejected from the primary set.
	gate := applyEUGate(resolved, r.plugins, r.euMode, r.euIncludePublicResearch)
	resolved = gate.Admitted
	euSkipped := gate.Skipped
	if len(fallbackList) > 0 && r.euMode == EUModeStrict {
		fbGate := applyEUGate(fallbackList, r.plugins, r.euMode, r.euIncludePublicResearch)
		fallbackList = fbGate.Admitted
		euSkipped = append(euSkipped, fbGate.Skipped...)
	}
	if len(resolved) == 0 {
		// Gate filtered everyone out — likely eu_strict with no EU sources
		// configured. Return a useful empty merged result so the caller can
		// surface the skip reasons rather than getting a generic error.
		auditRef := r.emitAuditEvent(ctx, params, nil, euSkipped, nil, false, false, false)
		return &MergedSearchResult{
			TotalResults:   0,
			Results:        []Publication{},
			SourcesQueried: []string{},
			SourcesFailed:  []string{},
			SourcesSkipped: euSkipped,
			AuditRef:       auditRef,
			HasMore:        false,
		}, nil
	}

	// Step 2: Generate request ID and log.
	requestID := GenerateRequestID()
	ctx = WithRequestID(ctx, requestID)
	logEUGateDecision(ctx, r.logger, requestID, gate)
	r.logger.Info(logMsgSearchStart,
		slog.String(LogKeyRequestID, requestID),
		slog.Any(LogKeySources, resolved),
		slog.String(LogKeyQuery, params.Query),
		slog.Int(LogKeyLimit, params.Limit),
	)

	// Step 3: Check cache.
	cacheKey := ""
	if r.cache != nil {
		var err error
		cacheKey, err = GenerateCacheKey(params, resolved)
		if err == nil {
			if cached, hit := r.cache.Get(cacheKey); hit {
				r.logger.Info(logMsgCacheHit,
					slog.String(LogKeyRequestID, requestID),
					slog.String(LogKeyCacheKey, cacheKey),
				)
				// Cache stores only successful results. On hit, SourcesFailed is
				// always empty because partial-failure results are not cached —
				// only the merged/deduped output from successful sources is stored.
				auditRef := r.emitAuditEvent(ctx, params, resolved, euSkipped, nil, false, false, true)
				return &MergedSearchResult{
					TotalResults:   cached.Total,
					Results:        cached.Results,
					SourcesQueried: resolved,
					SourcesFailed:  []string{},
					SourcesSkipped: euSkipped,
					AuditRef:       auditRef,
					HasMore:        cached.HasMore,
				}, nil
			}
			r.logger.Debug(logMsgCacheMiss,
				slog.String(LogKeyRequestID, requestID),
				slog.String(LogKeyCacheKey, cacheKey),
			)
		}
	}

	// Step 4: Fan-out to all resolved sources.
	fanoutParams := params
	fanoutParams.Limit = params.Limit * dedupHeadroomMultiplier

	var mu sync.Mutex
	collected := make([]sourceResult, 0, len(resolved))
	var wg sync.WaitGroup

	for _, srcID := range resolved {
		wg.Add(1)
		go func(sourceID string) {
			defer wg.Done()
			res := r.searchOneSource(ctx, sourceID, fanoutParams, creds)
			mu.Lock()
			collected = append(collected, res)
			mu.Unlock()
		}(srcID)
	}
	wg.Wait()

	// Step 5: Classify results.
	// Initialize as empty slices (not nil) to ensure consistent JSON serialization
	// (produces [] rather than null) matching the cache-hit path.
	var (
		sourcesQueried = make([]string, 0, len(resolved))
		sourcesFailed  = make([]string, 0)
		allResults     []Publication
	)

	// Process in deterministic source-ID order for stable dedup primary selection.
	slices.SortFunc(collected, func(a, b sourceResult) int {
		return strings.Compare(a.sourceID, b.sourceID)
	})

	for _, sr := range collected {
		sourcesQueried = append(sourcesQueried, sr.sourceID)
		if sr.err != nil {
			sourcesFailed = append(sourcesFailed, sr.sourceID)
			r.metrics.RecordSearch(sr.sourceID, metricStatusError, sr.duration)
			r.logger.Warn(logMsgSourceSearchFailed,
				slog.String(LogKeyRequestID, requestID),
				slog.String(LogKeySource, sr.sourceID),
				slog.String(LogKeyError, sr.err.Error()),
			)
			continue
		}
		r.metrics.RecordSearch(sr.sourceID, metricStatusSuccess, sr.duration)
		if sr.result != nil {
			allResults = append(allResults, sr.result.Results...)
		}
	}

	// All sources failed in the primary set. If a fallback chain is configured
	// for this intent, walk it now: each fallback source is queried in order
	// until one yields any hit or the list is exhausted. Otherwise return.
	if len(sourcesFailed) == len(resolved) {
		if len(fallbackList) == 0 {
			return nil, fmt.Errorf("%w: "+errDetailAllSourcesFailed, ErrAllSourcesFailed, len(resolved))
		}
		// Walk fallback. Continues from current sourcesQueried/sourcesFailed.
		extra := r.walkFallback(ctx, fallbackList, fanoutParams, creds, requestID)
		sourcesQueried, sourcesFailed, allResults = applyFallbackWalk(sourcesQueried, sourcesFailed, allResults, extra)
		if len(allResults) == 0 {
			return nil, fmt.Errorf("%w: "+errDetailAllSourcesFailed, ErrAllSourcesFailed, len(sourcesQueried))
		}
	} else if len(allResults) == 0 && len(fallbackList) > 0 {
		// Primary returned successfully but with zero hits. Walk fallback.
		extra := r.walkFallback(ctx, fallbackList, fanoutParams, creds, requestID)
		sourcesQueried, sourcesFailed, allResults = applyFallbackWalk(sourcesQueried, sourcesFailed, allResults, extra)
	}

	// Step 6-7: Dedup.
	if r.dedupEnabled {
		allResults = dedup(allResults)
	}

	// Step 7.5: Post-merge enrichment (Cycle 2 task #17). Iterates paper
	// results with a DOI but no PDFURL and consults Unpaywall to fill in
	// the OA PDF link + license + open_access flag. Errors are swallowed —
	// enrichment failures must never fail the search.
	r.enrichWithUnpaywall(ctx, allResults)

	// Step 7.7: PublishedAfter / PublishedBefore exact-window trim (v2.22.0).
	// No-op when neither bound is set. Hits with missing/unparseable
	// published_at are kept by default and dropped only when
	// Filters.StrictPublishedAt is true. Runs before sort so the truncate
	// pages over the post-filtered set.
	allResults = filterByPublishedWindow(allResults, params.Filters)

	// Step 8: Sort.
	allResults = sortResults(allResults, params.Sort)

	// Step 9: Truncate.
	hasMore := false
	if len(allResults) > params.Limit {
		allResults = allResults[:params.Limit]
		hasMore = true
	}

	// Track fallback-walked status for audit. The fallback block above mutates
	// `sourcesQueried` so we deduce the flag by checking whether any non-
	// originally-resolved source ID appeared.
	fallbackWalked := false
	if len(fallbackList) > 0 {
		resolvedSet := make(map[string]struct{}, len(resolved))
		for _, id := range resolved {
			resolvedSet[id] = struct{}{}
		}
		for _, id := range sourcesQueried {
			if _, ok := resolvedSet[id]; !ok {
				fallbackWalked = true
				break
			}
		}
	}

	auditRef := r.emitAuditEvent(ctx, params, sourcesQueried, euSkipped, sourcesFailed, fallbackWalked, false, false)

	merged := &MergedSearchResult{
		TotalResults:   len(allResults),
		Results:        allResults,
		SourcesQueried: sourcesQueried,
		SourcesFailed:  sourcesFailed,
		SourcesSkipped: euSkipped,
		AuditRef:       auditRef,
		FallbackWalked: fallbackWalked,
		HasMore:        hasMore,
	}

	// Step 10: Cache result.
	if r.cache != nil && cacheKey != "" {
		r.cache.Set(cacheKey, &SearchResult{
			Total:   merged.TotalResults,
			Results: merged.Results,
			HasMore: merged.HasMore,
		})
	}

	// Step 11: Log completion.
	r.logger.Info(logMsgSearchComplete,
		slog.String(LogKeyRequestID, requestID),
		slog.Int(LogKeyResultCnt, len(allResults)),
		slog.Any(LogKeySources, sourcesQueried),
		slog.Duration(LogKeyDuration, time.Since(start)),
	)

	return merged, nil
}

// searchOneSource executes a search against a single source plugin,
// handling credential resolution, rate limiting, and timeout.
func (r *Router) searchOneSource(
	ctx context.Context,
	sourceID string,
	params SearchParams,
	creds *CallCredentials,
) sourceResult {
	start := time.Now()
	plugin := r.plugins[sourceID]

	// Credential resolution. The resolved credential string is not used here —
	// plugins read credentials from ctx via CredentialFor. We only need the
	// deterministic bucket key for per-credential rate limiting.
	serverDefault := r.serverDefaults[sourceID]
	_, bucketKey := r.credentials.Resolve(sourceID, creds, serverDefault)

	// Attach legacy *CallCredentials to ctx so plugins can read it via
	// CredentialFor (the new map-based path is mirrored in by pkg/retrievr.Client).
	ctx = WithCallCredentials(ctx, creds)

	// Build the per-source resilience chain. Order outermost → innermost:
	// retry → rate-limit → timeout → plugin. Retry above rate-limit so each
	// attempt consumes its own bucket token (matches liz DC-145).
	var result *SearchResult
	op := func(opCtx context.Context) error {
		var e error
		result, e = plugin.Search(opCtx, params)
		return e
	}
	chain := chainPluginMW(
		withRetry(r.retry, r.logger, sourceID, nil),
		withRateLimit(r.rateLimits, r.metrics, sourceID, bucketKey),
		withTimeout(r.timeout),
	)
	if err := chain(op)(ctx); err != nil {
		return sourceResult{sourceID: sourceID, err: fmt.Errorf("%w: %s: %w", ErrSearchFailed, sourceID, err), duration: time.Since(start)}
	}

	return sourceResult{sourceID: sourceID, result: result, duration: time.Since(start)}
}

// walkFallback queries each fallback source in order, stopping at the first
// source that returns at least one result. Returns the per-source results
// collected (whether successful or failed). Does NOT mutate Router state.
func (r *Router) walkFallback(
	ctx context.Context,
	fallbackList []string,
	params SearchParams,
	creds *CallCredentials,
	requestID string,
) []sourceResult {
	collected := make([]sourceResult, 0, len(fallbackList))
	for _, sourceID := range fallbackList {
		if err := ctx.Err(); err != nil {
			return collected
		}
		r.logger.Debug(logMsgFallbackAttempt,
			slog.String(LogKeyRequestID, requestID),
			slog.String(LogKeySource, sourceID),
		)
		res := r.searchOneSource(ctx, sourceID, params, creds)
		collected = append(collected, res)
		// Short-circuit on first hit. err == nil + at least one Result.
		if res.err == nil && res.result != nil && len(res.result.Results) > 0 {
			return collected
		}
	}
	return collected
}

// applyFallbackWalk merges per-source fallback results into the running
// accumulators (sourcesQueried, sourcesFailed, allResults).
func applyFallbackWalk(
	sourcesQueried, sourcesFailed []string,
	allResults []Publication,
	extra []sourceResult,
) ([]string, []string, []Publication) {
	for _, sr := range extra {
		sourcesQueried = append(sourcesQueried, sr.sourceID)
		if sr.err != nil {
			sourcesFailed = append(sourcesFailed, sr.sourceID)
			continue
		}
		if sr.result != nil {
			allResults = append(allResults, sr.result.Results...)
		}
	}
	return sourcesQueried, sourcesFailed, allResults
}

// SearchV2 runs Search and converts the merged Publication list into the
// v2 fat-struct shape (Result with Kind discriminator + per-kind blocks).
// Cycle 2 v1.6.0 entry point for callers wanting the new wire shape;
// v1.7+ may make this the default and v2.0.0 retires Search().
//
// Internally a thin wrapper: Router.Search performs the resilience chain,
// fan-out, dedup, sort, truncate, audit, etc. SearchV2 only reshapes the
// Publication slice.
func (r *Router) SearchV2(
	ctx context.Context,
	params SearchParams,
	sources []string,
	creds *CallCredentials,
) (*MergedSearchResultV2, error) {
	merged, err := r.Search(ctx, params, sources, creds)
	if err != nil {
		return nil, err
	}
	return &MergedSearchResultV2{
		TotalResults:   merged.TotalResults,
		Results:        r.PublicationsToResults(merged.Results),
		SourcesQueried: merged.SourcesQueried,
		SourcesFailed:  merged.SourcesFailed,
		SourcesSkipped: merged.SourcesSkipped,
		AuditRef:       merged.AuditRef,
		FallbackWalked: merged.FallbackWalked,
		EUFallbackUsed: merged.EUFallbackUsed,
		HasMore:        merged.HasMore,
	}, nil
}

// enrichWithUnpaywall iterates paper results with a DOI but no PDFURL and
// fills them via the configured Unpaywall plugin. No-op when no plugin is
// installed. Per-result errors are logged at Debug and skipped — enrichment
// failures must never fail the search.
func (r *Router) enrichWithUnpaywall(ctx context.Context, pubs []Publication) {
	if r.unpaywallEnrichment == nil {
		return
	}
	for i := range pubs {
		if pubs[i].DOI == "" || pubs[i].PDFURL != "" {
			continue
		}
		if _, err := r.unpaywallEnrichment.EnrichPublication(ctx, &pubs[i]); err != nil {
			r.logger.Debug("retrievr unpaywall enrichment failed",
				slog.String("doi", pubs[i].DOI),
				slog.String(LogKeyError, err.Error()),
			)
		}
	}
}

// emitAuditEvent constructs and dispatches an AuditEvent describing this
// Search call (Hook #3 of EU mode). Returns the generated audit_ref so the
// caller can echo it in MergedSearchResult.AuditRef. Safe with a nil sink:
// Router defaults to NoopAuditSink when no WithAuditSink option is supplied.
func (r *Router) emitAuditEvent(
	ctx context.Context,
	params SearchParams,
	invoked []string,
	skipped []SkipNote,
	failed []string,
	fallbackWalked, euFallbackUsed, cacheHit bool,
) string {
	ref := generateAuditRef()
	evt := AuditEvent{
		AuditRef:         ref,
		Mode:             r.euMode,
		Intent:           string(params.Intent),
		QueryHash:        hashQuery(params.Query),
		ProvidersInvoked: invoked,
		ProvidersSkipped: skipped,
		ProvidersFailed:  failed,
		FallbackWalked:   fallbackWalked,
		EUFallbackUsed:   euFallbackUsed,
		CacheHit:         cacheHit,
		Ts:               time.Now().UTC(),
	}
	if r.auditLogQueryPlaintext {
		evt.QueryPlaintext = params.Query
	}
	if r.auditSink != nil {
		r.auditSink.Emit(ctx, evt)
	}
	return ref
}

// resolveByIntent returns the primary source set + ordered fallback list for
// the given intent. When intent is empty or unmapped, returns
// (defaultSources, nil) — preserving legacy behavior.
//
// Both lists are filtered through the plugin registry (unknown source IDs
// are dropped silently) but NOT deduplicated against each other; a source
// that appears in both primary and fallback for some chain is fine.
func (r *Router) resolveByIntent(intent Intent) (primary, fallback []string) {
	if intent == "" {
		return r.filterRegistered(r.defaultSources), nil
	}
	chainName, ok := r.fallback.IntentToChain[string(intent)]
	if !ok {
		return r.filterRegistered(r.defaultSources), nil
	}
	chain, ok := r.fallback.Chains[chainName]
	if !ok {
		return r.filterRegistered(r.defaultSources), nil
	}
	return r.filterRegistered(chain.Primary), r.filterRegistered(chain.Fallback)
}

// filterRegistered returns sources filtered to only those present in the
// plugin map, preserving order.
func (r *Router) filterRegistered(sources []string) []string {
	if len(sources) == 0 {
		return nil
	}
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		if _, ok := r.plugins[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

// resolveSources returns the list of source IDs to query, filtered to only
// those that are registered in the router's plugin map. If sources is nil or
// empty, the router's default sources are used.
func (r *Router) resolveSources(sources []string) []string {
	if len(sources) == 0 {
		sources = r.defaultSources
	}

	resolved := make([]string, 0, len(sources))
	for _, s := range sources {
		if _, ok := r.plugins[s]; ok {
			resolved = append(resolved, s)
		}
	}
	return resolved
}

// ---------------------------------------------------------------------------
// Router.Get
// ---------------------------------------------------------------------------

// Get retrieves a single publication by its prefixed ID (e.g., "arxiv:2401.12345").
// The prefix is parsed to route to the correct plugin, then stripped before
// calling the plugin's Get method.
func (r *Router) Get(
	ctx context.Context,
	prefixedID string,
	include []IncludeField,
	format ContentFormat,
	creds *CallCredentials,
) (*Publication, error) {
	start := time.Now()

	// Step 1: Parse prefixed ID.
	sourceID, rawID, err := ParsePrefixedID(prefixedID)
	if err != nil {
		return nil, err
	}

	// Step 2: Look up plugin.
	plugin, ok := r.plugins[sourceID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSourceNotFound, sourceID)
	}

	// Step 3: Request ID + logging.
	requestID := GenerateRequestID()
	ctx = WithRequestID(ctx, requestID)
	r.logger.Info(logMsgGetStart,
		slog.String(LogKeyRequestID, requestID),
		slog.String(LogKeySource, sourceID),
		slog.String(LogKeyPubID, rawID),
	)

	// Step 4: Credential resolution. Plugins read credentials from ctx via
	// CredentialFor; here we only need the deterministic bucket key for the
	// per-credential rate limiter.
	serverDefault := r.serverDefaults[sourceID]
	_, bucketKey := r.credentials.Resolve(sourceID, creds, serverDefault)

	// Step 5: Plugin call through the resilience chain (retry → rate-limit →
	// timeout). When BibTeX is requested, call the plugin with FormatNative
	// and generate BibTeX centrally after retrieval to avoid per-plugin
	// BibTeX duplication.
	pluginFormat := format
	if format == FormatBibTeX {
		pluginFormat = FormatNative
	}

	ctx = WithCallCredentials(ctx, creds)

	var pub *Publication
	op := func(opCtx context.Context) error {
		var e error
		pub, e = plugin.Get(opCtx, rawID, include, pluginFormat)
		return e
	}
	chain := chainPluginMW(
		withRetry(r.retry, r.logger, sourceID, nil),
		withRateLimit(r.rateLimits, r.metrics, sourceID, bucketKey),
		withTimeout(r.timeout),
	)
	if err := chain(op)(ctx); err != nil {
		r.metrics.RecordGet(sourceID, metricStatusError)
		return nil, fmt.Errorf("%w: %s: %w", ErrGetFailed, sourceID, err)
	}

	// Step 5.5: Central BibTeX generation.
	if format == FormatBibTeX {
		bibtex, bibErr := GenerateBibTeX(pub)
		if bibErr != nil {
			r.metrics.RecordGet(sourceID, metricStatusError)
			return nil, fmt.Errorf("%w: %s: %w", ErrGetFailed, sourceID, bibErr)
		}
		pub.FullText = &FullTextContent{
			Content:       bibtex,
			ContentFormat: FormatBibTeX,
			ContentLength: len(bibtex),
			Truncated:     false,
		}
	}

	r.metrics.RecordGet(sourceID, metricStatusSuccess)

	// Step 6: Log completion.
	r.logger.Info(logMsgGetComplete,
		slog.String(LogKeyRequestID, requestID),
		slog.String(LogKeySource, sourceID),
		slog.Duration(LogKeyDuration, time.Since(start)),
	)

	return pub, nil
}

// ---------------------------------------------------------------------------
// Router.ListSources
// ---------------------------------------------------------------------------

// ListSources returns information about all registered plugins, sorted by ID
// for deterministic output.
func (r *Router) ListSources(ctx context.Context) []SourceInfo {
	infos := make([]SourceInfo, 0, len(r.plugins))

	for _, plugin := range r.plugins {
		caps := plugin.Capabilities()
		health := plugin.Health(ctx)

		remaining := float64(0)
		if r.rateLimits != nil {
			remaining = r.rateLimits.Remaining(plugin.ID(), CredentialAnonymous)
		}

		residency := plugin.Residency()
		acceptsCreds := sourceAcceptsCredentialsFromCaps(plugin.ID(), caps)
		requiresKey := caps.RequiresCredential

		infos = append(infos, SourceInfo{
			ID:                       plugin.ID(),
			Name:                     plugin.Name(),
			Description:              plugin.Description(),
			Enabled:                  true, // registered implies enabled
			ContentTypes:             plugin.ContentTypes(),
			NativeFormat:             plugin.NativeFormat(),
			AvailableFormats:         plugin.AvailableFormats(),
			SupportsFullText:         caps.SupportsFullText,
			SupportsCitations:        caps.SupportsCitations,
			SupportsDateFilter:       caps.SupportsDateFilter,
			SupportsAuthorFilter:     caps.SupportsAuthorFilter,
			SupportsCategoryFilter:   caps.SupportsCategoryFilter,
			SupportsOpenAccessFilter: caps.SupportsOpenAccessFilter,
			SupportsDomainFilter:     caps.SupportsDomainFilter,
			SupportsChannelFilter:    caps.SupportsChannelFilter,
			SupportsLanguageFilter:   caps.SupportsLanguageFilter,
			SupportsSortRelevance:    caps.SupportsSortRelevance,
			SupportsSortDate:         caps.SupportsSortDate,
			SupportsSortCitations:    caps.SupportsSortCitations,
			SupportsPagination:       caps.SupportsPagination,
			MaxResultsPerQuery:       caps.MaxResultsPerQuery,
			RateLimit: RateLimitInfo{
				RequestsPerSecond: health.RateLimit,
				Remaining:         remaining,
			},
			CategoriesHint:     caps.CategoriesHint,
			AcceptsCredentials: acceptsCreds,

			// Cycle-2 additions surfaced for LLM agents + compliance review.
			Kinds:           caps.Kinds,
			QueryIntents:    caps.QueryIntents,
			Region:          residency.Region,
			DPAStatus:       residency.DPAStatus,
			SubprocessorURL: residency.SubprocessorURL,
			// FreeTier means the plugin works without a key (anonymous
			// or optional-credential). RequiresKey means it refuses
			// without one (paid / strictly-keyed).
			FreeTier:    !requiresKey,
			RequiresKey: requiresKey,

			SupportsPublishedAfterFilter: caps.SupportsPublishedAfterFilter,
		})
	}

	// Sort by ID for deterministic output.
	slices.SortFunc(infos, func(a, b SourceInfo) int {
		return strings.Compare(a.ID, b.ID)
	})

	r.logger.Debug(logMsgListSources,
		slog.Int(LogKeyResultCnt, len(infos)),
	)

	return infos
}

// ---------------------------------------------------------------------------
// Deduplication
// ---------------------------------------------------------------------------

// Dedup-key family constants. Index maps in dedup() are keyed by
// (family, value); the family bucket guarantees cross-class dedup is
// impossible (a video and a paper with the same string key never collide).
const (
	dedupFamilyDOI       = "doi"
	dedupFamilyArXivID   = "arxiv_id"
	dedupFamilyYouTubeID = "youtube_id"
	dedupFamilyOSMID     = "osm_id"
	dedupFamilyCoord     = "coord"
	dedupFamilyWikimedia = "wikimedia_file"
	dedupFamilyMediaURL  = "media_url"
	dedupFamilyAtproto   = "atproto_uri"
	dedupFamilyPostURL   = "post_url"
	dedupFamilyQA        = "qa_question_id"
	dedupFamilyPackage   = "package_id"
	dedupFamilyPatent    = "patent_number"
	dedupFamilyLaw       = "citation_code"
	dedupFamilyAudio     = "audio_id"

	// dedupCoordPrecisionFmt rounds lat/lon to 5 decimal places (~1 m),
	// applied to place results lacking osm_id. Two places within ~1 m of
	// each other are considered the same point of interest.
	dedupCoordPrecisionFmt = "%.5f,%.5f"
)

// dedup removes duplicate publications from the merged result list using
// per-ContentType exact-match keys. The first occurrence (by deterministic
// source order) becomes the primary; duplicates contribute to AlsoFoundIn,
// and their citation counts and source metadata are merged.
//
// Dedup-key family by ContentType:
//   - paper / model / dataset / "" / any → DOI, then ArXiv ID
//   - video → SourceMetadata["youtube_id"]
//   - place → SourceMetadata["osm_id"], then (lat,lon) rounded to 5 dp
//   - image → SourceMetadata["wikimedia_file"], then MediaURL
//   - post  → SourceMetadata["atproto_uri"], then URL
//
// Cross-class merging is impossible by construction: index keys carry a
// family tag, so a video with key "X" and a paper with key "X" never collide.
func dedup(results []Publication) []Publication {
	if len(results) == 0 {
		return results
	}

	// Single composite index keyed by (family, value). Cross-family collisions
	// are impossible because the family is part of the key.
	type dkey struct {
		family string
		value  string
	}
	index := make(map[dkey]int, len(results)*2)
	keep := make([]bool, len(results))
	for i := range keep {
		keep[i] = true
	}

	// tryDedup checks the (family, value) index. On hit, marks i as a
	// duplicate, merges into the primary, and returns true. On miss,
	// registers i as the primary for this key and returns false.
	// Empty values are skipped (returns false without indexing).
	tryDedup := func(family, value string, i int) bool {
		if value == "" {
			return false
		}
		k := dkey{family: family, value: value}
		if primaryIdx, exists := index[k]; exists {
			keep[i] = false
			mergeInto(&results[primaryIdx], &results[i])
			return true
		}
		index[k] = i
		return false
	}

	for i := range results {
		switch results[i].ContentType {
		case ContentTypeVideo:
			tryDedup(dedupFamilyYouTubeID, stringFromMeta(results[i].SourceMetadata, MetaKeyYouTubeID), i)
		case ContentTypePlace:
			if tryDedup(dedupFamilyOSMID, stringFromMeta(results[i].SourceMetadata, MetaKeyOSMID), i) {
				continue
			}
			if results[i].Lat != nil && results[i].Lon != nil {
				tryDedup(dedupFamilyCoord, fmt.Sprintf(dedupCoordPrecisionFmt, *results[i].Lat, *results[i].Lon), i)
			}
		case ContentTypeImage:
			if tryDedup(dedupFamilyWikimedia, stringFromMeta(results[i].SourceMetadata, MetaKeyWikimediaFile), i) {
				continue
			}
			tryDedup(dedupFamilyMediaURL, results[i].MediaURL, i)
		case ContentTypePost:
			if tryDedup(dedupFamilyAtproto, stringFromMeta(results[i].SourceMetadata, MetaKeyAtprotoURI), i) {
				continue
			}
			tryDedup(dedupFamilyPostURL, results[i].URL, i)
		case ContentTypePackage:
			if tryDedup(dedupFamilyPackage, stringFromMeta(results[i].SourceMetadata, MetaKeyPackageID), i) {
				continue
			}
			// Some package records (Zenodo software releases) carry a
			// DOI; honour it as a secondary key inside the package
			// family so they still dedup when the ecosystem-prefixed
			// package_id wasn't synthesized upstream.
			tryDedup(dedupFamilyDOI, results[i].DOI, i)
		case ContentTypePatent:
			if tryDedup(dedupFamilyPatent, stringFromMeta(results[i].SourceMetadata, MetaKeyPatentNumber), i) {
				continue
			}
			// A patent record from googlepatents may fail to parse the
			// publication number under the unofficial xhr endpoint;
			// fall back to DOI when present (EPO OPS records also
			// carry an `epodoc` publication number that's effectively
			// a DOI-like identifier).
			tryDedup(dedupFamilyDOI, results[i].DOI, i)
		case ContentTypeAudio:
			if tryDedup(dedupFamilyAudio, stringFromMeta(results[i].SourceMetadata, MetaKeyAudioID), i) {
				continue
			}
			// Audio fallback: identical preview/audio URL.
			tryDedup(dedupFamilyMediaURL, results[i].MediaURL, i)
		default:
			// paper / model / dataset / "" / any — DOI + ArXiv ID family.
			// DOI hit short-circuits ArXivID indexing (matches v2 behavior:
			// secondary identifiers of a duplicate are not back-indexed).
			//
			// QA results (Stack Exchange, Hacker News) emit ContentTypePaper
			// with a populated MetaKeyQAQuestionID — the composite
			// "<site>:<id>" namespaces across sites by construction. They
			// have no DOI/ArXivID, so route them on the QA family.
			if qaKey := stringFromMeta(results[i].SourceMetadata, MetaKeyQAQuestionID); qaKey != "" {
				tryDedup(dedupFamilyQA, qaKey, i)
				continue
			}
			// Law results (CourtListener, EUR-Lex) emit ContentTypePaper
			// with MetaKeyCitationCode populated — route on the law
			// family so cross-citation dedup works (e.g. CourtListener
			// "410 U.S. 113" and a paper preprint referencing the same
			// case never collide because the family is distinct).
			if lawKey := stringFromMeta(results[i].SourceMetadata, MetaKeyCitationCode); lawKey != "" {
				tryDedup(dedupFamilyLaw, lawKey, i)
				continue
			}
			if tryDedup(dedupFamilyDOI, results[i].DOI, i) {
				continue
			}
			tryDedup(dedupFamilyArXivID, results[i].ArXivID, i)
		}
	}

	// Compact: build new slice from kept entries.
	compacted := make([]Publication, 0, len(results))
	for i, pub := range results {
		if keep[i] {
			compacted = append(compacted, pub)
		}
	}
	return compacted
}

// stringFromMeta returns the string value of meta[key] or "" if absent or
// non-string. Used by dedup() to read v3 multimodal dedup keys from the
// SourceMetadata map without panicking on type-asserts.
func stringFromMeta(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	v, ok := meta[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// mergeInto merges metadata from a duplicate publication into the primary.
func mergeInto(primary, duplicate *Publication) {
	// Track AlsoFoundIn.
	primary.AlsoFoundIn = appendUnique(primary.AlsoFoundIn, duplicate.Source)
	for _, src := range duplicate.AlsoFoundIn {
		primary.AlsoFoundIn = appendUnique(primary.AlsoFoundIn, src)
	}

	// Merge citation count: highest wins.
	primary.CitationCount = mergeCitationCount(primary.CitationCount, duplicate.CitationCount)

	// Merge source metadata.
	primary.SourceMetadata = mergeSourceMetadata(primary.SourceMetadata, duplicate.SourceMetadata)
}

// appendUnique appends value to slice only if it is not already present.
func appendUnique(slice []string, value string) []string {
	if slices.Contains(slice, value) {
		return slice
	}
	return append(slice, value)
}

// mergeCitationCount returns the higher of two citation count pointers.
// If one is nil, the other is returned. If both nil, nil is returned.
func mergeCitationCount(primary, duplicate *int) *int {
	if primary == nil && duplicate == nil {
		return nil
	}
	if primary == nil {
		return duplicate
	}
	if duplicate == nil {
		return primary
	}
	if *duplicate > *primary {
		return duplicate
	}
	return primary
}

// mergeSourceMetadata combines two source metadata maps.
// Primary keys take precedence on conflict.
func mergeSourceMetadata(primary, duplicate map[string]any) map[string]any {
	if primary == nil && duplicate == nil {
		return nil
	}

	merged := make(map[string]any, len(primary)+len(duplicate))

	// Copy duplicate first so primary overwrites on conflict.
	maps.Copy(merged, duplicate)
	maps.Copy(merged, primary)

	return merged
}

// ---------------------------------------------------------------------------
// Sorting
// ---------------------------------------------------------------------------

// sortResults re-sorts the integrated result list according to the requested order.
func sortResults(results []Publication, order SortOrder) []Publication {
	switch order {
	case SortRelevance:
		return roundRobinInterleave(results)
	case SortDateDesc:
		slices.SortStableFunc(results, func(a, b Publication) int {
			return strings.Compare(b.Published, a.Published)
		})
	case SortDateAsc:
		slices.SortStableFunc(results, func(a, b Publication) int {
			return strings.Compare(a.Published, b.Published)
		})
	case SortCitations:
		slices.SortStableFunc(results, func(a, b Publication) int {
			ci, cj := a.CitationCount, b.CitationCount
			if ci == nil && cj == nil {
				return 0
			}
			if ci == nil {
				return 1 // nil goes last
			}
			if cj == nil {
				return -1
			}
			return cmp.Compare(*cj, *ci) // descending
		})
	}
	return results
}

// roundRobinInterleave reorders results by interleaving them round-robin
// across sources. Source groups are ordered alphabetically by source ID
// for determinism. Within each source group, the original per-source
// ranking order is preserved.
func roundRobinInterleave(results []Publication) []Publication {
	if len(results) == 0 {
		return results
	}

	// Group results by source, preserving per-source order.
	groups := make(map[string][]Publication)
	for _, pub := range results {
		groups[pub.Source] = append(groups[pub.Source], pub)
	}

	// Sort source IDs for deterministic interleaving.
	sourceIDs := make([]string, 0, len(groups))
	for id := range groups {
		sourceIDs = append(sourceIDs, id)
	}
	slices.Sort(sourceIDs)

	// Interleave: take rank-N from each source in round-robin fashion.
	interleaved := make([]Publication, 0, len(results))
	rank := 0
	for len(interleaved) < len(results) {
		added := false
		for _, id := range sourceIDs {
			if rank < len(groups[id]) {
				interleaved = append(interleaved, groups[id][rank])
				added = true
			}
		}
		if !added {
			break
		}
		rank++
	}

	return interleaved
}
