package scheduler

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidZone reports a timezone name that is neither an IANA zone nor a
// recognised abbreviation.
var ErrInvalidZone = errors.New("unknown timezone")

// fixedZones maps the zone abbreviations that appear in usage-limit messages to
// their fixed offset east of UTC, in seconds.
//
// An abbreviation names an offset, not a rule set: "EST" means UTC-5 even in
// July, when New York actually observes EDT. Mapping "EST" onto
// America/New_York would silently apply a summer-time shift the message never
// mentioned, so each abbreviation becomes a fixed zone that also keeps its
// original spelling for display.
var fixedZones = map[string]int{
	"UTC": 0, "GMT": 0, "UT": 0, "Z": 0, "WET": 0,
	"EST": -5 * 3600, "EDT": -4 * 3600,
	"CST": -6 * 3600, "CDT": -5 * 3600,
	"MST": -7 * 3600, "MDT": -6 * 3600,
	"PST": -8 * 3600, "PDT": -7 * 3600,
	"AKST": -9 * 3600, "AKDT": -8 * 3600,
	"HST":  -10 * 3600,
	"WEST": 3600, "BST": 3600, "CET": 3600, "CEST": 2 * 3600,
	"EET": 2 * 3600, "EEST": 3 * 3600,
	"MSK": 3 * 3600,
	"IST": 5*3600 + 1800, // India, +05:30
	"PKT": 5 * 3600,
	"GST": 4 * 3600, "AST": 3 * 3600,
	"SGT": 8 * 3600, "HKT": 8 * 3600, "AWST": 8 * 3600,
	"JST": 9 * 3600, "KST": 9 * 3600,
	"AEST": 10 * 3600, "AEDT": 11 * 3600,
	"ACST": 9*3600 + 1800, "ACDT": 10*3600 + 1800,
	"NZST": 12 * 3600, "NZDT": 13 * 3600,
}

// LoadLocation resolves an IANA zone name or a known abbreviation.
//
// The IANA database is embedded in the binary (see the time/tzdata import in the
// config package), so resolution does not depend on the host having zoneinfo
// installed — which matters on Windows, where it usually does not.
func LoadLocation(name string) (*time.Location, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty name", ErrInvalidZone)
	}
	upper := strings.ToUpper(trimmed)
	if upper == "UTC" || upper == "GMT" || upper == "Z" || upper == "UT" {
		return time.UTC, nil
	}
	if offset, ok := fixedZones[upper]; ok {
		return time.FixedZone(upper, offset), nil
	}
	loc, err := time.LoadLocation(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidZone, name)
	}
	return loc, nil
}

// KnownZone reports whether name resolves. It is used by configuration
// validation so a typo is caught at startup rather than at reset time.
func KnownZone(name string) bool {
	_, err := LoadLocation(name)
	return err == nil
}
