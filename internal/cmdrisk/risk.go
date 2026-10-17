// Package cmdrisk ports jcode-command-risk: the bash destructive-command
// gate. Stage 1 (assess) decides whether a command deserves scrutiny; stage
// 2 (gate) decides what happens next. The catastrophic tier is absolute and
// simple: a small set of paths that may never be recursively destroyed,
// compared after lexical normalization.
package cmdrisk

import (
	"strings"
)

// RiskLevel classifies one finding.
type RiskLevel int

// Risk levels, ordered by severity.
const (
	// Safe: no destructive potential detected.
	Safe RiskLevel = iota
	// Low: destructive but bounded (inside the working directory, recoverable
	// via git, or under a temp dir). Runs, but is recorded.
	Low
	// Confirm: the target cannot be determined statically. Requires the model
	// to re-justify against the user's actual request before running.
	Confirm
	// Catastrophic: would destroy home, root, or credentials. Never runs, and
	// no amount of model justification can unlock it.
	Catastrophic
)

// RunsImmediately reports whether execution may proceed without reflection.
func (l RiskLevel) RunsImmediately() bool { return l == Safe || l == Low }

// IsAbsoluteDeny reports whether no confirmation could unlock this.
func (l RiskLevel) IsAbsoluteDeny() bool { return l == Catastrophic }

// RiskFinding is one reason a command was flagged.
type RiskFinding struct {
	Level  RiskLevel
	Reason string
	// Target is the concrete path or argument that triggered this.
	Target string
}

// RiskContext carries the blast-radius inputs; the crate does no I/O of its
// own beyond lexical path inspection.
type RiskContext struct {
	WorkingDir string
	HomeDir    string
	ScratchDir string
}

// Assessment is the full verdict for one command.
type Assessment struct {
	Level    RiskLevel
	Findings []RiskFinding
}

func (a Assessment) fromFindings(findings []RiskFinding) Assessment {
	level := Safe
	for _, finding := range findings {
		if finding.Level > level {
			level = finding.Level
		}
	}
	return Assessment{Level: level, Findings: findings}
}

// Explanation renders the findings for the model.
func (a Assessment) Explanation() string {
	var out strings.Builder
	for _, finding := range a.Findings {
		out.WriteString("- ")
		out.WriteString(finding.Reason)
		if finding.Target != "" {
			out.WriteString(" (target: ")
			out.WriteString(finding.Target)
			out.WriteString(")")
		}
		out.WriteString("\n")
	}
	return out.String()
}

// Destructive commands: destroy data as their primary purpose. Presence here
// does not by itself mean danger — `rm` inside the working directory is
// routine. It means "inspect the targets".
var destructiveCommands = map[string]bool{
	"rm": true, "rmdir": true, "shred": true, "unlink": true, "truncate": true,
	"dd": true, "mkfs": true, "fdisk": true, "parted": true, "wipefs": true, "srm": true,
}

// Wrapper commands run another command; the real program is one of their
// arguments, so `sudo rm -rf ~` must be unwrapped before classification.
var wrapperCommands = map[string]bool{
	"sudo": true, "doas": true, "env": true, "nice": true, "ionice": true,
	"time": true, "timeout": true, "nohup": true, "xargs": true, "command": true,
	"builtin": true, "exec": true, "setsid": true, "stdbuf": true, "chroot": true,
	"su": true, "watch": true, "eval": true,
}

// Shell grammar words that may prefix the actual command in a segment.
var shellControlPrefixes = map[string]bool{
	"then": true, "do": true, "else": true, "elif": true, "if": true, "while": true,
	"until": true, "case": true, "in": true, "select": true,
}

