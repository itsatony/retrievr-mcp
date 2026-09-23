package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pplxTestServerKey  = "pplx-server-key"
	pplxTestPerCallKey = "pplx-per-call-key"
)

func newPerplexityTestPlugin(t *testing.T, baseURL, apiKey string) *PerplexityPlugin {
	t.Helper()
	p := &PerplexityPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 1}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestPerplexity_IdentityAndCapabilities(t *testing.T) {
	t.Parallel()
	p := &PerplexityPlugin{}
	assert.Equal(t, "perplexity", p.ID())
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindWeb)
	assert.True(t, caps.SupportsCitations)
	assert.True(t, caps.SupportsFullText)
}

func TestPerplexity_Residency_USBlocked(t *testing.T) {
	t.Parallel()
	tag := (&PerplexityPlugin{}).Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.False(t, tag.Region.IsEU())
}

// agentResponse is the /v1/agent shape MEASURED on 2026-09-23 (retrievr-mcp#2): a `search_results`
// item beside the `message` item, each source carrying title, url, snippet and date.
func agentResponse(answer string, sources ...perplexityAgentResult) perplexityAgentResponse {
	out := []perplexityAgentOutput{}
	if len(sources) > 0 {
		out = append(out, perplexityAgentOutput{Type: perplexityOutputResults, Results: sources})
	}
	out = append(out, perplexityAgentOutput{Type: perplexityOutputMessage,
		Content: []perplexityAgentPart{{Type: perplexityOutputText, Text: answer}}})
	return perplexityAgentResponse{ID: "resp_123", Model: perplexityDefaultModel, Status: "completed", Output: out}
}

func TestPerplexity_Search_HappyPath_AnswerPlusCitations(t *testing.T) {
	t.Parallel()
	resp := agentResponse("Attention mechanisms compute weighted sums over input tokens.",
		perplexityAgentResult{Title: "A paper", URL: "https://example.com/paper", Snippet: "about attention", Date: "2026-09-10"},
		perplexityAgentResult{URL: "https://huggingface.co/blog/attention"},
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, perplexityAgentPath, r.URL.Path, "the retired /chat/completions must not be called")
		assert.Equal(t, perplexityAuthScheme+pplxTestServerKey, r.Header.Get(perplexityAuthHeader))

		// ⛔ The Agent API decodes STRICTLY: exactly these four keys, nothing else.
		var raw map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&raw))
		keys := make([]string, 0, len(raw))
		for k := range raw {
			keys = append(keys, k)
		}
		assert.ElementsMatch(t, []string{"model", "input", "tools", "tool_choice"}, keys)
		var body perplexityAgentRequest
		b, _ := json.Marshal(raw)
		require.NoError(t, json.Unmarshal(b, &body))
		assert.Equal(t, perplexityDefaultModel, body.Model)
		require.Len(t, body.Input, 1)
		assert.Equal(t, "user", body.Input[0].Role)
		assert.Equal(t, "explain attention", body.Input[0].Content)
		// ⛔ Without web_search the Agent API answers from memory with NO sources.
		require.Len(t, body.Tools, 1)
		assert.Equal(t, perplexityToolWebSearch, body.Tools[0].Type)
		// ⛔ And the search is REQUIRED, not offered: offered, the model skips it for some questions.
		assert.Equal(t, perplexityToolChoiceRequired, body.ToolChoice)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newPerplexityTestPlugin(t, srv.URL, pplxTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "explain attention"})
	require.NoError(t, err)
	require.Len(t, got.Results, 3, "1 synthesized + 2 sources")

	primary := got.Results[0]
	assert.Equal(t, "perplexity:resp_123", primary.ID)
	assert.Contains(t, primary.Title, "synthesized answer")
	assert.Equal(t, "Attention mechanisms compute weighted sums over input tokens.", primary.Abstract)
	assert.Equal(t, "https://example.com/paper", primary.URL, "primary URL = first source")
	assert.Equal(t, true, primary.SourceMetadata["grounded"])

	assert.Equal(t, "A paper", got.Results[1].Title, "the source's own title, which the old API never gave")
	assert.Equal(t, "2026-09-10", got.Results[1].Published)
	assert.Equal(t, "huggingface.co", got.Results[2].Title, "the host is the fallback title")
}

