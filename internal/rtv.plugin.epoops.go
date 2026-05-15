package internal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// EPO OPS (European Patent Office Open Patent Services) — v5 cycle 5 /
// v2.12.0.
//
// API root: https://ops.epo.org/3.2/rest-services
// Auth: OAuth2 client_credentials flow.
//   1. Register at https://developers.epo.org → receive consumer_key + secret
//   2. POST /auth/accesstoken
//        Header:  Authorization: Basic base64(key:secret)
//        Body:    grant_type=client_credentials
//        Returns: {access_token, expires_in, token_type:"BearerToken"}
//   3. Use Bearer token on subsequent /rest-services calls
//
// Search (biblio): GET /published-data/search/biblio?q=<q>&Range=<from-to>
//   Header:  Authorization: Bearer <token>
//            Accept:        application/json
//   Returns nested OPS XML-style JSON envelope under
//   "ops:world-patent-data" → "ops:biblio-search" with
//   "exchange-documents" carrying the per-record biblio data.
//
// This plugin implements the lightweight /search/ path (publication
// references only — title, dates, family-id) to keep the cycle's gate
// small. Biblio enrichment (inventors, applicants, abstracts) is a
// follow-on cycle; the patent_number returned here already dedups
// against Google Patents and CrossRef on shared publication numbers.
//
// Per-call credential: `epoops` (value is "consumer_key:consumer_secret"
// joined by colon).
//
// Residency: EU (EPO, Munich).
// ---------------------------------------------------------------------------

const (
	epoopsPluginID          = SourceEPOOPS
	epoopsPluginName        = "EPO OPS"
	epoopsPluginDescription = "Search the European Patent Office Open Patent Services (ops.epo.org). Free with registration (OAuth2 client_credentials). Returns worldwide patent publication references (EP, WO, US, JP, ...). Cross-source dedup keyed on publication number. EU-resident."

	epoopsDefaultBaseURL = "https://ops.epo.org/3.2"
	epoopsTokenPath      = "/auth/accesstoken"
	epoopsSearchPath     = "/rest-services/published-data/search"
	epoopsDefaultLimit   = 25
	epoopsMaxLimitCap    = 100
	epoopsDefaultRPS     = 1.0
	epoopsDefaultTimeout = 30 * time.Second
	epoopsTokenLifetime  = 1100 * time.Second // EPO returns 1200s; refresh early

	epoopsIDPrefix = "epoops:"

	epoopsCategoriesHint = "EPO supports CQL classification filters via CPC/IPC tokens appended to q (e.g. \"cpc=G06N3/08\"). Pass codes in filters.categories."

	epoopsHeaderAuthorization = "Authorization"
	epoopsBearerPrefix        = "Bearer "
	epoopsBasicPrefix         = "Basic "
	epoopsContentTypeForm     = "application/x-www-form-urlencoded"
	epoopsGrantClientCreds    = "grant_type=client_credentials"
)

// ---------------------------------------------------------------------------
// OPS wire types (lightweight /search/ shape)
// ---------------------------------------------------------------------------

type epoopsTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   string `json:"expires_in"`
}

// epoopsSearchEnvelope captures only the fields we surface. The OPS
// JSON is a heavily nested mirror of its XML schema — every leaf value
// lives under `$`. We use json.RawMessage and a custom extraction step
// for the publication-reference array because the shape can be a single
// object OR a list depending on result count.
type epoopsSearchEnvelope struct {
	WPD epoopsWPD `json:"ops:world-patent-data"`
}

type epoopsWPD struct {
	BiblioSearch epoopsBiblioSearch `json:"ops:biblio-search"`
}

type epoopsBiblioSearch struct {
	TotalResultCount string                `json:"@total-result-count"`
	SearchResult     epoopsSearchResultRaw `json:"ops:search-result"`
}

// epoopsSearchResultRaw uses RawMessage because OPS returns
// publication-reference as either a JSON object (1 result) or an array
// (>1 result).
type epoopsSearchResultRaw struct {
	PublicationReference json.RawMessage `json:"ops:publication-reference"`
}

type epoopsPublicationReference struct {
	FamilyID   string             `json:"@family-id,omitempty"`
	DocumentID []epoopsDocumentID `json:"document-id,omitempty"`
}

type epoopsDocumentID struct {
	Type      string       `json:"@document-id-type,omitempty"`
	Country   epoopsDollar `json:"country,omitempty"`
	DocNumber epoopsDollar `json:"doc-number,omitempty"`
	Kind      epoopsDollar `json:"kind,omitempty"`
	Date      epoopsDollar `json:"date,omitempty"`
}