// Wrapper-specific flags that consume the following word.
func wrapperFlagTakesValue(wrapper, flag string) bool {
	switch wrapper {
	case "sudo", "doas":
		return flag == "-u" || flag == "--user" || flag == "-g" || flag == "--group" || flag == "-C"
	case "env":
		return flag == "-u" || flag == "--unset" || flag == "-C" || flag == "--chdir"
	case "nice":
		return flag == "-n" || flag == "--adjustment"
	case "ionice":
		return flag == "-c" || flag == "--class" || flag == "-n" || flag == "--classdata" ||
			flag == "-p" || flag == "--pid"
	case "timeout":
		return flag == "-s" || flag == "--signal" || flag == "-k" || flag == "--kill-after"
	case "xargs":
		return flag == "-n" || flag == "--max-args" || flag == "-P" || flag == "--max-procs" ||
			flag == "-s" || flag == "--max-chars"
	case "chroot":
		return flag == "--userspec" || flag == "--groups"
	default:
		return false
	}
}

// Shells take their program from a string argument we cannot parse reliably.
var shellCommands = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
}

// Conditionally destructive: destructive only with specific flags.
var conditionallyDestructive = map[string][]string{
	"git":   {"clean"},
	"chmod": {"-R"},
	"chown": {"-R"},
}

// Assess is the crate's entry point and is intentionally total: any input,
// including garbage, produces an assessment rather than an error.
func Assess(command string, ctx *RiskContext) Assessment {
	var findings []RiskFinding
	// Keep the trusted context for literal protected paths, but never approve
	// commands whose own assignments can invalidate known-variable expansion.
	if strings.Contains(command, "HOME=") || strings.Contains(command, "JCODE_SCRATCH_DIR=") {
		findings = append(findings, RiskFinding{Level: Confirm,
			Reason: "command reassigns a path variable used by risk assessment"})
	}

	for _, segment := range SplitSegments(command) {
		assessSegment(segment, ctx, &findings)
	}

	// `"$(...)"`, backticks and unquoted heredoc bodies run commands the word
	// tokenizer flattens into literals (#1753). Each body is strictly shorter
	// than its parent, so the recursion terminates.
	for _, source := range EmbeddedSubstitutions(command) {
		for _, finding := range Assess(source, ctx).Findings {
			if !containsFinding(findings, finding) {
				findings = append(findings, finding)
			}
		}
	}

	if len(findings) == 0 {
		return Assessment{Level: Safe}
	}
	return Assessment{}.fromFindings(findings)
}

func containsFinding(findings []RiskFinding, finding RiskFinding) bool {
	for _, existing := range findings {
		if existing == finding {
			return true
		}
	}
	return false
}

