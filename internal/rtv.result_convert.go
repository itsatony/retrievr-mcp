package internal

import (
	"net/url"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Publication -> Result conversion (Cycle 2 task #10).
//
// Plugins continue to emit Publication; Router converts to Result post-merge.
// This minimizes plugin-side churn during the migration and keeps the cycle-1
// scholarly providers byte-stable on the v1 wire while v2 callers get the
// fat-struct shape via Client.SearchV2 / rtv_search compat:"v2".
//
// Wave-1 providers (cycle 2) emit Publication too but stuff kind-specific
// data (snippet, domain, stars, etc.) into Publication.SourceMetadata; the
// converter unpacks it into the right per-kind data block.
// ---------------------------------------------------------------------------

// SourceMetadata keys recognized by toResult — wave-1 plugins set these on
// Publication.SourceMetadata when their result has non-paper-shaped data.
const (
	smetaKindOverride = "kind"        // explicit Kind override (e.g., "web")
	smetaSnippet      = "snippet"     // short summary for web/news/code
	smetaDomain       = "domain"      // hostname (web/news/code)
	smetaLanguage     = "language"    // BCP-47 (any kind)
	smetaStars        = "stars"       // *int (code/model)
	smetaSiteName     = "site_name"   // web
	smetaPublishedAt  = "published_at" // web/news (precise timestamp)
	smetaReadingMins  = "reading_mins" // web

	// Code-kind keys.
	smetaRepo       = "repo"
	smetaPath       = "path"
	smetaSHA        = "sha"
	smetaCodeLang   = "code_language"
	smetaTopics     = "topics"
	smetaForks      = "forks"
	smetaLastCommit = "last_commit"

	// Encyclopedia-kind keys.
	smetaArticle   = "article"
	smetaSections  = "sections"
	smetaLanguages = "languages"
	smetaRevision  = "revision"
)

// kindForSource picks the Kind for a Publication produced by sourceID.
// Lookup priority:
//  1. SourceMetadata["kind"] — explicit per-result override.
//  2. plugins[sourceID].Capabilities().Kinds[0] — declared default.
//  3. KindPaper — cycle-1 fallback (every existing plugin is paper).
func kindForSource(p Publication, plugin SourcePlugin) ResultKind {
	if v, ok := p.SourceMetadata[smetaKindOverride].(string); ok && IsValidResultKind(v) {
		return ResultKind(v)
	}
	if plugin != nil {
		if kinds := plugin.Capabilities().Kinds; len(kinds) > 0 {
			return kinds[0]
		}
	}
	return KindPaper
}

// (r *Router) toResult converts one Publication to a Result. The rank is
// the position in the merged result list (0-indexed); we use it to compute
// a normalized lexical score (1 / (1 + rank)) so v2 consumers have a
// uniform ordering signal even when the upstream provider didn't return
// one.
func (r *Router) toResult(p Publication, rank int) Result {
	plugin := r.plugins[p.Source]
	kind := kindForSource(p, plugin)

	res := Result{
		Kind:        kind,
		ID:          p.ID,
		Source:      p.Source,
		AlsoFoundIn: append([]string(nil), p.AlsoFoundIn...),
		Title:       p.Title,
		URL:         p.URL,
		Abstract:    p.Abstract,
		Authors:     append([]Author(nil), p.Authors...),
		Published:   p.Published,
		Updated:     p.Updated,
		License:     p.License,
		Score:       1.0 / (1.0 + float64(rank)),
		ScoreParts: &ScoreBreakdown{
			Lexical: 1.0 / (1.0 + float64(rank)),
		},
	}

	// Domain — derive from URL when not explicitly set in SourceMetadata.
	if v := metaString(p.SourceMetadata, smetaDomain); v != "" {
		res.Domain = v
	} else if p.URL != "" {
		if u, err := url.Parse(p.URL); err == nil && u.Hostname() != "" {
			res.Domain = u.Hostname()
		}
	}

	// Snippet — explicit override wins; otherwise truncate Abstract for
	// non-paper kinds (paper kind keeps Abstract as the long-form field).
	if v := metaString(p.SourceMetadata, smetaSnippet); v != "" {
		res.Snippet = v
	} else if kind != KindPaper && p.Abstract != "" {
		res.Snippet = truncateSnippet(p.Abstract)
	}

	if v := metaString(p.SourceMetadata, smetaLanguage); v != "" {
		res.Language = v
	}
	if v, ok := metaInt(p.SourceMetadata, smetaStars); ok {
		res.Stars = &v
	}

	// Per-kind blocks. Populate the one matching res.Kind; leave others nil.
	switch kind {
	case KindPaper:
		res.Paper = paperDataFromPublication(p)
	case KindWeb:
		res.Web = &WebData{
			SiteName:    metaString(p.SourceMetadata, smetaSiteName),
			PublishedAt: metaString(p.SourceMetadata, smetaPublishedAt),
			ReadingMins: metaIntOrZero(p.SourceMetadata, smetaReadingMins),
		}
	case KindNews:
		res.News = &NewsData{
			Outlet:      metaString(p.SourceMetadata, "outlet"),
			Section:     metaString(p.SourceMetadata, "section"),
			PublishedAt: metaString(p.SourceMetadata, smetaPublishedAt),
		}
	case KindCode:
		res.Code = codeDataFromPublication(p)
	case KindModel:
		res.Model = modelDataFromPublication(p)
	case KindDataset:
		res.Dataset = datasetDataFromPublication(p)
	case KindEncyclopedia:
		res.Encyclopedia = &EncyclopediaData{
			Article:   metaString(p.SourceMetadata, smetaArticle),
			Sections:  metaStringSlice(p.SourceMetadata, smetaSections),
			Languages: metaStringSlice(p.SourceMetadata, smetaLanguages),
			Revision:  metaString(p.SourceMetadata, smetaRevision),
		}
	}

	// Provenance — single tag for now; cycle-3 will append for each
	// also_found_in source after the residency lookup.
	if plugin != nil {
		tag := plugin.Residency()
		res.Provenance = []ProvenanceTag{{
			Source:    p.Source,
			Region:    string(tag.Region),
			DPAStatus: string(tag.DPAStatus),
			FetchedAt: time.Now().UTC(),
		}}
	}

	return res
}

// paperDataFromPublication builds the kind-specific PaperData block from a
// cycle-1 Publication. All fields map directly except References/Citations
// which already match.
func paperDataFromPublication(p Publication) *PaperData {
	if p.DOI == "" && p.ArXivID == "" && p.PDFURL == "" && p.CitationCount == nil &&
		len(p.Categories) == 0 && len(p.References) == 0 && len(p.Citations) == 0 {
		// Truly minimal — return nil to keep the JSON compact.
		return nil
	}
	return &PaperData{
		DOI:           p.DOI,
		ArXivID:       p.ArXivID,
		Categories:    append([]string(nil), p.Categories...),
		CitationCount: p.CitationCount,
		PDFURL:        p.PDFURL,
		References:    append([]Reference(nil), p.References...),
		Citations:     append([]Reference(nil), p.Citations...),
		Venue:         metaString(p.SourceMetadata, "venue"),
	}
}

func codeDataFromPublication(p Publication) *CodeData {
	cd := &CodeData{
		Repo:       metaString(p.SourceMetadata, smetaRepo),
		Path:       metaString(p.SourceMetadata, smetaPath),
		Language:   metaString(p.SourceMetadata, smetaCodeLang),
		SHA:        metaString(p.SourceMetadata, smetaSHA),
		Topics:     metaStringSlice(p.SourceMetadata, smetaTopics),
		LastCommit: metaString(p.SourceMetadata, smetaLastCommit),
	}
	if v, ok := metaInt(p.SourceMetadata, smetaStars); ok {
		cd.Stars = &v
	}
	if v, ok := metaInt(p.SourceMetadata, smetaForks); ok {
		cd.Forks = &v
	}
	return cd
}

func modelDataFromPublication(p Publication) *ModelData {
	md := &ModelData{
		Architecture: metaString(p.SourceMetadata, "architecture"),
		Parameters:   metaString(p.SourceMetadata, "parameters"),
		Tags:         metaStringSlice(p.SourceMetadata, "tags"),
		BaseModel:    metaString(p.SourceMetadata, "base_model"),
	}
	if v, ok := metaInt(p.SourceMetadata, "downloads"); ok {
		md.Downloads = &v
	}
	if v, ok := metaInt(p.SourceMetadata, "likes"); ok {
		md.Likes = &v
	}
	return md
}

func datasetDataFromPublication(p Publication) *DatasetData {
	dd := &DatasetData{
		Tasks:    metaStringSlice(p.SourceMetadata, "tasks"),
		Modality: metaStringSlice(p.SourceMetadata, "modality"),
		License:  p.License,
	}
	if v, ok := metaInt(p.SourceMetadata, "rows"); ok {
		dd.Rows = &v
	}
	if v, ok := metaInt64(p.SourceMetadata, "size_bytes"); ok {
		dd.SizeBytes = &v
	}
	return dd
}

// PublicationsToResults is the merged-list converter — what Client.SearchV2
// calls after Router.Search returns. Pass the active *Router so per-source
// plugin lookups (for Kind + Residency) work.
func (r *Router) PublicationsToResults(pubs []Publication) []Result {
	out := make([]Result, len(pubs))
	for i, p := range pubs {
		out[i] = r.toResult(p, i)
	}
	return out
}

// ---------------------------------------------------------------------------
// metadata helpers
// ---------------------------------------------------------------------------

const snippetMaxRunes = 200

// truncateSnippet trims abstract-style text to ~200 runes on a sentence
// or word boundary. Used as a fallback Snippet when the plugin doesn't
// provide one explicitly. Cheap to compute; we don't load a UTF-8 word
// segmenter — just look for the last whitespace before the cut point.
func truncateSnippet(s string) string {
	if len([]rune(s)) <= snippetMaxRunes {
		return s
	}
	r := []rune(s)
	cut := snippetMaxRunes
	// Walk back to last space within 30 runes so we don't cut a word.
	for i := cut - 1; i > cut-30 && i >= 0; i-- {
		if r[i] == ' ' || r[i] == '\n' || r[i] == '\t' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(r[:cut]), " .,;:") + "…"
}

func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func metaInt(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

func metaIntOrZero(m map[string]any, key string) int {
	v, _ := metaInt(m, key)
	return v
}

func metaInt64(m map[string]any, key string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	}
	return 0, false
}

func metaStringSlice(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	switch v := m[key].(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
