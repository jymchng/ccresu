package recovery

// Acceptance tests for PRD 2 (prds/authoritative-session-state.md).
//
// The defect these tests close is a single reduction: the supervisor publishes
// `working`, `blocked`, `done`, `failed` or `stopped`, and the recovery engine
// used to fold all of them into one boolean and then decide success from a
// transcript line count. The tests are therefore about the decision, not the
// plumbing: a blocked session must never read as recovered, absence must not
// read as a verdict, and a longer transcript must not be able to prove anything
// on its own.
//
// The mapping is a pure function, so most of these feed it directly. The two
// that need the manager drive it through the same provider stub the rest of the
// package uses.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/storage"
)

// observedAt is a fixed instant so an observation is never mistaken for "not
// recorded", which the zero time means (R8).
var observedAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestABlockedObservationIsNeverResumed(t *testing.T) {
	t.Parallel()

	// PRD 2 §6 acceptance criteria, and the whole point of the PRD: `blocked`
	// "always means the session needs something from you before it can
	// continue", so no path may turn it into RESUMED. The payload is fed through
	// the real parser first, so the test covers the wire shape as well as the
	// mapping.
	const payload = `[{"id":"ab12cd34","sessionId":"11111111-1111-4111-8111-111111111111","state":"blocked","status":"waiting","waitingFor":"permission prompt"}]`
	entries, err := claude.ParseAgentsJSON([]byte(payload))
	if err != nil {
		t.Fatalf("ParseAgentsJSON() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	observation := StateObservation{
		SessionID:  entries[0].SessionID,
		State:      entries[0].State,
		Status:     entries[0].Status,
		WaitingFor: entries[0].WaitingFor,
		ObservedAt: observedAt,
		InListing:  true,
	}

	for _, handoff := range []bool{false, true} {
		decision := Observe(observation, handoff, false)
		if decision.State == storage.StateResumed {
			t.Fatalf("handoff=%v: State = RESUMED, want a blocked session never reported recovered", handoff)
		}
		if decision.State != storage.StateWaitingForInput {
			t.Errorf("handoff=%v: State = %q, want %q", handoff, decision.State, storage.StateWaitingForInput)
		}
		if !decision.NeedsInput {
			t.Errorf("handoff=%v: NeedsInput = false, want the operator told", handoff)
		}
		if !decision.Transition() {
			t.Errorf("handoff=%v: Transition() = false, want the session parked instead of left in place", handoff)
		}
		// R12: the reason is carried verbatim, never paraphrased.
		if decision.WaitingFor != "permission prompt" {
			t.Errorf("handoff=%v: WaitingFor = %q, want it verbatim", handoff, decision.WaitingFor)
		}
		if !decision.WaitingForReported {
			t.Errorf("handoff=%v: WaitingForReported = false, want true when the CLI named a reason", handoff)
		}
	}
}

func TestTheSameStateMeansDifferentThingsWithAnOpenHandoff(t *testing.T) {
	t.Parallel()

	// R5 and R6. The mapping takes "is a handoff outstanding" as an argument
	// rather than inferring it, because `done` means opposite things across that
	// boundary: before a handoff the conversation has nothing left to continue,
	// and after one the continuation finished the work. Inferring it is how a
	// finished conversation gets handed a "continue" message it does not need.
	done := StateObservation{SessionID: sessionA, State: "done", InListing: true, ObservedAt: observedAt}

	before := Observe(done, false, false)
	if before.State != storage.StateCompleted {
		t.Errorf("done with no handoff: State = %q, want %q", before.State, storage.StateCompleted)
	}
	if before.State == storage.StateResumed {
		t.Error("done with no handoff: State = RESUMED, want COMPLETED: there is nothing to continue")
	}

	after := Observe(done, true, false)
	if after.State != storage.StateResumed {
		t.Errorf("done with a handoff: State = %q, want %q", after.State, storage.StateResumed)
	}
}

func TestTranscriptGrowthCannotProveSuccess(t *testing.T) {
	t.Parallel()

	// R3, and the previous PRD's RC5 in a new place: the transcript is a file
	// the attempt itself writes to, so growth cannot be the proof. A grown
	// transcript alongside `state: blocked` must yield neither a proof nor a
	// resume.
	observation := StateObservation{SessionID: sessionA, State: "blocked", InListing: true, ObservedAt: observedAt}
	verification := Verification{
		SessionIDPreserved: true,
		TranscriptGrew:     true,
		LinesBefore:        5,
		LinesAfter:         9,
		SizeBefore:         100,
		SizeAfter:          180,
	}

	decision := Observe(observation, false, false)
	verification.ObservePublished(observation, decision)

	if verification.Proven {
		t.Error("Proven = true, want false: a grown transcript cannot prove a blocked session continued")
	}
	if decision.State != storage.StateWaitingForInput {
		t.Errorf("State = %q, want %q", decision.State, storage.StateWaitingForInput)
	}
	if decision.State == storage.StateResumed {
		t.Error("State = RESUMED, want a blocked session never resumed")
	}
	// The measurement is kept, just demoted: an operator should be able to see
	// the numbers without being told they are evidence.
	if !verification.TranscriptGrew {
		t.Error("TranscriptGrew = false, want the measurement retained as a diagnostic")
	}
	if !containsNote(verification.Notes, "growth alone is not proof") {
		t.Errorf("Notes = %v, want the diagnostic disclaimer", verification.Notes)
	}
}

func TestASessionMissingFromTheListingIsHandoffUnobserved(t *testing.T) {
	t.Parallel()

	// R8: absence is not an observation. A session missing from the listing is
	// its own outcome, so it can be retried and reported without ever being read
	// as a failure or a success. The naive fix \u2014 swapping a transcript read for a
	// state read \u2014 would turn "the supervisor said nothing" into "the session is
	// fine".
	missing := StateObservation{SessionID: sessionA, InListing: false, ObservedAt: observedAt}

	withHandoff := Observe(missing, true, false)
	if !withHandoff.HandoffUnobserved {
		t.Error("HandoffUnobserved = false, want true when a handoff is outstanding and the session is absent")
	}
	if withHandoff.State != storage.StateRetryWait {
		t.Errorf("State = %q, want %q", withHandoff.State, storage.StateRetryWait)
	}
	if withHandoff.State == storage.StateResumed {
		t.Error("State = RESUMED, want absence never read as recovery")
	}
	if withHandoff.State == storage.StateUnrecoverable {
		t.Error("State = UNRECOVERABLE, want absence never read as a permanent verdict")
	}

	// Before a handoff, absence is expected: a session that has never been a
	// background session publishes no state, and refusing it would decline to
	// recover every session on its first pass.
	withoutHandoff := Observe(missing, false, false)
	if withoutHandoff.Transition() {
		t.Errorf("no handoff: State = %q, want no transition", withoutHandoff.State)
	}
	if withoutHandoff.HandoffUnobserved {
		t.Error("no handoff: HandoffUnobserved = true, want false: there is no handoff to be unobserved")
	}
}

func TestAnUnknownStateValueIsRecordedAndNotGuessed(t *testing.T) {
	t.Parallel()

	// R10: a newer CLI's vocabulary is not ccresu's to guess. An unrecognised
	// value is recorded verbatim and retried \u2014 never coerced into a known value
	// and never read as success.
	observation := StateObservation{SessionID: sessionA, State: "paused", InListing: true, ObservedAt: observedAt}

	decision := Observe(observation, false, false)
	if decision.State != storage.StateRetryWait {
		t.Errorf("State = %q, want %q", decision.State, storage.StateRetryWait)
	}
	if decision.State == storage.StateResumed {
		t.Error("State = RESUMED, want an unknown value never read as success")
	}
	if !strings.Contains(decision.Reason, "paused") {
		t.Errorf("Reason = %q, want the raw value recorded so the attempt row can carry it", decision.Reason)
	}
}

func TestNoJobsFileIsRead(t *testing.T) {
	// R2 and the non-goals: nothing under ~/.claude/jobs/ may be read for state,
	// at any point, for any reason. The files are documented as unstable and
	// replaced on the next update, and reading them is how a stale value becomes
	// a verdict.
	//
	// The fixture is decisive by contradiction: the jobs file says `blocked`
	// while the listing says `working`. If any code path consulted the file, the
	// session would park in WAITING_FOR_INPUT; following the listing makes it
	// RESUMED.
	const id = sessionB
	home := t.TempDir()
	jobsDir := filepath.Join(home, ".claude", "jobs", "ab12cd34")
	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		t.Fatalf("mkdir jobs fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(jobsDir, "state.json"), []byte(`{"state":"blocked"}`), 0o600); err != nil {
		t.Fatalf("write jobs fixture: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))

	store := newTestStore(t)
	seedSession(t, store, recoverableSession(id))
	provider := &providerStub{
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 10, 200), nil
		},
		// The supervisor's opinion, which is the only state input allowed.
		listingFn: publishedWorking(id),
	}
	manager := testManager(t, store, provider, nil)

	if _, err := manager.Recover(context.Background(), id); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}

	stored, err := store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if stored.State == storage.StateWaitingForInput {
		t.Fatalf("State = WAITING_FOR_INPUT: the decision followed the jobs file, which must never be read")
	}
	if stored.State != storage.StateResumed {
		t.Errorf("State = %q, want %q: the decision follows the published listing", stored.State, storage.StateResumed)
	}
}