type epoopsDollar struct {
	Value string `json:"$,omitempty"`
}

// ---------------------------------------------------------------------------
// EPOOPSPlugin
// ---------------------------------------------------------------------------

// EPOOPSPlugin implements SourcePlugin for EPO OPS. Thread-safe after
// Initialize; token state guarded by mu.
type EPOOPSPlugin struct {
	baseURL    string
	apiKey     string // "consumer_key:consumer_secret"
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu          sync.RWMutex
	healthy     bool
	lastError   string
	accessToken string
	tokenExpiry time.Time
}

// ID returns "epoops".
func (p *EPOOPSPlugin) ID() string { return epoopsPluginID }

// Name returns the human-readable label.
func (p *EPOOPSPlugin) Name() string { return epoopsPluginName }

// Description returns the LLM-facing one-liner.
func (p *EPOOPSPlugin) Description() string { return epoopsPluginDescription }

// ContentTypes — patent.
func (p *EPOOPSPlugin) ContentTypes() []ContentType { return []ContentType{ContentTypePatent} }

// NativeFormat — JSON.
func (p *EPOOPSPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *EPOOPSPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports EPO OPS's filter/sort surface.
func (p *EPOOPSPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       true,
		SupportsAuthorFilter:     false,
		SupportsCategoryFilter:   true,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       epoopsMaxLimitCap,
		CategoriesHint:           epoopsCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentDeepResearch, IntentPrimarySource},
		Kinds:                    []ResultKind{KindPatent},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *EPOOPSPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = epoopsDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = epoopsDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = epoopsDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *EPOOPSPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes an EPO OPS /search query.
func (p *EPOOPSPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = epoopsDefaultLimit
	}
	if limit > epoopsMaxLimitCap {
		limit = epoopsMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceEPOOPS, p.apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: epoops requires consumer_key:consumer_secret credential", ErrCredentialRequired)
	}

	token, err := p.ensureToken(ctx, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}

	env, err := p.doSearch(ctx, params, limit, token)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	refs := parseEPOOPSPublicationRefs(env.WPD.BiblioSearch.SearchResult.PublicationReference)
	pubs := make([]Publication, 0, len(refs))
	for i := range refs {
		pubs = append(pubs, epoopsRefToPublication(&refs[i]))
	}

	total, _ := strconv.Atoi(env.WPD.BiblioSearch.TotalResultCount)
	return &SearchResult{
		Total:   total,
		Results: pubs,
		HasMore: total > params.Offset+len(pubs),
	}, nil
}

// Get is not wired in cycle 5 — biblio enrichment via
// /rest-services/published-data/publication/docdb/<id>/biblio is a
// follow-on cycle.
func (p *EPOOPSPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: epoops Get is not wired in cycle 5", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// OAuth2 + HTTP transport
// ---------------------------------------------------------------------------

// ensureToken returns a cached access token if still valid, otherwise
// fetches a fresh one via client_credentials.
func (p *EPOOPSPlugin) ensureToken(ctx context.Context, apiKey string) (string, error) {
	p.mu.RLock()
	cached := p.accessToken
	expiry := p.tokenExpiry
	p.mu.RUnlock()
	if cached != "" && time.Now().Before(expiry) {
		return cached, nil
	}

	parts := strings.SplitN(apiKey, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("%w: epoops credential must be 'consumer_key:consumer_secret'", ErrCredentialInvalid)
	}
	basic := base64.StdEncoding.EncodeToString([]byte(apiKey))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+epoopsTokenPath, strings.NewReader(epoopsGrantClientCreds))
	if err != nil {
		return "", fmt.Errorf("epoops: build token request: %w", err)
	}
	req.Header.Set(epoopsHeaderAuthorization, epoopsBasicPrefix+basic)
	req.Header.Set("Content-Type", epoopsContentTypeForm)
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("epoops: token http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized || httpResp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%w: epoops", ErrCredentialInvalid)
	}
	if httpResp.StatusCode >= 400 {
		buf, _ := io.ReadAll(httpResp.Body)
		return "", fmt.Errorf("epoops: token status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var tr epoopsTokenResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("epoops: decode token: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("%w: epoops empty access token", ErrCredentialInvalid)
	}

	p.mu.Lock()
	p.accessToken = tr.AccessToken
	p.tokenExpiry = time.Now().Add(epoopsTokenLifetime)
	p.mu.Unlock()
	return tr.AccessToken, nil
}

