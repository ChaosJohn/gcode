// Tests for the replace, apply_patch, and batch tools (ported upstream
// cases plus wire-shape assertions).
package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/agent"
	"github.com/ChaosJohn/gcode/internal/toolcore"
)

func replaceCtx(root string) toolcore.ToolContext {
	return toolcore.ToolContext{SessionID: "test-session", WorkingDir: root}
}

func TestReplaceLiteralMultiFile(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(body), 0o644)
	}
	write("a.go", "alpha OLD beta\nOLD again\n")
	write("b.go", "no match here\n")
	write("c.txt", "OLD in txt\n")
	write("nested/d.go", "OLD nested\n")

	tool := NewReplaceTool()
	// expected_count mismatch writes nothing.
	input := json.RawMessage(`{"pattern":"OLD","replacement":"NEW","expected_count":99}`)
	if _, err := tool.Execute(input, replaceCtx(root)); err == nil ||
		!strings.Contains(err.Error(), "Nothing written") {
		t.Fatalf("expected mismatch: %v", err)
	}
	if body, _ := os.ReadFile(filepath.Join(root, "a.go")); strings.Contains(string(body), "NEW") {
		t.Fatal("wrote despite count mismatch")
	}

	input = json.RawMessage(`{"pattern":"OLD","replacement":"NEW","glob":"**/*.go"}`)
	out, err := tool.Execute(input, replaceCtx(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Replaced 3 match(es) in 2 file(s)", "a.go: 2", "nested/d.go: 1", "-alpha OLD beta", "+alpha NEW beta"} {
		if !strings.Contains(out.Output, want) {
			t.Fatalf("missing %q in %s", want, out.Output)
		}
	}
	// Only *.go touched.
	if body, _ := os.ReadFile(filepath.Join(root, "c.txt")); strings.Contains(string(body), "NEW") {
		t.Fatal("glob filter ignored")
	}
}

func TestReplaceRegexAndDryRun(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "log.txt"),
		[]byte("count=1\ncount=22\n"), 0o644)
	tool := NewReplaceTool()
	// Dry run reports but does not write.
	input := json.RawMessage(`{"pattern":"count=(\\d+)","replacement":"n=$1","regex":true,"dry_run":true}`)
	out, err := tool.Execute(input, replaceCtx(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, "Would replace 2 match(es)") ||
		!strings.Contains(out.Output, "+n=22") {
		t.Fatalf("dry run: %s", out.Output)
	}
	if body, _ := os.ReadFile(filepath.Join(root, "log.txt")); strings.Contains(string(body), "n=") {
		t.Fatal("dry run wrote")
	}
	// Real run expands capture groups.
	input = json.RawMessage(`{"pattern":"count=(\\d+)","replacement":"n=$1","regex":true}`)
	if _, err := tool.Execute(input, replaceCtx(root)); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "log.txt"))
	if string(body) != "n=1\nn=22\n" {
		t.Fatalf("written: %q", string(body))
	}
	// Zero matches is a clean error.
	input = json.RawMessage(`{"pattern":"zzz","replacement":"x"}`)
	if _, err := tool.Execute(input, replaceCtx(root)); err == nil ||
		!strings.Contains(err.Error(), "No matches") {
		t.Fatalf("zero: %v", err)
	}
}

