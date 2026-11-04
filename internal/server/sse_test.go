package server

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// recordTransition appends one audit row through the store so the feed has
// something real to follow.
func (h *harness) recordTransition(sessionID string, from, to storage.State, reason string) {
	h.t.Helper()
	err := h.store.RecordTransition(context.Background(), storage.Transition{
		SessionID: sessionID,
		FromState: from,
		ToState:   to,
		Reason:    reason,
	})
	if err != nil {
		h.t.Fatalf("record transition %s->%s: %v", from, to, err)
	}
}

// eventFrame is one parsed SSE frame.
type eventFrame struct {
	id    string
	name  string
	retry string
	data  map[string]any
	raw   string
	lines []string
}

// readFrame reads frames from a stream until predicate matches, or the deadline
// passes. Comments and blank lines are skipped, and every frame is returned so a
// test can assert on ordering.
func readFrames(t *testing.T, r *bufio.Reader, want int) []eventFrame {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var frames []eventFrame
	var current eventFrame
	var dataLine string
	for len(frames) < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %d frame(s): %+v", len(frames), frames)
		}
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream after %d frame(s): %v", len(frames), err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if current.retry != "" {
				frames = append(frames, current)
			} else if dataLine != "" || current.name != "" {
				current.data = map[string]any{}
				if dataLine != "" {
					if err := json.Unmarshal([]byte(dataLine), &current.data); err != nil {
						t.Fatalf("decode event data %q: %v", dataLine, err)
					}
				}
				frames = append(frames, current)
			}
			current, dataLine = eventFrame{}, ""
			continue
		}
		current.lines = append(current.lines, trimmed)
		current.raw += trimmed + "\n"
		switch {
		case strings.HasPrefix(trimmed, ": "):
			current.lines = current.lines[:len(current.lines)-1]
			current.raw = strings.TrimSuffix(current.raw, trimmed+"\n")
			current.raw += "#comment\n"
		case strings.HasPrefix(trimmed, "id: "):
			current.id = strings.TrimPrefix(trimmed, "id: ")
		case strings.HasPrefix(trimmed, "event: "):
			current.name = strings.TrimPrefix(trimmed, "event: ")
		case strings.HasPrefix(trimmed, "data: "):
			dataLine += strings.TrimPrefix(trimmed, "data: ")
		case strings.HasPrefix(trimmed, "retry: "):
			current.retry = strings.TrimPrefix(trimmed, "retry: ")
		default:
			t.Fatalf("unexpected SSE line %q", trimmed)
		}
	}
	return frames
}

