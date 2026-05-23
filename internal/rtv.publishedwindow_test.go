package internal

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestParsePublishedAt covers the strict RFC3339 contract: empty input is
// treated as "not set", malformed values return ErrInvalidPublishedAt, and
// valid inputs are normalized to UTC.
func TestParsePublishedAt(t *testing.T) {
	t.Run("empty returns not-set without error", func(t *testing.T) {
		_, ok, err := parsePublishedAt("")
		assert.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("whitespace-only treated as empty", func(t *testing.T) {
		_, ok, err := parsePublishedAt("  ")
		assert.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("valid RFC3339 with Z", func(t *testing.T) {
		got, ok, err := parsePublishedAt("2026-05-23T08:00:00Z")
		assert.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC), got)
	})
	t.Run("valid RFC3339 with offset normalizes to UTC", func(t *testing.T) {
		got, ok, err := parsePublishedAt("2026-05-23T10:00:00+02:00")
		assert.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC), got)
	})
	t.Run("date-only is rejected", func(t *testing.T) {
		_, _, err := parsePublishedAt("2026-05-23")
		assert.ErrorIs(t, err, ErrInvalidPublishedAt)
	})
	t.Run("garbage is rejected", func(t *testing.T) {
		_, _, err := parsePublishedAt("yesterday")
		assert.ErrorIs(t, err, ErrInvalidPublishedAt)
	})
}

// TestValidatePublishedWindow verifies the router-boundary precondition:
// malformed inputs raise ErrInvalidPublishedAt; an inverted window
// (after > before) is also rejected.
func TestValidatePublishedWindow(t *testing.T) {
	t.Run("both empty is OK", func(t *testing.T) {
		assert.NoError(t, validatePublishedWindow(SearchFilters{}))
	})
	t.Run("only after set is OK", func(t *testing.T) {
		assert.NoError(t, validatePublishedWindow(SearchFilters{
			PublishedAfter: "2026-05-23T00:00:00Z",
		}))
	})
	t.Run("malformed after is rejected", func(t *testing.T) {
		err := validatePublishedWindow(SearchFilters{PublishedAfter: "not-a-date"})
		assert.True(t, errors.Is(err, ErrInvalidPublishedAt))
	})
	t.Run("inverted window is rejected", func(t *testing.T) {
		err := validatePublishedWindow(SearchFilters{
			PublishedAfter:  "2026-05-23T10:00:00Z",
			PublishedBefore: "2026-05-23T08:00:00Z",
		})
		assert.True(t, errors.Is(err, ErrInvalidPublishedAt))
	})
	t.Run("equal bounds is allowed (yields empty window in post-filter)", func(t *testing.T) {
		assert.NoError(t, validatePublishedWindow(SearchFilters{
			PublishedAfter:  "2026-05-23T08:00:00Z",
			PublishedBefore: "2026-05-23T08:00:00Z",
		}))
	})
}

// TestEffectiveDateBounds verifies that coarse+postfilter plugins receive a
// day-precision bound when only the precise PublishedAfter is set, and that
// an explicit DateFrom wins.
func TestEffectiveDateBounds(t *testing.T) {
	t.Run("zero filters → empty bounds", func(t *testing.T) {
		from, to := effectiveDateBounds(SearchFilters{})
		assert.Empty(t, from)
		assert.Empty(t, to)
	})
	t.Run("DateFrom wins over PublishedAfter", func(t *testing.T) {
		from, _ := effectiveDateBounds(SearchFilters{
			DateFrom:       "2026-01-15",
			PublishedAfter: "2026-05-23T08:00:00Z",
		})
		assert.Equal(t, "2026-01-15", from)
	})
	t.Run("PublishedAfter downcasts to UTC day floor", func(t *testing.T) {
		from, _ := effectiveDateBounds(SearchFilters{
			PublishedAfter: "2026-05-23T23:30:00+02:00", // = 2026-05-23T21:30 UTC
		})
		assert.Equal(t, "2026-05-23", from)
	})
	t.Run("PublishedBefore downcasts symmetrically", func(t *testing.T) {
		_, to := effectiveDateBounds(SearchFilters{
			PublishedBefore: "2026-05-24T00:30:00Z",
		})
		assert.Equal(t, "2026-05-24", to)
	})
}

// TestFilterByPublishedWindow exercises the router-level safety net. Hits
// with missing/unparseable timestamps are kept by default and dropped only
// under StrictPublishedAt. Boundaries are exclusive.
func TestFilterByPublishedWindow(t *testing.T) {
	mk := func(pub string) Publication {
		p := Publication{Title: pub}
		if pub != "" {
			p.SourceMetadata = map[string]any{"published_at": pub}
		}
		return p
	}

	all := []Publication{
		mk("2026-05-23T07:00:00Z"), // before window
		mk("2026-05-23T08:00:00Z"), // boundary — excluded
		mk("2026-05-23T09:00:00Z"), // in window
		mk("2026-05-23T11:00:00Z"), // boundary — excluded
		mk("2026-05-23T12:00:00Z"), // after window
		mk(""),                     // missing
		{Title: "garbage", SourceMetadata: map[string]any{"published_at": "yesterday"}},
	}

	t.Run("no filter is a no-op", func(t *testing.T) {
		got := filterByPublishedWindow(all, SearchFilters{})
		assert.Len(t, got, len(all))
	})

	t.Run("after/before window keeps only strictly-inside hits + unparseable", func(t *testing.T) {
		got := filterByPublishedWindow(all, SearchFilters{
			PublishedAfter:  "2026-05-23T08:00:00Z",
			PublishedBefore: "2026-05-23T11:00:00Z",
		})
		// Expect: in-window (09:00), missing, garbage.
		titles := make([]string, 0, len(got))
		for _, p := range got {
			titles = append(titles, p.Title)
		}
		assert.ElementsMatch(t, []string{
			"2026-05-23T09:00:00Z",
			"",
			"garbage",
		}, titles)
	})

	t.Run("strict mode drops missing and unparseable", func(t *testing.T) {
		got := filterByPublishedWindow(all, SearchFilters{
			PublishedAfter:    "2026-05-23T08:00:00Z",
			PublishedBefore:   "2026-05-23T11:00:00Z",
			StrictPublishedAt: true,
		})
		assert.Len(t, got, 1)
		assert.Equal(t, "2026-05-23T09:00:00Z", got[0].Title)
	})

	t.Run("only PublishedAfter set", func(t *testing.T) {
		got := filterByPublishedWindow([]Publication{
			mk("2026-05-23T07:00:00Z"),
			mk("2026-05-23T08:00:00Z"),
			mk("2026-05-23T09:00:00Z"),
		}, SearchFilters{PublishedAfter: "2026-05-23T08:00:00Z"})
		assert.Len(t, got, 1)
		assert.Equal(t, "2026-05-23T09:00:00Z", got[0].Title)
	})

	t.Run("only PublishedBefore set", func(t *testing.T) {
		got := filterByPublishedWindow([]Publication{
			mk("2026-05-23T07:00:00Z"),
			mk("2026-05-23T08:00:00Z"),
			mk("2026-05-23T09:00:00Z"),
		}, SearchFilters{PublishedBefore: "2026-05-23T08:00:00Z"})
		assert.Len(t, got, 1)
		assert.Equal(t, "2026-05-23T07:00:00Z", got[0].Title)
	})
}
