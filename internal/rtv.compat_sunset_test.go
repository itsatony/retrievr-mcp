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
// Cycle 3 task #22 — compat:"v1" sunset coverage.
//
// v2.0.0 flips the rtv_search default to v2 (fat-struct Result) and rejects
// explicit compat:"v1" with a typed RTV_COMPAT_V1_SUNSET error. These tests
// pin both behaviors.
// ---------------------------------------------------------------------------

func TestSunset_DefaultIsV2(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, []Publication{
			testPub(SourceArXiv, "arxiv:1", testDOI1, intPtr(10)),
		}),
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{SourceArXiv}
	router := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())

	handler := NewSearchHandler(router)

	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameSearch
	req.Params.Arguments = map[string]any{
		FieldQuery: "x",
		FieldLimit: 5,
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError, "default rtv_search must succeed")

	// Decode and verify the response is v2 shape (has Kind on results).
	text := extractTextContent(t, result)
	var v2 MergedSearchResultV2
	require.NoError(t, json.Unmarshal([]byte(text), &v2))
	require.NotEmpty(t, v2.Results, "default search must return results")
	assert.NotEmpty(t, v2.Results[0].Kind, "default response must be v2 shape — Kind discriminator present")
	assert.Equal(t, KindPaper, v2.Results[0].Kind)
}

func TestSunset_ExplicitV1ReturnsSunsetError(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, nil),
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{SourceArXiv}
	router := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())

	handler := NewSearchHandler(router)
	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameSearch
	req.Params.Arguments = map[string]any{
		FieldQuery:  "x",
		FieldCompat: CompatV1,
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err) // handler returns the error as a tool-result error, not a Go error
	require.NotNil(t, result)
	assert.True(t, result.IsError, "compat:v1 must surface as a tool-result error")

	text := extractTextContent(t, result)
	assert.Contains(t, text, "sunset")
}

func TestSunset_ExplicitV2Works(t *testing.T) {
	t.Parallel()

	plugins := map[string]SourcePlugin{
		SourceArXiv: newMockPlugin(SourceArXiv, []Publication{
			testPub(SourceArXiv, "arxiv:1", testDOI1, nil),
		}),
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = []string{SourceArXiv}
	router := NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())

	handler := NewSearchHandler(router)
	req := mcp.CallToolRequest{}
	req.Params.Name = ToolNameSearch
	req.Params.Arguments = map[string]any{
		FieldQuery:  "x",
		FieldCompat: CompatV2,
	}

	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, result.IsError, "explicit compat:v2 must succeed (idempotent with default)")

	text := extractTextContent(t, result)
	var v2 MergedSearchResultV2
	require.NoError(t, json.Unmarshal([]byte(text), &v2))
	require.NotEmpty(t, v2.Results)
}
