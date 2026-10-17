package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/version"
)

func runForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestVersionCommand(t *testing.T) {
	code, out, errOut := runForTest(t, "version")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want empty", errOut)
	}
	if !strings.Contains(out, version.Name) || !strings.Contains(out, "pure-Go/no-cgo") {
		t.Errorf("stdout = %q, want banner with name and no-cgo marker", out)
	}
}

func TestHelpAliases(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		code, _, errOut := runForTest(t, arg)
		if code != exitOK || errOut != "" {
			t.Errorf("run(%q) exit = %d stderr = %q, want 0/empty", arg, code, errOut)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, errOut := runForTest(t, "definitely-not-a-command")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Errorf("stderr = %q, want unknown-command message", errOut)
	}
}

// Planned commands must fail with the not-implemented code and name the
// milestone, so scripts can distinguish "not ported yet" from misuse.
func TestPlannedCommandsReportMilestone(t *testing.T) {
	cases := map[string]string{
		"serve": "M4",
	}
	for cmd, milestone := range cases {
		code, _, errOut := runForTest(t, cmd)
		if code != exitNotImpl {
			t.Errorf("run(%q) exit = %d, want %d", cmd, code, exitNotImpl)
		}
		if !strings.Contains(errOut, milestone) {
			t.Errorf("run(%q) stderr = %q, want mention of milestone %s", cmd, errOut, milestone)
		}
	}
}

// import-jcode is live as of M4 batch 2: no jcode home is a usage error,
// and a real import succeeds end to end.
func TestImportJcodeCommand(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	t.Setenv("JCODE_HOME", filepath.Join(t.TempDir(), "absent"))
	code, out, _ := runForTest(t, "import-jcode")
	if code != exitUsage {
		t.Fatalf("missing home exit = %d, want %d", code, exitUsage)
	}
	source := t.TempDir()
	sessions := filepath.Join(source, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "x1.json"),
		[]byte(`{"id":"x1","status":"active","messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runForTest(t, "import-jcode", source)
	if code != exitOK || !strings.Contains(out, "Imported 1 session(s)") {
		t.Fatalf("import: code=%d out=%q", code, out)
	}
	// Re-import skips the already-present id.
	code, out, _ = runForTest(t, "import-jcode", source)
	if code != exitOK || !strings.Contains(out, "skipped") {
		t.Fatalf("re-import: code=%d out=%q", code, out)
	}
}

func TestNoArgsPrintsUsageAndFails(t *testing.T) {
	code, _, errOut := runForTest(t)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "Usage:") {
		t.Errorf("stderr = %q, want usage block", errOut)
	}
}
