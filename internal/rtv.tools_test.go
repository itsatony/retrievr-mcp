package internal

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Tool definition tests
// ---------------------------------------------------------------------------

func TestSearchToolDefinition(t *testing.T) {
	t.Parallel()
	tool := SearchToolDefinition()
	assert.Equal(t, ToolNameSearch, tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Contains(t, tool.InputSchema.Required, FieldQuery, "query should be required")
	assert.Contains(t, tool.InputSchema.Properties, FieldQuery)
	assert.Contains(t, tool.InputSchema.Properties, FieldSources)
	assert.Contains(t, tool.InputSchema.Properties, FieldContentType)
	assert.Contains(t, tool.InputSchema.Properties, FieldSort)
	assert.Contains(t, tool.InputSchema.Properties, FieldLimit)
	assert.Contains(t, tool.InputSchema.Properties, FieldOffset)
	assert.Contains(t, tool.InputSchema.Properties, FieldFilters)
	assert.Contains(t, tool.InputSchema.Properties, FieldCredentials)
}

func TestGetToolDefinition(t *testing.T) {
	t.Parallel()
	tool := GetToolDefinition()
	assert.Equal(t, ToolNameGet, tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Contains(t, tool.InputSchema.Required, FieldID, "id should be required")
	assert.Contains(t, tool.InputSchema.Properties, FieldID)
	assert.Contains(t, tool.InputSchema.Properties, FieldInclude)
	assert.Contains(t, tool.InputSchema.Properties, FieldFormat)
	assert.Contains(t, tool.InputSchema.Properties, FieldCredentials)
}

func TestListSourcesToolDefinition(t *testing.T) {
	t.Parallel()
	tool := ListSourcesToolDefinition()
	assert.Equal(t, ToolNameListSources, tool.Name)
	assert.NotEmpty(t, tool.Description)
	assert.Empty(t, tool.InputSchema.Required, "list_sources should have no required fields")
}

// ---------------------------------------------------------------------------
// Search handler tests
// ---------------------------------------------------------------------------

func TestSearchHandler_Success(t *testing.T) {
	t.Parallel()

	citations := 42
	pubs := []Publication{
		{
			ID:            "arxiv:2401.99999",
			Source:        SourceArXiv,
			ContentType:   ContentTypePaper,
			Title:         "Test Paper",
			Authors:       []Author{{Name: "Alice"}},
			Published:     "2024-01-15",
			URL:           "https://arxiv.org/abs/2401.99999",
			CitationCount: &citations,
		},
	}

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, pubs),
	}
	router := testRouter(plugins)
	handler := NewSearchHandler(router)

	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameSearch
	req.Params.Arguments = map[string]any{
		FieldQuery: "test query",
		FieldLimit: float64(DefaultSearchLimit),
		FieldSort:  string(SortRelevance),
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	// Parse the JSON response.
	text := extractTextContent(t, result)
	var merged MergedSearchResult
	err = json.Unmarshal([]byte(text), &merged)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, merged.TotalResults, 1)
	assert.Equal(t, "Test Paper", merged.Results[0].Title)
}

func TestSearchHandler_MissingQuery(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, nil),
	}
	router := testRouter(plugins)
	handler := NewSearchHandler(router)

	// No query field.
	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameSearch
	req.Params.Arguments = map[string]any{
		FieldLimit: float64(DefaultSearchLimit),
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err, "Go error should be nil; error is in the result")
	require.NotNil(t, result)
	assert.True(t, result.IsError, "missing query should produce an error result")

	text := extractTextContent(t, result)
	assert.Contains(t, text, ErrMsgInvalidInput)
}

func TestSearchHandler_WithFilters(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, []Publication{
			{
				ID: "arxiv:2401.11111", Source: SourceArXiv,
				ContentType: ContentTypePaper, Title: "Filtered Paper",
				Published: "2024-01-15", URL: "https://arxiv.org/abs/2401.11111",
			},
		}),
	}
	router := testRouter(plugins)
	handler := NewSearchHandler(router)

	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameSearch
	req.Params.Arguments = map[string]any{
		FieldQuery: "attention",
		FieldFilters: map[string]any{
			FilterTitle:      "attention",
			FilterDateFrom:   "2024-01-01",
			FilterDateTo:     "2024-12-31",
			FilterCategories: []any{"cs.AI", "cs.CL"},
		},
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)
}

