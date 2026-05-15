package internal

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Result fat-struct — Cycle 2 task #10 (plan §3.3).
//
// Status: SHIPPING. v1.6.0 introduces this as the v2 wire shape, accessible
// via Client.SearchV2 and the rtv_search compat:"v2" MCP arg. Default
// Search() / rtv_search calls keep returning v1 Publication shape so
// existing consumers stay byte-stable; v1 sunset announced for v2.0.0.
//
// Result is a fat struct rather than a sealed interface (ux-advocate's
// recommendation in cycle-1 design): Go consumers + LLM agents both get a
// single access pattern (`r.Kind` then optional `r.Paper / r.Web / ...`),
// no type assertions, no kind-switch ceremony.
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

	// v3 multimodal kinds (cycle 2+ / v2.3.0+).
	KindVideo ResultKind = "video"
	KindPlace ResultKind = "place"
	KindImage ResultKind = "image"
	KindPost  ResultKind = "post"

	// v5 cycle 1 / v2.8.0 — Q&A (Stack Exchange, Hacker News).
	KindQA ResultKind = "qa"

	// v5 cycle 3 / v2.10.0 — structured facts (Wikidata).
	KindFact ResultKind = "fact"
)

// IsValidResultKind returns true if the given string is a known kind.
func IsValidResultKind(k string) bool {
	switch ResultKind(k) {
	case KindPaper, KindModel, KindDataset, KindWeb, KindNews, KindCode, KindEncyclopedia,
		KindVideo, KindPlace, KindImage, KindPost, KindQA, KindFact:
		return true
	}
	return false
}

// Result is the v2 unified search/get return type spanning paper, model,
// dataset, web, news, code, and encyclopedia content. Consumers always
// check Kind before dereferencing the kind-specific pointer block.
type Result struct {
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

	// Cross-kind signal.
	Stars *int `json:"stars,omitempty"`

	// Kind-specific blocks. Exactly one is populated for a given Result.
	Paper        *PaperData        `json:"paper,omitempty"`
	Model        *ModelData        `json:"model,omitempty"`
	Dataset      *DatasetData      `json:"dataset,omitempty"`
	Web          *WebData          `json:"web,omitempty"`
	News         *NewsData         `json:"news,omitempty"`
	Code         *CodeData         `json:"code,omitempty"`
	Encyclopedia *EncyclopediaData `json:"encyclopedia,omitempty"`

	// v3 multimodal blocks (cycle 2+ / v2.3.0+).
	Video *VideoData `json:"video,omitempty"`
	Place *PlaceData `json:"place,omitempty"`
	Image *ImageData `json:"image,omitempty"`
	Post  *PostData  `json:"post,omitempty"`

	// v5 cycle 1 / v2.8.0 — Q&A block.
	QA *QAData `json:"qa,omitempty"`

	// Raw provider response (opt-in via WithIncludeRaw).
	Raw json.RawMessage `json:"raw,omitempty"`

	// Provenance tags (one per source that contributed).
	Provenance []ProvenanceTag `json:"provenance,omitempty"`
}

