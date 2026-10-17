package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/toolcore"
)

func directContext(t *testing.T, dir string) toolcore.ToolContext {
	t.Helper()
	return toolcore.ToolContext{SessionID: "test-session", MessageID: "m",
		ToolCallID: "t", WorkingDir: dir, Mode: toolcore.ExecutionDirect}
}

// Ported: file_diff.rs whole_file_positions_and_disjoint_hunks.
func TestUnifiedDiffWholeFile(t *testing.T) {
	var old strings.Builder
	for n := 1; n <= 100; n++ {
		old.WriteString("line " + itoa(n) + "\n")
	}
	new := strings.Replace(strings.Replace(old.String(), "line 40\n", "  replaced\n\n", 1),
		"line 90\n", "end\n", 1)
	diff := unifiedFileDiff("file.rs", "file.rs", old.String(), new)
	if !contains(diff, "@@ -37,7 +37,8 @@") {
		t.Fatalf("hunk 1: %s", diff)
	}
	if !contains(diff, "@@ -87,7 +88,7 @@") {
		t.Fatalf("hunk 2: %s", diff)
	}
	if !contains(diff, "+  replaced\n+\n") {
		t.Fatalf("insert lines: %s", diff)
	}
}

// Ported: file_diff.rs creation_deletion_eof_and_noop + header quoting.
func TestUnifiedDiffEdges(t *testing.T) {
	created := unifiedFileDiff("/dev/null", "new file", "", "last")
	if !contains(created, "@@ -0,0 +1 @@") || !contains(created, "\\ No newline at end of file") {
		t.Fatalf("creation: %s", created)
	}
	if deleted := unifiedFileDiff("f", "/dev/null", "old\n", ""); !contains(deleted, "@@ -1 +0,0 @@") {
		t.Fatalf("deletion: %s", deleted)
	}
	if noop := unifiedFileDiff("f", "f", "same\n", "same\n"); noop != "--- a/f\n+++ b/f\n" {
		t.Fatalf("noop: %q", noop)
	}
	diff := unifiedFileDiff("tab\tand\nnewline", "f", "old\n", "new\n")
	if !strings.HasPrefix(diff, "--- \"a/tab\\tand\\nnewline\"\n") {
		t.Fatalf("quoted header: %s", diff)
	}
	if control := unifiedFileDiff("control\x01", "f", "old", "new"); !strings.HasPrefix(control, "--- \"a/control\\001\"\n") {
		t.Fatalf("control header: %s", control)
	}
}