func assessSegment(tokens []Token, ctx *RiskContext, findings *[]RiskFinding) {
	// Shell redirects happen even when a wrapper only prints information.
	for _, target := range tokens {
		if !target.IsTruncatingRedirectTarget {
			continue
		}
		if !isSafeRedirectSink(target.Text) {
			expanded := Expand(target.Text, ctx)
			if finding := ClassifyTarget(expanded, target.Text, false, ctx); finding != nil {
				*findings = append(*findings, *finding)
			}
		}
	}
	// Strip shell grammar prefixes so the destructive verb underneath is the
	// one we classify.
	for len(tokens) > 1 && shellControlPrefixes[tokens[0].Text] {
		tokens = tokens[1:]
	}

	wrappedBy := ""
	for {
		if len(tokens) == 0 {
			// Ran off the end while unwrapping: the payload is invisible.
			if wrappedBy != "" {
				*findings = append(*findings, RiskFinding{Level: Confirm,
					Reason: "`" + wrappedBy + "` runs another command that could not be identified statically"})
			}
			return
		}
		name := tokens[0].Basename()
		if !wrapperCommands[name] {
			break
		}
		// `command -v/-V` describes names, including names of wrappers.
		if name == "command" {
			describes := false
			for _, token := range tokens[1:] {
				if !(token.IsFlag() && token.Text != "--") {
					break
				}
				if !strings.HasPrefix(token.Text, "--") && strings.ContainsAny(token.Text[1:], "vV") {
					describes = true
					break
				}
			}
			if describes {
				return
			}
		}
		wrappedBy = name
		// Skip the wrapper plus its own options and VAR=value assignments,
		// landing on the wrapped program.
		rest := tokens[1:]
		idx := 0
		for idx < len(rest) {
			token := rest[idx]
			if name == "env" && (strings.HasPrefix(token.Text, "--split-string") ||
				(strings.HasPrefix(token.Text, "-") && !strings.HasPrefix(token.Text, "--") &&
					strings.Contains(token.Text, "S"))) {
				*findings = append(*findings, RiskFinding{Level: Confirm,
					Reason: "`env` split-string payload cannot be identified statically"})
				return
			}
			if token.IsOperator || strings.Contains(token.Text, "=") {
				idx++
				continue
			}
			if token.IsFlag() {
				idx++
				if wrapperFlagTakesValue(name, token.Text) && idx < len(rest) {
					idx++
				}
				continue
			}
			// A bare number is an operand of the wrapper itself.
			if allDigitsOrDots(token.Text) {
				idx++
				continue
			}
			break
		}
		if name == "env" && idx == len(rest) {
			return
		}
		tokens = rest[idx:]
	}

	if len(tokens) == 0 {
		if wrappedBy != "" {
			*findings = append(*findings, RiskFinding{Level: Confirm,
				Reason: "`" + wrappedBy + "` runs another command that could not be identified statically"})
		}
		return
	}
	program := tokens[0]
	programName := program.Basename()

	if programName == "find" {
		assessFind(tokens, ctx, findings)
		return
	}

	// A shell invoked with an inline script is opaque; assess the script text
	// too, so `sh -c "rm -rf ~"` is not a free pass.
	if shellCommands[programName] {
		for _, token := range tokens[1:] {
			if token.IsFlag() {
				continue
			}
			for _, segment := range SplitSegments(token.Text) {
				assessSegment(segment, ctx, findings)
			}
		}
		return
	}

	triggered := destructiveCommands[programName]
	if !triggered {
		if flags, ok := conditionallyDestructive[programName]; ok {
			for _, token := range tokens {
				for _, flag := range flags {
					if token.Text == flag {
						triggered = true
						break
					}
				}
			}
		}
	}
	if !triggered {
		return
	}

	var targets []Token
	for _, token := range tokens[1:] {
		if !token.IsFlag() && !token.IsOperator && !token.IsTruncatingRedirectTarget {
			targets = append(targets, token)
		}
	}

	// A destructive command fed by a pipe takes its operands from the
	// previous command's output, which we cannot enumerate.
	if program.ReceivesPipe {
		*findings = append(*findings, RiskFinding{Level: Confirm,
			Reason: "`" + programName + "` deletes paths supplied by a pipe, so the set of affected files cannot be checked before it runs"})
	}
	// A destructive program with no parsable target is more suspicious, not
	// less.
	if len(targets) == 0 {
		*findings = append(*findings, RiskFinding{Level: Confirm,
			Reason: "`" + programName + "` is destructive but its target could not be determined statically, so its blast radius is unknown"})
		return
	}

	// Flags belonging to a read-only command are not deletion flags. In
	// particular, the `r` in `find -printf` must not turn a redirect into a
	// recursive deletion.
	recursive := false
	for _, token := range tokens {
		if token.IsRecursiveFlag() {
			recursive = true
			break
		}
	}

	for _, target := range targets {
		// `dd`-style key=value operands hide the path from a naive scan.
		raw := target.Text
		if key, value, found := strings.Cut(target.Text, "="); found &&
			(key == "of" || key == "if" || key == "seek" || key == "conv") {
			raw = value
		}
		expanded := Expand(raw, ctx)
		if finding := ClassifyTarget(expanded, raw, recursive, ctx); finding != nil {
			*findings = append(*findings, *finding)
		}
	}
}

func allDigitsOrDots(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		if (ch < '0' || ch > '9') && ch != '.' {
			return false
		}
	}
	return true
}

