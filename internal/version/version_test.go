package version

import (
	"strings"
	"testing"
)

func TestBanner(t *testing.T) {
	Version, Commit, Date = "9.9.9-test", "abc1234", "2026-10-17T00:00:00Z"
	defer func() { Version, Commit, Date = "0.0.0-dev", "none", "unknown" }()

	b := Banner("gcode")
	for _, want := range []string{"gcode 9.9.9-test", "commit abc1234", "built 2026-10-17T00:00:00Z", "pure-Go/no-cgo"} {
		if !strings.Contains(b, want) {
			t.Errorf("Banner() = %q, want substring %q", b, want)
		}
	}
}