func TestOneAgentsQueryServesEverySessionInAPass(t *testing.T) {
	// R1: the recovery read is one `claude agents --json --all` query per pass,
	// served to every session in it. Reading per session would answer different
	// sessions from different instants while claiming they were observed
	// together, and would turn a machine-wide read into N reads.
	store := newTestStore(t)
	for _, id := range []string{sessionA, sessionB, sessionC} {
		seedSession(t, store, recoverableSession(id))
	}
	provider := &providerStub{
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 10, 200), nil
		},
		// A handoff does not re-read the listing after the resume, so the one
		// pre-resume read stands for the whole pass.
		listingFn: func(int) (claude.PublishedListing, error) {
			return claude.PublishedListing{
				Entries: []claude.AgentEntry{
					{SessionID: sessionA, State: "working"},
					{SessionID: sessionB, State: "working"},
					{SessionID: sessionC, State: "working"},
				},
				ReadAt: observedAt,
			}, nil
		},
	}
	manager := testManager(t, store, provider, nil)

	for _, id := range []string{sessionA, sessionB, sessionC} {
		if _, err := manager.Recover(context.Background(), id); err != nil {
			t.Fatalf("Recover(%s) error = %v", id, err)
		}
	}

	if got := provider.publishedCount(); got != 1 {
		t.Errorf("listing reads = %d, want exactly 1 for the pass", got)
	}
}
func TestObserveIsTotalOverEveryDocumentedValue(t *testing.T) {
	t.Parallel()

	// R3-R8 and R10 in one table: every documented state, both sides of the
	// handoff boundary, and the two absent cases. The assertion that matters is
	// the negative one repeated in every row: no documented value is left to a
	// fall-through that could produce success, so a new CLI value becomes a
	// retry rather than a false recovery.
	failed := StateObservation{SessionID: sessionA, State: "failed", InListing: true, ObservedAt: observedAt}
	stopped := StateObservation{SessionID: sessionA, State: "stopped", InListing: true, ObservedAt: observedAt}
	working := StateObservation{SessionID: sessionA, State: "working", InListing: true, ObservedAt: observedAt}
	noStateInListing := StateObservation{SessionID: sessionA, InListing: true, ObservedAt: observedAt}
	noStateNotListed := StateObservation{SessionID: sessionA, InListing: false, ObservedAt: observedAt}

	cases := []struct {
		name     string
		obs      StateObservation
		handoff  bool
		ccresu   bool
		want     storage.State
		wantHeld bool // HandoffUnobserved
	}{
		{name: "failed is a retryable failure", obs: failed, want: storage.StateRetryWait},
		{name: "failed after a handoff is still a retry", obs: failed, handoff: true, want: storage.StateRetryWait},
		{name: "stopped outside ccresu is a retry", obs: stopped, want: storage.StateRetryWait},
		{name: "stopped by ccresu is the operator's decision", obs: stopped, ccresu: true, want: storage.StateUserTerminated},
		{name: "working before a handoff continued on its own", obs: working, want: storage.StateResumed},
		{name: "working after a handoff leaves the attempt open", obs: working, handoff: true, want: ""},
		{name: "no state but listed, after a handoff is unobserved", obs: noStateInListing, handoff: true, want: storage.StateRetryWait, wantHeld: true},
		{name: "no state and not listed, after a handoff is unobserved", obs: noStateNotListed, handoff: true, want: storage.StateRetryWait, wantHeld: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decision := Observe(tc.obs, tc.handoff, tc.ccresu)
			if decision.State != tc.want {
				t.Errorf("State = %q, want %q", decision.State, tc.want)
			}
			if decision.HandoffUnobserved != tc.wantHeld {
				t.Errorf("HandoffUnobserved = %v, want %v", decision.HandoffUnobserved, tc.wantHeld)
			}
			if decision.Transition() != (tc.want != "") {
				t.Errorf("Transition() = %v, want %v", decision.Transition(), tc.want != "")
			}
			// The invariant the whole PRD rests on, asserted for every row:
			// nothing except a published `working`/`done` may read as RESUMED.
			if decision.State == storage.StateResumed && tc.obs.State != "working" && tc.obs.State != "done" {
				t.Errorf("State = RESUMED for published state %q, which must never be a recovery", tc.obs.State)
			}
		})
	}
}

