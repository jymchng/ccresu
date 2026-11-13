package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/scheduler"
	"github.com/jymchng/ccresu/internal/storage"
)

// limitParker builds the hook the recovery manager calls when `claude -p
// --resume` was refused by a usage limit.
//
// The manager knows a refusal happened; it does not know how to read a banner or
// how to persist a wait, and teaching it either would duplicate the detector and
// the scheduler. So the app supplies the bridge: the detector interprets the
// banner, the scheduler resolves the reset phrase and stores the wait, and the
// usage_limits row records what the CLI actually said.
//
// This is deliberately the same seam Daemon.detectIn uses. A limit learned from
// a refusal and a limit learned from a transcript must leave the database in the
// same shape, or `ccresu inspect` would describe two different kinds of wait for
// the same fact.
func limitParker(store *storage.Store, sched *scheduler.Scheduler, det *detector.Detector,
	clk clock.Clock, log *slog.Logger) recovery.LimitPark {
	return func(ctx context.Context, sessionID, banner string) (recovery.Wait, error) {
		if store == nil || sched == nil {
			return recovery.Wait{}, errors.New("app: no store or scheduler to plan the wait")
		}
		if clk == nil {
			clk = clock.System()
		}
		now := clk.Now().UTC()

		// The CLI said the limit is in force, so the wait is real even when the
		// banner cannot be read: an unparsed phrase degrades to polling rather
		// than to another resume.
		var event detector.UsageLimitEvent
		if det != nil {
			event = det.Analyze(banner)
		}
		phrase := detector.ResetPhrase{}
		kind := storage.LimitTypeOther
		confidence := 0.0
		paused := false
		if event.IsUsageLimit() {
			phrase = event.Reset
			kind = limitType(event.Limit)
			confidence = event.Confidence
			paused = event.PausedGoal
		}

		plan, resolution, err := sched.Plan(sessionID, 0, phrase, now)
		if err != nil {
			return recovery.Wait{}, err
		}
		recorded, err := store.RecordUsageLimit(ctx, storage.UsageLimit{
			SessionID: sessionID,
			LimitType: kind,
			// The banner is the evidence, so it is what is stored; bounded so a
			// hostile banner cannot fill the database.
			RawMessage:    truncateText(banner, 2000),
			ResetAt:       plan.ResetAt,
			ResetTimezone: plan.ResetTimezone,
			ResetRaw:      plan.ResetRaw,
			Confidence:    confidence,
			PausedGoal:    paused,
			DetectedAt:    now,
		})
		if err != nil {
			return recovery.Wait{}, err
		}

		plan.UsageLimitID = recorded.ID
		if _, err := sched.Save(ctx, plan); err != nil {
			return recovery.Wait{}, err
		}

		dueAt, _ := plan.DueAt()
		wait := recovery.Wait{At: dueAt, Known: resolution.Known, LimitID: recorded.ID}
		if delay, ok := plan.Wait(now); ok {
			wait.Delay = delay
		}
		if log != nil {
			log.Info("usage limit refusal recorded",
				"session", sessionID, "known_reset", resolution.Known,
				"reset_at", plan.ResetAt, "due_at", wait.At, "wait", wait.Delay)
		}
		return wait, nil
	}
}
