package internal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouterVideoCrossProviderDedupOnYouTubeID exercises the end-to-end
// Cycle-2 invariant: a video returned by both the official YouTube plugin
// and the Scrapingdog fallback merges into a single Result via the
// MetaKeyYouTubeID dedup key. This guards the SearchPath wiring (mock
// plugins → router.dedup() → Result), not just the dedup() unit.
func TestRouterVideoCrossProviderDedupOnYouTubeID(t *testing.T) {
	t.Parallel()

	const sharedID = "dQw4w9WgXcQ"

	mkVideoPub := func(source string) Publication {
		return Publication{
			ID:          source + ":" + sharedID,
			Source:      source,
			ContentType: ContentTypeVideo,
			Title:       "Never Gonna Give You Up",
			URL:         "https://www.youtube.com/watch?v=" + sharedID,
			SourceMetadata: map[string]any{
				MetaKeyYouTubeID: sharedID,
			},
		}
	}

	// Use mockPlugin (declared in rtv.router_test.go) but with the two
	// real source IDs so the Router accepts them.
	plugins := map[string]SourcePlugin{
		SourceYouTube: newMockPlugin(SourceYouTube, []Publication{
			mkVideoPub(SourceYouTube),
		}),
		SourceScrapingdogYouTube: newMockPlugin(SourceScrapingdogYouTube, []Publication{
			mkVideoPub(SourceScrapingdogYouTube),
		}),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{SourceYouTube, SourceScrapingdogYouTube}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1, "same youtube_id from two providers must merge into one Result")

	primary := result.Results[0]
	assert.NotEmpty(t, primary.AlsoFoundIn, "dedup must record the other provider")
	// Deterministic sort by source ID in collected results — `scrapingdog_youtube`
	// sorts before `youtube`, so scrapingdog wins the primary slot and youtube
	// goes into AlsoFoundIn. Just assert both are accounted for.
	allSources := append([]string{primary.Source}, primary.AlsoFoundIn...)
	assert.Contains(t, allSources, SourceYouTube)
	assert.Contains(t, allSources, SourceScrapingdogYouTube)
}
