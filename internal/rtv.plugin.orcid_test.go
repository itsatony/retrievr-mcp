package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v5 cycle 3 / v2.10.0 — ORCID tests.
// ---------------------------------------------------------------------------

func newORCIDTestPlugin(t *testing.T, baseURL, apiKey string) *ORCIDPlugin {
	t.Helper()
	p := &ORCIDPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestORCID_Identity(t *testing.T) {
	t.Parallel()
	p := &ORCIDPlugin{}
	assert.Equal(t, SourceORCID, p.ID())
}

func TestORCID_Capabilities(t *testing.T) {
	t.Parallel()
	p := &ORCIDPlugin{}
	caps := p.Capabilities()
	assert.True(t, caps.SupportsAuthorFilter)
	assert.False(t, caps.SupportsDateFilter)
	assert.Contains(t, caps.Kinds, KindFact)
}

func TestORCID_Residency(t *testing.T) {
	t.Parallel()
	p := &ORCIDPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
	assert.Equal(t, DPACoveredBySCC, tag.DPAStatus)
}

func TestORCID_Search_HappyPath(t *testing.T) {
	t.Parallel()
	entries := []orcidEntry{{
		ORCIDID:         "0000-0001-2345-6789",
		GivenNames:      "Jane",
		FamilyNames:     "Doe",
		CreditName:      "Jane M. Doe",
		InstitutionName: []string{"Broad Institute", "MIT"},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, orcidSearchPath, r.URL.Path)
		assert.Equal(t, "Bearer pub-token", r.Header.Get("Authorization"))
		q := r.URL.Query()
		assert.Equal(t, "Jane Doe", q.Get(orcidParamQuery))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(orcidSearchResponse{NumFound: 1, ExpandedResult: entries})
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newORCIDTestPlugin(t, srv.URL, "pub-token")
	res, err := p.Search(context.Background(), SearchParams{Query: "Jane Doe", Limit: 10})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "orcid:0000-0001-2345-6789", pub.ID)
	assert.Equal(t, "Jane M. Doe", pub.Title)
	assert.Equal(t, "https://orcid.org/0000-0001-2345-6789", pub.URL)
	require.Len(t, pub.Authors, 1)
	assert.Equal(t, "Broad Institute", pub.Authors[0].Affiliation)
	assert.Equal(t, "0000-0001-2345-6789", pub.Authors[0].ORCID)
	assert.Contains(t, pub.Abstract, "Broad Institute")
	assert.Equal(t, []string{"Broad Institute", "MIT"}, pub.SourceMetadata[orcidMetaKeyInstitutions])
}

func TestORCID_Search_NameFallback(t *testing.T) {
	t.Parallel()
	entries := []orcidEntry{{
		ORCIDID:     "0000-0002-0000-0001",
		GivenNames:  "Alice",
		FamilyNames: "Smith",
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b, _ := json.Marshal(orcidSearchResponse{NumFound: 1, ExpandedResult: entries})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	p := newORCIDTestPlugin(t, srv.URL, "")
	res, err := p.Search(context.Background(), SearchParams{Query: "Alice Smith"})
	require.NoError(t, err)
	assert.Equal(t, "Alice Smith", res.Results[0].Title)
}

func TestORCID_Search_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newORCIDTestPlugin(t, srv.URL, "bad-key")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func TestORCID_Search_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newORCIDTestPlugin(t, srv.URL, "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestORCID_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &ORCIDPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

// pre-empt io import drift
var _ = io.Discard
