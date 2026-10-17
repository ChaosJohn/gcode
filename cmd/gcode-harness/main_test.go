package main

import (
	"bytes"
	"strings"
	"testing"
)

func runForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestVersion(t *testing.T) {
	code, out, errOut := runForTest(t, "version")
	if code != exitOK || errOut != "" {
		t.Fatalf("exit = %d stderr = %q, want %d/empty", code, errOut, exitOK)
	}
	if !strings.Contains(out, "gcode-harness") {
		t.Errorf("stdout = %q, want harness banner", out)
	}
}

func TestServeNotImplemented(t *testing.T) {
	for _, args := range [][]string{nil, {"serve"}} {
		code, _, errOut := runForTest(t, args...)
		if code != exitNotImpl {
			t.Errorf("run(%v) exit = %d, want %d", args, code, exitNotImpl)
		}
		if !strings.Contains(errOut, "M4") {
			t.Errorf("run(%v) stderr = %q, want M4 mention", args, errOut)
		}
	}
}
