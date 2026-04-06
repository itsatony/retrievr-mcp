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
)

// ---------------------------------------------------------------------------
// Credential acceptance
// ---------------------------------------------------------------------------

// sourcesAcceptingCredentials identifies sources that accept per-call credentials.
var sourcesAcceptingCredentials = map[string]bool{
	SourcePubMed:      true,
	SourceS2:          true,
	SourceOpenAlex:    true,
	SourceHuggingFace: true,
}

// SourceAcceptsCredentials returns true if the given source supports per-call credentials.
func SourceAcceptsCredentials(sourceID string) bool {
	return sourcesAcceptingCredentials[sourceID]
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
}

// NewRouter creates a Router wired to the given plugins and infrastructure.
// The plugins map is defensively copied. A nil logger is replaced with a discard logger.
// The metrics parameter is optional (nil disables Prometheus instrumentation).
func NewRouter(
	cfg RouterConfig,
	plugins map[string]SourcePlugin,
	serverDefaults map[string]string,
	cache *Cache,
	rateLimits *SourceRateLimitManager,
	creds *CredentialResolver,
	metrics *Metrics,
	logger *slog.Logger,
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

	return &Router{
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
	}
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

	// Step 1: Resolve sources.
	resolved := r.resolveSources(sources)
	if len(resolved) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrSearchFailed, errDetailNoValidSources)
	}

	// Step 2: Generate request ID and log.
	requestID := GenerateRequestID()
	ctx = WithRequestID(ctx, requestID)
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
				return &MergedSearchResult{
					TotalResults:   cached.Total,
					Results:        cached.Results,
					SourcesQueried: resolved,
					SourcesFailed:  []string{},
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

	// All sources failed.
	if len(sourcesFailed) == len(resolved) {
		return nil, fmt.Errorf("%w: "+errDetailAllSourcesFailed, ErrAllSourcesFailed, len(resolved))
	}

	// Step 6-7: Dedup.
	if r.dedupEnabled {
		allResults = dedup(allResults)
	}

	// Step 8: Sort.
	allResults = sortResults(allResults, params.Sort)

	// Step 9: Truncate.
	hasMore := false
	if len(allResults) > params.Limit {
		allResults = allResults[:params.Limit]
		hasMore = true
	}

	merged := &MergedSearchResult{
		TotalResults:   len(allResults),
		Results:        allResults,
		SourcesQueried: sourcesQueried,
		SourcesFailed:  sourcesFailed,
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
	// plugins resolve credentials internally via creds.ResolveForSource(). We only
	// need the deterministic bucket key for per-credential rate limiting.
	serverDefault := r.serverDefaults[sourceID]
	_, bucketKey := r.credentials.Resolve(sourceID, creds, serverDefault)

	// Rate limit wait.
	// Double %w wrapping is intentional (Go 1.20+ multi-error): errors.Is()
	// matches both the sentinel (ErrSearchFailed) and the underlying cause.
	if r.rateLimits != nil {
		throttled, err := r.rateLimits.Wait(ctx, sourceID, bucketKey)
		if err != nil {
			return sourceResult{sourceID: sourceID, err: fmt.Errorf("%w: %s: %w", ErrSearchFailed, sourceID, err), duration: time.Since(start)}
		}
		if throttled {
			r.metrics.RecordRateLimitWait(sourceID)
		}
	}

	// Per-source timeout.
	childCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	result, err := plugin.Search(childCtx, params, creds)
	if err != nil {
		return sourceResult{sourceID: sourceID, err: fmt.Errorf("%w: %s: %w", ErrSearchFailed, sourceID, err), duration: time.Since(start)}
	}

	return sourceResult{sourceID: sourceID, result: result, duration: time.Since(start)}
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

	// Step 4: Credential resolution + rate limit wait.
	// Only the bucket key is needed here; plugins resolve credentials internally.
	serverDefault := r.serverDefaults[sourceID]
	_, bucketKey := r.credentials.Resolve(sourceID, creds, serverDefault)

	if r.rateLimits != nil {
		throttled, err := r.rateLimits.Wait(ctx, sourceID, bucketKey)
		if err != nil {
			r.metrics.RecordGet(sourceID, metricStatusError)
			return nil, fmt.Errorf("%w: %w", ErrGetFailed, err)
		}
		if throttled {
			r.metrics.RecordRateLimitWait(sourceID)
		}
	}

	// Step 5: Per-source timeout + plugin call.
	// When BibTeX is requested, call the plugin with FormatNative and generate
	// BibTeX centrally after retrieval. This avoids per-plugin BibTeX duplication.
	pluginFormat := format
	if format == FormatBibTeX {
		pluginFormat = FormatNative
	}

	childCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	pub, err := plugin.Get(childCtx, rawID, include, pluginFormat, creds)
	if err != nil {
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

		infos = append(infos, SourceInfo{
			ID:                     plugin.ID(),
			Name:                   plugin.Name(),
			Description:            plugin.Description(),
			Enabled:                true, // registered implies enabled
			ContentTypes:           plugin.ContentTypes(),
			NativeFormat:           plugin.NativeFormat(),
			AvailableFormats:       plugin.AvailableFormats(),
			SupportsFullText:       caps.SupportsFullText,
			SupportsCitations:      caps.SupportsCitations,
			SupportsDateFilter:     caps.SupportsDateFilter,
			SupportsAuthorFilter:   caps.SupportsAuthorFilter,
			SupportsCategoryFilter: caps.SupportsCategoryFilter,
			RateLimit: RateLimitInfo{
				RequestsPerSecond: health.RateLimit,
				Remaining:         remaining,
			},
			CategoriesHint:     caps.CategoriesHint,
			AcceptsCredentials: SourceAcceptsCredentials(plugin.ID()),
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

// dedup removes duplicate publications from the merged result list using
// exact-match on DOI or ArXiv ID. The first occurrence (by deterministic
// source order) becomes the primary; duplicates contribute to AlsoFoundIn,
// and their citation counts and source metadata are merged.
func dedup(results []Publication) []Publication {
	if len(results) == 0 {
		return results
	}

	// Index maps: identifier → index of first occurrence.
	doiIndex := make(map[string]int, len(results))
	arxivIndex := make(map[string]int, len(results))
	keep := make([]bool, len(results))
	for i := range keep {
		keep[i] = true
	}

	for i := range results {
		// Check DOI dedup. When a duplicate is found, `continue` intentionally
		// skips the ArXiv ID registration below. This means a duplicate's
		// secondary identifiers are not indexed — transitive cross-identifier
		// dedup is out of scope per the "exact-match dedup only" design.
		if results[i].DOI != "" {
			if primaryIdx, exists := doiIndex[results[i].DOI]; exists {
				keep[i] = false
				mergeInto(&results[primaryIdx], &results[i])
				continue
			}
			doiIndex[results[i].DOI] = i
		}

		// Check ArXiv ID dedup.
		if results[i].ArXivID != "" {
			if primaryIdx, exists := arxivIndex[results[i].ArXivID]; exists {
				keep[i] = false
				mergeInto(&results[primaryIdx], &results[i])
				continue
			}
			arxivIndex[results[i].ArXivID] = i
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