// MergedSearchResultV2 is the v2 wire shape of a merged search response.
// Mirrors MergedSearchResult one-to-one with []Publication replaced by
// []Result. v1 callers continue using MergedSearchResult; v2 callers opt
// in via Client.SearchV2 or rtv_search with compat:"v2".
type MergedSearchResultV2 struct {
	TotalResults   int        `json:"total_results"`
	Results        []Result   `json:"results"`
	SourcesQueried []string   `json:"sources_queried"`
	SourcesFailed  []string   `json:"sources_failed"`
	SourcesSkipped []SkipNote `json:"sources_skipped,omitempty"`
	AuditRef       string     `json:"audit_ref,omitempty"`
	FallbackWalked bool       `json:"fallback_walked,omitempty"`
	EUFallbackUsed bool       `json:"eu_fallback_used,omitempty"`
	HasMore        bool       `json:"has_more"`
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

// VideoData carries video-specific fields (YouTube, etc.). Cycle 2 / v2.3.0.
// Top-level Result.URL holds the watch URL; Title, Authors (channel name as
// Author.Name), Published, Language stay on Result. ThumbnailURL and
// DurationSeconds live here so consumers can render previews without
// re-fetching.
type VideoData struct {
	ChannelName     string `json:"channel_name,omitempty"`
	ChannelID       string `json:"channel_id,omitempty"`
	VideoID         string `json:"video_id,omitempty"` // platform-native, e.g. YouTube videoId
	ThumbnailURL    string `json:"thumbnail_url,omitempty"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`
	ViewCount       *int   `json:"view_count,omitempty"`
	LikeCount       *int   `json:"like_count,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
	LiveBroadcast   string `json:"live_broadcast,omitempty"` // "none" | "live" | "upcoming"
}

// PlaceData carries geographic-place fields (Photon, Nominatim, TomTom).
// Cycle 3 / v2.4.0. Top-level Result.Title holds the place name; Lat/Lon
// duplicate the Publication v3 fields for callers that consume Result
// without round-tripping through Publication.
type PlaceData struct {
	OSMID       string   `json:"osm_id,omitempty"`   // composite "<type>:<id>" e.g. "node:240109189"
	OSMType     string   `json:"osm_type,omitempty"` // node | way | relation
	Lat         float64  `json:"lat,omitempty"`
	Lon         float64  `json:"lon,omitempty"`
	Address     string   `json:"address,omitempty"` // single-line formatted
	Country     string   `json:"country,omitempty"`
	CountryCode string   `json:"country_code,omitempty"` // ISO 3166-1 alpha-2
	City        string   `json:"city,omitempty"`
	State       string   `json:"state,omitempty"`
	Postcode    string   `json:"postcode,omitempty"`
	Street      string   `json:"street,omitempty"`
	HouseNumber string   `json:"house_number,omitempty"`
	Categories  []string `json:"categories,omitempty"` // POI categories (TomTom)
	PlaceType   string   `json:"place_type,omitempty"` // city | street | poi | building | ...
	Importance  float64  `json:"importance,omitempty"` // 0-1 ranking signal (Nominatim/Photon)
}

// ImageData carries image-specific fields (Wikimedia, Europeana, Brave).
// Cycle 4 / v2.5.0. License is first-class because openly-licensed image
// reuse REQUIRES attribution + license tracking to be safe — consumers
// should refuse to use an image with no License populated.
type ImageData struct {
	MediaURL     string `json:"media_url,omitempty"`     // full-resolution URL
	ThumbnailURL string `json:"thumbnail_url,omitempty"` // preview URL (smaller)
	MediaMime    string `json:"media_mime,omitempty"`    // image/jpeg, image/png, image/svg+xml, ...
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	License      string `json:"license,omitempty"`     // human-readable, e.g. "CC BY-SA 3.0"
	LicenseURL   string `json:"license_url,omitempty"` // canonical license page
	Artist       string `json:"artist,omitempty"`      // attribution / author
	SourcePage   string `json:"source_page,omitempty"` // page describing the image (provider-side)
}

// PostData carries social-post-specific fields (Mastodon, Bluesky, Reddit).
// Cycle 5 / v2.6.0. EngagementScore on the parent Publication carries a
// normalized sum (likes+reposts+replies) per provider; per-component
// breakdown lives here.
type PostData struct {
	AuthorHandle string `json:"author_handle,omitempty"` // @alice@mastodon.social, alice.bsky.social, u/alice
	AuthorURL    string `json:"author_url,omitempty"`
	AtprotoURI   string `json:"atproto_uri,omitempty"`  // Bluesky-specific
	PlatformURL  string `json:"platform_url,omitempty"` // canonical post URL on the platform
	LikeCount    *int   `json:"like_count,omitempty"`
	RepostCount  *int   `json:"repost_count,omitempty"`
	ReplyCount   *int   `json:"reply_count,omitempty"`
	PublishedAt  string `json:"published_at,omitempty"` // RFC3339 (full precision)
	MediaCount   int    `json:"media_count,omitempty"`
	Subreddit    string `json:"subreddit,omitempty"` // Reddit-specific
	Instance     string `json:"instance,omitempty"`  // Mastodon-specific (hostname)
	Verified     bool   `json:"verified,omitempty"`
}

// QAData carries Q&A-specific fields (Stack Exchange, Hacker News).
// v5 cycle 1 / v2.8.0. Site discriminates per-network sub-sites (Stack
// Exchange's "stackoverflow", "serverfault", "askubuntu", ...; HN uses
// "hackernews"). QuestionID + Site form the cross-source dedup key.
type QAData struct {
	Site             string   `json:"site,omitempty"`               // stackoverflow | hackernews | ...
	QuestionID       string   `json:"question_id,omitempty"`        // provider-native ID
	Tags             []string `json:"tags,omitempty"`               // SE tags / HN _tags filter
	AnswerCount      *int     `json:"answer_count,omitempty"`       // SE answer count; HN num_comments
	AcceptedAnswerID string   `json:"accepted_answer_id,omitempty"` // SE accepted_answer_id (HN unused)
	Score            *int     `json:"score,omitempty"`              // SE question score / HN points
	IsAnswered       *bool    `json:"is_answered,omitempty"`        // SE is_answered (HN unused)
	AuthorHandle     string   `json:"author_handle,omitempty"`      // SE owner display_name / HN author
}

// EncyclopediaData carries reference-style article fields (Wikipedia, etc.).
type EncyclopediaData struct {
	Article   string   `json:"article,omitempty"`
	Sections  []string `json:"sections,omitempty"`
	Languages []string `json:"languages,omitempty"`
	Revision  string   `json:"revision,omitempty"`
}

// ScoreBreakdown decomposes the final Score by signal source. Cycle 2
// populates only Lexical (per-source rank) when the converter runs. Cycle 3
// rerank stage fills Reranker.
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