func (p *EPOOPSPlugin) doSearch(ctx context.Context, params SearchParams, limit int, token string) (*epoopsSearchEnvelope, error) {
	q := url.Values{}
	q.Set("q", epoopsBuildQuery(params))
	rangeStart := params.Offset + 1
	if rangeStart < 1 {
		rangeStart = 1
	}
	rangeEnd := rangeStart + limit - 1
	q.Set("Range", strconv.Itoa(rangeStart)+"-"+strconv.Itoa(rangeEnd))

	reqURL := p.baseURL + epoopsSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("epoops: build request: %w", err)
	}
	req.Header.Set(epoopsHeaderAuthorization, epoopsBearerPrefix+token)
	req.Header.Set("Accept", "application/json")

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("epoops: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: epoops", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		// Force token refresh on next call.
		p.mu.Lock()
		p.accessToken = ""
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: epoops", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("epoops: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var env epoopsSearchEnvelope
	if err := json.NewDecoder(httpResp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("epoops: decode response: %w", err)
	}
	return &env, nil
}

// epoopsBuildQuery folds free-text + CPC/IPC categories + date filters
// into the CQL-style query string EPO OPS expects on its q= param.
func epoopsBuildQuery(params SearchParams) string {
	parts := []string{}
	if q := strings.TrimSpace(params.Query); q != "" {
		parts = append(parts, q)
	}
	for _, c := range params.Filters.Categories {
		if v := strings.TrimSpace(c); v != "" {
			parts = append(parts, "cpc="+v)
		}
	}
	from := strings.TrimSpace(params.Filters.DateFrom)
	to := strings.TrimSpace(params.Filters.DateTo)
	if from != "" || to != "" {
		if from == "" {
			from = "1700-01-01"
		}
		if to == "" {
			to = "2100-12-31"
		}
		parts = append(parts, "pd within \""+normalizeDateYYYYMMDD(from, true)+" "+normalizeDateYYYYMMDD(to, false)+"\"")
	}
	return strings.Join(parts, " AND ")
}

// parseEPOOPSPublicationRefs handles the object-vs-array shape switch in
// OPS responses. Empty or invalid JSON returns nil.
func parseEPOOPSPublicationRefs(raw json.RawMessage) []epoopsPublicationReference {
	if len(raw) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var refs []epoopsPublicationReference
		if err := json.Unmarshal(raw, &refs); err == nil {
			return refs
		}
		return nil
	}
	var ref epoopsPublicationReference
	if err := json.Unmarshal(raw, &ref); err == nil {
		return []epoopsPublicationReference{ref}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func epoopsRefToPublication(ref *epoopsPublicationReference) Publication {
	// Prefer the epodoc document-id-type (e.g. "EP3456789A1") — it's the
	// canonical aggregated identifier. Fall back to docdb otherwise.
	var pubNum, country, kind, date string
	for _, d := range ref.DocumentID {
		if d.Type == "epodoc" && d.DocNumber.Value != "" {
			pubNum = d.DocNumber.Value + d.Kind.Value
			country = d.Country.Value
			kind = d.Kind.Value
			date = d.Date.Value
			break
		}
	}
	if pubNum == "" && len(ref.DocumentID) > 0 {
		d := ref.DocumentID[0]
		pubNum = d.Country.Value + d.DocNumber.Value + d.Kind.Value
		country = d.Country.Value
		kind = d.Kind.Value
		date = d.Date.Value
	}

	published := date
	if len(published) == 8 {
		published = published[:4] + "-" + published[4:6] + "-" + published[6:8]
	}

	meta := map[string]any{
		MetaKeyPatentNumber: pubNum,
	}
	if country != "" {
		meta[smetaPatentJurisdiction] = country
	}
	if kind != "" {
		meta[smetaPatentKindCode] = kind
	}
	if ref.FamilyID != "" {
		meta["epoops_family_id"] = ref.FamilyID
	}

	return Publication{
		ID:             epoopsIDPrefix + pubNum,
		Source:         SourceEPOOPS,
		ContentType:    ContentTypePatent,
		Title:          pubNum,
		URL:            "https://worldwide.espacenet.com/patent/search?q=" + url.QueryEscape(pubNum),
		Published:      published,
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *EPOOPSPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *EPOOPSPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
