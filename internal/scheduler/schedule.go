package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// ErrNoDeadline reports a schedule that has neither a reset time nor a poll
// time, so waiting for it could never finish.
var ErrNoDeadline = errors.New("schedule has no deadline")

// Schedule is one pending wait for a usage limit to reset.
//
// It is the unit the scheduler persists: a restart loads these rows and keeps
// waiting, which is what makes recovery survive a reboot as well as a crash.
type Schedule struct {
	// SessionID is the Claude Code session being waited for.
	SessionID string
	// UsageLimitID links the wait to the detection that caused it.
	UsageLimitID int64
	// ResetAt is the resolved reset instant in UTC. Zero when Unknown.
	ResetAt time.Time
	// ResetTimezone preserves the zone the phrase was written in.
	ResetTimezone string
	// ResetRaw preserves the original phrase, e.g. "12pm (Europe/Berlin)".
	ResetRaw string
	// Unknown reports that the message carried no usable reset time, so the
	// session must be polled instead of woken at a fixed instant.
	Unknown bool
	// Attempts counts the polls spent so far; it persists so backoff survives a
	// restart instead of snapping back to five minutes.
	Attempts int
	// NextPollAt is when an unknown reset should next be polled.
	NextPollAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DueAt returns the instant this schedule cares about and whether it has one.
func (s Schedule) DueAt() (time.Time, bool) {
	if s.Unknown {
		if s.NextPollAt.IsZero() {
			return time.Time{}, false
		}
		return s.NextPollAt.UTC(), true
	}
	if s.ResetAt.IsZero() {
		return time.Time{}, false
	}
	return s.ResetAt.UTC(), true
}

// Due reports whether the schedule is ready to fire at now.
func (s Schedule) Due(now time.Time) bool {
	at, ok := s.DueAt()
	return ok && !at.After(now.UTC())
}

// Wait returns how long remains before the schedule is due; zero when it
// already is.
func (s Schedule) Wait(now time.Time) (time.Duration, bool) {
	at, ok := s.DueAt()
	if !ok {
		return 0, false
	}
	remaining := at.Sub(now.UTC())
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

// Store persists pending schedules.
//
// The interface is deliberately small: anything that can round-trip these rows
// works, which keeps the scheduler testable without a database and lets the
// daemon supply the SQLite-backed implementation.
type Store interface {
	// SaveSchedule inserts or replaces the pending schedule for a session.
	SaveSchedule(ctx context.Context, sched Schedule) (Schedule, error)
	// LoadSchedules returns every pending schedule, earliest deadline first.
	LoadSchedules(ctx context.Context) ([]Schedule, error)
	// DeleteSchedule removes the pending schedule for a session.
	DeleteSchedule(ctx context.Context, sessionID string) error
}

// MemoryStore is an in-memory Store for tests and for running without a
// database. It is safe for concurrent use.
type MemoryStore struct {
	mu    sync.Mutex
	rows  map[string]Schedule
	fails error
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[string]Schedule{}}
}

// FailWith makes every subsequent call return err, so failure paths can be
// exercised without a database.
func (m *MemoryStore) FailWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fails = err
}

// SaveSchedule stores or replaces a schedule.
func (m *MemoryStore) SaveSchedule(_ context.Context, sched Schedule) (Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fails != nil {
		return Schedule{}, m.fails
	}
	if strings.TrimSpace(sched.SessionID) == "" {
		return Schedule{}, errors.New("scheduler: session id is required")
	}
	if now := sched.UpdatedAt; now.IsZero() {
		sched.UpdatedAt = time.Now().UTC()
	}
	if sched.CreatedAt.IsZero() {
		if existing, ok := m.rows[sched.SessionID]; ok {
			sched.CreatedAt = existing.CreatedAt
		} else {
			sched.CreatedAt = sched.UpdatedAt
		}
	}
	m.rows[sched.SessionID] = sched
	return sched, nil
}

