// Ported tests from jcode-command-risk (gate_tests.rs, paths_tests.rs,
// assess_tests.rs).
package cmdrisk

import (
	"strings"
	"testing"
)

func testCtx() RiskContext {
	return RiskContext{WorkingDir: "/home/u/proj", HomeDir: "/home/u",
		ScratchDir: "/home/u/.jcode/scratch"}
}

func level(t *testing.T, command string) RiskLevel {
	t.Helper()
	ctx := testCtx()
	return Assess(command, &ctx).Level
}

// Ported: home_directory_is_catastrophic_in_every_spelling.
func TestHomeDirectoryCatastrophicEverySpelling(t *testing.T) {
	ctx := testCtx()
	for _, spelling := range []string{"~", "$HOME", "${HOME}", "/home/u", "/home/u/",
		"/home/u/.", "/home/u/foo/..", "~/", "~/.", "/home/u/../u", "/home/./u"} {
		expanded := Expand(spelling, &ctx)
		if !IsCatastrophicTarget(expanded, &ctx) {
			t.Fatalf("%q resolved to %q and was not caught", spelling, expanded)
		}
	}
}

// Ported: parent_traversal_out_of_home_reaches_root_and_is_caught.
func TestParentTraversalReachesRoot(t *testing.T) {
	ctx := testCtx()
	for _, spelling := range []string{"~/../..", "/home/u/../..", "~/../../", "/home/u/../../"} {
		expanded := Expand(spelling, &ctx)
		if expanded != "/" {
			t.Fatalf("%q expanded to %q", spelling, expanded)
		}
		if !IsCatastrophicTarget(expanded, &ctx) {
			t.Fatalf("%q not caught", spelling)
		}
	}
}

// Ported: root_and_system_paths_are_catastrophic + credential stores.
func TestProtectedPaths(t *testing.T) {
	ctx := testCtx()
	for _, p := range []string{"/", "/etc", "/usr", "/var", "/boot", "/home", "/Users"} {
		if !IsCatastrophicTarget(p, &ctx) {
			t.Fatalf("%s must be protected", p)
		}
	}
	for _, sub := range []string{".ssh", ".gnupg", ".aws", ".kube", ".docker"} {
		if !IsCatastrophicTarget("/home/u/"+sub, &ctx) ||
			!IsCatastrophicTarget("/home/u/"+sub+"/id_ed25519", &ctx) {
			t.Fatalf("credential store %s not protected", sub)
		}
	}
	// Ordinary project paths and single files inside config roots are fine.
	if IsCatastrophicTarget("/home/u/proj", &ctx) {
		t.Fatalf("project must not be protected")
	}
	if IsCatastrophicTarget("/home/u/.config/app/stale.toml", &ctx) {
		t.Fatalf("single file inside config root must not be catastrophic")
	}
	if !IsCatastrophicTarget("/home/u/.config", &ctx) {
		t.Fatalf("config root itself must be protected")
	}
	if !IsCatastrophicTarget("/home/u", &ctx) {
		t.Fatalf("home itself must be protected")
	}
}

// Ported: the_issue_604_command_is_blocked_outright + root destruction.
func TestIssue604CommandsBlocked(t *testing.T) {
	for _, command := range []string{"rm -rf ~", "rm -rf $HOME", "rm -rf \"$HOME\"",
		"rm -rf ${HOME}", "rm -rf /home/u", "rm -fr ~/",
		"/bin/rm -rf ~", "rm -rf ~/*", "rm -rf /", "rm -rf /*", "rm -rf /etc",
		"rm --recursive --force /usr"} {
		if got := level(t, command); got != Catastrophic {
			t.Fatalf("%q → %v, want Catastrophic", command, got)
		}
	}
}

// Ported: credential_theft_or_destruction_is_blocked.
func TestCredentialDestructionBlocked(t *testing.T) {
	for _, command := range []string{"rm -rf ~/.ssh", "rm -rf ~/.gnupg"} {
		if got := level(t, command); got != Catastrophic {
			t.Fatalf("%q → %v", command, got)
		}
	}
}

// Ported: chaining_does_not_bypass_the_gate.
func TestChainingDoesNotBypass(t *testing.T) {
	if level(t, "echo starting && rm -rf ~") != Catastrophic {
		t.Fatalf("&& chain")
	}
	if level(t, "cd /tmp; rm -rf $HOME") != Catastrophic {
		t.Fatalf("; chain")
	}
	if level(t, "true || rm -rf /") != Catastrophic {
		t.Fatalf("|| chain")
	}
}

// Ported: routine_cleanup_inside_the_project_is_not_blocked.
func TestRoutineCleanupNotBlocked(t *testing.T) {
	for _, command := range []string{"rm -rf target", "rm -rf ./node_modules",
		"rm -f Cargo.lock", "rm -rf /home/u/proj/build", "rm -rf /tmp/scratch",
		"cargo clean", "ls -la", "git status", "echo hello > out.txt"} {
		if !level(t, command).RunsImmediately() {
			t.Fatalf("%q should run without a reflection turn, got %v", command, level(t, command))
		}
	}
}

// Ported: non_rm_destructive_tools_are_covered + redirect semantics.
func TestOtherDestructiveTools(t *testing.T) {
	if level(t, "find /home/u -delete") != Catastrophic {
		t.Fatalf("find -delete")
	}
	if level(t, "dd if=/dev/zero of=/dev/sda").RunsImmediately() {
		t.Fatalf("dd to device node must not run immediately")
	}
	if level(t, "shred /home/u/other/secrets.txt") != Low {
		t.Fatalf("shred outside cwd → Low")
	}
	if level(t, "echo line >> /home/u/other/log.txt").RunsImmediately() == false {
		t.Fatalf("append redirect must run")
	}
	if got := level(t, "cat report > /home/u/other/final.md"); got != Low {
		t.Fatalf("truncating redirect outside cwd → Low, got %v", got)
	}
}

