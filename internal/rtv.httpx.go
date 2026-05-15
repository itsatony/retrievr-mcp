package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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

	// egressMaxRedirects caps the number of HTTP redirects a single
	// outbound request will follow. Default Go would allow 10; we cap
	// at 3 to limit redirect-chain SSRF amplification.
	egressMaxRedirects = 3

	// defaultMaxResponseBytes is the response-body decode cap applied
	// by limitedDecode. 10 MB is enough for the largest realistic
	// search response and limits memory blast radius from a hostile
	// or misbehaving upstream.
	defaultMaxResponseBytes = 10 << 20

	// healthLastErrorMax bounds the per-plugin lastError string so
	// upstream response bodies echoed via fmt.Errorf("...: body=%s",
	// truncateForError(...)) never inflate the Health() surface.
	healthLastErrorMax = 200
)

// redactURLSecretParams lists query-parameter names that must be
// stripped from any URL before it is logged or wrapped into a
// returned error. Names are matched case-insensitively. Extending
// this list is the cheapest way to add a new provider whose key
// rides the query string (SerpAPI, NewsAPI, Wolfram Alpha, KG API,
// Europeana, HERE, Mojeek, Scrapingdog YouTube).
var redactURLSecretParams = []string{
	"api_key", "apikey", "api-key",
	"key",    // Google KG API, Europeana wskey is matched below
	"wskey",  // Europeana
	"appid",  // Wolfram Alpha
	"apiKey", // NewsAPI exact-case
	"access_token", "accesstoken",
	"token",
}

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
		CheckRedirect: egressCheckRedirect,
	}
}

// egressCheckRedirect caps redirect-chain depth and strips bearer-style
// headers on every hop. Go's default policy preserves Authorization /
// Cookie on same-host redirects, which would forward bearer tokens
// across paths the plugin never intended; we strip aggressively to keep
// credential blast-radius minimal.
func egressCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= egressMaxRedirects {
		return http.ErrUseLastResponse
	}
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	req.Header.Del("X-Goog-Api-Key")
	req.Header.Del("X-ListenAPI-Key")
	return nil
}

// redactURL returns urlStr with the value of any known secret-bearing
// query parameter replaced by "[REDACTED]". Returns the input unchanged
// when it doesn't parse. Use this anywhere a URL flows into an error,
// log entry, or health-status line — Go's net/http transport embeds
// the full URL in connection errors, which can otherwise leak query-
// string API keys into stdout / log aggregators.
func redactURL(urlStr string) string {
	u, err := url.Parse(urlStr)
	if err != nil {
		return urlStr
	}
	q := u.Query()
	mutated := false
	for k := range q {
		for _, secret := range redactURLSecretParams {
			if strings.EqualFold(k, secret) {
				q.Set(k, "[REDACTED]")
				mutated = true
				break
			}
		}
	}
	if !mutated {
		return urlStr
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// redactURLErr unwraps a Go transport error (specifically *url.Error,
// which net/http returns for round-trip failures) and rewrites the
// embedded URL with redactURL so secret query parameters don't appear
// in error text. Any other error type is returned unchanged.
//
// MUST be called BEFORE the error is wrapped with fmt.Errorf("...:
// %w", err) — fmt.Errorf snapshots the formatted message at
// construction, so mutating the inner *url.Error after wrapping has
// no effect on the rendered output. Recommended call site:
//
//	httpResp, err := p.httpClient.Do(req)
//	if err != nil {
//	    return nil, fmt.Errorf("plugin: http: %w", redactURLErr(err))
//	}
func redactURLErr(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	ue.URL = redactURL(ue.URL)
	return err
}

// limitedDecode wraps json.NewDecoder with an io.LimitReader so a
// hostile or misbehaving upstream cannot exhaust memory by streaming
// an arbitrarily large response. Pass 0 for maxBytes to use the
// package default (10 MB).
func limitedDecode(body io.Reader, v any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	return json.NewDecoder(io.LimitReader(body, maxBytes)).Decode(v)
}

// sanitizeHealthError reduces an arbitrary plugin error to a string
// safe for placement in Health().LastError. It drops any trailing
// "body=..." fragment (upstream response echoes can include the
// rejected credential value) and caps the result at healthLastErrorMax
// bytes. The unwrapped root error message stays intact for
// errors.Is-style inspection on the caller side; only the *string
// surfaced via Health is sanitized.
func sanitizeHealthError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Strip everything after "body=" — upstream response snippets can
	// include credential echoes, JSON error envelopes carrying key
	// prefixes, etc.
	if idx := strings.Index(msg, " body="); idx > 0 {
		msg = msg[:idx]
	}
	// Strip URL query strings entirely from any embedded url.Error,
	// which net/http formats as `Get "<url>": ...`.
	msg = stripURLQueryStrings(msg)
	if len(msg) > healthLastErrorMax {
		msg = msg[:healthLastErrorMax] + "…"
	}
	return msg
}

// stripURLQueryStrings finds `<scheme>://...?<q>` substrings and replaces
// each query string with the literal text "?[REDACTED]". Order-preserving
// scan; allocates only on a hit.
func stripURLQueryStrings(s string) string {
	if !strings.Contains(s, "?") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		q := strings.Index(s[i:], "?")
		if q < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+q])
		b.WriteString("?[REDACTED]")
		// Consume to next whitespace, quote, or end (whichever first).
		j := i + q + 1
		for j < len(s) && s[j] != ' ' && s[j] != '"' && s[j] != '\t' && s[j] != '\n' {
			j++
		}
		i = j
	}
	return b.String()
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
