package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// On-disk layout (verified with Claude Code 2.1.223):
//
//	~/.claude/projects/<slugified-cwd>/<session-id>.jsonl   session transcript
//	~/.claude/session-env/<session-id>/                     per-session environment
//
// The project directory name is produced by replacing every character outside
// [a-zA-Z0-9] with '-', truncating to 200 characters and appending "-" plus the
// absolute value of a 32-bit Java-style string hash in base 36 for long paths.
const (
	// slugMaxLength is the pre-hash truncation length used by the CLI.
	slugMaxLength = 200
	// transcriptExt is the transcript file extension.
	transcriptExt = ".jsonl"
	// tailWindow is how many trailing transcript lines are decoded for status.
	tailWindow = 24
	// tailBytesLimit caps the bytes retained for the trailing window, so one
	// enormous tool-result line cannot exhaust memory.
	tailBytesLimit = 4 << 20
	// headWindow is how many leading transcript lines are decoded for metadata.
	headWindow = 24
)

// DefaultClaudeHome returns the Claude Code data directory, honouring
// $CLAUDE_CONFIG_DIR and falling back to ~/.claude.
func DefaultClaudeHome() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claude: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

// SlugifyCWD reproduces the CLI's project-directory naming exactly.
func SlugifyCWD(cwd string) string {
	replaced := make([]byte, 0, len(cwd))
	for i := 0; i < len(cwd); i++ {
		c := cwd[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			replaced = append(replaced, c)
			continue
		}
		replaced = append(replaced, '-')
	}
	slug := string(replaced)
	if len(slug) <= slugMaxLength {
		return slug
	}
	return fmt.Sprintf("%s-%s", slug[:slugMaxLength], base36(absHash32(cwd)))
}

// hash32 is the Java-style 31-bit string hash the CLI uses.
func hash32(s string) int32 {
	var h int32
	for _, r := range s {
		h = (h << 5) - h + int32(r)
	}
	return h
}

// absHash32 returns |hash32(s)| as an int64.
func absHash32(s string) int64 {
	h := int64(hash32(s))
	if h < 0 {
		h = -h
	}
	return h
}

