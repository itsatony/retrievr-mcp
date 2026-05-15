package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---------------------------------------------------------------------------
// Tool name constants
// ---------------------------------------------------------------------------

const (
	ToolNameSearch      = "rtv_search"
	ToolNameGet         = "rtv_get"
	ToolNameListSources = "rtv_list_sources"
)

// ---------------------------------------------------------------------------
// Tool description constants
// ---------------------------------------------------------------------------

const (
	ToolDescSearch = `Search across published information: papers, preprints, AI models, datasets, web pages, news, code repos, packages, patents, court decisions, regulations, encyclopedia articles, multimedia, social posts, places, podcasts, and structured facts. 61 source plugins behind one tool. Call rtv_list_sources for the full catalog with per-source capabilities (residency, intents, filter support, free-tier flag).

Use rtv_search for: academic papers, code provenance, web context, news, encyclopedia lookup, place/POI search, multimedia discovery, structured facts, mixed evidence-gathering. Do NOT use for: real-time data (live prices, weather), private documents, authenticated services.

content_type — pick the kind of result:
  paper      peer-reviewed + preprints (arxiv, pubmed, s2, openalex, europmc, crossref, dblp, biorxiv, ads, core, openaire, zenodo, dimensions, lens, iascholar)
  model      ML models (huggingface)
  dataset    research datasets (huggingface, zenodo, datacite, openaire)
  video      video media (youtube, scrapingdog_youtube)
  place      POIs / geocoding (googleplaces, osmoverpass, here, photon, tomtom, nominatim)
  image      images (brave images, wikimedia, europeana)
  post       social posts + Q&A (mastodon, bluesky, reddit, stackexchange, hackernews)
  package    code packages (npm, pypi, crates, pkggodev)
  patent     patents (googlepatents, epoops)
  audio      podcasts / audio (listennotes, itunes)
  any        mixed / let the router pick

intent — declarative source selection (preferred over passing sources):
  deep_research    academic primary set + scholarly fallback (s2, openalex → arxiv, crossref, europmc, pubmed)
  primary_source   DOI-backed OA papers only (same chain, OA-biased)
  quick_lookup     one fast web source per type (kagi/mojeek/serpapi → brave/exa)
  code_provenance  packages + CS literature (npm/pypi/crates/pkggodev → github → arxiv/dblp/s2)
  news             news + open web (newsapi/serpapinews/gdelt → brave → wikipedia)
  reference        structured facts + encyclopedia (wolframalpha/kgapi/wikidata → wikipedia)
  (empty)          use server-configured DefaultSources

filters — narrow the result set. See FieldDescFilters for the per-provider matrix.

eu_mode — server-side only, not a tool argument. eu_strict admits only EU-resident sources plus the optional public-research-infrastructure tier.

format / compat — response shape lives in rtv_get's format; rtv_search returns Result objects with a kind discriminator + per-kind data blocks. Always check result.kind before reading kind-specific fields (result.paper.{doi, citation_count, ...}, result.web.{site_name, ...}, result.code.{repo, stars, ...}, etc.). compat:"v1" is sunset and returns RTV_COMPAT_V1_SUNSET.

Response fields: results, sources_queried, sources_failed (errored providers), sources_skipped (gated out by eu_mode with reason), fallback_walked (true if the chain walked past the primary set), eu_fallback_used (true when EUModePreferred fell back to a non-EU provider), audit_ref (correlate to retrievr's audit log).`

	ToolDescGet = `Fetch full details for a single result by its prefixed ID. ID format is "<source>:<native_id>" — examples: "arxiv:2401.12345", "doi:10.1038/s41586-023-06600-9", "github:owner/repo", "wikipedia:Attention_(machine_learning)", "openalex:W4366341216", "npm:react", "googlepatents:US20230123456A1", "youtube:dQw4w9WgXcQ", "osmoverpass:node/1234567890". Returns metadata, abstract/extract, and optionally BibTeX, full text, references, or citations depending on what the source supports.

format: "native" (default — source's native shape), "json", "xml" (only where supported), "markdown" (firecrawl + brave native), "bibtex" (assembled from metadata for scholarly sources with sufficient bibliographic fields).
include: list of extra blocks to fetch — "abstract" (default), "full_text", "references", "citations", "related", "metadata". Per-source support is in SourceInfo.supports_full_text / supports_citations.`

	ToolDescListSources = `List every registered source (61 plugins) with full capability surface: content_types served, native + available formats, residency posture (region + DPA status), supported result kinds (paper/web/code/place/...), query intents, rate limits, sort/filter capabilities, max_results_per_query, requires_credential, and free_tier flag. Use this to pick sources by intent + jurisdiction + filter support without trial-and-error.`
)

