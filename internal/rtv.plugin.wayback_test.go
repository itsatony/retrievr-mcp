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
// v5 cycle 6 / v2.13.0 — Wayback resolver tests.
// ---------------------------------------------------------------------------

func newWaybackTestPlugin(t *testing.T, baseURL string) *WaybackPlugin {
	t.Helper()
	p := &WaybackPlugin{}
	cfg := PluginConfig{Enabled: true, BaseURL: baseURL, RateLimit: 100}
	require.NoError(t, p.Initialize(context.Background(), cfg))
	return p
}

func TestWayback_Identity(t *testing.T) {
	t.Parallel()
	p := &WaybackPlugin{}
	assert.Equal(t, SourceWayback, p.ID())
}

func TestWayback_Capabilities(t *testing.T) {
	t.Parallel()
	p := &WaybackPlugin{}
	caps := p.Capabilities()
	assert.Contains(t, caps.Kinds, KindWeb)
	assert.Equal(t, 1, caps.MaxResultsPerQuery)
}

func TestWayback_Residency(t *testing.T) {
	t.Parallel()
	p := &WaybackPlugin{}
	tag := p.Residency()
	assert.Equal(t, RegionUS, tag.Region)
}

func TestWayback_Search_ReturnsHint(t *testing.T) {
	t.Parallel()
	p := &WaybackPlugin{}
	require.NoError(t, p.Initialize(context.Background(), PluginConfig{Enabled: true}))
	res, err := p.Search(context.Background(), SearchParams{Query: "anthropic.com"})
	require.NoError(t, err)
	require.Len(t, res.Results, 1)
	assert.Contains(t, res.Results[0].Title, "resolver")
}

func TestWayback_Get_HappyPath(t *testing.T) {
	t.Parallel()
	resp := waybackAvailableResponse{
		URL: "https://anthropic.com",
		ArchivedSnapshots: waybackSnapshots{
			Closest: &waybackSnapshot{
				Available: true,
				URL:       "http://web.archive.org/web/20230114230000/https://www.anthropic.com/",
				Timestamp: "20230114230000",
				Status:    "200",
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, waybackAvailablePath, r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "https://anthropic.com", q.Get(waybackParamURL))
		assert.Equal(t, "20230114", q.Get(waybackParamTimestamp))
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	p := newWaybackTestPlugin(t, srv.URL)
	pub, err := p.Get(context.Background(), "https://anthropic.com:20230114", nil, FormatNative)
	require.NoError(t, err)
	require.NotNil(t, pub)
	assert.Equal(t, "wayback:https://anthropic.com", pub.ID)
	assert.Equal(t, "2023-01-14", pub.Published)
	assert.Contains(t, pub.URL, "20230114230000")
	assert.Equal(t, "200", pub.SourceMetadata[waybackMetaKeyHTTPStatus])
}

func TestWayback_Get_NoSnapshot(t *testing.T) {
	t.Parallel()
	resp := waybackAvailableResponse{URL: "https://example.com"} // no closest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	p := newWaybackTestPlugin(t, srv.URL)
	_, err := p.Get(context.Background(), "https://example.com", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrGetFailed))
}

func TestWayback_Get_EmptyIDFails(t *testing.T) {
	t.Parallel()
	p := newWaybackTestPlugin(t, "http://unused")
	_, err := p.Get(context.Background(), "", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidID))
}

func TestWayback_Get_HTTP429MapsToRateLimit(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := newWaybackTestPlugin(t, srv.URL)
	_, err := p.Get(context.Background(), "https://example.com", nil, FormatNative)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimitExceeded))
}

func TestWayback_SplitIDArg(t *testing.T) {
	t.Parallel()
	target, ts := waybackSplitIDArg("https://anthropic.com:20230114")
	assert.Equal(t, "https://anthropic.com", target)
	assert.Equal(t, "20230114", ts)

	target, ts = waybackSplitIDArg("https://anthropic.com")
	assert.Equal(t, "https://anthropic.com", target)
	assert.Equal(t, "", ts)

	target, ts = waybackSplitIDArg("anthropic.com/path:more")
	// Not 8+ digits → no timestamp split.
	assert.Equal(t, "anthropic.com/path:more", target)
	assert.Equal(t, "", ts)
}

func TestWayback_TimestampToDate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "2023-01-14", waybackTimestampToDate("20230114230000"))
	assert.Equal(t, "", waybackTimestampToDate("short"))
}

// keep io import alive for future fixture writers.
var _ = io.Discard
