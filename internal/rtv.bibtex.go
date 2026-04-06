package internal

import (
	"fmt"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// BibTeX entry type constants
// ---------------------------------------------------------------------------

const (
	bibtexEntryArticle = "@article"
	bibtexEntryMisc    = "@misc"
)

// ---------------------------------------------------------------------------
// BibTeX field name constants
// ---------------------------------------------------------------------------

const (
	bibtexFieldTitle         = "title"
	bibtexFieldAuthor        = "author"
	bibtexFieldYear          = "year"
	bibtexFieldMonth         = "month"
	bibtexFieldDOI           = "doi"
	bibtexFieldURL           = "url"
	bibtexFieldAbstract      = "abstract"
	bibtexFieldKeywords      = "keywords"
	bibtexFieldNote          = "note"
	bibtexFieldEprint        = "eprint"
	bibtexFieldArchivePrefix = "archiveprefix"
	bibtexFieldPrimaryClass  = "primaryclass"
	bibtexFieldJournal       = "journal"
)

// ---------------------------------------------------------------------------
// BibTeX formatting constants
// ---------------------------------------------------------------------------

const (
	bibtexAuthorSeparator  = " and "
	bibtexKeywordSeparator = ", "
	bibtexFieldIndent      = "  "
	bibtexFieldAssign      = " = {"
	bibtexFieldClose       = "}"
	bibtexEntryOpen        = "{"
	bibtexEntryClose       = "}"
	bibtexFieldSep         = ","

	bibtexDefaultAuthor = "Unknown"
	bibtexDefaultYear   = "n.d."

	bibtexNotePrefix         = "Retrieved from "
	bibtexArchivePrefixArXiv = "arXiv"

	bibtexYearLength    = 4
	bibtexMonthStart    = 5
	bibtexMonthEnd      = 7
	bibtexDateMinLenMon = 7 // YYYY-MM minimum length to extract month

	bibtexCiteKeyFallback     = "unknown"
	bibtexCiteKeySuffixMaxLen = 8
	bibtexErrDetailNilPub     = "publication is nil"
	bibtexErrDetailEmptyTitle = "publication title is empty"
)

// ---------------------------------------------------------------------------
// BibTeX special character escape pairs
// ---------------------------------------------------------------------------

// bibtexEscapePairs lists characters that must be escaped in BibTeX field
// values. Order matters: ampersand first to avoid double-escaping.
// Immutable after init. Do not modify.
var bibtexEscapePairs = []struct {
	old string
	new string
}{
	{"&", `\&`},
	{"%", `\%`},
	{"#", `\#`},
	{"_", `\_`},
	{"~", `\~{}`},
	{"^", `\^{}`},
}

// ---------------------------------------------------------------------------
// BibTeX month abbreviation map
// ---------------------------------------------------------------------------

// bibtexMonthNames maps two-digit month strings to BibTeX three-letter names.
// Immutable after init. Do not modify.
var bibtexMonthNames = map[string]string{
	"01": "jan", "02": "feb", "03": "mar", "04": "apr",
	"05": "may", "06": "jun", "07": "jul", "08": "aug",
	"09": "sep", "10": "oct", "11": "nov", "12": "dec",
}

// ---------------------------------------------------------------------------
// BibTeX stop words for cite key generation
// ---------------------------------------------------------------------------

// bibtexStopWords are common words skipped when selecting the title word
// for a BibTeX cite key.
// Immutable after init. Do not modify.
var bibtexStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "on": true, "of": true,
	"for": true, "in": true, "to": true, "and": true, "with": true,
	"is": true, "are": true, "by": true, "from": true, "at": true,
}

// ---------------------------------------------------------------------------
// BibTeX source metadata key constants
// ---------------------------------------------------------------------------

const (
	bibtexMetaKeyJournal = "journal"
)

// bibtexJournalKeys lists all source-specific metadata keys that carry a
// journal or venue name. Order defines priority: the first non-empty match
// wins. Source-specific keys come before the generic fallback.
var bibtexJournalKeys = []string{
	pmMetaKeyJournal,       // "pubmed_journal"
	s2MetaKeyJournal,       // "s2_journal"
	emcMetaKeyJournal,      // "emc_journal"
	oaMetaKeyVenue,         // "oa_venue"
	arxivMetaKeyJournalRef, // "arxiv_journal_ref"
	bibtexMetaKeyJournal,   // "journal" (generic fallback)
}

