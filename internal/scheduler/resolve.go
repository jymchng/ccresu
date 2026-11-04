package scheduler

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/detector"
)

// Kind records how a reset instant was obtained, so a surprising wake-up can be
// explained later without re-reading the message.
type Kind string

const (
	// KindUnknown means the phrase carried no usable reset information.
	KindUnknown Kind = "unknown"
	// KindEpoch means the message carried a Unix timestamp.
	KindEpoch Kind = "epoch"
	// KindRelative means the phrase was a duration from the detection time.
	KindRelative Kind = "relative"
	// KindTimeOfDay means the phrase was a clock time, e.g. "12pm".
	KindTimeOfDay Kind = "time_of_day"
	// KindWeekday means the phrase named a weekday, e.g. "Monday 9am".
	KindWeekday Kind = "weekday"
	// KindDate means the phrase named a calendar date, e.g. "June 15" or
	// "2026-06-15 09:00".
	KindDate Kind = "date"
)

// Resolution is the outcome of resolving one reset phrase.
//
// At is always UTC: everything ccresu stores or compares is absolute, and the
// original timezone is kept alongside it as LocationName so the user still sees
// the time their message was written in.
type Resolution struct {
	// Known reports whether a usable instant was found. When false, callers must
	// poll rather than wait for a fixed instant.
	Known bool
	// Kind is how the instant was obtained.
	Kind Kind
	// At is the resolved instant in UTC. Zero when Known is false.
	At time.Time
	// LocationName is the zone the phrase was resolved in. Empty for durations
	// and epochs, which carry no zone.
	LocationName string
	// Local is the chosen civil time rendered in LocationName, e.g.
	// "2026-06-15 12:00". Empty for durations and epochs.
	Local string
	// DSTCase reports a daylight-saving collision, if any.
	DSTCase DSTCase
	// Notes explains every assumption made, so `ccresu inspect` can show why a
	// session is waiting until a particular minute.
	Notes []string
	// Phrase is the original reset phrase, preserved verbatim.
	Phrase string
}

// Options controls how reset phrases are resolved.
type Options struct {
	// Location is the zone used when the phrase does not name one. It defaults
	// to UTC so behaviour never depends on the host's TZ setting.
	Location *time.Location
	// LocationName is how that default zone is reported.
	LocationName string
	// PreferEarlierOnAmbiguous selects the first of the two instants a repeated
	// wall clock maps to. The default (false) selects the later one, because
	// waking after the reset is safe while waking before it wastes an attempt.
	PreferEarlierOnAmbiguous bool
}

// DefaultOptions returns options that resolve in UTC.
func DefaultOptions() Options {
	return Options{Location: time.UTC, LocationName: "UTC"}
}

// withDefaults fills in unset fields.
func (o Options) withDefaults() Options {
	if o.Location == nil {
		o.Location = time.UTC
	}
	if o.LocationName == "" {
		o.LocationName = o.Location.String()
	}
	return o
}

