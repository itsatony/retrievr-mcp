package internal

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Issue #1, part 3, option (b): ids stay opaque, `url` becomes the contract.
//
// A web result's id is `<source>:<sha256(url)[:16]>` — one-way by
// construction, so nothing inside retrievr can turn it back into a page. The
// caller reaches the content through `url`, which therefore has to be
// something the caller can COUNT on being there.
//
// An `omitempty` key that disappears when empty makes "this source returned no
// link" indistinguishable from a schema mismatch on the caller's side, so the
// key is now emitted unconditionally.
// ---------------------------------------------------------------------------

func TestResultJSONAlwaysCarriesURL(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		res  Result
		want string
	}{
		{"web_result_with_url", Result{Kind: KindWeb, ID: "linkup:abc", Title: "t", URL: "https://example.com/a"}, "https://example.com/a"},
		{"result_without_url", Result{Kind: KindPlace, ID: "photon:abc", Title: "t"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.res)
			require.NoError(t, err)

			var decoded map[string]any
			require.NoError(t, json.Unmarshal(raw, &decoded))

			value, present := decoded["url"]
			require.True(t, present,
				"the v2 Result wire must always carry a `url` key — it is THE way to reach a "+
					"source whose supports_get is false: %s", raw)
			assert.Equal(t, tc.want, value)
		})
	}
}

// TestHashedIDSourcesAllPublishURL measures the premise behind option (b):
// every source that mints an OPAQUE hashed id must be one whose results carry
// a url, otherwise the content would be unreachable by any route at all.
func TestHashedIDSourcesAllPublishURL(t *testing.T) {
	t.Parallel()

	// hashURL is a truncated sha256 — assert that, because the whole design
	// decision rests on the id being one-way.
	first := hashURL("https://example.com/a")
	second := hashURL("https://example.com/a")
	assert.Equal(t, first, second, "the id must be stable for the same url")
	assert.NotContains(t, first, "example.com", "the id must not be a reversible encoding of the url")
	assert.NotEqual(t, first, hashURL("https://example.com/b"))
}
