package internal

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifySourceError_EveryCode(t *testing.T) {
	t.Parallel()
	wrap := func(e error) error { return fmt.Errorf("%w: src: %w", ErrSearchFailed, e) }
	cases := []struct {
		err  error
		want SourceErrorCode
	}{
		{wrap(&QuotaExhaustedError{Source: "s", Reason: "r"}), SourceErrQuotaExhausted},
		{wrap(ErrRateLimitExceeded), SourceErrRateLimited},
		{wrap(ErrCredentialRequired), SourceErrCredentialRequired},
		{wrap(ErrCredentialInvalid), SourceErrCredentialInvalid},
		{wrap(context.Canceled), SourceErrCanceled},
		{wrap(context.DeadlineExceeded), SourceErrTimeout},
		{wrap(ErrUpstreamTimeout), SourceErrTimeout},
		{wrap(NewGetUnsupportedError("x")), SourceErrUnsupported},
		{wrap(ErrBibTeXUnsupported), SourceErrUnsupported},
		{wrap(ErrInvalidLanguageTag), SourceErrInvalidRequest},
		{wrap(ErrTooManyChannels), SourceErrInvalidRequest},
		{wrap(errors.New("boom")), SourceErrUpstream},
	}
	seen := map[SourceErrorCode]bool{}
	for _, tc := range cases {
		assert.Equal(t, tc.want, ClassifySourceError(tc.err), tc.err.Error())
		seen[tc.want] = true
	}
	// Every declared code is reachable — a code nothing can produce is a
	// branch a caller will write and never see fire.
	for _, code := range []SourceErrorCode{
		SourceErrQuotaExhausted, SourceErrRateLimited, SourceErrCredentialRequired,
		SourceErrCredentialInvalid, SourceErrTimeout, SourceErrCanceled,
		SourceErrInvalidRequest, SourceErrUnsupported, SourceErrUpstream,
	} {
		assert.True(t, seen[code], "code %q has no producing case", code)
	}
}

func TestAllSourcesFailedError_MatchesSentinelAndCauses(t *testing.T) {
	t.Parallel()
	err := newAllSourcesFailedError(2, []sourceResult{
		{sourceID: "a", err: fmt.Errorf("%w: a: %w", ErrSearchFailed, ErrCredentialInvalid)},
		{sourceID: "b", err: nil},
		{sourceID: "c", err: fmt.Errorf("%w: c: %w", ErrSearchFailed, &QuotaExhaustedError{Source: "c", Reason: "quotaExceeded"})},
	})
	assert.ErrorIs(t, err, ErrAllSourcesFailed)
	assert.ErrorIs(t, err, ErrCredentialInvalid)
	assert.ErrorIs(t, err, ErrQuotaExhausted)
	assert.NotErrorIs(t, err, ErrUpstreamTimeout)
	assert.Equal(t, "all sources failed: queried 2 sources (a=credential_invalid, c=quota_exhausted)", err.Error())

	var all *AllSourcesFailedError
	require.ErrorAs(t, err, &all)
	assert.Len(t, all.Failures, 2, "a source that answered is not a failure")
}

func TestAllSourcesFailedError_NoFailuresKeepsLegacyMessage(t *testing.T) {
	t.Parallel()
	err := newAllSourcesFailedError(3, nil)
	assert.Equal(t, "all sources failed: queried 3 sources", err.Error())
	assert.ErrorIs(t, err, ErrAllSourcesFailed)
}

func TestSourceErrorsFrom_NilWhenClean(t *testing.T) {
	t.Parallel()
	assert.Nil(t, sourceErrorsFrom(nil))
	got := sourceErrorsFrom([]sourceResult{{sourceID: "x", err: ErrRateLimitExceeded}})
	require.Len(t, got, 1)
	assert.True(t, got[0].Retryable)
	assert.Equal(t, SourceErrRateLimited, got[0].Code)
}
