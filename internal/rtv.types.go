package internal

import (
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Content type constants
// ---------------------------------------------------------------------------

// ContentType represents the type of content a source provides.
type ContentType string

const (
	ContentTypePaper   ContentType = "paper"
	ContentTypeModel   ContentType = "model"
	ContentTypeDataset ContentType = "dataset"
	ContentTypeAny     ContentType = "any"

	// v3 multimodal additions (cycle 1 / v2.2.0). Each new ContentType
	// has its own dedup-key family in dedup() — cross-class dedup is
	// intentionally disabled (a video about a place is not a duplicate
	// of the place result).
	ContentTypeVideo ContentType = "video"
	ContentTypePlace ContentType = "place"
	ContentTypeImage ContentType = "image"
	ContentTypePost  ContentType = "post"

	// v5 cycle 4 / v2.11.0 — code-package registries (npm, PyPI, crates,
	// pkg.go.dev). Dedup keyed by SourceMetadata["package_id"] formatted
	// as "<ecosystem>:<name>" (e.g. "npm:react", "pypi:requests") so
	// same-name packages across ecosystems never collide.
	ContentTypePackage ContentType = "package"

	// v5 cycle 5 / v2.12.0 — patent records (Google Patents, EPO OPS).
	// Dedup keyed by SourceMetadata["patent_number"] (publication number,
	// e.g. "EP3456789A1", "US2023123456"). Court decisions and EU
	// regulations stay on ContentTypePaper with Result.Kind = KindLaw and
	// dedup on SourceMetadata["citation_code"].
	ContentTypePatent ContentType = "patent"

	// v6 cycle 2 / v2.15.0 — audio (podcast episodes, podcast shows).
	// Dedup keyed by SourceMetadata["audio_id"] formatted as
	// "<provider>:<external_id>" (e.g. "listennotes:abc123",
	// "itunes:1234567890") so same-id episodes across providers never
	// collide by construction.
	ContentTypeAudio ContentType = "audio"
)

// IsValidContentType returns true if the given string maps to a known
// ContentType. The empty string is intentionally NOT valid — callers must
// opt in. ContentTypeAny is treated as a wildcard match for routing but is
// still a valid value here.
func IsValidContentType(ct string) bool {
	switch ContentType(ct) {
	case ContentTypePaper, ContentTypeModel, ContentTypeDataset, ContentTypeAny,
		ContentTypeVideo, ContentTypePlace, ContentTypeImage, ContentTypePost,
		ContentTypePackage, ContentTypePatent, ContentTypeAudio:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Content format constants
// ---------------------------------------------------------------------------

// ContentFormat represents the format of content returned by a source.
type ContentFormat string

const (
	FormatNative   ContentFormat = "native"
	FormatJSON     ContentFormat = "json"
	FormatXML      ContentFormat = "xml"
	FormatMarkdown ContentFormat = "markdown"
	FormatBibTeX   ContentFormat = "bibtex"
)

// ---------------------------------------------------------------------------
// Include field constants
// ---------------------------------------------------------------------------

// IncludeField specifies what additional data to include in a get request.
type IncludeField string

const (
	IncludeAbstract   IncludeField = "abstract"
	IncludeFullText   IncludeField = "full_text"
	IncludeReferences IncludeField = "references"
	IncludeCitations  IncludeField = "citations"
	IncludeRelated    IncludeField = "related"
	IncludeMetadata   IncludeField = "metadata"
)

// ---------------------------------------------------------------------------
// Sort order constants
// ---------------------------------------------------------------------------

// SortOrder specifies the ordering of search results.
type SortOrder string

const (
	SortRelevance SortOrder = "relevance"
	SortDateDesc  SortOrder = "date_desc"
	SortDateAsc   SortOrder = "date_asc"
	SortCitations SortOrder = "citations"
)

// ---------------------------------------------------------------------------
// Source ID constants
// ---------------------------------------------------------------------------

const (
	SourceArXiv       = "arxiv"
	SourcePubMed      = "pubmed"
	SourceS2          = "s2"
	SourceOpenAlex    = "openalex"
	SourceHuggingFace = "huggingface"
	SourceEuropePMC   = "europmc"
	SourceCrossRef    = "crossref"
	SourceBioRxiv     = "biorxiv"
	SourceDBLP        = "dblp"
	SourceADS         = "ads"

	// Cycle-2 Wave-1 providers.
	SourceExa       = "exa"
	SourceBrave     = "brave"
	SourceLinkup    = "linkup"
	SourceFirecrawl = "firecrawl"
	SourceGitHub    = "github"
	SourceWikipedia = "wikipedia"
	SourceUnpaywall = "unpaywall"

	// Cycle-3 Wave-2 providers.
	SourcePerplexity = "perplexity"

	// v3 cycle 2 / v2.3.0 — video providers.
	SourceYouTube            = "youtube"
	SourceScrapingdogYouTube = "scrapingdog_youtube"

	// v3 cycle 3 / v2.4.0 — place providers.
	SourcePhoton    = "photon"
	SourceTomTom    = "tomtom"
	SourceNominatim = "nominatim"

	// v3 cycle 4 / v2.5.0 — image providers.
	// (Brave gains an image-search code path but is NOT a new source ID.)
	SourceWikimedia = "wikimedia"
	SourceEuropeana = "europeana"

	// v3 cycle 5 / v2.6.0 — social-post providers.
	SourceMastodon = "mastodon"
	SourceBluesky  = "bluesky"
	SourceReddit   = "reddit"

	// v5 cycle 1 / v2.8.0 — Q&A providers.
	SourceStackExchange = "stackexchange"
	SourceHackerNews    = "hackernews"

	// v5 cycle 2 / v2.9.0 — OpenScience aggregators.
	SourceZenodo   = "zenodo"
	SourceCORE     = "core"
	SourceOpenAIRE = "openaire"

	// v5 cycle 3 / v2.10.0 — Structured knowledge.
	SourceWikidata = "wikidata"
	SourceDataCite = "datacite"
	SourceORCID    = "orcid"

	// v5 cycle 4 / v2.11.0 — code-package registries.
	SourceNPM      = "npm"
	SourcePyPI     = "pypi"
	SourceCrates   = "crates"
	SourcePkgGoDev = "pkggodev"

	// v5 cycle 5 / v2.12.0 — patents + law.
	SourceGooglePatents = "googlepatents"
	SourceEPOOPS        = "epoops"
	SourceCourtListener = "courtlistener"
	SourceEURLex        = "eurlex"

	// v5 cycle 6 / v2.13.0 — temporal archives.
	SourceGDELT     = "gdelt"
	SourceIAScholar = "iascholar"
	SourceWayback   = "wayback"

	// v6 cycle 1 / v2.14.0 — premium geo / place.
	SourceGooglePlaces = "googleplaces"
	SourceOSMOverpass  = "osmoverpass"
	SourceHERE         = "here"

	// v6 cycle 2 / v2.15.0 — audio / podcast.
	SourceListenNotes = "listennotes"
	SourceITunes      = "itunes"

	// v6 cycle 3 / v2.16.0 — premium scholarly.
	SourceDimensions = "dimensions"
	SourceLens       = "lens"

	// v6 cycle 4 / v2.17.0 — premium web.
	SourceKagi    = "kagi"
	SourceMojeek  = "mojeek"
	SourceSerpAPI = "serpapi"

	// v6 cycle 5 / v2.18.0 — premium knowledge.
	SourceWolframAlpha = "wolframalpha"
	SourceKGAPI        = "kgapi"

	// v6 cycle 6 / v2.19.0 — premium news.
	SourceNewsAPI     = "newsapi"
	SourceSerpAPINews = "serpapinews"
)

// validSourceIDs is the internal immutable lookup set.
// Access via IsValidSourceID(). Wave-1 IDs are added incrementally as
// their respective plugin tasks land — keeps the TestPluginFactories
// "has_all_sources" invariant green between tasks #11–17.
var validSourceIDs = map[string]bool{
	SourceArXiv:       true,
	SourcePubMed:      true,
	SourceS2:          true,
	SourceOpenAlex:    true,
	SourceHuggingFace: true,
	SourceEuropePMC:   true,
	SourceCrossRef:    true,
	SourceBioRxiv:     true,
	SourceDBLP:        true,
	SourceADS:         true,
	// Wave-1 — added per task as plugins land.
	SourceExa:       true,
	SourceBrave:     true,
	SourceLinkup:    true,
	SourceFirecrawl: true,
	SourceGitHub:    true,
	SourceWikipedia: true,
	SourceUnpaywall: true,
	// Wave-2.
	SourcePerplexity: true,
	// v3 cycle 2 — video.
	SourceYouTube:            true,
	SourceScrapingdogYouTube: true,
	// v3 cycle 3 — place.
	SourcePhoton:    true,
	SourceTomTom:    true,
	SourceNominatim: true,
	// v3 cycle 4 — image.
	SourceWikimedia: true,
	SourceEuropeana: true,
	// v3 cycle 5 — social posts.
	SourceMastodon: true,
	SourceBluesky:  true,
	SourceReddit:   true,
	// v5 cycle 1 — Q&A.
	SourceStackExchange: true,
	SourceHackerNews:    true,
	// v5 cycle 2 — OpenScience aggregators.
	SourceZenodo:   true,
	SourceCORE:     true,
	SourceOpenAIRE: true,
	// v5 cycle 3 — Structured knowledge.
	SourceWikidata: true,
	SourceDataCite: true,
	SourceORCID:    true,
	// v5 cycle 4 — code-package registries.
	SourceNPM:      true,
	SourcePyPI:     true,
	SourceCrates:   true,
	SourcePkgGoDev: true,
	// v5 cycle 5 — patents + law.
	SourceGooglePatents: true,
	SourceEPOOPS:        true,
	SourceCourtListener: true,
	SourceEURLex:        true,
	// v5 cycle 6 — temporal archives.
	SourceGDELT:     true,
	SourceIAScholar: true,
	SourceWayback:   true,
	// v6 cycle 1 — premium geo.
	SourceGooglePlaces: true,
	SourceOSMOverpass:  true,
	SourceHERE:         true,
	// v6 cycle 2 — audio / podcast.
	SourceListenNotes: true,
	SourceITunes:      true,
	// v6 cycle 3 — premium scholarly.
	SourceDimensions: true,
	SourceLens:       true,
	// v6 cycle 4 — premium web.
	SourceKagi:    true,
	SourceMojeek:  true,
	SourceSerpAPI: true,
	// v6 cycle 5 — premium knowledge.
	SourceWolframAlpha: true,
	SourceKGAPI:        true,
	// v6 cycle 6 — premium news.
	SourceNewsAPI:     true,
	SourceSerpAPINews: true,
}

// IsValidSourceID returns true if the given ID is a known source.
func IsValidSourceID(id string) bool {
	return validSourceIDs[id]
}

// AllSourceIDs returns a fresh slice of all known source identifiers.
func AllSourceIDs() []string {
	ids := make([]string, 0, len(validSourceIDs))
	for id := range validSourceIDs {
		ids = append(ids, id)
	}
	return ids
}

// SourceCount is the number of known source plugins. Cycle-2 Wave-1
// increments this as each provider lands. v3 cycle 2 / v2.3.0 added
// SourceYouTube + SourceScrapingdogYouTube → 20. v3 cycle 3 / v2.4.0
// added Photon + TomTom + Nominatim → 23. v3 cycle 4 / v2.5.0 added
// Wikimedia + Europeana → 25 (Brave gained image search without
// becoming a new SourceID). v3 cycle 5 / v2.6.0 added Mastodon +
// Bluesky + Reddit → 28. v5 cycle 1 / v2.8.0 added StackExchange +
// HackerNews → 30. v5 cycle 2 / v2.9.0 added Zenodo + CORE + OpenAIRE → 33.
// v5 cycle 3 / v2.10.0 added Wikidata + DataCite + ORCID → 36.
// v5 cycle 4 / v2.11.0 added npm + PyPI + crates + pkg.go.dev → 40.
// v5 cycle 5 / v2.12.0 added Google Patents + EPO OPS + CourtListener +
// EUR-Lex → 44. v5 cycle 6 / v2.13.0 added GDELT + IA Scholar +
// Wayback → 47. v6 cycle 1 / v2.14.0 added Google Places + OSM
// Overpass + HERE → 50. v6 cycle 2 / v2.15.0 added Listen Notes +
// iTunes → 52. v6 cycle 3 / v2.16.0 added Dimensions + Lens → 54.
// v6 cycle 4 / v2.17.0 added Kagi + Mojeek + SerpAPI → 57.
// v6 cycle 5 / v2.18.0 added Wolfram Alpha + Google KG → 59.
// v6 cycle 6 / v2.19.0 added NewsAPI + SerpAPI News → 61 (closes v6).
const SourceCount = 61

// SourceMetadata key constants for v3 multimodal dedup keys. Plugins
// populate these on Publication.SourceMetadata so Router.dedup() can
// merge cross-source duplicates within a content class. Cross-class
// dedup is disabled — these keys are scoped by ContentType in dedup().
const (
	MetaKeyYouTubeID     = "youtube_id"     // video
	MetaKeyOSMID         = "osm_id"         // place
	MetaKeyWikimediaFile = "wikimedia_file" // image
	MetaKeyAtprotoURI    = "atproto_uri"    // post

	// v5 cycle 1 / v2.8.0 — Q&A dedup key. The value is the
	// provider-namespaced question identifier: "<site>:<question_id>"
	// (e.g. "stackoverflow:12345", "hackernews:9001234"). Cross-site
	// dedup is disabled by construction — Stack Overflow #1 and Server
	// Fault #1 are distinct questions.
	MetaKeyQAQuestionID = "qa_question_id"

	// v5 cycle 4 / v2.11.0 — code-package dedup key. The value is
	// "<ecosystem>:<name>" (e.g. "npm:react", "pypi:requests",
	// "crates:tokio", "pkggodev:github.com/go-chi/chi"). Same-name
	// packages across ecosystems never collide by construction.
	MetaKeyPackageID = "package_id"

	// Per-component package metadata keys (mirrors smetaQA* style).
	smetaPackageEcosystem = "package_ecosystem"
	smetaPackageName      = "package_name"
	smetaPackageVersion   = "package_version"
	smetaPackageDownloads = "package_downloads"
	smetaPackageRepoURL   = "package_repo_url"
	smetaPackageHomeURL   = "package_home_url"
	smetaPackageKeywords  = "package_keywords"
	smetaPackageScore     = "package_score"

	// v5 cycle 5 / v2.12.0 — patent + law dedup keys.
	//
	// MetaKeyPatentNumber namespaces a patent publication number (e.g.
	// "EP3456789A1", "US2023123456"). The router dedup family is
	// scoped to ContentTypePatent so a patent and a paper with the
	// same string never collide.
	MetaKeyPatentNumber = "patent_number"
	// MetaKeyCitationCode namespaces a court-decision or regulation
	// citation (e.g. "410 U.S. 113", "Regulation (EU) 2016/679").
	// Routed before DOI when present on a ContentTypePaper hit.
	MetaKeyCitationCode = "citation_code"

	// Per-component patent metadata keys (the dedup key MetaKeyPatentNumber
	// doubles as the canonical "patent_number" entry).
	smetaPatentAssignee     = "patent_assignee"
	smetaPatentInventors    = "patent_inventors"
	smetaPatentCPC          = "patent_cpc"
	smetaPatentJurisdiction = "patent_jurisdiction"
	smetaPatentKindCode     = "patent_kind_code"
	smetaPatentFilingDate   = "patent_filing_date"

	// Per-component law metadata keys.
	smetaLawCourt        = "law_court"
	smetaLawJurisdiction = "law_jurisdiction"
	smetaLawCitationCode = "law_citation_code"
	smetaLawDecisionDate = "law_decision_date"
	smetaLawDocketNumber = "law_docket_number"
	smetaLawCelex        = "law_celex"

	// v6 cycle 2 / v2.15.0 — audio dedup key. The value is
	// "<provider>:<external_id>" (e.g. "listennotes:abc123",
	// "itunes:1234567890"). Cross-provider matching happens only when
	// providers expose a shared RSS GUID; otherwise the namespace
	// keeps each plugin's hits distinct by construction.
	MetaKeyAudioID = "audio_id"

	// Per-component audio metadata keys.
	smetaAudioShowTitle       = "audio_show_title"
	smetaAudioEpisodeNumber   = "audio_episode_number"
	smetaAudioDurationSeconds = "audio_duration_seconds"
	smetaAudioPublisher       = "audio_publisher"
	smetaAudioExplicit        = "audio_explicit"
	smetaAudioAudioURL        = "audio_audio_url"
	smetaAudioImageURL        = "audio_image_url"
)

// ---------------------------------------------------------------------------
// Domain structs
// ---------------------------------------------------------------------------

// Publication is the unified result type across all sources.
type Publication struct {
	ID            string           `json:"id"`                       // Prefixed: "arxiv:2401.12345"
	Source        string           `json:"source"`                   // Primary source
	AlsoFoundIn   []string         `json:"also_found_in,omitempty"`  // Cross-source dedup tracking
	ContentType   ContentType      `json:"content_type"`             //nolint:tagliatelle
	Title         string           `json:"title"`                    //
	Authors       []Author         `json:"authors"`                  //
	Published     string           `json:"published"`                // YYYY-MM-DD
	Updated       string           `json:"updated,omitempty"`        //
	Abstract      string           `json:"abstract,omitempty"`       //
	URL           string           `json:"url"`                      //
	PDFURL        string           `json:"pdf_url,omitempty"`        //
	DOI           string           `json:"doi,omitempty"`            //
	ArXivID       string           `json:"arxiv_id,omitempty"`       // For cross-source dedup
	Categories    []string         `json:"categories,omitempty"`     //
	CitationCount *int             `json:"citation_count,omitempty"` // Pointer: nil when unknown
	FullText      *FullTextContent `json:"full_text,omitempty"`      //
	References    []Reference      `json:"references,omitempty"`     //
	Citations     []Reference      `json:"citations,omitempty"`      //
	Related       []Reference      `json:"related,omitempty"`        //
	License       string           `json:"license,omitempty"`        //

	// v3 multimodal fields (cycle 1 / v2.2.0). All optional; semantics
	// gated by ContentType:
	//   - video: ThumbnailURL, DurationSeconds, MediaURL, MediaMime, Language, EngagementScore
	//   - place: Lat, Lon, Address
	//   - image: ThumbnailURL, MediaURL, MediaMime, License
	//   - post:  EngagementScore, Language, ThumbnailURL (preview media)
	// Paper/model/dataset results leave these nil.
	ThumbnailURL    string   `json:"thumbnail_url,omitempty"`
	DurationSeconds *int     `json:"duration_seconds,omitempty"`
	Lat             *float64 `json:"lat,omitempty"`
	Lon             *float64 `json:"lon,omitempty"`
	Address         string   `json:"address,omitempty"`
	MediaURL        string   `json:"media_url,omitempty"`
	MediaMime       string   `json:"media_mime,omitempty"`
	EngagementScore *int     `json:"engagement_score,omitempty"`
	Language        string   `json:"language,omitempty"`

	// SourceMetadata holds source-specific key-value pairs that vary by plugin
	// (e.g., journal name, venue, volume, MeSH terms, youtube_id, osm_id,
	// wikimedia_file, atproto_uri). The value type is `any` because different
	// sources store strings, integers, booleans, and slices.
	SourceMetadata map[string]any `json:"source_metadata,omitempty"`
}

// Author represents a publication author.
type Author struct {
	Name        string `json:"name"`
	Affiliation string `json:"affiliation,omitempty"`
	ORCID       string `json:"orcid,omitempty"`
}

// Reference represents a cited or related publication.
type Reference struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title"`
	Year  int    `json:"year,omitempty"`
}

// FullTextContent holds retrieved content in its native or requested format.
type FullTextContent struct {
	Content       string        `json:"content"`
	ContentFormat ContentFormat `json:"content_format"`
	ContentLength int           `json:"content_length"`
	Truncated     bool          `json:"truncated"`
}

// ---------------------------------------------------------------------------
// Intent — declarative search-mode selector
//
// Cycle-1 task #4: Intent lets the caller declare what they're trying to do,
// letting Router pick the right primary source set + fallback chain instead
// of forcing the caller to enumerate sources. When unset, Router falls back
// to defaultSources (legacy behavior).
// ---------------------------------------------------------------------------

// Intent classifies the caller's high-level retrieval mode.
type Intent string

// Intent constants.
const (
	IntentDeepResearch   Intent = "deep_research"
	IntentQuickLookup    Intent = "quick_lookup"
	IntentPrimarySource  Intent = "primary_source"
	IntentCodeProvenance Intent = "code_provenance"
	IntentNews           Intent = "news"
	IntentReference      Intent = "reference"
)

// IsValidIntent returns true if the given string maps to a known Intent.
// The empty string is intentionally NOT valid — callers must opt in.
func IsValidIntent(i string) bool {
	switch Intent(i) {
	case IntentDeepResearch, IntentQuickLookup, IntentPrimarySource,
		IntentCodeProvenance, IntentNews, IntentReference:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Search types
// ---------------------------------------------------------------------------

// SearchParams contains all parameters for a search request.
type SearchParams struct {
	Query       string        `json:"query"`
	ContentType ContentType   `json:"content_type"`
	Filters     SearchFilters `json:"filters"`
	Sort        SortOrder     `json:"sort"`
	Limit       int           `json:"limit"`
	Offset      int           `json:"offset"`
	// Intent selects a primary source set + fallback chain via the
	// configured RouterFallbackConfig. When empty, Router uses
	// DefaultSources (legacy behavior). When set, an explicit Sources
	// argument to Router.Search still overrides intent-based resolution.
	Intent Intent `json:"intent,omitempty"`
}

// SearchFilters contains optional filters to narrow search results.
//
// v2.7.0 additions (smart filters): IncludeDomains / ExcludeDomains
// (honoured by brave, exa), Channels (honoured by youtube,
// scrapingdog_youtube), Subreddits (honoured by reddit), and Language
// (honoured by brave, youtube, scrapingdog_youtube, bluesky,
// europeana, and post-fetch by mastodon). Plugins that do not
// natively support a filter MUST ignore it — never post-filter
// silently. The single sanctioned post-filter is mastodon language;
// its capability is flagged via SourceCapabilities.SupportsLanguageFilter
// with the post-fetch policy documented in docs/filter-reference.md.
type SearchFilters struct {
	Title          string   `json:"title,omitempty"`
	Authors        []string `json:"authors,omitempty"`
	DateFrom       string   `json:"date_from,omitempty"` // YYYY-MM-DD or YYYY
	DateTo         string   `json:"date_to,omitempty"`   // YYYY-MM-DD or YYYY
	Categories     []string `json:"categories,omitempty"`
	OpenAccess     *bool    `json:"open_access,omitempty"`   // Pointer: nil = not set
	MinCitations   *int     `json:"min_citations,omitempty"` // Pointer: nil = not set
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
	Channels       []string `json:"channels,omitempty"`
	Subreddits     []string `json:"subreddits,omitempty"`
	Language       string   `json:"language,omitempty"` // BCP-47, e.g. "en", "de", "fr-CA"
}

// SearchResult is the per-plugin search return type.
type SearchResult struct {
	Total   int           `json:"total"`
	Results []Publication `json:"results"`
	HasMore bool          `json:"has_more"`
}

// MergedSearchResult is the router's merged, deduplicated search response.
type MergedSearchResult struct {
	TotalResults   int           `json:"total_results"`
	Results        []Publication `json:"results"`
	SourcesQueried []string      `json:"sources_queried"`
	SourcesFailed  []string      `json:"sources_failed"`
	HasMore        bool          `json:"has_more"`

	// Cycle-2 additions (EU mode + audit observability):

	// SourcesSkipped lists providers excluded from the fan-out by the
	// EU-mode gate, with a structured reason. Empty when no gating
	// occurred. Populated by Hook #2 of EU mode.
	SourcesSkipped []SkipNote `json:"sources_skipped,omitempty"`

	// AuditRef identifies the AuditEvent emitted for this call, so callers
	// can correlate the response to logs. Format: "evt_aud_<16hex>".
	AuditRef string `json:"audit_ref,omitempty"`

	// FallbackWalked is true when the fallback chain was walked beyond
	// the primary set (primary yielded zero or all-fail). Cycle-2 lift
	// of the existing per-intent walk into the response surface.
	FallbackWalked bool `json:"fallback_walked,omitempty"`

	// EUFallbackUsed is true when EUModePreferred fell back to a non-EU
	// provider after the EU set yielded zero / all-fail.
	EUFallbackUsed bool `json:"eu_fallback_used,omitempty"`
}

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

// CallCredentials carries per-call auth that overrides server config.
// Enables multi-tenant / multi-user operation.
type CallCredentials struct {
	PubMedAPIKey   string `json:"pubmed_api_key,omitempty"`
	S2APIKey       string `json:"s2_api_key,omitempty"`
	OpenAlexAPIKey string `json:"openalex_api_key,omitempty"`
	HFToken        string `json:"hf_token,omitempty"`
	ADSAPIKey      string `json:"ads_api_key,omitempty"`
}

// ResolveForSource returns the credential relevant to a given source ID.
// Falls back to serverDefault if the per-call value is empty.
func (c *CallCredentials) ResolveForSource(sourceID string, serverDefault string) string {
	if c == nil {
		return serverDefault
	}

	var perCall string
	switch sourceID {
	case SourcePubMed:
		perCall = c.PubMedAPIKey
	case SourceS2:
		perCall = c.S2APIKey
	case SourceOpenAlex:
		perCall = c.OpenAlexAPIKey
	case SourceHuggingFace:
		perCall = c.HFToken
	case SourceADS:
		perCall = c.ADSAPIKey
	}

	if perCall != "" {
		return perCall
	}
	return serverDefault
}

// ---------------------------------------------------------------------------
// Source capabilities and health
// ---------------------------------------------------------------------------

// SourceCapabilities reports what filtering, sorting, and features a source supports.
type SourceCapabilities struct {
	SupportsFullText         bool            `json:"supports_full_text"`
	SupportsCitations        bool            `json:"supports_citations"`
	SupportsDateFilter       bool            `json:"supports_date_filter"`
	SupportsAuthorFilter     bool            `json:"supports_author_filter"`
	SupportsCategoryFilter   bool            `json:"supports_category_filter"`
	SupportsSortRelevance    bool            `json:"supports_sort_relevance"`
	SupportsSortDate         bool            `json:"supports_sort_date"`
	SupportsSortCitations    bool            `json:"supports_sort_citations"`
	SupportsOpenAccessFilter bool            `json:"supports_open_access_filter"`
	SupportsDomainFilter     bool            `json:"supports_domain_filter"`
	SupportsChannelFilter    bool            `json:"supports_channel_filter"`
	SupportsLanguageFilter   bool            `json:"supports_language_filter"`
	SupportsPagination       bool            `json:"supports_pagination"`
	MaxResultsPerQuery       int             `json:"max_results_per_query"`
	CategoriesHint           string          `json:"categories_hint,omitempty"`
	NativeFormat             ContentFormat   `json:"native_format"`
	AvailableFormats         []ContentFormat `json:"available_formats"`
	// QueryIntents is the set of Intents this provider is reasonable to
	// dispatch for. Used informationally by Router for chain validation
	// and surfaced via rtv_list_sources so callers (and LLM agents) can
	// pick sources by intent.
	QueryIntents []Intent `json:"query_intents,omitempty"`

	// Kinds is the set of ResultKinds this provider emits. Drives the
	// Publication->Result conversion (cycle-2 task #10): when converting,
	// the first entry becomes Result.Kind. Cycle-1 scholarly providers
	// return [KindPaper]; wave-1 providers declare web/code/encyclopedia.
	Kinds []ResultKind `json:"kinds,omitempty"`

	// RequiresCredential is true when the plugin refuses to operate
	// without a configured API key or per-call credential. Surfaces in
	// rtv_list_sources so callers can filter the catalog by what's
	// reachable in their tenant. Added in v6 cycle 1 / v2.14.0 alongside
	// the first paid-provider tier; free plugins keep the zero value.
	RequiresCredential bool `json:"requires_credential,omitempty"`
}

// SourceHealth represents the current health and rate-limit status of a source.
type SourceHealth struct {
	Enabled            bool    `json:"enabled"`
	Healthy            bool    `json:"healthy"`
	RateLimit          float64 `json:"requests_per_second"`
	RateLimitRemaining float64 `json:"remaining,omitempty"`
	LastError          string  `json:"last_error,omitempty"`
}

// ---------------------------------------------------------------------------
// Plugin config
// ---------------------------------------------------------------------------

// PluginConfig is the configuration for a single source plugin.
// Used both for YAML config deserialization and as the Initialize parameter.
type PluginConfig struct {
	Enabled        bool              `yaml:"enabled"                    json:"enabled"`
	APIKey         string            `yaml:"api_key,omitempty"          json:"api_key,omitempty"`
	BaseURL        string            `yaml:"base_url,omitempty"         json:"base_url,omitempty"`
	Timeout        Duration          `yaml:"timeout,omitempty"          json:"timeout,omitzero"`
	RateLimit      float64           `yaml:"rate_limit,omitempty"       json:"rate_limit,omitempty"`
	RateLimitBurst int               `yaml:"rate_limit_burst,omitempty" json:"rate_limit_burst,omitempty"`
	Extra          map[string]string `yaml:"extra,omitempty"            json:"extra,omitempty"`
}

// Duration wraps time.Duration for YAML unmarshaling from string format (e.g. "10s").
type Duration struct {
	time.Duration
}

// ---------------------------------------------------------------------------
// Source info (for rtv_list_sources response)
// ---------------------------------------------------------------------------

// RateLimitInfo provides rate limit status for a source.
type RateLimitInfo struct {
	RequestsPerSecond float64 `json:"requests_per_second"`
	Remaining         float64 `json:"remaining"`
}

// SourceInfo is the response item from the list_sources tool.
//
// Cycle 2 expanded the surface to include residency posture, intent tags,
// kinds, and free-tier flags so LLM agents and operators can pick sources
// without enumerating booleans.
type SourceInfo struct {
	ID                     string          `json:"id"`
	Name                   string          `json:"name"`
	Description            string          `json:"description"`
	Enabled                bool            `json:"enabled"`
	ContentTypes           []ContentType   `json:"content_types"`
	NativeFormat           ContentFormat   `json:"native_format"`
	AvailableFormats       []ContentFormat `json:"available_formats"`
	SupportsFullText       bool            `json:"supports_full_text"`
	SupportsCitations      bool            `json:"supports_citations"`
	SupportsDateFilter     bool            `json:"supports_date_filter"`
	SupportsAuthorFilter   bool            `json:"supports_author_filter"`
	SupportsCategoryFilter bool            `json:"supports_category_filter"`
	SupportsDomainFilter   bool            `json:"supports_domain_filter"`
	SupportsChannelFilter  bool            `json:"supports_channel_filter"`
	SupportsLanguageFilter bool            `json:"supports_language_filter"`
	RateLimit              RateLimitInfo   `json:"rate_limit"`
	CategoriesHint         string          `json:"categories_hint,omitempty"`
	AcceptsCredentials     bool            `json:"accepts_credentials"`

	// Cycle-2 additions:
	Kinds           []ResultKind `json:"kinds,omitempty"`         // result kinds this source emits
	QueryIntents    []Intent     `json:"query_intents,omitempty"` // intents this source serves well
	Region          Region       `json:"region,omitempty"`        // EU / US / public-research-infrastructure / ...
	DPAStatus       DPAStatus    `json:"dpa_status,omitempty"`    // signed / covered-by-scc / n/a / unknown
	SubprocessorURL string       `json:"subprocessor_url,omitempty"`
	FreeTier        bool         `json:"free_tier,omitempty"`    // works without a paid key (incl. anon-tier providers)
	RequiresKey     bool         `json:"requires_key,omitempty"` // mirror of AcceptsCredentials but renamed for clarity
}

// ---------------------------------------------------------------------------
// BCP-47 language helpers (v2.7.0 smart filters)
// ---------------------------------------------------------------------------

// BCP47FirstSubtag returns the primary language subtag (lower-cased) from a
// BCP-47 tag. Examples: "en" -> "en", "en-US" -> "en", "fr-CA" -> "fr",
// "DE-DE" -> "de". Whitespace is trimmed. Empty input returns "".
//
// This helper is used by plugins whose upstream APIs accept only a two/three
// letter primary language code (Brave search_lang, YouTube relevanceLanguage,
// Bluesky lang, Europeana lang).
func BCP47FirstSubtag(tag string) string {
	t := strings.TrimSpace(tag)
	if t == "" {
		return ""
	}
	if i := strings.IndexByte(t, '-'); i > 0 {
		t = t[:i]
	}
	return strings.ToLower(t)
}

// MatchesLanguagePrefix reports whether a returned record-level language tag
// (e.g. "de-DE") satisfies a filter tag (e.g. "de"). Match is case-insensitive
// and accepts either exact equality or a prefix-with-dash relationship
// ("de" matches "de", "de-DE", "DE-AT"; "de" does NOT match "deu" or "den").
// Empty recordLang fails open: returns true so providers do not silently
// drop records whose language metadata is missing.
func MatchesLanguagePrefix(recordLang, filterLang string) bool {
	if filterLang == "" {
		return true
	}
	if recordLang == "" {
		return true // fail-open: missing record metadata is not a rejection signal
	}
	r := strings.ToLower(strings.TrimSpace(recordLang))
	f := strings.ToLower(strings.TrimSpace(filterLang))
	if r == f {
		return true
	}
	return strings.HasPrefix(r, f+"-")
}

// ValidateDomainList returns ErrInvalidDomainList if any entry in the list is
// not a bare registered-domain (no scheme, no path, no whitespace, must
// contain at least one dot, no leading/trailing/double dots, no port). Empty
// list and nil are valid. Used by plugins that wire
// IncludeDomains/ExcludeDomains.
//
// This is a shape check, not a DNS validator — it does NOT resolve the
// domain or check for Public Suffix List membership. The intent is to
// prevent obvious URL-form misuse (e.g. `https://example.com/path`) which
// would always 4xx from Brave/Exa.
func ValidateDomainList(domains []string) error {
	for _, d := range domains {
		if d == "" {
			return ErrInvalidDomainList
		}
		if strings.ContainsAny(d, " \t\r\n/:") {
			return ErrInvalidDomainList
		}
		if strings.Contains(d, "://") {
			return ErrInvalidDomainList
		}
		if strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") {
			return ErrInvalidDomainList
		}
		if strings.Contains(d, "..") {
			return ErrInvalidDomainList
		}
		if !strings.Contains(d, ".") {
			// Bare hostnames like "localhost" are not registered domains.
			return ErrInvalidDomainList
		}
	}
	return nil
}

// ValidateLanguageTag returns ErrInvalidLanguageTag if the BCP-47 tag is
// not a plausible language code. The check is intentionally permissive —
// it only rejects obvious garbage (whitespace, control bytes, non-ASCII,
// path/query characters) so well-formed yet exotic tags like "zh-Hant-TW"
// pass through. Empty input is allowed (means "no language filter").
//
// Called by plugins that wire SearchFilters.Language before forwarding it
// to the upstream API. Currently every wired plugin extracts only the
// first BCP-47 subtag via BCP47FirstSubtag, but the upstream may still
// see the raw tag if the plugin's API consumes the full form — defensive
// validation prevents header / path injection regardless.
func ValidateLanguageTag(tag string) error {
	if tag == "" {
		return nil
	}
	t := strings.TrimSpace(tag)
	if t == "" || t != tag {
		// Leading/trailing whitespace is not valid.
		return ErrInvalidLanguageTag
	}
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return ErrInvalidLanguageTag
		}
	}
	return nil
}