// ---------------------------------------------------------------------------
// Tool input field name constants
// ---------------------------------------------------------------------------

const (
	FieldQuery       = "query"
	FieldSources     = "sources"
	FieldContentType = "content_type"
	FieldSort        = "sort"
	FieldLimit       = "limit"
	FieldOffset      = "offset"
	FieldFilters     = "filters"
	FieldCredentials = "credentials"
	FieldID          = "id"
	FieldInclude     = "include"
	FieldFormat      = "format"
	FieldIntent      = "intent"
	FieldCompat      = "compat"
)

// Compat values for the rtv_search MCP tool's response shape.
const (
	CompatV1 = "v1" // legacy Publication shape (default; matches v1.5.0 wire)
	CompatV2 = "v2" // fat-struct Result shape with Kind discriminator
)

// ---------------------------------------------------------------------------
// Tool input field description constants
// ---------------------------------------------------------------------------

const (
	FieldDescQuery       = "Search query string"
	FieldDescSources     = "List of source IDs to search (e.g., [\"arxiv\", \"s2\"]). Defaults to server-configured sources."
	FieldDescContentType = "Type of content to search for: paper, model, dataset, video, place, image, post, package, patent, audio, or any. See ToolDescSearch for the per-type source list."
	FieldDescSort        = "Sort order for results: relevance, date_desc, date_asc, or citations"
	FieldDescLimit       = "Maximum number of results to return (1-100)"
	FieldDescOffset      = "Number of results to skip for pagination"
	FieldDescFilters     = "Optional filters to narrow search results. Providers that don't natively support a filter ignore it silently — query SourceCapabilities via rtv_list_sources for the runtime truth matrix. Keys:\n" +
		"  title (string) — title-only match: arxiv, pubmed, europmc.\n" +
		"  authors ([]string) — author-only filter: arxiv, pubmed, europmc. Other scholarly sources fold author terms into the main query.\n" +
		"  date_from / date_to (YYYY-MM-DD or YYYY) — honored by every source that exposes a date field. Brave maps to freshness buckets (pd/pw/pm/py); StackExchange/HackerNews convert to unix seconds; Dimensions/Lens floor to year; bioRxiv REQUIRES date_from.\n" +
		"  categories ([]string) — semantics vary by source: arxiv/ads/europmc/dimensions/lens = subject taxonomy; stackexchange = tags; npm = keywords; zenodo/datacite/openaire = resource_type; googleplaces/here/itunes/listennotes/osmoverpass = POI/podcast category (first entry only); serpapi/serpapinews = country (gl/cr code, first entry). Read SourceInfo.categories_hint for each source's accepted vocabulary.\n" +
		"  open_access (bool) — currently honored natively by zenodo only. Other providers ignore it; use intent=primary_source for OA-biased scholarly retrieval.\n" +
		"  min_citations (int) — currently NOT wired by any provider. Will be honored by s2/openalex/europmc in a future release.\n" +
		"  include_domains / exclude_domains ([]string, bare domains, no scheme) — honored by brave, exa, gdelt, kagi, mojeek, serpapi, newsapi.\n" +
		"  channels ([]string) — YouTube channel IDs/handles: youtube, scrapingdog_youtube.\n" +
		"  subreddits ([]string) — reddit only.\n" +
		"  language (BCP-47 tag, e.g. \"en\", \"de\", \"fr-CA\") — honored by brave, youtube, scrapingdog_youtube, bluesky, europeana, mastodon (post-fetch), serpapi/serpapinews/kagi/mojeek/newsapi/gdelt/eurlex/kgapi/wikidata/here/googleplaces/listennotes."
	FieldDescCredentials = "Optional per-call API credentials that override server defaults. Object with optional string fields: pubmed_api_key, s2_api_key, openalex_api_key, hf_token, ads_api_key. Each plugin reads only its own key; unknown keys are ignored. Per-credential rate-limit buckets keep tenants isolated."
	FieldDescID          = "Prefixed publication ID (e.g., \"arxiv:2401.12345\", \"s2:abc123\", \"doi:10.1038/...\", \"github:owner/repo\", \"npm:react\")."
	FieldDescInclude     = "Additional data to include: abstract (default), full_text, references, citations, related, metadata. Honored only by sources whose SourceInfo flags supports_full_text / supports_citations."
	FieldDescFormat      = "Desired content format: native (default), json, xml, markdown, bibtex. \"bibtex\" is assembled from metadata; works only on scholarly sources with sufficient bibliographic fields. \"markdown\" is native for firecrawl + brave; other sources may reject it."
)

