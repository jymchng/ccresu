package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AgentEntry is one element of `claude agents --json` after defensive mapping.
//
// Verified shape (Claude Code 2.1.223, printed by the CLI's printAgentsJson):
//
//	{
//	  "pid": 1234,                       // omitted when unknown
//	  "id": "<state id>",                // NOT the session id
//	  "cwd": "/path/to/project",
//	  "kind": "interactive" | "background",
//	  "startedAt": 1717171717171,        // epoch milliseconds
//	  "sessionId": "<uuid>",             // omitted when the CLI does not know it
//	  "name": "session name",            // omitted when unnamed
//	  "status": "busy" | "idle" | "waiting",
//	  "waitingFor": "...",               // only while waiting
//	  "state": "working" | "blocked" | "done" | "failed" | "stopped" // background
//	}
//
// Every field is optional from ccresu's point of view: unknown keys are ignored
// and a missing field is treated as absent rather than as an error, because the
// CLI reserves the right to add or drop keys between releases.
type AgentEntry struct {
	// StateID is the CLI's "id", which identifies the agent job, not the
	// conversation.
	StateID string
	// SessionID is the conversation UUID; empty when the CLI did not report one.
	SessionID string
	// CWD is the session's working directory.
	CWD string
	// Kind is "interactive", "background" or an unknown value from a newer CLI.
	Kind string
	// PID is the session process id, zero when absent.
	PID int
	// Name is the CLI-reported session name.
	Name string
	// Status is busy, idle or waiting.
	Status string
	// State is the background state: working, blocked, done, failed or stopped.
	State string
	// WaitingFor describes what a waiting session waits for.
	WaitingFor string
	// StartedAt is the start instant in UTC; zero when absent.
	StartedAt time.Time
	// Raw preserves the original JSON object for diagnostics.
	Raw json.RawMessage
}

// ParseAgentsJSON maps `claude agents --json` output.
//
// The verified output is a JSON array. A single object, an object wrapping the
// array under "sessions" or "agents", and a null are also accepted so an
// envelope change does not break discovery; anything else is rejected with
// ErrMalformedOutput instead of being guessed at.
func ParseAgentsJSON(data []byte) ([]AgentEntry, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: empty claude agents output", ErrMalformedOutput)
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}

	switch trimmed[0] {
	case '[':
		var raws []json.RawMessage
		if err := json.Unmarshal(trimmed, &raws); err != nil {
			return nil, fmt.Errorf("%w: agents array: %v", ErrMalformedOutput, err)
		}
		return mapAgentEntries(raws)
	case '{':
		var envelope struct {
			Sessions []json.RawMessage `json:"sessions"`
			Agents   []json.RawMessage `json:"agents"`
		}
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return nil, fmt.Errorf("%w: agents object: %v", ErrMalformedOutput, err)
		}
		switch {
		case envelope.Sessions != nil:
			return mapAgentEntries(envelope.Sessions)
		case envelope.Agents != nil:
			return mapAgentEntries(envelope.Agents)
		default:
			return mapAgentEntries([]json.RawMessage{json.RawMessage(trimmed)})
		}
	default:
		return nil, fmt.Errorf("%w: agents output is neither an array nor an object", ErrMalformedOutput)
	}
}

// mapAgentEntries converts raw objects, skipping non-object elements.
func mapAgentEntries(raws []json.RawMessage) ([]AgentEntry, error) {
	entries := make([]AgentEntry, 0, len(raws))
	for _, raw := range raws {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}
		if trimmed[0] != '{' {
			continue
		}
		entry, err := mapAgentEntry(trimmed)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// agentsWire is the permissive wire form of one agent entry.
type agentsWire struct {
	ID         flexString `json:"id"`
	SessionID  flexString `json:"sessionId"`
	SessionID2 flexString `json:"session_id"`
	CWD        flexString `json:"cwd"`
	WorkingDir flexString `json:"workingDirectory"`
	Kind       flexString `json:"kind"`
	Type       flexString `json:"type"`
	PID        flexInt    `json:"pid"`
	ProcessID  flexInt    `json:"processId"`
	Name       flexString `json:"name"`
	Title      flexString `json:"title"`
	Status     flexString `json:"status"`
	State      flexString `json:"state"`
	WaitingFor flexString `json:"waitingFor"`
	StartedAt  flexTime   `json:"startedAt"`
	CreatedAt  flexTime   `json:"createdAt"`
	LastActive flexTime   `json:"lastActivity"`
}

// mapAgentEntry maps one JSON object.
func mapAgentEntry(raw json.RawMessage) (AgentEntry, error) {
	var wire agentsWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return AgentEntry{}, fmt.Errorf("%w: agent entry: %v", ErrMalformedOutput, err)
	}
	entry := AgentEntry{
		StateID:    firstNonEmpty(wire.ID.String(), ""),
		SessionID:  firstNonEmpty(wire.SessionID.String(), wire.SessionID2.String()),
		CWD:        firstNonEmpty(wire.CWD.String(), wire.WorkingDir.String()),
		Kind:       strings.ToLower(firstNonEmpty(wire.Kind.String(), wire.Type.String())),
		PID:        firstNonZeroInt(wire.PID.Int(), wire.ProcessID.Int()),
		Name:       firstNonEmpty(wire.Name.String(), wire.Title.String()),
		Status:     strings.ToLower(wire.Status.String()),
		State:      strings.ToLower(wire.State.String()),
		WaitingFor: wire.WaitingFor.String(),
		StartedAt:  firstNonZeroTime(wire.StartedAt.Time(), wire.CreatedAt.Time(), wire.LastActive.Time()),
		Raw:        append(json.RawMessage(nil), raw...),
	}
	return entry, nil
}

