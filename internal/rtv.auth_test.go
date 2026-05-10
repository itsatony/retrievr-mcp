package internal

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// AuthConfig.ResolvedAuthMode
// ---------------------------------------------------------------------------

func TestAuthConfig_ResolvedAuthMode_Default(t *testing.T) {
	t.Parallel()
	a := AuthConfig{}
	assert.Equal(t, AuthModeHybrid, a.ResolvedAuthMode())
}

func TestAuthConfig_ResolvedAuthMode_Explicit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode string
		want string
	}{
		{AuthModePerRequest, AuthModePerRequest},
		{AuthModeServerSide, AuthModeServerSide},
		{AuthModeHybrid, AuthModeHybrid},
	}
	for _, tc := range cases {
		a := AuthConfig{Mode: tc.mode}
		assert.Equal(t, tc.want, a.ResolvedAuthMode())
	}
}

// ---------------------------------------------------------------------------
// ClearServerCredentials
// ---------------------------------------------------------------------------

func TestClearServerCredentials_RemovesAllAPIKeys(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Sources: map[string]PluginConfig{
			"exa":   {APIKey: "exa-key-from-yaml", Enabled: true},
			"brave": {APIKey: "brave-key-from-yaml", Enabled: true},
			"s2":    {APIKey: "", Enabled: true}, // already empty
		},
	}
	cleared := ClearServerCredentials(cfg)
	assert.ElementsMatch(t, []string{"exa", "brave"}, cleared)
	assert.Empty(t, cfg.Sources["exa"].APIKey)
	assert.Empty(t, cfg.Sources["brave"].APIKey)
	assert.Empty(t, cfg.Sources["s2"].APIKey)
}

func TestClearServerCredentials_Idempotent(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Sources: map[string]PluginConfig{
			"exa": {APIKey: "key", Enabled: true},
		},
	}
	first := ClearServerCredentials(cfg)
	second := ClearServerCredentials(cfg)
	assert.Equal(t, []string{"exa"}, first)
	assert.Empty(t, second, "second call clears nothing")
}

// ---------------------------------------------------------------------------
// PerRequestCredsContextFunc — header → ctx extraction
// ---------------------------------------------------------------------------

func TestPerRequestCredsContextFunc_ExtractsHeaders(t *testing.T) {
	t.Parallel()
	r, err := http.NewRequest(http.MethodPost, "/mcp", nil)
	require.NoError(t, err)
	r.Header.Set("X-Retrievr-Cred-Exa", "tenant-a-exa-key")
	r.Header.Set("X-Retrievr-Cred-Brave", "tenant-a-brave-key")
	r.Header.Set("Authorization", "Bearer ignored")

	fn := PerRequestCredsContextFunc()
	ctx := fn(t.Context(), r)

	creds := PerCallCredsMapFromContext(ctx)
	require.NotNil(t, creds)
	assert.Equal(t, "tenant-a-exa-key", creds["exa"])
	assert.Equal(t, "tenant-a-brave-key", creds["brave"])
	_, hasAuth := creds["authorization"]
	assert.False(t, hasAuth, "non-cred headers must not leak into the credential map")
}

func TestPerRequestCredsContextFunc_NoHeaders_LeavesCtxUnchanged(t *testing.T) {
	t.Parallel()
	r, err := http.NewRequest(http.MethodGet, "/mcp", nil)
	require.NoError(t, err)
	fn := PerRequestCredsContextFunc()
	ctx := fn(t.Context(), r)
	assert.Nil(t, PerCallCredsMapFromContext(ctx))
}

func TestPerRequestCredsContextFunc_LowercasesSourceID(t *testing.T) {
	t.Parallel()
	r, err := http.NewRequest(http.MethodPost, "/mcp", nil)
	require.NoError(t, err)
	// Go's http.Header canonicalizes to "X-Retrievr-Cred-Exa"; we expect
	// the source ID part ("Exa") to be lowercased to match plugin IDs.
	r.Header.Set("x-retrievr-cred-EXA", "key1")
	fn := PerRequestCredsContextFunc()
	ctx := fn(t.Context(), r)
	creds := PerCallCredsMapFromContext(ctx)
	require.NotNil(t, creds)
	assert.Equal(t, "key1", creds["exa"])
}

func TestPerRequestCredsContextFunc_EmptyValueDropped(t *testing.T) {
	t.Parallel()
	r, err := http.NewRequest(http.MethodPost, "/mcp", nil)
	require.NoError(t, err)
	r.Header.Set("X-Retrievr-Cred-Exa", "")
	fn := PerRequestCredsContextFunc()
	ctx := fn(t.Context(), r)
	assert.Nil(t, PerCallCredsMapFromContext(ctx),
		"empty header value must not register as a credential")
}
