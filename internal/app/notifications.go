package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"
)

// AttentionEpisodes is the notification bookkeeping a needs_input message is
// keyed on. *storage.Store satisfies it.
//
// It is a one-method interface rather than the whole store because the observer
// needs exactly one fact from it: when the episode two producers are describing
// began. Passing the store would let this code write session state, which the
// hook path may not do (PRD 4 R11) and the recovery path has no reason to.
type AttentionEpisodes interface {
	// BeginAttentionEpisode records that the session is waiting and returns the
	// instant the episode began, which is the first observer's.
	BeginAttentionEpisode(ctx context.Context, sessionID, reason string, at time.Time) (time.Time, error)
}

// notificationObserver maps recovery milestones onto the notification queue.
//
// It is the producer for five of the eight events the templates render:
// recovery_started, recovery_success, recovery_failure, permanent_failure and —
// since PRD 4 — needs_input. The other three are produced elsewhere:
// session_completed by the daemon (a settled session never enters the engine) and
// limit_detected/waiting_for_reset by the limit path.
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
func notificationObserver(notifier Notifier, episodes AttentionEpisodes, log *slog.Logger) recovery.Observer {
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
		data := milestoneData(milestone)
		if event == storage.EventNeedsInput {
			// Both producers of this event must compute the same dedupe key, and
			// the only fact they can agree on is the persisted episode instant.
			// Without it the message has no identity, so no message is queued.
			if episodes == nil {
				log.Warn("cannot announce a blocked session without attention episodes",
					"session", milestone.Outcome.SessionID)
				return
			}
			reason, _ := milestone.NeedsInputReason()
			start, err := episodes.BeginAttentionEpisode(ctx, milestone.Outcome.SessionID, reason, time.Time{})
			if err != nil {
				log.Warn("record the attention episode",
					"session", milestone.Outcome.SessionID, "error", err)
				return
			}
			data.AttentionStart = start
		}
		_, _, err := notifier.Notify(ctx, telegram.Request{
			Event:     event,
			SessionID: milestone.Outcome.SessionID,
			AttemptID: milestone.Outcome.Attempt.ID,
			Data:      data,
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
	case recovery.MilestoneNeedsInput:
		return storage.EventNeedsInput, true
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
	// A needs_input milestone carries the reason the supervisor published, and
	// nothing else: a blocked session has no failure to report, and the template
	// states the absence of a reason rather than inventing one.
	if milestone.Kind == recovery.MilestoneNeedsInput {
		if reason, reported := milestone.NeedsInputReason(); reported {
			data.Reason = reason
		}
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
