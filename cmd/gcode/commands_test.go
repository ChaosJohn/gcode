// Live-command coverage: doctor's static checks, resume listing, run usage
// error. Real provider calls are not exercised here (no network in tests).
package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/sessionstore"
)

func TestDoctorReportsEnvironment(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	t.Setenv("JCODE_HOME", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-doctor")
	code, out, errOut := runForTest(t, "doctor")
	if code != exitOK {
		t.Fatalf("doctor exit=%d out=%s err=%s", code, out, errOut)
	}
	if !strings.Contains(out, "16 registered") || !strings.Contains(out, "claude credentials") {
		t.Fatalf("doctor output: %s", out)
	}
}

func TestRunRequiresPrompt(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	code, _, errOut := runForTest(t, "run")
	if code != exitUsage || !strings.Contains(errOut, "usage: gcode run") {
		t.Fatalf("run usage: code=%d err=%s", code, errOut)
	}
	// Missing credentials must fail with a clean internal error, not a panic.
	t.Setenv("JCODE_HOME", filepath.Join(t.TempDir(), "absent"))
	runCode, _, runErr := runForTest(t, "run", "hello")
	if runCode != exitInternal || !strings.Contains(runErr, "credentials") {
		t.Fatalf("run without creds: code=%d err=%s", runCode, runErr)
	}
}

func TestResumeListsSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GCODE_HOME", home)
	store := sessionstore.NewStore(sessionstore.Session{
		ID: "live-1", Status: "active", CustomTitle: strPtrLive("Deep work")}, home)
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runForTest(t, "resume")
	if code != exitOK || !strings.Contains(out, "live-1") ||
		!strings.Contains(out, "Deep work (0 messages)") {
		t.Fatalf("resume list: code=%d out=%s", code, out)
	}
	// A concrete session id with no prompt prints the restore notice.
	code, out, _ = runForTest(t, "resume", "live-1")
	if code != exitOK || !strings.Contains(out, "Restored live-1") {
		t.Fatalf("resume restore: code=%d out=%s", code, out)
	}
	// Unknown id is a usage error.
	if code, _, _ := runForTest(t, "resume", "nope"); code != exitUsage {
		t.Fatalf("unknown session exit=%d", code)
	}
}

func strPtrLive(s string) *string { return &s }
