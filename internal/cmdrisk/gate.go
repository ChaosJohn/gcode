package cmdrisk

import (
	"fmt"
	"strings"
)

// gate.rs port: stage 2 — the reflection gate. The harness refuses once and
// hands back a structured prompt that forces the generating model to do the
// thinking it skipped. The refusal is not satisfiable by repetition: the
// model must supply a justification naming what the user actually asked for.

// GateOutcome is what the harness should do with a tool call.
type GateOutcome int

// Outcomes.
const (
	// Allow: run it.
	Allow GateOutcome = iota
	// Reflect: refuse this attempt and make the model justify itself first.
	Reflect
	// Deny: refuse permanently. No justification unlocks this.
	Deny
)

// minJustificationLen is the minimum characters for a justification to count
// as considered.
const minJustificationLen = 25

var emptyAffirmations = map[string]bool{
	"yes": true, "ok": true, "okay": true, "sure": true, "confirmed": true,
	"proceed": true, "do it": true, "continue": true, "y": true, "approved": true,
	"go ahead": true,
}

// Justification is the model's account of which user request this serves.
type Justification struct {
	Text string
}

// IsSubstantive is intentionally a low bar: the goal is to force a reflection
// turn, not to grade prose. Anything that shows the model re-read the request
// passes; an empty string or a bare "yes" does not.
func (j Justification) IsSubstantive() bool {
	text := strings.TrimSpace(j.Text)
	if len(text) < minJustificationLen {
		return false
	}
	stripped := strings.TrimRight(strings.ToLower(text), ".!?")
	stripped = strings.TrimSpace(stripped)
	return !emptyAffirmations[stripped]
}

// Gate decides what to do with an assessed command.
func Gate(assessment Assessment, justification Justification) (GateOutcome, string) {
	switch assessment.Level {
	case Safe, Low:
		return Allow, ""
	case Catastrophic:
		return Deny, fmt.Sprintf(
			"This command is blocked and cannot be confirmed.\n\n%s\n"+
				"If the user genuinely wants this, they must run it themselves outside the agent.",
			assessment.Explanation())
	default: // Confirm
		if justification.IsSubstantive() {
			return Allow, ""
		}
		return Reflect, reflectionPrompt(assessment)
	}
}

// reflectionPrompt asks the model to compare the command against what the
// user actually asked for — a checkable claim about a specific message,
// rather than a vague appeal to good judgement.
func reflectionPrompt(assessment Assessment) string {
	return fmt.Sprintf(
		"This command was not run. It is irreversible:\n\n%s\n"+
			"Before it can proceed, stop and check it against the user's actual request:\n"+
			"- Which specific thing the user asked for requires deleting this?\n"+
			"- Did the user name this path, or did you infer it?\n"+
			"- If you inferred it, is a narrower target enough?\n"+
			"- If this is wrong, nothing here can be recovered.\n\n"+
			"If it is genuinely what the user asked for, re-issue the same call with a "+
			"`justification` field explaining which request it serves. If you are not sure, "+
			"ask the user instead: that costs one message, and being wrong costs their data.",
		assessment.Explanation())
}
