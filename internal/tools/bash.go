package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ChaosJohn/gcode/internal/cmdrisk"
	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// bash tool (ported from tool/bash.rs + bash_destructive_gate.rs +
// jcode-command-risk). The upstream foreground path pipes stdio (PTY is only
// used by the interactive TUI shell, not the tool).

const (
	bashMaxOutputLen     = 30000
	bashDefaultTimeoutMs = 120000
	bashMaxTimeoutMs     = 600000
)

const bashToolDescription = "Run a bash command. For repository searches, prefer agentgrep or rg over grep -r. rg skips hidden and ignored files by default, not all untracked files. Use --hidden --no-ignore when those files must be searched."

const bashBackgroundProgressGuidance = "For long-running background commands, prefer scripts or commands that periodically print progress updates. Best format: print lines starting with `JCODE_PROGRESS ` followed by JSON like {\"percent\":42,\"message\":\"Running\"} or {\"current\":120,\"total\":1000,\"unit\":\"batches\",\"message\":\"Epoch 2/5\",\"eta_seconds\":30}. Supported JSON fields are `percent`, `message`, `current`, `total`, `unit`, `eta_seconds`, and optional `kind`=`indeterminate` or `kind`=`checkpoint`. For milestone-style wakeups, print `JCODE_CHECKPOINT {\"message\":\"Unit tests passed\"}`. Generic fallback output that can be parsed includes `42%`, `3/10 tests`, `3 of 10 steps`, `1.5/3.0 GiB`, or phase lines like `Compiling ...`, `Downloading ...`, `Running ...`, and `Building ...`. If you are writing the script yourself, add these progress/checkpoint lines explicitly. Put large temporary files, worktrees, and virtual environments under `$JCODE_SCRATCH_DIR`, not `/tmp`, because `/tmp` may be RAM-backed."

// BackgroundTask is one registered background command.
type BackgroundTask struct {
	ID          string
	Display     string
	SessionID   string
	OutputFile  string
	StatusFile  string
	StartedAt   time.Time
	ExitCode    *int
	description string
}

var (
	backgroundMu    sync.Mutex
	backgroundTasks = map[string]*BackgroundTask{}
	backgroundSeq   = 0
)

// LookupBackgroundTask returns a task by id.
func LookupBackgroundTask(id string) (*BackgroundTask, bool) {
	backgroundMu.Lock()
	defer backgroundMu.Unlock()
	task, ok := backgroundTasks[id]
	return task, ok
}

// BashTool runs shell commands behind the destructive gate.
type BashTool struct{}

// NewBashTool builds the bash tool.
func NewBashTool() *BashTool { return &BashTool{} }

// Name implements Tool.
func (t *BashTool) Name() string { return "bash" }

// Description implements Tool.
func (t *BashTool) Description() string { return bashToolDescription }