func TestNewObservationSourceDefaultsAndRefusesWithoutAProvider(t *testing.T) {
	t.Parallel()

	// A source with no clock must still stamp its observations, and a source
	// with no provider must fail rather than report an empty listing: "the
	// supervisor is not wired up" and "the supervisor says nothing" are the two
	// facts R8 exists to keep apart.
	source := NewObservationSource(nil, nil)
	if source.Clock == nil {
		t.Fatal("Clock = nil, want the system clock by default")
	}

	observation, err := source.Observe(context.Background(), sessionA)
	if err == nil {
		t.Fatal("Observe() error = nil, want ErrObservationUnavailable without a provider")
	}
	if !errors.Is(err, ErrObservationUnavailable) {
		t.Errorf("error = %v, want it to wrap ErrObservationUnavailable", err)
	}
	if observation.Recorded() {
		t.Error("observation.Recorded() = true, want no observation from a failed read")
	}
}
func TestWaitingForInputIsAParkNotAVerdict(t *testing.T) {
	t.Parallel()

	// R11 defines the state by three properties at once, and each one is a
	// different kind of mistake if it is wrong: a settled park is never
	// re-observed (so the operator unblocks the session and nothing notices), a
	// terminal park cannot even record a failure, and a park with no edge to
	// RETRY_WAIT strands the session when the observation itself starts failing.
	state := storage.StateWaitingForInput

	if !state.Valid() {
		t.Fatalf("%s is not a known state: it must persist like the other fourteen", state)
	}
	if !Pending(state) {
		t.Errorf("Pending(%s) = false, want true: a restarted daemon must re-observe it", state)
	}
	if Settled(state) {
		t.Errorf("Settled(%s) = true, want false: the conversation is not finished", state)
	}
	if Terminal(state) {
		t.Errorf("Terminal(%s) = true, want false: a park is not an endpoint", state)
	}
	if Failed(state) {
		t.Errorf("Failed(%s) = true, want false: needing a human is not a failure ccresu gave up on", state)
	}

	// The documented edges, and the self-edge that lets a repeated `blocked`
	// observation be recorded.
	for _, target := range []State{
		StateRunning, StateResumed, StateRetryWait, StateUserTerminated,
		StateDisabled, StateUnrecoverable, StateDiscovered, StateWaitingForInput,
	} {
		if !CanTransition(state, target) {
			t.Errorf("CanTransition(%s, %s) = false, want the documented edge", state, target)
		}
	}
	// The edges that must NOT exist: ccresu never hands a continuation to a
	// blocked session, so it cannot go straight back to work from here.
	for _, target := range []State{StateResuming, StateCheckingLimit, StateCompleted} {
		if CanTransition(state, target) {
			t.Errorf("CanTransition(%s, %s) = true, want no such edge", state, target)
		}
	}
	// Both ways of parking must be legal: the pre-resume observation and a
	// resume that turns out to have landed on a blocked session.
	for _, from := range []State{StateCheckingSession, StateResuming} {
		if !CanTransition(from, state) {
			t.Errorf("CanTransition(%s, %s) = false, want the parking edge", from, state)
		}
	}
}