// Resolve turns a detector reset phrase into an absolute instant.
//
// now is the instant the message was observed; a phrase such as "12pm" is
// interpreted as the next 12pm at or after it. Every assumption the resolution
// makes is recorded in Notes rather than applied silently.
func Resolve(phrase detector.ResetPhrase, now time.Time, opts Options) Resolution {
	opts = opts.withDefaults()
	now = now.UTC()

	res := Resolution{Phrase: phrase.Raw, Kind: KindUnknown}
	if !phrase.Present {
		res.Notes = append(res.Notes, "the message carried no reset phrase")
		return res
	}

	// A Unix timestamp is already absolute: no calendar arithmetic, no zone.
	if phrase.EpochUnix > 0 {
		at := time.Unix(phrase.EpochUnix, 0).UTC()
		res.Known = true
		res.Kind = KindEpoch
		res.At = at
		res.LocationName = "UTC"
		res.Local = at.Format("2006-01-02 15:04")
		return res
	}

	expr := normalizeExpression(phrase.Expression)
	if expr == "" {
		res.Notes = append(res.Notes, "the reset phrase was empty")
		return res
	}

	// A duration is relative to detection and needs no timezone at all.
	if d, ok := parseDuration(expr); ok {
		res.Known = true
		res.Kind = KindRelative
		res.At = now.Add(d)
		res.Notes = append(res.Notes, "the phrase was a duration, so it was measured from the detection time")
		return res
	}

	loc, locName, note := phraseLocation(phrase, opts)
	if loc == nil {
		res.Notes = append(res.Notes, note)
		return res
	}
	res.LocationName = locName
	if note != "" {
		res.Notes = append(res.Notes, note)
	}

	today := wallOf(now, loc)
	today.hour, today.minute = 0, 0

	plan, ok := planCivil(expr, today)
	if !ok {
		res.Notes = append(res.Notes, fmt.Sprintf("the reset phrase %q was not understood", phrase.Expression))
		return res
	}
	if plan.note != "" {
		res.Notes = append(res.Notes, plan.note)
	}

	at, dstCase, rolled, ok := rollForward(plan, loc, now, opts)
	if !ok {
		res.Notes = append(res.Notes, fmt.Sprintf("the reset phrase %q did not resolve to a date", phrase.Expression))
		return res
	}
	if rolled {
		res.Notes = append(res.Notes, "the named time had already passed, so the next occurrence was used")
	}

	res.Known = true
	res.Kind = plan.kind
	res.At = at
	res.DSTCase = dstCase
	res.Local = wallOf(at, loc).String()
	if at.Before(now) {
		res.Notes = append(res.Notes, "the resolved instant is already in the past, so the schedule is due immediately")
	}
	switch dstCase {
	case DSTAmbiguous:
		res.Notes = append(res.Notes, "that wall clock occurs twice when the clocks go back; the later instant was chosen so recovery cannot start too early")
	case DSTGap:
		res.Notes = append(res.Notes, "that wall clock does not exist when the clocks go forward; the instant after the gap was chosen")
	}
	return res
}

// civilPlan is a candidate civil time before it has been rolled forward.
type civilPlan struct {
	wall wall
	kind Kind
	// note explains an assumed value, e.g. a defaulted time of day.
	note string
	// rollable reports that the plan may move forward to its next occurrence
	// because the named time has already passed.
	rollable bool
	// step is how far to roll: one day for a clock time, a week for a weekday,
	// a year for a date without a year.
	step rollStep
}

// rollStep is the forward-search granularity for a passed civil time.
type rollStep int

const (
	rollNone rollStep = iota
	rollDay
	rollWeek
	rollYear
)

// rollForward moves a plan to its next occurrence at or after now.
//
// Rolling is done on the civil date and the wall clock resolved again each time,
// never by adding 24 hours to an instant, because on a DST day those differ.
func rollForward(plan civilPlan, loc *time.Location, now time.Time, opts Options) (time.Time, DSTCase, bool, bool) {
	candidate := plan.wall
	at, dstCase := resolveWall(candidate, loc, opts.PreferEarlierOnAmbiguous)
	if !plan.rollable {
		return at, dstCase, false, true
	}
	rolled := false
	// At most a handful of iterations: a passed clock time rolls a day, a passed
	// weekday a week, a passed month/day a year.
	for i := 0; i < 8 && !at.After(now); i++ {
		switch plan.step {
		case rollDay:
			candidate = candidate.addDays(1)
		case rollWeek:
			candidate = candidate.addDays(7)
		case rollYear:
			candidate = candidate.addYears(1)
		default:
			return at, dstCase, rolled, true
		}
		rolled = true
		at, dstCase = resolveWall(candidate, loc, opts.PreferEarlierOnAmbiguous)
	}
	return at, dstCase, rolled, true
}

// phraseLocation picks the zone to resolve in and explains the choice.
func phraseLocation(phrase detector.ResetPhrase, opts Options) (*time.Location, string, string) {
	if phrase.Timezone == "" {
		return opts.Location, opts.LocationName,
			"the message named no timezone, so the configured default (" + opts.LocationName + ") was used"
	}
	loc, err := LoadLocation(phrase.Timezone)
	if err != nil {
		// Guessing a different zone could shift the wake-up by hours, so this is
		// treated as "unknown" and polled instead.
		return nil, "", fmt.Sprintf("the timezone %q is not recognised", phrase.Timezone)
	}
	return loc, phrase.Timezone, ""
}