// LoadSchedules returns the stored schedules, earliest deadline first.
func (m *MemoryStore) LoadSchedules(_ context.Context) ([]Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fails != nil {
		return nil, m.fails
	}
	out := make([]Schedule, 0, len(m.rows))
	for _, sched := range m.rows {
		out = append(out, sched)
	}
	sort.Slice(out, func(i, j int) bool {
		left, leftOK := out[i].DueAt()
		right, rightOK := out[j].DueAt()
		if leftOK != rightOK {
			return leftOK
		}
		if !left.Equal(right) {
			return left.Before(right)
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
}

// DeleteSchedule removes a schedule; deleting an absent one is not an error.
func (m *MemoryStore) DeleteSchedule(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fails != nil {
		return m.fails
	}
	delete(m.rows, sessionID)
	return nil
}

// StorageStore adapts the SQLite repository to the scheduler's Store interface.
//
// The two packages keep separate types on purpose: the scheduler owns the
// behaviour of a wait, the storage package owns its representation, and neither
// has to import the other's internals.
type StorageStore struct {
	store *storage.Store
}

// NewStorageStore wraps a storage.Store.
func NewStorageStore(store *storage.Store) *StorageStore {
	return &StorageStore{store: store}
}

// SaveSchedule persists a schedule.
func (s *StorageStore) SaveSchedule(ctx context.Context, sched Schedule) (Schedule, error) {
	if s.store == nil {
		return Schedule{}, errors.New("scheduler: no storage configured")
	}
	saved, err := s.store.SaveSchedule(ctx, toStorageSchedule(sched))
	if err != nil {
		return Schedule{}, err
	}
	return fromStorageSchedule(saved), nil
}

// LoadSchedules loads every pending schedule.
func (s *StorageStore) LoadSchedules(ctx context.Context) ([]Schedule, error) {
	if s.store == nil {
		return nil, errors.New("scheduler: no storage configured")
	}
	rows, err := s.store.LoadSchedules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Schedule, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromStorageSchedule(row))
	}
	return out, nil
}

// DeleteSchedule removes a pending schedule.
func (s *StorageStore) DeleteSchedule(ctx context.Context, sessionID string) error {
	if s.store == nil {
		return errors.New("scheduler: no storage configured")
	}
	return s.store.DeleteSchedule(ctx, sessionID)
}

// toStorageSchedule converts to the persistence representation.
func toStorageSchedule(sched Schedule) storage.Schedule {
	return storage.Schedule{
		SessionID:     sched.SessionID,
		UsageLimitID:  sched.UsageLimitID,
		ResetAt:       sched.ResetAt,
		ResetTimezone: sched.ResetTimezone,
		ResetRaw:      sched.ResetRaw,
		Unknown:       sched.Unknown,
		Attempts:      sched.Attempts,
		NextPollAt:    sched.NextPollAt,
		CreatedAt:     sched.CreatedAt,
		UpdatedAt:     sched.UpdatedAt,
	}
}

// fromStorageSchedule converts from the persistence representation.
func fromStorageSchedule(row storage.Schedule) Schedule {
	return Schedule{
		SessionID:     row.SessionID,
		UsageLimitID:  row.UsageLimitID,
		ResetAt:       row.ResetAt,
		ResetTimezone: row.ResetTimezone,
		ResetRaw:      row.ResetRaw,
		Unknown:       row.Unknown,
		Attempts:      row.Attempts,
		NextPollAt:    row.NextPollAt,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

// String renders a schedule for logs.
func (s Schedule) String() string {
	if s.Unknown {
		return fmt.Sprintf("schedule(%s unknown reset, poll %s, attempts %d)",
			s.SessionID, s.NextPollAt.Format(time.RFC3339), s.Attempts)
	}
	return fmt.Sprintf("schedule(%s reset at %s)", s.SessionID, s.ResetAt.Format(time.RFC3339))
}
