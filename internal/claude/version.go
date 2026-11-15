package claude

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a parsed `claude --version` value.
type Version struct {
	// Raw is the exact stdout of `claude --version`, trimmed.
	Raw string
	// Major, Minor and Patch are the semantic version components.
	Major, Minor, Patch int
	// PreRelease is the pre-release suffix (without the leading '-'), if any.
	PreRelease string
	// Parsed reports whether the numeric components were recognised.
	Parsed bool
}

var versionPattern = regexp.MustCompile(`(?i)v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.\-]+))?`)

// ParseVersion extracts the semantic version from `claude --version` output.
//
// Verified output format (Claude Code 2.1.223):
//
//	2.1.223 (Claude Code)
//
// A "v" prefix, a leading program name and arbitrary trailing text are
// tolerated because the CLI has changed its banner before; anything without a
// numeric version is rejected so ccresu never guesses.
func ParseVersion(raw string) (Version, error) {
	trimmed := strings.TrimSpace(raw)
	v := Version{Raw: trimmed}
	if trimmed == "" {
		return v, fmt.Errorf("%w: empty version output", ErrUnrecognizedVersion)
	}
	match := versionPattern.FindStringSubmatch(trimmed)
	if match == nil {
		return v, fmt.Errorf("%w: %q", ErrUnrecognizedVersion, trimmed)
	}
	v.Major, _ = strconv.Atoi(match[1])
	v.Minor, _ = strconv.Atoi(match[2])
	v.Patch, _ = strconv.Atoi(match[3])
	v.PreRelease = match[4]
	v.Parsed = true
	return v, nil
}

// String renders the parsed version, falling back to the raw text.
func (v Version) String() string {
	if !v.Parsed {
		return v.Raw
	}
	out := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.PreRelease != "" {
		out += "-" + v.PreRelease
	}
	return out
}

// Compare orders v against other, returning -1, 0 or 1.
func (v Version) Compare(other Version) int {
	if c := compareInt(v.Major, other.Major); c != 0 {
		return c
	}
	if c := compareInt(v.Minor, other.Minor); c != 0 {
		return c
	}
	if c := compareInt(v.Patch, other.Patch); c != 0 {
		return c
	}
	// A pre-release sorts before the corresponding release.
	switch {
	case v.PreRelease == other.PreRelease:
		return 0
	case v.PreRelease == "":
		return 1
	case other.PreRelease == "":
		return -1
	default:
		return strings.Compare(v.PreRelease, other.PreRelease)
	}
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// AtLeast reports whether v is the same as or newer than other.
func (v Version) AtLeast(other Version) bool { return v.Compare(other) >= 0 }

// MinimumVersion is the oldest Claude Code release ccresu is verified against.
//
// The floor moved to the release that makes `--resume <id> --bg` continue the
// session in place, or start a copy and say so on a `note:` line. Recovery has
// no print-mode path any more, so a CLI below this release cannot perform a
// recovery at all, and saying so once is better than probing flags that are not
// there.
var MinimumVersion = Version{Major: 2, Minor: 1, Patch: 257, Parsed: true}

// Supported reports whether the interpreter is new enough for the verified
// interfaces.
func (v Version) Supported() bool { return v.Parsed && v.AtLeast(MinimumVersion) }

// checkSupported turns an unsupported version into an actionable error.
func (v Version) checkSupported() error { return checkVersion(v, MinimumVersion) }

// VersionRequirementError is the named failure for a CLI below the floor.
//
// It exists as a type rather than a formatted string because three call sites
// need to tell it apart from a crash: the recovery classifier must not retry
// it, the command layer must not present it as a mystery, and the operator
// needs the same remedy every time. It unwraps to ErrUnsupportedVersion so
// existing errors.Is checks keep working.
type VersionRequirementError struct {
	// Have is the version the configured CLI reported.
	Have Version
	// Need is the floor this build requires.
	Need Version
}

// Error renders the requirement and the two remedies, in that order.
func (e *VersionRequirementError) Error() string {
	return fmt.Sprintf("%s is older than the required %s: upgrade Claude Code, or stay on ccresu v0.6.0 which still resumes in print mode",
		e.Have, e.Need)
}

// Unwrap makes the error readable as ErrUnsupportedVersion.
func (e *VersionRequirementError) Unwrap() error { return ErrUnsupportedVersion }

// checkVersion validates v against an explicit floor.
func checkVersion(v, min Version) error {
	if v.Parsed && v.AtLeast(min) {
		return nil
	}
	if !v.Parsed {
		return fmt.Errorf("%w: %q", ErrUnrecognizedVersion, v.Raw)
	}
	return &VersionRequirementError{Have: v, Need: min}
}
