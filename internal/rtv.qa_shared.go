package internal

import (
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Shared Q&A plugin helpers — v5 cycle 1 / v2.8.0.
//
// Both Stack Exchange and Hacker News need to:
//   - Render unix epoch seconds → "YYYY-MM-DD" Published strings.
//   - Parse SearchFilters.DateFrom / DateTo (YYYY-MM-DD or YYYY) into
//     unix epoch seconds for the upstream date-filter params.
//
// Keeping the helpers here (rather than letting HN reach into SE's
// namespace, which was a transient cross-plugin coupling caught in v2.8
// review) makes the shared semantics explicit and gives a single home
// for any future date-parsing extension (RFC3339 fallback, etc.).
// ---------------------------------------------------------------------------

// qaShortDateLayout is the canonical YYYY-MM-DD layout used by Q&A
// providers when populating Publication.Published.
const qaShortDateLayout = "2006-01-02"

// qaYearDateLayout is the fallback layout for year-only SearchFilters
// date inputs.
const qaYearDateLayout = "2006"

// qaFilterDateLayouts is the parser try-list for SearchFilters dates.
// Order matters: more specific layouts first.
var qaFilterDateLayouts = []string{qaShortDateLayout, qaYearDateLayout}

// unixSecondsToShortDate converts a unix epoch (seconds) to YYYY-MM-DD
// in UTC. Returns "" for the zero value.
func unixSecondsToShortDate(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(qaShortDateLayout)
}

// parseFilterDateUnix parses a SearchFilters date string (YYYY-MM-DD or
// YYYY) into a unix epoch (seconds, UTC). Returns (0, false) when the
// input is empty or unparseable. Callers use ok to decide whether to
// emit the filter at all.
func parseFilterDateUnix(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	for _, layout := range qaFilterDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Unix(), true
		}
	}
	return 0, false
}
