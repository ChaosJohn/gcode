// Command gcode-harness will serve the harness API (the jcode-harness
// equivalent): a local server backed by internal/base/storage so external
// clients can list and drive gcode sessions.
//
// Implemented in milestone M4 (docs/port/PORT_PLAN.md); until then the
// binary builds and reports its version, and exits with code 3 for any
// serve attempt.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ChaosJohn/gcode/internal/version"
)

const (
	exitOK      = 0
	exitUsage   = 2
	exitNotImpl = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Fprintln(stdout, version.Banner("gcode-harness"))
			return exitOK
		case "help", "--help", "-h":
			fmt.Fprintln(stdout, "Usage: gcode-harness [serve|version]\nserve starts the harness API server (planned: M4).")
			return exitOK
		}
	}
	fmt.Fprintln(stderr, "gcode-harness serve: not implemented yet — planned for M4 (docs/port/PORT_PLAN.md)")
	return exitNotImpl
}
