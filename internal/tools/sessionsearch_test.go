// Ported session_search behaviors: stop-word rejection, filters, per-session
// diversity, renderer shape.
package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/sessionstore"
	"github.com/ChaosJohn/gcode/internal/toolcore"
	"github.com/ChaosJohn/gcode/internal/types"
)

func seedSession(t *testing.T, home, id, title, dir, userText, assistantText string) {
	t.Helper()
	store := sessionstore.NewStore(sessionstore.Session{
		ID: id, Status: "active", WorkingDir: dir, CustomTitle: &title}, home)
	store.Session.Messages = []types.StoredMessage{
		{ID: id + "-u", Role: types.RoleUser,
			Content: []types.ContentBlock{types.TextBlock(userText)}},
		{ID: id + "-a", Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.TextBlock(assistantText)}},
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionSearchFindsHits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GCODE_HOME", home)
	seedSession(t, home, "rust", "Rust session", "/repo",
		"explain the borrow checker", "The borrow checker prevents data races")
	seedSession(t, home, "python", "Python session", "/other",
		"borrow checker in other languages", "Rust is not alone here")
	output, err := NewSessionSearchTool().Execute(
		mustJSON(t, map[string]any{"query": "borrow checker"}), directContext(t, home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Output, "## Found 2 results for 'borrow checker'") {
		t.Fatalf("header: %s", output.Output)
	}
	if !strings.Contains(output.Output, "### Result 1 - Python session") ||
		!strings.Contains(output.Output, "### Result 2 - Rust session") ||
		!strings.Contains(output.Output, "- Source: `jcode`") ||
		!strings.Contains(output.Output, "- Working dir: `/repo`") {
		t.Fatalf("body: %s", output.Output)
	}
	// Result 1 is the newest session (python saved after rust).
	if !strings.Contains(output.Output, "Python session") {
		t.Fatalf("python hit missing: %s", output.Output)
	}
}

func TestSessionSearchRejectsStopWordsOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GCODE_HOME", home)
	output, err := NewSessionSearchTool().Execute(
		mustJSON(t, map[string]any{"query": "what is the how of"}), directContext(t, home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Output, "too generic after removing common stop words") {
		t.Fatalf("stop-word gate: %s", output.Output)
	}
	if strings.Contains(output.Output, "Query cannot be empty") {
		t.Fatalf("empty gate wrong")
	}
}

func TestSessionSearchFilters(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GCODE_HOME", home)
	seedSession(t, home, "a1", "Alpha", "/work/repo", "kubernetes deployment", "answer a")
	seedSession(t, home, "b1", "Beta", "/personal", "kubernetes secrets", "answer b")
	tool := NewSessionSearchTool()
	// working_dir prefix filter (case-insensitive substring per port).
	output, err := tool.Execute(mustJSON(t, map[string]any{
		"query": "kubernetes", "working_dir": "/WORK"}), directContext(t, home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Output, "Alpha") || strings.Contains(output.Output, "Beta") {
		t.Fatalf("dir filter: %s", output.Output)
	}
	// max_per_session=1 keeps one hit per session (2 total, unfiltered).
	output, err = tool.Execute(mustJSON(t, map[string]any{
		"query": "kubernetes"}), directContext(t, home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Output, "Found 2 results") {
		t.Fatalf("diversity: %s", output.Output)
	}
	// role filter restricts to assistant messages.
	output, err = tool.Execute(mustJSON(t, map[string]any{
		"query": "answer", "role": "assistant"}), directContext(t, home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Output, "Found 2 results") {
		t.Fatalf("role filter: %s", output.Output)
	}
	// Empty query.
	output, _ = tool.Execute(mustJSON(t, map[string]any{"query": "   "}), directContext(t, home))
	if !strings.Contains(output.Output, "Query cannot be empty") {
		t.Fatalf("empty query: %s", output.Output)
	}
	// No results guidance.
	output, _ = tool.Execute(mustJSON(t, map[string]any{"query": "xylophone-zeppelin"}),
		directContext(t, home))
	if !strings.Contains(output.Output, "No session matches") {
		t.Fatalf("no results: %s", output.Output)
	}
	_ = json.Marshal
	_ = toolcore.ToolOutput{}
}