// flexString accepts a JSON string, number, boolean, null, or an object with a
// nested "id"/"value"/"name" string. Anything else is treated as absent.
type flexString struct {
	value string
	set   bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexString) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		f.value, f.set = s, true
	case '{':
		var nested struct {
			ID    flexString `json:"id"`
			Value flexString `json:"value"`
			Name  flexString `json:"name"`
		}
		if err := json.Unmarshal(trimmed, &nested); err != nil {
			return nil
		}
		switch {
		case nested.ID.set:
			f.value, f.set = nested.ID.value, true
		case nested.Value.set:
			f.value, f.set = nested.Value.value, true
		case nested.Name.set:
			f.value, f.set = nested.Name.value, true
		}
	default:
		var num json.Number
		if err := json.Unmarshal(trimmed, &num); err == nil {
			f.value, f.set = num.String(), true
		}
	}
	return nil
}

// String returns the mapped value.
func (f flexString) String() string { return f.value }

// flexInt accepts a JSON number or a numeric string.
type flexInt struct {
	value int
	set   bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexInt) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(trimmed, &num); err == nil {
		if n, convErr := num.Int64(); convErr == nil {
			f.value, f.set = int(n), true
			return nil
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return nil
	}
	if n, convErr := strconv.Atoi(strings.TrimSpace(s)); convErr == nil {
		f.value, f.set = n, true
	}
	return nil
}

// Int returns the mapped value.
func (f flexInt) Int() int { return f.value }

// flexTime accepts an RFC 3339 string, an epoch timestamp (seconds or
// milliseconds), or null.
type flexTime struct {
	value time.Time
	set   bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexTime) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil
		}
		if t, ok := parseFlexibleTime(s); ok {
			f.value, f.set = t, true
		}
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(trimmed, &num); err != nil {
		return nil
	}
	if n, convErr := num.Int64(); convErr == nil {
		if t, ok := epochTime(n); ok {
			f.value, f.set = t, true
		}
	}
	return nil
}

// Time returns the mapped instant.
func (f flexTime) Time() time.Time { return f.value }

// parseFlexibleTime accepts RFC 3339 and a few common variants.
func parseFlexibleTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// epochTime converts an epoch value, guessing seconds versus milliseconds from
// the magnitude. Negative or absurd values are rejected.
func epochTime(n int64) (time.Time, bool) {
	if n <= 0 {
		return time.Time{}, false
	}
	switch {
	case n >= 1e17: // nanoseconds
		return time.Unix(0, n).UTC(), true
	case n >= 1e14: // microseconds
		return time.Unix(0, n*int64(time.Microsecond)).UTC(), true
	case n >= 1e11: // milliseconds
		return time.UnixMilli(n).UTC(), true
	case n >= 1e8: // seconds
		return time.Unix(n, 0).UTC(), true
	default:
		return time.Time{}, false
	}
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// firstNonZeroInt returns the first non-zero int.
func firstNonZeroInt(values ...int) int {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

// firstNonZeroTime returns the first non-zero time.
func firstNonZeroTime(values ...time.Time) time.Time {
	for _, v := range values {
		if !v.IsZero() {
			return v.UTC()
		}
	}
	return time.Time{}
}

// flexBool accepts true/false, "true"/"false" and 0/1.
//
// It lived beside the print-mode resume mapping until a resume became a
// background dispatch; the transcript reader needs it too, so it lives with the
// other permissive wire types.
type flexBool struct {
	value bool
	set   bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexBool) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "1", "yes":
			f.value, f.set = true, true
		case "false", "0", "no":
			f.value, f.set = false, true
		}
	case 't':
		f.value, f.set = true, true
	case 'f':
		f.value, f.set = false, true
	default:
		var num json.Number
		if err := json.Unmarshal(trimmed, &num); err == nil {
			if n, convErr := num.Int64(); convErr == nil {
				f.value, f.set = n != 0, true
			}
		}
	}
	return nil
}

// Bool returns the mapped value.
func (f flexBool) Bool() bool { return f.value }
