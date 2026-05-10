package retrievr

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// ResultKind discriminator
// ---------------------------------------------------------------------------

// ResultKind discriminates the per-kind data block populated on a Result.
type ResultKind string

// ResultKind constants.
const (
	KindPaper        ResultKind = "paper"
	KindModel        ResultKind = "model"
	KindDataset      ResultKind = "dataset"
	KindWeb          ResultKind = "web"
	KindNews         ResultKind = "news"
	KindCode         ResultKind = "code"
	KindEncyclopedia ResultKind = "encyclopedia"
)

// ---------------------------------------------------------------------------
// Result fat struct
//
// Cycle-1 status: declared but not yet populated by plugins. Plugins
// continue to emit Publication through internal.Router; cycle 2 introduces
// converters and switches the public API to []Result.
// ---------------------------------------------------------------------------

// Result is the unified search/get return type spanning paper, model, dataset,
// web, news, code, and encyclopedia content. Consumers always check Kind
// before dereferencing the kind-specific pointer block.
type Result struct {
	// Discriminator — always set.
	Kind ResultKind `json:"kind"`

	// Core identity.
	ID          string   `json:"id"`
	Source      string   `json:"source"`
	AlsoFoundIn []string `json:"also_found_in,omitempty"`

	// Core content.
	Title    string `json:"title"`
	URL      string `json:"url,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
	Abstract string `json:"abstract,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Language string `json:"language,omitempty"`

	// Metadata.
	Authors   []Author `json:"authors,omitempty"`
	Published string   `json:"published,omitempty"`
	Updated   string   `json:"updated,omitempty"`
	License   string   `json:"license,omitempty"`

	// Ranking + LLM hints.
	Score      float64         `json:"score,omitempty"`
	ScoreParts *ScoreBreakdown `json:"score_parts,omitempty"`
	LLMContext string          `json:"llm_context,omitempty"`

	// Cross-kind signal that surfaces at top level (e.g. HF model + GitHub repo
	// stars are conceptually the same engagement metric).
	Stars *int `json:"stars,omitempty"`

	// Kind-specific blocks. Exactly one is populated for a given Result.
	Paper        *PaperData        `json:"paper,omitempty"`
	Model        *ModelData        `json:"model,omitempty"`
	Dataset      *DatasetData      `json:"dataset,omitempty"`
	Web          *WebData          `json:"web,omitempty"`
	News         *NewsData         `json:"news,omitempty"`
	Code         *CodeData         `json:"code,omitempty"`
	Encyclopedia *EncyclopediaData `json:"encyclopedia,omitempty"`

	// Raw provider response (opt-in via WithIncludeRaw).
	Raw json.RawMessage `json:"raw,omitempty"`

	// Provenance tags (one per source that contributed to this merged result).
	Provenance []ProvenanceTag `json:"provenance,omitempty"`
}

// ---------------------------------------------------------------------------
// Per-kind data blocks
// ---------------------------------------------------------------------------

// PaperData carries scholarly-paper-specific fields.
type PaperData struct {
	DOI           string      `json:"doi,omitempty"`
	ArXivID       string      `json:"arxiv_id,omitempty"`
	Categories    []string    `json:"categories,omitempty"`
	CitationCount *int        `json:"citation_count,omitempty"`
	PDFURL        string      `json:"pdf_url,omitempty"`
	OpenAccess    *bool       `json:"open_access,omitempty"`
	References    []Reference `json:"references,omitempty"`
	Citations     []Reference `json:"citations,omitempty"`
	Venue         string      `json:"venue,omitempty"`
}

// WebData carries general-web-page-specific fields.
type WebData struct {
	Favicon     string `json:"favicon,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	ReadingMins int    `json:"reading_mins,omitempty"`
}

// NewsData carries news-article-specific fields.
type NewsData struct {
	Outlet      string `json:"outlet,omitempty"`
	Section     string `json:"section,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
}

// CodeData carries source-code-specific fields (GitHub, etc.).
type CodeData struct {
	Repo       string   `json:"repo,omitempty"`
	Path       string   `json:"path,omitempty"`
	Language   string   `json:"language,omitempty"`
	LineFrom   int      `json:"line_from,omitempty"`
	LineTo     int      `json:"line_to,omitempty"`
	SHA        string   `json:"sha,omitempty"`
	Stars      *int     `json:"stars,omitempty"`
	License    string   `json:"license,omitempty"`
	Topics     []string `json:"topics,omitempty"`
	Forks      *int     `json:"forks,omitempty"`
	LastCommit string   `json:"last_commit,omitempty"`
}

// ModelData carries ML-model-specific fields (HuggingFace, etc.).
type ModelData struct {
	Architecture string   `json:"architecture,omitempty"`
	Parameters   string   `json:"parameters,omitempty"`
	Downloads    *int     `json:"downloads,omitempty"`
	Likes        *int     `json:"likes,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	BaseModel    string   `json:"base_model,omitempty"`
}

// DatasetData carries dataset-specific fields.
type DatasetData struct {
	Rows      *int     `json:"rows,omitempty"`
	SizeBytes *int64   `json:"size_bytes,omitempty"`
	Tasks     []string `json:"tasks,omitempty"`
	Modality  []string `json:"modality,omitempty"`
	License   string   `json:"license,omitempty"`
}

// EncyclopediaData carries reference-style article fields (Wikipedia, etc.).
type EncyclopediaData struct {
	Article   string   `json:"article,omitempty"`
	Sections  []string `json:"sections,omitempty"`
	Languages []string `json:"languages,omitempty"`
	Revision  string   `json:"revision,omitempty"`
}

// ScoreBreakdown decomposes the final Score by signal source.
type ScoreBreakdown struct {
	Lexical   float64 `json:"lexical,omitempty"`
	Semantic  float64 `json:"semantic,omitempty"`
	Authority float64 `json:"authority,omitempty"`
	Recency   float64 `json:"recency,omitempty"`
	Reranker  float64 `json:"reranker,omitempty"`
}

// ProvenanceTag records which source contributed a result and under what
// residency/DPA classification at fetch time.
type ProvenanceTag struct {
	Source    string    `json:"source"`
	Region    string    `json:"region"`
	DPAStatus string    `json:"dpa_status,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}
