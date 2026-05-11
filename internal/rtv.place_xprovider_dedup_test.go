package internal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouterPlaceCrossProviderDedupOnOSMID exercises the end-to-end
// Cycle-3 invariant: a place returned by both Photon and Nominatim with
// the same `osm_id` composite key merges into a single Result. Guards
// the cross-provider dedup wiring through Router.Search.
func TestRouterPlaceCrossProviderDedupOnOSMID(t *testing.T) {
	t.Parallel()

	const sharedOSMID = "node:240109189"

	mkPlacePub := func(source string) Publication {
		lat := 52.51629
		lon := 13.37770
		return Publication{
			ID:          source + ":" + sharedOSMID,
			Source:      source,
			ContentType: ContentTypePlace,
			Title:       "Brandenburger Tor",
			Lat:         &lat,
			Lon:         &lon,
			SourceMetadata: map[string]any{
				MetaKeyOSMID: sharedOSMID,
				smetaOSMType: "node",
			},
		}
	}

	plugins := map[string]SourcePlugin{
		SourcePhoton: newMockPlugin(SourcePhoton, []Publication{
			mkPlacePub(SourcePhoton),
		}),
		SourceNominatim: newMockPlugin(SourceNominatim, []Publication{
			mkPlacePub(SourceNominatim),
		}),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "test", Limit: 10, Sort: SortRelevance,
	}, []string{SourcePhoton, SourceNominatim}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1, "same osm_id from two providers must merge into one Result")

	primary := result.Results[0]
	assert.NotEmpty(t, primary.AlsoFoundIn)
	allSources := append([]string{primary.Source}, primary.AlsoFoundIn...)
	assert.Contains(t, allSources, SourcePhoton)
	assert.Contains(t, allSources, SourceNominatim)
}

// TestRouterPlaceCrossProviderDedupOnCoords exercises the secondary
// dedup path: two providers return a place without osm_id but with
// matching lat/lon (rounded to 5 dp). They should still merge.
func TestRouterPlaceCrossProviderDedupOnCoords(t *testing.T) {
	t.Parallel()

	lat := 48.85837
	lon := 2.29448

	mkPlacePub := func(source string) Publication {
		l1, l2 := lat, lon
		return Publication{
			ID:          source + ":" + "no-osm-id-1",
			Source:      source,
			ContentType: ContentTypePlace,
			Title:       "Eiffel Tower (no osm_id)",
			Lat:         &l1,
			Lon:         &l2,
			// Deliberately no SourceMetadata[MetaKeyOSMID]. Dedup must fall
			// through to the coord-rounding path.
		}
	}

	plugins := map[string]SourcePlugin{
		SourceTomTom: newMockPlugin(SourceTomTom, []Publication{
			mkPlacePub(SourceTomTom),
		}),
		SourcePhoton: newMockPlugin(SourcePhoton, []Publication{
			mkPlacePub(SourcePhoton),
		}),
	}
	r := testRouter(plugins)

	result, err := r.Search(context.Background(), SearchParams{
		Query: "x", Limit: 10, Sort: SortRelevance,
	}, []string{SourceTomTom, SourcePhoton}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1, "same (lat,lon) rounded to 5 dp must merge when no osm_id")
}