// ---------------------------------------------------------------------------
// Filter sub-field name constants
// ---------------------------------------------------------------------------

const (
	FilterTitle        = "title"
	FilterAuthors      = "authors"
	FilterDateFrom     = "date_from"
	FilterDateTo       = "date_to"
	FilterCategories   = "categories"
	FilterOpenAccess   = "open_access"
	FilterMinCitations = "min_citations"

	// v2.7.0 smart-filter keys. Per-provider honoring is documented in
	// docs/filter-reference.md and surfaced at runtime via
	// SourceCapabilities.SupportsDomainFilter / SupportsChannelFilter /
	// SupportsLanguageFilter on each entry of rtv_list_sources.
	FilterIncludeDomains = "include_domains"
	FilterExcludeDomains = "exclude_domains"
	FilterChannels       = "channels"
	FilterSubreddits     = "subreddits"
	FilterLanguage       = "language"
)

// ---------------------------------------------------------------------------
// Credential sub-field name constants
// ---------------------------------------------------------------------------

const (
	CredFieldPubMedAPIKey   = "pubmed_api_key"
	CredFieldS2APIKey       = "s2_api_key"
	CredFieldOpenAlexAPIKey = "openalex_api_key"
	CredFieldHFToken        = "hf_token"
	CredFieldADSAPIKey      = "ads_api_key"
)

// ---------------------------------------------------------------------------
// Tool input default values
// ---------------------------------------------------------------------------

const (
	DefaultSearchLimit  = 10
	DefaultSearchOffset = 0
)

// ---------------------------------------------------------------------------
// Tool error format constants
// ---------------------------------------------------------------------------

const (
	errFmtFieldRequired = "%s: %s is required"
	logMsgFilterParse   = "failed to parse filters from tool input"
)

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