func TestTheRecoveryReadAlwaysPassesAll(t *testing.T) {
	t.Parallel()

	// R9, and the last of PRD 2's acceptance criteria. `discover_all_sessions`
	// is a discovery and UI preference, not a bound on the recovery read: with
	// it off a finished background session is simply not listed, and a reader
	// that treats absence as evidence would read a finished session as a missing
	// one. The assertion is made through the observation source — the path the
	// manager actually reads through — so the test fails if the provider's
	// preference is ever threaded into the recovery query, which is the mistake
	// a unit test of `PublishedListing` alone cannot catch.
	var seen [][]string
	exec := claude.NewFakeExecer(func(call claude.Call) (claude.Output, error) {
		seen = append(seen, append([]string{call.Name}, call.Args...))
		argv := strings.Join(call.Args, " ")
		switch {
		case argv == "--version":
			return claude.Output{Stdout: []byte("2.1.257 (Claude Code)")}, nil
		case strings.HasPrefix(argv, "agents"):
			return claude.Output{Stdout: []byte(`[{"id":"ab12cd34","sessionId":"` + sessionA + `","state":"working"}]`)}, nil
		}
		return claude.Output{}, nil
	})
	provider, err := claude.New(claude.Options{
		Executable: "claude",
		Home:       t.TempDir(),
		Execer:     exec,
		// The preference is off, which is exactly the configuration the recovery
		// read must ignore.
		All: false,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	observation, err := NewObservationSource(provider, nil).Observe(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observation.State != "working" {
		t.Errorf("observed State = %q, want %q from the listing", observation.State, "working")
	}

	var agentsArgv []string
	for _, argv := range seen {
		if len(argv) > 1 && argv[1] == "agents" {
			agentsArgv = argv
		}
	}
	if agentsArgv == nil {
		t.Fatalf("no `agents` invocation recorded; saw %v", seen)
	}
	joined := strings.Join(agentsArgv, " ")
	if !strings.Contains(joined, "agents --json --all") {
		t.Errorf("argv = %q, want it to contain `agents --json --all` even with discover_all_sessions off", joined)
	}
}

func TestABlockedObservationParksTheSessionAndTellsTheOperatorOnce(t *testing.T) {
	// R4, R11 and R12 asserted at the engine, not at the mapping. The pure
	// function can be right while a pass still hands a continuation to a blocked
	// session, parks it without recording the state it saw, or raises the
	// milestone from a paraphrase \u2014 so this drives one real pass through
	// Recover, with an observer installed, which is the branch a deployment with
	// notifications takes.
	//
	// Both payload shapes are exercised because R12 turns on the difference:
	// a blocked session whose `waitingFor` names a permission prompt and one that
	// reports no reason at all must both reach the operator, and neither may
	// invent the other's message.
	for _, tc := range []struct {
		name       string
		waitingFor string
	}{
		{name: "with the reason the supervisor published", waitingFor: "permission prompt"},
		{name: "with no reason reported at all", waitingFor: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = sessionA
			entry := claude.AgentEntry{
				StateID:    "ab12cd34",
				SessionID:  id,
				Kind:       "background",
				State:      "blocked",
				Status:     "waiting",
				WaitingFor: tc.waitingFor,
			}
			store := newTestStore(t)
			seedSession(t, store, recoverableSession(id))
			provider := &providerStub{
				statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
					return statusFor(sessionID, 10, 200), nil
				},
				// The supervisor's opinion, which is the only state input allowed.
				listingFn: func(int) (claude.PublishedListing, error) {
					return claude.PublishedListing{
						Entries:     []claude.AgentEntry{entry},
						BySessionID: map[string]claude.AgentEntry{id: entry},
						ReadAt:      observedAt,
					}, nil
				},
			}
			var milestones []Milestone
			manager := testManager(t, store, provider, func(cfg *Config) {
				cfg.Observer = func(_ context.Context, m Milestone) {
					milestones = append(milestones, m)
				}
			})

			outcome, err := manager.Recover(context.Background(), id)
			if err != nil {
				t.Fatalf("Recover() error = %v", err)
			}

			// A session waiting on a human is never handed a continuation: it
			// would queue behind a prompt nobody is there to answer.
			if got := provider.resumeCount(); got != 0 {
				t.Errorf("resumes = %d, want 0: a blocked session is never resumed", got)
			}
			if outcome.Resumed {
				t.Error("Resumed = true, want false: `blocked` is never a recovery")
			}
			if outcome.State != storage.StateWaitingForInput {
				t.Errorf("State = %q, want %q", outcome.State, storage.StateWaitingForInput)
			}
			stored, err := store.GetSession(context.Background(), id)
			if err != nil {
				t.Fatalf("GetSession() error = %v", err)
			}
			if stored.State != storage.StateWaitingForInput {
				t.Errorf("persisted State = %q, want %q", stored.State, storage.StateWaitingForInput)
			}

			// Exactly one needs-input milestone, and no success standing beside
			// it: the two would contradict each other in the operator's inbox.
			var needsInput []Milestone
			for _, m := range milestones {
				switch m.Kind {
				case MilestoneNeedsInput:
					needsInput = append(needsInput, m)
				case MilestoneSucceeded:
					t.Error("MilestoneSucceeded raised for a blocked session, want only needs_input")
				}
			}
			if len(needsInput) != 1 {
				t.Fatalf("needs_input milestones = %d, want exactly 1", len(needsInput))
			}

			// The payload quotes the CLI verbatim, and says so when the CLI said
			// nothing rather than suppressing the milestone.
			reason, reported := needsInput[0].NeedsInputReason()
			if want := tc.waitingFor != ""; reported != want {
				t.Errorf("NeedsInputReason() reported = %v, want %v", reported, want)
			}
			if reason != tc.waitingFor {
				t.Errorf("NeedsInputReason() reason = %q, want %q carried verbatim", reason, tc.waitingFor)
			}
			if got := needsInput[0].Observation.State; got != "blocked" {
				t.Errorf("milestone Observation.State = %q, want the published value carried through", got)
			}
			if got := needsInput[0].Outcome.State; got != storage.StateWaitingForInput {
				t.Errorf("milestone Outcome.State = %q, want %q", got, storage.StateWaitingForInput)
			}
		})
	}
}

func TestAParkDoesNotDependOnANotifier(t *testing.T) {
	// The nil-observer guard in announceObservation is a design property, not a
	// defensive afterthought: a deployment with no notifier must recover
	// identically to one with a notifier, because delivery sits beside the
	// recovery rather than in its path. So a blocked pass with no observer
	// installed still has to park the session and return without error.
	const id = sessionA
	entry := claude.AgentEntry{
		StateID:    "ab12cd34",
		SessionID:  id,
		Kind:       "background",
		State:      "blocked",
		Status:     "waiting",
		WaitingFor: "sandbox request",
	}
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(id))
	provider := &providerStub{
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 10, 200), nil
		},
		listingFn: func(int) (claude.PublishedListing, error) {
			return claude.PublishedListing{
				Entries:     []claude.AgentEntry{entry},
				BySessionID: map[string]claude.AgentEntry{id: entry},
				ReadAt:      observedAt,
			}, nil
		},
	}
	// testManager installs no observer unless a mutate function does.
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), id)
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if outcome.State != storage.StateWaitingForInput {
		t.Errorf("State = %q, want %q with no notifier installed", outcome.State, storage.StateWaitingForInput)
	}
	if got := provider.resumeCount(); got != 0 {
		t.Errorf("resumes = %d, want 0: no notifier must not turn a park into a resume", got)
	}
}

