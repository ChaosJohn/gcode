// Tool-markup recovery tests (ported from tool_markup_recovery_tests.rs
// plus agent-loop end-to-end over a scripted provider).
package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/types"
)

func recoveredNames(recovery TextRecovery) []string {
	names := make([]string, 0, len(recovery.Calls))
	for _, call := range recovery.Calls {
		names = append(names, call.Name)
	}
	return names
}

// The canonical DSML envelope with the full-width prefix.
func TestScanDSMLEnvelope(t *testing.T) {
	text := `<｜DSML｜function_calls>
<｜DSML｜invoke name="bash">
<｜DSML｜parameter name="command" string="true">ls -la</｜DSML｜parameter>
</｜DSML｜invoke>
</｜DSML｜function_calls>`
	recovery := ScanToolCallMarkup(text)
	if recovery.Status != "recovered" || len(recovery.Calls) != 1 ||
		recovery.Calls[0].Name != "bash" {
		t.Fatalf("scan: %+v", recovery)
	}
	if string(recovery.Calls[0].Arguments) != `{"command":"ls -la"}` {
		t.Fatalf("arguments: %s", string(recovery.Calls[0].Arguments))
	}
	if strings.Contains(recovery.Sanitized, "invoke") {
		t.Fatalf("sanitized: %q", recovery.Sanitized)
	}
}

// Mangling variants: missing prefix, bare invoke, tool_calls wrapper.
func TestScanMangledVariants(t *testing.T) {
	variants := []string{
		`<invoke name="read"><parameter name="file_path">a.go</parameter></invoke>`,
		`<tool_calls><invoke name="read"><parameter name="file_path">a.go</parameter></invoke></tool_calls>`,
		`<DSML invoke name="read"><DSML parameter name="file_path">a.go</DSML parameter></DSML invoke>`,
	}
	for _, text := range variants {
		recovery := ScanToolCallMarkup(text)
		if recovery.Status != "recovered" ||
			strings.Join(recoveredNames(recovery), ",") != "read" {
			t.Fatalf("variant: %+v for %q", recovery, text)
		}
	}
}

