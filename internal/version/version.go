// Package version carries build metadata shared by all gcode binaries.
// Values are injected at build time via -ldflags -X; see the Makefile.
package version

import (
	"fmt"
	"runtime"
)

// Name is the canonical program name used in banners and help text.
const Name = "gcode"

// Overridable build metadata.
var (
	Version = "0.0.0-dev"
	Commit  = "none"
	Date    = "unknown"
)

// Banner renders the one-line identity of a gcode binary. The
// "pure-Go/no-cgo" marker is part of the contract: any build that quietly
// enables CGO violates the project constraint (docs/port/DECISIONS.md D5),
// so the banner always advertises the build discipline.
func Banner(binary string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s, %s, pure-Go/no-cgo)",
		binary, Version, Commit, Date, runtime.Version())
}