// openStream connects to the event endpoint and returns a line reader plus a
// cancel function that closes the connection.
func (h *harness) openStream(mutate func(*http.Request)) (*bufio.Reader, func()) {
	h.t.Helper()
	base := h.serve()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+APIPrefix+"/events", nil)
	if err != nil {
		cancel()
		h.t.Fatalf("build stream request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if mutate != nil {
		mutate(req)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		cancel()
		h.t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		h.t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		cancel()
		h.t.Fatalf("content type = %q, want text/event-stream", ct)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		cancel()
		h.t.Fatalf("cache-control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		cancel()
		h.t.Fatalf("x-accel-buffering = %q, want no", resp.Header.Get("X-Accel-Buffering"))
	}
	stop := func() {
		cancel()
		_ = resp.Body.Close()
	}
	h.t.Cleanup(stop)
	return bufio.NewReader(resp.Body), stop
}

func TestHubSubscribePublishRelease(t *testing.T) {
	hub := newHub()
	events, release := hub.subscribe()
	if got := hub.subscribers(); got != 1 {
		t.Fatalf("subscribers = %d, want 1", got)
	}
	hub.publish(event{Type: "recovery", SessionID: sessionID})
	select {
	case ev := <-events:
		if ev.Type != "recovery" || ev.SessionID != sessionID {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event delivered")
	}
	release()
	if got := hub.subscribers(); got != 0 {
		t.Fatalf("subscribers after release = %d, want 0", got)
	}
	if _, open := <-events; open {
		t.Fatal("channel still open after release")
	}
	// A double release must not panic on a closed channel.
	release()
}

func TestHubDropsForASlowSubscriber(t *testing.T) {
	hub := newHub()
	events, release := hub.subscribe()
	defer release()
	// Publishing more than the backlog must never block the feed.
	for i := 0; i < maxSSEBacklog*3; i++ {
		hub.publish(event{Type: "transition", ID: int64(i + 1)})
	}
	if got := len(events); got != maxSSEBacklog {
		t.Fatalf("buffered events = %d, want %d", got, maxSSEBacklog)
	}
}

func TestHubCloseEndsEveryStream(t *testing.T) {
	hub := newHub()
	first, releaseFirst := hub.subscribe()
	defer releaseFirst()
	second, releaseSecond := hub.subscribe()
	defer releaseSecond()

	hub.close()
	for i, ch := range []<-chan event{first, second} {
		if _, open := <-ch; open {
			t.Fatalf("stream %d still open after close", i)
		}
	}
	if got := hub.subscribers(); got != 0 {
		t.Fatalf("subscribers = %d, want 0", got)
	}
	// Publishing after close is a no-op and must not panic.
	hub.publish(event{Type: "transition"})
	hub.close()

	late, releaseLate := hub.subscribe()
	defer releaseLate()
	if _, open := <-late; open {
		t.Fatal("subscribing after close returned an open channel")
	}
}

func TestCursorNeverMovesBackwards(t *testing.T) {
	hub := newHub()
	if got := hub.cursor(); got != 0 {
		t.Fatalf("initial cursor = %d, want 0", got)
	}
	hub.advance(7)
	if got := hub.cursor(); got != 7 {
		t.Fatalf("cursor = %d, want 7", got)
	}
	hub.advance(3)
	if got := hub.cursor(); got != 7 {
		t.Fatalf("cursor went backwards: %d", got)
	}
	hub.advance(0)
	hub.advance(-4)
	if got := hub.cursor(); got != 7 {
		t.Fatalf("cursor changed on a non-positive id: %d", got)
	}
}

func TestSeedFeedStartsAtTheHighWaterMark(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, nil)
	h.recordTransition(sessionID, storage.StateRunning, storage.StateUsageLimitDetected, "limit")
	h.recordTransition(sessionID, storage.StateUsageLimitDetected, storage.StateWaitingForReset, "queued")

	transitions, err := h.store.ListTransitionsSince(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	if len(transitions) != 2 {
		t.Fatalf("seeded %d transitions, want 2", len(transitions))
	}
	newest := transitions[len(transitions)-1].ID

	if err := h.server.seedFeed(context.Background()); err != nil {
		t.Fatalf("seed feed: %v", err)
	}
	if got := h.server.hub.cursor(); got != newest {
		t.Fatalf("cursor = %d, want %d (history must not be replayed as live)", got, newest)
	}

	events, release := h.server.hub.subscribe()
	defer release()
	if err := h.server.pollTransitions(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := len(events); got != 0 {
		t.Fatalf("poll published %d already-seeded events", got)
	}
}

func TestPollTransitionsPublishesNewOnes(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, nil)
	if err := h.server.seedFeed(context.Background()); err != nil {
		t.Fatalf("seed feed: %v", err)
	}
	events, release := h.server.hub.subscribe()
	defer release()

	h.recordTransition(sessionID, storage.StateRunning, storage.StateUsageLimitDetected, "usage limit hit")
	if err := h.server.pollTransitions(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	select {
	case ev := <-events:
		if ev.Type != "transition" {
			t.Fatalf("type = %q, want transition", ev.Type)
		}
		if ev.SessionID != sessionID {
			t.Fatalf("session = %q, want %s", ev.SessionID, sessionID)
		}
		if ev.FromState != string(storage.StateRunning) || ev.ToState != string(storage.StateUsageLimitDetected) {
			t.Fatalf("transition = %s->%s", ev.FromState, ev.ToState)
		}
		if ev.Reason != "usage limit hit" {
			t.Fatalf("reason = %q", ev.Reason)
		}
		if ev.ID <= 0 {
			t.Fatalf("id = %d, want the transition id", ev.ID)
		}
		if h.server.hub.cursor() != ev.ID {
			t.Fatalf("cursor = %d, want %d", h.server.hub.cursor(), ev.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("no event published")
	}

	// A second poll publishes nothing new.
	if err := h.server.pollTransitions(context.Background()); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if got := len(events); got != 0 {
		t.Fatalf("second poll published %d duplicates", got)
	}
}

func TestLastEventID(t *testing.T) {
	cases := []struct {
		header string
		want   int64
	}{
		{"", 0},
		{"   ", 0},
		{"42", 42},
		{" 42 ", 42},
		{"0", 0},
		{"-3", 0},
		{"abc", 0},
		{"9.5", 0},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, APIPrefix+"/events", nil)
		if tc.header != "" {
			req.Header.Set("Last-Event-ID", tc.header)
		}
		if got := lastEventID(req); got != tc.want {
			t.Fatalf("lastEventID(%q) = %d, want %d", tc.header, got, tc.want)
		}
	}
}

func TestHandleEventsStreamsReadyHeartbeatAndTransitions(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, nil)
	if err := h.server.seedFeed(context.Background()); err != nil {
		t.Fatalf("seed feed: %v", err)
	}
	reader, stop := h.openStream(nil)
	defer stop()

	first := readFrames(t, reader, 2)
	if first[0].retry != "3000" {
		t.Fatalf("stream prelude = %+v, want retry 3000", first[0])
	}
	if first[1].name != "ready" {
		t.Fatalf("first frame = %+v, want ready", first[1])
	}
	if first[1].id != "" {
		t.Fatalf("ready frame carried an id: %q", first[1].id)
	}
	data := first[1].data["data"].(map[string]any)
	if data["api"] != APIPrefix {
		t.Fatalf("ready api = %v, want %s", data["api"], APIPrefix)
	}
	if first[1].data["type"] != "ready" {
		t.Fatalf("ready type = %v", first[1].data["type"])
	}

	// The harness heartbeat is 50ms, so a keep-alive comment must arrive well
	// before the poll interval would.
	if !waitForComment(t, reader) {
		t.Fatal("no keep-alive comment on an idle stream")
	}

	// An API-driven mutation is published immediately, without waiting for the
	// store poll.
	h.server.publishMutation("recovery", sessionID, "manual", map[string]any{"state": "resuming"})
	frames := readFrames(t, reader, 1)
	if frames[0].name != "recovery" {
		t.Fatalf("mutation frame = %+v, want recovery", frames[0])
	}
	if frames[0].data["session_id"] != sessionID {
		t.Fatalf("mutation session = %v", frames[0].data["session_id"])
	}
	if frames[0].data["reason"] != "manual" {
		t.Fatalf("mutation reason = %v", frames[0].data["reason"])
	}
}

// waitForComment blocks until the stream emits a comment line.
func waitForComment(t *testing.T, r *bufio.Reader) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			return false
		}
		if strings.HasPrefix(line, ": ") {
			return true
		}
	}
	return false
}

