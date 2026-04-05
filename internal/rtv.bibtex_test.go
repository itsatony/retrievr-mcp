package internal

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

const (
	testBibAuthor1    = "Alice Smith"
	testBibAuthor2    = "Bob Jones"
	testBibTitle      = "Attention Is All You Need"
	testBibDate       = "2024-06-15"
	testBibDOI        = "10.1234/example.2024"
	testBibURL        = "https://arxiv.org/abs/2401.12345"
	testBibAbstract   = "We propose a new architecture."
	testBibArXivID    = "2401.12345"
	testBibCategory1  = "cs.AI"
	testBibCategory2  = "cs.CL"
	testBibJournal    = "Nature Machine Intelligence"
	testBibPubID      = "arxiv:2401.12345"
	testBibHFModelID  = "huggingface:models/bert-base"
	testBibDatasetID  = "huggingface:datasets/squad"
	testBibS2ID       = "s2:abc123"
	testBibPMID       = "pubmed:12345678"
	testBibEMCID      = "europmc:PMC7654321"
	testBibCiteYear   = "2024"
	testBibCiteMonth  = "jun"
	testBibSpecialStr = "Results & Discussion: 100% of #1_ranked ~ models^2"
)

// ---------------------------------------------------------------------------
// TestGenerateBibTeX
// ---------------------------------------------------------------------------

