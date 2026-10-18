package internal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// v2.28.0 — a spent YouTube quota must reach the caller as a typed,
// distinguishable failure, never as an empty result, and must not be retried.

// youtubeErrorBody renders the Data API v3 error envelope for one reason.
func youtubeErrorBody(code int, reason string) string {
	return fmt.Sprintf(`{"error":{"code":%d,"message":%q,"errors":[{"domain":"youtube.quota","reason":%q,"message":"test"}]}}`,
		code, reason, reason)
}

// youtubeFailingServer answers every request with status + body and counts hits.
func youtubeFailingServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestYouTube_ErrorReasonsAreDistinguishable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		status        int
		body          string
		wantCode      SourceErrorCode
		wantQuota     bool
		wantRateLimit bool
		wantPermanent bool
	}{
		{"quotaExceeded", http.StatusForbidden, youtubeErrorBody(403, youtubeReasonQuotaExceeded), SourceErrQuotaExhausted, true, true, true},
		{"dailyLimitExceeded", http.StatusForbidden, youtubeErrorBody(403, youtubeReasonDailyLimitExceeded), SourceErrQuotaExhausted, true, true, true},
		{"rateLimitExceeded", http.StatusForbidden, youtubeErrorBody(403, youtubeReasonRateLimitExceeded), SourceErrRateLimited, false, true, false},
		{"userRateLimitExceeded", http.StatusForbidden, youtubeErrorBody(403, youtubeReasonUserRateLimitExceeded), SourceErrRateLimited, false, true, false},
		{"bare 429", http.StatusTooManyRequests, ``, SourceErrRateLimited, false, true, false},
		{"keyInvalid", http.StatusBadRequest, youtubeErrorBody(400, youtubeReasonKeyInvalid), SourceErrCredentialInvalid, false, false, true},
		{"bare 403", http.StatusForbidden, `{}`, SourceErrCredentialInvalid, false, false, true},
		{"500", http.StatusInternalServerError, `oops`, SourceErrUpstream, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := youtubeFailingServer(t, tc.status, tc.body)
			p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)

			res, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
			require.Error(t, err, "a failed upstream must never read as an empty result")
			assert.Nil(t, res)

			assert.Equal(t, tc.wantCode, ClassifySourceError(err))
			assert.Equal(t, tc.wantQuota, errors.Is(err, ErrQuotaExhausted))
			assert.Equal(t, tc.wantRateLimit, errors.Is(err, ErrRateLimitExceeded))
			assert.Equal(t, tc.wantPermanent, IsPermanentError(err))
		})
	}
}

func TestYouTube_QuotaExhaustedError_Shape(t *testing.T) {
	t.Parallel()
	srv, _ := youtubeFailingServer(t, http.StatusForbidden, youtubeErrorBody(403, youtubeReasonQuotaExceeded))
	p := newYouTubeTestPlugin(t, srv.URL, youtubeTestServerKey)

	_, err := p.Search(context.Background(), SearchParams{Query: "x", Limit: 1})
	var qe *QuotaExhaustedError
	require.ErrorAs(t, err, &qe)
	assert.Equal(t, SourceYouTube, qe.Source)
	assert.Equal(t, youtubeReasonQuotaExceeded, qe.Reason)
	assert.Contains(t, err.Error(), ErrMsgQuotaExhausted)
	assert.NotContains(t, err.Error(), youtubeTestServerKey, "the key must never ride in the error")
}

// youtubeQuotaRouter wires the REAL YouTube plugin (pointed at srv) into a
// router with fast retries, plus optional extra plugins.
func youtubeQuotaRouter(t *testing.T, srvURL string, extra map[string]SourcePlugin) *Router {
	t.Helper()
	plugins := map[string]SourcePlugin{SourceYouTube: newYouTubeTestPlugin(t, srvURL, youtubeTestServerKey)}
	for id, p := range extra {
		plugins[id] = p
	}
	cfg := testRouterConfig()
	cfg.DefaultSources = nil
	for id := range plugins {
		cfg.DefaultSources = append(cfg.DefaultSources, id)
	}
	cfg.Retry = RouterRetryConfig{
		MaxAttempts: 3,
		BaseDelay:   Duration{Duration: time.Millisecond},
		MaxDelay:    Duration{Duration: 2 * time.Millisecond},
	}
	return NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())
}

