package internal

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Shared egress / sanitization helper tests.
// ---------------------------------------------------------------------------

func TestRedactURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no_secret", "https://example.com/x?q=hello", "https://example.com/x?q=hello"},
		{"api_key", "https://example.com/x?q=hello&api_key=SECRET", ""},
		{"appid", "https://api.wolframalpha.com/v2/query?input=2pi&appid=SECRET", ""},
		{"wskey", "https://api.europeana.eu/api/v2/search.json?query=x&wskey=SECRET", ""},
		{"key_case_insensitive", "https://kgsearch.googleapis.com/v1/entities:search?query=x&KEY=SECRET", ""},
		{"token", "https://example.com/x?token=SECRET&q=hello", ""},
		{"invalid_url_passthrough", "not a url ::///", "not a url ::///"},
	}
	for _, tc := range cases {
		got := redactURL(tc.in)
		if tc.want == "" {
			assert.NotContains(t, got, "SECRET", tc.name)
			// URL-encoded form: brackets escape to %5B / %5D.
			gotDecoded, _ := url.QueryUnescape(got)
			assert.Contains(t, gotDecoded, "[REDACTED]", tc.name)
		} else {
			assert.Equal(t, tc.want, got, tc.name)
		}
	}
}

func TestRedactURLErr(t *testing.T) {
	t.Parallel()
	// Build a url.Error like net/http would. Call redactURLErr BEFORE
	// wrapping (the helper mutates the live *url.Error pointer; once
	// wrapped, fmt.Errorf has snapshotted the formatted message).
	ue := &url.Error{
		Op:  "Get",
		URL: "https://example.com/x?api_key=SECRET",
		Err: errors.New("dial tcp: timeout"),
	}
	wrapped := fmt.Errorf("plugin: http: %w", redactURLErr(ue))
	assert.NotContains(t, wrapped.Error(), "SECRET")
	// Underlying error chain preserved (sentinels still reach errors.Is).
	assert.True(t, errors.As(wrapped, new(*url.Error)))

	// Non url.Error round-trips unchanged.
	plain := errors.New("nope")
	assert.Same(t, plain, redactURLErr(plain))
	assert.Nil(t, redactURLErr(nil))
}

func TestLimitedDecode_RespectsCap(t *testing.T) {
	t.Parallel()
	// 64-byte cap, 200-byte input → must fail because the body is truncated
	// mid-JSON before the closing brace.
	body := strings.NewReader(`{"x":"` + strings.Repeat("a", 200) + `"}`)
	var out map[string]string
	err := limitedDecode(body, &out, 64)
	assert.Error(t, err)
}

func TestLimitedDecode_DefaultCapWhenZero(t *testing.T) {
	t.Parallel()
	body := strings.NewReader(`{"x":"hello"}`)
	var out map[string]string
	err := limitedDecode(body, &out, 0)
	assert.NoError(t, err)
	assert.Equal(t, "hello", out["x"])
}

func TestSanitizeHealthError(t *testing.T) {
	t.Parallel()
	// "body=..." fragment stripped.
	got := sanitizeHealthError(errors.New(`plugin: status=401 body={"error":"invalid_client","key_prefix":"sk_live_a1b2"}`))
	assert.NotContains(t, got, "sk_live_a1b2")
	assert.NotContains(t, got, "body=")
	assert.Contains(t, got, "status=401")

	// URL query string stripped.
	got = sanitizeHealthError(errors.New(`Get "https://api.example.com/v1/x?api_key=SECRET&q=hello": dial tcp: timeout`))
	assert.NotContains(t, got, "SECRET")
	assert.Contains(t, got, "[REDACTED]")

	// Cap respected.
	long := strings.Repeat("x", 1024)
	got = sanitizeHealthError(errors.New(long))
	assert.LessOrEqual(t, len(got), healthLastErrorMax+5) // +ellipsis byte budget

	// Nil round-trips empty.
	assert.Equal(t, "", sanitizeHealthError(nil))
}

func TestStripURLQueryStrings(t *testing.T) {
	t.Parallel()
	in := `Get "https://api.example.com/v1/search?api_key=SECRET&q=hello": dial tcp: i/o timeout`
	out := stripURLQueryStrings(in)
	assert.NotContains(t, out, "SECRET")
	assert.Contains(t, out, "[REDACTED]")
	// No query string → unchanged.
	plain := "plain old error message, no urls"
	assert.Equal(t, plain, stripURLQueryStrings(plain))
}

func TestEgressCheckRedirect_StripsBearerHeaders(t *testing.T) {
	t.Parallel()
	req, err := http.NewRequest("GET", "https://upstream/v1/redirected", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer SECRET")
	req.Header.Set("Cookie", "session=SECRET")
	req.Header.Set("X-Goog-Api-Key", "SECRET")
	req.Header.Set("X-ListenAPI-Key", "SECRET")

	// 0 prior hops — allowed (cap is 3).
	require.NoError(t, egressCheckRedirect(req, nil))
	assert.Equal(t, "", req.Header.Get("Authorization"))
	assert.Equal(t, "", req.Header.Get("Cookie"))
	assert.Equal(t, "", req.Header.Get("X-Goog-Api-Key"))
	assert.Equal(t, "", req.Header.Get("X-ListenAPI-Key"))
}

func TestEgressCheckRedirect_CapsDepth(t *testing.T) {
	t.Parallel()
	req, err := http.NewRequest("GET", "https://upstream/v1/redirected", nil)
	require.NoError(t, err)
	prior := make([]*http.Request, egressMaxRedirects)
	err = egressCheckRedirect(req, prior)
	assert.ErrorIs(t, err, http.ErrUseLastResponse)
}