// SearchToolDefinition returns the MCP tool definition for rtv_search.
func SearchToolDefinition() mcp.Tool {
	return mcp.NewTool(ToolNameSearch,
		mcp.WithDescription(ToolDescSearch),
		mcp.WithString(FieldQuery,
			mcp.Required(),
			mcp.Description(FieldDescQuery),
		),
		mcp.WithArray(FieldSources,
			mcp.Description(FieldDescSources),
			mcp.WithStringItems(),
		),
		mcp.WithString(FieldContentType,
			mcp.Description(FieldDescContentType),
			mcp.DefaultString(string(ContentTypePaper)),
			mcp.Enum(
				string(ContentTypePaper),
				string(ContentTypeModel),
				string(ContentTypeDataset),
				string(ContentTypeAny),
				// v3 multimodal additions (v2.2.0 / cycle 1).
				string(ContentTypeVideo),
				string(ContentTypePlace),
				string(ContentTypeImage),
				string(ContentTypePost),
				// v5 + v6 additions (packages, patents, audio).
				string(ContentTypePackage),
				string(ContentTypePatent),
				string(ContentTypeAudio),
			),
		),
		mcp.WithString(FieldSort,
			mcp.Description(FieldDescSort),
			mcp.DefaultString(string(SortRelevance)),
			mcp.Enum(
				string(SortRelevance),
				string(SortDateDesc),
				string(SortDateAsc),
				string(SortCitations),
			),
		),
		mcp.WithNumber(FieldLimit,
			mcp.Description(FieldDescLimit),
			mcp.DefaultNumber(float64(DefaultSearchLimit)),
		),
		mcp.WithNumber(FieldOffset,
			mcp.Description(FieldDescOffset),
			mcp.DefaultNumber(float64(DefaultSearchOffset)),
		),
		mcp.WithObject(FieldFilters,
			mcp.Description(FieldDescFilters),
		),
		mcp.WithObject(FieldCredentials,
			mcp.Description(FieldDescCredentials),
		),
		mcp.WithString(FieldIntent,
			mcp.Description("Drives source selection + fallback chains: deep_research | quick_lookup | primary_source | code_provenance | news | reference. Empty = use defaults."),
			mcp.Enum(
				string(IntentDeepResearch),
				string(IntentQuickLookup),
				string(IntentPrimarySource),
				string(IntentCodeProvenance),
				string(IntentNews),
				string(IntentReference),
			),
		),
		mcp.WithString(FieldCompat,
			mcp.Description("Response shape. v2.0.0 default is \"v2\" (Result with kind discriminator + per-kind data blocks). \"v1\" (legacy Publication shape) was SUNSET in v2.0.0 — explicit compat:\"v1\" returns RTV_COMPAT_V1_SUNSET; omit the field for the new default."),
			mcp.DefaultString(CompatV2),
			mcp.Enum(CompatV2),
		),
	)
}

// GetToolDefinition returns the MCP tool definition for rtv_get.
func GetToolDefinition() mcp.Tool {
	return mcp.NewTool(ToolNameGet,
		mcp.WithDescription(ToolDescGet),
		mcp.WithString(FieldID,
			mcp.Required(),
			mcp.Description(FieldDescID),
		),
		mcp.WithArray(FieldInclude,
			mcp.Description(FieldDescInclude),
			mcp.WithStringItems(),
		),
		mcp.WithString(FieldFormat,
			mcp.Description(FieldDescFormat),
			mcp.DefaultString(string(FormatNative)),
			mcp.Enum(
				string(FormatNative),
				string(FormatJSON),
				string(FormatXML),
				string(FormatMarkdown),
				string(FormatBibTeX),
			),
		),
		mcp.WithObject(FieldCredentials,
			mcp.Description(FieldDescCredentials),
		),
	)
}

// ListSourcesToolDefinition returns the MCP tool definition for rtv_list_sources.
func ListSourcesToolDefinition() mcp.Tool {
	return mcp.NewTool(ToolNameListSources,
		mcp.WithDescription(ToolDescListSources),
	)
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

// NewSearchHandler returns a ToolHandlerFunc that delegates to Router.Search.
func NewSearchHandler(router *Router) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract required query.
		query, err := req.RequireString(FieldQuery)
		if err != nil {
			return mcp.NewToolResultError(
				NewMCPError(ErrMsgInvalidInput, "", fmt.Sprintf(errFmtFieldRequired, ErrMsgInvalidInput, FieldQuery)),
			), nil
		}

		// Extract optional parameters.
		sources := req.GetStringSlice(FieldSources, nil)
		contentType := ContentType(req.GetString(FieldContentType, string(ContentTypePaper)))
		sortOrder := SortOrder(req.GetString(FieldSort, string(SortRelevance)))
		limit := req.GetInt(FieldLimit, DefaultSearchLimit)
		offset := req.GetInt(FieldOffset, DefaultSearchOffset)
		intent := Intent(req.GetString(FieldIntent, ""))
		// v2.0.0 sunset: default response shape is v2 (fat-struct Result).
		// Explicit compat:"v1" returns ErrCompatV1Sunset. Explicit
		// compat:"v2" still works (idempotent with default).
		compat := req.GetString(FieldCompat, CompatV2)
		if compat == CompatV1 {
			return mcp.NewToolResultError(NewMCPErrorFromErr(ErrCompatV1Sunset, "")), nil
		}

		// Extract optional filters and credentials from raw arguments.
		args := req.GetArguments()
		filters := extractFilters(args)
		creds := extractCredentials(args)

		params := SearchParams{
			Query:       query,
			ContentType: contentType,
			Filters:     filters,
			Sort:        sortOrder,
			Limit:       limit,
			Offset:      offset,
			Intent:      intent,
		}

		result, err := router.SearchV2(ctx, params, sources, creds)
		if err != nil {
			return mcp.NewToolResultError(NewMCPErrorFromErr(err, "")), nil
		}
		return marshalToolResult(result)
	}
}