// planCivil parses a time expression into a civil time relative to today.
func planCivil(expr string, today wall) (civilPlan, bool) {
	switch strings.ToLower(expr) {
	case "noon", "midday":
		return civilPlan{wall: withTime(today, 12, 0), kind: KindTimeOfDay, rollable: true, step: rollDay}, true
	case "midnight":
		return civilPlan{wall: withTime(today, 0, 0), kind: KindTimeOfDay, rollable: true, step: rollDay}, true
	}

	if m := dayWordPattern.FindStringSubmatch(expr); m != nil {
		word := strings.ToLower(m[1])
		target := today
		switch word {
		case "tomorrow":
			target = today.addDays(1)
		case "tonight":
			target = today
		}
		timePart := strings.TrimSpace(m[2])
		if timePart == "" {
			switch word {
			case "tonight":
				return civilPlan{
					wall: withTime(target, 23, 59), kind: KindTimeOfDay, rollable: true, step: rollDay,
					note: "no time of day was given, so the end of the evening (23:59) was assumed",
				}, true
			default:
				return civilPlan{
					wall: withTime(target, 0, 0), kind: KindTimeOfDay, rollable: word == "today", step: rollDay,
					note: "no time of day was given, so midnight was assumed",
				}, true
			}
		}
		hour, minute, ok := parseTimeOfDay(timePart)
		if !ok {
			return civilPlan{}, false
		}
		return civilPlan{wall: withTime(target, hour, minute), kind: KindTimeOfDay, rollable: word == "today", step: rollDay}, true
	}

	if m := weekdayPattern.FindStringSubmatch(expr); m != nil {
		day, ok := weekdayIndex(m[1])
		if !ok {
			return civilPlan{}, false
		}
		hour, minute := 0, 0
		note := "no time of day was given, so midnight was assumed"
		if timePart := strings.TrimSpace(m[2]); timePart != "" {
			hour, minute, ok = parseTimeOfDay(timePart)
			if !ok {
				return civilPlan{}, false
			}
			note = ""
		}
		ahead := (int(day) - int(today.weekday()) + 7) % 7
		target := today.addDays(ahead)
		return civilPlan{
			wall: withTime(target, hour, minute), kind: KindWeekday,
			rollable: true, step: rollWeek, note: note,
		}, true
	}

	if m := monthDayPattern.FindStringSubmatch(expr); m != nil {
		month, ok := monthIndex(m[1])
		if !ok {
			return civilPlan{}, false
		}
		day, err := strconv.Atoi(m[2])
		if err != nil || day < 1 || day > 31 {
			return civilPlan{}, false
		}
		year := today.year
		note := ""
		explicitYear := m[3] != ""
		if explicitYear {
			year = mustAtoi(m[3])
		}
		hour, minute := 0, 0
		timePart := strings.TrimSpace(m[4])
		if timePart != "" {
			if hour, minute, ok = parseTimeOfDay(timePart); !ok {
				return civilPlan{}, false
			}
		} else {
			note = "no time of day was given, so midnight was assumed"
		}
		candidate := wall{year: year, month: month, day: day, hour: hour, minute: minute}
		rollable := false
		if !explicitYear {
			switch {
			case beforeDate(candidate, today):
				// A date already gone means next year's. Rolling by a year is
				// only right when the day really is behind us.
				candidate = candidate.addYears(1)
				note = joinNotes(note, "the date had already passed this year, so next year was assumed")
			case !sameDate(candidate, today) || timePart != "":
				// A date later this year, or today with an explicit time, is
				// taken as written; if its instant has passed the schedule is
				// simply due, which is safer than waiting a year.
				rollable = false
			}
		}
		return civilPlan{
			wall: candidate, kind: KindDate,
			rollable: rollable, step: rollYear, note: note,
		}, true
	}

	if m := isoDatePattern.FindStringSubmatch(expr); m != nil {
		year, _ := strconv.Atoi(m[1])
		month := time.Month(mustAtoi(m[2]))
		day := mustAtoi(m[3])
		hour, minute := 0, 0
		note := "no time of day was given, so midnight was assumed"
		if m[4] != "" {
			hour = mustAtoi(m[4])
			minute = mustAtoi(m[5])
			note = ""
		}
		if day < 1 || day > 31 || hour > 23 || minute > 59 {
			return civilPlan{}, false
		}
		return civilPlan{
			wall: wall{year: year, month: month, day: day, hour: hour, minute: minute},
			kind: KindDate, note: note,
		}, true
	}

	if m := timeOfDayDayPattern.FindStringSubmatch(expr); m != nil {
		hour, minute, ok := parseTimeParts(m[1], m[2], m[3])
		if !ok {
			return civilPlan{}, false
		}
		target := today
		if strings.ToLower(m[4]) == "tomorrow" {
			target = today.addDays(1)
		}
		return civilPlan{
			wall: withTime(target, hour, minute), kind: KindTimeOfDay, rollable: true, step: rollDay,
		}, true
	}

	if hour, minute, ok := parseTimeOfDay(expr); ok {
		return civilPlan{
			wall: withTime(today, hour, minute), kind: KindTimeOfDay, rollable: true, step: rollDay,
		}, true
	}
	return civilPlan{}, false
}

