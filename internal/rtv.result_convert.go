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
	smetaKindOverride = "kind"         // explicit Kind override (e.g., "web")
	smetaSnippet      = "snippet"      // short summary for web/news/code
	smetaDomain       = "domain"       // hostname (web/news/code)
	smetaLanguage     = "language"     // BCP-47 (any kind)
	smetaStars        = "stars"        // *int (code/model)
	smetaSiteName     = "site_name"    // web
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

	// v3 video-kind keys (cycle 2 / v2.3.0). The youtube_id, thumbnail and
	// duration are also surfaced on Publication itself (cycle-1 fields), but
	// channel info / view count / like count / live state live in metadata.
	smetaChannelName   = "channel_name"
	smetaChannelID     = "channel_id"
	smetaViewCount     = "view_count"
	smetaLikeCount     = "like_count"
	smetaLiveBroadcast = "live_broadcast"

	// v3 place-kind keys (cycle 3 / v2.4.0). Lat/Lon/Address live on
	// Publication directly (cycle-1 fields). osm_id lives in SourceMetadata
	// (the canonical dedup key). The rest are administrative breakdowns of
	// the formatted address + POI categorization (TomTom).
	smetaOSMType     = "osm_type" // node | way | relation
	smetaCountry     = "country"
	smetaCountryCode = "country_code" // ISO 3166-1 alpha-2
	smetaCity        = "city"
	smetaState       = "state"
	smetaPostcode    = "postcode"
	smetaStreet      = "street"
	smetaHouseNumber = "house_number"
	smetaCategories  = "categories" // []string POI categories
	smetaPlaceType   = "place_type" // city | street | poi | building | ...
	smetaImportance  = "importance" // float64 0-1

	// v3 image-kind keys (cycle 4 / v2.5.0). MediaURL, ThumbnailURL,
	// MediaMime, License live on Publication directly. Width/Height,
	// LicenseURL, Artist, SourcePage live in SourceMetadata.
	smetaWidth      = "width"
	smetaHeight     = "height"
	smetaLicenseURL = "license_url"
	smetaArtist     = "artist"
	smetaSourcePage = "source_page"

	// v3 post-kind keys (cycle 5 / v2.6.0). EngagementScore on Publication
	// is the normalized sum; per-component counts live here so consumers
	// can render a breakdown.
	smetaAuthorHandle = "author_handle"
	smetaAuthorURL    = "author_url"
	smetaPlatformURL  = "platform_url"
	// smetaLikeCount is shared with video (cycle 2) — same key value "like_count".
	smetaRepostCount = "repost_count"
	smetaReplyCount  = "reply_count"
	smetaMediaCount  = "media_count"
	smetaSubreddit   = "subreddit"
	smetaInstance    = "instance"
	smetaVerified    = "verified"
)

