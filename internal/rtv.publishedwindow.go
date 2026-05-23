package internal

import (
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// PublishedAfter / PublishedBefore — v2.22.0 ISO-8601 freshness window.
//
// The PublishedAfter / PublishedBefore SearchFilters fields complement the
// day-precision DateFrom / DateTo with sub-day cutoffs. Three integration
// points use the helpers below:
//
//   1. parsePublishedAt  — strict RFC3339 parse; used by the router for
//      validation and by plugin push-downs that need the parsed time.Time.
//   2. effectiveDateBounds — used by coarse+postfilter plugins (everything
//      that only accepts day-precision upstream). Returns the existing
//      DateFrom/DateTo when set; otherwise downcasts PublishedAfter /
//      PublishedBefore to a UTC-day floor / ceiling so the upstream query
//      window is at least as inclusive as the client window.
//   3. filterByPublishedWindow — the router-level safety net. Trims merged
//      results to the precise window using each hit's
//      SourceMetadata["published_at"]. Boundaries are exclusive; hits with
//      missing/unparseable timestamps are kept by default and dropped only
//      when SearchFilters.StrictPublishedAt is true.
// ---------------------------------------------------------------------------

// parsePublishedAt parses s as time.RFC3339. Empty input is treated as "not
// set" — the zero time.Time + false are returned without an error, so
// callers can branch on the boolean rather than separately checking for
// empty. Returns ErrInvalidPublishedAt on a non-empty malformed value.
func parsePublishedAt(s string) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false, ErrInvalidPublishedAt
	}
	return t.UTC(), true, nil
}

// validatePublishedWindow checks that PublishedAfter and PublishedBefore
// parse as RFC3339 and that, when both are set, after <= before. The router
// calls this once at the top of Router.Search so plugins downstream can
// trust the field shape.
func validatePublishedWindow(f SearchFilters) error {
	after, hasAfter, err := parsePublishedAt(f.PublishedAfter)
	if err != nil {
		return err
	}
	before, hasBefore, err := parsePublishedAt(f.PublishedBefore)
	if err != nil {
		return err
	}
	if hasAfter && hasBefore && after.After(before) {
		return ErrInvalidPublishedAt
	}
	return nil
}

// effectiveDateBounds returns the date_from / date_to a coarse+postfilter
// plugin should send upstream. When DateFrom is set it wins (callers
// explicitly chose day precision); otherwise PublishedAfter is downcast to
// its UTC-day floor (YYYY-MM-DD). Symmetric for the upper bound.
//
// Invalid PublishedAfter / PublishedBefore strings are ignored here —
// validatePublishedWindow runs at the router boundary and would have
// returned an error before any plugin reached this code path.
func effectiveDateBounds(f SearchFilters) (dateFrom, dateTo string) {
	dateFrom = f.DateFrom
	dateTo = f.DateTo
	if dateFrom == "" {
		if t, ok, _ := parsePublishedAt(f.PublishedAfter); ok {
			dateFrom = t.Format("2006-01-02")
		}
	}
	if dateTo == "" {
		if t, ok, _ := parsePublishedAt(f.PublishedBefore); ok {
			dateTo = t.Format("2006-01-02")
		}
	}
	return dateFrom, dateTo
}

// filterByPublishedWindow trims a merged result slice to the exact
// PublishedAfter / PublishedBefore window. No-op when neither bound is set.
// Hits with missing or unparseable SourceMetadata["published_at"] are kept
// by default and dropped only when f.StrictPublishedAt is true.
//
// Boundaries are exclusive: "after T" means strictly later than T, "before
// T" means strictly earlier than T. This matches the natural reading and
// avoids accidental boundary collisions with upstream timestamps quantized
// to the second.
func filterByPublishedWindow(in []Publication, f SearchFilters) []Publication {
	after, hasAfter, _ := parsePublishedAt(f.PublishedAfter)
	before, hasBefore, _ := parsePublishedAt(f.PublishedBefore)
	if !hasAfter && !hasBefore {
		return in
	}

	out := in[:0]
	for _, p := range in {
		raw := metaString(p.SourceMetadata, smetaPublishedAt)
		if raw == "" {
			if f.StrictPublishedAt {
				continue
			}
			out = append(out, p)
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			if f.StrictPublishedAt {
				continue
			}
			out = append(out, p)
			continue
		}
		t = t.UTC()
		if hasAfter && !t.After(after) {
			continue
		}
		if hasBefore && !t.Before(before) {
			continue
		}
		out = append(out, p)
	}
	return out
}
