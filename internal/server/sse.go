package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// event is one item on the SSE stream.
//
// The payload is deliberately flat: a script that greps the stream by session id
// or by target state does not have to walk a nested document.
type event struct {
	// ID is the transition id the event came from, when it came from one. SSE
	// clients may send it back as Last-Event-ID.
	ID int64 `json:"-"`
	// Type names the event: "ready", "transition", "recovery", "enable" or
	// "disable".
	Type string `json:"type"`
	// At is when the event was produced, in UTC.
	At time.Time `json:"at"`
	// SessionID is the session the event is about.
	SessionID string `json:"session_id,omitempty"`
	// FromState and ToState carry a transition.
	FromState string `json:"from_state,omitempty"`
	ToState   string `json:"to_state,omitempty"`
	// Reason is the recorded explanation of a transition or mutation.
	Reason string `json:"reason,omitempty"`
	// Data carries an optional, already-serialisable detail document.
	Data any `json:"data,omitempty"`
}

// hub fans events out to SSE subscribers.
//
// Publishing never blocks: a subscriber that cannot keep up loses events rather
// than stalling the request or the feed goroutine, which is the same rule the
// Telegram queue follows. Dropping is visible to the client as a gap, never as a
// stuck server.
type hub struct {
	mu       sync.Mutex
	subs     map[chan event]struct{}
	closed   bool
	lastSeen int64
}

// newHub returns an empty hub.
func newHub() *hub {
	return &hub{subs: make(map[chan event]struct{}, 4)}
}

// subscribe registers a subscriber and returns its channel plus the release
// function the handler must defer.
func (h *hub) subscribe() (<-chan event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan event, maxSSEBacklog)
	if h.closed {
		close(ch)
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	release := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
	return ch, release
}

// publish delivers ev to every subscriber without blocking.
func (h *hub) publish(ev event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			// Full: drop for this subscriber only.
		}
	}
}

// close ends every stream. It is idempotent so both the shutdown path and a
// failed listener can call it.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}

// subscribers reports how many streams are open. Tests use it to prove a
// disconnected client was released.
func (h *hub) subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// cursor returns the highest transition id already published.
func (h *hub) cursor() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastSeen
}

// advance moves the published cursor forward, never backwards.
func (h *hub) advance(id int64) {
	if id <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if id > h.lastSeen {
		h.lastSeen = id
	}
}

// startEvents launches the transition feed.
//
// The feed is what makes the stream real-time for work the API did not itself
// trigger: the daemon drives recoveries in its own process, and every state it
// records lands in the transitions table, which this loop follows by id.
func (s *Server) startEvents(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// Start from the current high-water mark so a client that connects later
		// does not receive the whole history as if it had just happened.
		if err := s.seedFeed(ctx); err != nil {
			s.cfg.Log.Warn("seed the event feed", "error", err)
		}
		ticker := time.NewTicker(s.cfg.EventPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.pollTransitions(ctx); err != nil && ctx.Err() == nil {
					s.cfg.Log.Warn("poll the event feed", "error", err)
				}
			}
		}
	}()
}

// seedFeed records the newest transition id without publishing it.
func (s *Server) seedFeed(ctx context.Context) error {
	transitions, err := s.cfg.Store.ListTransitionsSince(ctx, 0, eventBatchLimit)
	if err != nil {
		return err
	}
	for _, transition := range transitions {
		s.hub.advance(transition.ID)
	}
	return nil
}

// transitionEvent renders a stored transition as a stream event.
//
// Both the live feed and the replay after a reconnect build the frame here, so a
// resumed client sees exactly the same payload it would have seen live.
func transitionEvent(transition storage.Transition) event {
	return event{
		ID:        transition.ID,
		Type:      "transition",
		At:        transition.OccurredAt,
		SessionID: transition.SessionID,
		FromState: string(transition.FromState),
		ToState:   string(transition.ToState),
		Reason:    transition.Reason,
	}
}

