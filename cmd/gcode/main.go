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
	"path/filepath"

	"github.com/ChaosJohn/gcode/internal/sessionstore"
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
	{name: "run", synopsis: "run <prompt>", summary: "headless single-shot agent run", milestone: ""},
	{name: "resume", synopsis: "resume [session] [prompt]", summary: "list sessions or continue one", milestone: ""},
	{name: "serve", synopsis: "serve", summary: "harness API server mode", milestone: "M4"},
	{name: "import-jcode", synopsis: "import-jcode [jcode-home]", summary: "import an existing ~/.jcode home", milestone: ""},
	{name: "doctor", synopsis: "doctor", summary: "provider and environment diagnostics", milestone: ""},
	{name: "mcp-login", synopsis: "mcp-login <server-url>", summary: "authorize an MCP remote server (OAuth + PKCE)", milestone: ""},
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
	case "import-jcode":
		return runImportJcode(rest, stdout, stderr)
	case "run":
		return runRun(rest, stdout, stderr)
	case "resume":
		return runResume(rest, stdout, stderr)
	case "doctor":
		return runDoctor(rest, stdout, stderr)
	case "mcp-login":
		return runMcpLogin(rest, stdout, stderr)
	case "serve":
		c := lookup(cmd)
		fmt.Fprintf(stderr, "gcode %s: not implemented yet — planned for %s (docs/port/PORT_PLAN.md)\n", cmd, c.milestone)
		return exitNotImpl
	default:
		fmt.Fprintf(stderr, "gcode: unknown command %q\n\n", cmd)
		usage(stderr)
		return exitUsage
	}
}

// runImportJcode copies jcode sessions into the gcode home (M4 interop).
func runImportJcode(args []string, stdout, stderr io.Writer) int {
	source := os.Getenv("JCODE_HOME")
	if source == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(stderr, "gcode import-jcode: %v\n", err)
			return exitInternal
		}
		source = filepath.Join(home, ".jcode")
	}
	if len(args) > 0 {
		source = args[0]
	}
	if info, err := os.Stat(source); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "gcode import-jcode: no jcode home at %s\n", source)
		return exitUsage
	}
	imported, skipped, err := sessionstore.ImportJcode(source, sessionstore.HomeDir())
	if err != nil {
		fmt.Fprintf(stderr, "gcode import-jcode: %v\n", err)
		return exitInternal
	}
	fmt.Fprintf(stdout, "Imported %d session(s) from %s (%d already present, skipped).\n",
		imported, source, skipped)
	return exitOK
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
