package internal

import (
	"context"
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
// ORCID researcher-profile provider — v5 cycle 3 / v2.10.0.
//
// API: GET https://pub.orcid.org/v3.0/expanded-search/?q=<q>&start=<n>&rows=<n>
//   Headers:
//     Accept: application/json
//     Authorization: Bearer <public-data-token>  (optional but recommended;
//       anonymous calls are throttled aggressively)
//
// Response (v3.0 expanded-search):
//   {
//     "num-found": int,
//     "expanded-result": [
//       {
//         "orcid-id": "0000-0001-2345-6789",
//         "given-names": "Jane",
//         "family-names": "Doe",
//         "credit-name": "Jane M. Doe",
//         "other-name": [...],
//         "email": [...],
//         "institution-name": ["University X", "..."]
//       }
//     ]
//   }
//
// Free with public-data API token (registration at orcid.org/developer-tools).
// US non-profit.
//
// Returns Publication-shaped person records: Title = display name, Authors
// = [self], URL = ORCID profile, SourceMetadata carries the ORCID iD and
// affiliations. Cross-source dedup is by ORCID iD (a future cycle can
// unify with Author.ORCID on papers).
//
// Residency: US (ORCID Inc., Bethesda MD; non-profit).
// ---------------------------------------------------------------------------

const (
	orcidPluginID          = SourceORCID
	orcidPluginName        = "ORCID"
	orcidPluginDescription = "Search ORCID (pub.orcid.org) for researcher profiles by name, affiliation, or keyword. Public-data API; free with registration token. Returns person records (display name, affiliations, ORCID iD). Cross-source dedup keys on ORCID iD."

	orcidDefaultBaseURL = "https://pub.orcid.org/v3.0"
	orcidSearchPath     = "/expanded-search/"
	orcidDefaultLimit   = 20
	orcidMaxLimitCap    = 200
	orcidDefaultRPS     = 5.0
	orcidDefaultTimeout = 15 * time.Second

	orcidIDPrefix = "orcid:"

	orcidParamQuery = "q"
	orcidParamStart = "start"
	orcidParamRows  = "rows"

	orcidProfileURLPrefix = "https://orcid.org/"

	orcidCategoriesHint = "ORCID expanded-search hits over name, affiliation, email, keywords. No first-class category filter; the q field accepts the Lucene fielded syntax (e.g. \"family-name:Doe AND affiliation-org-name:MIT\")."

	orcidMetaKeyORCIDiD      = "orcid_id"
	orcidMetaKeyInstitutions = "orcid_institutions"
	orcidMetaKeyGivenNames   = "orcid_given_names"
	orcidMetaKeyFamilyNames  = "orcid_family_names"
	orcidMetaKeyCreditName   = "orcid_credit_name"
)

// ---------------------------------------------------------------------------
// ORCID wire types
// ---------------------------------------------------------------------------

type orcidSearchResponse struct {
	NumFound       int          `json:"num-found"`
	ExpandedResult []orcidEntry `json:"expanded-result"`
}

type orcidEntry struct {
	ORCIDID         string   `json:"orcid-id,omitempty"`
	GivenNames      string   `json:"given-names,omitempty"`
	FamilyNames     string   `json:"family-names,omitempty"`
	CreditName      string   `json:"credit-name,omitempty"`
	OtherName       []string `json:"other-name,omitempty"`
	Email           []string `json:"email,omitempty"`
	InstitutionName []string `json:"institution-name,omitempty"`
}

// ---------------------------------------------------------------------------
// ORCIDPlugin
// ---------------------------------------------------------------------------

// ORCIDPlugin implements SourcePlugin for the ORCID public-data search.
// Thread-safe after Initialize.
type ORCIDPlugin struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	enabled    bool
	rateLimit  float64

	mu        sync.RWMutex
	healthy   bool
	lastError string
}

// ID returns "orcid".
func (p *ORCIDPlugin) ID() string { return orcidPluginID }

// Name returns the human-readable label.
func (p *ORCIDPlugin) Name() string { return orcidPluginName }

// Description returns the LLM-facing one-liner.
func (p *ORCIDPlugin) Description() string { return orcidPluginDescription }

// ContentTypes — ORCID returns person records; surface as paper so
// existing routers can fan-out searches. The KindFact discriminator at
// the v2 layer handles person-vs-fact ambiguity; cycle 3 keeps it simple.
func (p *ORCIDPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypePaper}
}

// NativeFormat — JSON.
func (p *ORCIDPlugin) NativeFormat() ContentFormat { return FormatJSON }

// AvailableFormats — JSON only.
func (p *ORCIDPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

// Capabilities reports ORCID's filter/sort surface.
func (p *ORCIDPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsFullText:         false,
		SupportsCitations:        false,
		SupportsDateFilter:       false,
		SupportsAuthorFilter:     true,
		SupportsCategoryFilter:   false,
		SupportsSortRelevance:    true,
		SupportsSortDate:         false,
		SupportsSortCitations:    false,
		SupportsOpenAccessFilter: false,
		SupportsDomainFilter:     false,
		SupportsChannelFilter:    false,
		SupportsLanguageFilter:   false,
		SupportsPagination:       true,
		MaxResultsPerQuery:       orcidMaxLimitCap,
		CategoriesHint:           orcidCategoriesHint,
		NativeFormat:             FormatJSON,
		AvailableFormats:         []ContentFormat{FormatJSON},
		QueryIntents:             []Intent{IntentReference, IntentQuickLookup},
		Kinds:                    []ResultKind{KindFact},
	}
}

