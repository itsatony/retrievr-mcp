package internal

import (
	"fmt"
	"net"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// Outbound HTTP hygiene — EU-mode Hook #4 (plan §3.7).
//
// Every plugin that talks to an upstream API SHOULD use NewEgressClient
// (cycle 2 introduces this; cycle-1 plugins still use ad-hoc *http.Client
// instances and will be migrated in cycle 3). The egress client enforces:
//
//   - Neutral User-Agent ("retrievr/<version> (+repo URL)") so providers
//     can identify retrievr in their logs without leaking end-user info.
//   - No Referer header forwarded (Go's default RoundTripper already omits
//     it, but we strip explicitly in case a future plugin sets one).
//   - No X-Forwarded-For — would leak the originating IP if liz/nexus
//     proxies a user request to retrievr in-process.
//   - No cookies (jar=nil, deliberate).
//
// The implementation is a thin RoundTripper wrapper. Plugins that need
// custom transport behavior (e.g., HTTPS pinning, retry-aware connection
// reuse) can decorate this with their own RoundTripper.
// ---------------------------------------------------------------------------

const (
	// egressUserAgentTemplate produces the User-Agent header value. The
	// "%s" is the runtime retrievr version.
	egressUserAgentTemplate = "retrievr/%s (+https://github.com/itsatony/retrievr-mcp)"

	// egressDialTimeout caps the TCP+TLS handshake duration. Distinct from
	// per-request timeout (which bounds the entire round trip).
	egressDialTimeout = 10 * time.Second

	// egressIdleConnTimeout is how long an unused connection stays in the
	// pool before being closed.
	egressIdleConnTimeout = 90 * time.Second

	// egressMaxIdleConnsPerHost is the per-host idle-pool ceiling.
	egressMaxIdleConnsPerHost = 4

	// egressMaxIdleConnsTotal caps the global idle-pool size.
	egressMaxIdleConnsTotal = 100
)

// NewEgressClient returns an *http.Client configured for retrievr's outbound
// hygiene contract. timeout bounds the entire round trip per request; pass
// 0 for no per-request timeout. Safe for concurrent use across goroutines.
func NewEgressClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: egressDialTimeout}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         dialer.DialContext,
		MaxIdleConns:        egressMaxIdleConnsTotal,
		MaxIdleConnsPerHost: egressMaxIdleConnsPerHost,
		IdleConnTimeout:     egressIdleConnTimeout,
		TLSHandshakeTimeout: egressDialTimeout,
	}
	return &http.Client{
		Transport: &egressRoundTripper{base: transport},
		Timeout:   timeout,
		// Jar deliberately nil: cookies on retrievr's outbound calls would
		// be a privacy/cross-tenant correlation hazard.
	}
}

// egressRoundTripper wraps a base RoundTripper and enforces the hygiene
// contract on every outbound request.
type egressRoundTripper struct {
	base http.RoundTripper
}

// RoundTrip implements http.RoundTripper. Mutates a clone of the request
// (per the http.RoundTripper contract — must not modify the input).
func (e *egressRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())

	// Neutral User-Agent. Plugins MAY override by setting it explicitly
	// (e.g., Wikipedia requires a polite UA with contact email).
	if r2.Header.Get("User-Agent") == "" {
		r2.Header.Set("User-Agent", buildEgressUserAgent())
	}

	// Strip headers that would leak originating-request context.
	r2.Header.Del("Referer")
	r2.Header.Del("X-Forwarded-For")
	r2.Header.Del("X-Real-IP")
	r2.Header.Del("Forwarded")

	return e.base.RoundTrip(r2)
}

// buildEgressUserAgent renders the version-stamped UA. Falls back to "dev"
// when LoadVersion hasn't been called (test environments).
func buildEgressUserAgent() string {
	v := GetVersion()
	if v == "" {
		v = "dev"
	}
	return fmt.Sprintf(egressUserAgentTemplate, v)
}
