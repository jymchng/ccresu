package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/config"
)

// TestReadsNeedNoTokenButWritesDo proves the central security rule: the bearer
// token guards every state-changing request and nothing else.
func TestReadsNeedNoTokenButWritesDo(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, nil)

	// Reads work without a credential: the listener is loopback-only and a
	// browser cannot read them cross-origin.
	h.get("/health").expect(http.StatusOK)
	h.get("/sessions").expect(http.StatusOK)
	h.get("/sessions/" + sessionID).expect(http.StatusOK)

	// A write without a token is refused, and says how to authenticate.
	missing := h.doWithToken(http.MethodPost, "/sessions/"+sessionID+"/disable", nil, "")
	missing.expect(http.StatusUnauthorized)
	if auth := missing.header.Get("WWW-Authenticate"); !strings.Contains(auth, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", auth)
	}
	if code := missing.errorOf().Code; code != codeUnauthorized {
		t.Errorf("code = %q, want %q", code, codeUnauthorized)
	}

	// A wrong token is refused too.
	wrong := h.doWithToken(http.MethodPost, "/sessions/"+sessionID+"/disable", nil, "not-the-token")
	wrong.expect(http.StatusUnauthorized)

	// A token that is a prefix of the real one is not accepted: the comparison is
	// over the whole value.
	prefix := h.doWithToken(http.MethodPost, "/sessions/"+sessionID+"/disable", nil, h.token[:len(h.token)-1])
	prefix.expect(http.StatusUnauthorized)

	// The correct token works, and the write really happened.
	ok := h.do(http.MethodPost, "/sessions/"+sessionID+"/disable", nil)
	ok.expect(http.StatusOK)
	session, err := h.store.GetSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if session.Enabled {
		t.Fatal("the session is still enabled after a disable request")
	}
}

// TestAuthenticateSkipsSafeMethods proves GET/HEAD/OPTIONS never need a token.
func TestAuthenticateSkipsSafeMethods(t *testing.T) {
	h := newHarness(t)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		req, err := http.NewRequest(method, APIPrefix+"/health", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		if apiErr := h.server.authenticate(req); apiErr != nil {
			t.Errorf("%s was rejected: %v", method, apiErr)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, err := http.NewRequest(method, APIPrefix+"/health", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		if apiErr := h.server.authenticate(req); apiErr == nil {
			t.Errorf("%s was allowed without a token", method)
		}
	}
}

// TestOriginAllowlist proves a browser origin must be on the list, and that an
// allowed origin gets the CORS answer it needs.
func TestOriginAllowlist(t *testing.T) {
	h := newHarness(t)

	allowed := h.doRequest(http.MethodGet, "/health", nil, func(req *http.Request) {
		req.Header.Set("Origin", "https://docs.example.test")
	})
	allowed.expect(http.StatusOK)
	if got := allowed.header.Get("Access-Control-Allow-Origin"); got != "https://docs.example.test" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if got := allowed.header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") {
		t.Errorf("Access-Control-Allow-Headers = %q, want Authorization", got)
	}
	if got := allowed.header.Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary = %q, want it to include Origin", got)
	}
	if got := allowed.header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want it unset: the token is a header, not a cookie", got)
	}

	denied := h.doRequest(http.MethodGet, "/health", nil, func(req *http.Request) {
		req.Header.Set("Origin", "https://evil.test")
	})
	denied.expect(http.StatusForbidden)
	if code := denied.errorOf().Code; code != codeOriginForbidden {
		t.Errorf("code = %q, want %q", code, codeOriginForbidden)
	}
	if got := denied.header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("a denied origin received Access-Control-Allow-Origin = %q", got)
	}

	// No Origin header means a script or curl, not a browser: allowed.
	h.doRequest(http.MethodGet, "/health", nil, nil).expect(http.StatusOK)
}

// TestPreflight proves the OPTIONS handshake answers for an allowed origin and is
// refused for a hostile one.
func TestPreflight(t *testing.T) {
	h := newHarness(t)

	preflight := h.doRequest(http.MethodOptions, "/sessions/"+sessionID+"/disable", nil, func(req *http.Request) {
		req.Header.Set("Origin", "http://localhost:8787")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "Authorization")
	})
	preflight.expect(http.StatusNoContent)
	if got := preflight.header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("Access-Control-Allow-Methods = %q", got)
	}
	if got := preflight.header.Get("Access-Control-Max-Age"); got == "" {
		t.Error("Access-Control-Max-Age is unset")
	}

	refused := h.doRequest(http.MethodOptions, "/sessions/x/disable", nil, func(req *http.Request) {
		req.Header.Set("Origin", "https://evil.test")
	})
	refused.expect(http.StatusForbidden)
}

