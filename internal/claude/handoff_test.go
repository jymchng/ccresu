package claude

import (
	"errors"
	"strings"
	"testing"
)

// TestParseHandoffReadsTheBlock is the acceptance test for R7: the printed
// `backgrounded \u00b7 <short-id> \u00b7 <name>` block is the only place the state ID
// exists, and the state ID is not the session UUID -- `claude attach`,
// `claude logs` and `claude stop` all take it.
func TestParseHandoffReadsTheBlock(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		input      string
		wantState  string
		wantName   string
		wantServed bool
		wantCopied bool
		wantCopyID string
	}{
		{
			name:      "plain block",
			input:     "backgrounded \u00b7 837eaaea \u00b7 ccresu-3f1e2c7a\n",
			wantState: "837eaaea",
			wantName:  "ccresu-3f1e2c7a",
		},
		{
			name:       "supervisor started first",
			input:      "Starting background service\u2026\nbackgrounded \u00b7 837eaaea \u00b7 ccresu-3f1e2c7a\n",
			wantState:  "837eaaea",
			wantName:   "ccresu-3f1e2c7a",
			wantServed: true,
		},
		{
			name:       "copy is recorded, not rejected",
			input:      "backgrounded \u00b7 837eaaea \u00b7 ccresu-3f1e2c7a\nnote: started a copy in session " + testOtherSessionID + "\n",
			wantState:  "837eaaea",
			wantName:   "ccresu-3f1e2c7a",
			wantCopied: true,
			wantCopyID: testOtherSessionID,
		},
		{
			name:      "pipe separator and trailing commands",
			input:     "backgrounded | 837eaaea | ccresu-3f1e2c7a\nclaude attach 837eaaea\n",
			wantState: "837eaaea",
			wantName:  "ccresu-3f1e2c7a",
		},
		{
			name:  "bare block still acknowledges",
			input: "backgrounded\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handoff, err := ParseHandoff([]byte(tc.input))
			if err != nil {
				t.Fatalf("ParseHandoff() error = %v", err)
			}
			if !handoff.Acknowledged {
				t.Error("Acknowledged = false, want true")
			}
			if handoff.StateID != tc.wantState {
				t.Errorf("StateID = %q, want %q", handoff.StateID, tc.wantState)
			}
			if handoff.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", handoff.Name, tc.wantName)
			}
			if handoff.StartedService != tc.wantServed {
				t.Errorf("StartedService = %v, want %v", handoff.StartedService, tc.wantServed)
			}
			if handoff.Copied != tc.wantCopied {
				t.Errorf("Copied = %v, want %v", handoff.Copied, tc.wantCopied)
			}
			if handoff.CopySessionID != tc.wantCopyID {
				t.Errorf("CopySessionID = %q, want %q", handoff.CopySessionID, tc.wantCopyID)
			}
		})
	}
}

// TestParseHandoffWithoutABlockIsAnError keeps "the CLI said nothing" out of the
// success path: without the block ccresu cannot name the session it created.
func TestParseHandoffWithoutABlockIsAnError(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "resumed\n", "error: no such session\n"} {
		if _, err := ParseHandoff([]byte(input)); !errors.Is(err, ErrHandoffNotAcknowledged) {
			t.Errorf("ParseHandoff(%q) error = %v, want ErrHandoffNotAcknowledged", input, err)
		}
	}
}

// TestHandoffLabelIsDeterministic is R6: the label is ccresu's own, so a
// recovered session is identifiable in agent view without consulting the
// database, and two handoffs of one session cannot disagree.
func TestHandoffLabelIsDeterministic(t *testing.T) {
	t.Parallel()

	first := HandoffLabel(testSessionID)
	second := HandoffLabel(testSessionID)
	if first != second {
		t.Fatalf("HandoffLabel() is not deterministic: %q then %q", first, second)
	}
	if first != "ccresu-3f1e2c7a" {
		t.Errorf("HandoffLabel() = %q, want ccresu-3f1e2c7a", first)
	}
	if got := HandoffLabel(""); got != "ccresu" {
		t.Errorf("HandoffLabel(\"\") = %q, want ccresu", got)
	}
	if got := HandoffLabel("AB"); got != "ccresu-ab" {
		t.Errorf("HandoffLabel(\"AB\") = %q, want ccresu-ab", got)
	}
}

// TestWorkspaceNotTrustedIsItsOwnOutcome is R9: the refusal gets a name, because
// its remedy is the operator's decision and not a retry.
func TestWorkspaceNotTrustedIsItsOwnOutcome(t *testing.T) {
	t.Parallel()

	refusal := Output{
		Stdout:   []byte("Workspace not trusted.\nRun claude in this directory once to trust it.\n"),
		ExitCode: 1,
	}
	if !WorkspaceNotTrusted(refusal) {
		t.Error("WorkspaceNotTrusted() = false, want true")
	}
	if WorkspaceNotTrusted(Output{Stdout: []byte("all good\n")}) {
		t.Error("WorkspaceNotTrusted() = true on ordinary output")
	}
	if WorkspaceNotTrusted(Output{Stdout: []byte("No conversation found with session ID: " + testSessionID)}) {
		t.Error("WorkspaceNotTrusted() matched an unrelated refusal")
	}
}