// Ported: runtime_computed_targets_require_justification + no visible target.
func TestRuntimeTargetsAndUnknownTargets(t *testing.T) {
	if level(t, "rm -rf $TARGET") < Confirm {
		t.Fatalf("$TARGET")
	}
	if level(t, "rm -rf $(cat list.txt)") < Confirm {
		t.Fatalf("$(cat list.txt)")
	}
	if level(t, "rm -rf") < Confirm {
		t.Fatalf("rm -rf with no target")
	}
	if level(t, "cat list.txt | xargs rm -rf") < Confirm {
		t.Fatalf("piped targets")
	}
}

// Ported: issue_922 + heredoc_inside_double_quoted_command_substitution.
func TestHeredocPayloadsAreData(t *testing.T) {
	for _, command := range []string{
		"cat >> log.md <<'EOF'\nO(n^2) time and O(n) space\nEOF",
		"cat > helper.sh <<'EOF'\n#!/bin/bash\nrm -f \"$SOME_VAR\"\nEOF",
		"echo \"$(cat <<'EOF'\n`\"p>\"` y\nEOF\n)\"",
		"gh api repos/o/r/pulls -f body=\"$(cat <<'EOF'\nRan `tool resume X \"<pointer>\"` ok.\nEOF\n)\"",
		"git commit -m \"$(cat <<'EOF'\nSummary with `rm -rf ~` in prose\nEOF\n)\"",
	} {
		if !level(t, command).RunsImmediately() {
			t.Fatalf("%q must not trip the gate", command)
		}
	}
	if level(t, "cat <<'EOF'\nrm -rf ~\nEOF\nrm -rf ~") != Catastrophic {
		t.Fatalf("real command after heredoc")
	}
	if level(t, "echo \"$(cat <<'EOF'\ninert\nEOF\n)\"; rm -rf ~") != Catastrophic {
		t.Fatalf("command after substitution")
	}
	// `<<` inside single quotes is still not a heredoc.
	if level(t, "echo '$(cat <<EOF'\nrm -rf ~\nEOF") != Catastrophic {
		t.Fatalf("single-quoted << must stay source")
	}
}

// Ported: sh -c inline scripts are assessed too.
func TestShellInlineScript(t *testing.T) {
	if level(t, "sh -c \"rm -rf ~\"") != Catastrophic {
		t.Fatalf("sh -c payload")
	}
	if level(t, "sudo rm -rf ~") != Catastrophic {
		t.Fatalf("sudo unwrapping")
	}
	if level(t, "cat list.txt | xargs rm -rf") < Confirm {
		t.Fatalf("xargs fed by a pipe")
	}
}

// Ported: assessment_is_total_over_garbage_input.
func TestAssessmentTotalOverGarbage(t *testing.T) {
	for _, command := range []string{"", "   ", "'''", "\"\"\"", "((((", "$(", "~", "!!"} {
		_ = Assess(command, &RiskContext{WorkingDir: "/home/u/proj", HomeDir: "/home/u", ScratchDir: "/home/u/.jcode/scratch"}) // must not panic
	}
}

// Ported: explanation_names_the_offending_target.
func TestExplanationNamesTarget(t *testing.T) {
	assessment := Assess("rm -rf /home/u/other/important", &RiskContext{WorkingDir: "/home/u/proj", HomeDir: "/home/u", ScratchDir: "/home/u/.jcode/scratch"})
	if !strings.Contains(assessment.Explanation(), "/home/u/other/important") {
		t.Fatalf("explanation: %s", assessment.Explanation())
	}
}

// Ported: safe_commands_pass_straight_through / reflection prompts /
// justification handling.
func TestGateOutcomes(t *testing.T) {
	ctx := RiskContext{WorkingDir: "/home/u/proj", HomeDir: "/home/u"}
	none := Justification{}
	if outcome, _ := Gate(Assess("cargo test", &ctx), none); outcome != Allow {
		t.Fatalf("safe command: %v", outcome)
	}
	if outcome, _ := Gate(Assess("rm -rf target", &ctx), none); outcome != Allow {
		t.Fatalf("bounded cleanup: %v", outcome)
	}
	outcome, reason := Gate(Assess("rm -rf ~", &ctx), Justification{Text: "the user explicitly asked for this cleanup in their last message"})
	if outcome != Deny || !strings.Contains(reason, "cannot be confirmed") {
		t.Fatalf("catastrophic with justification: %v %s", outcome, reason)
	}
	outcome, prompt := Gate(Assess("rm -rf $TARGET", &ctx), none)
	if outcome != Reflect || !strings.Contains(prompt, "user's actual") ||
		!strings.Contains(prompt, "justification") || !strings.Contains(prompt, "ask the user") {
		t.Fatalf("reflect prompt: %v %s", outcome, prompt)
	}
	// A blind retry does not satisfy the gate.
	outcome2, prompt2 := Gate(Assess("rm -rf $TARGET", &ctx), none)
	if outcome2 != Reflect || prompt2 != prompt {
		t.Fatalf("retry must reflect identically")
	}
	for _, affirmation := range []string{"yes", "ok", "sure", "confirmed", "proceed", "do it", "y", "approved", "go ahead", ""} {
		if (Justification{Text: affirmation}).IsSubstantive() {
			t.Fatalf("affirmation %q must be rejected", affirmation)
		}
	}
	outcome, _ = Gate(Assess("rm -rf $TARGET", &ctx),
		Justification{Text: "the user's last message asked to clear the data directory"})
	if outcome != Allow {
		t.Fatalf("substantive justification: %v", outcome)
	}
}