func TestGenerateBibTeX(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pub        *Publication
		wantErr    bool
		wantPrefix string   // expected start of output
		wantFields []string // strings that must appear in output
		wantAbsent []string // strings that must NOT appear in output
	}{
		{
			name: "full paper with all fields",
			pub: &Publication{
				ID:          testBibPubID,
				Source:      SourceArXiv,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
				Authors:     []Author{{Name: testBibAuthor1}, {Name: testBibAuthor2}},
				Published:   testBibDate,
				Abstract:    testBibAbstract,
				URL:         testBibURL,
				DOI:         testBibDOI,
				ArXivID:     testBibArXivID,
				Categories:  []string{testBibCategory1, testBibCategory2},
				SourceMetadata: map[string]any{
					bibtexMetaKeyJournal: testBibJournal,
				},
			},
			wantPrefix: bibtexEntryArticle + bibtexEntryOpen,
			wantFields: []string{
				testBibAuthor1 + bibtexAuthorSeparator + testBibAuthor2,
				testBibTitle,
				testBibCiteYear,
				testBibCiteMonth,
				testBibDOI,
				testBibURL,
				testBibAbstract,
				testBibCategory1 + bibtexKeywordSeparator + testBibCategory2,
				testBibArXivID,
				bibtexArchivePrefixArXiv,
				testBibCategory1,
				bibtexNotePrefix + SourceArXiv,
				testBibJournal,
			},
		},
		{
			name: "paper without optional fields",
			pub: &Publication{
				ID:          testBibS2ID,
				Source:      SourceS2,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
				Authors:     []Author{{Name: testBibAuthor1}},
				Published:   testBibDate,
				URL:         testBibURL,
			},
			wantPrefix: bibtexEntryArticle + bibtexEntryOpen,
			wantFields: []string{
				testBibAuthor1,
				testBibTitle,
				testBibCiteYear,
			},
			wantAbsent: []string{
				bibtexFieldDOI + bibtexFieldAssign,
				bibtexFieldEprint + bibtexFieldAssign,
				bibtexFieldAbstract + bibtexFieldAssign,
				bibtexFieldKeywords + bibtexFieldAssign,
				bibtexFieldJournal + bibtexFieldAssign,
			},
		},
		{
			name: "missing authors uses default",
			pub: &Publication{
				ID:          testBibPubID,
				Source:      SourceArXiv,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
				Published:   testBibDate,
			},
			wantFields: []string{bibtexDefaultAuthor},
		},
		{
			name: "authors with empty names uses default",
			pub: &Publication{
				ID:          testBibPubID,
				Source:      SourceArXiv,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
				Published:   testBibDate,
				Authors:     []Author{{Name: ""}, {Name: "  "}},
			},
			wantFields: []string{bibtexDefaultAuthor},
		},
		{
			name: "missing date uses default year",
			pub: &Publication{
				ID:          testBibPubID,
				Source:      SourceArXiv,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
			},
			wantFields: []string{bibtexDefaultYear},
		},
		{
			name: "year-only date has no month",
			pub: &Publication{
				ID:          testBibPubID,
				Source:      SourceArXiv,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
				Published:   "2024",
			},
			wantFields: []string{testBibCiteYear},
			wantAbsent: []string{bibtexFieldMonth + bibtexFieldAssign},
		},
		{
			name: "model uses @misc entry type",
			pub: &Publication{
				ID:          testBibHFModelID,
				Source:      SourceHuggingFace,
				ContentType: ContentTypeModel,
				Title:       "BERT Base Uncased",
				Authors:     []Author{{Name: "Google Research"}},
				Published:   testBibDate,
				URL:         "https://huggingface.co/bert-base-uncased",
			},
			wantPrefix: bibtexEntryMisc + bibtexEntryOpen,
			wantFields: []string{
				"BERT Base Uncased",
				"Google Research",
				bibtexNotePrefix + SourceHuggingFace,
			},
		},
		{
			name: "dataset uses @misc entry type",
			pub: &Publication{
				ID:          testBibDatasetID,
				Source:      SourceHuggingFace,
				ContentType: ContentTypeDataset,
				Title:       "SQuAD v2",
				Authors:     []Author{{Name: "Rajpurkar et al."}},
				Published:   "2018-06-11",
				URL:         "https://huggingface.co/datasets/squad",
			},
			wantPrefix: bibtexEntryMisc + bibtexEntryOpen,
		},
		{
			name: "special characters in title are escaped",
			pub: &Publication{
				ID:          testBibPubID,
				Source:      SourceArXiv,
				ContentType: ContentTypePaper,
				Title:       testBibSpecialStr,
				Published:   testBibDate,
			},
			wantFields: []string{
				`\&`,
				`\%`,
				`\#`,
				`\_`,
				`\~{}`,
				`\^{}`,
			},
		},
		{
			name: "arxiv fields included when arxiv_id present",
			pub: &Publication{
				ID:          testBibPubID,
				Source:      SourceArXiv,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
				Published:   testBibDate,
				ArXivID:     testBibArXivID,
				Categories:  []string{testBibCategory1},
			},
			wantFields: []string{
				bibtexFieldEprint + bibtexFieldAssign + testBibArXivID,
				bibtexFieldArchivePrefix + bibtexFieldAssign + bibtexArchivePrefixArXiv,
				bibtexFieldPrimaryClass + bibtexFieldAssign + testBibCategory1,
			},
		},
		{
			name:    "nil publication returns error",
			pub:     nil,
			wantErr: true,
		},
		{
			name: "empty title returns error",
			pub: &Publication{
				ID:     testBibPubID,
				Source: SourceArXiv,
				Title:  "",
			},
			wantErr: true,
		},
		{
			name: "whitespace-only title returns error",
			pub: &Publication{
				ID:     testBibPubID,
				Source: SourceArXiv,
				Title:  "   ",
			},
			wantErr: true,
		},
		{
			name: "single author no separator",
			pub: &Publication{
				ID:          testBibPubID,
				Source:      SourceArXiv,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
				Authors:     []Author{{Name: testBibAuthor1}},
				Published:   testBibDate,
			},
			wantFields: []string{testBibAuthor1},
			wantAbsent: []string{bibtexAuthorSeparator},
		},
		{
			name: "no categories omits keywords and primaryclass",
			pub: &Publication{
				ID:          testBibPubID,
				Source:      SourceArXiv,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
				Published:   testBibDate,
				ArXivID:     testBibArXivID,
			},
			wantAbsent: []string{
				bibtexFieldKeywords + bibtexFieldAssign,
				bibtexFieldPrimaryClass + bibtexFieldAssign,
			},
			wantFields: []string{
				bibtexFieldEprint + bibtexFieldAssign + testBibArXivID,
			},
		},
		{
			name: "pubmed source in note",
			pub: &Publication{
				ID:          testBibPMID,
				Source:      SourcePubMed,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
				Published:   testBibDate,
			},
			wantFields: []string{bibtexNotePrefix + SourcePubMed},
		},
		{
			name: "europmc source in note",
			pub: &Publication{
				ID:          testBibEMCID,
				Source:      SourceEuropePMC,
				ContentType: ContentTypePaper,
				Title:       testBibTitle,
				Published:   testBibDate,
			},
			wantFields: []string{bibtexNotePrefix + SourceEuropePMC},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := GenerateBibTeX(tc.pub)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrBibTeXGeneration)
				assert.Empty(t, result)
				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, result)

			if tc.wantPrefix != "" {
				assert.True(t, strings.HasPrefix(result, tc.wantPrefix),
					"expected prefix %q, got %q", tc.wantPrefix, result[:min(len(tc.wantPrefix)+10, len(result))])
			}

			for _, field := range tc.wantFields {
				assert.Contains(t, result, field, "expected field %q in BibTeX output", field)
			}

			for _, absent := range tc.wantAbsent {
				assert.NotContains(t, result, absent, "unexpected content %q in BibTeX output", absent)
			}

			// All outputs must end with closing brace + newline.
			assert.True(t, strings.HasSuffix(result, bibtexEntryClose+"\n"),
				"expected output to end with %q", bibtexEntryClose+"\n")
		})
	}
}

