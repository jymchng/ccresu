package claude

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPackageDocsDescribeTheDispatchedResume is a documentation contract test.
//
// The package doc comments are the contract an integrator reads before writing
// code against this package, and they have already been wrong once: they kept
// advertising `claude -p --resume <id> --output-format json <message>` after the
// resume became a background dispatch, and nothing failed, because prose has no
// compiler. This test reads the same files a reader would and fails on the two
// mistakes that matter:
//
//   - naming the retired headless command (`-p` together with `--resume`), which
//     ccresu never runs and which the CLI would refuse anyway;
//   - failing to name the dispatch that is run, `--resume <id> --bg <message>`.
//
// It deliberately asserts on substrings rather than prose. Rewording is free;
// reintroducing the dead command is not.
func TestPackageDocsDescribeTheDispatchedResume(t *testing.T) {
	t.Parallel()

	// runtime.Caller(0) is this file, so the package directory is its dir. The
	// test therefore reads the sources of the package it belongs to, wherever the
	// module is checked out.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the package directory: runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)

	files := []string{"cli.go", "doc.go", "version.go"}
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(raw)

		if strings.Contains(text, "-p --resume") {
			t.Errorf(`%s: documents the retired headless command "-p --resume"; `+
				`a resume is a background dispatch, so it must read "--resume <id> --bg <message>"`, name)
		}
		if strings.Contains(text, "--output-format json <message>") {
			t.Errorf(`%s: documents "--output-format json <message>", which is never passed: `+
				`print mode refuses --bg`, name)
		}
		if !strings.Contains(text, "--bg") {
			t.Errorf("%s: never mentions --bg, so it cannot be describing the dispatch ccresu runs", name)
		}
	}

	// cli.go and doc.go spell the vector out; an integrator copies it from there.
	for _, name := range []string{"cli.go", "doc.go"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(raw), "claude --resume <session-id> --bg") {
			t.Errorf("%s: does not spell out `claude --resume <session-id> --bg <message>`", name)
		}
	}

	// The stop interface is the one command that may follow a refused resume, and
	// both files that describe the dispatch must also describe it.
	for _, name := range []string{"cli.go", "doc.go"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(raw), "claude stop <job-id>") &&
			!strings.Contains(string(raw), "claude stop <job-id>,") &&
			!strings.Contains(string(raw), "`claude stop <job-id>`") {
			t.Errorf("%s: does not document the `claude stop <job-id>` fallback", name)
		}
	}
}

// TestResumeArgsIsTheOnlyResumeVector states the invariant the docs claim: the
// package can build exactly one resume vector, and it is the dispatched one.
func TestResumeArgsIsTheOnlyResumeVector(t *testing.T) {
	t.Parallel()

	args := ResumeArgs("044e2a0a-9b1b-4a4f-8f38-6b1f1c0c1c1c", "continue")
	want := []string{"--resume", "044e2a0a-9b1b-4a4f-8f38-6b1f1c0c1c1c", "--bg", "continue"}
	if len(args) != len(want) {
		t.Fatalf("ResumeArgs() = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("ResumeArgs() = %v, want %v", args, want)
		}
	}
	for _, arg := range args {
		switch arg {
		case "-p", "--print", "--output-format":
			t.Fatalf("ResumeArgs() = %v, want no print-mode flag: --bg and --print conflict", args)
		}
	}
}