func TestApplyPatchUpdateAddDelete(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\n\nfunc main() {\n\tprintln(\"old\")\n}\n"), 0o644)
	tool := NewApplyPatchTool()
	patch := "*** Begin Patch\n" +
		"*** Update File: main.go\n" +
		"@@\n" +
		"\tprintln(\"old\")\n" +
		"-	println(\"old\")\n" +
		"+	println(\"new\")\n" +
		"*** Add File: extra.txt\n" +
		"+hello\n" +
		"+world\n" +
		"*** Delete File: gone.txt\n" +
		"*** End Patch\n"
	// Deleting a missing file fails atomically (main.go untouched).
	if _, err := tool.Execute(json.RawMessage(`{"patch_text":`+
		jsonMarshal(patch)+`}`), replaceCtx(root)); err == nil {
		t.Fatal("expected delete error")
	}
	if body, _ := os.ReadFile(filepath.Join(root, "main.go")); strings.Contains(string(body), "new") {
		t.Fatal("partial apply")
	}
	os.WriteFile(filepath.Join(root, "gone.txt"), []byte("bye\n"), 0o644)
	out, err := tool.Execute(json.RawMessage(`{"patch_text":`+jsonMarshal(patch)+`}`),
		replaceCtx(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, "Applied patch to 3 file(s)") {
		t.Fatalf("summary: %s", out.Output)
	}
	body, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if !strings.Contains(string(body), `println("new")`) {
		t.Fatalf("main.go: %s", string(body))
	}
	added, _ := os.ReadFile(filepath.Join(root, "extra.txt"))
	if string(added) != "hello\nworld\n" {
		t.Fatalf("extra.txt: %q", string(added))
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("gone.txt still present")
	}
}

// Context-less first chunk (no @@ line) and EOF anchoring.
func TestApplyPatchFirstChunkAndEOF(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	tool := NewApplyPatchTool()
	patch := "*** Begin Patch\n" +
		"*** Update File: f.txt\n" +
		" one\n" +
		"-two\n" +
		"+TWO\n" +
		"*** End Patch\n"
	if _, err := tool.Execute(json.RawMessage(`{"patch_text":`+jsonMarshal(patch)+`}`),
		replaceCtx(root)); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(body) != "one\nTWO\nthree\n" {
		t.Fatalf("f.txt: %q", string(body))
	}
	// *** End of File anchor (after the diff lines) appends at the end.
	patch2 := "*** Begin Patch\n" +
		"*** Update File: f.txt\n" +
		"+four\n" +
		"*** End of File\n" +
		"*** End Patch\n"
	if _, err := tool.Execute(json.RawMessage(`{"patch_text":`+jsonMarshal(patch2)+`}`),
		replaceCtx(root)); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(filepath.Join(root, "f.txt"))
	if !strings.HasSuffix(string(body), "three\nfour\n") {
		t.Fatalf("append: %q", string(body))
	}
	// Unfindable context errors with the offending lines.
	patch3 := "*** Begin Patch\n*** Update File: f.txt\n-nope\n+yes\n*** End Patch\n"
	if _, err := tool.Execute(json.RawMessage(`{"patch_text":`+jsonMarshal(patch3)+`}`),
		replaceCtx(root)); err == nil || !strings.Contains(err.Error(), "Failed to find") {
		t.Fatalf("missing context: %v", err)
	}
}

func TestBatchParallel(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("A"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("B"), 0o644)
	registry := agent.NewRegistry()
	registry.Register(NewReadTool())
	registry.Register(NewBashTool())
	registry.Register(NewBatchTool())
	SetBatchRegistry(registry)
	tool := NewBatchTool()
	ctx := replaceCtx(root)
	input := json.RawMessage(`{"tool_calls":[
		{"tool":"read","parameters":{"file_path":"a.txt"}},
		{"tool":"read","parameters":{"file_path":"missing.txt"}},
		{"tool":"bash","parameters":{"command":"echo hello"}}
	]}`)
	out, err := tool.Execute(input, ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--- [1] read ---", "--- [2] read ---", "--- [3] bash ---",
		"hello", "Completed: 2 succeeded, 1 failed",
	} {
		if !strings.Contains(out.Output, want) {
			t.Fatalf("missing %q in:\n%s", want, out.Output)
		}
	}
	// Empty batch rejected.
	if _, err := tool.Execute(json.RawMessage(`{"tool_calls":[]}`), ctx); err == nil {
		t.Fatal("empty batch accepted")
	}
}

func jsonMarshal(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}
