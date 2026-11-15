package recovery

import (
	"context"
	"strings"
)

// MilestoneKind names a recovery moment an operator may want to be told about.
//
// The names are the vocabulary of the recovery engine, not of a transport:
// internal/app maps them onto storage.NotificationEvent when a notifier is
// installed. A caller with no notifier leaves Config.Observer nil, and the
// manager then announces nothing.
type MilestoneKind string

const (
	// MilestoneStarted is reported the moment an attempt row exists, which is
	// when ccresu is actually about to resume the conversation. It is reported
	// after StartAttempt rather than before, because a request that spends no
	// attempt — an ineligible session, a spent budget — has started nothing.
	MilestoneStarted MilestoneKind = "recovery_started"
	// MilestoneSucceeded is reported when the conversation is proven to have
	// continued: either ccresu resumed it and the transcript grew, or the
	// session had already resumed on its own once the window reset.
	MilestoneSucceeded MilestoneKind = "recovery_success"
	// MilestoneFailed is reported for a retryable failure: the attempt failed,
	// the session is in RETRY_WAIT, and ccresu will try again by itself.
	//
	// A failure reported before the resume was issued — the pre-resume status
	// check or the limit check — is announced too: it also rests in RETRY_WAIT
	// with a scheduled retry, and it is the only notice an operator gets that
	// ccresu is looping without spending budget. A usage-limit refusal is not
	// announced at all: it is a wait with a known end, not a failure, and the
	// limit-detected messages already reported it.
	MilestoneFailed MilestoneKind = "recovery_failure"
	// MilestonePermanent is reported for a failure the retry policy classified
	// ClassPermanent — an authentication failure, a missing binary, a session id
	// the CLI will not accept.
	//
	// It is deliberately not reported for a spent attempt budget: that returns
	// the session to DISCOVERED, which is a pause in a cycle rather than a
	// verdict about the conversation, and calling it "automatic recovery
	// stopped" would be a false alarm every night.
	MilestonePermanent MilestoneKind = "permanent_failure"
	// MilestoneNeedsInput is reported when the supervisor publishes `blocked`
	// for a session: it is waiting on a permission prompt, a sandbox decision,
	// an answer, or an expired login that only the operator can clear.
	//
	// The kind is named for the event PRD 4 maps it onto (`needs_input`), so the
	// two vocabularies cannot drift apart.
	//
	// It is raised whether or not the observation carried a `waitingFor`. A live
	// payload shows blocked entries with no reason at all, and a missing reason
	// is a fact worth reporting rather than a reason to stay silent. Use
	// Milestone.NeedsInputReason to render both cases honestly.
	MilestoneNeedsInput MilestoneKind = "needs_input"
)

// Milestone is one recovery moment, carrying the facts a message needs.
type Milestone struct {
	// Kind says what happened.
	Kind MilestoneKind
	// Outcome is the outcome as it stands at this moment: the session id, the
	// state the session is in, the attempt row when one exists, the retry delay
	// and the verification.
	Outcome Outcome
	// MaxAttempts is the attempt budget the attempt was counted against, so a
	// message can say "2/10" without consulting the configuration again.
	MaxAttempts int
	// Cause is the failure, for the two failure kinds. Nil otherwise.
	Cause error
	// ProjectPath is the session's working directory, when the caller knows it.
	// The recovery engine leaves it empty; `ccresu hook` fills it from the
	// persisted session row, so a needs-input message can name the project the
	// prompt belongs to without the hook resolving or trusting the payload's
	// cwd.
	ProjectPath string
	// RunsInPath is where the session actually runs, when the caller knows it
	// (PRD 6 R1). It is filled from the persisted row like ProjectPath, and it
	// differs exactly when the session has moved into a worktree — the case a
	// message should name rather than hide.
	RunsInPath string
	// Observation is the published state a needs-input milestone was raised
	// from, verbatim. It is the payload the message is built from, so the
	// operator sees what the CLI said — including that it said no reason —
	// rather than a paraphrase.
	Observation StateObservation
}

// NeedsInputReason returns the reason the supervisor published, verbatim, and
// whether it published one at all.
//
// The two answers are what keep the operator-facing message honest: a blocked
// session with `waitingFor: "permission prompt"` names the prompt, and a blocked
// session with no `waitingFor` says no reason was reported. Neither case invents
// the other, and neither suppresses the milestone.
func (m Milestone) NeedsInputReason() (reason string, reported bool) {
	reason = strings.TrimSpace(m.Observation.WaitingFor)
	return reason, reason != ""
}

// Observer receives the milestones of every recovery.
//
// It is called on the goroutine performing the recovery, so it must return
// promptly: the whole point of the notification queue is that delivery cannot
// delay a resume. It returns nothing and cannot fail the recovery, so an
// implementation must swallow and log its own errors rather than propagate them
// — a broken Telegram token must never turn a successful resume into a failure.
type Observer func(ctx context.Context, m Milestone)