// R8 is a distinction before it is a behaviour: "the supervisor could not be
// asked" and "the supervisor has no opinion about this session" are the same
// bytes if the error is swallowed. An empty listing is a fact the mapping is
// allowed to act on — it means "not observed", and therefore no decision — while
// an unreadable one is an outage that must reach the caller instead of being
// folded into that fact.
func TestAFailedListingIsReportedAndNotReadAsEmpty(t *testing.T) {
	t.Parallel()

	outage := errors.New("agents --json --all: connection refused")
	entry := claude.AgentEntry{SessionID: sessionA, State: "working", Status: "running"}
	var (
		reads int
		down  = true
	)
	provider := &providerStub{listingFn: func(int) (claude.PublishedListing, error) {
		reads++
		if down {
			return claude.PublishedListing{}, outage
		}
		return claude.PublishedListing{
			Entries:     []claude.AgentEntry{entry},
			BySessionID: map[string]claude.AgentEntry{sessionA: entry},
			ReadAt:      observedAt,
		}, nil
	}}
	source := NewObservationSource(provider, clock.NewFake(observedAt))

	observation, err := source.Observe(context.Background(), sessionA)
	if err == nil {
		t.Fatal("Observe() error = nil, want an unreadable listing reported as an error")
	}
	if !errors.Is(err, ErrObservationUnavailable) {
		t.Errorf("error = %v, want it to wrap ErrObservationUnavailable", err)
	}
	if !errors.Is(err, outage) {
		t.Errorf("error = %v, want it to carry the provider's own failure", err)
	}
	if observation.Recorded() {
		t.Error("observation.Recorded() = true, want no observation recorded from a read that failed")
	}
	if observation.InListing {
		t.Error("observation.InListing = true, want false: this session was not observed, it was unobserved")
	}

	// A second session in the same pass is answered from the same read, and must
	// fail the same way. If the cached failure fell through to the zero listing,
	// that session would look like one the supervisor had no opinion about —
	// exactly the empty-listing reading R8 forbids.
	second, err := source.Observe(context.Background(), sessionB)
	if err == nil {
		t.Fatal("Observe() for a second session error = nil, want the same outage reported")
	}
	if !errors.Is(err, ErrObservationUnavailable) {
		t.Errorf("second error = %v, want it to wrap ErrObservationUnavailable", err)
	}
	if second.Recorded() {
		t.Error("second observation.Recorded() = true, want no observation from an unreadable pass")
	}
	if reads != 1 {
		t.Errorf("listing reads = %d, want 1: one read serves every session in a pass", reads)
	}

	// The failure belongs to the pass, not to the source: Reset ends it and the
	// next pass reads again. Otherwise one bad read would silence a session until
	// the daemon restarted.
	down = false
	source.Reset()
	recovered, err := source.Observe(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Observe() after Reset() error = %v", err)
	}
	if reads != 2 {
		t.Errorf("listing reads = %d, want 2 after a Reset", reads)
	}
	if !recovered.Recorded() || !recovered.InListing {
		t.Errorf("observation after Reset() = %+v, want a recorded listing entry", recovered)
	}
	if recovered.State != "working" {
		t.Errorf("State = %q, want the published %q", recovered.State, "working")
	}
}

