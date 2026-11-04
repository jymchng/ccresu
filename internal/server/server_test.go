package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/config"
)

// TestNewRejectsUnusableConfigurations proves the constructor fails loudly rather
// than starting an API nobody can secure.
func TestNewRejectsUnusableConfigurations(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "api-token")
	base := func() Config {
		h := newHarness(t)
		return Config{
			Addr:      "127.0.0.1:0",
			TokenPath: tokenPath,
			Store:     h.store,
		}
	}

	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no store", func(c *Config) { c.Store = nil }, "a store is required"},
		{"bad address", func(c *Config) { c.Addr = "not-an-address" }, "not-an-address"},
		{"empty port", func(c *Config) { c.Addr = "127.0.0.1:" }, "has no port"},
		{"wildcard origin", func(c *Config) { c.AllowedOrigins = []string{"*"} }, "wildcard"},
		{"origin without scheme", func(c *Config) { c.AllowedOrigins = []string{"example.com"} }, "must be http or https"},
		{"origin with a path", func(c *Config) { c.AllowedOrigins = []string{"https://example.com/app"} }, "must not include a path"},
		{"no token path", func(c *Config) { c.TokenPath = "  " }, "token_path is required"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := base()
			testCase.mutate(&cfg)
			server, err := New(cfg)
			if err == nil {
				t.Fatalf("New accepted an unusable configuration: %+v", server)
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %q, want it to mention %q", err, testCase.want)
			}
		})
	}
}

// TestNormalizeAddr proves the API never binds beyond loopback by accident and
// that the port is validated.
func TestNormalizeAddr(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", DefaultAddr, false},
		{"   ", DefaultAddr, false},
		{":8787", "127.0.0.1:8787", false},
		{"127.0.0.1:0", "127.0.0.1:0", false},
		{"localhost:9999", "localhost:9999", false},
		{"[::1]:8080", "[::1]:8080", false},
		{"garbage", "", true},
		{"host:", "", true},
	}
	for _, testCase := range cases {
		got, err := normalizeAddr(testCase.in)
		if testCase.wantErr {
			if err == nil {
				t.Errorf("normalizeAddr(%q) = %q, want an error", testCase.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeAddr(%q): %v", testCase.in, err)
			continue
		}
		if got != testCase.want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}

// TestTokenLifecycle covers creation, reuse, tightening and the rejection of a
// placeholder token.
func TestTokenLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "api-token")

	h := newHarness(t)
	cfg := Config{Addr: "127.0.0.1:0", TokenPath: path, Store: h.store}
	first, err := New(cfg)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	token, mode := readToken(t, path)
	if mode != 0o600 {
		t.Fatalf("token mode = %o, want 600", mode)
	}
	if len(token) != tokenBytes*2 {
		t.Fatalf("token length = %d, want %d hex characters", len(token), tokenBytes*2)
	}
	if token != first.token {
		t.Fatal("the server token does not match the file")
	}

	// A second start reuses the same token, so a client keeps working across a
	// restart.
	second, err := New(cfg)
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	if second.token != token {
		t.Fatal("restart changed the token")
	}

	// A loosened mode is corrected instead of trusted.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := New(cfg); err != nil {
		t.Fatalf("New after chmod: %v", err)
	}
	if _, mode := readToken(t, path); mode != 0o600 {
		t.Fatalf("mode after restart = %o, want 600", mode)
	}

	// A short or empty file is refused rather than accepted as a weak secret.
	for _, content := range []string{"", "   ", "short"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := New(cfg); !errors.Is(err, ErrNoToken) {
			t.Fatalf("token %q: error = %v, want ErrNoToken", content, err)
		}
	}
}

// TestTokenGenerationIsRandom proves two servers do not share a token.
func TestTokenGenerationIsRandom(t *testing.T) {
	first, err := generateToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	second, err := generateToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if first == second {
		t.Fatal("two generated tokens are identical")
	}
	if len(first) != tokenBytes*2 {
		t.Fatalf("length = %d, want %d", len(first), tokenBytes*2)
	}
}

// TestRunServesAndShutsDown proves the lifecycle: the listener comes up, requests
// are answered, and cancelling the context stops everything with no goroutine
// left behind.
func TestRunServesAndShutsDown(t *testing.T) {
	h := newHarness(t)
	server := h.server
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()

	waitFor(t, "the listener", func() bool { return server.Addr() != "" })
	addr := server.Addr()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("bound address %q is not loopback", addr)
	}

	resp, err := http.Get("http://" + addr + APIPrefix + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want nil after a graceful stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
	if server.hub.subscribers() != 0 {
		t.Fatalf("%d subscribers outlived the server", server.hub.subscribers())
	}
}

// TestRunRejectsANilContext proves the contract is checked rather than panicking.
func TestRunRejectsANilContext(t *testing.T) {
	//lint:ignore SA1012 the nil context is the case under test
	if err := newHarness(t).server.Run(nil); err == nil {
		t.Fatal("Run(nil) returned no error")
	}
}