// ParametersSchema implements Tool (ported bash_parameters_schema).
func (t *BashTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["command"],
		"properties": {
			"command": {
				"type": "string",
				"description": "The bash command to execute. Put large temp files under $JCODE_SCRATCH_DIR, not /tmp."
			},
			"timeout": {
				"type": "integer",
				"description": "Timeout in MILLISECONDS (not seconds), e.g. 600000 = 10min; kills with exit 124. Omit for no timeout."
			},
			"run_in_background": {
				"type": "boolean",
				"description": "Run in background. Emit ` + "`JCODE_PROGRESS {json}`" + ` lines for progress reporting."
			},
			"notify": {"type": "boolean", "description": "Notify on completion."},
			"wake": {"type": "boolean", "description": "Wake on completion."},
			"stall_wake_seconds": {
				"type": "integer",
				"description": "With run_in_background: wake after N seconds without output/progress (min 30). For jobs that may hang."
			},
			"justification": {
				"type": "string",
				"description": "Only when re-issuing a command the destructive gate refused; explain which user request it serves."
			}
		}
	}`)
}

// BashInput is the decoded bash tool input.
type BashInput struct {
	Command          string  `json:"command"`
	Intent           *string `json:"intent"`
	Timeout          *uint64 `json:"timeout"`
	RunInBackground  *bool   `json:"run_in_background"`
	Notify           *bool   `json:"notify"`
	Wake             *bool   `json:"wake"`
	StallWakeSeconds *uint64 `json:"stall_wake_seconds"`
	Justification    *string `json:"justification"`
}

// ScratchDir resolves $JCODE_SCRATCH_DIR under the gcode home (disk-backed).
func ScratchDir() string {
	if dir := os.Getenv("GCODE_SCRATCH_DIR"); dir != "" {
		return dir
	}
	home := os.Getenv("GCODE_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".gcode")
	}
	dir := filepath.Join(home, "scratch")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// Execute implements Tool.
func (t *BashTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params BashInput
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	if trimSpace(params.Command) == "" {
		return toolcore.ToolOutput{}, fmt.Errorf("bash requires a non-empty 'command'")
	}

	// Destructive-command gate (#604), before background dispatch.
	refusal := bashDestructiveRefusal(params.Command, params.Justification, ctx.WorkingDir)
	if refusal != "" {
		return toolcore.ToolOutput{}, fmt.Errorf("%s", refusal)
	}

	if wrapped := wrapRepoCargoCommands(params.Command, ctx.WorkingDir); wrapped != "" {
		params.Command = wrapped
	}

	if params.RunInBackground != nil && *params.RunInBackground {
		return t.executeBackground(params, ctx)
	}

	hint := fileEditHint(params.Command)
	output, err := t.executeForeground(params, ctx)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	if hint != "" {
		output.Output += "\n\n" + hint
	}
	return output, nil
}

// bashDestructiveRefusal ports destructive_command_refusal.
func bashDestructiveRefusal(command string, justification *string, workingDir string) string {
	ctx := cmdrisk.RiskContext{WorkingDir: workingDir,
		HomeDir: os.Getenv("HOME"), ScratchDir: ScratchDir()}
	assessment := cmdrisk.Assess(command, &ctx)
	if assessment.Level.RunsImmediately() {
		return ""
	}
	just := cmdrisk.Justification{}
	if justification != nil {
		just.Text = *justification
	}
	outcome, message := cmdrisk.Gate(assessment, just)
	if outcome == cmdrisk.Allow {
		return ""
	}
	return message
}

// wrapRepoCargoCommands routes ordinary cargo invocations through a repo
// wrapper script when one exists (ported; inert without scripts/dev_cargo.sh).
func wrapRepoCargoCommands(command, workingDir string) string {
	if workingDir == "" {
		return ""
	}
	dir := workingDir
	for {
		wrapper := filepath.Join(dir, "scripts", "dev_cargo.sh")
		if info, err := os.Stat(wrapper); err == nil && !info.IsDir() {
			quoted := "'" + strings.ReplaceAll(wrapper, "'", `'"'"'`) + "'"
			return fmt.Sprintf(`export JCODE_DEV_CARGO_SCRIPT=%s
cargo() {
  if [[ "${JCODE_IN_DEV_CARGO:-0}" == "1" ]]; then
    command cargo "$@"
  else
    JCODE_IN_DEV_CARGO=1 "$JCODE_DEV_CARGO_SCRIPT" "$@"
  fi
}
export -f cargo
%s`, quoted, command)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// executeForeground runs `bash -c` with piped stdio and a timeout that kills
// the process group (the upstream timeout-promotes-to-background path needs
// the background task registry, which arrives with the agent loop).
func (t *BashTool) executeForeground(params BashInput, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	title := params.Command
	if params.Intent != nil && *params.Intent != "" {
		title = *params.Intent
	}

	command := exec.Command("bash", "-c", params.Command)
	command.Env = append(os.Environ(),
		"GCODE_SESSION_ID="+ctx.SessionID,
		"JCODE_SESSION_ID="+ctx.SessionID,
		"TMPDIR="+ScratchDir(),
		"JCODE_SCRATCH_DIR="+ScratchDir(),
		"GCODE_SCRATCH_DIR="+ScratchDir(),
	)
	if ctx.WorkingDir != "" {
		command.Dir = ctx.WorkingDir
	}
	// Own process group so a timeout kills the whole tree.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// One shared writer value: exec connects stdout and stderr to the same
	// pipe, keeping interleaving ordered and race-free.
	capture := &outputCapture{limit: bashMaxOutputLen}
	command.Stdout = capture
	command.Stderr = capture
	command.Stdin = nil

	startErr := command.Start()
	if startErr != nil {
		return toolcore.ToolOutput{}, fmt.Errorf("Failed to spawn command: %s", startErr.Error())
	}

	timeout := bashDefaultTimeoutMs
	// Omitted timeout means no timeout in upstream; a zero/negative param
	// keeps the default. We follow the schema text: omit for no timeout.
	if params.Timeout != nil {
		timeout = int(*params.Timeout)
		if timeout > bashMaxTimeoutMs {
			timeout = bashMaxTimeoutMs
		}
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	var waitErr error
	timedOut := false
	if params.Timeout == nil {
		waitErr = <-done
	} else {
		timer := time.NewTimer(time.Duration(timeout) * time.Millisecond)
		defer timer.Stop()
		select {
		case waitErr = <-done:
		case <-timer.C:
			timedOut = true
			if command.Process != nil {
				// Kill the whole process group.
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			}
			waitErr = <-done
		}
	}

	output := capture.String()
	if timedOut {
		output += "\n--- " + timeoutMessage(timeout) + " ---\n"
		return toolcore.NewToolOutput(formatCommandOutput(output, exitCodeOf(waitErr, 124))).
			WithTitle(title), nil
	}
	return toolcore.NewToolOutput(formatCommandOutput(output, exitCodeOf(waitErr, 0))).
		WithTitle(title), nil
}

// executeBackground spawns the command detached-ish with output captured to
// files and registers a task (the `bg` tool consumer arrives with the agent
// loop).
func (t *BashTool) executeBackground(params BashInput, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	display := params.Command
	if params.Intent != nil && *params.Intent != "" {
		display = *params.Intent
	}
	if len([]rune(display)) > 80 {
		display = truncateRunes(display, 80)
	}

	backgroundMu.Lock()
	backgroundSeq++
	taskID := fmt.Sprintf("bash-%d-%d", os.Getpid(), backgroundSeq)
	outputFile := filepath.Join(ScratchDir(), "tasks", taskID+".out")
	statusFile := filepath.Join(ScratchDir(), "tasks", taskID+".status")
	_ = os.MkdirAll(filepath.Dir(outputFile), 0o755)
	task := &BackgroundTask{ID: taskID, Display: display, SessionID: ctx.SessionID,
		OutputFile: outputFile, StatusFile: statusFile, StartedAt: time.Now()}
	backgroundTasks[taskID] = task
	backgroundMu.Unlock()

	command := exec.Command("bash", "-c", params.Command)
	command.Env = append(os.Environ(),
		"GCODE_SESSION_ID="+ctx.SessionID,
		"JCODE_SESSION_ID="+ctx.SessionID,
		"TMPDIR="+ScratchDir(),
		"JCODE_SCRATCH_DIR="+ScratchDir(),
		"GCODE_SCRATCH_DIR="+ScratchDir(),
	)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if ctx.WorkingDir != "" {
		command.Dir = ctx.WorkingDir
	}
	out, err := os.Create(outputFile)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	command.Stdout = out
	command.Stderr = out
	command.Stdin = nil
	if err := command.Start(); err != nil {
		_ = out.Close()
		return toolcore.ToolOutput{}, fmt.Errorf("Failed to spawn command: %s", err.Error())
	}
	go func() {
		waitErr := command.Wait()
		code := 0
		if waitErr != nil {
			code = exitCodeOf(waitErr, 1)
		}
		_, _ = out.WriteString(fmt.Sprintf("\n--- Command finished with exit code: %d ---\n", code))
		_ = out.Close()
		_ = os.WriteFile(statusFile, []byte(fmt.Sprintf("%d", code)), 0o644)
		backgroundMu.Lock()
		if task, ok := backgroundTasks[taskID]; ok {
			task.ExitCode = &code
		}
		backgroundMu.Unlock()
	}()

	body := fmt.Sprintf(
		"Started background command.\n\nTask ID: %s\nName: %s\nOutput file: %s\nStatus file: %s\n\n%s",
		taskID, display, outputFile, statusFile, bashBackgroundProgressGuidance)
	metadata, _ := json.Marshal(map[string]any{
		"task_id": taskID, "display_name": display,
		"output_file": outputFile, "status_file": statusFile,
	})
	return toolcore.ToolOutput{Output: body, Metadata: metadata}, nil
}

func exitCodeOf(err error, fallback int) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return 128 + int(status.Signal())
			}
		}
		return exitErr.ExitCode()
	}
	return fallback
}

