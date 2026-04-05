package internal

import (
	"encoding/hex"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// TestCredentialHash
// ---------------------------------------------------------------------------

func TestCredentialHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		sourceID    string
		credential  string
		assertFuncs []func(t *testing.T, got string)
	}{
		{
			name:       "deterministic — same inputs produce same output",
			sourceID:   SourceS2,
			credential: "key-abc",
			assertFuncs: []func(t *testing.T, got string){
				func(t *testing.T, got string) {
					t.Helper()
					second := CredentialHash(SourceS2, "key-abc")
					assert.Equal(t, got, second, "two calls with identical inputs must match")
				},
			},
		},
		{
			name:       "different sourceID same credential — different hash",
			sourceID:   SourcePubMed,
			credential: "shared-key",
			assertFuncs: []func(t *testing.T, got string){
				func(t *testing.T, got string) {
					t.Helper()
					other := CredentialHash(SourceS2, "shared-key")
					assert.NotEqual(t, got, other, "different sources must produce different hashes")
				},
			},
		},
		{
			name:       "same sourceID different credential — different hash",
			sourceID:   SourceOpenAlex,
			credential: "key-one",
			assertFuncs: []func(t *testing.T, got string){
				func(t *testing.T, got string) {
					t.Helper()
					other := CredentialHash(SourceOpenAlex, "key-two")
					assert.NotEqual(t, got, other, "different credentials must produce different hashes")
				},
			},
		},
		{
			name:       "empty credential equals explicit CredentialAnonymous",
			sourceID:   SourceArXiv,
			credential: "",
			assertFuncs: []func(t *testing.T, got string){
				func(t *testing.T, got string) {
					t.Helper()
					explicit := CredentialHash(SourceArXiv, CredentialAnonymous)
					assert.Equal(t, got, explicit, "empty credential must hash identically to CredentialAnonymous")
				},
			},
		},
		{
			name:       "output length is exactly credentialHashLength",
			sourceID:   SourceHuggingFace,
			credential: "hf-token-xyz",
			assertFuncs: []func(t *testing.T, got string){
				func(t *testing.T, got string) {
					t.Helper()
					assert.Len(t, got, credentialHashLength)
				},
			},
		},
		{
			name:       "output is valid hex",
			sourceID:   SourceEuropePMC,
			credential: "some-api-key",
			assertFuncs: []func(t *testing.T, got string){
				func(t *testing.T, got string) {
					t.Helper()
					_, err := hex.DecodeString(got)
					require.NoError(t, err, "output must be valid hex")
				},
			},
		},
		{
			name:       "empty credential output is also valid hex",
			sourceID:   SourcePubMed,
			credential: "",
			assertFuncs: []func(t *testing.T, got string){
				func(t *testing.T, got string) {
					t.Helper()
					assert.Len(t, got, credentialHashLength)
					_, err := hex.DecodeString(got)
					require.NoError(t, err, "anonymous hash must be valid hex")
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := CredentialHash(tc.sourceID, tc.credential)
			for _, fn := range tc.assertFuncs {
				fn(t, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCredentialHashAnonymous
// ---------------------------------------------------------------------------

func TestCredentialHashAnonymous(t *testing.T) {
	t.Parallel()

	t.Run("empty credential for same source is consistent across calls", func(t *testing.T) {
		t.Parallel()
		first := CredentialHash(SourceS2, "")
		second := CredentialHash(SourceS2, "")
		assert.Equal(t, first, second)
	})

	t.Run("empty credential for different sources produces different hashes", func(t *testing.T) {
		t.Parallel()
		sources := []string{
			SourceArXiv,
			SourcePubMed,
			SourceS2,
			SourceOpenAlex,
			SourceHuggingFace,
			SourceEuropePMC,
		}
		seen := make(map[string]string, len(sources))
		for _, src := range sources {
			h := CredentialHash(src, "")
			for prevSrc, prevHash := range seen {
				assert.NotEqualf(t, prevHash, h,
					"anonymous hash for %q must differ from %q", src, prevSrc)
			}
			seen[src] = h
		}
	})
}

// ---------------------------------------------------------------------------
// TestCredentialResolverResolve
// ---------------------------------------------------------------------------

func TestCredentialResolverResolve(t *testing.T) {
	t.Parallel()

	resolver := &CredentialResolver{}

	tests := []struct {
		name              string
		sourceID          string
		creds             *CallCredentials
		serverDefault     string
		wantCredential    string
		wantBucketKeyFunc func(t *testing.T, bucketKey string)
	}{
		{
			name:     "per-call S2 key wins over server default",
			sourceID: SourceS2,
			creds: &CallCredentials{
				S2APIKey: "call-level-key",
			},
			serverDefault:  "server-default-key",
			wantCredential: "call-level-key",
			wantBucketKeyFunc: func(t *testing.T, bucketKey string) {
				t.Helper()
				expected := CredentialHash(SourceS2, "call-level-key")
				assert.Equal(t, expected, bucketKey)
			},
		},
		{
			name:     "server default used when per-call is empty",
			sourceID: SourcePubMed,
			creds: &CallCredentials{
				PubMedAPIKey: "",
			},
			serverDefault:  "server-pubmed-key",
			wantCredential: "server-pubmed-key",
			wantBucketKeyFunc: func(t *testing.T, bucketKey string) {
				t.Helper()
				expected := CredentialHash(SourcePubMed, "server-pubmed-key")
				assert.Equal(t, expected, bucketKey)
			},
		},
		{
			name:           "anonymous — both per-call and server default empty",
			sourceID:       SourceArXiv,
			creds:          &CallCredentials{},
			serverDefault:  "",
			wantCredential: "",
			wantBucketKeyFunc: func(t *testing.T, bucketKey string) {
				t.Helper()
				expected := CredentialHash(SourceArXiv, "")
				assert.Equal(t, expected, bucketKey)
				assert.Len(t, bucketKey, credentialHashLength)
				_, err := hex.DecodeString(bucketKey)
				require.NoError(t, err)
			},
		},
		{
			name:           "nil CallCredentials falls back to server default",
			sourceID:       SourceOpenAlex,
			creds:          nil,
			serverDefault:  "server-openalex-key",
			wantCredential: "server-openalex-key",
			wantBucketKeyFunc: func(t *testing.T, bucketKey string) {
				t.Helper()
				expected := CredentialHash(SourceOpenAlex, "server-openalex-key")
				assert.Equal(t, expected, bucketKey)
			},
		},
		{
			name:           "nil CallCredentials and empty server default — anonymous",
			sourceID:       SourceHuggingFace,
			creds:          nil,
			serverDefault:  "",
			wantCredential: "",
			wantBucketKeyFunc: func(t *testing.T, bucketKey string) {
				t.Helper()
				expected := CredentialHash(SourceHuggingFace, "")
				assert.Equal(t, expected, bucketKey)
			},
		},
		{
			name:           "bucket key is deterministic — calling twice yields same result",
			sourceID:       SourceEuropePMC,
			creds:          &CallCredentials{},
			serverDefault:  "europmc-server-key",
			wantCredential: "europmc-server-key",
			wantBucketKeyFunc: func(t *testing.T, bucketKey string) {
				t.Helper()
				// Call a second time and compare
				_, secondKey := resolver.Resolve(SourceEuropePMC, &CallCredentials{}, "europmc-server-key")
				assert.Equal(t, bucketKey, secondKey, "bucket key must be deterministic")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			credential, bucketKey := resolver.Resolve(tc.sourceID, tc.creds, tc.serverDefault)
			assert.Equal(t, tc.wantCredential, credential, "credential mismatch")
			tc.wantBucketKeyFunc(t, bucketKey)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCredentialHashConcurrent
// ---------------------------------------------------------------------------

func TestCredentialHashConcurrent(t *testing.T) {
	t.Parallel()

	const goroutines = 100
	results := make(chan string, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			results <- CredentialHash(SourceS2, "test-key")
		}()
	}

	wg.Wait()
	close(results)

	var first string
	count := 0
	for got := range results {
		if count == 0 {
			first = got
		}
		assert.Equal(t, first, got, "all concurrent results must be identical")
		count++
	}

	require.Equal(t, goroutines, count, "must collect result from every goroutine")
}