// base36 renders n in base 36.
func base36(n int64) string {
	if n == 0 {
		return "0"
	}
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	var digits []byte
	for n > 0 {
		digits = append(digits, alphabet[n%36])
		n /= 36
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

// Entry is one decoded transcript line.
//
// Verified transcript lines carry a "type" ("user", "assistant", "summary",
// "system", "attachment", "last-prompt", "permission-mode", ...), and the
// conversation-bearing ones also carry "sessionId", "cwd", "version",
// "gitBranch", "timestamp" and "message".
type Entry struct {
	// Type is the line type.
	Type string
	// Subtype is the optional subtype.
	Subtype string
	// UUID is the line identifier.
	UUID string
	// ParentUUID is the parent line identifier.
	ParentUUID string
	// Timestamp is the line timestamp in UTC; zero when absent or invalid.
	Timestamp time.Time
	// SessionID is the session the line belongs to.
	SessionID string
	// CWD is the working directory recorded on the line.
	CWD string
	// Version is the Claude Code version that wrote the line.
	Version string
	// GitBranch is the branch recorded on the line.
	GitBranch string
	// Role is the message role when the line carries a message.
	Role string
	// Text is the flattened message text.
	Text string
	// IsSidechain reports whether the line belongs to a sub-agent sidechain.
	IsSidechain bool
	// IsMeta reports whether the line is CLI metadata rather than conversation.
	IsMeta bool
	// Raw preserves the original JSON line.
	Raw json.RawMessage
}

// entryWire is the permissive wire form of one transcript line.
type entryWire struct {
	Type        flexString `json:"type"`
	Subtype     flexString `json:"subtype"`
	UUID        flexString `json:"uuid"`
	ParentUUID  flexString `json:"parentUuid"`
	SessionID   flexString `json:"sessionId"`
	CWD         flexString `json:"cwd"`
	Version     flexString `json:"version"`
	GitBranch   flexString `json:"gitBranch"`
	IsSidechain flexBool   `json:"isSidechain"`
	IsMeta      flexBool   `json:"isMeta"`
	Timestamp   flexTime   `json:"timestamp"`
	Message     struct {
		Role    flexString      `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

// parseEntry decodes one transcript line. Malformed lines yield an error; the
// caller decides whether to skip them.
func parseEntry(line []byte) (Entry, error) {
	var wire entryWire
	if err := json.Unmarshal(line, &wire); err != nil {
		return Entry{}, fmt.Errorf("%w: transcript line: %v", ErrMalformedOutput, err)
	}
	entry := Entry{
		Type:        wire.Type.String(),
		Subtype:     wire.Subtype.String(),
		UUID:        wire.UUID.String(),
		ParentUUID:  wire.ParentUUID.String(),
		SessionID:   wire.SessionID.String(),
		CWD:         wire.CWD.String(),
		Version:     wire.Version.String(),
		GitBranch:   wire.GitBranch.String(),
		IsSidechain: wire.IsSidechain.Bool(),
		IsMeta:      wire.IsMeta.Bool(),
		Timestamp:   wire.Timestamp.Time(),
		Role:        wire.Message.Role.String(),
		Text:        messageText(wire.Message.Content),
		Raw:         append(json.RawMessage(nil), line...),
	}
	return entry, nil
}

// messageText flattens a message content value.
//
// Content is either a string or an array of blocks; only "text" blocks are
// conversation text, while "tool_use"/"tool_result"/"thinking" blocks are
// retained as rendered summaries so the detector can still see interrupted
// turns without confusing tool output for prose.
func messageText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
		return ""
	}
	if trimmed[0] != '[' {
		return ""
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		case "tool_result":
			if text := strings.TrimSpace(messageText(block.Content)); text != "" {
				parts = append(parts, text)
			}
		case "thinking", "redacted_thinking":
			// Not conversation text; never classified by the detector.
		}
	}
	return strings.Join(parts, "\n")
}

// TranscriptInfo is the metadata ccresu needs from a transcript file.
type TranscriptInfo struct {
	// Path is the transcript file.
	Path string
	// SessionID is the session UUID, taken from the file name and confirmed by
	// the lines when they carry one.
	SessionID string
	// ProjectDir is the ~/.claude/projects/<slug> directory name.
	ProjectDir string
	// CWD is the working directory recorded in the transcript.
	CWD string
	// Version is the Claude Code version that wrote the newest lines.
	Version string
	// GitBranch is the branch recorded in the newest lines.
	GitBranch string
	// Size is the file size in bytes.
	Size int64
	// Lines is the number of newline-terminated lines.
	Lines int64
	// EntryCount is the number of decoded (head and tail) lines.
	EntryCount int
	// FirstTimestamp is the oldest decoded timestamp.
	FirstTimestamp time.Time
	// LastTimestamp is the newest decoded timestamp.
	LastTimestamp time.Time
	// LastEntryType is the type of the newest line.
	LastEntryType string
	// LastAssistantText is the newest assistant text, used by the detector.
	LastAssistantText string
	// LastUserText is the newest user prompt text.
	LastUserText string
	// ModTime is the file modification time.
	ModTime time.Time
	// Empty reports whether the file has no decodable conversation lines.
	Empty bool
}

// Store reads the on-disk Claude Code state under Home.
type Store struct {
	home string
}

// NewStore returns a store rooted at home. An empty home resolves through
// DefaultClaudeHome.
func NewStore(home string) (*Store, error) {
	root := strings.TrimSpace(home)
	if root == "" {
		resolved, err := DefaultClaudeHome()
		if err != nil {
			return nil, err
		}
		root = resolved
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("claude: resolve claude home %q: %w", root, err)
	}
	return &Store{home: abs}, nil
}

// Home returns the resolved data directory.
func (s *Store) Home() string { return s.home }

// ProjectsDir returns ~/.claude/projects.
func (s *Store) ProjectsDir() string { return filepath.Join(s.home, "projects") }

// SessionEnvDir returns ~/.claude/session-env.
func (s *Store) SessionEnvDir() string { return filepath.Join(s.home, "session-env") }

// SessionEnvPath returns the session-env directory for one session.
func (s *Store) SessionEnvPath(sessionID string) string {
	return filepath.Join(s.SessionEnvDir(), sessionID)
}

// HasSessionEnv reports whether the session has a session-env directory.
func (s *Store) HasSessionEnv(sessionID string) bool {
	info, err := os.Stat(s.SessionEnvPath(sessionID))
	return err == nil && info.IsDir()
}

// TranscriptPath returns the transcript path for a session, searching every
// project directory because the slug is derived from the (possibly unknown) cwd.
func (s *Store) TranscriptPath(sessionID string) (string, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return "", err
	}
	matches, err := filepath.Glob(filepath.Join(s.ProjectsDir(), "*", sessionID+transcriptExt))
	if err != nil {
		return "", fmt.Errorf("claude: locate transcript for %s: %w", sessionID, err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("%w: no transcript for %s under %s", ErrSessionNotFound, sessionID, s.ProjectsDir())
	}
	sort.Strings(matches)
	return matches[0], nil
}

// TranscriptPathForCWD returns the transcript path for a session under the
// project directory derived from cwd.
func (s *Store) TranscriptPathForCWD(cwd, sessionID string) string {
	return filepath.Join(s.ProjectsDir(), SlugifyCWD(cwd), sessionID+transcriptExt)
}

// ScanTranscript reads one transcript, decoding only a bounded window of lines
// at each end while still counting every line, so polling stays cheap on long
// conversations.
func (s *Store) ScanTranscript(path string) (TranscriptInfo, error) {
	info := TranscriptInfo{Path: path, SessionID: strings.TrimSuffix(filepath.Base(path), transcriptExt)}
	info.ProjectDir = filepath.Base(filepath.Dir(path))

	stat, err := os.Stat(path)
	if err != nil {
		return info, fmt.Errorf("claude: stat transcript %s: %w", path, err)
	}
	info.Size = stat.Size()
	info.ModTime = stat.ModTime().UTC()

	file, err := os.Open(path) // #nosec G304 -- path is resolved under the Claude data directory.
	if err != nil {
		return info, fmt.Errorf("claude: open transcript %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReaderSize(file, 256<<10)
	head := make([]Entry, 0, headWindow)
	// Only the raw bytes of the trailing lines are retained; they are decoded
	// after the pass, so a long transcript costs one pass of byte scanning
	// rather than one JSON decode per line.
	tailRaw := make([][]byte, 0, tailWindow)
	tailBytes := 0
	var lines, decoded int64

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if line[len(line)-1] == '\n' {
				lines++
			}
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				decoded++
				if len(head) < headWindow {
					if entry, parseErr := parseEntry(trimmed); parseErr == nil {
						head = append(head, entry)
					}
				}
				if len(trimmed) <= tailBytesLimit {
					tailRaw = append(tailRaw, append([]byte(nil), trimmed...))
					tailBytes += len(trimmed)
					for len(tailRaw) > tailWindow || (tailBytes > tailBytesLimit && len(tailRaw) > 1) {
						tailBytes -= len(tailRaw[0])
						tailRaw = tailRaw[1:]
					}
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return info, fmt.Errorf("claude: read transcript %s: %w", path, readErr)
		}
	}

	tail := make([]Entry, 0, len(tailRaw))
	for _, raw := range tailRaw {
		if entry, parseErr := parseEntry(raw); parseErr == nil {
			tail = append(tail, entry)
		}
	}

	info.Lines = lines
	// EntryCount reports how many entries were actually decoded, which is
	// bounded by the two windows no matter how long the transcript is. When the
	// whole file fits in the head window, the tail is the same set of lines and
	// must not be counted twice.
	if decoded <= headWindow {
		info.EntryCount = len(head)
	} else {
		info.EntryCount = len(head) + len(tail)
	}

	for _, entry := range head {
		if entry.SessionID != "" {
			info.SessionID = entry.SessionID
		}
		if entry.CWD != "" {
			info.CWD = entry.CWD
		}
		if entry.Version != "" {
			info.Version = entry.Version
		}
		if !entry.Timestamp.IsZero() && (info.FirstTimestamp.IsZero() || entry.Timestamp.Before(info.FirstTimestamp)) {
			info.FirstTimestamp = entry.Timestamp
		}
	}
	for _, entry := range tail {
		if entry.SessionID != "" {
			info.SessionID = entry.SessionID
		}
		if entry.CWD != "" {
			info.CWD = entry.CWD
		}
		if entry.Version != "" {
			info.Version = entry.Version
		}
		if entry.GitBranch != "" {
			info.GitBranch = entry.GitBranch
		}
		if !entry.Timestamp.IsZero() && (info.LastTimestamp.IsZero() || entry.Timestamp.After(info.LastTimestamp)) {
			info.LastTimestamp = entry.Timestamp
		}
	}
	if len(tail) > 0 {
		info.LastEntryType = tail[len(tail)-1].Type
		for i := len(tail) - 1; i >= 0; i-- {
			entry := tail[i]
			if entry.IsSidechain || entry.IsMeta {
				continue
			}
			switch entry.Type {
			case "assistant":
				if info.LastAssistantText == "" {
					info.LastAssistantText = entry.Text
				}
			case "user":
				if info.LastUserText == "" {
					info.LastUserText = entry.Text
				}
			}
			if info.LastAssistantText != "" && info.LastUserText != "" {
				break
			}
		}
	}
	info.Empty = info.EntryCount == 0
	return info, nil
}

// SessionRecord is one session discovered on disk.
type SessionRecord struct {
	// ID is the session UUID.
	ID string
	// ProjectDir is the project directory name.
	ProjectDir string
	// Transcript is the transcript metadata; Path is empty when no transcript
	// file exists.
	Transcript TranscriptInfo
	// EnvPath is the session-env directory; empty when absent.
	EnvPath string
}

// CWD returns the best-known working directory for the record.
func (r SessionRecord) CWD() string { return r.Transcript.CWD }

// List scans every project transcript and every session-env entry.
//
// A missing ~/.claude is not an error: it simply means there are no local
// sessions yet, which is the normal state of a fresh machine.
func (s *Store) List() ([]SessionRecord, error) {
	records := make(map[string]SessionRecord)
	byID := func(id string) SessionRecord {
		if rec, ok := records[id]; ok {
			return rec
		}
		return SessionRecord{ID: id}
	}

	transcripts, err := filepath.Glob(filepath.Join(s.ProjectsDir(), "*", "*"+transcriptExt))
	if err != nil {
		return nil, fmt.Errorf("claude: list transcripts: %w", err)
	}
	sort.Strings(transcripts)
	for _, path := range transcripts {
		info, scanErr := s.ScanTranscript(path)
		if scanErr != nil {
			// A transcript being written can be momentarily unreadable; treat it
			// as absent rather than failing the whole discovery pass.
			if errors.Is(scanErr, os.ErrNotExist) {
				continue
			}
			continue
		}
		id := info.SessionID
		if id == "" {
			id = strings.TrimSuffix(filepath.Base(path), transcriptExt)
		}
		if !ValidSessionID(id) {
			continue
		}
		rec := byID(id)
		rec.ID = id
		rec.ProjectDir = info.ProjectDir
		rec.Transcript = info
		records[id] = rec
	}

	entries, err := os.ReadDir(s.SessionEnvDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("claude: list session-env: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !ValidSessionID(entry.Name()) {
			continue
		}
		rec := byID(entry.Name())
		rec.ID = entry.Name()
		rec.EnvPath = filepath.Join(s.SessionEnvDir(), entry.Name())
		records[entry.Name()] = rec
	}

	out := make([]SessionRecord, 0, len(records))
	for _, rec := range records {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Transcript.LastTimestamp.Equal(out[j].Transcript.LastTimestamp) {
			return out[i].Transcript.LastTimestamp.After(out[j].Transcript.LastTimestamp)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get returns the on-disk record for one session, or ErrSessionNotFound.
func (s *Store) Get(sessionID string) (SessionRecord, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return SessionRecord{}, err
	}
	rec := SessionRecord{ID: sessionID}
	found := false
	if path, err := s.TranscriptPath(sessionID); err == nil {
		info, scanErr := s.ScanTranscript(path)
		if scanErr != nil {
			return SessionRecord{}, scanErr
		}
		rec.Transcript = info
		rec.ProjectDir = info.ProjectDir
		found = true
	} else if !errors.Is(err, ErrSessionNotFound) {
		return SessionRecord{}, err
	}
	if s.HasSessionEnv(sessionID) {
		rec.EnvPath = s.SessionEnvPath(sessionID)
		found = true
	}
	if !found {
		return SessionRecord{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
	}
	return rec, nil
}

// ReadEntriesSince returns the transcript entries written after offset bytes and
// the new offset.
//
// Recovery uses it to prove a resumed conversation actually advanced: the
// transcript must grow and contain the new turn. A shrinking file (rotation) is
// reported by returning the entries from the start plus the new size.
func (s *Store) ReadEntriesSince(path string, offset int64) ([]Entry, int64, error) {
	file, err := os.Open(path) // #nosec G304 -- path is resolved under the Claude data directory.
	if err != nil {
		return nil, offset, fmt.Errorf("claude: open transcript %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	stat, err := file.Stat()
	if err != nil {
		return nil, offset, fmt.Errorf("claude: stat transcript %s: %w", path, err)
	}
	if offset < 0 || offset > stat.Size() {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, fmt.Errorf("claude: seek transcript %s: %w", path, err)
	}

	reader := bufio.NewReaderSize(file, 256<<10)
	var entries []Entry
	consumed := offset
	for {
		line, readErr := reader.ReadBytes('\n')
		// Only newline-terminated lines are consumed. Claude Code appends to a
		// transcript while a turn runs, so the last chunk is often half a line;
		// counting it would advance the offset past a record that is not yet
		// complete and lose it forever.
		if len(line) > 0 && line[len(line)-1] == '\n' {
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				if entry, parseErr := parseEntry(trimmed); parseErr == nil {
					entries = append(entries, entry)
				}
			}
			consumed += int64(len(line))
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return entries, consumed, fmt.Errorf("claude: read transcript %s: %w", path, readErr)
		}
	}
	return entries, consumed, nil
}

// TailEntries returns the last n decodable entries of a transcript. It is a
// convenience wrapper used by the detector and by `ccresu inspect`.
func (s *Store) TailEntries(path string, n int) ([]Entry, error) {
	if n <= 0 {
		return nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("claude: stat transcript %s: %w", path, err)
	}
	// Read at most the last 512 KiB; a conversation turn is far smaller.
	const window = 512 << 10
	start := info.Size() - window
	if start < 0 {
		start = 0
	}
	entries, _, err := s.ReadEntriesSince(path, start)
	if err != nil {
		return nil, err
	}
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries, nil
}