// timeoutMessage spells out the seconds equivalent and, for suspiciously
// short timeouts, hints that the unit is milliseconds.
func timeoutMessage(timeoutMs int) string {
	secs := float64(timeoutMs) / 1000.0
	message := fmt.Sprintf("Command timed out after %dms (%.1fs)", timeoutMs, secs)
	if timeoutMs <= 5000 {
		message += ". Note: the `timeout` parameter is in MILLISECONDS, not seconds. " +
			"If you meant a longer limit, pass a larger value (e.g. 600000 = 10min) or omit `timeout`."
	}
	return message
}

// outputCapture accumulates combined stdout/stderr, capped at limit bytes
// (truncation marker appended by formatCommandOutput).
type outputCapture struct {
	limit   int
	mu      sync.Mutex
	builder strings.Builder
	written int
}

func (w *outputCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.written >= w.limit {
		return len(p), nil
	}
	remaining := w.limit - w.written
	if len(p) > remaining {
		w.builder.Write(p[:remaining])
		w.written += remaining
		return len(p), nil
	}
	w.builder.Write(p)
	w.written += len(p)
	return len(p), nil
}

func (w *outputCapture) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.builder.String()
}

// formatCommandOutput truncates on a UTF-8 boundary, appends non-zero exit
// codes, and replaces empty output.
func formatCommandOutput(output string, exitCode int) string {
	if len(output) > bashMaxOutputLen {
		output = truncateRunes(output, bashMaxOutputLen)
		output += "\n... (output truncated)"
	}
	if exitCode != 0 {
		output += fmt.Sprintf("\n\nExit code: %d", exitCode)
	}
	if trimSpace(output) == "" {
		return "Command completed successfully (no output)"
	}
	return output
}

