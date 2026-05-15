package internal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// v5 cycle 5 / v2.12.0 — EPO OPS tests.
// ---------------------------------------------------------------------------

func newEPOOPSTestPlugin(t *testing.T, baseURL, apiKey string) *EPOOPSPlugin {
	t.Helper()
	p := &EPOOPSPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, APIKey: apiKey, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

// epoopsTestServer returns an httptest server that handles both the
// token endpoint and a single search call. token must be returned at
// least once and then echoed back as the Bearer header on /search.
func epoopsTestServer(t *testing.T, search func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, epoopsTokenPath):
			assert.True(t, strings.HasPrefix(r.Header.Get(epoopsHeaderAuthorization), epoopsBasicPrefix))
			body, _ := io.ReadAll(r.Body)
			assert.Equal(t, epoopsGrantClientCreds, string(body))
			b, _ := json.Marshal(epoopsTokenResponse{AccessToken: "test-token", TokenType: "BearerToken", ExpiresIn: "1200"})
			_, _ = w.Write(b)
		case strings.HasSuffix(r.URL.Path, epoopsSearchPath):
			assert.Equal(t, epoopsBearerPrefix+"test-token", r.Header.Get(epoopsHeaderAuthorization))
			search(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestEPOOPS_Identity(t *testing.T) {
	t.Parallel()
	p := &EPOOPSPlugin{}
	assert.Equal(t, SourceEPOOPS, p.ID())
}

func TestEPOOPS_Capabilities(t *testing.T) {
	t.Parallel()
	p := &EPOOPSPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindPatent)
	assert.True(t, caps.SupportsDateFilter)
	assert.True(t, caps.SupportsCategoryFilter)
}

func TestEPOOPS_Residency(t *testing.T) {
	t.Parallel()
	p := &EPOOPSPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionEU, tag.Region)
	assert.Equal(t, DPASigned, tag.DPAStatus)
}

func TestEPOOPS_Search_MissingCredential(t *testing.T) {
	t.Parallel()
	p := newEPOOPSTestPlugin(t, "http://unused", "")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialRequired))
}

func TestEPOOPS_Search_HappyPath(t *testing.T) {
	t.Parallel()

	// One result, returned as a single OBJECT (not array) under
	// publication-reference to exercise that shape-switch handling.
	ref := epoopsPublicationReference{
		FamilyID: "12345",
		DocumentID: []epoopsDocumentID{
			{Type: "epodoc", DocNumber: epoopsDollar{Value: "EP3456789"}, Kind: epoopsDollar{Value: "A1"}, Country: epoopsDollar{Value: "EP"}, Date: epoopsDollar{Value: "20200101"}},
		},
	}
	refRaw, _ := json.Marshal(ref)
	envelope := epoopsSearchEnvelope{
		WPD: epoopsWPD{
			BiblioSearch: epoopsBiblioSearch{
				TotalResultCount: "1",
				SearchResult:     epoopsSearchResultRaw{PublicationReference: refRaw},
			},
		},
	}

	srv := epoopsTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Contains(t, q.Get("q"), "vortex tube")
		assert.Equal(t, "1-25", q.Get("Range"))
		b, _ := json.Marshal(envelope)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	defer srv.Close()

	p := newEPOOPSTestPlugin(t, srv.URL, "key:secret")
	res, err := p.Search(context.Background(), SearchParams{Query: "vortex tube", Limit: 25})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)

	pub := res.Results[0]
	assert.Equal(t, "epoops:EP3456789A1", pub.ID)
	assert.Equal(t, ContentTypePatent, pub.ContentType)
	assert.Equal(t, "EP3456789A1", pub.SourceMetadata[MetaKeyPatentNumber])
	assert.Equal(t, "EP", pub.SourceMetadata[smetaPatentJurisdiction])
	assert.Equal(t, "2020-01-01", pub.Published)
	assert.Equal(t, "12345", pub.SourceMetadata["epoops_family_id"])
}

func TestEPOOPS_Search_BadCredential(t *testing.T) {
	t.Parallel()
	srv := newEPOOPSAuthFailServer(t)
	defer srv.Close()
	p := newEPOOPSTestPlugin(t, srv.URL, "bad:creds")
	_, err := p.Search(context.Background(), SearchParams{Query: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialInvalid))
}

func newEPOOPSAuthFailServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
}

func TestEPOOPS_Get_NotWired(t *testing.T) {
	t.Parallel()
	p := &EPOOPSPlugin{}
	_, err := p.Get(context.Background(), "x", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestEPOOPS_ParsePublicationRefs_Array(t *testing.T) {
	t.Parallel()
	refs := []epoopsPublicationReference{
		{FamilyID: "1", DocumentID: []epoopsDocumentID{{Type: "epodoc", DocNumber: epoopsDollar{Value: "EP1"}}}},
		{FamilyID: "2", DocumentID: []epoopsDocumentID{{Type: "epodoc", DocNumber: epoopsDollar{Value: "EP2"}}}},
	}
	raw, _ := json.Marshal(refs)
	out := parseEPOOPSPublicationRefs(raw)
	require.Len(t, out, 2)
	assert.Equal(t, "1", out[0].FamilyID)
}
