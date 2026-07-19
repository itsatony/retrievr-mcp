package retrievr_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itsatony/retrievr-mcp/v2/pkg/retrievr"
)

// bytesConfigYAML is a minimal, valid retrievr config in per_request auth mode —
// the shape an embedding consumer (nx2) ships baked into its binary.
const bytesConfigYAML = `
server:
  name: "retrievr-embedded"
  http_addr: ":0"
  log_level: "info"
  log_format: "json"

router:
  default_sources: ["arxiv", "wikipedia"]
  per_source_timeout: "10s"
  dedup_enabled: true

auth:
  mode: "per_request"

sources:
  arxiv:
    enabled: true
    timeout: "15s"
  wikipedia:
    enabled: true
    timeout: "10s"
`

// TestNewClientFromConfigBytes verifies the in-memory bootstrap path an
// embedding consumer relies on: build a Client from a byte slice (no file on
// disk), enumerate sources offline, and release background goroutines via the
// returned Close func. It performs NO network search — ListSources is a pure
// in-process catalog read.
func TestNewClientFromConfigBytes(t *testing.T) {
	client, cleanup, err := retrievr.NewClientFromConfigBytes([]byte(bytesConfigYAML), nil)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, cleanup)
	defer cleanup()

	sources := client.ListSources(context.Background())
	require.NotEmpty(t, sources)

	enabled := map[string]bool{}
	for _, s := range sources {
		if s.Enabled {
			enabled[s.ID] = true
		}
	}
	assert.True(t, enabled["arxiv"], "arxiv should be enabled")
	assert.True(t, enabled["wikipedia"], "wikipedia should be enabled")
}

func TestNewClientFromConfigBytesInvalid(t *testing.T) {
	client, cleanup, err := retrievr.NewClientFromConfigBytes([]byte("{{not valid yaml"), nil)
	assert.Nil(t, client)
	assert.Nil(t, cleanup)
	require.Error(t, err)
}
