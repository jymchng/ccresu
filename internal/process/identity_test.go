package process

import (
	"strings"
	"testing"
)

func TestParseStat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want Stat
	}{
		{
			name: "typical",
			data: "4242 (claude) S 100 4242 4242 0 -1 4194560 1 0 0 0 10 5 0 0 20 0 4 0 987654 0 0",
			want: Stat{PID: 4242, Name: "claude", State: "S", PPID: 100, StartTime: 987654},
		},
		{
			name: "name with spaces and parentheses",
			data: "77 (claude code (node)) R 1 77 77 0 -1 0 0 0 0 0 0 0 0 0 20 0 4 0 12345678 0 0",
			want: Stat{PID: 77, Name: "claude code (node)", State: "R", PPID: 1, StartTime: 12345678},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseStat(tc.data)
			if err != nil {
				t.Fatalf("ParseStat: %v", err)
			}
			if got != tc.want {
				t.Errorf("ParseStat = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseStatErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"empty":          "",
		"no name":        "4242 100 S",
		"bad pid":        "abc (claude) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20",
		"too few fields": "4242 (claude) S 100 4242",
		"bad ppid":       "4242 (claude) S notanumber 4242 4242 0 -1 0 0 0 0 0 0 0 0 0 20 0 4 0 987654",
		"bad starttime":  "4242 (claude) S 100 4242 4242 0 -1 0 0 0 0 0 0 0 0 0 20 0 4 0 notanumber",
	}

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseStat(data); err == nil {
				t.Fatalf("ParseStat(%q) should have failed", data)
			}
		})
	}
}

func TestParseCmdline(t *testing.T) {
	t.Parallel()

	if got := ParseCmdline(""); got != nil {
		t.Errorf("ParseCmdline(\"\") = %v, want nil", got)
	}
	if got := ParseCmdline("\x00\x00"); got != nil {
		t.Errorf("ParseCmdline(empty fields) = %v, want nil", got)
	}

	got := ParseCmdline("claude\x00-p\x00--resume\x00abc\x00")
	want := []string{"claude", "-p", "--resume", "abc"}
	if len(got) != len(want) {
		t.Fatalf("ParseCmdline = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIdentitySameProcess(t *testing.T) {
	t.Parallel()

	base := Identity{PID: 100, StartTime: 5000}

	if !base.SameProcess(Identity{PID: 100, StartTime: 5000}) {
		t.Error("identical identities should match")
	}
	if base.SameProcess(Identity{PID: 100, StartTime: 9999}) {
		t.Error("a reused PID with a different start time must not match")
	}
	if base.SameProcess(Identity{PID: 101, StartTime: 5000}) {
		t.Error("different PIDs must not match")
	}
	if (Identity{}).SameProcess(Identity{}) {
		t.Error("zero identities must not match")
	}
	if !base.SameProcess(Identity{PID: 100}) {
		t.Error("a missing start time should fall back to the PID comparison")
	}
	if !(Identity{PID: 100}).SameProcess(base) {
		t.Error("a missing start time on the left should fall back to the PID comparison")
	}
}

func TestIdentityValidAndString(t *testing.T) {
	t.Parallel()

	if (Identity{}).Valid() {
		t.Error("zero identity should not be valid")
	}
	if !(Identity{PID: 1}).Valid() {
		t.Error("identity with a PID should be valid")
	}

	withName := Identity{PID: 42, PPID: 1, StartTime: 7, Name: "claude"}
	got := withName.String()
	for _, fragment := range []string{"pid=42", "ppid=1", "start=7", "name=claude"} {
		if !strings.Contains(got, fragment) {
			t.Errorf("String() = %q, missing %q", got, fragment)
		}
	}

	if got := (Identity{PID: 1}).String(); !strings.Contains(got, "name=unknown") {
		t.Errorf("String() should fall back to an unknown name, got %q", got)
	}
}
