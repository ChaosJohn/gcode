package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/types"
)

func msg(role types.Role, content ...types.ContentBlock) types.Message {
	return types.Message{Role: role, Content: content}
}

func txt(s string) types.ContentBlock { return types.ContentBlock{Type: types.BlockText, Text: &s} }

func use(id, name string) types.ContentBlock {
	return types.ContentBlock{Type: types.BlockToolUse, ID: &id, Name: &name, Input: json.RawMessage(`{}`)}
}

// Ported: test_orphaned_tool_result_is_rewritten_as_text.
func TestOrphanedToolResultRewrittenAsText(t *testing.T) {
	messages := []types.Message{
		msg(types.RoleUser, txt("go")),
		msg(types.RoleAssistant, use("tool_a", "bash")),
		msg(types.RoleUser, tr("tool_a", "ok")),
		msg(types.RoleUser, tr("tool_ghost", "no leftovers")),
	}
	formatted := FormatMessagesWithTools(messages, false, nil)
	last := formatted[len(formatted)-1]
	if last.Role != "user" {
		t.Fatalf("role = %s", last.Role)
	}
	if tr, ok := last.Content[0].(ToolResultContentBlockMsg); !ok || tr.ToolUseID != "tool_a" {
		t.Errorf("first block = %+v", last.Content[0])
	}
	for _, block := range last.Content {
		if tr, ok := block.(ToolResultContentBlockMsg); ok && tr.ToolUseID == "tool_ghost" {
			t.Error("ghost result must be rewritten as text")
		}
	}
	found := false
	for _, block := range last.Content {
		if text, ok := block.(TextContent); ok &&
			strings.Contains(text.Text, "tool_ghost") && strings.Contains(text.Text, "no leftovers") {
			found = true
		}
	}
	if !found {
		t.Errorf("rewritten text must keep tool_ghost content: %+v", last.Content)
	}
}

// Ported: test_displaced_tool_result_still_answers_tool_use_in_place.
func TestDisplacedToolResultAnswersInPlace(t *testing.T) {
	messages := []types.Message{
		msg(types.RoleUser, txt("go")),
		msg(types.RoleAssistant, use("tool_x", "bash")),
		msg(types.RoleUser, txt("are you there?")),
		msg(types.RoleAssistant, txt("yes")),
		msg(types.RoleUser, tr("tool_x", "late output")),
	}
	formatted := FormatMessagesWithTools(messages, false, nil)

	if formatted[1].Role != "assistant" {
		t.Fatalf("formatted[1].role = %s", formatted[1].Role)
	}
	next := formatted[2]
	if next.Role != "user" {
		t.Fatalf("formatted[2].role = %s", next.Role)
	}
	if tr, ok := next.Content[0].(ToolResultContentBlockMsg); !ok || tr.ToolUseID != "tool_x" || tr.IsError {
		t.Errorf("real output must lead unerrored: %+v", next.Content[0])
	}
	found := false
	for _, block := range next.Content {
		if text, ok := block.(TextContent); ok && text.Text == "are you there?" {
			found = true
		}
	}
	if !found {
		t.Errorf("interjected text must survive: %+v", next.Content)
	}
	dump := mustJSON(t, formatted)
	if !strings.Contains(dump, "late output") {
		t.Errorf("dump missing late output: %s", dump)
	}
	if strings.Contains(dump, "Recovered orphaned tool output") {
		t.Errorf("paired result must not be rewritten: %s", dump)
	}
	assertToolUsesAnswered(t, formatted)
}

// Ported: test_no_repair_when_tool_results_present.
func TestNoRepairWhenResultsPresent(t *testing.T) {
	messages := []types.Message{
		msg(types.RoleUser, txt("Hello")),
		msg(types.RoleAssistant, use("tool_123", "bash")),
		msg(types.RoleUser, tr("tool_123", "file1.txt\nfile2.txt")),
	}
	formatted := FormatMessagesWithTools(messages, false, nil)
	if len(formatted) != 3 {
		t.Fatalf("formatted = %d", len(formatted))
	}
	last := formatted[2]
	if tr, ok := last.Content[0].(ToolResultContentBlockMsg); !ok ||
		tr.Content.Text == nil || !strings.Contains(*tr.Content.Text, "file1.txt") {
		t.Errorf("last block = %+v", last.Content[0])
	}
}