// withTime returns w with the given time of day.
func withTime(w wall, hour, minute int) wall {
	w.hour, w.minute = hour, minute
	return w
}

// sameDate reports whether the two civil times fall on the same calendar day.
func sameDate(a, b wall) bool {
	return a.year == b.year && a.month == b.month && a.day == b.day
}

// beforeDate reports whether a falls on an earlier calendar day than b. Times of
// day are ignored, so a date "today" never counts as behind us.
func beforeDate(a, b wall) bool {
	if a.year != b.year {
		return a.year < b.year
	}
	if a.month != b.month {
		return a.month < b.month
	}
	return a.day < b.day
}

// joinNotes appends a note to an existing one, keeping both.
func joinNotes(existing, extra string) string {
	if existing == "" {
		return extra
	}
	return existing + "; " + extra
}

// String renders a wall clock like a human would write it.
func (w wall) String() string {
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d", w.year, int(w.month), w.day, w.hour, w.minute)
}

// weekday returns the weekday of the civil date.
func (w wall) weekday() time.Weekday {
	return time.Date(w.year, w.month, w.day, 12, 0, 0, 0, time.UTC).Weekday()
}

// normalizeExpression lowercases and collapses an expression for parsing.
func normalizeExpression(expr string) string {
	cleaned := strings.ToLower(strings.TrimSpace(expr))
	cleaned = strings.TrimRight(cleaned, ".")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return cleaned
}

// mustAtoi converts a numeric capture, defaulting to zero on garbage; callers
// range-check the result.
func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

