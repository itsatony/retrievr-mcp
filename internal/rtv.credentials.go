package internal

import (
	"crypto/sha256"
	"encoding/hex"
)

// ---------------------------------------------------------------------------
// Credential constants
// ---------------------------------------------------------------------------

const (
	// CredentialAnonymous is the marker used for anonymous (no-credential) callers.
	CredentialAnonymous = "__anonymous__"

	credentialHashSeparator = ":"
	credentialHashLength    = 16 // 8 bytes = 16 hex chars
)

// ---------------------------------------------------------------------------
// CredentialHash
// ---------------------------------------------------------------------------

// CredentialHash returns a short, deterministic hex string that uniquely
// identifies a (sourceID, credential) pair for use as a rate-limit bucket key.
//
// If credential is empty, CredentialAnonymous is substituted so that all
// anonymous callers for a given source share the same bucket.
//
// The hash is the first credentialHashLength hex characters of the SHA-256
// of "sourceID:effectiveCredential". It is a pure function with no side effects.
func CredentialHash(sourceID, credential string) string {
	effective := credential
	if effective == "" {
		effective = CredentialAnonymous
	}

	input := sourceID + credentialHashSeparator + effective
	raw := sha256.Sum256([]byte(input))
	full := hex.EncodeToString(raw[:])
	return full[:credentialHashLength]
}

// ---------------------------------------------------------------------------
// CredentialResolver
// ---------------------------------------------------------------------------

// CredentialResolver encapsulates the three-level credential resolution chain
// and bucket key generation. Stateless — safe for concurrent use.
type CredentialResolver struct{}

// Resolve applies the three-level resolution chain for a given source:
//  1. Per-call credential (from creds, if non-nil and non-empty for the source)
//  2. Server-level default (serverDefault)
//  3. Anonymous (empty string — CredentialHash will substitute CredentialAnonymous)
//
// It returns:
//   - credential: the effective API key / token string (may be empty for anonymous)
//   - bucketKey:  a short deterministic hash suitable for rate-limit bucket keying
func (r *CredentialResolver) Resolve(
	sourceID string,
	creds *CallCredentials,
	serverDefault string,
) (credential string, bucketKey string) {
	credential = creds.ResolveForSource(sourceID, serverDefault)
	bucketKey = CredentialHash(sourceID, credential)
	return credential, bucketKey
}