// TestVersionRequirementErrorNamesBothRemedies keeps the error explainable: the
// operator is told what is too old and what to do about it.
func TestVersionRequirementErrorNamesBothRemedies(t *testing.T) {
	t.Parallel()

	err := &VersionRequirementError{
		Have: Version{Major: 2, Minor: 1, Patch: 223, Parsed: true},
		Need: MinimumVersion,
	}
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Error("errors.Is(err, ErrUnsupportedVersion) = false, want true")
	}
	message := err.Error()
	if !strings.Contains(message, "2.1.223") || !strings.Contains(message, "2.1.257") {
		t.Errorf("error = %q, want both versions", message)
	}
	if !strings.Contains(message, "v0.6.0") {
		t.Errorf("error = %q, want the fallback release named", message)
	}
}

// TestHandoffArgsNeverCarriesPrintMode asserts the property on the built argv,
// not on a caller's intention.
func TestHandoffArgsNeverCarriesPrintMode(t *testing.T) {
	t.Parallel()

	for _, message := range []string{"go", "-p", "--print --resume x", "$(whoami) `id`"} {
		argv := HandoffArgs(testSessionID, message)
		for _, arg := range argv[:len(argv)-1] {
			if arg == "-p" || arg == "--print" || arg == "--output-format" {
				t.Errorf("HandoffArgs(%q) = %q, want no print-mode flag", message, argv)
			}
		}
		if argv[len(argv)-1] != message {
			t.Errorf("HandoffArgs(%q) message = %q, want it passed as one argv element", message, argv[len(argv)-1])
		}
	}
}

// TestGuardrailFlagsRideTheHandoff is R2: the four flags with no print-mode
// restriction are appended when configured, in a fixed order, and a value is
// never passed as an empty argument.
func TestGuardrailFlagsRideTheHandoff(t *testing.T) {
	t.Parallel()

	full := Guardrails{
		Model:          "claude-sonnet-4-6",
		Effort:         "high",
		FallbackModel:  "claude-haiku-4-5,claude-sonnet-4-6",
		PermissionMode: "dontAsk",
	}
	argv := HandoffArgs(testSessionID, "continue", full)
	want := []string{
		"--bg", "--resume", testSessionID, "--name", HandoffLabel(testSessionID),
		"--model", "claude-sonnet-4-6",
		"--effort", "high",
		"--fallback-model", "claude-haiku-4-5,claude-sonnet-4-6",
		"--permission-mode", "dontAsk",
		"continue",
	}
	if len(argv) != len(want) {
		t.Fatalf("HandoffArgs = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("HandoffArgs[%d] = %q, want %q (full vector %q)", i, argv[i], want[i], argv)
		}
	}

	// One knob at a time: the others stay absent rather than arriving empty.
	for _, tc := range []struct {
		name string
		set  Guardrails
		flag string
	}{
		{"model", Guardrails{Model: "m"}, "--model"},
		{"effort", Guardrails{Effort: "low"}, "--effort"},
		{"fallback", Guardrails{FallbackModel: "f"}, "--fallback-model"},
		{"permission", Guardrails{PermissionMode: "dontAsk"}, "--permission-mode"},
	} {
		argv := HandoffArgs(testSessionID, "continue", tc.set)
		if got := strings.Join(argv, " "); !strings.Contains(got, tc.flag+" ") {
			t.Errorf("%s: HandoffArgs = %q, want it to carry %s", tc.name, argv, tc.flag)
		}
		for _, other := range []string{"--model", "--effort", "--fallback-model", "--permission-mode"} {
			if other == tc.flag {
				continue
			}
			if strings.Contains(strings.Join(argv, " "), other) {
				t.Errorf("%s: HandoffArgs = %q, want no %s from an unset knob", tc.name, argv, other)
			}
		}
	}

	// Whitespace is not a value: it must be omitted, not passed as " ".
	blank := HandoffArgs(testSessionID, "continue", Guardrails{Model: "   ", Effort: "\t"})
	if strings.Contains(strings.Join(blank, " "), "--model") || strings.Contains(strings.Join(blank, " "), "--effort") {
		t.Errorf("HandoffArgs = %q, want blank knobs omitted entirely", blank)
	}
}

// TestUnsetGuardrailsProduceTheUnguardedVector pins the identity the variadic
// signature exists for: no guardrails and an empty guardrail set are the same
// vector, element for element.
func TestUnsetGuardrailsProduceTheUnguardedVector(t *testing.T) {
	t.Parallel()

	bare := HandoffArgs(testSessionID, "continue")
	empty := HandoffArgs(testSessionID, "continue", Guardrails{})
	if len(bare) != len(empty) {
		t.Fatalf("HandoffArgs() = %q, HandoffArgs(Guardrails{}) = %q, want identical vectors", bare, empty)
	}
	for i := range bare {
		if bare[i] != empty[i] {
			t.Fatalf("vector differs at %d: %q vs %q", i, bare[i], empty[i])
		}
	}

	// The four print-mode-only flags, plus --print/-p, must remain unreachable
	// from a fully populated guardrail set (R3). The comparison is per argv
	// element: a substring search would match "-p" inside "--permission-mode".
	full := Guardrails{
		Model:          "m",
		Effort:         "high",
		FallbackModel:  "f",
		PermissionMode: "dontAsk",
	}
	forbidden := map[string]bool{
		"--max-budget-usd": true, "--max-turns": true, "--json-schema": true,
		"--permission-prompts": true, "--permission-prompt-tool": true,
		"--print": true, "-p": true, "--output-format": true,
	}
	for _, arg := range HandoffArgs(testSessionID, "continue", full) {
		if forbidden[arg] {
			t.Errorf("vector = %q, want no %s on a handoff", HandoffArgs(testSessionID, "continue", full), arg)
		}
	}
}