func TestSearchHandler_WithCredentials(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceS2: newMockPlugin(SourceS2, []Publication{
			{
				ID: "s2:abc123", Source: SourceS2,
				ContentType: ContentTypePaper, Title: "S2 Paper",
				Published: "2024-06-01", URL: "https://semanticscholar.org/paper/abc123",
			},
		}),
	}

	cfg := testRouterConfig()
	cfg.DefaultSources = []string{SourceS2}
	router := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())
	handler := NewSearchHandler(router)

	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameSearch
	req.Params.Arguments = map[string]any{
		FieldQuery: "test",
		FieldCredentials: map[string]any{
			CredFieldS2APIKey: "per-call-s2-key",
		},
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)
}

func TestSearchHandler_RouterError(t *testing.T) {
	t.Parallel()

	// Router with no plugins returns ErrSearchFailed (no valid sources).
	plugins := map[string]SourcePlugin{}
	router := testRouter(plugins)
	handler := NewSearchHandler(router)

	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameSearch
	req.Params.Arguments = map[string]any{
		FieldQuery: "test",
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err, "Go error should be nil")
	require.NotNil(t, result)
	assert.True(t, result.IsError, "router failure should produce error result")
}

// ---------------------------------------------------------------------------
// Get handler tests
// ---------------------------------------------------------------------------

func TestGetHandler_Success(t *testing.T) {
	t.Parallel()

	pubs := []Publication{
		{
			ID: "arxiv:2401.99999", Source: SourceArXiv,
			ContentType: ContentTypePaper, Title: "Retrieved Paper",
			Published: "2024-01-15", URL: "https://arxiv.org/abs/2401.99999",
		},
	}

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, pubs),
	}
	router := testRouter(plugins)
	handler := NewGetHandler(router)

	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameGet
	req.Params.Arguments = map[string]any{
		FieldID: "arxiv:2401.99999",
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractTextContent(t, result)
	var pub Publication
	err = json.Unmarshal([]byte(text), &pub)
	require.NoError(t, err)
	assert.Equal(t, "Retrieved Paper", pub.Title)
}

func TestGetHandler_InvalidID(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, nil),
	}
	router := testRouter(plugins)
	handler := NewGetHandler(router)

	// Missing ID.
	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameGet
	req.Params.Arguments = map[string]any{}

	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError, "missing ID should produce error result")
}

func TestGetHandler_MalformedID(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, nil),
	}
	router := testRouter(plugins)
	handler := NewGetHandler(router)

	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameGet
	req.Params.Arguments = map[string]any{
		FieldID: "no-separator",
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError, "malformed ID should produce error result")
}

func TestGetHandler_NotFound(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, nil),
	}
	router := testRouter(plugins)
	handler := NewGetHandler(router)

	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameGet
	req.Params.Arguments = map[string]any{
		FieldID: "arxiv:nonexistent",
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
}

// ---------------------------------------------------------------------------
// ListSources handler tests
// ---------------------------------------------------------------------------

func TestListSourcesHandler_Success(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, nil),
		SourceS2:    newMockPlugin(SourceS2, nil),
	}
	router := testRouter(plugins)
	handler := NewListSourcesHandler(router)

	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameListSources

	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractTextContent(t, result)
	var infos []SourceInfo
	err = json.Unmarshal([]byte(text), &infos)
	require.NoError(t, err)
	assert.Len(t, infos, 2)
	// Should be sorted by ID.
	assert.Equal(t, SourceArXiv, infos[0].ID)
	assert.Equal(t, SourceS2, infos[1].ID)
}

// ---------------------------------------------------------------------------
// extractFilters tests
// ---------------------------------------------------------------------------

func TestExtractFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     map[string]any
		validate func(t *testing.T, f SearchFilters)
	}{
		{
			name: "nil args",
			args: nil,
			validate: func(t *testing.T, f SearchFilters) {
				assert.Empty(t, f.Title)
			},
		},
		{
			name: "no filters key",
			args: map[string]any{FieldQuery: "test"},
			validate: func(t *testing.T, f SearchFilters) {
				assert.Empty(t, f.Title)
			},
		},
		{
			name: "full filters",
			args: map[string]any{
				FieldFilters: map[string]any{
					FilterTitle:      "attention",
					FilterAuthors:    []any{"Smith"},
					FilterDateFrom:   "2024-01-01",
					FilterDateTo:     "2024-12-31",
					FilterCategories: []any{"cs.AI"},
				},
			},
			validate: func(t *testing.T, f SearchFilters) {
				assert.Equal(t, "attention", f.Title)
				assert.Equal(t, []string{"Smith"}, f.Authors)
				assert.Equal(t, "2024-01-01", f.DateFrom)
				assert.Equal(t, "2024-12-31", f.DateTo)
				assert.Equal(t, []string{"cs.AI"}, f.Categories)
			},
		},
		{
			name: "partial filters",
			args: map[string]any{
				FieldFilters: map[string]any{
					FilterDateFrom: "2023-06-01",
				},
			},
			validate: func(t *testing.T, f SearchFilters) {
				assert.Equal(t, "2023-06-01", f.DateFrom)
				assert.Empty(t, f.Title)
				assert.Empty(t, f.DateTo)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			filters := extractFilters(tt.args)
			tt.validate(t, filters)
		})
	}
}

// ---------------------------------------------------------------------------
// extractCredentials tests
// ---------------------------------------------------------------------------

func TestExtractCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     map[string]any
		expected *CallCredentials
	}{
		{
			name:     "nil args",
			args:     nil,
			expected: nil,
		},
		{
			name:     "no credentials key",
			args:     map[string]any{FieldQuery: "test"},
			expected: nil,
		},
		{
			name: "empty credentials",
			args: map[string]any{
				FieldCredentials: map[string]any{},
			},
			expected: nil,
		},
		{
			name: "s2 api key",
			args: map[string]any{
				FieldCredentials: map[string]any{
					CredFieldS2APIKey: "my-s2-key",
				},
			},
			expected: &CallCredentials{S2APIKey: "my-s2-key"},
		},
		{
			name: "all credentials",
			args: map[string]any{
				FieldCredentials: map[string]any{
					CredFieldPubMedAPIKey:   "pm-key",
					CredFieldS2APIKey:       "s2-key",
					CredFieldOpenAlexAPIKey: "oa-key",
					CredFieldHFToken:        "hf-tok",
				},
			},
			expected: &CallCredentials{
				PubMedAPIKey:   "pm-key",
				S2APIKey:       "s2-key",
				OpenAlexAPIKey: "oa-key",
				HFToken:        "hf-tok",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			creds := extractCredentials(tt.args)
			if tt.expected == nil {
				assert.Nil(t, creds)
			} else {
				require.NotNil(t, creds)
				assert.Equal(t, tt.expected.PubMedAPIKey, creds.PubMedAPIKey)
				assert.Equal(t, tt.expected.S2APIKey, creds.S2APIKey)
				assert.Equal(t, tt.expected.OpenAlexAPIKey, creds.OpenAlexAPIKey)
				assert.Equal(t, tt.expected.HFToken, creds.HFToken)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// marshalToolResult tests
// ---------------------------------------------------------------------------

func TestMarshalToolResult_Success(t *testing.T) {
	t.Parallel()

	data := map[string]string{"key": "value"}
	result, err := marshalToolResult(data)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)

	text := extractTextContent(t, result)
	assert.Contains(t, text, `"key"`)
	assert.Contains(t, text, `"value"`)
}

func TestMarshalToolResult_MarshalError(t *testing.T) {
	t.Parallel()

	// Create an unmarshalable value (channel).
	result, err := marshalToolResult(make(chan int))
	require.NoError(t, err, "Go error should be nil; error is in the result")
	require.NotNil(t, result)
	assert.True(t, result.IsError)

	text := extractTextContent(t, result)
	assert.Contains(t, text, ErrMsgJSONMarshal)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// extractTextContent extracts the text from the first TextContent in a CallToolResult.
func extractTextContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, result.Content, "result should have at least one content item")
	tc, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "first content item should be TextContent, got %T", result.Content[0])
	return tc.Text
}
