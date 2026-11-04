package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// transcriptFixture builds a realistic transcript body.
//
// It mirrors the verified line shapes: CLI metadata lines first (no message),
// then a user prompt and an assistant answer, each carrying sessionId, cwd,
// version, gitBranch and timestamp.
func transcriptFixture(t *testing.T, sessionID, cwd string, lines int) string {
	t.Helper()

	type block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	type line struct {
		Type        string          `json:"type,omitempty"`
		Subtype     string          `json:"subtype,omitempty"`
		UUID        string          `json:"uuid,omitempty"`
		ParentUUID  *string         `json:"parentUuid,omitempty"`
		SessionID   string          `json:"sessionId,omitempty"`
		CWD         string          `json:"cwd,omitempty"`
		Version     string          `json:"version,omitempty"`
		GitBranch   string          `json:"gitBranch,omitempty"`
		IsSidechain bool            `json:"isSidechain,omitempty"`
		Timestamp   string          `json:"timestamp,omitempty"`
		Message     *message        `json:"message,omitempty"`
		Attachment  json.RawMessage `json:"attachment,omitempty"`
	}

	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	out := []line{
		{Type: "last-prompt", SessionID: sessionID},
		{Type: "permission-mode", SessionID: sessionID},
	}
	for i := 0; i < lines; i++ {
		ts := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		switch i % 2 {
		case 0:
			out = append(out, line{
				Type:      "user",
				UUID:      "user-" + strings.Repeat("a", i%3+1),
				SessionID: sessionID,
				CWD:       cwd,
				Version:   "2.1.223",
				GitBranch: "main",
				Timestamp: ts,
				Message:   &message{Role: "user", Content: "prompt " + strings.Repeat("x", i)},
			})
		default:
			out = append(out, line{
				Type:      "assistant",
				UUID:      "asst-" + strings.Repeat("b", i%3+1),
				SessionID: sessionID,
				CWD:       cwd,
				Version:   "2.1.223",
				GitBranch: "main",
				Timestamp: ts,
				Message: &message{Role: "assistant", Content: []block{
					{Type: "thinking", Text: "internal reasoning"},
					{Type: "text", Text: "answer " + strings.Repeat("y", i)},
				}},
			})
		}
	}

	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, l := range out {
		if err := enc.Encode(l); err != nil {
			t.Fatalf("encode fixture: %v", err)
		}
	}
	return b.String()
}

// newTestStore creates a store whose data directory lives in a temp dir.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

