package claude_test

// Acceptance tests for internal/claude/published.go (PRD 2: a blocked session
// must read as blocked, not as recovered).
//
// The published listing is the only supported way to read a session's state
// from outside Claude Code, so the tests here are mostly about the two
// distinctions the type exists to preserve: whether a session was in the
// listing at all (R8), and whether the read failed rather than came back empty.
// A reader that cannot tell those apart turns an outage into a verdict.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/claude"
)

// agentListJSON is a two-entry listing in the shape the CLI prints: `id` is the
// state id, `sessionId` the conversation UUID. Both pieces are present because
// the index must key on the conversation, never on the job id.
const agentListJSON = `[
  {"id":"ab12cd34","sessionId":"11111111-1111-4111-8111-111111111111","kind":"background","state":"working","status":"busy"},
  {"id":"cd34ef56","sessionId":"22222222-2222-4222-8222-222222222222","kind":"background","state":"blocked","status":"waiting","waitingFor":"permission prompt"}
]`

// providerFor builds a real Provider over a scripted CLI, so the tests exercise
// the production query path (`claude agents --json`) rather than a stub of it.
func providerFor(t *testing.T, exec *fakeCLI, all bool) *claude.Provider {
	t.Helper()
	provider, err := claude.New(claude.Options{
		Executable: "claude",
		Home:       t.TempDir(),
		Execer:     exec,
		All:        all,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return provider
}

func TestPublishedListingIndexesBySessionID(t *testing.T) {
	t.Parallel()

	exec := &fakeCLI{t: t, version: "2.1.257 (Claude Code)", agents: agentListJSON}
	provider := providerFor(t, exec, true)

	listing, err := provider.PublishedListing(context.Background())
	if err != nil {
		t.Fatalf("PublishedListing() error = %v", err)
	}
	if listing.Failed() {
		t.Fatalf("Failed() = true, want false: %v", listing.Err)
	}
	if len(listing.Entries) != 2 {
		t.Fatalf("Entries = %d, want 2 in the CLI's order", len(listing.Entries))
	}
	// The order is the CLI's, not a map iteration's: an operator reading a
	// listing expects the order they were given.
	if listing.Entries[0].StateID != "ab12cd34" || listing.Entries[1].StateID != "cd34ef56" {
		t.Errorf("Entries order = %q, %q, want the printed order", listing.Entries[0].StateID, listing.Entries[1].StateID)
	}

	// R1: the observation is keyed by sessionId. `id` identifies the agent job,
	// not the conversation, and must never be the lookup key.
	working, ok := listing.Entry("11111111-1111-4111-8111-111111111111")
	if !ok {
		t.Fatal("Entry(working session) = absent, want present")
	}
	if working.State != "working" || working.Status != "busy" {
		t.Errorf("working entry = %+v, want state working and status busy", working)
	}
	blocked, ok := listing.Entry("22222222-2222-4222-8222-222222222222")
	if !ok {
		t.Fatal("Entry(blocked session) = absent, want present")
	}
	if blocked.State != "blocked" || blocked.WaitingFor != "permission prompt" {
		t.Errorf("blocked entry = %+v, want the blocked state and its reason", blocked)
	}
	if _, ok := listing.Entry("ab12cd34"); ok {
		t.Error("Entry(state id) = present: the job id must not identify a conversation")
	}
}

func TestPublishedListingAlwaysPassesAll(t *testing.T) {
	t.Parallel()

	// R9: the recovery read must always pass --all, even when the
	// discover_all_sessions preference is off. With it off a finished
	// background session is simply not listed, and a caller that reads absence
	// as evidence would read a finished session as a missing one.
	exec := &fakeCLI{t: t, version: "2.1.257 (Claude Code)", agents: agentListJSON}
	provider := providerFor(t, exec, false)

	if _, err := provider.PublishedListing(context.Background()); err != nil {
		t.Fatalf("PublishedListing() error = %v", err)
	}

	var agentsArgv []string
	for _, argv := range exec.seen {
		if len(argv) > 1 && argv[1] == "agents" {
			agentsArgv = argv
		}
	}
	if agentsArgv == nil {
		t.Fatalf("no `agents` invocation recorded; saw %v", exec.seen)
	}
	joined := strings.Join(agentsArgv, " ")
	if !strings.Contains(joined, "agents --json --all") {
		t.Errorf("argv = %q, want it to contain `agents --json --all`", joined)
	}
	if strings.Contains(joined, "--cwd") {
		t.Errorf("argv = %q, want no --cwd: the recovery read is machine-wide", joined)
	}
}

func TestPublishedListingRecordsAFailedRead(t *testing.T) {
	t.Parallel()

	// R8: a failed listing is recorded, never read as an empty listing. The
	// distinction is the whole reason PublishedListing carries Err: an outage
	// must not become a verdict that every session is gone.
	exec := &fakeCLI{t: t, version: "2.1.257 (Claude Code)", agents: agentListJSON, exit: 1}
	provider := providerFor(t, exec, true)

	listing, err := provider.PublishedListing(context.Background())
	if err == nil {
		t.Fatal("PublishedListing() error = nil, want the failure surfaced")
	}
	if !listing.Failed() {
		t.Error("Failed() = false, want true: the read failed and must say so")
	}
	if listing.Err == nil {
		t.Error("Err = nil, want the failure recorded on the listing")
	}
	if len(listing.Entries) != 0 {
		t.Errorf("Entries = %d, want none from a failed read", len(listing.Entries))
	}
	// The decisive assertion: a failed read must not answer "absent" as though
	// the session had been looked for and not found.
	if _, ok := listing.Entry("11111111-1111-4111-8111-111111111111"); ok {
		t.Error("Entry() = present on a failed read, which cannot have observed anything")
	}
}

func TestPublishedListingEntryReportsAbsence(t *testing.T) {
	t.Parallel()

	exec := &fakeCLI{t: t, version: "2.1.257 (Claude Code)", agents: agentListJSON}
	provider := providerFor(t, exec, true)

	listing, err := provider.PublishedListing(context.Background())
	if err != nil {
		t.Fatalf("PublishedListing() error = %v", err)
	}
	entry, ok := listing.Entry("33333333-3333-4333-8333-333333333333")
	if ok {
		t.Errorf("Entry(unknown) = %+v, true, want absent", entry)
	}
	if entry.StateID != "" || entry.SessionID != "" || entry.State != "" || entry.Status != "" {
		t.Errorf("Entry(unknown) = %+v, want the zero entry", entry)
	}
}

func TestPublishedListingWithoutAProvider(t *testing.T) {
	t.Parallel()

	// A provider with no CLI cannot answer, and must fail rather than report a
	// machine with no sessions. The two look identical downstream otherwise.
	listing, err := (&claude.Provider{}).PublishedListing(context.Background())
	if err == nil {
		t.Fatal("PublishedListing() error = nil, want ErrCLIUnavailable")
	}
	if !errors.Is(err, claude.ErrCLIUnavailable) {
		t.Errorf("error = %v, want it to wrap ErrCLIUnavailable", err)
	}
	if !listing.Failed() {
		t.Error("Failed() = false, want true")
	}
}

func TestPublishedListingReadAtIsStamped(t *testing.T) {
	t.Parallel()

	// The instant is what lets an operator judge staleness: an observation is
	// a statement about a moment, not a standing fact.
	exec := &fakeCLI{t: t, version: "2.1.257 (Claude Code)", agents: agentListJSON}
	provider := providerFor(t, exec, true)

	listing, err := provider.PublishedListing(context.Background())
	if err != nil {
		t.Fatalf("PublishedListing() error = %v", err)
	}
	if listing.ReadAt.IsZero() {
		t.Error("ReadAt is zero, want the instant the query was answered")
	}
	if listing.Failed() {
		t.Errorf("Failed() = true on a successful read: %v", listing.Err)
	}
}

func TestIndexBySessionIDKeepsTheStateBearingEntry(t *testing.T) {
	t.Parallel()

	// A duplicate conversation id must not demote a decision-bearing entry to
	// an empty one: the state is the only field a recovery decision may rest
	// on, so losing it would silently turn an observation into an absence.
	// An entry with no sessionId cannot be attributed to a conversation and is
	// not indexed at all.
	const duplicate = `[
	  {"id":"ab12cd34","sessionId":"11111111-1111-4111-8111-111111111111","state":"working"},
	  {"id":"ef56ab78","sessionId":"11111111-1111-4111-8111-111111111111","state":""},
	  {"id":"99999999","state":"blocked"}
	]`
	exec := &fakeCLI{t: t, version: "2.1.257 (Claude Code)", agents: duplicate}
	provider := providerFor(t, exec, true)

	listing, err := provider.PublishedListing(context.Background())
	if err != nil {
		t.Fatalf("PublishedListing() error = %v", err)
	}
	entry, ok := listing.Entry("11111111-1111-4111-8111-111111111111")
	if !ok {
		t.Fatal("Entry(duplicated session) = absent, want present")
	}
	if entry.State != "working" {
		t.Errorf("indexed State = %q, want %q: an empty duplicate must not win", entry.State, "working")
	}
	if len(listing.BySessionID) != 1 {
		t.Errorf("BySessionID = %d entries, want 1: an entry with no sessionId is unattributable", len(listing.BySessionID))
	}
}