func TestRouter_YouTubeOnly_QuotaExhaustedSurfacesTypedAndIsNotRetried(t *testing.T) {
	t.Parallel()
	srv, hits := youtubeFailingServer(t, http.StatusForbidden, youtubeErrorBody(403, youtubeReasonQuotaExceeded))
	r := youtubeQuotaRouter(t, srv.URL, nil)

	res, err := r.Search(context.Background(), SearchParams{Query: "x", Limit: 5}, []string{SourceYouTube}, nil)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrAllSourcesFailed, "pre-v2.28.0 matchers keep working")
	assert.ErrorIs(t, err, ErrQuotaExhausted, "the cause must be reachable, not logged and dropped")

	var all *AllSourcesFailedError
	require.ErrorAs(t, err, &all)
	require.Len(t, all.Failures, 1)
	assert.Equal(t, SourceYouTube, all.Failures[0].Source)
	assert.Equal(t, SourceErrQuotaExhausted, all.Failures[0].Code)
	assert.False(t, all.Failures[0].Retryable)
	assert.Contains(t, err.Error(), "youtube=quota_exhausted")

	assert.Equal(t, int32(1), hits.Load(), "a spent daily quota must not be retried")

	// The MCP envelope keeps "all sources failed" as its message and carries
	// the per-source code in the detail.
	mcp := NewMCPErrorFromErr(err, "")
	assert.Contains(t, mcp, `"error":"`+ErrMsgAllSourcesFailed+`"`)
	assert.Contains(t, mcp, "youtube=quota_exhausted")
}

// Positive control for the retry assertion above: the same router DOES retry
// a short-window throttle, so hits==1 there is a property of the quota
// classification and not of a router that never retries.
func TestRouter_YouTubeOnly_RateLimitIsRetried(t *testing.T) {
	t.Parallel()
	srv, hits := youtubeFailingServer(t, http.StatusForbidden, youtubeErrorBody(403, youtubeReasonRateLimitExceeded))
	r := youtubeQuotaRouter(t, srv.URL, nil)

	_, err := r.Search(context.Background(), SearchParams{Query: "x", Limit: 5}, []string{SourceYouTube}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimitExceeded)
	assert.NotErrorIs(t, err, ErrQuotaExhausted)
	assert.Contains(t, err.Error(), "youtube=rate_limited")
	assert.Equal(t, int32(3), hits.Load(), "a throttle is retried up to MaxAttempts")
}

func TestRouter_MixedSources_QuotaExhaustedReportedBesideResults(t *testing.T) {
	t.Parallel()
	srv, _ := youtubeFailingServer(t, http.StatusForbidden, youtubeErrorBody(403, youtubeReasonDailyLimitExceeded))
	ok := newMockPlugin(mockSourceA, []Publication{{ID: mockSourceA + ":1", Source: mockSourceA, Title: "hit"}})
	r := youtubeQuotaRouter(t, srv.URL, map[string]SourcePlugin{mockSourceA: ok})

	res, err := r.Search(context.Background(), SearchParams{Query: "x", Limit: 5}, []string{SourceYouTube, mockSourceA}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Results)
	assert.Equal(t, []string{SourceYouTube}, res.SourcesFailed)
	require.Len(t, res.SourceErrors, 1)
	assert.Equal(t, SourceYouTube, res.SourceErrors[0].Source)
	assert.Equal(t, SourceErrQuotaExhausted, res.SourceErrors[0].Code)
	assert.False(t, res.SourceErrors[0].Retryable)
	assert.NotContains(t, res.SourceErrors[0].Message, youtubeTestServerKey)

	// SearchV2 carries the same report.
	v2, err := r.SearchV2(context.Background(), SearchParams{Query: "x", Limit: 5}, []string{SourceYouTube, mockSourceA}, nil)
	require.NoError(t, err)
	require.Len(t, v2.SourceErrors, 1)
	assert.Equal(t, SourceErrQuotaExhausted, v2.SourceErrors[0].Code)
}

func TestRouter_CleanSearch_HasNoSourceErrors(t *testing.T) {
	t.Parallel()
	ok := newMockPlugin(mockSourceA, []Publication{{ID: mockSourceA + ":1", Source: mockSourceA, Title: "hit"}})
	r := testRouter(map[string]SourcePlugin{mockSourceA: ok})
	res, err := r.Search(context.Background(), SearchParams{Query: "x", Limit: 5, Sort: SortRelevance}, []string{mockSourceA}, nil)
	require.NoError(t, err)
	assert.Nil(t, res.SourceErrors, "omitempty: a clean search carries no source_errors key")
}
