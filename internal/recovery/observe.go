package recovery

import "context"

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
}

// Observer receives the milestones of every recovery.
//
// It is called on the goroutine performing the recovery, so it must return
// promptly: the whole point of the notification queue is that delivery cannot
// delay a resume. It returns nothing and cannot fail the recovery, so an
// implementation must swallow and log its own errors rather than propagate them
// — a broken Telegram token must never turn a successful resume into a failure.
type Observer func(ctx context.Context, m Milestone)
