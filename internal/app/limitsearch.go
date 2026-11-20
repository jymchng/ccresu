package app

import (
	"context"
	"errors"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// transcriptLimitScan is the LimitScan `ccresu search-limit` gives the recovery
// engine: read the newest transcript lines and ask the detector whether one of
// them is a usage-limit banner.
//
// The session is closed over rather than looked up again because the caller has
// already read it, and a second read inside the search could describe a session
// the state walk is no longer holding. The tail is the caller's, so the command's
// --tail means what it says.
//
// It is deliberately the same read the daemon's detect stage performs
// (Daemon.detectIn): one transcript tail, one call to the detector, the newest
// lines first. A limit found by an operator and a limit found by a pass must be
// recognised from the same evidence, or the two would disagree about the same
// banner.
func transcriptLimitScan(rt *Runtime, session storage.Session, tail int) recovery.LimitScan {
	return func(ctx context.Context, sessionID string) (string, bool, error) {
		if rt == nil || rt.Detector == nil || rt.Transcripts == nil {
			return "", false, errors.New("no detector or transcript reader is configured")
		}
		if sessionID != session.ID {
			return "", false, errors.New("the scan was asked about a different session")
		}
		if session.TranscriptPath == "" {
			return "", false, errors.New("the session has no transcript to read")
		}
		entries, err := rt.Transcripts.TailEntries(session.TranscriptPath, tail)
		if err != nil {
			return "", false, err
		}
		text := transcriptText(entries)
		if text == "" {
			return "", false, nil
		}
		event := rt.Detector.Analyze(text)
		if !event.IsUsageLimit() {
			return "", false, nil
		}
		return text, true, nil
	}
}

// transcriptText joins the newest transcript lines so a banner split across lines
// is still recognised.
//
// It lives here rather than on the daemon because both the pass and an operator's
// search read the same window, and two copies of "how much of the tail counts as
// the banner" is how they would stop agreeing.
func transcriptText(entries []claude.Entry) string {
	var text string
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Text == "" {
			continue
		}
		if text == "" {
			text = entries[i].Text
			continue
		}
		text += "\n" + entries[i].Text
		if len(text) > 4000 {
			break
		}
	}
	return text
}
