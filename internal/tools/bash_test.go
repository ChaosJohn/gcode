// Ported: bash.rs tests (file_edit_hint, format_command_output UTF-8
// truncation, scratch dir wiring) + destructive gate integration.
package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// Ported: flags_in_place_edits.
func TestFileEditHintFlags(t *testing.T) {
	for _, command := range []string{
		"sed -i 's/a/b/' src/main.rs",
		"cd x && sed -Ei 's/a/b/g' f.rs",
		"sed --in-place=.bak 's/a/b/' f",
		"perl -pi -e 's/a/b/' f.rs",
		"python3 -c \"import pathlib;p=pathlib.Path('f');p.write_text(p.read_text().replace('a','b'))\"",
	} {
		if fileEditHint(command) == "" {
			t.Fatalf("missing hint: %s", command)
		}
	}
}

// Ported: ignores_read_only_commands.
func TestFileEditHintIgnoresReadOnly(t *testing.T) {
	for _, command := range []string{
		"sed -n '1,20p' f.rs",
		"sed 's/a/b/' f.rs",
		"grep -i foo f.rs",
		"cargo test -p jcode-app-core",
		"python3 -c \"print('a'.replace('a','b'))\"",
		"git diff --ignore-space-change",
	} {
		if fileEditHint(command) != "" {
			t.Fatalf("unexpected hint: %s", command)
		}
	}
}

// Ported: format_command_output_truncates_on_utf8_boundary.
func TestFormatCommandOutputTruncatesUTF8(t *testing.T) {
	input := strings.Repeat("a", 29999) + "é"
	output := formatCommandOutput(input, 0)
	if !strings.HasSuffix(output, "\n... (output truncated)") {
		t.Fatalf("truncation marker: %q", output[len(output)-40:])
	}
	if !strings.HasPrefix(output, strings.Repeat("a", 29999)) {
		t.Fatalf("utf8 boundary cut")
	}
	if strings.Contains(formatCommandOutput("", 0), "Exit code") {
		t.Fatalf("empty output shape")
	}
	if formatCommandOutput("", 0) != "Command completed successfully (no output)" {
		t.Fatalf("empty output message")
	}
	if !strings.Contains(formatCommandOutput("hi", 3), "\n\nExit code: 3") {
		t.Fatalf("exit code line")
	}
}

func bashContext(t *testing.T) toolcore.ToolContext {
	t.Helper()
	t.Setenv("GCODE_HOME", filepath.Join(t.TempDir(), ".gcode"))
	return directContext(t, t.TempDir())
}

func TestBashForegroundExecutes(t *testing.T) {
	ctx := bashContext(t)
	output, err := NewBashTool().Execute(mustJSON(t, map[string]any{
		"command": "printf '%s\\n%s\\n' \"$TMPDIR\" \"$JCODE_SCRATCH_DIR\"; echo done",
	}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.Output), "\n")
	if len(lines) < 3 || lines[0] != lines[1] {
		t.Fatalf("scratch dir wiring: %q", output.Output)
	}
	if !strings.Contains(output.Output, "done") {
		t.Fatalf("output: %s", output.Output)
	}
	// The scratch dir must be disk-backed (under GCODE_HOME), not /tmp.
	if !strings.Contains(lines[0], ".gcode") {
		t.Fatalf("scratch dir not under gcode home: %s", lines[0])
	}
}

func TestBashCapturesStderrAndExitCode(t *testing.T) {
	ctx := bashContext(t)
	output, err := NewBashTool().Execute(mustJSON(t, map[string]any{
		"command": "echo out; echo err 1>&2; exit 7",
	}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Output, "out\nerr") || !strings.Contains(output.Output, "Exit code: 7") {
		t.Fatalf("output: %s", output.Output)
	}
}

func TestBashTimeoutKillsAndReports(t *testing.T) {
	ctx := bashContext(t)
	output, err := NewBashTool().Execute(mustJSON(t, map[string]any{
		"command": "echo started; sleep 30; echo never", "timeout": 500,
	}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Output, "Command timed out after 500ms (0.5s)") {
		t.Fatalf("timeout message: %s", output.Output)
	}
	if !strings.Contains(output.Output, "MILLISECONDS") {
		t.Fatalf("short-timeout hint: %s", output.Output)
	}
	if strings.Contains(output.Output, "never") {
		t.Fatalf("process not killed")
	}
}

// The destructive gate refuses rm -rf ~ and the refusal text matches the
// upstream reflection/denial prompts.
func TestBashGateRefusals(t *testing.T) {
	ctx := bashContext(t)
	tool := NewBashTool()

	_, err := tool.Execute(mustJSON(t, map[string]any{"command": "rm -rf ~"}), ctx)
	if err == nil || !strings.Contains(err.Error(), "cannot be confirmed") {
		t.Fatalf("catastrophic denial: %v", err)
	}
	_, err = tool.Execute(mustJSON(t, map[string]any{"command": "rm -rf $TARGET"}), ctx)
	if err == nil || !strings.Contains(err.Error(), "user's actual") ||
		!strings.Contains(err.Error(), "justification") {
		t.Fatalf("reflection prompt: %v", err)
	}
	// A substantive justification lets it through.
	if _, err = tool.Execute(mustJSON(t, map[string]any{
		"command":       "rm -rf $TARGET",
		"justification": "the user's latest message asked to clear the target directory",
	}), ctx); err != nil {
		t.Fatalf("justified command: %v", err)
	}
	// Routine cleanup runs without a reflection turn.
	if _, err = tool.Execute(mustJSON(t, map[string]any{
		"command": "mkdir -p target && rm -rf target",
	}), ctx); err != nil {
		t.Fatalf("routine cleanup: %v", err)
	}
}

// Ported: build_shell_command_uses_disk_backed_scratch_directory (gcode env
// naming) plus background task registration.
func TestBashBackgroundRegistersTask(t *testing.T) {
	ctx := bashContext(t)
	output, err := NewBashTool().Execute(mustJSON(t, map[string]any{
		"command": "echo bg-output", "run_in_background": true,
	}), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Output, "Task ID:") || !strings.Contains(output.Output, "JCODE_PROGRESS") {
		t.Fatalf("background output: %s", output.Output)
	}
	var meta struct {
		TaskID     string `json:"task_id"`
		OutputFile string `json:"output_file"`
	}
	_ = jsonUnmarshal(output.Metadata, &meta)
	if meta.TaskID == "" || meta.OutputFile == "" {
		t.Fatalf("background metadata: %s", string(output.Metadata))
	}
	task, ok := LookupBackgroundTask(meta.TaskID)
	if !ok {
		t.Fatalf("task not registered")
	}
	// Wait briefly for completion, then check the output file.
	deadline := 50
	for i := 0; i < deadline; i++ {
		if task.ExitCode != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if task.ExitCode == nil || *task.ExitCode != 0 {
		t.Fatalf("task exit: %v", task.ExitCode)
	}
	raw, _ := os.ReadFile(task.OutputFile)
	if !strings.Contains(string(raw), "bg-output") ||
		!strings.Contains(string(raw), "Command finished with exit code: 0") {
		t.Fatalf("output file: %q", string(raw))
	}
}

func jsonUnmarshal(raw []byte, target any) error {
	return jsonUnmarshalImpl(raw, target)
}

func jsonUnmarshalImpl(raw []byte, target any) error { return jsonImpl(raw, target) }
