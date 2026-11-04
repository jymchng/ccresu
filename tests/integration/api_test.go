package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// apiPrefix mirrors the documented route prefix. It is spelled out rather than
// imported from internal/server so this suite fails if the published contract
// moves, instead of following it silently.
const apiPrefix = "/api/v1"

// TestServeServesTheLiveAPIUntilInterrupted runs `ccresu serve` as its own process
// and drives the API over TCP.
//
// Everything between the command line and the wire is real: flag parsing, the
// configuration file, the SQLite file, the token file, the middleware chain, the
// event feed and the graceful shutdown. Nothing is mounted on httptest.
func TestServeServesTheLiveAPIUntilInterrupted(t *testing.T) {
	f := newFixture(t)
	// The session the API will list and mutate is written by the test process,
	// which also proves both processes agree on the on-disk schema.
	f.seedSession(t, seededSessionID, nil)

	proc := f.startProcess("serve", "--config", f.ConfigPath, "--addr", fmt.Sprintf("127.0.0.1:%d", f.Port))
	base := fmt.Sprintf("http://127.0.0.1:%d", f.Port)
	client := &http.Client{Timeout: 10 * time.Second}

	// The listener is up when /health answers; the printed line alone is not a
	// readiness signal a user should have to trust.
	waitForHealth(t, client, base+apiPrefix+"/health")

	// --- Read-only surface -------------------------------------------------

	health := getJSON(t, client, base+apiPrefix+"/health", nil)
	if health["status"] != "ok" {
		t.Fatalf("health = %v, want ok", health)
	}
	if health["api"] != apiPrefix {
		t.Fatalf("health api = %v, want %q", health["api"], apiPrefix)
	}

	version := getJSON(t, client, base+apiPrefix+"/version", nil)
	if version["version"] != "dev" {
		t.Fatalf("version = %v, want the built-in default dev", version["version"])
	}

	status := getJSON(t, client, base+apiPrefix+"/status", nil)
	if got := status["sessions"]; got != float64(1) {
		t.Fatalf("status sessions = %v, want 1", got)
	}

	sessions := getJSON(t, client, base+apiPrefix+"/sessions", nil)
	listed, ok := sessions["sessions"].([]any)
	if !ok || len(listed) != 1 {
		t.Fatalf("sessions = %v, want exactly one", sessions)
	}
	first, _ := listed[0].(map[string]any)
	if first["id"] != seededSessionID {
		t.Fatalf("session id = %v, want %q", first["id"], seededSessionID)
	}
	if first["state"] != string(storage.StateRunning) {
		t.Fatalf("session state = %v, want RUNNING", first["state"])
	}

	single := getJSON(t, client, base+apiPrefix+"/sessions/"+seededSessionID, nil)
	if session, _ := single["session"].(map[string]any); session == nil || session["id"] != seededSessionID {
		t.Fatalf("session detail = %v", single)
	}
	history := getJSON(t, client, base+apiPrefix+"/sessions/"+seededSessionID+"/history", nil)
	if _, present := history["attempts"]; !present {
		t.Fatalf("history has no attempts field: %v", history)
	}
	getJSON(t, client, base+apiPrefix+"/recoveries", nil)
	getJSON(t, client, base+apiPrefix+"/settings", nil)
	getJSON(t, client, base+apiPrefix+"/telegram/status", nil)

	// The configuration document is served with its credentials redacted, never
	// echoed: the token fields carry a note instead of a value.
	config := getJSON(t, client, base+apiPrefix+"/config", nil)
	if config["config_path"] != f.ConfigPath {
		t.Fatalf("config endpoint config_path = %v, want %q", config["config_path"], f.ConfigPath)
	}
	telegram, ok := config["telegram"].(map[string]any)
	if !ok {
		t.Fatalf("config endpoint has no telegram section: %v", config)
	}
	if token := fmt.Sprint(telegram["bot_token"]); token == "" || strings.Contains(token, "SUPER-SECRET") {
		t.Fatalf("config endpoint leaked or omitted the bot token: %v", telegram["bot_token"])
	}

	// --- Authentication and CORS ------------------------------------------

	// A state-changing request without the bearer token is refused before it can
	// touch the store.
	unauthorized := doRequest(t, client, http.MethodPost, base+apiPrefix+"/sessions/"+seededSessionID+"/disable", nil, nil)
	if unauthorized.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated disable = %d, want 401 (%s)", unauthorized.status, unauthorized.body)
	}
	if session := f.readSession(t, seededSessionID); !session.Enabled {
		t.Fatal("an unauthenticated request changed the session")
	}

	// A browser Origin outside the allowlist is refused even for a read.
	blocked := doRequest(t, client, http.MethodGet, base+apiPrefix+"/status", nil, map[string]string{
		"Origin": "https://attacker.example",
	})
	if blocked.status != http.StatusForbidden {
		t.Fatalf("cross-origin read = %d, want 403 (%s)", blocked.status, blocked.body)
	}

	allowedOrigin := fmt.Sprintf("http://127.0.0.1:%d", f.Port)
	allowed := doRequest(t, client, http.MethodGet, base+apiPrefix+"/status", nil, map[string]string{
		"Origin": allowedOrigin,
	})
	if allowed.status != http.StatusOK {
		t.Fatalf("allow-listed origin = %d, want 200 (%s)", allowed.status, allowed.body)
	}
	if got := allowed.header.Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, allowedOrigin)
	}

	// The token file the server created is the credential the API expects, and it
	// is readable only by its owner.
	token, mode := readTokenFile(t, f.TokenPath)
	if len(token) < 32 {
		t.Fatalf("api token is %d characters, want at least 32", len(token))
	}
	if mode != 0o600 {
		t.Fatalf("api token mode = %04o, want 0600", mode)
	}

	// --- Authenticated mutations ------------------------------------------

	authorizedHeaders := map[string]string{"Authorization": "Bearer " + token}
	disabled := doRequest(t, client, http.MethodPost, base+apiPrefix+"/sessions/"+seededSessionID+"/disable", nil, authorizedHeaders)
	if disabled.status != http.StatusOK {
		t.Fatalf("disable = %d, want 200 (%s)", disabled.status, disabled.body)
	}
	if session := f.readSession(t, seededSessionID); session.Enabled {
		t.Fatal("disable through the live API did not persist")
	}

	enabled := doRequest(t, client, http.MethodPost, base+apiPrefix+"/sessions/"+seededSessionID+"/enable", nil, authorizedHeaders)
	if enabled.status != http.StatusOK {
		t.Fatalf("enable = %d, want 200 (%s)", enabled.status, enabled.body)
	}
	if session := f.readSession(t, seededSessionID); !session.Enabled {
		t.Fatal("enable through the live API did not persist")
	}

	// The self-test endpoint reaches the store, the recovery engine and the
	// version probe through the same process the daemon uses.
	test := doRequest(t, client, http.MethodPost, base+apiPrefix+"/test", strings.NewReader(`{"telegram":true}`),
		withJSON(authorizedHeaders))
	if test.status != http.StatusOK {
		t.Fatalf("test = %d, want 200 (%s)", test.status, test.body)
	}
	var selfTest struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"checks"`
	}
	decodeBody(t, test, &selfTest)
	if !selfTest.OK {
		t.Fatalf("self-test reported a failure: %s", test.body)
	}
	names := make(map[string]bool, len(selfTest.Checks))
	for _, check := range selfTest.Checks {
		names[check.Name] = true
	}
	for _, want := range []string{"database", "recovery", "claude"} {
		if !names[want] {
			t.Fatalf("self-test did not check %q: %s", want, test.body)
		}
	}

	// --- Error envelopes ---------------------------------------------------

	notFound := doRequest(t, client, http.MethodGet, base+apiPrefix+"/does-not-exist", nil, nil)
	if notFound.status != http.StatusNotFound {
		t.Fatalf("unknown endpoint = %d, want 404", notFound.status)
	}
	method := doRequest(t, client, http.MethodDelete, base+apiPrefix+"/status", nil, nil)
	if method.status != http.StatusMethodNotAllowed {
		t.Fatalf("unsupported method = %d, want 405", method.status)
	}
	if got := method.header.Get("Allow"); !strings.Contains(got, http.MethodGet) {
		t.Fatalf("405 Allow header = %q, want GET", got)
	}

	// --- Server-sent events ------------------------------------------------

	// A real stream: the ready frame plus the keep-alive comment prove the feed
	// goroutine and the flush path work over a live socket.
	streamCtx, cancelStream := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStream()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, base+apiPrefix+"/events", nil)
	if err != nil {
		t.Fatalf("build events request: %v", err)
	}
	stream, err := client.Do(req)
	if err != nil {
		t.Fatalf("open the event stream: %v", err)
	}
	if stream.StatusCode != http.StatusOK {
		_ = stream.Body.Close()
		t.Fatalf("events = %d, want 200", stream.StatusCode)
	}
	if contentType := stream.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		_ = stream.Body.Close()
		t.Fatalf("events content type = %q", contentType)
	}
	var opened strings.Builder
	reader := bufio.NewReader(stream.Body)
	for i := 0; i < 3 && !strings.Contains(opened.String(), "event: ready"); i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read the event stream: %v (got %q)", err, opened.String())
		}
		opened.WriteString(line)
	}
	if !strings.Contains(opened.String(), "event: ready") {
		t.Fatalf("the event stream did not open with a ready frame: %q", opened.String())
	}
	// A well-behaved client hangs up, which is also what lets the graceful
	// shutdown below finish without waiting out its deadline.
	_ = stream.Body.Close()
	cancelStream()
	waitForClosedStream(t, client, base+apiPrefix+"/health")

	// --- Graceful shutdown -------------------------------------------------

	proc.waitForOutput(t, "ccresu API listening on")
	proc.interrupt(t)
	if code := proc.waitExit(t, 20*time.Second); code != 0 {
		stdout, stderr := proc.output()
		t.Fatalf("serve exited %d after SIGINT\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	stdout, stderr := proc.output()
	if !strings.Contains(stdout, "ccresu API stopped") {
		t.Fatalf("serve did not report a clean shutdown: %q (stderr %q)", stdout, stderr)
	}
	if !portClosed(f.Port) {
		t.Fatalf("port %d is still open after serve exited", f.Port)
	}

	// The mutation survives the process: read it back straight from the file.
	if session := f.readSession(t, seededSessionID); !session.Enabled {
		t.Fatal("the session enabled through the API was not persisted")
	}
}

// TestServeUsesTheDefaultConfigurationAddress proves the --addr override is an
// override: without it the configured host:port is what the API binds.
func TestServeUsesTheDefaultConfigurationAddress(t *testing.T) {
	f := newFixture(t)
	proc := f.startProcess("serve", "--config", f.ConfigPath)
	defer proc.interrupt(t)

	proc.waitForOutput(t, fmt.Sprintf("ccresu API listening on 127.0.0.1:%d", f.Port))
	client := &http.Client{Timeout: 10 * time.Second}
	waitForHealth(t, client, fmt.Sprintf("http://127.0.0.1:%d%s/health", f.Port, apiPrefix))
}

// waitForHealth polls the health endpoint until the API answers, so a slow start
// is a wait rather than a flaky failure.
func waitForHealth(t *testing.T, client *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the API never became healthy at %s: %v", url, lastErr)
}

// waitForClosedStream waits until the API answers again, which shows the closed
// stream released its handler rather than leaving it pinned to the connection.
func waitForClosedStream(t *testing.T, client *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the API stopped answering after the event stream closed: %s", url)
}

// apiResponse is one decoded API reply.
type apiResponse struct {
	status int
	header http.Header
	body   string
}

// decodeBody unmarshals a reply body.
func decodeBody(t *testing.T, resp apiResponse, dst any) {
	t.Helper()
	if err := json.Unmarshal([]byte(resp.body), dst); err != nil {
		t.Fatalf("decode %q: %v", resp.body, err)
	}
}

// doRequest performs one API request with optional headers.
func doRequest(t *testing.T, client *http.Client, method, url string, body io.Reader, headers map[string]string) apiResponse {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, url, err)
	}
	return apiResponse{status: resp.StatusCode, header: resp.Header.Clone(), body: string(payload)}
}

// getJSON performs a GET and decodes the JSON object it returns.
func getJSON(t *testing.T, client *http.Client, url string, headers map[string]string) map[string]any {
	t.Helper()
	resp := doRequest(t, client, http.MethodGet, url, nil, headers)
	if resp.status != http.StatusOK {
		t.Fatalf("GET %s = %d (%s)", url, resp.status, resp.body)
	}
	var decoded map[string]any
	decodeBody(t, resp, &decoded)
	return decoded
}

// withJSON adds the JSON content type to a header set.
func withJSON(headers map[string]string) map[string]string {
	copied := make(map[string]string, len(headers)+1)
	for name, value := range headers {
		copied[name] = value
	}
	copied["Content-Type"] = "application/json"
	return copied
}

// readTokenFile returns the token and its permission bits.
func readTokenFile(t *testing.T, path string) (string, os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the api token: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the api token: %v", err)
	}
	return strings.TrimSpace(string(data)), info.Mode().Perm()
}
