package internal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouterPostCrossProviderDedupOnAtprotoURI exercises the end-to-end
// Cycle-5 invariant: a post with the same atproto_uri from two providers
// (in practice unlikely between Bluesky and Mastodon, but a hypothetical
// Bluesky bridge could produce it) merges into a single Result.
func TestRouterPostCrossProviderDedupOnAtprotoURI(t *testing.T) {
	t.Parallel()
	const sharedURI = "at://did:plc:abc/app.bsky.feed.post/3kdq5xyz"

	mkPostPub := func(source, id string) Publication {
		return Publication{
			ID:          source + ":" + id,
			Source:      source,
			ContentType: ContentTypePost,
			Title:       "shared post",
			URL:         "https://bsky.app/profile/alice.bsky.social/post/3kdq5xyz",
			SourceMetadata: map[string]any{
				MetaKeyAtprotoURI: sharedURI,
			},
		}
	}

	plugins := map[string]SourcePlugin{
		SourceBluesky: newMockPlugin(SourceBluesky, []Publication{
			mkPostPub(SourceBluesky, "3kdq5xyz"),
		}),
		// Second provider with the same atproto URI to prove cross-source merge.
		SourceMastodon: newMockPlugin(SourceMastodon, []Publication{
			mkPostPub(SourceMastodon, "mirror-1"),
		}),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "x", Limit: 10, Sort: SortRelevance,
	}, []string{SourceBluesky, SourceMastodon}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1, "same atproto_uri from two providers must merge")
	primary := result.Results[0]
	assert.NotEmpty(t, primary.AlsoFoundIn)
	allSources := append([]string{primary.Source}, primary.AlsoFoundIn...)
	assert.Contains(t, allSources, SourceBluesky)
	assert.Contains(t, allSources, SourceMastodon)
}

// TestRouterPostCrossProviderDedupOnURLFallback proves the secondary URL
// dedup path when atproto_uri is absent — e.g. Reddit + Mastodon both
// linking to the same external URL (unlikely but the safety net must hold).
func TestRouterPostCrossProviderDedupOnURLFallback(t *testing.T) {
	t.Parallel()
	const sharedURL = "https://example.com/article"

	mkPostPub := func(source, id string) Publication {
		return Publication{
			ID:          source + ":" + id,
			Source:      source,
			ContentType: ContentTypePost,
			Title:       "post linking out",
			URL:         sharedURL,
			// No SourceMetadata[MetaKeyAtprotoURI] → secondary path.
		}
	}

	plugins := map[string]SourcePlugin{
		SourceReddit: newMockPlugin(SourceReddit, []Publication{
			mkPostPub(SourceReddit, "t3_a"),
		}),
		SourceMastodon: newMockPlugin(SourceMastodon, []Publication{
			mkPostPub(SourceMastodon, "m1"),
		}),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "x", Limit: 10, Sort: SortRelevance,
	}, []string{SourceReddit, SourceMastodon}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1, "same URL must merge when atproto_uri is absent")
}