// ---------------------------------------------------------------------------
// GenerateBibTeX
// ---------------------------------------------------------------------------

// GenerateBibTeX produces a BibTeX entry string from a Publication's metadata.
// It works for all sources and content types. Papers produce @article entries;
// models and datasets produce @misc entries.
//
// Returns ErrBibTeXGeneration if the publication is nil or has an empty title.
func GenerateBibTeX(pub *Publication) (string, error) {
	if pub == nil {
		return "", fmt.Errorf("%w: %s", ErrBibTeXGeneration, bibtexErrDetailNilPub)
	}
	if strings.TrimSpace(pub.Title) == "" {
		return "", fmt.Errorf("%w: %s", ErrBibTeXGeneration, bibtexErrDetailEmptyTitle)
	}

	entryType := bibtexEntryType(pub.ContentType)
	citeKey := generateCiteKey(pub)

	var b strings.Builder

	// Entry header.
	b.WriteString(entryType)
	b.WriteString(bibtexEntryOpen)
	b.WriteString(citeKey)
	b.WriteString(bibtexFieldSep)
	b.WriteByte('\n')

	// Author.
	authorStr := bibtexAuthors(pub.Authors)
	writeBibTeXField(&b, bibtexFieldAuthor, authorStr)

	// Title.
	writeBibTeXField(&b, bibtexFieldTitle, escapeBibTeX(pub.Title))

	// Year.
	year := bibtexYear(pub.Published)
	writeBibTeXField(&b, bibtexFieldYear, year)

	// Month.
	if month := bibtexMonth(pub.Published); month != "" {
		writeBibTeXField(&b, bibtexFieldMonth, month)
	}

	// Journal (from source metadata if available).
	if journal := bibtexJournal(pub.SourceMetadata); journal != "" {
		writeBibTeXField(&b, bibtexFieldJournal, escapeBibTeX(journal))
	}

	// DOI.
	if pub.DOI != "" {
		writeBibTeXField(&b, bibtexFieldDOI, pub.DOI)
	}

	// URL.
	if pub.URL != "" {
		writeBibTeXField(&b, bibtexFieldURL, pub.URL)
	}

	// Abstract.
	if pub.Abstract != "" {
		writeBibTeXField(&b, bibtexFieldAbstract, escapeBibTeX(pub.Abstract))
	}

	// Keywords (from categories).
	if len(pub.Categories) > 0 {
		writeBibTeXField(&b, bibtexFieldKeywords, strings.Join(pub.Categories, bibtexKeywordSeparator))
	}

	// ArXiv-specific fields.
	if pub.ArXivID != "" {
		writeBibTeXField(&b, bibtexFieldEprint, pub.ArXivID)
		writeBibTeXField(&b, bibtexFieldArchivePrefix, bibtexArchivePrefixArXiv)
		if len(pub.Categories) > 0 {
			writeBibTeXField(&b, bibtexFieldPrimaryClass, pub.Categories[0])
		}
	}

	// Note.
	if pub.Source != "" {
		writeBibTeXField(&b, bibtexFieldNote, bibtexNotePrefix+pub.Source)
	}

	// Close entry.
	b.WriteString(bibtexEntryClose)
	b.WriteByte('\n')

	return b.String(), nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// bibtexEntryType returns the BibTeX entry type for the given content type.
func bibtexEntryType(ct ContentType) string {
	switch ct {
	case ContentTypePaper:
		return bibtexEntryArticle
	default:
		return bibtexEntryMisc
	}
}

// bibtexAuthors formats a list of authors for BibTeX.
func bibtexAuthors(authors []Author) string {
	if len(authors) == 0 {
		return bibtexDefaultAuthor
	}
	names := make([]string, 0, len(authors))
	for _, a := range authors {
		name := strings.TrimSpace(a.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return bibtexDefaultAuthor
	}
	return strings.Join(names, bibtexAuthorSeparator)
}

// bibtexYear extracts the four-digit year from a YYYY-MM-DD or YYYY date string.
func bibtexYear(published string) string {
	if len(published) >= bibtexYearLength {
		return published[:bibtexYearLength]
	}
	return bibtexDefaultYear
}

// bibtexMonth extracts the BibTeX month abbreviation from a YYYY-MM-DD date.
func bibtexMonth(published string) string {
	if len(published) < bibtexDateMinLenMon {
		return ""
	}
	monthDigits := published[bibtexMonthStart:bibtexMonthEnd]
	return bibtexMonthNames[monthDigits]
}

// bibtexJournal extracts the journal/venue name from source metadata.
// It checks all source-specific metadata keys in priority order (see
// bibtexJournalKeys) and returns the first non-empty string found.
func bibtexJournal(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	for _, key := range bibtexJournalKeys {
		if v, ok := metadata[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// writeBibTeXField appends a single BibTeX field line to the builder.
func writeBibTeXField(b *strings.Builder, name, value string) {
	b.WriteString(bibtexFieldIndent)
	b.WriteString(name)
	b.WriteString(bibtexFieldAssign)
	b.WriteString(value)
	b.WriteString(bibtexFieldClose)
	b.WriteString(bibtexFieldSep)
	b.WriteByte('\n')
}

// escapeBibTeX escapes BibTeX special characters in a string.
// Braces are not escaped as they are used for BibTeX grouping.
func escapeBibTeX(s string) string {
	result := s
	for _, pair := range bibtexEscapePairs {
		result = strings.ReplaceAll(result, pair.old, pair.new)
	}
	return result
}

// generateCiteKey creates a BibTeX citation key from the publication.
// Format: lastnameyearfirstword (e.g., "smith2024attention").
// Falls back to source + sanitized ID suffix if author/title insufficient.
func generateCiteKey(pub *Publication) string {
	var parts []string

	// Author last name.
	lastName := ""
	if len(pub.Authors) > 0 {
		lastName = extractLastName(pub.Authors[0].Name)
	}
	if lastName != "" {
		parts = append(parts, sanitizeCiteKeyPart(lastName))
	}

	// Year.
	year := bibtexYear(pub.Published)
	if year != bibtexDefaultYear {
		parts = append(parts, year)
	}

	// First significant title word.
	word := firstSignificantWord(pub.Title)
	if word != "" {
		parts = append(parts, sanitizeCiteKeyPart(word))
	}

	key := strings.Join(parts, "")
	if key == "" {
		// Fallback: source + last 8 chars of ID (or whole ID if short).
		key = bibtexCiteKeyFallback
		if pub.Source != "" {
			key = pub.Source
		}
		// Append a short suffix from ID to make it somewhat unique.
		if rawID := stripSourcePrefix(pub.ID); rawID != "" {
			sanitized := sanitizeCiteKeyPart(rawID)
			if len(sanitized) > bibtexCiteKeySuffixMaxLen {
				sanitized = sanitized[:bibtexCiteKeySuffixMaxLen]
			}
			key += sanitized
		}
	}
	return key
}

// extractLastName returns the last whitespace-separated token of a name.
func extractLastName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	parts := strings.Fields(name)
	return parts[len(parts)-1]
}

// firstSignificantWord returns the first non-stop-word from a title,
// lowercased and with non-alphanumeric characters removed.
func firstSignificantWord(title string) string {
	words := strings.Fields(strings.TrimSpace(title))
	for _, w := range words {
		lower := strings.ToLower(w)
		cleaned := sanitizeCiteKeyPart(lower)
		if cleaned != "" && !bibtexStopWords[cleaned] {
			return cleaned
		}
	}
	// If all words are stop words, use the first word anyway.
	if len(words) > 0 {
		return sanitizeCiteKeyPart(strings.ToLower(words[0]))
	}
	return ""
}

// sanitizeCiteKeyPart removes non-alphanumeric characters and lowercases.
func sanitizeCiteKeyPart(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// stripSourcePrefix removes the "source:" prefix from a prefixed ID.
func stripSourcePrefix(id string) string {
	_, after, found := strings.Cut(id, prefixedIDSeparator)
	if found {
		return after
	}
	return id
}