// Ported: edit tests + the authoritative-diff integration shape.
func TestEditToolSingleAndMulti(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.rs")
	var original strings.Builder
	for n := 1; n <= 100; n++ {
		original.WriteString("line " + itoa(n) + "\n")
	}
	if err := os.WriteFile(path, []byte(original.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := directContext(t, dir)

	edit := NewEditTool()
	output, err := edit.Execute(mustJSON(t, map[string]any{
		"file_path": "file.rs", "old_string": "ine 40", "new_string": "ong 40",
	}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(output.Output, "@@ -37,7 +37,7 @@") || !contains(output.Output, "40- ine 40") {
		t.Fatalf("single edit output: %s", output.Output)
	}
	var meta struct {
		Diff string `json:"diff"`
	}
	_ = json.Unmarshal(output.Metadata, &meta)
	diff := meta.Diff
	after, _ := os.ReadFile(path)
	if diff != unifiedFileDiff("file.rs", "file.rs", original.String(), string(after)) {
		t.Fatalf("authoritative diff mismatch")
	}

	output, err = edit.Execute(mustJSON(t, map[string]any{
		"file_path": "file.rs",
		"edits": []map[string]any{
			{"old_string": "line 10\n", "new_string": "inserted\nextra\n"},
			{"old_string": "line 90", "new_string": "tail"},
		},
	}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(output.Output, "✓ Edit 1") || !contains(output.Output, "at line 10") {
		t.Fatalf("multi edit report: %s", output.Output)
	}

	output, err = edit.Execute(mustJSON(t, map[string]any{
		"file_path": "file.rs", "old_string": "line", "new_string": "row", "replace_all": true,
	}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(output.Output, "replaced 97 occurrence") {
		t.Fatalf("replace_all: %s", output.Output)
	}

	// Failure: missing old_string.
	if _, err := edit.Execute(mustJSON(t, map[string]any{
		"file_path": "file.rs", "old_string": "missing", "new_string": "not applied",
	}), ctx); err == nil {
		t.Fatalf("missing old_string must fail")
	}

	// Both forms at once is rejected.
	if _, err := edit.Execute(mustJSON(t, map[string]any{
		"file_path": "file.rs", "edits": []map[string]any{{"old_string": "a", "new_string": "b"}},
		"old_string": "a", "new_string": "b",
	}), ctx); err == nil || !contains(err.Error(), "not both") {
		t.Fatalf("mixed forms: %v", err)
	}
}

// Ported: flexible_match_hint diagnostics.
func TestFlexibleMatchHint(t *testing.T) {
	content := "  indented\nplain\n"
	hint := flexibleMatchHint(content, "indented", "f.rs")
	if !contains(hint, "different indentation") {
		t.Fatalf("indentation hint: %s", hint)
	}
	hint = flexibleMatchHint(content, "  indented ", "f.rs")
	if !contains(hint, "after trimming whitespace") {
		t.Fatalf("trim hint: %s", hint)
	}
	hint = flexibleMatchHint(content, "absent", "f.rs")
	if !contains(hint, "old_string not found in f.rs") {
		t.Fatalf("absent hint: %s", hint)
	}
}

// Ported: read range normalization errors + line numbering + continuation.
func TestReadTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	var content strings.Builder
	for n := 1; n <= 30; n++ {
		content.WriteString(itoa(n) + "\n")
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := directContext(t, dir)
	read := NewReadTool()

	output, err := read.Execute(mustJSON(t, map[string]any{"file_path": "sample.txt"}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(output.Output, "    1\t1\n") || !contains(output.Output, "   30\t30") {
		t.Fatalf("numbering: %q", output.Output[:80])
	}
	if contains(output.Output, "more lines") {
		t.Fatalf("no continuation expected")
	}

	output, err = read.Execute(mustJSON(t, map[string]any{"file_path": "sample.txt", "start_line": 28}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(output.Output, "   28\t28") || !contains(output.Output, "   30\t30") {
		t.Fatalf("start_line read: %s", output.Output)
	}
	if contains(output.Output, "more lines") {
		t.Fatalf("full tail needs no continuation: %s", output.Output)
	}

	output, err = read.Execute(mustJSON(t, map[string]any{"file_path": "sample.txt", "limit": 5}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(output.Output, "... 25 more lines (use offset=5 to continue)") {
		t.Fatalf("continuation: %s", output.Output)
	}

	// Mixed styles rejected with the upstream message.
	if _, err := read.Execute(mustJSON(t, map[string]any{
		"file_path": "sample.txt", "start_line": 2, "offset": 3,
	}), ctx); err == nil || !contains(err.Error(), "not both") {
		t.Fatalf("mixed styles: %v", err)
	}
	if _, err := read.Execute(mustJSON(t, map[string]any{
		"file_path": "sample.txt", "start_line": 0,
	}), ctx); err == nil || !contains(err.Error(), "1-based") {
		t.Fatalf("zero start_line: %v", err)
	}
	if _, err := read.Execute(mustJSON(t, map[string]any{
		"file_path": "sample.txt", "end_line": 2, "start_line": 5,
	}), ctx); err == nil || !contains(err.Error(), "greater than or equal to start_line") {
		t.Fatalf("inverted range: %v", err)
	}

	// Missing file with suggestions.
	if err := os.WriteFile(filepath.Join(dir, "sample2.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The similarity check is containment-based (upstream find_similar_files).
	if _, err := read.Execute(mustJSON(t, map[string]any{"file_path": "ample.txt"}), ctx); err == nil ||
		!contains(err.Error(), "Did you mean") {
		t.Fatalf("suggestions: %v", err)
	}
	if _, err := read.Execute(mustJSON(t, map[string]any{"file_path": "totally-missing-xyz.txt"}), ctx); err == nil ||
		!contains(err.Error(), "File not found") || contains(err.Error(), "Did you mean") {
		t.Fatalf("no suggestion expected: %v", err)
	}
}

// Ported: binary file detection and the binary notice.
func TestReadBinaryDetection(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(binaryPath, bytesRepeat([]byte{0, 1, 2, 0, 0, 0, 0, 0, 0, 0}, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := directContext(t, dir)
	output, err := NewReadTool().Execute(mustJSON(t, map[string]any{"file_path": "blob.bin"}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(output.Output, "Binary file detected") {
		t.Fatalf("binary notice: %s", output.Output)
	}
}

// Ported: write diff summary + created/updated shapes.
func TestWriteTool(t *testing.T) {
	dir := t.TempDir()
	ctx := directContext(t, dir)
	write := NewWriteTool()

	output, err := write.Execute(mustJSON(t, map[string]any{
		"file_path": "new.txt", "content": "a\nb\nc\n",
	}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.Output, "Created new.txt (3 lines):\n") {
		t.Fatalf("created: %s", output.Output)
	}
	if !contains(MetadataDiff(output), "--- /dev/null\n+++ b/new.txt\n") {
		t.Fatalf("creation diff: %q", MetadataDiff(output))
	}

	output, err = write.Execute(mustJSON(t, map[string]any{
		"file_path": "new.txt", "content": "a\nchanged\nc\n",
	}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.Output, "Updated new.txt (3 lines):") {
		t.Fatalf("updated: %s", output.Output)
	}
	if !contains(output.Output, "2- b") || !contains(output.Output, "2+ changed") {
		t.Fatalf("diff summary: %s", output.Output)
	}
	if !contains(MetadataDiff(output), "@@ -1,3 +1,3 @@") {
		t.Fatalf("unified: %q", MetadataDiff(output))
	}

	// A write to the active config appends the change report.
	home := t.TempDir()
	t.Setenv("GCODE_HOME", filepath.Join(home, ".gcode"))
	configPath := filepath.Join(home, ".gcode", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[provider]\nmodel = \"a\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := ConfigPathProvider
	ConfigPathProvider = func() string { return configPath }
	t.Cleanup(func() { ConfigPathProvider = prev })

	output, err = write.Execute(mustJSON(t, map[string]any{
		"file_path": configPath, "content": "[provider]\nmodel = \"b\"\n",
	}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(output.Output, "Config changes:\n- `provider.model`: \"a\" -> \"b\" (live now)") {
		t.Fatalf("config notice: %s", output.Output)
	}
}

// Ported: ls default ignore + sorting + truncation count.
func TestLsTool(t *testing.T) {
	dir := t.TempDir()
	for _, dirName := range []string{"sub", "node_modules", ".git"} {
		if err := os.MkdirAll(filepath.Join(dir, dirName), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := directContext(t, dir)
	output, err := NewLsTool().Execute(mustJSON(t, map[string]any{"path": "."}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(output.Output, "sub/\n") || !contains(output.Output, "  nested.txt\n") {
		t.Fatalf("tree: %s", output.Output)
	}
	if contains(output.Output, "node_modules") || contains(output.Output, ".git") {
		t.Fatalf("defaults ignored: %s", output.Output)
	}
	if !contains(output.Output, "2 files, 1 directories") {
		t.Fatalf("counts: %s", output.Output)
	}
	if _, err := NewLsTool().Execute(mustJSON(t, map[string]any{"path": "a.txt"}), ctx); err == nil ||
		!contains(err.Error(), "Not a directory") {
		t.Fatalf("file path: %v", err)
	}
}

// The glob matcher covers the ignore patterns ls uses.
func TestGlobMatch(t *testing.T) {
	if !globMatch("*.log", "debug.log") || globMatch("*.log", "debug.txt") {
		t.Fatalf("star")
	}
	if !globMatch("node_modules", "node_modules") {
		t.Fatalf("exact")
	}
	if !globMatch("target/**", "target/x") || !globMatch("target/**", "target/x/y") {
		t.Fatalf("double star")
	}
	if globMatch("target/**", "src/target/x") {
		t.Fatalf("double star is anchored")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func bytesRepeat(pattern []byte, times int) []byte {
	out := make([]byte, 0, len(pattern)*times)
	for i := 0; i < times; i++ {
		out = append(out, pattern...)
	}
	return out
}

func itoa(n int) string { return json.Number(jsonInt(n)).String() }

func jsonInt(n int) string {
	encoded, _ := json.Marshal(n)
	return string(encoded)
}
