package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/detector"
)

// ErrNoSession reports a schedule attempt with no session ID.
var ErrNoSession = errors.New("scheduler: session id is required")

// Config assembles a Scheduler.
//
// Every field has a safe default, so the zero Config is usable: system clock,
// UTC, five-minute initial poll and thirty-minute maximum.
type Config struct {
	// Clock is the time source. Defaults to the system clock.
	Clock clock.Clock
	// Location resolves phrases that name no timezone. Defaults to UTC, so
	// behaviour never depends on the host's TZ setting.
	Location *time.Location
	// LocationName is how Location is reported. Defaults to Location.String().
	LocationName string
	// PreferEarlierOnAmbiguous chooses the earlier of a repeated wall clock.
	// The default (false) chooses the later one, which is the safe direction.
	PreferEarlierOnAmbiguous bool
	// InitialInterval is the first poll delay for an unknown reset time.
	InitialInterval time.Duration
	// MaxInterval caps the poll delay.
	MaxInterval time.Duration
	// Store persists schedules. Nil means schedules are not persisted, which is
	// only appropriate for a one-shot command.
	Store Store
}

// Scheduler resolves reset phrases and waits for them.
type Scheduler struct {
	clock   clock.Clock
	opts    Options
	initial time.Duration
	max     time.Duration
	store   Store
}

