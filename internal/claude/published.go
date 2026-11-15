package claude

import (
	"context"
	"strings"
	"time"
)

// PublishedListing is one read of the supervisor's published session state.
//
// It exists because "the session was not in the listing" and "the session was in
// the listing but published no state" are different facts, and the merged
// Session record cannot tell them apart: a session the CLI has never heard of
// and one the CLI just reported as `working` both arrive downstream with an
// empty State. Carrying the listing read itself — every entry, whether the read
// succeeded, and when it happened — is what lets a caller distinguish those,
// and refuse to turn "the supervisor said nothing" into "the session is fine".
//
// The listing is the supported read: the files under ~/.claude/jobs/ are
// documented as unstable and are never consulted for state.
type PublishedListing struct {
	// Entries is every entry the CLI printed, in its order.
	Entries []AgentEntry
	// BySessionID indexes Entries by conversation UUID. An entry with no
	// SessionID cannot be attributed to a conversation and is not indexed; a
	// duplicate session id keeps the last entry that carries a state, because a
	// state is the only field a decision may rest on.
	BySessionID map[string]AgentEntry
	// ReadAt is when the query was answered. Zero means it was never made.
	ReadAt time.Time
	// Err is the failure that stopped the read, if any. A listing that failed is
	// recorded and reported, never silently treated as an empty listing: that
	// conversion is how an outage becomes a verdict.
	Err error
}

// Failed reports whether the listing could not be read.
func (l PublishedListing) Failed() bool { return l.Err != nil }

// Entry returns the published entry for a conversation and whether it was
// present at all.
func (l PublishedListing) Entry(sessionID string) (AgentEntry, bool) {
	if l.BySessionID == nil {
		return AgentEntry{}, false
	}
	entry, ok := l.BySessionID[sessionID]
	return entry, ok
}

// PublishedListing reads `claude agents --json --all` once and indexes it.
//
// `--all` is always passed, regardless of discover_all_sessions. That setting is
// a discovery and UI preference: with it off a finished background session is
// simply not listed, and a caller that reads absence as evidence would read a
// finished session as a missing one. The recovery read cannot afford that
// ambiguity, so it asks for everything it is entitled to see.
//
// A failed query is returned as a PublishedListing carrying Err rather than as a
// bare error, so a caller can record "the read failed" separately from "the
// session was not there". The error is also returned, for callers that want to
// stop.
func (p *Provider) PublishedListing(ctx context.Context) (PublishedListing, error) {
	listing := PublishedListing{}
	if p == nil || p.cli == nil {
		listing.Err = ErrCLIUnavailable
		return listing, listing.Err
	}
	entries, err := p.cli.Agents(ctx, AgentsQuery{All: true})
	if err != nil {
		listing.Err = err
		return listing, err
	}
	listing.Entries = entries
	listing.BySessionID = indexBySessionID(entries)
	listing.ReadAt = p.clock.Now().UTC()
	return listing, nil
}

// indexBySessionID keys entries by conversation UUID.
//
// A later entry wins only when it carries a state and the earlier one did not,
// so two entries for one conversation cannot demote a decision-bearing value to
// an empty one.
func indexBySessionID(entries []AgentEntry) map[string]AgentEntry {
	out := make(map[string]AgentEntry, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.SessionID)
		if id == "" {
			continue
		}
		if existing, ok := out[id]; ok {
			if existing.State != "" && entry.State == "" {
				continue
			}
		}
		out[id] = entry
	}
	return out
}