// NewGetHandler returns a ToolHandlerFunc that delegates to Router.Get.
func NewGetHandler(router *Router) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract required ID.
		id, err := req.RequireString(FieldID)
		if err != nil {
			return mcp.NewToolResultError(
				NewMCPError(ErrMsgInvalidInput, "", fmt.Sprintf(errFmtFieldRequired, ErrMsgInvalidInput, FieldID)),
			), nil
		}

		// Extract optional include fields.
		includeStrs := req.GetStringSlice(FieldInclude, []string{string(IncludeAbstract)})
		include := make([]IncludeField, len(includeStrs))
		for i, s := range includeStrs {
			include[i] = IncludeField(s)
		}

		// Extract optional format and credentials.
		format := ContentFormat(req.GetString(FieldFormat, string(FormatNative)))
		args := req.GetArguments()
		creds := extractCredentials(args)

		pub, err := router.Get(ctx, id, include, format, creds)
		if err != nil {
			return mcp.NewToolResultError(NewMCPErrorFromErr(err, "")), nil
		}

		return marshalToolResult(pub)
	}
}

// NewListSourcesHandler returns a ToolHandlerFunc that delegates to Router.ListSources.
func NewListSourcesHandler(router *Router) server.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		infos := router.ListSources(ctx)
		return marshalToolResult(infos)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractFilters safely extracts SearchFilters from the raw arguments map.
// Uses JSON round-trip for type-safe conversion. Returns zero-value filters
// if the field is missing or malformed.
func extractFilters(args map[string]any) SearchFilters {
	var filters SearchFilters
	raw, ok := args[FieldFilters]
	if !ok || raw == nil {
		return filters
	}

	// JSON round-trip: map[string]any → JSON bytes → SearchFilters.
	data, err := json.Marshal(raw)
	if err != nil {
		slog.Warn(logMsgFilterParse, slog.String(LogKeyError, err.Error()))
		return filters
	}
	if err := json.Unmarshal(data, &filters); err != nil {
		slog.Warn(logMsgFilterParse, slog.String(LogKeyError, err.Error()))
		return filters
	}
	return filters
}

// extractCredentials safely extracts CallCredentials from the raw arguments map.
// Uses JSON round-trip for type-safe conversion. Returns nil if the field is
// missing or malformed.
func extractCredentials(args map[string]any) *CallCredentials {
	raw, ok := args[FieldCredentials]
	if !ok || raw == nil {
		return nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}

	var creds CallCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil
	}

	// Return nil if all fields are empty (no credentials provided).
	if creds.PubMedAPIKey == "" && creds.S2APIKey == "" &&
		creds.OpenAlexAPIKey == "" && creds.HFToken == "" &&
		creds.ADSAPIKey == "" {
		return nil
	}

	return &creds
}

// marshalToolResult marshals any value to JSON and wraps it as an MCP text result.
// Returns an error result if marshaling fails.
func marshalToolResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError(
			NewMCPError(ErrMsgJSONMarshal, "", err.Error()),
		), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}
