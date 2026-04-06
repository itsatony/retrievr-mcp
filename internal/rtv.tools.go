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
	ToolDescSearch = "Search academic papers, AI models, and datasets across 10 sources: " +
		"ArXiv, PubMed, Semantic Scholar, OpenAlex, HuggingFace, Europe PMC, " +
		"CrossRef, DBLP, NASA ADS, and bioRxiv. " +
		"Returns merged, deduplicated results with title, authors, abstract, URL, " +
		"DOI, and citation count. Supports date filters and sorting by relevance, " +
		"date, or citations."

	ToolDescGet = "Get full details for a publication by its prefixed ID " +
		"(e.g., \"arxiv:2401.12345\", \"crossref:10.1038/s41586-024-07487-w\", " +
		"\"ads:2024ApJ...123..456A\"). Returns metadata, abstract, and optionally " +
		"BibTeX, full text, references, or citations."

	ToolDescListSources = "List available academic sources with their capabilities, " +
		"content types, rate limits, and supported output formats."
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
)

// ---------------------------------------------------------------------------
// Tool input field description constants
// ---------------------------------------------------------------------------

const (
	FieldDescQuery       = "Search query string"
	FieldDescSources     = "List of source IDs to search (e.g., [\"arxiv\", \"s2\"]). Defaults to server-configured sources."
	FieldDescContentType = "Type of content to search for: paper, model, dataset, or any"
	FieldDescSort        = "Sort order for results: relevance, date_desc, date_asc, or citations"
	FieldDescLimit       = "Maximum number of results to return (1-100)"
	FieldDescOffset      = "Number of results to skip for pagination"
	FieldDescFilters     = "Optional filters to narrow search results"
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
		}

		result, err := router.Search(ctx, params, sources, creds)
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
		creds.OpenAlexAPIKey == "" && creds.HFToken == "" {
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
