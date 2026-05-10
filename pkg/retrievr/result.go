package retrievr

import "github.com/itsatony/retrievr-mcp/internal"

// ---------------------------------------------------------------------------
// Result fat-struct re-exports — Cycle 2 (v1.6.0).
//
// The canonical types live in internal so Router can produce them. This
// file is the public surface for external Go consumers. v1 Publication
// shape continues to exist alongside; the MCP rtv_search tool defaults
// to v1 for compatibility, with compat:"v2" as the opt-in for new shape.
// ---------------------------------------------------------------------------

// ResultKind discriminates the per-kind data block populated on a Result.
type ResultKind = internal.ResultKind

// ResultKind constants.
const (
	KindPaper        = internal.KindPaper
	KindModel        = internal.KindModel
	KindDataset      = internal.KindDataset
	KindWeb          = internal.KindWeb
	KindNews         = internal.KindNews
	KindCode         = internal.KindCode
	KindEncyclopedia = internal.KindEncyclopedia
)

// IsValidResultKind returns true if the given string is a known kind.
func IsValidResultKind(k string) bool { return internal.IsValidResultKind(k) }

// Result is the v2 unified search/get return type.
type Result = internal.Result

// MergedSearchResultV2 is the v2 wire shape returned by Client.SearchV2.
type MergedSearchResultV2 = internal.MergedSearchResultV2

// Per-kind data blocks.
type (
	PaperData        = internal.PaperData
	WebData          = internal.WebData
	NewsData         = internal.NewsData
	CodeData         = internal.CodeData
	ModelData        = internal.ModelData
	DatasetData      = internal.DatasetData
	EncyclopediaData = internal.EncyclopediaData
)

// Auxiliary types.
type (
	ScoreBreakdown = internal.ScoreBreakdown
	ProvenanceTag  = internal.ProvenanceTag
	SkipNote       = internal.SkipNote
)
