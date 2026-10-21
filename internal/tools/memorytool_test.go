// Memory tests (ported cases): store round-trip, duplicate reinforcement,
// normalized search, scope isolation, prompt rendering, and the tool
// surface verbatim outputs.
package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/memory"
	"github.com/ChaosJohn/gcode/internal/toolcore"
)

func memoryContext(t *testing.T, projectDir string) toolcore.ToolContext {
	t.Helper()
	t.Setenv("GCODE_HOME", t.TempDir())
	if projectDir == "" {
		projectDir = t.TempDir()
	}
	return toolcore.ToolContext{SessionID: "test-session", WorkingDir: projectDir}
}

func TestMemoryRememberRecallSearchForget(t *testing.T) {
	ctx := memoryContext(t, "")
	tool := NewMemoryTool()
	run := func(input string) (string, error) {
		out, err := tool.Execute(json.RawMessage(input), ctx)
		return out.Output, err
	}
	out, err := run(`{"action":"remember","content":"User prefers compact diffs","category":"preference"}`)
	if err != nil || !strings.HasPrefix(out, "Remembered preference (project):") {
		t.Fatalf("remember: %q %v", out, err)
	}
	id := out[strings.Index(out, "[id: ")+5 : len(out)-1]

	// Duplicate reinforces instead of adding a row.
	if _, err := run(`{"action":"remember","content":"  User prefers compact diffs ","category":"preference"}`); err != nil {
		t.Fatal(err)
	}
	listOut, err := run(`{"action":"list"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(listOut, "Memories (1):") || strings.Count(listOut, "id: ") != 1 {
		t.Fatalf("dedup: %q", listOut)
	}
	// Access count bumped on the duplicate path.
	if !strings.Contains(listOut, "access_count") && strings.Count(listOut, "id: ") == 1 {
		// rendered output doesn't show counts; check the store file directly.
		home := os.Getenv("GCODE_HOME")
		matches, _ := filepath.Glob(filepath.Join(home, "memory", "projects", "*.json"))
		raw, _ := os.ReadFile(matches[0])
		var store struct {
			Entries []struct {
				AccessCount int `json:"access_count"`
			} `json:"entries"`
		}
		_ = json.Unmarshal(raw, &store)
		if len(store.Entries) != 1 || store.Entries[0].AccessCount != 1 {
			t.Fatalf("reinforce: %s", string(raw))
		}
	}

	// Tag then search by tag.
	if out, err = run(`{"action":"tag","id":"` + id + `","tags":["style"]}`); err != nil ||
		!strings.HasPrefix(out, "Tagged memory") {
		t.Fatalf("tag: %q %v", out, err)
	}
	out, err = run(`{"action":"search","query":"style"}`)
	if err != nil || !strings.HasPrefix(out, "Found 1 memories:") {
		t.Fatalf("tag search: %q %v", out, err)
	}
	// Normalized search: separators map to spaces.
	out, err = run(`{"action":"search","query":"user-prefers"}`)
	if err != nil || !strings.HasPrefix(out, "Found 1 memories:") {
		t.Fatalf("normalized search: %q %v", out, err)
	}
	out, err = run(`{"action":"search","query":"zzz-absent"}`)
	if err != nil || out != "No memories matching 'zzz-absent'" {
		t.Fatalf("miss: %q %v", out, err)
	}

	// Forget (soft delete) then recall says none.
	if out, err = run(`{"action":"forget","id":"` + id + `"}`); err != nil ||
		out != "Forgot: "+id {
		t.Fatalf("forget: %q %v", out, err)
	}
	if out, err = run(`{"action":"recall"}`); err != nil ||
		out != "No memories stored yet." {
		t.Fatalf("recall after forget: %q %v", out, err)
	}
	if out, err = run(`{"action":"forget","id":"` + id + `"}`); err != nil ||
		out != "Not found: "+id {
		t.Fatalf("double forget: %q %v", out, err)
	}
}

// Project and global scopes are separate stores.
func TestMemoryScopeIsolation(t *testing.T) {
	tool := NewMemoryTool()
	home := t.TempDir()
	t.Setenv("GCODE_HOME", home)
	projectA := toolcore.ToolContext{SessionID: "s", WorkingDir: t.TempDir()}
	projectB := toolcore.ToolContext{SessionID: "s", WorkingDir: t.TempDir()}
	_ = home
	tool.Execute(json.RawMessage(`{"action":"remember","content":"A fact","category":"fact","scope":"project"}`), projectA)
	tool.Execute(json.RawMessage(`{"action":"remember","content":"Global fact","category":"fact","scope":"global"}`), projectA)
	toolB := NewMemoryTool()
	toolB.Execute(json.RawMessage(`{"action":"remember","content":"B fact","category":"fact","scope":"project"}`), projectB)

	outA, _ := tool.Execute(json.RawMessage(`{"action":"search","query":"fact","scope":"project"}`), projectA)
	if !strings.Contains(outA.Output, "A fact") || strings.Contains(outA.Output, "B fact") {
		t.Fatalf("project A: %q", outA.Output)
	}
	outB, _ := toolB.Execute(json.RawMessage(`{"action":"search","query":"fact","scope":"project"}`), projectB)
	if !strings.Contains(outB.Output, "B fact") || strings.Contains(outB.Output, "A fact") {
		t.Fatalf("project B: %q", outB.Output)
	}
	outAll, _ := tool.Execute(json.RawMessage(`{"action":"search","query":"fact","scope":"all"}`), projectA)
	if !strings.Contains(outAll.Output, "A fact") || !strings.Contains(outAll.Output, "Global fact") {
		t.Fatalf("all: %q", outAll.Output)
	}
	// Remember with no working dir and no scope defaults to global.
	homeless := toolcore.ToolContext{SessionID: "s", WorkingDir: ""}
	if _, err := tool.Execute(json.RawMessage(`{"action":"remember","content":"homeless"}`), homeless); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("GCODE_HOME"), "memory", "global.json")); err != nil {
		t.Fatalf("global store: %v", err)
	}
}

// Prompt rendering groups categories in the ported order.
func TestMemoryFormatForPrompt(t *testing.T) {
	entries := []memory.Entry{
		{Category: memory.CategoryFact, Content: "fact one"},
		{Category: memory.CategoryCorrection, Content: "correction one"},
		{Category: memory.CategoryPreference, Content: "pref one"},
		{Category: memory.CategoryEntity, Content: "entity one"},
		{Category: memory.Category("custom"), Content: "custom one"},
	}
	formatted := memory.FormatForPrompt(entries, 0)
	order := []string{
		"## Corrections\n1. correction one",
		"## Facts\n1. fact one",
		"## Preferences\n1. pref one",
		"## Entities\n1. entity one",
		"## custom\n1. custom one",
	}
	last := -1
	for _, section := range order {
		idx := strings.Index(formatted, section)
		if idx < 0 || idx < last {
			t.Fatalf("order around %q in:\n%s", section, formatted)
		}
		last = idx
	}
}

// Bad inputs surface clean errors.
func TestMemoryBadInputs(t *testing.T) {
	ctx := memoryContext(t, "")
	tool := NewMemoryTool()
	if _, err := tool.Execute(json.RawMessage(`{"action":"remember","content":"x","category":"weird"}`), ctx); err == nil ||
		!strings.Contains(err.Error(), "Unknown category") {
		t.Fatalf("category: %v", err)
	}
	if _, err := tool.Execute(json.RawMessage(`{"action":"remember","content":"x","scope":"weird"}`), ctx); err == nil ||
		!strings.Contains(err.Error(), "Unknown scope") {
		t.Fatalf("scope: %v", err)
	}
	if _, err := tool.Execute(json.RawMessage(`{"action":"tag","id":"x"}`), ctx); err == nil ||
		!strings.Contains(err.Error(), "tag required") {
		t.Fatalf("tags: %v", err)
	}
	if _, err := tool.Execute(json.RawMessage(`{"action":"dance"}`), ctx); err == nil ||
		!strings.Contains(err.Error(), "Unknown action") {
		t.Fatalf("action: %v", err)
	}
}
