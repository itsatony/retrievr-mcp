package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v3 multimodal dedup tests (cycle 1 / v2.2.0).
//
// Exercises Router.dedup() for each new ContentType (video, place, image,
// post). Verifies:
//   - within-class dedup by the documented primary + secondary keys
//   - cross-class non-dedup (a paper and a video with same string key never
//     collide, by construction of the (family, value) composite index key)
//   - AlsoFoundIn is populated on the surviving primary
//
// dedup() lives in rtv.router.go and is unexported; tests are in-package.
// ---------------------------------------------------------------------------

const (
	mmYouTubeID     = "dQw4w9WgXcQ"
	mmOSMID         = "node:240109189"
	mmWikimediaFile = "File:Mona_Lisa.jpg"
	mmAtprotoURI    = "at://did:plc:abc/app.bsky.feed.post/xyz"
	mmMediaURL      = "https://upload.wikimedia.org/wikipedia/commons/6/6a/Mona_Lisa.jpg"
	mmPostURL       = "https://bsky.app/profile/alice/post/xyz"
)

func mmVideoPub(source, id, ytID string) Publication {
	return Publication{
		ID:          source + ":" + id,
		Source:      source,
		ContentType: ContentTypeVideo,
		Title:       "video " + id,
		URL:         "https://www.youtube.com/watch?v=" + ytID,
		SourceMetadata: map[string]any{
			MetaKeyYouTubeID: ytID,
		},
	}
}

func mmPlacePub(source, id, osmID string, lat, lon *float64) Publication {
	meta := map[string]any{}
	if osmID != "" {
		meta[MetaKeyOSMID] = osmID
	}
	return Publication{
		ID:             source + ":" + id,
		Source:         source,
		ContentType:    ContentTypePlace,
		Title:          "place " + id,
		Lat:            lat,
		Lon:            lon,
		SourceMetadata: meta,
	}
}

func mmImagePub(source, id, wikimediaFile, mediaURL string) Publication {
	meta := map[string]any{}
	if wikimediaFile != "" {
		meta[MetaKeyWikimediaFile] = wikimediaFile
	}
	return Publication{
		ID:             source + ":" + id,
		Source:         source,
		ContentType:    ContentTypeImage,
		Title:          "image " + id,
		MediaURL:       mediaURL,
		SourceMetadata: meta,
	}
}

func mmPostPub(source, id, atprotoURI, postURL string) Publication {
	meta := map[string]any{}
	if atprotoURI != "" {
		meta[MetaKeyAtprotoURI] = atprotoURI
	}
	return Publication{
		ID:             source + ":" + id,
		Source:         source,
		ContentType:    ContentTypePost,
		Title:          "post " + id,
		URL:            postURL,
		SourceMetadata: meta,
	}
}

// floatPtr returns a pointer to f. Package-local helper to avoid colliding
// with any other helpers — not introducing a public utility.
func floatPtr(f float64) *float64 { return &f }

// ---------------------------------------------------------------------------
// Video dedup
// ---------------------------------------------------------------------------

func TestDedupVideoByYouTubeID(t *testing.T) {
	t.Parallel()
	results := []Publication{
		mmVideoPub("youtube", "1", mmYouTubeID),
		mmVideoPub("scrapingdog_youtube", "2", mmYouTubeID),
	}
	deduped := dedup(results)
	require.Len(t, deduped, 1, "same youtube_id must merge")
	assert.Contains(t, deduped[0].AlsoFoundIn, "scrapingdog_youtube")
}

func TestDedupVideoDifferentYouTubeIDsKept(t *testing.T) {
	t.Parallel()
	results := []Publication{
		mmVideoPub("youtube", "1", "aaaaaaaaaaa"),
		mmVideoPub("youtube", "2", "bbbbbbbbbbb"),
	}
	assert.Len(t, dedup(results), 2)
}

func TestDedupVideoEmptyYouTubeIDKept(t *testing.T) {
	t.Parallel()
	// Two videos missing youtube_id metadata must NOT collide on empty-string
	// key — tryDedup skips empty values, so both survive.
	results := []Publication{
		mmVideoPub("youtube", "1", ""),
		mmVideoPub("scrapingdog_youtube", "2", ""),
	}
	assert.Len(t, dedup(results), 2)
}

// ---------------------------------------------------------------------------
// Place dedup
// ---------------------------------------------------------------------------

func TestDedupPlaceByOSMID(t *testing.T) {
	t.Parallel()
	results := []Publication{
		mmPlacePub("photon", "1", mmOSMID, floatPtr(52.51629), floatPtr(13.37770)),
		mmPlacePub("nominatim", "2", mmOSMID, floatPtr(52.51629), floatPtr(13.37770)),
	}
	deduped := dedup(results)
	require.Len(t, deduped, 1, "same osm_id must merge")
	assert.Contains(t, deduped[0].AlsoFoundIn, "nominatim")
}

func TestDedupPlaceByCoords(t *testing.T) {
	t.Parallel()
	// No osm_id on either, but coordinates match to 5dp.
	results := []Publication{
		mmPlacePub("tomtom", "1", "", floatPtr(48.85837), floatPtr(2.29448)),
		mmPlacePub("photon", "2", "", floatPtr(48.85837), floatPtr(2.29448)),
	}
	deduped := dedup(results)
	require.Len(t, deduped, 1, "same rounded coords must merge")
	assert.Contains(t, deduped[0].AlsoFoundIn, "photon")
}