const bashFileEditHint = "Note: this command edits files in place. Next time use `edit` " +
	"(exact replacements, all-or-nothing, pass `edits` for several), `replace` (regex or multi-file, " +
	"with expected_count), or `apply_patch` (multi-file patches). They fail loudly on a missed match " +
	"instead of silently writing nothing, and show a reviewable diff."

// fileEditHint detects shell commands that rewrite source files in place.
func fileEditHint(command string) string {
	fields := strings.Fields(command)
	compact := strings.Join(fields, " ")

	// sed -i / --in-place[=suffix]
	sedInPlace := false
	for _, segment := range strings.FieldsFunc(compact, func(r rune) bool {
		return r == '|' || r == ';' || r == '&'
	}) {
		words := strings.Fields(segment)
		// The program must be sed; the flag appears among its arguments.
		if len(words) > 1 && words[0] == "sed" {
			for _, word := range words[1:] {
				if word == "--in-place" || strings.HasPrefix(word, "--in-place=") ||
					(strings.HasPrefix(word, "-") && !strings.HasPrefix(word, "--") &&
						strings.Contains(word[1:], "i")) {
					sedInPlace = true
				}
			}
		}
	}
	// perl -pi -e / -pi -e ...
	perlInPlace := false
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "perl" && strings.HasPrefix(fields[i+1], "-") &&
			strings.Contains(fields[i+1], "i") {
			perlInPlace = true
		}
	}
	// python/node scripts writing files.
	scriptRewrite := false
	if strings.Contains(compact, "python") || strings.Contains(compact, "node ") {
		if strings.Contains(compact, ".replace(") || strings.Contains(compact, "re.sub(") {
			if strings.Contains(compact, ",'w')") || strings.Contains(compact, ",\"w)\")") ||
				strings.Contains(compact, ", 'w')") || strings.Contains(compact, ", \"w\")") ||
				strings.Contains(compact, "write_text(") || strings.Contains(compact, "writeFileSync(") {
				scriptRewrite = true
			}
		}
	}
	if sedInPlace || perlInPlace || scriptRewrite {
		return bashFileEditHint
	}
	return ""
}
