package internal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouterImageCrossProviderDedupOnWikimediaFile exercises the end-to-end
// Cycle-4 invariant: an image returned by both Wikimedia and Europeana
// with the same Commons file (via MetaKeyWikimediaFile) merges into a
// single Result.
func TestRouterImageCrossProviderDedupOnWikimediaFile(t *testing.T) {
	t.Parallel()

	const sharedFile = "File:Mona_Lisa.jpg"

	mkImagePub := func(source string) Publication {
		return Publication{
			ID:          source + ":" + sharedFile,
			Source:      source,
			ContentType: ContentTypeImage,
			Title:       "Mona Lisa",
			MediaURL:    "https://upload.wikimedia.org/wikipedia/commons/6/6a/Mona_Lisa.jpg",
			MediaMime:   "image/jpeg",
			License:     "Public domain",
			SourceMetadata: map[string]any{
				MetaKeyWikimediaFile: sharedFile,
			},
		}
	}

	plugins := map[string]SourcePlugin{
		SourceWikimedia: newMockPlugin(SourceWikimedia, []Publication{
			mkImagePub(SourceWikimedia),
		}),
		SourceEuropeana: newMockPlugin(SourceEuropeana, []Publication{
			mkImagePub(SourceEuropeana),
		}),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{SourceWikimedia, SourceEuropeana}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1, "same wikimedia_file from two providers must merge into one Result")

	primary := result.Results[0]
	assert.NotEmpty(t, primary.AlsoFoundIn)
	allSources := append([]string{primary.Source}, primary.AlsoFoundIn...)
	assert.Contains(t, allSources, SourceWikimedia)
	assert.Contains(t, allSources, SourceEuropeana)
}

// TestRouterImageCrossProviderDedupOnMediaURLFallback exercises the
// secondary dedup path: providers without `wikimedia_file` metadata fall
// through to MediaURL equality.
func TestRouterImageCrossProviderDedupOnMediaURLFallback(t *testing.T) {
	t.Parallel()

	const sharedMediaURL = "https://i.example.com/photo.jpg"

	mkImagePub := func(source, id string) Publication {
		return Publication{
			ID:          source + ":" + id,
			Source:      source,
			ContentType: ContentTypeImage,
			Title:       "photo",
			MediaURL:    sharedMediaURL,
			// No SourceMetadata[MetaKeyWikimediaFile] → secondary path.
		}
	}

	plugins := map[string]SourcePlugin{
		SourceBrave: newMockPlugin(SourceBrave, []Publication{
			mkImagePub(SourceBrave, "image:a"),
		}),
		SourceWikimedia: newMockPlugin(SourceWikimedia, []Publication{
			mkImagePub(SourceWikimedia, "File:Photo.jpg"),
		}),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "x", Limit: 10, Sort: SortRelevance,
	}, []string{SourceBrave, SourceWikimedia}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1, "same MediaURL must merge when no wikimedia_file")
}
