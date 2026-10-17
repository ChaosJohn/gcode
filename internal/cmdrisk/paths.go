package cmdrisk

import (
	"path/filepath"
	"strings"
)

// paths.rs port: the safety-critical core. Written to be absolute and
// simple: a small set of paths that may never be recursively destroyed,
// compared after normalization, with no dependence on correctly understanding
// the surrounding command.

// Credential stores, protected recursively: destroying a single private key
// inside ~/.ssh is as damaging as destroying the directory.
var protectedCredentialSubpaths = []string{".ssh", ".gnupg", ".aws", ".kube", ".docker"}

// Directories whose wholesale destruction is unacceptable, but whose
// individual files are legitimately edited all the time. Matched exactly.
var protectedHomeSubpaths = []string{
	".config", ".jcode", ".gcode", ".claude", ".local", ".local/share",
	"Documents", "Desktop",
}

// Absolute system paths that must never be recursively destroyed.
var protectedSystemPaths = []string{
	"/", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/opt", "/proc",
	"/root", "/sbin", "/srv", "/sys", "/usr", "/var", "/Applications",
	"/System", "/Library", "/Users", "/home",
}

// System paths where the contents are as critical as the directory itself.
var systemPathsProtectedRecursively = []string{
	"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/sbin",
	"/sys", "/usr", "/var/lib", "/System", "/Library",
}

// Expand resolves `~`, `$HOME`, and relative paths into something
// comparable. Lexical only: never touches the filesystem.
func Expand(raw string, ctx *RiskContext) string {
	text := expandKnownPrefix(raw, ctx)
	// Never normalize unresolved substitutions away (e.g. `$UNKNOWN/..`).
	if strings.ContainsAny(text, "$`") {
		return text
	}
	if filepath.IsAbs(text) {
		return normalizePath(text)
	}
	if ctx.WorkingDir != "" {
		return normalizePath(filepath.Join(ctx.WorkingDir, text))
	}
	return text
}

// expandKnownPrefix expands only complete, known leading variables. Prefix
// lookalikes such as `$HOME_BACKUP` remain unresolved.
func expandKnownPrefix(raw string, ctx *RiskContext) string {
	for name, value := range map[string]string{
		"HOME":              ctx.HomeDir,
		"JCODE_SCRATCH_DIR": ctx.ScratchDir,
	} {
		if value == "" {
			continue
		}
		for _, prefix := range []string{"$" + name, "${" + name + "}"} {
			if rest, ok := strings.CutPrefix(raw, prefix); ok &&
				(rest == "" || strings.HasPrefix(rest, "/")) {
				return value + rest
			}
		}
	}
	if ctx.HomeDir != "" {
		if raw == "~" {
			return ctx.HomeDir
		}
		if rest, ok := strings.CutPrefix(raw, "~/"); ok {
			return ctx.HomeDir + "/" + rest
		}
	}
	return raw
}

// normalizePath lexically removes `.` and `..` so `/home/u/../..` is seen as
// `/`. Intentionally not symlink-aware (defense in depth, not correctness).
func normalizePath(path string) string {
	if path == "" {
		return "/"
	}
	parts := strings.Split(path, "/")
	var out []string
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, part)
		}
	}
	joined := "/" + strings.Join(out, "/")
	if path != "" && !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "~") {
		// Relative input with no working dir: keep relative shape.
		joined = strings.Join(out, "/")
	}
	if joined == "" {
		return "/"
	}
	return joined
}