// writeTranscript writes a fixture transcript for cwd/sessionID.
func writeTranscript(t *testing.T, store *Store, cwd, sessionID string, lines int) string {
	t.Helper()
	dir := filepath.Join(store.ProjectsDir(), SlugifyCWD(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(transcriptFixture(t, sessionID, cwd, lines)), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func TestSlugifyCWD(t *testing.T) {
	t.Parallel()

	cases := []struct {
		cwd  string
		want string
	}{
		// Verified against a real Claude Code installation.
		{"/root/python_projects/python-password-generator", "-root-python-projects-python-password-generator"},
		{"/root/golang_projects/ccresu", "-root-golang-projects-ccresu"},
		{"/tmp", "-tmp"},
		{"relative/path", "relative-path"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := SlugifyCWD(tc.cwd); got != tc.want {
			t.Errorf("SlugifyCWD(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
	}

	long := "/" + strings.Repeat("a", slugMaxLength+50)
	slug := SlugifyCWD(long)
	if len(slug) > slugMaxLength+1+6 {
		t.Errorf("long slug not truncated: len=%d", len(slug))
	}
	// The leading '/' becomes '-', so the prefix is '-' followed by 199 'a's.
	if !strings.HasPrefix(slug, "-"+strings.Repeat("a", slugMaxLength-1)) {
		t.Errorf("truncated slug prefix wrong: %q", slug[:20])
	}
	if !strings.Contains(slug, "-"+base36(absHash32(long))) {
		t.Errorf("truncated slug must end with the base36 path hash: %q", slug)
	}
	if SlugifyCWD(long) != slug {
		t.Error("SlugifyCWD is not deterministic")
	}
}

func TestDefaultClaudeHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, err := DefaultClaudeHome()
	if err != nil {
		t.Fatalf("DefaultClaudeHome() error = %v", err)
	}
	if !strings.HasSuffix(home, ".claude") {
		t.Errorf("home = %q, want the ~/.claude default", home)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "/custom/claude-config")
	home, err = DefaultClaudeHome()
	if err != nil || home != "/custom/claude-config" {
		t.Fatalf("DefaultClaudeHome() = %q, %v", home, err)
	}

	// NewStore must resolve an empty home through the same rules.
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if store.Home() != "/custom/claude-config" {
		t.Errorf("store home = %q", store.Home())
	}
}

func TestScanTranscript(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	cwd := "/root/project"
	path := writeTranscript(t, store, cwd, testSessionID, 6)

	info, err := store.ScanTranscript(path)
	if err != nil {
		t.Fatalf("ScanTranscript() error = %v", err)
	}
	if info.SessionID != testSessionID {
		t.Errorf("SessionID = %q, want %q", info.SessionID, testSessionID)
	}
	if info.CWD != cwd {
		t.Errorf("CWD = %q, want %q", info.CWD, cwd)
	}
	if info.Version != "2.1.223" || info.GitBranch != "main" {
		t.Errorf("version/branch = %q/%q", info.Version, info.GitBranch)
	}
	if info.Lines != 8 {
		t.Errorf("Lines = %d, want 8", info.Lines)
	}
	if info.EntryCount != 8 {
		t.Errorf("EntryCount = %d, want 8", info.EntryCount)
	}
	if info.Size <= 0 {
		t.Errorf("Size = %d", info.Size)
	}
	if info.Empty {
		t.Error("Empty = true for a populated transcript")
	}
	if info.LastAssistantText == "" || !strings.HasPrefix(info.LastAssistantText, "answer") {
		t.Errorf("LastAssistantText = %q", info.LastAssistantText)
	}
	if info.LastUserText == "" || !strings.HasPrefix(info.LastUserText, "prompt") {
		t.Errorf("LastUserText = %q", info.LastUserText)
	}
	if info.FirstTimestamp.IsZero() || info.LastTimestamp.IsZero() {
		t.Error("timestamps not recorded")
	}
	if !info.LastTimestamp.After(info.FirstTimestamp) {
		t.Errorf("timestamps out of order: %v → %v", info.FirstTimestamp, info.LastTimestamp)
	}
	if info.ModTime.IsZero() {
		t.Error("ModTime not recorded")
	}
	if info.ProjectDir != SlugifyCWD(cwd) {
		t.Errorf("ProjectDir = %q", info.ProjectDir)
	}
}

func TestScanTranscriptSkippingJunk(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	dir := filepath.Join(store.ProjectsDir(), "-root-junk")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, testSessionID+".jsonl")
	body := `not json at all
{"type":"assistant","sessionId":"` + testSessionID + `","cwd":"/root/junk","message":{"role":"assistant","content":"plain string"}}

{"type":"summary","summary":"a summary line"}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := store.ScanTranscript(path)
	if err != nil {
		t.Fatalf("ScanTranscript() error = %v", err)
	}
	if info.EntryCount != 2 {
		t.Errorf("EntryCount = %d, want 2 (junk and blank lines skipped)", info.EntryCount)
	}
	if info.Lines != 4 {
		t.Errorf("Lines = %d, want 4", info.Lines)
	}
	if info.LastAssistantText != "plain string" {
		t.Errorf("LastAssistantText = %q", info.LastAssistantText)
	}
}

func TestScanTranscriptMissing(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	if _, err := store.ScanTranscript(filepath.Join(store.Home(), "nope.jsonl")); err == nil {
		t.Fatal("expected an error for a missing transcript")
	}
}

func TestStoreListAndGet(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	writeTranscript(t, store, "/root/project", testSessionID, 4)
	writeTranscript(t, store, "/root/other", testOtherSessionID, 2)
	if err := os.MkdirAll(store.SessionEnvPath(testOtherSessionID), 0o700); err != nil {
		t.Fatalf("mkdir env: %v", err)
	}

	records, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	byID := map[string]SessionRecord{}
	for _, record := range records {
		byID[record.ID] = record
	}
	if rec, ok := byID[testSessionID]; !ok || rec.Transcript.Path == "" || rec.Transcript.CWD != "/root/project" {
		t.Errorf("first record mapped badly: %+v", rec)
	}
	rec := byID[testOtherSessionID]
	if rec.EnvPath == "" {
		t.Error("session-env directory not recorded")
	}
	if !store.HasSessionEnv(testOtherSessionID) {
		t.Error("HasSessionEnv() = false, want true")
	}
	if store.HasSessionEnv(testSessionID) {
		t.Error("HasSessionEnv() = true for a session without env")
	}

	got, err := store.Get(testSessionID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Transcript.SessionID != testSessionID || got.CWD() != "/root/project" {
		t.Errorf("Get() = %+v", got)
	}

	if _, err := store.Get("not-a-uuid"); !errors.Is(err, ErrInvalidSessionID) {
		t.Errorf("Get(invalid) error = %v, want ErrInvalidSessionID", err)
	}
	if _, err := store.Get("11111111-2222-4333-8444-555555555555"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Get(unknown) error = %v, want ErrSessionNotFound", err)
	}
}

func TestStoreListMissingHome(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	records, err := store.List()
	if err != nil {
		t.Fatalf("List() on an empty home error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %d, want 0", len(records))
	}
}

func TestStoreIgnoresUnrelatedEntries(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)

	// A project directory with a non-session file and a non-UUID transcript.
	dir := filepath.Join(store.ProjectsDir(), "-root-mixed")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"README.md", "notes.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// A non-directory, non-UUID entry in session-env.
	if err := os.MkdirAll(store.SessionEnvDir(), 0o700); err != nil {
		t.Fatalf("mkdir env root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.SessionEnvDir(), "garbage"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	records, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %+v, want none", records)
	}
}

func TestTranscriptPathHelpers(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	cwd := "/root/project"
	path := writeTranscript(t, store, cwd, testSessionID, 2)

	found, err := store.TranscriptPath(testSessionID)
	if err != nil {
		t.Fatalf("TranscriptPath() error = %v", err)
	}
	if found != path {
		t.Errorf("TranscriptPath() = %q, want %q", found, path)
	}
	if want := store.TranscriptPathForCWD(cwd, testSessionID); want != path {
		t.Errorf("TranscriptPathForCWD() = %q, want %q", want, path)
	}
	if _, err := store.TranscriptPath("nope"); !errors.Is(err, ErrInvalidSessionID) {
		t.Errorf("TranscriptPath(invalid) error = %v", err)
	}
	if _, err := store.TranscriptPath("11111111-2222-4333-8444-555555555555"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("TranscriptPath(unknown) error = %v", err)
	}
}

func TestReadEntriesSince(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	path := writeTranscript(t, store, "/root/project", testSessionID, 4)

	entries, offset, err := store.ReadEntriesSince(path, 0)
	if err != nil {
		t.Fatalf("ReadEntriesSince() error = %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("entries = %d, want 6", len(entries))
	}
	if offset <= 0 {
		t.Fatalf("offset = %d, want the file size", offset)
	}

	// Nothing new yet.
	entries, offset2, err := store.ReadEntriesSince(path, offset)
	if err != nil {
		t.Fatalf("second ReadEntriesSince() error = %v", err)
	}
	if len(entries) != 0 || offset2 != offset {
		t.Fatalf("expected no new entries, got %d (offset %d → %d)", len(entries), offset, offset2)
	}

	// Append a turn: this is the proof a resume actually advanced.
	extra := `{"type":"assistant","sessionId":"` + testSessionID + `","cwd":"/root/project","timestamp":"2026-06-01T11:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"resumed turn"}]}}` + "\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := file.WriteString(extra); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, offset3, err := store.ReadEntriesSince(path, offset)
	if err != nil {
		t.Fatalf("third ReadEntriesSince() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Text != "resumed turn" {
		t.Fatalf("entries = %+v, want the appended turn", entries)
	}
	if offset3 <= offset2 {
		t.Errorf("offset did not grow: %d → %d", offset2, offset3)
	}

	// An out-of-range offset restarts from the beginning instead of failing.
	entries, _, err = store.ReadEntriesSince(path, 1<<40)
	if err != nil {
		t.Fatalf("ReadEntriesSince(huge offset) error = %v", err)
	}
	if len(entries) != 7 {
		t.Errorf("entries = %d, want 7", len(entries))
	}
}

func TestTailEntries(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	path := writeTranscript(t, store, "/root/project", testSessionID, 10)

	entries, err := store.TailEntries(path, 3)
	if err != nil {
		t.Fatalf("TailEntries() error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[len(entries)-1].Type != "assistant" {
		t.Errorf("last entry type = %q, want assistant", entries[len(entries)-1].Type)
	}

	if got, err := store.TailEntries(path, 0); err != nil || got != nil {
		t.Errorf("TailEntries(0) = %v, %v", got, err)
	}
	if _, err := store.TailEntries(filepath.Join(store.Home(), "missing.jsonl"), 3); err == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestMessageText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", ``, ""},
		{"plain string", `"hello"`, "hello"},
		{"not a string or array", `42`, ""},
		{"text blocks", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb"},
		{"thinking ignored", `[{"type":"thinking","text":"secret"},{"type":"text","text":"visible"}]`, "visible"},
		{"tool_result flattened", `[{"type":"tool_result","content":[{"type":"text","text":"output"}]}]`, "output"},
		{"blank text dropped", `[{"type":"text","text":"  "}]`, ""},
		{"invalid json", `[`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := messageText(json.RawMessage(tc.input)); got != tc.want {
				t.Errorf("messageText(%s) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseEntry(t *testing.T) {
	t.Parallel()

	entry, err := parseEntry([]byte(`{"type":"assistant","subtype":"final","uuid":"u1","parentUuid":"p1",` +
		`"sessionId":"` + testSessionID + `","cwd":"/w","version":"2.1.223","gitBranch":"main",` +
		`"isSidechain":true,"isMeta":true,"timestamp":"2026-06-01T10:00:00Z",` +
		`"message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`))
	if err != nil {
		t.Fatalf("parseEntry() error = %v", err)
	}
	if entry.Type != "assistant" || entry.Subtype != "final" || entry.UUID != "u1" || entry.ParentUUID != "p1" {
		t.Errorf("identity fields wrong: %+v", entry)
	}
	if entry.SessionID != testSessionID || entry.CWD != "/w" || entry.GitBranch != "main" {
		t.Errorf("context fields wrong: %+v", entry)
	}
	if !entry.IsSidechain || !entry.IsMeta {
		t.Errorf("flag fields wrong: %+v", entry)
	}
	if entry.Text != "hi" || entry.Role != "assistant" {
		t.Errorf("message fields wrong: %+v", entry)
	}
	if !entry.Timestamp.Equal(time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("timestamp = %v", entry.Timestamp)
	}

	if _, err := parseEntry([]byte("{")); !errors.Is(err, ErrMalformedOutput) {
		t.Errorf("parseEntry(broken) error = %v, want ErrMalformedOutput", err)
	}
}