// ⚠ An answer with no sources was not grounded in a search, and says so.
func TestPerplexity_Search_AnUngroundedAnswerIsMarked(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agentResponse("Paris."))
	}))
	defer srv.Close()
	got, err := newPerplexityTestPlugin(t, srv.URL, pplxTestServerKey).Search(context.Background(), SearchParams{Query: "x"})
	require.NoError(t, err)
	require.Len(t, got.Results, 1)
	assert.Equal(t, false, got.Results[0].SourceMetadata["grounded"])
}

// ⛔ Every deployed config says `sonar`, and the Agent API refuses a bare name — so the migration
// is what keeps an un-updated config alive after 2026-09-27.
func TestPerplexity_AgentModel_MigratesALegacyName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "perplexity/sonar", agentModel("sonar"))
	assert.Equal(t, "perplexity/sonar-pro", agentModel(" sonar-pro "))
	assert.Equal(t, "perplexity/sonar", agentModel(""))
	assert.Equal(t, "openai/gpt-5", agentModel("openai/gpt-5"), "a vendor-prefixed name is used as written")
}

func TestPerplexity_Search_PerCallCredentialOverride(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, perplexityAuthScheme+pplxTestPerCallKey, r.Header.Get(perplexityAuthHeader))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agentResponse("stub"))
	}))
	defer srv.Close()
	p := newPerplexityTestPlugin(t, srv.URL, pplxTestServerKey)
	ctx := WithPerCallCredsMap(context.Background(), map[string]string{SourcePerplexity: pplxTestPerCallKey})
	_, err := p.Search(ctx, SearchParams{Query: "x"})
	require.NoError(t, err)
}

func TestPerplexity_Search_NoCredentialReturnsErrCredentialRequired(t *testing.T) {
	t.Parallel()
	p := newPerplexityTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestPerplexity_Search_AuthErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		code int
		want error
	}{
		{"401", http.StatusUnauthorized, ErrCredentialInvalid},
		{"403", http.StatusForbidden, ErrCredentialInvalid},
		{"429", http.StatusTooManyRequests, ErrRateLimitExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()
			p := newPerplexityTestPlugin(t, srv.URL, pplxTestServerKey)
			_, err := p.Search(context.Background(), SearchParams{Query: "x"})
			assert.True(t, errors.Is(err, tc.want))
		})
	}
}

func TestPerplexity_Get_ReturnsFormatUnsupported(t *testing.T) {
	t.Parallel()
	p := &PerplexityPlugin{}
	_, err := p.Get(context.Background(), "abc", nil, FormatNative)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestPerplexity_HostFromURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"https://example.com/page", "example.com"},
		{"https://en.wikipedia.org/wiki/Foo", "en.wikipedia.org"},
		{"not-a-url", ""},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, hostFromURL(c.in))
	}
}

func TestPerplexity_LiveSmoke(t *testing.T) {
	apiKey := os.Getenv("PERPLEXITY_API_KEY")
	if apiKey == "" {
		t.Skip("PERPLEXITY_API_KEY not set; skipping live smoke")
	}
	p := newPerplexityTestPlugin(t, "", apiKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "what is the difference between encoder-only and decoder-only transformers"})
	require.NoError(t, err)
	require.NotEmpty(t, got.Results)
	primary := got.Results[0]
	t.Logf("synthesized: %s", primary.Abstract[:min(len(primary.Abstract), 200)])
	assert.True(t, strings.HasPrefix(primary.ID, "perplexity:"))
	assert.NotEmpty(t, primary.Abstract)
	// ⛔ THE POINT OF A SEARCH SOURCE (retrievr-mcp#2): the answer must be GROUNDED — the live
	// Agent API returns sources only when web_search is requested, so this is what proves the
	// request shape, not merely that something answered.
	assert.Equal(t, true, primary.SourceMetadata["grounded"], "the live answer came back with no sources")
	require.Greater(t, len(got.Results), 1, "at least one source beside the synthesized answer")
	assert.NotEmpty(t, got.Results[1].URL)
	assert.NotEmpty(t, got.Results[1].Title)
}