// A listing that carries no ReadAt still produces a recorded observation: the
// source's clock stamps it, so "not recorded" keeps meaning "not read" rather
// than "read from a CLI that omits the field".
func TestAnUnstampedListingIsStampedFromTheClock(t *testing.T) {
	t.Parallel()

	entry := claude.AgentEntry{SessionID: sessionA, State: "done", Status: "finished"}
	provider := &providerStub{listingFn: func(int) (claude.PublishedListing, error) {
		return claude.PublishedListing{
			Entries:     []claude.AgentEntry{entry},
			BySessionID: map[string]claude.AgentEntry{sessionA: entry},
		}, nil
	}}
	source := NewObservationSource(provider, clock.NewFake(observedAt))

	observation, err := source.Observe(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if !observation.ObservedAt.Equal(observedAt) {
		t.Errorf("ObservedAt = %v, want the clock's instant %v", observation.ObservedAt, observedAt)
	}
	if !observation.Recorded() {
		t.Error("Recorded() = false, want an observation stamped from the clock to count as recorded")
	}
}

// R8 at the engine. Once the published state is what decides a session, a pass
// that could not read it has no verdict to give: no continuation, nothing
// persisted that reads as recovery, and no attempt spent, because an outage is
// ccresu's problem and not the session's. The pass is rescheduled, which is the
// difference between "ask again" and "the session failed".
func TestAnUnreadableListingDecidesNothing(t *testing.T) {
	const id = sessionA
	outage := errors.New("agents --json --all: connection refused")
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(id))
	provider := &providerStub{
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 10, 200), nil
		},
		listingFn: func(int) (claude.PublishedListing, error) {
			return claude.PublishedListing{}, outage
		},
	}
	var milestones []Milestone
	manager := testManager(t, store, provider, func(cfg *Config) {
		cfg.Observer = func(_ context.Context, m Milestone) {
			milestones = append(milestones, m)
		}
	})

	outcome, err := manager.Recover(context.Background(), id)
	if err == nil {
		t.Fatal("Recover() error = nil, want the unreadable listing reported")
	}
	if !errors.Is(err, ErrObservationUnavailable) {
		t.Errorf("Recover() error = %v, want it to wrap ErrObservationUnavailable", err)
	}
	if !errors.Is(err, outage) {
		t.Errorf("Recover() error = %v, want the provider's failure carried out", err)
	}

	// Nothing was continued, and nothing claims the session recovered.
	if got := provider.resumeCount(); got != 0 {
		t.Errorf("resumes = %d, want 0: an unreadable listing is not a reason to hand off", got)
	}
	if outcome.Resumed {
		t.Error("Resumed = true, want false: an outage is not a verdict about a session")
	}
	if outcome.State == storage.StateResumed {
		t.Errorf("State = %q, want anything but %q", outcome.State, storage.StateResumed)
	}

	// The attempt stays open: no attempt row was written or finished, and the
	// pass is scheduled to ask again rather than treated as a failure of the
	// session.
	attempts, err := store.ListAttempts(context.Background(), id, 0)
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 0 {
		t.Errorf("attempts = %d, want 0: an outage spends no budget", len(attempts))
	}
	stored, err := store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if stored.State != storage.StateRetryWait {
		t.Errorf("persisted State = %q, want %q", stored.State, storage.StateRetryWait)
	}
	if stored.RetryAt == nil {
		t.Error("RetryAt = nil, want the next pass written down so the retry is not lost")
	}
	if !strings.Contains(stored.LastError, outage.Error()) {
		t.Errorf("LastError = %q, want the provider's failure recorded verbatim", stored.LastError)
	}

	// The operator hears "still working on it", never "recovered".
	for _, m := range milestones {
		if m.Kind == MilestoneSucceeded {
			t.Error("MilestoneSucceeded raised for a pass that never read the published state")
		}
	}
}