// New builds a Scheduler from cfg, applying defaults.
func New(cfg Config) (*Scheduler, error) {
	if cfg.Clock == nil {
		cfg.Clock = clock.System()
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if cfg.LocationName == "" {
		cfg.LocationName = cfg.Location.String()
	}
	if cfg.InitialInterval <= 0 {
		cfg.InitialInterval = DefaultInitialInterval
	}
	if cfg.MaxInterval <= 0 {
		cfg.MaxInterval = DefaultMaxInterval
	}
	if cfg.MaxInterval < cfg.InitialInterval {
		return nil, fmt.Errorf("scheduler: max interval %s is shorter than the initial interval %s",
			cfg.MaxInterval, cfg.InitialInterval)
	}
	return &Scheduler{
		clock: cfg.Clock,
		opts: Options{
			Location:                 cfg.Location,
			LocationName:             cfg.LocationName,
			PreferEarlierOnAmbiguous: cfg.PreferEarlierOnAmbiguous,
		},
		initial: cfg.InitialInterval,
		max:     cfg.MaxInterval,
		store:   cfg.Store,
	}, nil
}

// FromConfig builds a Scheduler from the application configuration.
//
// It returns an error for an unusable app.timezone rather than silently falling
// back to UTC, because a wrong zone moves every wake-up by hours.
func FromConfig(cfg *config.Config, clk clock.Clock, store Store) (*Scheduler, error) {
	if cfg == nil {
		return nil, errors.New("scheduler: nil config")
	}
	location := time.UTC
	locationName := "UTC"
	if zone := cfg.App.Timezone; zone != "" {
		loaded, err := LoadLocation(zone)
		if err != nil {
			return nil, fmt.Errorf("scheduler: app.timezone: %w", err)
		}
		location, locationName = loaded, zone
	}
	return New(Config{
		Clock:           clk,
		Location:        location,
		LocationName:    locationName,
		InitialInterval: cfg.Scheduler.UnknownResetInitialInterval.Duration,
		MaxInterval:     cfg.Scheduler.UnknownResetMaxInterval.Duration,
		Store:           store,
	})
}

// Options returns the resolution options in force.
func (s *Scheduler) Options() Options { return s.opts }

// Clock returns the scheduler's time source.
func (s *Scheduler) Clock() clock.Clock { return s.clock }

// Now reports the current time in UTC.
func (s *Scheduler) Now() time.Time { return s.clock.Now().UTC() }

// Resolve resolves a reset phrase against the scheduler's options.
func (s *Scheduler) Resolve(phrase detector.ResetPhrase, now time.Time) Resolution {
	return Resolve(phrase, now, s.opts)
}

// Plan resolves a detection into a schedule without persisting it.
//
// A resolution that failed produces an unknown-reset schedule whose first poll
// is one initial interval away: not knowing the reset time degrades to polling
// rather than to giving up.
func (s *Scheduler) Plan(sessionID string, usageLimitID int64, phrase detector.ResetPhrase, now time.Time) (Schedule, Resolution, error) {
	if sessionID == "" {
		return Schedule{}, Resolution{}, ErrNoSession
	}
	now = now.UTC()
	res := Resolve(phrase, now, s.opts)

	sched := Schedule{
		SessionID:     sessionID,
		UsageLimitID:  usageLimitID,
		ResetTimezone: res.LocationName,
		ResetRaw:      phrase.Raw,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if res.Known {
		sched.ResetAt = res.At
		if sched.ResetTimezone == "" {
			sched.ResetTimezone = phrase.Timezone
		}
		return sched, res, nil
	}
	sched.Unknown = true
	sched.Attempts = 0
	sched.NextPollAt = now.Add(s.initial)
	return sched, res, nil
}

// PlanEvent plans from a detector event, using its embedded reset phrase.
//
// The event is session-agnostic by design, so the caller supplies the session it
// was found in.
func (s *Scheduler) PlanEvent(sessionID string, event detector.UsageLimitEvent, now time.Time) (Schedule, Resolution, error) {
	return s.Plan(sessionID, 0, event.Reset, now)
}

// Save persists a schedule.
func (s *Scheduler) Save(ctx context.Context, sched Schedule) (Schedule, error) {
	if s.store == nil {
		return sched, nil
	}
	return s.store.SaveSchedule(ctx, sched)
}

// Restore loads pending schedules, which is what makes a restart resumable.
func (s *Scheduler) Restore(ctx context.Context) ([]Schedule, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.LoadSchedules(ctx)
}

// Complete removes a session's schedule once its wait is over.
func (s *Scheduler) Complete(ctx context.Context, sessionID string) error {
	if s.store == nil {
		return nil
	}
	return s.store.DeleteSchedule(ctx, sessionID)
}

// Next returns the schedule with the earliest deadline that is not yet finished.
//
// Schedules with no deadline are skipped rather than returned: they would make
// the caller wait forever.
func (s *Scheduler) Next(schedules []Schedule, now time.Time) (Schedule, bool) {
	var (
		best   Schedule
		bestAt time.Time
		found  bool
	)
	for _, sched := range schedules {
		at, ok := sched.DueAt()
		if !ok {
			continue
		}
		if !found || at.Before(bestAt) || (at.Equal(bestAt) && sched.SessionID < best.SessionID) {
			best, bestAt, found = sched, at, true
		}
	}
	return best, found
}

// Wait blocks until the schedule is due or ctx is done, and returns the instant
// it observed. A schedule that is already due returns immediately.
func (s *Scheduler) Wait(ctx context.Context, sched Schedule) (time.Time, error) {
	if ctx == nil {
		return time.Time{}, errors.New("scheduler: nil context")
	}
	now := s.Now()
	remaining, ok := sched.Wait(now)
	if !ok {
		return now, fmt.Errorf("%w: session %s", ErrNoDeadline, sched.SessionID)
	}
	if remaining <= 0 {
		return now, ctx.Err()
	}
	select {
	case <-ctx.Done():
		return s.Now(), ctx.Err()
	case <-s.clock.After(remaining):
		return s.Now(), ctx.Err()
	}
}

// Advance records one poll on an unknown-reset schedule and returns the schedule
// with its next poll time, applying exponential backoff. It persists the result
// when a store is configured, so a restart continues at the same interval.
//
// Gaps between polls are 5m, 10m, 20m, 30m…: the interval a poll waited out is
// the one named by the attempt count, and the next gap is one doubling longer.
func (s *Scheduler) Advance(ctx context.Context, sched Schedule, now time.Time) (Schedule, error) {
	if !sched.Unknown {
		return sched, nil
	}
	now = now.UTC()
	sched.Attempts++
	sched.NextPollAt = now.Add(s.IntervalFor(sched))
	sched.UpdatedAt = now
	return s.Save(ctx, sched)
}

// PollDelay reports the gap before poll number attempt, one-based.
func (s *Scheduler) PollDelay(attempt int) time.Duration {
	return Backoff(attempt, s.initial, s.max)
}

// IntervalFor reports the gap before the schedule's next poll.
func (s *Scheduler) IntervalFor(sched Schedule) time.Duration {
	return Backoff(sched.Attempts+1, s.initial, s.max)
}