// TestSecureHeaders proves every response carries the hardening headers, even an
// error.
func TestSecureHeaders(t *testing.T) {
	h := newHarness(t)
	for _, resp := range []response{
		h.get("/health"),
		h.get("/does-not-exist"),
		h.doWithToken(http.MethodPut, "/settings", map[string]string{"key": "x", "value": "y"}, ""),
	} {
		expected := map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"X-Frame-Options":              "DENY",
			"Referrer-Policy":              "no-referrer",
			"Cross-Origin-Resource-Policy": "same-origin",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cache-Control":                "no-store",
		}
		for header, want := range expected {
			if got := resp.header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		if got := resp.header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
			t.Errorf("Content-Security-Policy = %q", got)
		}
	}
}

// TestBodyLimits proves an oversized body is refused before it is parsed.
func TestBodyLimits(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *config.Config) { c.MaxBodyBytes = 32 })

	large := h.do(http.MethodPut, "/settings", map[string]string{
		"key":   "big",
		"value": strings.Repeat("x", 512),
	})
	large.expect(http.StatusRequestEntityTooLarge)
	if code := large.errorOf().Code; code != codeTooLarge {
		t.Errorf("code = %q, want %q", code, codeTooLarge)
	}
}

// TestContentTypeIsEnforced proves the API accepts JSON only, which keeps every
// other parser away from request bodies.
func TestContentTypeIsEnforced(t *testing.T) {
	h := newHarness(t)

	form := h.doRequest(http.MethodPut, "/settings", "key=a&value=b", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	})
	form.expect(http.StatusUnsupportedMediaType)
	if code := form.errorOf().Code; code != codeUnsupportedType {
		t.Errorf("code = %q, want %q", code, codeUnsupportedType)
	}
}

// TestUnknownAndMalformedBodies proves the decoder rejects what it does not
// understand. This is the guard that keeps a "command" field from being quietly
// ignored: the field does not exist anywhere.
func TestUnknownAndMalformedBodies(t *testing.T) {
	h := newHarness(t)
	h.makeRecoverable(sessionID)

	unknown := h.do(http.MethodPost, "/sessions/"+sessionID+"/recover", map[string]string{
		"command": "rm -rf /",
	})
	unknown.expect(http.StatusBadRequest)
	if detail := unknown.errorOf().Code; detail != codeInvalid {
		t.Errorf("code = %q, want %q", detail, codeInvalid)
	}

	malformed := h.do(http.MethodPost, "/sessions/"+sessionID+"/recover", "{not json")
	malformed.expect(http.StatusBadRequest)

	double := h.do(http.MethodPost, "/sessions/"+sessionID+"/recover", `{"message":"a"}{"message":"b"}`)
	double.expect(http.StatusBadRequest)

	// An empty body is fine where the body is optional.
	h.doWithToken(http.MethodPost, "/sessions/"+sessionID+"/enable", "", h.token).
		expect(http.StatusOK)
}

// TestRateLimit proves the limiter answers 429 with a Retry-After, and that it
// recovers once the bucket refills.
func TestRateLimit(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *config.Config) {
		c.RateLimit = 1
		c.RateBurst = 2
	})

	var limited response
	for i := 0; i < 10; i++ {
		resp := h.get("/health")
		if resp.status == http.StatusTooManyRequests {
			limited = resp
			break
		}
		if resp.status != http.StatusOK {
			t.Fatalf("unexpected status %d", resp.status)
		}
	}
	if limited.status != http.StatusTooManyRequests {
		t.Fatal("the limiter never engaged")
	}
	if code := limited.errorOf().Code; code != codeRateLimited {
		t.Errorf("code = %q, want %q", code, codeRateLimited)
	}
	retryAfter := limited.header.Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("Retry-After is missing")
	}
	if seconds, err := strconv.Atoi(retryAfter); err != nil || seconds < 1 {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", retryAfter)
	}
}