// IsCatastrophicTarget reports whether destroying this path is categorically
// unacceptable — the single most important predicate in the crate.
func IsCatastrophicTarget(path string, ctx *RiskContext) bool {
	path = normalizePath(path)

	// Exact system roots, plus anything inside the ones whose contents are as
	// unrecoverable as the directory itself.
	for _, protected := range protectedSystemPaths {
		if path == protected {
			return true
		}
	}
	for _, protected := range systemPathsProtectedRecursively {
		if path == protected || strings.HasPrefix(path, protected+"/") {
			return true
		}
	}

	if ctx.HomeDir == "" {
		return false
	}
	home := normalizePath(ctx.HomeDir)

	if path == home {
		return true
	}
	for _, sub := range protectedCredentialSubpaths {
		if path == home+"/"+sub || strings.HasPrefix(path, home+"/"+sub+"/") {
			return true
		}
	}
	for _, sub := range protectedHomeSubpaths {
		if path == home+"/"+sub {
			return true
		}
	}
	return false
}

// ClassifyTarget classifies one resolved target, returning a finding when it
// is notable.
func ClassifyTarget(expanded, raw string, recursive bool, ctx *RiskContext) *RiskFinding {
	// Glob and variable expansion we did not perform: we cannot know the
	// footprint, so escalate rather than guess.
	if (strings.Contains(raw, "*") || strings.Contains(raw, "?")) &&
		!strings.ContainsAny(expandKnownPrefix(raw, ctx), "$`") {
		// A bare `/*` or `~/*` is catastrophic in effect even though no single
		// resolved path is protected.
		parent := filepath.Dir(expanded)
		if filepath.Base(expanded) == "*" && IsCatastrophicTarget(parent, ctx) {
			return &RiskFinding{Level: Catastrophic,
				Reason: "would destroy the entire contents of a protected directory",
				Target: raw}
		}
		// A glob in a filename cannot escape its concrete parent directory.
		if !strings.ContainsAny(parent, "*?") {
			insideCwd := ctx.WorkingDir != "" &&
				strings.HasPrefix(parent, normalizePath(ctx.WorkingDir))
			if insideCwd || isTempPath(parent) {
				return &RiskFinding{Level: Low,
					Reason: "glob is bounded to the working or temporary directory",
					Target: raw}
			}
		}
		return &RiskFinding{Level: Confirm,
			Reason: "target contains a glob, so the exact set of affected files is not known before execution",
			Target: raw}
	}

	// Check the resolved path first: `$HOME` expands to a protected path and
	// must be denied outright, not merely queried.
	if IsCatastrophicTarget(expanded, ctx) {
		return &RiskFinding{Level: Catastrophic,
			Reason: "targets a protected system or home path that must never be destroyed",
			Target: expanded}
	}

	if strings.ContainsAny(expandKnownPrefix(raw, ctx), "$`") {
		return &RiskFinding{Level: Confirm,
			Reason: "target is computed at runtime (variable or command substitution), so its value cannot be checked in advance",
			Target: raw}
	}

	// Raw device nodes are never a safe write target.
	if strings.HasPrefix(expanded, "/dev") &&
		!strings.HasPrefix(expanded, "/dev/null") &&
		!strings.HasPrefix(expanded, "/dev/stdout") &&
		!strings.HasPrefix(expanded, "/dev/stderr") {
		return &RiskFinding{Level: Catastrophic,
			Reason: "writes directly to a device node, which can destroy a filesystem or disk",
			Target: expanded}
	}

	insideCwd := ctx.WorkingDir != "" &&
		strings.HasPrefix(expanded, normalizePath(ctx.WorkingDir))
	if insideCwd {
		// The routine case: an agent tidying up its own workspace.
		if recursive {
			return &RiskFinding{Level: Low,
				Reason: "recursive delete inside the working directory", Target: expanded}
		}
		return nil
	}

	if isTempPath(expanded) {
		return nil
	}

	// A concrete path outside the workspace is visible in risk logs without
	// requiring a justification retry.
	return &RiskFinding{Level: Low,
		Reason: "destructive operation targets a concrete path outside the working directory",
		Target: expanded}
}

// isTempPath: temp directories are conventionally disposable.
func isTempPath(path string) bool {
	for _, prefix := range []string{"/tmp", "/var/tmp", "/private/tmp"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