// TestRunReportsABindFailure proves a port already in use is reported, not
// swallowed.
func TestRunReportsABindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	h := newHarness(t, func(c *Config, _ *config.Config) { c.Addr = listener.Addr().String() })
	err = h.server.Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded on a busy port")
	}
	if !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("error = %q, want it to name the listen failure", err)
	}
}

// TestRoutingErrors proves unknown paths, wrong methods and paths outside the
// prefix all answer with the JSON envelope.
func TestRoutingErrors(t *testing.T) {
	h := newHarness(t)

	outside := h.doRequest(http.MethodGet, "/elsewhere", nil, nil)
	if outside.status != http.StatusOK && outside.status != http.StatusNotFound {
		t.Fatalf("unexpected status %d outside the prefix", outside.status)
	}
	body := h.doRequestRaw("/not-api")
	outside = body
	outside.expect(http.StatusNotFound)
	if code := outside.errorOf().Code; code != codeNotFound {
		t.Errorf("code = %q, want %q", code, codeNotFound)
	}

	// A known path with the wrong method is a 405 with an Allow header.
	wrongMethod := h.doRequest(http.MethodDelete, "/status", nil, nil)
	wrongMethod.expect(http.StatusMethodNotAllowed)
	if allow := wrongMethod.header.Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, want it to list GET", allow)
	}
	if code := wrongMethod.errorOf().Code; code != codeMethod {
		t.Errorf("code = %q, want %q", code, codeMethod)
	}

	// An unknown path under the prefix is a 404.
	missing := h.doRequest(http.MethodGet, "/nope", nil, nil)
	missing.expect(http.StatusNotFound)
	if code := missing.errorOf().Code; code != codeNotFound {
		t.Errorf("code = %q, want %q", code, codeNotFound)
	}
}

// doRequestRaw performs a GET against an absolute URL path, bypassing the API
// prefix, so the prefix guard itself is exercised.
func (h *harness) doRequestRaw(path string) response {
	h.t.Helper()
	url := h.serve() + path
	resp, err := http.Get(url)
	if err != nil {
		h.t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload := make([]byte, 0, 256)
	buffer := make([]byte, 256)
	for {
		n, readErr := resp.Body.Read(buffer)
		payload = append(payload, buffer[:n]...)
		if readErr != nil {
			break
		}
	}
	return response{t: h.t, status: resp.StatusCode, header: resp.Header.Clone(), body: payload}
}

// TestPanicBecomesAServerError proves a handler panic is contained: the response
// is a JSON 500 and the process keeps serving.
func TestPanicBecomesAServerError(t *testing.T) {
	h := newHarness(t)
	h.server.routes = append(h.server.routes, route{
		method: http.MethodGet,
		parts:  []string{"boom"},
		handler: func(*Server, http.ResponseWriter, *http.Request, params) {
			panic("deliberate test panic")
		},
	})

	boom := h.get("/boom")
	boom.expect(http.StatusInternalServerError)
	if code := boom.errorOf().Code; code != codeInternal {
		t.Errorf("code = %q, want %q", code, codeInternal)
	}
	// The server still answers afterwards.
	h.get("/health").expect(http.StatusOK)
}

// TestSplitPath covers the prefix parsing directly.
func TestSplitPath(t *testing.T) {
	cases := []struct {
		path     string
		segments []string
		prefixed bool
	}{
		{"/api/v1/health", []string{"health"}, true},
		{"/api/v1", nil, true},
		{"/api/v1/", nil, true},
		{"/api/v1/sessions/abc", []string{"sessions", "abc"}, true},
		{"/api/v2/health", nil, false},
		{"/health", nil, false},
	}
	for _, testCase := range cases {
		segments, prefixed := splitPath(testCase.path)
		if prefixed != testCase.prefixed {
			t.Errorf("%s: prefixed = %v, want %v", testCase.path, prefixed, testCase.prefixed)
		}
		if len(segments) != len(testCase.segments) {
			t.Errorf("%s: segments = %v, want %v", testCase.path, segments, testCase.segments)
			continue
		}
		for i := range segments {
			if segments[i] != testCase.segments[i] {
				t.Errorf("%s: segments = %v, want %v", testCase.path, segments, testCase.segments)
				break
			}
		}
	}
}

// TestOriginAllowed covers the comparison rules.
func TestOriginAllowed(t *testing.T) {
	h := newHarness(t)
	allowed := []string{"https://docs.example.test", "HTTP://LocalHost:8787/"}
	for _, origin := range allowed {
		if !h.server.originAllowed(origin) {
			t.Errorf("%q should be allowed", origin)
		}
	}
	for _, origin := range []string{"https://evil.test", "https://docs.example.test.evil.test", ""} {
		if h.server.originAllowed(origin) {
			t.Errorf("%q should not be allowed", origin)
		}
	}
}
