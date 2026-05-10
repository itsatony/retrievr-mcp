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

func TestPerplexity_Search_HappyPath_AnswerPlusCitations(t *testing.T) {
	t.Parallel()
	resp := perplexityChatResponse{
		ID:    "sonar-req-123",
		Model: "sonar",
		Citations: []string{
			"https://example.com/paper",
			"https://huggingface.co/blog/attention",
		},
		Choices: []perplexityChoice{{
			Index: 0,
			Message: perplexityMessage{
				Role:    "assistant",
				Content: "Attention mechanisms compute weighted sums over input tokens to capture relationships at arbitrary distances.",
			},
			FinishReason: "stop",
		}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, perplexityCompletionsPath, r.URL.Path)
		assert.Equal(t, perplexityAuthScheme+pplxTestServerKey, r.Header.Get(perplexityAuthHeader))

		var body perplexityChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, perplexityDefaultModel, body.Model)
		assert.True(t, body.ReturnCitations)
		require.Len(t, body.Messages, 1)
		assert.Equal(t, "user", body.Messages[0].Role)
		assert.Equal(t, "explain attention", body.Messages[0].Content)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := newPerplexityTestPlugin(t, srv.URL, pplxTestServerKey)
	got, err := p.Search(context.Background(), SearchParams{Query: "explain attention"})
	require.NoError(t, err)
	require.Len(t, got.Results, 3, "1 synthesized + 2 citations")

	// Primary result: synthesized answer.
	primary := got.Results[0]
	assert.Equal(t, "perplexity:sonar-req-123", primary.ID)
	assert.Contains(t, primary.Title, "synthesized answer")
	assert.Equal(t, resp.Choices[0].Message.Content, primary.Abstract)
	assert.Equal(t, resp.Citations[0], primary.URL, "primary URL = first citation")
	assert.Contains(t, primary.SourceMetadata, "llm_context")
	assert.Equal(t, resp.Choices[0].Message.Content, primary.SourceMetadata["llm_context"])

	// Citation results.
	for i, c := range got.Results[1:] {
		assert.Equal(t, resp.Citations[i], c.URL)
		assert.NotEmpty(t, c.Title)
	}
}

func TestPerplexity_Search_PerCallCredentialOverride(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, perplexityAuthScheme+pplxTestPerCallKey, r.Header.Get(perplexityAuthHeader))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(perplexityChatResponse{
			ID:      "x",
			Model:   "sonar",
			Choices: []perplexityChoice{{Message: perplexityMessage{Content: "stub"}}},
		})
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
}

