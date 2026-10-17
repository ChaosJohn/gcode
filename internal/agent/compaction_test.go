// Compaction tests (ported from compaction-core cases): transcript builder,
// cutoff selection with tool-pairing repair, and end-to-end auto-compact on
// a scripted context-limit error.
package agent

import (
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/types"
)

func textMessage(role types.Role, text string) types.Message {
	return types.Message{Role: role,
		Content: []types.ContentBlock{types.TextBlock(text)}}
}

func TestBuildCompactionConversationText(t *testing.T) {
	messages := []types.Message{
		textMessage(types.RoleUser, "hello"),
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.TextBlock("calling tool"),
			{Type: types.BlockToolUse, ID: strPtrC("call-1"), Name: strPtrC("echo"),
				Input: []byte(`{"text":"hi"}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.BlockToolResult, ToolUseID: strPtrC("call-1"),
				Content: strPtrC(strings.Repeat("x", 600))},
		}},
	}
	text := BuildCompactionConversationText(messages, "prior summary")
	for _, want := range []string{
		"## Previous Summary", "prior summary", "**User:**\nhello",
		"[Tool: echo - {\"text\":\"hi\"}]", "[Result: ",
		"... (truncated)", "**Assistant:**",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	// Result truncated at 500 chars.
	if !strings.Contains(text, strings.Repeat("x", 500)) ||
		strings.Contains(text, strings.Repeat("x", 501)) {
		t.Fatal("truncation length")
	}
	// Prompt wraps the transcript with the verbatim summary prompt.
	prompt := BuildCompactionPrompt(messages, "", 1<<20)
	if !strings.HasSuffix(prompt, "code snippets.") ||
		!strings.Contains(prompt, "**Context:**") {
		t.Fatal("prompt shape")
	}
}

func TestSafeCompactionCutoffToolPairing(t *testing.T) {
	toolUse := func(id string) types.Message {
		return types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.BlockToolUse, ID: strPtrC(id), Name: strPtrC("echo")}}}
	}
	toolResult := func(id string) types.Message {
		return types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.BlockToolResult, ToolUseID: strPtrC(id), Content: strPtrC("out")}}}
	}
	// Messages 0..3: user turns with results whose uses sit before the
	// naive cutoff. The cutoff must grow to include the uses.
	messages := []types.Message{
		textMessage(types.RoleUser, "t1"), toolUse("call-1"), toolResult("call-1"),
		textMessage(types.RoleUser, "t2"), toolUse("call-2"), toolResult("call-2"),
	}
	cutoff := SafeCompactionCutoff(messages, 4) // would orphan call-2's result
	if cutoff > 4 {
		t.Fatalf("cutoff %d", cutoff)
	}
	// The kept suffix [cutoff:] must contain no orphaned results.
	for _, message := range messages[cutoff:] {
		for _, block := range message.Content {
			if block.Type == types.BlockToolResult && block.ToolUseID != nil {
				found := false
				for _, prior := range messages[cutoff:] {
					for _, priorBlock := range prior.Content {
						if priorBlock.Type == types.BlockToolUse &&
							priorBlock.ID != nil && *priorBlock.ID == *block.ToolUseID {
							found = true
						}
					}
				}
				if !found {
					t.Fatalf("orphaned result %s at cutoff %d", *block.ToolUseID, cutoff)
				}
			}
		}
	}
}

func TestAutoCompactOnContextLimit(t *testing.T) {
	// Call 0 hits a context-limit error; call 1 is the summarizer; call 2
	// answers after compaction.
	prov := &scriptedProvider{
		failures: map[int]string{0: "prompt is too long: 400000 tokens > 200000 maximum context"},
	}
	prov.responses = []scriptedResponse{
		{}, // index 0: the failed turn never consumes a response
		{text: "Context: testing. What we did: filled history."},
		{text: "done after compaction"},
	}
	a := NewAgent(prov, NewRegistry())
	a.System = "sys"
	a.Messages = append(a.Messages, textMessage(types.RoleUser, "begin work"))
	for i := 0; i < 30; i++ {
		a.Messages = append(a.Messages, textMessage(types.RoleAssistant, "step"))
		a.Messages = append(a.Messages, textMessage(types.RoleUser, "continue"))
	}
	final, err := a.RunOnce("final push")
	if err != nil {
		t.Fatalf("auto-compact path failed: %v", err)
	}
	if final != "done after compaction" {
		t.Fatalf("final: %q", final)
	}
	// History was compacted: leading summary message + kept suffix.
	if len(a.Messages) == 0 || !strings.HasPrefix(
		firstText(a.Messages[0]), "## Previous Conversation Summary") {
		t.Fatalf("no summary message: %d messages", len(a.Messages))
	}
	if !strings.Contains(firstText(a.Messages[0]), "filled history") {
		t.Fatalf("summary missing: %q", firstText(a.Messages[0]))
	}
	// The final user prompt survived in the kept suffix, answered last.
	if firstText(a.Messages[len(a.Messages)-2]) != "final push" ||
		firstText(a.Messages[len(a.Messages)-1]) != "done after compaction" {
		t.Fatalf("tail: %q / %q", firstText(a.Messages[len(a.Messages)-2]),
			firstText(a.Messages[len(a.Messages)-1]))
	}
	// Three provider calls: failed turn, summarizer, post-compaction turn.
	if prov.calls != 3 {
		t.Fatalf("provider calls: %d", prov.calls)
	}
}

func firstText(message types.Message) string {
	for _, block := range message.Content {
		if block.Text != nil {
			return *block.Text
		}
	}
	return ""
}

func strPtrC(s string) *string { return &s }