// pollTransitions publishes every transition newer than the cursor.
func (s *Server) pollTransitions(ctx context.Context) error {
	transitions, err := s.cfg.Store.ListTransitionsSince(ctx, s.hub.cursor(), eventBatchLimit)
	if err != nil {
		return err
	}
	for _, transition := range transitions {
		s.hub.publish(transitionEvent(transition))
		s.hub.advance(transition.ID)
	}
	return nil
}

// lastEventID reads the resume position a client sent back.
//
// Browsers resend the id of the last frame they saw in the Last-Event-ID header,
// which is why every transition frame carries an id: without honouring it a
// reconnect would silently lose whatever happened while the client was away. A
// value that is not a positive integer is ignored rather than rejected, because a
// stale or hand-written header must not break the stream.
func lastEventID(r *http.Request) int64 {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// replayFrom writes the transitions a reconnecting client missed.
//
// It returns the newest id written, which the caller uses to drop the duplicate
// copies still queued in the subscriber channel.
func (s *Server) replayFrom(ctx context.Context, w http.ResponseWriter, since int64) int64 {
	transitions, err := s.cfg.Store.ListTransitionsSince(ctx, since, eventBatchLimit)
	if err != nil {
		if ctx.Err() == nil {
			s.cfg.Log.Warn("replay missed events", "since", since, "error", err)
		}
		return since
	}
	newest := since
	for _, transition := range transitions {
		s.writeEvent(w, transitionEvent(transition))
		if transition.ID > newest {
			newest = transition.ID
		}
	}
	return newest
}

// publishMutation reports a change the API itself made, so a connected client
// hears about it without waiting for the feed poll.
func (s *Server) publishMutation(kind, sessionID, reason string, data any) {
	s.hub.publish(event{
		Type:      kind,
		At:        s.now(),
		SessionID: sessionID,
		Reason:    reason,
		Data:      data,
	})
}

// handleEvents streams server-sent events until the client goes away.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, _ params) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, &apiError{
			Status:  http.StatusInternalServerError,
			Code:    codeInternal,
			Message: "this connection cannot stream events",
		})
		return
	}
	events, release := s.hub.subscribe()
	defer release()

	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")
	// Tell nginx-style proxies not to buffer a stream.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Ask the client to reconnect after three seconds if the stream drops, and
	// open with a ready event carrying the API version so a script can confirm it
	// reached the right service.
	fmt.Fprint(w, "retry: 3000\n\n")
	s.writeEvent(w, event{
		Type: "ready",
		At:   s.now(),
		Data: map[string]any{"api": APIPrefix, "heartbeat_seconds": int(s.cfg.Heartbeat.Seconds())},
	})
	flusher.Flush()

	// A browser that reconnects after a dropped connection sends the id of the
	// last frame it saw. Replaying from the store turns a reconnect into a
	// continuation rather than a silent gap; the frames still queued in the
	// subscriber channel are dropped below so nothing is delivered twice.
	resumed := lastEventID(r)
	if resumed > 0 {
		resumed = s.replayFrom(r.Context(), w, resumed)
		flusher.Flush()
	}

	heartbeat := time.NewTicker(s.cfg.Heartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-events:
			if !open {
				return
			}
			if ev.ID > 0 && ev.ID <= resumed {
				// Already replayed above.
				continue
			}
			s.writeEvent(w, ev)
			flusher.Flush()
		case <-heartbeat.C:
			// A comment keeps intermediaries from timing the stream out.
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// writeEvent renders one SSE frame. Encoding errors are swallowed into a comment
// so a stream never dies on an odd payload.
func (s *Server) writeEvent(w http.ResponseWriter, ev event) {
	payload, err := json.Marshal(ev)
	if err != nil {
		s.cfg.Log.Debug("encode sse event", "error", err)
		fmt.Fprint(w, ": event could not be encoded\n\n")
		return
	}
	if ev.ID > 0 {
		fmt.Fprintf(w, "id: %d\n", ev.ID)
	}
	name := ev.Type
	if name == "" {
		name = "message"
	}
	fmt.Fprintf(w, "event: %s\n", name)
	fmt.Fprintf(w, "data: %s\n\n", payload)
}
