package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config edit notice (ported from tool/config_edit_notice.rs +
// config/change_report.rs): every write that lands on the active config file
// appends an explicit report of which keys changed and whether each one is
// live in running sessions.

// ConfigPathProvider resolves the active config file; the app injects it.
var ConfigPathProvider func() string

// RestartRequiredSections are consumed once at startup rather than through
// the reloadable config cache.
var RestartRequiredSections = []string{"gateway", "acp", "launch_hotkeys"}

func comparablePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if resolved, err := filepath.Abs(path); err == nil {
		return resolved
	}
	return path
}

func isActiveConfigFile(path string) bool {
	if ConfigPathProvider == nil {
		return false
	}
	active := ConfigPathProvider()
	if active == "" {
		return false
	}
	return comparablePath(path) == comparablePath(active)
}

// ConfigEditNotice renders the report appended after a config write; empty
// for non-config files and comment-only edits.
func ConfigEditNotice(path, before, after string) string {
	if !isActiveConfigFile(path) {
		return ""
	}
	// A config file that no longer parses is silently ignored by the loader,
	// which falls back to defaults — the worst outcome to leave unreported.
	if _, err := os.Stat(path); err == nil {
		if raw, readErr := os.ReadFile(path); readErr == nil {
			var probe map[string]any
			if tomlErr := toml.Unmarshal(raw, &probe); tomlErr != nil {
				return fmt.Sprintf(
					"\n\nWARNING: %s no longer parses as TOML, so jcode is falling back to "+
						"default settings and every setting in this file is being ignored. "+
						"Fix the syntax error: %v", path, tomlErr)
			}
		}
	}
	changes := diffTOML(before, after)
	if len(changes) == 0 {
		return ""
	}
	return "\n\n" + SummarizeChanges(changes)
}

func appendConfigEditNotice(body, path, before, after string) string {
	return body + ConfigEditNotice(path, before, after)
}

// ConfigChange is one changed config key.
type ConfigChange struct {
	Key     string
	Before  *string
	After   *string
	LiveNow bool
}

// LivenessForKey reports whether a dotted key is live after edit.
func LivenessForKey(key string) bool {
	section, _, _ := strings.Cut(key, ".")
	for _, restart := range RestartRequiredSections {
		if section == restart {
			return false
		}
	}
	return true
}

func (c ConfigChange) render() string {
	before, after := "(unset)", "(unset)"
	if c.Before != nil {
		before = *c.Before
	}
	if c.After != nil {
		after = *c.After
	}
	liveness := "live now"
	if !c.LiveNow {
		liveness = "needs restart"
	}
	return fmt.Sprintf("- `%s`: %s -> %s (%s)", c.Key, before, after, liveness)
}

// DiffTOML diffs two config files into a per-key change list, sorted by key.
// A side that fails to parse is treated as empty.
func diffTOML(before, after string) []ConfigChange {
	beforeMap := flattenTOML(before)
	afterMap := flattenTOML(after)
	keys := map[string]bool{}
	for key := range beforeMap {
		keys[key] = true
	}
	for key := range afterMap {
		keys[key] = true
	}
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)
	var changes []ConfigChange
	for _, key := range sorted {
		oldValue, hasOld := beforeMap[key]
		newValue, hasNew := afterMap[key]
		if hasOld == hasNew && oldValue == newValue {
			continue
		}
		change := ConfigChange{Key: key, LiveNow: LivenessForKey(key)}
		if hasOld {
			change.Before = &oldValue
		}
		if hasNew {
			change.After = &newValue
		}
		changes = append(changes, change)
	}
	return changes
}

// SummarizeTOMLChange renders the human summary; empty when nothing changed.
func SummarizeTOMLChange(before, after string) string {
	changes := diffTOML(before, after)
	if len(changes) == 0 {
		return ""
	}
	return SummarizeChanges(changes)
}

// SummarizeChanges renders an already-computed change list.
func SummarizeChanges(changes []ConfigChange) string {
	var out strings.Builder
	out.WriteString("Config changes:\n")
	for _, change := range changes {
		out.WriteString(change.render())
		out.WriteString("\n")
	}
	var restart []string
	for _, change := range changes {
		if !change.LiveNow {
			restart = append(restart, change.Key)
		}
	}
	if len(restart) == 0 {
		out.WriteString("All changes are live in running jcode sessions; no restart needed." +
			"\nKeybinding edits apply to the next keystroke.")
	} else {
		fmt.Fprintf(&out, "Restart required for: %s. Other changes are already live.",
			strings.Join(restart, ", "))
	}
	return out.String()
}

// flattenTOML flattens TOML text into dotted-key → rendered-value pairs.
// Arrays and inline tables render as compact JSON so an element-level edit
// still shows up as a change on the owning key.
func flattenTOML(text string) map[string]string {
	out := map[string]string{}
	var value map[string]any
	if err := toml.Unmarshal([]byte(text), &value); err != nil {
		return out
	}
	flattenValue("", value, out)
	return out
}

func flattenValue(prefix string, value any, out map[string]string) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			flattenValue(path, child, out)
		}
	default:
		if prefix == "" {
			return
		}
		out[prefix] = renderValue(value)
	}
}

func renderValue(value any) string {
	if s, ok := value.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}