// R6 at the engine. The mapping test proves `done` + no handoff means COMPLETED,
// but the decision only binds if a real pass acts on it: a conversation that
// finished its last turn must not be handed a "continue" message, and must not
// be reported RESUMED — resuming finished work would duplicate it.
func TestADoneSessionIsSettledWithoutAContinuation(t *testing.T) {
	const id = sessionA
	entry := claude.AgentEntry{
		StateID:   "ab12cd34",
		SessionID: id,
		Kind:      "background",
		State:     "done",
		Status:    "finished",
	}
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(id))
	provider := &providerStub{
		statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return statusFor(sessionID, 10, 200), nil
		},
		listingFn: func(int) (claude.PublishedListing, error) {
			return claude.PublishedListing{
				Entries:     []claude.AgentEntry{entry},
				BySessionID: map[string]claude.AgentEntry{id: entry},
				ReadAt:      observedAt,
			}, nil
		},
	}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), id)
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if got := provider.resumeCount(); got != 0 {
		t.Errorf("resumes = %d, want 0: a finished conversation is not sent a continuation", got)
	}
	if outcome.Resumed {
		t.Error("Resumed = true, want false: `done` without an outstanding handoff is COMPLETED")
	}
	if outcome.State != storage.StateCompleted {
		t.Errorf("State = %q, want %q", outcome.State, storage.StateCompleted)
	}
	stored, err := store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if stored.State != storage.StateCompleted {
		t.Errorf("persisted State = %q, want %q", stored.State, storage.StateCompleted)
	}
	// The published state is the proof, so it is what the attempt records rather
	// than a transcript measurement standing in for it.
	if !outcome.Verification.Observation.Recorded() {
		t.Error("Verification.Observation not recorded, want the published state carried on the outcome")
	}
	if got := outcome.Verification.Observation.State; got != "done" {
		t.Errorf("Observation.State = %q, want %q", got, "done")
	}
}
