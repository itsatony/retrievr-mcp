package internal

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerateBibTeXUnsupportedContentTypes covers the v3 multimodal guard
// added in cycle 1 / v2.2.0. video / place / image / post have no meaningful
// BibTeX representation, so GenerateBibTeX returns ErrBibTeXUnsupported
// rather than emitting a misleading @misc entry.
func TestGenerateBibTeXUnsupportedContentTypes(t *testing.T) {
	t.Parallel()
	unsupported := []ContentType{
		ContentTypeVideo,
		ContentTypePlace,
		ContentTypeImage,
		ContentTypePost,
	}
	for _, ct := range unsupported {
		ct := ct
		t.Run(string(ct), func(t *testing.T) {
			t.Parallel()
			pub := &Publication{
				ID:          "x:1",
				Source:      "x",
				Title:       "Sample",
				ContentType: ct,
			}
			out, err := GenerateBibTeX(pub)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrBibTeXUnsupported), "expected ErrBibTeXUnsupported, got %v", err)
			assert.Empty(t, out)
		})
	}
}

// TestGenerateBibTeXSupportedContentTypes confirms the v3 guard does NOT
// break paper / model / dataset / empty ContentType — they continue to emit
// a BibTeX entry as in v2.
func TestGenerateBibTeXSupportedContentTypes(t *testing.T) {
	t.Parallel()
	supported := []ContentType{
		"", // legacy: unset ContentType still produces @misc
		ContentTypePaper,
		ContentTypeModel,
		ContentTypeDataset,
	}
	for _, ct := range supported {
		ct := ct
		t.Run(string(ct), func(t *testing.T) {
			t.Parallel()
			pub := &Publication{
				ID:          "x:1",
				Source:      "x",
				Title:       "Sample",
				ContentType: ct,
				Authors:     []Author{{Name: "Doe, J."}},
				Published:   "2024-01-15",
			}
			out, err := GenerateBibTeX(pub)
			require.NoError(t, err)
			assert.NotEmpty(t, out)
		})
	}
}