// Parameter value typing: string hint, JSON literals, unparsed strings.
func TestScanParameterTyping(t *testing.T) {
	text := `<invoke name="edit">
<parameter name="count">3</parameter>
<parameter name="label" string="true">3</parameter>
<parameter name="flag">true</parameter>
<parameter name="raw">[not json</parameter>
<parameter name="quoted">"keep"</parameter>
</invoke>`
	recovery := ScanToolCallMarkup(text)
	if recovery.Status != "recovered" {
		t.Fatalf("scan: %+v", recovery)
	}
	var args map[string]any
	if err := json.Unmarshal(recovery.Calls[0].Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if args["count"] != float64(3) {
		t.Fatalf("count: %v (%T)", args["count"], args["count"])
	}
	if args["label"] != "3" {
		t.Fatalf("label: %v", args["label"])
	}
	if args["flag"] != true {
		t.Fatalf("flag: %v", args["flag"])
	}
	if args["raw"] != "[not json" {
		t.Fatalf("raw: %v", args["raw"])
	}
	if args["quoted"] != `"keep"` {
		t.Fatalf("quoted: %v", args["quoted"])
	}
}

// Markup inside fenced code blocks and single stray tags stay prose.
func TestScanIgnoresFencesAndStrayTags(t *testing.T) {
	fenced := "Example usage:\n```xml\n<invoke name=\"bash\">\n</invoke>\n```\n"
	if recovery := ScanToolCallMarkup(fenced); recovery.Status != "none" {
		t.Fatalf("fenced: %+v", recovery)
	}
	stray := "Use <invoke name=\"bash\"> in your reply."
	if recovery := ScanToolCallMarkup(stray); recovery.Status != "none" {
		t.Fatalf("stray: %+v", recovery)
	}
}

// Broken envelopes report a reason and never execute.
func TestScanUnparseable(t *testing.T) {
	cases := []struct{ text, reason string }{
		{`<invoke name="bash"><parameter name="x">1</parameter>`,
			"unterminated invoke"},
		{`<invoke><parameter name="x">1</parameter></invoke>`,
			"invoke without a tool name"},
		{`<parameter name="x">1</parameter><parameter name="y">2</parameter>`,
			"parameter outside of an invoke block"},
		{`<invoke name="bash"><parameter name="x">1</invoke>`,
			"unterminated parameter"},
	}
	for _, item := range cases {
		recovery := ScanToolCallMarkup(item.text)
		if recovery.Status != "unparseable" ||
			!strings.Contains(recovery.Reason, item.reason) {
			t.Fatalf("%q: %+v", item.text, recovery)
		}
	}
}

// Prose around the envelope is preserved in the sanitized text.
func TestScanSanitizedKeepsProse(t *testing.T) {
	text := "Let me check that.\n<invoke name=\"ls\"><parameter name=\"path\">.</parameter></invoke>\nDone soon."
	recovery := ScanToolCallMarkup(text)
	if recovery.Status != "recovered" {
		t.Fatalf("scan: %+v", recovery)
	}
	if !strings.HasPrefix(recovery.Sanitized, "Let me check that.") ||
		!strings.HasSuffix(recovery.Sanitized, "Done soon.") {
		t.Fatalf("sanitized: %q", recovery.Sanitized)
	}
}

// The to=functions.NAME marker form.
func TestParseTextWrappedCall(t *testing.T) {
	text := `I'll run it now. to=functions.bash {"command":"pwd","timeout":5}`
	recovery := RecoverTextToolCalls(text)
	if recovery.Status != "recovered" || len(recovery.Calls) != 1 ||
		recovery.Calls[0].Name != "bash" {
		t.Fatalf("recovery: %+v", recovery)
	}
	var args map[string]any
	if err := json.Unmarshal(recovery.Calls[0].Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if args["command"] != "pwd" || args["timeout"] != float64(5) {
		t.Fatalf("args: %s", string(recovery.Calls[0].Arguments))
	}
	if recovery.Sanitized != "I'll run it now." {
		t.Fatalf("sanitized: %q", recovery.Sanitized)
	}
}

// End-to-end: a scripted model drifts into DSML text on turn 1; the agent
// recovers the call, executes the real tool, and finishes.
func TestAgentRecoversMarkupToolCall(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&echoTool{})
	prov := &scriptedProvider{responses: []scriptedResponse{
		{ // Turn 1: markup envelope instead of a structured tool call.
			text: "<｜DSML｜invoke name=\"echo\">\n" +
				"<｜DSML｜parameter name=\"text\" string=\"true\">RECOVERED-OK</｜DSML｜parameter>\n" +
				"</｜DSML｜invoke>",
		},
		{}, // The tool round consumed this; final turn:
		{text: "done"},
	}}
	a := NewAgent(prov, registry)
	final, err := a.RunOnce("run echo for me")
	if err != nil {
		t.Fatal(err)
	}
	if final != "done" {
		t.Fatalf("final: %q", final)
	}
	// The recovered call executed: a real tool result is in the transcript.
	found := false
	for _, message := range a.Messages {
		for _, block := range message.Content {
			if block.Type == types.BlockToolResult && block.Content != nil &&
				strings.Contains(*block.Content, "echoed:") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("recovered call never executed")
	}
}

// Unparseable markup nudges the model (max twice) and then ends the turn.
func TestAgentNudgesOnUnparseableMarkup(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&echoTool{})
	broken := "<invoke name=\"echo\"><parameter name=\"text\">echo x"
	prov := &scriptedProvider{responses: []scriptedResponse{
		{text: broken},
		{text: broken},
		{text: broken},
	}}
	a := NewAgent(prov, registry)
	final, err := a.RunOnce("do it")
	if err != nil {
		t.Fatal(err)
	}
	// Nudges exhausted: the turn ends with the raw (unexecuted) text.
	if final != broken {
		t.Fatalf("final: %q", final)
	}
	// Two reminder nudges were appended before the model complied.
	nudges := 0
	for _, message := range a.Messages {
		if message.Role == types.RoleUser && len(message.Content) > 0 &&
			message.Content[0].Text != nil &&
			strings.Contains(*message.Content[0].Text, "<system-reminder>Your previous response contained a tool call written as plain-text markup") {
			nudges++
		}
	}
	if nudges != 2 {
		t.Fatalf("nudges: %d", nudges)
	}
}