func TestHandleEventsResumesFromLastEventID(t *testing.T) {
	h := newHarness(t)
	h.seedSession(sessionID, nil)
	// Two transitions happen while no client is connected.
	h.recordTransition(sessionID, storage.StateRunning, storage.StateUsageLimitDetected, "first")
	h.recordTransition(sessionID, storage.StateUsageLimitDetected, storage.StateWaitingForReset, "second")
	transitions, err := h.store.ListTransitionsSince(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	if len(transitions) != 2 {
		t.Fatalf("got %d transitions, want 2", len(transitions))
	}
	first, second := transitions[0], transitions[1]

	// A reconnecting client says it saw the first one.
	reader, stop := h.openStream(func(req *http.Request) {
		req.Header.Set("Last-Event-ID", itoa(first.ID))
	})
	defer stop()

	frames := readFrames(t, reader, 3)
	if frames[1].name != "ready" {
		t.Fatalf("first frame = %+v, want ready", frames[1])
	}
	if frames[2].name != "transition" {
		t.Fatalf("second frame = %+v, want the missed transition", frames[2])
	}
	if frames[2].id != itoa(second.ID) {
		t.Fatalf("replayed id = %q, want %d (only the missed transition)", frames[2].id, second.ID)
	}
	if frames[2].data["reason"] != "second" {
		t.Fatalf("replayed reason = %v, want second", frames[2].data["reason"])
	}
	if frames[2].data["from_state"] != string(storage.StateUsageLimitDetected) {
		t.Fatalf("replayed from_state = %v", frames[2].data["from_state"])
	}
}

func TestHandleEventsGarbageLastEventIDStartsFresh(t *testing.T) {
	h := newHarness(t)
	reader, stop := h.openStream(func(req *http.Request) {
		req.Header.Set("Last-Event-ID", "not-a-number")
	})
	defer stop()
	frames := readFrames(t, reader, 2)
	if frames[0].retry != "3000" || frames[1].name != "ready" {
		t.Fatalf("stream did not start cleanly: %+v", frames)
	}
}

func TestHandleEventsReleasesTheSubscriberOnDisconnect(t *testing.T) {
	h := newHarness(t)
	reader, stop := h.openStream(nil)
	readFrames(t, reader, 1)
	if got := h.server.hub.subscribers(); got != 1 {
		t.Fatalf("subscribers = %d, want 1", got)
	}
	stop()
	waitFor(t, "the stream to be released", func() bool {
		return h.server.hub.subscribers() == 0
	})
}

func TestHandleEventsNeedsAFlushingConnection(t *testing.T) {
	h := newHarness(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, APIPrefix+"/events", nil)
	h.server.handleEvents(nonFlusher{rec}, req, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != codeInternal {
		t.Fatalf("error = %+v, want code %s", envelope.Error, codeInternal)
	}
}

// nonFlusher hides Flush so the handler has to detect the missing capability.
type nonFlusher struct{ http.ResponseWriter }

func TestHandleEventsStopsWhenTheHubCloses(t *testing.T) {
	h := newHarness(t)
	reader, stop := h.openStream(nil)
	defer stop()
	readFrames(t, reader, 1)

	h.server.hub.close()
	waitFor(t, "the subscriber to be released", func() bool {
		return h.server.hub.subscribers() == 0
	})
}

func TestWriteEvent(t *testing.T) {
	h := newHarness(t)
	rec := httptest.NewRecorder()
	h.server.writeEvent(rec, event{ID: 12, Type: "transition", SessionID: sessionID})
	body := rec.Body.String()
	if !strings.HasPrefix(body, "id: 12\nevent: transition\ndata: ") {
		t.Fatalf("frame = %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("frame does not end with a blank line: %q", body)
	}

	// Without an id no id line is written, and an unnamed event falls back to
	// the SSE default name.
	rec = httptest.NewRecorder()
	h.server.writeEvent(rec, event{Type: ""})
	body = rec.Body.String()
	if strings.Contains(body, "id: ") {
		t.Fatalf("id line written for an event without an id: %q", body)
	}
	if !strings.Contains(body, "event: message\n") {
		t.Fatalf("frame = %q, want the default event name", body)
	}

	// An unencodable payload becomes a comment instead of killing the stream.
	rec = httptest.NewRecorder()
	h.server.writeEvent(rec, event{Type: "transition", Data: math.Inf(1)})
	body = rec.Body.String()
	if !strings.Contains(body, ": event could not be encoded") {
		t.Fatalf("frame = %q, want an encoding comment", body)
	}
	if strings.Contains(body, "event: transition") {
		t.Fatalf("partially written frame: %q", body)
	}
}

func TestPublishMutationCarriesTheClock(t *testing.T) {
	h := newHarness(t)
	events, release := h.server.hub.subscribe()
	defer release()

	h.server.publishMutation("enable", sessionID, "operator", nil)
	select {
	case ev := <-events:
		if ev.Type != "enable" || ev.SessionID != sessionID || ev.Reason != "operator" {
			t.Fatalf("event = %+v", ev)
		}
		if !ev.At.Equal(baseTime) {
			t.Fatalf("at = %v, want the fake clock's %v", ev.At, baseTime)
		}
	case <-time.After(time.Second):
		t.Fatal("mutation not published")
	}
}

// itoa renders an id the way writeEvent does.
func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
