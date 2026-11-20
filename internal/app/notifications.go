package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"
)

// notificationObserver maps recovery milestones onto the notification queue.
//
// It is the producer for four of the events the templates render:
// recovery_started, recovery_success, recovery_failure and permanent_failure.
// The others are produced elsewhere: session_completed by the daemon (a settled
// session never enters the engine) and limit_detected/waiting_for_reset by the
// limit path.
//
// There is deliberately no needs_input producer. A `blocked` observation is
// handed a continuation like any other (see recovery.Observe), so "this session
// needs you" is not a fact ccresu can report: the session it would be about is
// one ccresu is already resuming. The milestone kind, the hook that raised it,
// the event it queued and the attention-episode bookkeeping that keyed it are
// all gone together, which is why this function takes nothing but the notifier.
//
// The observer runs on the recovery goroutine, so it does the cheapest thing that
// is still durable: render, insert a row, wake the sender. Notify never touches
// the network — delivery happens on the sender goroutine started by Run — and an
// error is logged rather than returned, because the observer cannot fail a
// recovery: a broken bot token must never turn a successful resume into a failed
// one.
//
// A nil notifier yields a nil observer, which is what makes "notifications are
// off" cost nothing: the manager then skips the hook entirely rather than calling
// a function that discards its argument.
func notificationObserver(notifier Notifier, log *slog.Logger) recovery.Observer {
	if notifier == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, milestone recovery.Milestone) {
		event, ok := notificationEvent(milestone.Kind)
		if !ok {
			log.Warn("recovery milestone has no notification event",
				"milestone", string(milestone.Kind),
				"session", milestone.Outcome.SessionID)
			return
		}
		// A switch that is off is not an error: Notify would report ErrDisabled
		// for every milestone of every recovery, which would be noise in the log
		// for a deliberate configuration choice.
		if !notifier.Enabled(event) {
			return
		}
		_, _, err := notifier.Notify(ctx, telegram.Request{
			Event:     event,
			SessionID: milestone.Outcome.SessionID,
			AttemptID: milestone.Outcome.Attempt.ID,
			Data:      milestoneData(milestone),
		})
		if err != nil && !errors.Is(err, telegram.ErrDisabled) {
			log.Warn("queue recovery notification",
				"event", string(event),
				"session", milestone.Outcome.SessionID,
				"error", err)
		}
	}
}

// notificationEvent maps a recovery milestone onto its notification event.
//
// The two vocabularies are deliberately separate: recovery.MilestoneKind names a
// moment in the engine, storage.NotificationEvent names a message an operator
// subscribed to. Keeping the mapping here means a new milestone is a compile-time
// visible omission (the ok result) rather than a silently dropped message, and
// TestEveryMilestoneHasAnEvent walks every kind so the default can never be
// reached by a kind the engine actually emits.
func notificationEvent(kind recovery.MilestoneKind) (storage.NotificationEvent, bool) {
	switch kind {
	case recovery.MilestoneStarted:
		return storage.EventRecoveryStarted, true
	case recovery.MilestoneSucceeded:
		return storage.EventRecoverySuccess, true
	case recovery.MilestoneFailed:
		return storage.EventRecoveryFailure, true
	case recovery.MilestonePermanent:
		return storage.EventPermanentFailure, true
	default:
		return "", false
	}
}

// milestoneData converts a milestone into the flat template values.
//
// The dedupe key is computed by the notifier from AttemptID and Attempt, so both
// are carried through even when only one is known: an event tied to an attempt row
// dedupes on the row id, and one that spent no budget dedupes on the attempt
// number instead.
func milestoneData(milestone recovery.Milestone) telegram.EventData {
	attempt := milestone.Outcome.Attempt
	data := telegram.EventData{
		SessionID:   milestone.Outcome.SessionID,
		ProjectPath: milestone.ProjectPath,
		RunsInPath:  milestone.RunsInPath,
		AttemptID:   attempt.ID,
		Attempt:     attempt.AttemptNumber,
		MaxAttempts: milestone.MaxAttempts,
		State:       string(milestone.Outcome.State),
		Delay:       milestone.Outcome.Delay,
	}
	if milestone.Cause != nil {
		data.Reason = milestone.Cause.Error()
	}
	// A finished attempt knows how long the resume took, which is the only
	// duration a message can honestly report. An unfinished or absent row leaves
	// it zero, and the template then omits the line rather than printing "0s".
	if !attempt.StartedAt.IsZero() && !attempt.FinishedAt.IsZero() {
		if elapsed := attempt.FinishedAt.Sub(attempt.StartedAt); elapsed > 0 {
			data.Duration = elapsed
		}
	}
	return data
}
