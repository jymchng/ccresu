package recovery

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/storage"
)

// ErrObservationUnavailable reports that the supervisor's published state could
// not be read, so no observation was made.
//
// It is deliberately its own error rather than a generic failure: an unreadable
// listing is an outage, and an outage must not become a verdict about a
// session. The attempt stays open and the next pass asks again, which is why the
// manager treats this as a retryable failure that spends no budget.
var ErrObservationUnavailable = errors.New("recovery: the published session state could not be read")

// ObservationSource reads `claude agents --json --all` at most once per recovery
// pass and serves every session in that pass from the one read.
//
// The listing is a list, and the documented way to read session state from
// outside Claude Code. Asking it once per session would turn a machine-wide read
// into N reads and, worse, would answer different sessions from different
// instants while claiming they were observed together. One read per pass is what
// makes "the supervisor said this about these sessions, now" a true statement.
//
// The cache is ended by Reset, which a caller driving a pass calls before it
// begins. A Manager used one-off (`ccresu recover`) simply reads once.
type ObservationSource struct {
	// Provider supplies the listing. Nil means no observation can be made.
	Provider claude.SessionProvider
	// Clock stamps the observation. Nil means the system clock.
	Clock clock.Clock

	mu      sync.Mutex
	listing claude.PublishedListing
	read    bool
	err     error
}

// NewObservationSource builds a source over a provider and a clock.
func NewObservationSource(provider claude.SessionProvider, clk clock.Clock) *ObservationSource {
	if clk == nil {
		clk = clock.System()
	}
	return &ObservationSource{Provider: provider, Clock: clk}
}

// Reset ends the current pass, so the next Observe reads the listing again.
func (s *ObservationSource) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listing = claude.PublishedListing{}
	s.read = false
	s.err = nil
}

// Observe returns what the supervisor published about one session.
//
// The error is returned rather than folded into the observation because a failed
// read and a session that is simply absent are different facts (R8). A caller
// that receives an error must not treat the session as observed.
func (s *ObservationSource) Observe(ctx context.Context, sessionID string) (StateObservation, error) {
	if s == nil || s.Provider == nil {
		return StateObservation{}, fmt.Errorf("%w: no session provider is configured", ErrObservationUnavailable)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.read {
		s.listing, s.err = s.Provider.PublishedListing(ctx)
		s.read = true
	}
	if s.err != nil {
		return StateObservation{}, fmt.Errorf("%w: %w", ErrObservationUnavailable, s.err)
	}

	observation := StateObservation{
		SessionID:  sessionID,
		ObservedAt: s.listing.ReadAt,
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = s.Clock.Now().UTC()
	}
	entry, listed := s.listing.Entry(sessionID)
	if !listed {
		return observation, nil
	}
	observation.InListing = true
	observation.State = entry.State
	observation.Status = entry.Status
	observation.WaitingFor = entry.WaitingFor
	return observation, nil
}

// RecordedObservation converts a published observation into the storage record
// an attempt row carries (R13).
//
// An observation that was never made is left as the zero record, so `ccresu
// inspect` can say "not recorded" instead of presenting a missing read as an
// empty state.
func RecordedObservation(obs StateObservation) (record storage.ObservationRecord) {
	if !obs.Recorded() {
		return record
	}
	listed := obs.InListing
	return storage.ObservationRecord{
		State:      obs.State,
		Status:     obs.Status,
		WaitingFor: obs.WaitingFor,
		ObservedAt: obs.ObservedAt,
		Listed:     &listed,
	}
}