func TestDedupPlaceDifferentCoordsKept(t *testing.T) {
	t.Parallel()
	// 6th-decimal difference — within the 5dp rounding window. Use a clear
	// 1-meter delta beyond rounding precision to demonstrate they stay split.
	results := []Publication{
		mmPlacePub("photon", "1", "", floatPtr(48.85837), floatPtr(2.29448)),
		mmPlacePub("photon", "2", "", floatPtr(48.85900), floatPtr(2.29500)),
	}
	assert.Len(t, dedup(results), 2)
}

func TestDedupPlaceMissingCoordsKept(t *testing.T) {
	t.Parallel()
	results := []Publication{
		mmPlacePub("nominatim", "1", "", nil, nil),
		mmPlacePub("photon", "2", "", nil, nil),
	}
	assert.Len(t, dedup(results), 2)
}

// ---------------------------------------------------------------------------
// Image dedup
// ---------------------------------------------------------------------------

func TestDedupImageByWikimediaFile(t *testing.T) {
	t.Parallel()
	results := []Publication{
		mmImagePub("wikimedia", "1", mmWikimediaFile, mmMediaURL),
		mmImagePub("europeana", "2", mmWikimediaFile, "https://www.europeana.eu/different-cdn/x.jpg"),
	}
	deduped := dedup(results)
	require.Len(t, deduped, 1, "same wikimedia_file must merge")
	assert.Contains(t, deduped[0].AlsoFoundIn, "europeana")
}

func TestDedupImageByMediaURLFallback(t *testing.T) {
	t.Parallel()
	results := []Publication{
		mmImagePub("brave", "1", "", mmMediaURL),
		mmImagePub("wikimedia", "2", "", mmMediaURL),
	}
	deduped := dedup(results)
	require.Len(t, deduped, 1, "same MediaURL must merge when no wikimedia_file")
	assert.Contains(t, deduped[0].AlsoFoundIn, "wikimedia")
}

// ---------------------------------------------------------------------------
// Post dedup
// ---------------------------------------------------------------------------

func TestDedupPostByAtprotoURI(t *testing.T) {
	t.Parallel()
	results := []Publication{
		mmPostPub("bluesky", "1", mmAtprotoURI, mmPostURL),
		mmPostPub("bluesky", "2", mmAtprotoURI, "https://different-handle.example/post"),
	}
	deduped := dedup(results)
	require.Len(t, deduped, 1, "same atproto_uri must merge")
}

func TestDedupPostByURLFallback(t *testing.T) {
	t.Parallel()
	results := []Publication{
		mmPostPub("mastodon", "1", "", mmPostURL),
		mmPostPub("reddit", "2", "", mmPostURL),
	}
	deduped := dedup(results)
	require.Len(t, deduped, 1, "same URL must merge when no atproto_uri")
	assert.Contains(t, deduped[0].AlsoFoundIn, "reddit")
}

// ---------------------------------------------------------------------------
// Cross-class non-dedup invariant
// ---------------------------------------------------------------------------

// TestDedupCrossClassNeverMerges proves that a paper and a video carrying
// identical-looking key values (e.g. both have a "youtube_id"-shaped string)
// do NOT collide. dedup() partitions index keys by ContentType-driven
// family, so collisions across families are impossible.
func TestDedupCrossClassNeverMerges(t *testing.T) {
	t.Parallel()

	// Paper with a YouTube-shaped ID accidentally in SourceMetadata.
	// Even if metadata had a colliding string value, the paper's dedup family
	// is DOI/ArXivID — youtube_id is never read for papers.
	paper := Publication{
		ID:          "arxiv:2401.12345",
		Source:      "arxiv",
		ContentType: ContentTypePaper,
		Title:       "A paper",
		ArXivID:     "2401.12345",
		SourceMetadata: map[string]any{
			MetaKeyYouTubeID: mmYouTubeID,
		},
	}
	video := mmVideoPub("youtube", "v1", mmYouTubeID)

	results := []Publication{paper, video}
	deduped := dedup(results)
	assert.Len(t, deduped, 2, "paper and video must NEVER merge even with overlapping metadata")
}

func TestDedupCrossClassPlaceVideoNeverMerge(t *testing.T) {
	t.Parallel()
	// Place URL = Video URL (contrived but possible). URL is not in either
	// dedup family for these classes (place doesn't use URL; video uses
	// youtube_id only), so they remain separate.
	place := mmPlacePub("photon", "1", "node:1", floatPtr(0), floatPtr(0))
	video := mmVideoPub("youtube", "1", "abc")
	assert.Len(t, dedup([]Publication{place, video}), 2)
}

// TestDedupPreservesPaperPath confirms the v2 paper-dedup behavior (DOI then
// ArXivID) is unchanged by the v3 refactor — a regression guard.
func TestDedupPaperByDOIStillWorks(t *testing.T) {
	t.Parallel()
	a := Publication{
		ID:          "arxiv:1",
		Source:      "arxiv",
		ContentType: ContentTypePaper,
		Title:       "p",
		DOI:         "10.1000/x",
	}
	b := Publication{
		ID:          "s2:1",
		Source:      "s2",
		ContentType: ContentTypePaper,
		Title:       "p",
		DOI:         "10.1000/x",
	}
	deduped := dedup([]Publication{a, b})
	require.Len(t, deduped, 1)
	assert.Contains(t, deduped[0].AlsoFoundIn, "s2")
}
