package config

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TestThereIsNoConfigurationSwitchForWorktrees pins PRD 6 R11 at the place a
// switch would actually be written: the configuration document.
//
// R11 is a prohibition — "there is no configuration switch to disable any of
// this" — so it has no behaviour to exercise and would otherwise be the one
// requirement in the PRD with nothing holding it. A knob such as
//
//	recovery:
//	  respect_worktree: false
//
// would have to appear as a field with a matching YAML key, so the walk below
// reads every key the configuration exposes and fails if any of them names a
// worktree. The companion check on the resolver's signature lives in
// internal/claude (TestThereIsNoSwitchToDisableTheResolution): together they
// cover both ways a caller could be handed the old behaviour back.
func TestThereIsNoConfigurationSwitchForWorktrees(t *testing.T) {
	type key struct {
		yaml  string
		field string
	}
	var keys []key

	// walk collects one entry per exposed configuration key, descending into
	// nested sections and into the element type of slices so a new struct inside
	// `custom_patterns` or `guardrails` is covered too.
	var walk func(typ reflect.Type, path string, depth int)
	walk = func(typ reflect.Type, path string, depth int) {
		for typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || depth > 4 {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := field.Name
			if tag := field.Tag.Get("yaml"); tag != "" {
				if tag == "-" {
					// Not part of the document (Config.Source), so it cannot be
					// a knob.
					continue
				}
				if comma := strings.IndexByte(tag, ','); comma >= 0 {
					tag = tag[:comma]
				}
				if tag != "" {
					name = tag
				}
			}
			fieldPath := path + "." + field.Name
			keys = append(keys, key{yaml: name, field: fieldPath})
			walk(field.Type, fieldPath, depth+1)
		}
	}
	walk(reflect.TypeOf(Config{}), "Config", 0)

	// A vacuous pass would be worse than no test: if the walk stopped finding
	// keys, the prohibition would silently stop being checked.
	if len(keys) < 30 {
		t.Fatalf("walked %d configuration keys, want at least 30: the walk is broken, so this check proves nothing", len(keys))
	}

	worktree := regexp.MustCompile(`(?i)worktree`)
	for _, k := range keys {
		if worktree.MatchString(k.yaml) || worktree.MatchString(k.field) {
			t.Errorf("%s is exposed as %q: PRD 6 R11 says there is no switch to disable the worktree-aware resolution, "+
				"because its only effect would be to reintroduce the bug it removes", k.field, k.yaml)
		}
	}
}
