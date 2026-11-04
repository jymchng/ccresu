package scheduler

import (
	"testing"
	"time"
)

func TestLoadLocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		zone     string
		wantName string
		wantErr  bool
	}{
		{name: "iana region", zone: "Europe/Berlin", wantName: "Europe/Berlin"},
		{name: "iana nested", zone: "America/Argentina/Buenos_Aires", wantName: "America/Argentina/Buenos_Aires"},
		{name: "utc", zone: "UTC", wantName: "UTC"},
		{name: "lowercase utc", zone: "utc", wantName: "UTC"},
		{name: "gmt", zone: "GMT", wantName: "UTC"},
		{name: "zulu", zone: "Z", wantName: "UTC"},
		{name: "abbreviation keeps its name", zone: "EST", wantName: "EST"},
		{name: "abbreviation is trimmed", zone: "  pst  ", wantName: "PST"},
		{name: "empty", zone: "", wantErr: true},
		{name: "blank", zone: "   ", wantErr: true},
		{name: "garbage", zone: "Mars/Olympus_Mons", wantErr: true},
		{name: "numeric", zone: "1234", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			loc, err := LoadLocation(tc.zone)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LoadLocation(%q) = %v, want an error", tc.zone, loc)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadLocation(%q): %v", tc.zone, err)
			}
			if loc.String() != tc.wantName {
				t.Fatalf("LoadLocation(%q).String() = %q, want %q", tc.zone, loc.String(), tc.wantName)
			}
		})
	}
}

// TestAbbreviationsAreFixedOffsets pins the deliberate choice that an
// abbreviation names an offset, not a rule set. "EST" is UTC-5 in July, when
// New York actually observes EDT; mapping it onto America/New_York would apply
// a shift the message never mentioned.
func TestAbbreviationsAreFixedOffsets(t *testing.T) {
	t.Parallel()

	july := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	est, err := LoadLocation("EST")
	if err != nil {
		t.Fatalf("LoadLocation(EST): %v", err)
	}
	_, offset := july.In(est).Zone()
	if offset != -5*3600 {
		t.Fatalf("EST offset in July = %d, want -18000", offset)
	}

	newYork, err := LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation(America/New_York): %v", err)
	}
	if _, nyOffset := july.In(newYork).Zone(); nyOffset == offset {
		t.Fatalf("America/New_York offset (%d) should differ from EST in July; the test is not proving anything", nyOffset)
	}

	ist, err := LoadLocation("IST")
	if err != nil {
		t.Fatalf("LoadLocation(IST): %v", err)
	}
	if _, istOffset := july.In(ist).Zone(); istOffset != 5*3600+1800 {
		t.Fatalf("IST offset = %d, want 19800", istOffset)
	}
}

func TestKnownZone(t *testing.T) {
	t.Parallel()

	for _, zone := range []string{"Europe/Berlin", "UTC", "EST", "Asia/Kolkata"} {
		if !KnownZone(zone) {
			t.Fatalf("KnownZone(%q) = false", zone)
		}
	}
	for _, zone := range []string{"", "Not/AZone", "Mars/Olympus_Mons"} {
		if KnownZone(zone) {
			t.Fatalf("KnownZone(%q) = true", zone)
		}
	}
}