// Initialize wires the plugin from PluginConfig.
func (p *ORCIDPlugin) Initialize(_ context.Context, cfg PluginConfig) error {
	p.enabled = cfg.Enabled
	p.rateLimit = cfg.RateLimit
	if p.rateLimit <= 0 {
		p.rateLimit = orcidDefaultRPS
	}

	p.apiKey = cfg.APIKey

	p.baseURL = cfg.BaseURL
	if p.baseURL == "" {
		p.baseURL = orcidDefaultBaseURL
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")

	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = orcidDefaultTimeout
	}
	p.httpClient = NewEgressClient(timeout)
	p.healthy = true
	return nil
}

// Health reports current status.
func (p *ORCIDPlugin) Health(_ context.Context) SourceHealth {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return SourceHealth{
		Enabled:   p.enabled,
		Healthy:   p.healthy,
		RateLimit: p.rateLimit,
		LastError: p.lastError,
	}
}

// Search executes an ORCID expanded-search query.
func (p *ORCIDPlugin) Search(ctx context.Context, params SearchParams) (*SearchResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = orcidDefaultLimit
	}
	if limit > orcidMaxLimitCap {
		limit = orcidMaxLimitCap
	}

	apiKey := CredentialFor(ctx, SourceORCID, p.apiKey)
	resp, err := p.doSearch(ctx, params, limit, apiKey)
	if err != nil {
		p.recordError(err)
		return nil, err
	}
	p.recordSuccess()

	pubs := make([]Publication, 0, len(resp.ExpandedResult))
	for i := range resp.ExpandedResult {
		pubs = append(pubs, orcidEntryToPublication(&resp.ExpandedResult[i]))
	}
	return &SearchResult{
		Total:   resp.NumFound,
		Results: pubs,
		HasMore: resp.NumFound > params.Offset+len(pubs),
	}, nil
}

// Get is not wired in cycle 3 — a future cycle can fetch full /person
// records by ORCID iD. The expanded-search response already carries the
// useful subset (display names + affiliations).
func (p *ORCIDPlugin) Get(_ context.Context, _ string, _ []IncludeField, _ ContentFormat) (*Publication, error) {
	return nil, fmt.Errorf("%w: orcid Get is not wired in cycle 3", ErrFormatUnsupported)
}

// ---------------------------------------------------------------------------
// HTTP transport
// ---------------------------------------------------------------------------

func (p *ORCIDPlugin) doSearch(ctx context.Context, params SearchParams, limit int, apiKey string) (*orcidSearchResponse, error) {
	q := url.Values{}
	q.Set(orcidParamQuery, params.Query)
	q.Set(orcidParamRows, strconv.Itoa(limit))
	if params.Offset > 0 {
		q.Set(orcidParamStart, strconv.Itoa(params.Offset))
	}

	reqURL := p.baseURL + orcidSearchPath + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("orcid: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("orcid: http: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: orcid", ErrRateLimitExceeded)
	case httpResp.StatusCode == http.StatusUnauthorized, httpResp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: orcid", ErrCredentialInvalid)
	case httpResp.StatusCode >= 400:
		buf, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("orcid: status=%d body=%s", httpResp.StatusCode, truncateForError(string(buf)))
	}

	var resp orcidSearchResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("orcid: decode response: %w", err)
	}
	return &resp, nil
}

// ---------------------------------------------------------------------------
// Wire → Publication mapping
// ---------------------------------------------------------------------------

func orcidEntryToPublication(e *orcidEntry) Publication {
	displayName := strings.TrimSpace(e.CreditName)
	if displayName == "" {
		parts := []string{}
		if g := strings.TrimSpace(e.GivenNames); g != "" {
			parts = append(parts, g)
		}
		if f := strings.TrimSpace(e.FamilyNames); f != "" {
			parts = append(parts, f)
		}
		displayName = strings.Join(parts, " ")
	}
	if displayName == "" {
		displayName = e.ORCIDID
	}

	institutions := make([]string, 0, len(e.InstitutionName))
	for _, inst := range e.InstitutionName {
		if v := strings.TrimSpace(inst); v != "" {
			institutions = append(institutions, v)
		}
	}

	primaryAffiliation := ""
	if len(institutions) > 0 {
		primaryAffiliation = institutions[0]
	}

	abstract := ""
	if primaryAffiliation != "" {
		abstract = "Affiliation: " + primaryAffiliation
	}
	if len(institutions) > 1 {
		abstract += " (and " + strconv.Itoa(len(institutions)-1) + " more)"
	}

	meta := map[string]any{
		orcidMetaKeyORCIDiD: e.ORCIDID,
	}
	if len(institutions) > 0 {
		meta[orcidMetaKeyInstitutions] = institutions
	}
	if e.GivenNames != "" {
		meta[orcidMetaKeyGivenNames] = e.GivenNames
	}
	if e.FamilyNames != "" {
		meta[orcidMetaKeyFamilyNames] = e.FamilyNames
	}
	if e.CreditName != "" {
		meta[orcidMetaKeyCreditName] = e.CreditName
	}

	return Publication{
		ID:          orcidIDPrefix + e.ORCIDID,
		Source:      SourceORCID,
		ContentType: ContentTypePaper,
		Title:       displayName,
		Abstract:    abstract,
		URL:         orcidProfileURLPrefix + e.ORCIDID,
		Authors: []Author{{
			Name:        displayName,
			Affiliation: primaryAffiliation,
			ORCID:       e.ORCIDID,
		}},
		SourceMetadata: meta,
	}
}

// ---------------------------------------------------------------------------
// Health helpers
// ---------------------------------------------------------------------------

func (p *ORCIDPlugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = true
	p.lastError = ""
}

func (p *ORCIDPlugin) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy = false
	if err != nil {
		p.lastError = err.Error()
	}
}
