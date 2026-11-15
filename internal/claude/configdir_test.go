package claude

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestOneConfigDirIsUsedForReadAndHandoff is R9's acceptance test.
//
// CLAUDE_CONFIG_DIR makes separate Claude Code instances, each with its own
// supervisor. ccresu reads one supervisor's listing and hands the continuation to
// a supervisor, so if those two disagree it can observe a session nobody is
// running and hand off to an instance that has never heard of it. The only fixed
// point is the config directory, and it has to be the same value for both the
// read and the handoff — resolved once, by the provider, and passed explicitly
// rather than inherited from however the daemon happened to be started.
func TestOneConfigDirIsUsedForReadAndHandoff(t *testing.T) {
	home := t.TempDir()
	resolved, err := filepath.Abs(home)
	if err != nil {
		t.Fatalf("Abs(%q): %v", home, err)
	}

	var seen []Call
	execer := NewFakeExecer(func(call Call) (Output, error) {
		seen = append(seen, call)
		switch {
		case len(call.Args) > 0 && call.Args[0] == "--version":
			return Output{Stdout: []byte("2.1.300 (Claude Code)\n")}, nil
		case len(call.Args) > 0 && call.Args[0] == "agents":
			return Output{Stdout: []byte("[]")}, nil
		case len(call.Args) > 0 && call.Args[0] == "--bg":
			return Output{Stdout: []byte("backgrounded · 837eaaea · ccresu-3f1e2c7a\n")}, nil
		}
		return Output{}, nil
	})

	provider, err := New(Options{Executable: "claude", Home: home, Execer: execer})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := provider.CLI().ConfigDir(); got != resolved {
		t.Fatalf("CLI config dir = %q, want the provider's resolved home %q", got, resolved)
	}

	ctx := context.Background()
	if _, err := provider.CLI().Agents(ctx, AgentsQuery{All: true}); err != nil {
		t.Fatalf("agents: %v", err)
	}
	if _, err := provider.CLI().Resume(ctx, testSessionID, "continue the goal", home); err != nil {
		t.Fatalf("resume: %v", err)
	}

	readDir, handoffDir := "", ""
	for _, call := range seen {
		if len(call.Args) == 0 {
			continue
		}
		want := "CLAUDE_CONFIG_DIR=" + resolved
		found := false
		for _, entry := range call.Options.Env {
			if entry == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%v ran with env %v, want %q", call.Args, call.Options.Env, want)
		}
		switch call.Args[0] {
		case "agents":
			readDir = envValue(call.Options.Env, EnvConfigDir)
		case "--bg":
			handoffDir = envValue(call.Options.Env, EnvConfigDir)
		}
	}
	if readDir == "" || handoffDir == "" {
		t.Fatalf("read dir = %q, handoff dir = %q, want both recorded", readDir, handoffDir)
	}
	if readDir != handoffDir {
		t.Errorf("the state read used %q and the handoff used %q: two supervisors, one of them wrong",
			readDir, handoffDir)
	}
}

// TestNoConfigDirInheritsTheEnvironment is the other half of R9: when ccresu
// resolves nothing, it adds nothing, so the CLI keeps resolving the directory from
// the environment exactly as before. Inventing a default here would silently
// override an operator's $CLAUDE_CONFIG_DIR.
func TestNoConfigDirInheritsTheEnvironment(t *testing.T) {
	var seen []Call
	execer := NewFakeExecer(func(call Call) (Output, error) {
		seen = append(seen, call)
		if len(call.Args) > 0 && call.Args[0] == "--version" {
			return Output{Stdout: []byte("2.1.300 (Claude Code)\n")}, nil
		}
		return Output{Stdout: []byte("[]")}, nil
	})
	cli := NewCLI(CLIOptions{Executable: "claude", Execer: execer})

	if _, err := cli.Agents(context.Background(), AgentsQuery{All: true}); err != nil {
		t.Fatalf("agents: %v", err)
	}
	for _, call := range seen {
		if value := envValue(call.Options.Env, EnvConfigDir); value != "" {
			t.Errorf("%v ran with %s=%q, want the environment inherited untouched", call.Args, EnvConfigDir, value)
		}
	}
}

// envValue reads one key out of a RunOptions environment overlay.
func envValue(env []string, key string) string {
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name == key {
			return value
		}
	}
	return ""
}