var (
	// timeOfDayPart is the shared clock-time shape: "12pm", "12:30 PM", "14:00".
	// The trailing dots of "a.m."/"p.m." are optional because normalisation
	// trims a phrase-final dot.
	timeOfDayPart = `(\d{1,2})(?::(\d{2}))?\s*(a\.?m\.?|p\.?m\.?)?`

	timeOfDayPattern    = regexp.MustCompile(`(?i)^` + timeOfDayPart + `$`)
	timeOfDayDayPattern = regexp.MustCompile(`(?i)^` + timeOfDayPart + `\s+(today|tomorrow|tonight)$`)

	dayWordPattern = regexp.MustCompile(`(?i)^(today|tomorrow|tonight)(?:\s*,?\s*(?:at\s+)?(.+))?$`)

	weekdayPattern = regexp.MustCompile(`(?i)^(sun(?:day)?|mon(?:day)?|tue(?:s)?(?:day)?|wed(?:nesday)?|thu(?:r)?(?:s)?(?:day)?|fri(?:day)?|sat(?:urday)?)(?:\s*,?\s*(?:at\s+)?(.+))?$`)

	monthDayPattern = regexp.MustCompile(`(?i)^(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:t)?(?:ember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\s+(\d{1,2})(?:st|nd|rd|th)?(?:\s*,?\s*(\d{4}))?(?:\s+(?:at\s+)?(.+))?$`)

	isoDatePattern = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})(?:[ t](\d{1,2}):(\d{2})(?::\d{2})?)?$`)

	durationToken = regexp.MustCompile(`(?i)(\d+)\s*(hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)`)
)

// parseTimeOfDay parses a clock time into 24-hour hour and minute.
func parseTimeOfDay(s string) (int, int, bool) {
	m := timeOfDayPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, false
	}
	return parseTimeParts(m[1], m[2], m[3])
}

// parseTimeParts converts the captured parts of a clock time into 24-hour hour
// and minute.
func parseTimeParts(hourStr, minuteStr, suffixStr string) (int, int, bool) {
	hour := mustAtoi(hourStr)
	minute := 0
	if minuteStr != "" {
		minute = mustAtoi(minuteStr)
	}
	if minute < 0 || minute > 59 {
		return 0, 0, false
	}
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(suffixStr), ".", "")) {
	case "am":
		if hour < 1 || hour > 12 {
			return 0, 0, false
		}
		hour %= 12
	case "pm":
		if hour < 1 || hour > 12 {
			return 0, 0, false
		}
		hour = hour%12 + 12
	default:
		// No meridiem: the clock is read as 24-hour. "12" is therefore noon,
		// not midnight, which matches how a bare reset time is written.
		if hour > 23 {
			return 0, 0, false
		}
	}
	return hour, minute, true
}

// parseDuration parses expressions such as "4 hours", "3h 20m" and
// "90 minutes" into a duration. The whole string must be consumed, so
// "4 hours ago" is rejected rather than silently read as "4 hours".
func parseDuration(expr string) (time.Duration, bool) {
	cleaned := strings.ToLower(expr)
	// "in 4 hours" and "4 hours" mean the same thing; the detector normally
	// strips the preposition, but a custom pattern may not.
	cleaned = strings.TrimPrefix(cleaned, "in ")
	cleaned = strings.TrimPrefix(cleaned, "after ")
	cleaned = strings.NewReplacer(",", " ", " and ", " ", "+", " ").Replace(cleaned)
	cleaned = strings.Join(strings.Fields(cleaned), " ")

	total := time.Duration(0)
	consumed := 0
	for _, m := range durationToken.FindAllStringSubmatchIndex(cleaned, -1) {
		// Only whitespace may separate components, so "3h 20m" parses while
		// "4 hours ago" is rejected instead of silently read as "4 hours".
		if strings.TrimSpace(cleaned[consumed:m[0]]) != "" {
			return 0, false
		}
		amount := mustAtoi(cleaned[m[2]:m[3]])
		unit := cleaned[m[4]:m[5]]
		switch unit[0] {
		case 'h':
			total += time.Duration(amount) * time.Hour
		case 'm':
			total += time.Duration(amount) * time.Minute
		case 's':
			total += time.Duration(amount) * time.Second
		}
		consumed = m[1]
	}
	if total <= 0 || strings.TrimSpace(cleaned[consumed:]) != "" {
		return 0, false
	}
	return total, true
}

// weekdayIndex maps a weekday name to its time.Weekday.
func weekdayIndex(name string) (time.Weekday, bool) {
	switch strings.ToLower(name) {
	case "sun", "sunday":
		return time.Sunday, true
	case "mon", "monday":
		return time.Monday, true
	case "tue", "tues", "tuesday":
		return time.Tuesday, true
	case "wed", "wednesday":
		return time.Wednesday, true
	case "thu", "thur", "thurs", "thursday":
		return time.Thursday, true
	case "fri", "friday":
		return time.Friday, true
	case "sat", "saturday":
		return time.Saturday, true
	}
	return 0, false
}

// monthIndex maps a month name to its time.Month.
func monthIndex(name string) (time.Month, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	if len(lower) < 3 {
		return 0, false
	}
	prefix := lower[:3]
	for month := time.January; month <= time.December; month++ {
		if strings.ToLower(month.String())[:3] == prefix {
			return month, true
		}
	}
	return 0, false
}
