package scheduler

import "time"

// DSTCase describes how a requested civil time related to a daylight-saving
// change. A reset phrase names a wall-clock time ("2:30am"), and twice a year
// that wall clock is either repeated or does not exist at all.
type DSTCase string

const (
	// DSTNone means the civil time maps to exactly one instant.
	DSTNone DSTCase = ""
	// DSTAmbiguous means the wall clock occurs twice (clocks went back). The
	// later instant is chosen by default: waiting slightly too long costs a few
	// minutes of idle time, while waking early loses an attempt and has to back
	// off again.
	DSTAmbiguous DSTCase = "ambiguous"
	// DSTGap means the wall clock never occurs (clocks jumped forward). The
	// instant is moved past the gap by the length of the shift, for the same
	// reason: early is worse than late.
	DSTGap DSTCase = "gap"
)

// wall is a civil date and time with no timezone attached.
type wall struct {
	year   int
	month  time.Month
	day    int
	hour   int
	minute int
}

// addDays returns the civil date n days later, keeping the time of day.
//
// Calendar arithmetic is done on the civil date rather than by adding 24 hours
// to an instant, because on a DST day those differ by an hour.
func (w wall) addDays(n int) wall {
	t := time.Date(w.year, w.month, w.day, 12, 0, 0, 0, time.UTC).AddDate(0, 0, n)
	w.year, w.month, w.day = t.Date()
	return w
}

// addYears returns the same civil date n years later.
//
// 29 February in a non-leap year normalises to 1 March, which is what time.Date
// does and what a reader would expect of "the same day next year".
func (w wall) addYears(n int) wall {
	t := time.Date(w.year+n, w.month, w.day, 12, 0, 0, 0, time.UTC)
	w.year, w.month, w.day = t.Date()
	return w
}

// wallOf reports the civil time that t shows in loc.
func wallOf(t time.Time, loc *time.Location) wall {
	local := t.In(loc)
	return wall{
		year:   local.Year(),
		month:  local.Month(),
		day:    local.Day(),
		hour:   local.Hour(),
		minute: local.Minute(),
	}
}

// resolveWall maps a civil time in loc to the instant that carries it.
//
// The returned instant is always in UTC so that everything stored or compared
// internally is absolute; call .In(loc) to show it to a human.
func resolveWall(w wall, loc *time.Location, preferEarlier bool) (time.Time, DSTCase) {
	t := time.Date(w.year, w.month, w.day, w.hour, w.minute, 0, 0, loc)
	if wallOf(t, loc) != w {
		// The civil time does not exist: the clocks jumped forward over it.
		// time.Date has already moved past the gap, which is the conservative
		// choice — the limit has definitely reset by then.
		return t.UTC(), DSTGap
	}

	// A repeated wall clock shows up as an earlier instant in the same zone
	// carrying the identical civil time. Offsets have historically shifted by
	// 30 minutes and by an hour, so both are probed.
	earliest := t
	ambiguous := false
	for _, back := range []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour} {
		if candidate := t.Add(-back); wallOf(candidate, loc) == w {
			earliest = candidate
			ambiguous = true
		}
	}
	if !ambiguous {
		return t.UTC(), DSTNone
	}
	if preferEarlier {
		return earliest.UTC(), DSTAmbiguous
	}
	return t.UTC(), DSTAmbiguous
}