// kindForSource picks the Kind for a Publication produced by sourceID.
// Lookup priority:
//  1. SourceMetadata["kind"] — explicit per-result override.
//  2. plugins[sourceID].Capabilities().Kinds[0] — declared default.
//  3. Publication.ContentType mapping — covers HuggingFace's mixed
//     paper/model/dataset emissions where Capabilities().Kinds is unset.
//  4. KindPaper — cycle-1 fallback (every cycle-1 plugin is paper).
func kindForSource(p Publication, plugin SourcePlugin) ResultKind {
	if v, ok := p.SourceMetadata[smetaKindOverride].(string); ok && IsValidResultKind(v) {
		return ResultKind(v)
	}
	if plugin != nil {
		if kinds := plugin.Capabilities().Kinds; len(kinds) > 0 {
			return kinds[0]
		}
	}
	switch p.ContentType {
	case ContentTypeModel:
		return KindModel
	case ContentTypeDataset:
		return KindDataset
	case ContentTypePaper:
		return KindPaper
	case ContentTypeVideo:
		return KindVideo
	case ContentTypePlace:
		return KindPlace
	case ContentTypeImage:
		return KindImage
	case ContentTypePost:
		return KindPost
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
	case KindVideo:
		res.Video = videoDataFromPublication(p)
	case KindPlace:
		res.Place = placeDataFromPublication(p)
	case KindImage:
		res.Image = imageDataFromPublication(p)
	case KindPost:
		res.Post = postDataFromPublication(p)
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

// videoDataFromPublication builds the kind-specific VideoData block.
// Reads Publication.ThumbnailURL/DurationSeconds (v3 cycle-1 fields) +
// SourceMetadata[youtube_id, channel_name, channel_id, view_count, like_count,
// live_broadcast, published_at].
func videoDataFromPublication(p Publication) *VideoData {
	vd := &VideoData{
		ChannelName:   metaString(p.SourceMetadata, smetaChannelName),
		ChannelID:     metaString(p.SourceMetadata, smetaChannelID),
		VideoID:       metaString(p.SourceMetadata, MetaKeyYouTubeID),
		ThumbnailURL:  p.ThumbnailURL,
		PublishedAt:   metaString(p.SourceMetadata, smetaPublishedAt),
		LiveBroadcast: metaString(p.SourceMetadata, smetaLiveBroadcast),
	}
	if p.DurationSeconds != nil {
		vd.DurationSeconds = *p.DurationSeconds
	}
	if v, ok := metaInt(p.SourceMetadata, smetaViewCount); ok {
		vd.ViewCount = &v
	}
	if v, ok := metaInt(p.SourceMetadata, smetaLikeCount); ok {
		vd.LikeCount = &v
	}
	return vd
}

// placeDataFromPublication builds the kind-specific PlaceData block.
// Reads Publication.Lat/Lon/Address (v3 cycle-1 fields) + SourceMetadata
// [osm_id, osm_type, country, country_code, city, state, postcode, street,
// house_number, categories, place_type, importance].
func placeDataFromPublication(p Publication) *PlaceData {
	pd := &PlaceData{
		OSMID:       metaString(p.SourceMetadata, MetaKeyOSMID),
		OSMType:     metaString(p.SourceMetadata, smetaOSMType),
		Address:     p.Address,
		Country:     metaString(p.SourceMetadata, smetaCountry),
		CountryCode: metaString(p.SourceMetadata, smetaCountryCode),
		City:        metaString(p.SourceMetadata, smetaCity),
		State:       metaString(p.SourceMetadata, smetaState),
		Postcode:    metaString(p.SourceMetadata, smetaPostcode),
		Street:      metaString(p.SourceMetadata, smetaStreet),
		HouseNumber: metaString(p.SourceMetadata, smetaHouseNumber),
		Categories:  metaStringSlice(p.SourceMetadata, smetaCategories),
		PlaceType:   metaString(p.SourceMetadata, smetaPlaceType),
	}
	if p.Lat != nil {
		pd.Lat = *p.Lat
	}
	if p.Lon != nil {
		pd.Lon = *p.Lon
	}
	if v, ok := metaFloat(p.SourceMetadata, smetaImportance); ok {
		pd.Importance = v
	}
	return pd
}

// imageDataFromPublication builds the kind-specific ImageData block.
// Reads Publication.MediaURL/ThumbnailURL/MediaMime/License (v3 cycle-1
// fields) + SourceMetadata[width, height, license_url, artist, source_page].
func imageDataFromPublication(p Publication) *ImageData {
	id := &ImageData{
		MediaURL:     p.MediaURL,
		ThumbnailURL: p.ThumbnailURL,
		MediaMime:    p.MediaMime,
		License:      p.License,
		LicenseURL:   metaString(p.SourceMetadata, smetaLicenseURL),
		Artist:       metaString(p.SourceMetadata, smetaArtist),
		SourcePage:   metaString(p.SourceMetadata, smetaSourcePage),
	}
	if v, ok := metaInt(p.SourceMetadata, smetaWidth); ok {
		id.Width = v
	}
	if v, ok := metaInt(p.SourceMetadata, smetaHeight); ok {
		id.Height = v
	}
	return id
}

// postDataFromPublication builds the kind-specific PostData block. Reads
// SourceMetadata for per-component counts + handle/platform URL/atproto URI.
// PublishedAt prefers smetaPublishedAt (full RFC3339); falls back to
// Publication.Published (YYYY-MM-DD).
func postDataFromPublication(p Publication) *PostData {
	pd := &PostData{
		AuthorHandle: metaString(p.SourceMetadata, smetaAuthorHandle),
		AuthorURL:    metaString(p.SourceMetadata, smetaAuthorURL),
		AtprotoURI:   metaString(p.SourceMetadata, MetaKeyAtprotoURI),
		PlatformURL:  metaString(p.SourceMetadata, smetaPlatformURL),
		Subreddit:    metaString(p.SourceMetadata, smetaSubreddit),
		Instance:     metaString(p.SourceMetadata, smetaInstance),
	}
	if pd.PlatformURL == "" {
		pd.PlatformURL = p.URL
	}
	publishedAt := metaString(p.SourceMetadata, smetaPublishedAt)
	if publishedAt == "" {
		publishedAt = p.Published
	}
	pd.PublishedAt = publishedAt

	if v, ok := metaInt(p.SourceMetadata, smetaLikeCount); ok {
		pd.LikeCount = &v
	}
	if v, ok := metaInt(p.SourceMetadata, smetaRepostCount); ok {
		pd.RepostCount = &v
	}
	if v, ok := metaInt(p.SourceMetadata, smetaReplyCount); ok {
		pd.ReplyCount = &v
	}
	if v, ok := metaInt(p.SourceMetadata, smetaMediaCount); ok {
		pd.MediaCount = v
	}
	if v, ok := p.SourceMetadata[smetaVerified].(bool); ok {
		pd.Verified = v
	}
	return pd
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

// metaFloat reads a numeric metadata value as float64. Accepts float64,
// int, or int64 — covers JSON-decoded and natively-typed values alike.
func metaFloat(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
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