// ---------------------------------------------------------------------------
// TestGenerateCiteKey
// ---------------------------------------------------------------------------

func TestGenerateCiteKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pub  *Publication
		want string
	}{
		{
			name: "normal case",
			pub: &Publication{
				Authors:   []Author{{Name: testBibAuthor1}},
				Published: testBibDate,
				Title:     testBibTitle,
			},
			want: "smith2024attention",
		},
		{
			name: "title starting with stop word",
			pub: &Publication{
				Authors:   []Author{{Name: "John Doe"}},
				Published: "2023-01-01",
				Title:     "The Impact of Climate",
			},
			want: "doe2023impact",
		},
		{
			name: "no author uses year and title word",
			pub: &Publication{
				ID:        "arxiv:2401.99999",
				Source:    SourceArXiv,
				Published: testBibDate,
				Title:     testBibTitle,
			},
			want: "2024attention",
		},
		{
			name: "no date omits year",
			pub: &Publication{
				Authors: []Author{{Name: testBibAuthor1}},
				Title:   testBibTitle,
			},
			want: "smithattention",
		},
		{
			name: "special chars in author name",
			pub: &Publication{
				Authors:   []Author{{Name: "José García-López"}},
				Published: "2022-03-10",
				Title:     "Neural Networks",
			},
			want: "garcíalópez2022neural",
		},
		{
			name: "single-word author",
			pub: &Publication{
				Authors:   []Author{{Name: "Aristotle"}},
				Published: "2024-01-01",
				Title:     "On Logic",
			},
			want: "aristotle2024logic",
		},
		{
			name: "all stop words title uses first word",
			pub: &Publication{
				Authors:   []Author{{Name: "Test Author"}},
				Published: "2024-01-01",
				Title:     "The A An",
			},
			want: "author2024the",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := generateCiteKey(tc.pub)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestEscapeBibTeX
// ---------------------------------------------------------------------------

func TestEscapeBibTeX(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"ampersand", "A & B", `A \& B`},
		{"percent", "100%", `100\%`},
		{"hash", "#1", `\#1`},
		{"underscore", "a_b", `a\_b`},
		{"tilde", "~user", `\~{}user`},
		{"caret", "x^2", `x\^{}2`},
		{"multiple specials", "a & b % c # d _ e ~ f ^ g", `a \& b \% c \# d \_ e \~{} f \^{} g`},
		{"no specials", "plain text", "plain text"},
		{"empty string", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := escapeBibTeX(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestExtractLastName
// ---------------------------------------------------------------------------

func TestExtractLastName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"two-part name", "Alice Smith", "Smith"},
		{"single name", "Aristotle", "Aristotle"},
		{"three-part name", "Jean-Pierre Dupont", "Dupont"},
		{"name with extra spaces", "  Alice   Smith  ", "Smith"},
		{"empty string", "", ""},
		{"whitespace only", "   ", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractLastName(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestFirstSignificantWord
// ---------------------------------------------------------------------------

func TestFirstSignificantWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"starts with content word", "Attention Is All You Need", "attention"},
		{"starts with stop word", "The Impact of Climate Change", "impact"},
		{"multiple stop words", "A Study on the Effects of Training", "study"},
		{"single word", "Transformers", "transformers"},
		{"empty string", "", ""},
		{"only stop words", "the a an", "the"},
		{"special chars removed", "Self-Attention Mechanisms", "selfattention"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := firstSignificantWord(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestBibTeXEntryType
// ---------------------------------------------------------------------------

func TestBibTeXEntryType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType ContentType
		want        string
	}{
		{"paper", ContentTypePaper, bibtexEntryArticle},
		{"model", ContentTypeModel, bibtexEntryMisc},
		{"dataset", ContentTypeDataset, bibtexEntryMisc},
		{"any", ContentTypeAny, bibtexEntryMisc},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bibtexEntryType(tc.contentType)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestBibTeXMonth
// ---------------------------------------------------------------------------

func TestBibTeXMonth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		published string
		want      string
	}{
		{"january", "2024-01-15", "jan"},
		{"june", "2024-06-01", "jun"},
		{"december", "2024-12-31", "dec"},
		{"year only", "2024", ""},
		{"empty", "", ""},
		{"partial", "2024-0", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bibtexMonth(tc.published)
			assert.Equal(t, tc.want, got)
		})
	}
}
