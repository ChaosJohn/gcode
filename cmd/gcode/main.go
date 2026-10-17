// Command gcode is the pure-Go replica of the jcode CLI.
//
// Milestone map (docs/port/PORT_PLAN.md):
//
//	version/help      — M0 (this file)
//	doctor            — M2 (provider diagnostics)
//	run               — M3 (headless single-shot agent run)
//	serve / resume /
//	import-jcode      — M4 (sessions, storage, jcode interop)
//	(default: TUI)    — M7
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ChaosJohn/gcode/internal/version"
)

// Exit codes, stable across milestones so scripts can rely on them.
const (
	exitOK       = 0
	exitUsage    = 2 // bad invocation, unknown command
	exitNotImpl  = 3 // command exists in the plan but not yet ported
	exitInternal = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type command struct {
	name      string // bare match token, no placeholders
	synopsis  string // display form, may include argument placeholders
	summary   string
	milestone string // milestone that implements it; "" = available now
}

var commands = []command{
	{name: "version", synopsis: "version", summary: "print build information", milestone: ""},
	{name: "run", synopsis: "run <prompt>", summary: "headless single-shot agent run", milestone: "M3"},
	{name: "resume", synopsis: "resume [session]", summary: "resume a stored session", milestone: "M4"},
	{name: "serve", synopsis: "serve", summary: "harness API server mode", milestone: "M4"},
	{name: "import-jcode", synopsis: "import-jcode", summary: "import an existing ~/.jcode home", milestone: "M4"},
	{name: "doctor", synopsis: "doctor", summary: "provider and environment diagnostics", milestone: "M2"},
	{name: "help", synopsis: "help", summary: "show this help", milestone: ""},
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "%s: interactive TUI lands in milestone M7; see docs/port/PORT_PLAN.md\n", version.Name)
		usage(stderr)
		return exitUsage
	}

	switch cmd, rest := args[0], args[1:]; cmd {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.Banner(version.Name))
		return exitOK
	case "help", "--help", "-h":
		usage(stdout)
		return exitOK
	case "run", "resume", "serve", "import-jcode", "doctor":
		c := lookup(cmd)
		fmt.Fprintf(stderr, "gcode %s: not implemented yet — planned for %s (docs/port/PORT_PLAN.md)\n", cmd, c.milestone)
		if len(rest) > 0 && cmd == "run" {
			fmt.Fprintf(stderr, "(received prompt: %q)\n", rest[0])
		}
		return exitNotImpl
	default:
		fmt.Fprintf(stderr, "gcode: unknown command %q\n\n", cmd)
		usage(stderr)
		return exitUsage
	}
}

func lookup(name string) command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
	}
	return command{}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "Usage: %s <command> [args]\n\nCommands:\n", version.Name)
	for _, c := range commands {
		status := ""
		if c.milestone != "" {
			status = fmt.Sprintf("   (planned: %s)", c.milestone)
		}
		fmt.Fprintf(w, "  %-16s %s%s\n", c.synopsis, c.summary, status)
	}
	fmt.Fprintf(w, "\nPure-Go replica of jcode (no CGO). Baseline: jcode v0.98.1 — docs/port/BASELINE.md\n")
}