// Ported: test_parallel_image_tool_results_stay_contiguous.
func TestParallelImageToolResultsStayContiguous(t *testing.T) {
	makeImageResult := func(id, label string) types.Message {
		media := "image/png"
		data := "AAAA"
		return types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
			tr(id, "Image: "+label),
			{Type: types.BlockImage, MediaType: &media, Data: &data},
			txt("[Attached image associated with the preceding tool result: " + label + "]"),
		}}
	}
	messages := []types.Message{
		msg(types.RoleAssistant, use("tool_a", "read"), use("tool_b", "read"), use("tool_c", "read")),
		makeImageResult("tool_a", "a.png"),
		makeImageResult("tool_b", "b.png"),
		makeImageResult("tool_c", "c.png"),
	}
	formatted := FormatMessagesWithTools(messages, false, nil)
	if len(formatted) != 2 {
		t.Fatalf("formatted = %d", len(formatted))
	}
	user := formatted[1]
	if user.Role != "user" {
		t.Fatalf("role = %s", user.Role)
	}
	seen := map[string]bool{}
	for _, block := range user.Content {
		tr, ok := block.(ToolResultContentBlockMsg)
		if !ok {
			t.Fatalf("non-tool_result block in merged user message: %T", block)
		}
		seen[tr.ToolUseID] = true
		hasImage, hasLabel := false, false
		if tr.Content.Text != nil {
			hasLabel = strings.Contains(*tr.Content.Text, "Image: ")
		}
		for _, b := range tr.Content.Blocks {
			if b.Type == "image" {
				hasImage = true
			}
			if b.Type == "text" && b.Text != nil && strings.Contains(*b.Text, "[Attached image") {
				hasLabel = true
			}
		}
		if !hasImage {
			t.Errorf("%s must carry its image", tr.ToolUseID)
		}
		if !hasLabel {
			t.Errorf("%s must fold its attachment label", tr.ToolUseID)
		}
	}
	for _, id := range []string{"tool_a", "tool_b", "tool_c"} {
		if !seen[id] {
			t.Errorf("missing %s", id)
		}
	}
}

// Ported: test_dangling_tool_use_repair (request-shaping half).
func TestDanglingToolUseRepair(t *testing.T) {
	messages := []types.Message{
		msg(types.RoleUser, txt("run it")),
		msg(types.RoleAssistant, use("tool_d", "bash")),
		// The session was interrupted before the result was persisted.
	}
	formatted := FormatMessagesWithTools(messages, false, nil)
	if len(formatted) != 3 {
		t.Fatalf("formatted = %d", len(formatted))
	}
	synthetic := formatted[2]
	if synthetic.Role != "user" {
		t.Fatalf("synthetic role = %s", synthetic.Role)
	}
	tr, ok := synthetic.Content[0].(ToolResultContentBlockMsg)
	if !ok || tr.ToolUseID != "tool_d" || !tr.IsError {
		t.Fatalf("synthetic result = %+v", synthetic.Content[0])
	}
	if tr.Content.Text == nil || !strings.Contains(*tr.Content.Text, "interrupted") {
		t.Errorf("synthetic text = %+v", tr.Content.Text)
	}
	assertToolUsesAnswered(t, formatted)
}

// assertToolUsesAnswered mirrors upstream's helper: roles alternate and every
// tool_use is answered by a tool_result in the immediately following message.
func assertToolUsesAnswered(t *testing.T, formatted []ApiMessage) {
	t.Helper()
	for i := 1; i < len(formatted); i++ {
		if formatted[i-1].Role == formatted[i].Role {
			t.Errorf("roles must alternate at %d", i)
		}
	}
	for i, m := range formatted {
		if m.Role != "assistant" {
			continue
		}
		for _, block := range m.Content {
			tu, ok := block.(ToolUseContent)
			if !ok {
				continue
			}
			if i+1 >= len(formatted) {
				t.Fatalf("tool_use %s has no next message", tu.ID)
			}
			next := formatted[i+1]
			if next.Role != "user" {
				t.Errorf("tool_use %s not followed by user", tu.ID)
			}
			answered := false
			for _, nb := range next.Content {
				if tr, ok := nb.(ToolResultContentBlockMsg); ok && tr.ToolUseID == tu.ID {
					answered = true
				}
			}
			if !answered {
				t.Errorf("tool_use %s not answered in message %d", tu.ID, i+1)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	encoded, err := MarshalOrdered(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