// assessFind handles find actions: search roots are deletion targets only
// for -delete or a destructive action, not for read-only inspection.
func assessFind(tokens []Token, ctx *RiskContext, findings *[]RiskFinding) {
	var roots []Token
	rest := tokens[1:]
	idx := 0
	for idx < len(rest) && (rest[idx].Text == "-H" || rest[idx].Text == "-L" ||
		rest[idx].Text == "-P" || rest[idx].Text == "--") {
		idx++
	}
	for ; idx < len(rest); idx++ {
		token := rest[idx]
		if token.IsFlag() || token.IsOperator || token.Text == "!" || token.Text == "(" ||
			token.IsTruncatingRedirectTarget {
			break
		}
		roots = append(roots, token)
	}
	classifyRoots := func() {
		raws := make([]string, 0, len(roots)+1)
		for _, root := range roots {
			raws = append(raws, root.Text)
		}
		if len(roots) == 0 {
			raws = append(raws, ".")
		}
		for _, raw := range raws {
			expanded := Expand(raw, ctx)
			if finding := ClassifyTarget(expanded, raw, true, ctx); finding != nil {
				*findings = append(*findings, *finding)
			}
		}
	}

	index := 1
	for index < len(tokens) {
		switch tokens[index].Text {
		case "-delete":
			classifyRoots()
		case "-fprint", "-fprint0", "-fprintf", "-fls":
			if index+1 < len(tokens) {
				target := tokens[index+1]
				if !isSafeRedirectSink(target.Text) {
					expanded := Expand(target.Text, ctx)
					if finding := ClassifyTarget(expanded, target.Text, false, ctx); finding != nil {
						*findings = append(*findings, *finding)
					}
				}
			} else {
				*findings = append(*findings, RiskFinding{Level: Confirm,
					Reason: "`find` output target cannot be identified statically"})
			}
			if tokens[index].Text == "-fprintf" {
				index++
			}
		case "-name", "-iname", "-path", "-ipath", "-wholename", "-iwholename",
			"-regex", "-iregex", "-lname", "-ilname", "-printf":
			// These predicates consume literal data, not another action.
			index++
		case "-exec", "-execdir", "-ok", "-okdir":
			start := index + 1
			end := len(tokens)
			for offset, token := range tokens[start:] {
				if token.Text == ";" || token.Text == "+" {
					end = start + offset
					break
				}
			}
			payload := tokens[start:end]
			if len(payload) == 0 || !readOnlyFindPayload(payload) || end == len(tokens) {
				assessSegment(payload, ctx, findings)
				for _, token := range payload {
					if strings.Contains(token.Text, "{}") {
						raws := make([]string, 0, len(roots)+1)
						for _, root := range roots {
							raws = append(raws, root.Text)
						}
						if len(roots) == 0 {
							raws = append(raws, ".")
						}
						for _, raw := range raws {
							substituted := make([]Token, len(payload))
							for i, sub := range payload {
								sub.Text = strings.ReplaceAll(sub.Text, "{}", raw)
								substituted[i] = sub
							}
							assessSegment(substituted, ctx, findings)
						}
						break
					}
				}
				*findings = append(*findings, RiskFinding{Level: Confirm,
					Reason: "`find` action may modify files and its full effects cannot be determined statically"})
			}
			index = end
		}
		index++
	}
}

func readOnlyFindPayload(tokens []Token) bool {
	if len(tokens) == 0 {
		return false
	}
	switch tokens[0].Basename() {
	case "cat", "readlink":
		return true
	case "sed":
		// Sed can execute commands or write files even without -i. Recognize
		// only simple print scripts rather than guessing about arbitrary code.
		var args []string
		for _, token := range tokens[1:] {
			args = append(args, token.Text)
		}
		scriptIndex := 0
		if len(args) > 0 && args[0] == "-n" {
			scriptIndex = 1
		}
		if scriptIndex >= len(args) {
			return false
		}
		script := args[scriptIndex]
		if !strings.HasSuffix(script, "p") {
			return false
		}
		for _, ch := range script[:len(script)-1] {
			if (ch < '0' || ch > '9') && ch != ',' && ch != '$' {
				return false
			}
		}
		for _, arg := range args[scriptIndex+1:] {
			if strings.HasPrefix(arg, "-") {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// isSafeRedirectSink: conventional bit buckets are safe *redirect*
// destinations. They remain protected when explicitly passed to a destructive
// command such as `rm`.
func isSafeRedirectSink(raw string) bool {
	switch raw {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "NUL":
		return true
	default:
		return false
	}
}
