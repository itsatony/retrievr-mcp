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
	ToolDescSearch = `Search across published information: papers, AI models, datasets, web pages, news, code repos, encyclopedia articles, multimedia, social posts, and Q&A threads. 30 sources behind one tool.

Use rtv_search when you need: academic papers, code provenance, web context, encyclopedia lookup, mixed evidence-gathering. Do NOT use for: real-time data (prices, weather), private documents, authenticated services.

content_type: "paper" (peer-reviewed/preprints), "model" (HuggingFace), "dataset", or "any" (mixed). Wave-1 web/code/encyclopedia results surface via "any".

intent (recommended over manual sources): "deep_research" fans across academic + web; "quick_lookup" hits one fast source per type; "primary_source" returns DOI-backed OA papers only; "code_provenance" targets GitHub + CS literature; "news"/"reference" target their respective providers.

eu_mode: not a tool param — set server-side. eu_strict admits only EU-resident sources (Linkup, DBLP, Europe PMC) plus optional public-research-infrastructure tier.

compat: "v1" (default) returns Publication-shaped results (title/authors/doi/citation_count flat). "v2" returns Result with kind discriminator + per-kind blocks (kind="paper" populates result.paper.{doi, citation_count, ...}; kind="web" populates result.web.{site_name, ...}; kind="code" populates result.code.{repo, stars, ...}). Always check kind before reading kind-specific fields.

sources_failed lists providers that errored. sources_skipped lists providers gated out by eu_mode with a reason. audit_ref correlates the response to retrievr's audit log.`

	ToolDescGet = `Fetch full details for a single result by its prefixed ID (e.g. "arxiv:2401.12345", "github:owner/repo", "wikipedia:Attention_(machine_learning)", "openalex:W4366341216"). Returns metadata, abstract/extract, and optionally BibTeX, full text, references, or citations depending on what the source supports.`

	ToolDescListSources = `List every registered source with capabilities, residency posture (region + DPA status), supported result kinds (paper/web/code/...), query intents, rate limits, and free-tier flag. Use this to pick sources by intent + jurisdiction without trial-and-error.`
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
	FieldDescContentType = "Type of content to search for: paper, model, dataset, video, place, image, post, or any"
	FieldDescSort        = "Sort order for results: relevance, date_desc, date_asc, or citations"
	FieldDescLimit       = "Maximum number of results to return (1-100)"
	FieldDescOffset      = "Number of results to skip for pagination"
	FieldDescFilters     = "Optional filters to narrow search results. Keys: title (string), authors ([]string), " +
		"date_from / date_to (YYYY-MM-DD or YYYY), categories ([]string), open_access (bool), " +
		"min_citations (int), include_domains / exclude_domains ([]string, honoured by brave + exa), " +
		"channels ([]string, honoured by youtube + scrapingdog_youtube), subreddits ([]string, honoured " +
		"by reddit), language (BCP-47 tag, honoured by brave + youtube + scrapingdog_youtube + bluesky + " +
		"europeana; mastodon applies as post-fetch filter). Providers that do not natively support a " +
		"filter silently ignore it — query SourceCapabilities via rtv_list_sources for the truth matrix."
	FieldDescCredentials = "Optional per-call API credentials that override server defaults"
	FieldDescID          = "Prefixed publication ID (e.g., \"arxiv:2401.12345\", \"s2:abc123\")"
	FieldDescInclude     = "Additional data to include: abstract, full_text, references, citations, related, metadata"
	FieldDescFormat      = "Desired content format: native, json, xml, markdown, or bibtex"
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
