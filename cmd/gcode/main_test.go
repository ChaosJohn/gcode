package main

import (
	"bytes"
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
		"run":          "M3",
		"doctor":       "M2",
		"serve":        "M4",
		"resume":       "M4",
		"import-jcode": "M4",
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

func TestNoArgsPrintsUsageAndFails(t *testing.T) {
	code, _, errOut := runForTest(t)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "Usage:") {
		t.Errorf("stderr = %q, want usage block", errOut)
	}
}
