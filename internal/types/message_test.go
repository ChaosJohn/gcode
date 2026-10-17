package types

import (
	"encoding/json"
	"testing"
)

// Golden shapes: each variant must re-serialize byte-identically to what
// serde emits for the corresponding Rust enum variant (field order matters).
func TestContentBlockGoldenJSON(t *testing.T) {
	text := "hello"
	cc := CacheControl{Kind: "ephemeral"}
	thinking := "signed thinking"
	signature := "sigBASE64=="
	id := "rs_1"
	enc := "enc=="
	status := "completed"
	name := "bash"
	input := json.RawMessage(`{"command":"ls"}`)
	toolID := "toolu_01"
	content := "output"
	isErr := true
	media := "image/png"
	data := "iVBORw0KGgo="
	toolName := "websearch"
	provider := "anthropic"
	item := json.RawMessage(`{"blob":"verbatim"}`)
	sig := "SIG"

	cases := map[string]struct {
		block ContentBlock
		want  string
	}{
		"text":            {ContentBlock{Type: BlockText, Text: &text}, `{"type":"text","text":"hello"}`},
		"text+cache":      {ContentBlock{Type: BlockText, Text: &text, CacheControl: &cc}, `{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}`},
		"reasoning":       {ContentBlock{Type: BlockReasoning, Text: &text}, `{"type":"reasoning","text":"hello"}`},
		"reasoning_trace": {ContentBlock{Type: BlockReasoningTrace, Text: &text}, `{"type":"reasoning_trace","text":"hello"}`},
		"anthropic_thinking": {ContentBlock{Type: BlockAnthropicThink, Thinking: &thinking, Signature: &signature},
			`{"type":"anthropic_thinking","thinking":"signed thinking","signature":"sigBASE64=="}`},
		"openai_reasoning": {ContentBlock{Type: BlockOpenAIReasoning, ID: &id, Summary: []string{"a"}, EncryptedContent: &enc, Status: &status},
			`{"type":"openai_reasoning","id":"rs_1","summary":["a"],"encrypted_content":"enc==","status":"completed"}`},
		"tool_use": {ContentBlock{Type: BlockToolUse, ID: &id, Name: &name, Input: input, ThoughtSignature: &sig},
			`{"type":"tool_use","id":"rs_1","name":"bash","input":{"command":"ls"},"thought_signature":"SIG"}`},
		"tool_result": {ContentBlock{Type: BlockToolResult, ToolUseID: &toolID, Content: &content, IsError: &isErr},
			`{"type":"tool_result","tool_use_id":"toolu_01","content":"output","is_error":true}`},
		"tool_result_ok": {ContentBlock{Type: BlockToolResult, ToolUseID: &toolID, Content: &content},
			`{"type":"tool_result","tool_use_id":"toolu_01","content":"output"}`},
		"image": {ContentBlock{Type: BlockImage, MediaType: &media, Data: &data},
			`{"type":"image","media_type":"image/png","data":"iVBORw0KGgo="}`},
		"openai_compaction": {ContentBlock{Type: BlockOpenAICompaction, EncryptedContent: &enc},
			`{"type":"openai_compaction","encrypted_content":"enc=="}`},
		"tool_reference": {ContentBlock{Type: BlockToolReference, ToolUseID: &toolID, ToolName: &toolName},
			`{"type":"tool_reference","tool_use_id":"toolu_01","tool_name":"websearch"}`},
		"provider_native": {ContentBlock{Type: BlockProviderNative, Provider: &provider, Item: item},
			`{"type":"provider_native","provider":"anthropic","item":{"blob":"verbatim"}}`},
	}

	for label, tc := range cases {
		got, err := json.Marshal(tc.block)
		if err != nil {
			t.Fatalf("%s: marshal: %v", label, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", label, got, tc.want)
		}
		// Round-trip must be stable.
		var back ContentBlock
		if err := json.Unmarshal(got, &back); err != nil {
			t.Fatalf("%s: unmarshal: %v", label, err)
		}
		again, _ := json.Marshal(back)
		if string(again) != tc.want {
			t.Errorf("%s: round-trip:\n got %s\nwant %s", label, again, tc.want)
		}
	}
}

func TestContentBlockUnknownTypeRejected(t *testing.T) {
	var b ContentBlock
	// serde errors on unknown variants too (no #[serde(other)]).
	if err := json.Unmarshal([]byte(`{"type":"hologram","text":"x"}`), &b); err == nil {
		t.Fatal("unknown block type must error")
	}
}

func TestContentBlockSummaryNilBecomesEmptyArray(t *testing.T) {
	var b ContentBlock
	if err := json.Unmarshal([]byte(`{"type":"openai_reasoning","id":"r","summary":null}`), &b); err != nil {
		t.Fatal(err)
	}
	if b.Summary == nil || len(b.Summary) != 0 {
		t.Fatalf("summary = %#v, want empty non-nil slice", b.Summary)
	}
	out, _ := json.Marshal(b)
	if string(out) != `{"type":"openai_reasoning","id":"r","summary":[]}` {
		t.Fatalf("got %s", out)
	}
}

// Ported upstream: absent_signature_stays_absent.
func TestAbsentSignatureStaysAbsent(t *testing.T) {
	call := ToolCall{ID: "toolu_3", Name: "read", Input: json.RawMessage(`{}`)}
	block := call.ToToolUseBlock()
	out, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"type":"tool_use","id":"toolu_3","name":"read","input":{}}` {
		t.Fatalf("got %s", out)
	}
}

// Ported upstream: to_tool_use_block_preserves_thought_signature.
func TestToToolUseBlockPreservesThoughtSignature(t *testing.T) {
	sig := "SIG_ABC"
	call := ToolCall{ID: "toolu_1", Name: "websearch", Input: json.RawMessage(`{"query":"water testing"}`), ThoughtSignature: &sig}
	block := call.ToToolUseBlock()
	if block.ID == nil || *block.ID != "toolu_1" || block.Name == nil || *block.Name != "websearch" {
		t.Fatalf("bad block: %+v", block)
	}
	if block.ThoughtSignature == nil || *block.ThoughtSignature != "SIG_ABC" {
		t.Fatalf("signature must be carried onto the stored block: %+v", block)
	}
}

// Ported upstream: thought_signature_round_trips_through_session_json.
func TestThoughtSignatureRoundTripsThroughSessionJSON(t *testing.T) {
	sig := "SIG_PERSIST"
	block := ToolCall{ID: "toolu_2", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`), ThoughtSignature: &sig}.ToToolUseBlock()
	encoded, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	var restored ContentBlock
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ThoughtSignature == nil || *restored.ThoughtSignature != "SIG_PERSIST" {
		t.Fatalf("signature lost: %+v", restored.ThoughtSignature)
	}
}

// Ported upstream: resolve_tool_name tests.
func TestResolveToolName(t *testing.T) {
	cases := map[string]string{
		"functions.bash":       "bash",
		"functions.shell_exec": "bash",
		"functions.file_grep":  "agentgrep",
		"mcp.functions.bash":   "mcp.functions.bash",
		"Read":                 "read",
		"Bash":                 "bash",
		"Write":                "write",
		"Edit":                 "edit",
		"multiedit":            "edit",
		"MultiEdit":            "edit",
		"patch":                "apply_patch",
		"Grep":                 "agentgrep",
		"Agent":                "subagent",
		"ScheduleWakeup":       "schedule",
		"Skill":                "skill_manage",
		"functions.Read":       "read",
		"communicate":          "swarm",
		"task":                 "subagent",
		"task_runner":          "subagent",
		"launch":               "open",
		"shell":                "bash",
		"read_file":            "read",
		"file_read":            "read",
		"write_file":           "write",
		"file_write":           "write",
		"edit_file":            "edit",
		"file_edit":            "edit",
		"multi_edit":           "edit",
		"Patch":                "apply_patch",
		"grep":                 "agentgrep",
		"file_grep":            "agentgrep",
		"skill":                "skill_manage",
		"discover_tools":       "integration_tools",
		"todoread":             "todo",
		"todowrite":            "todo",
		"todo_read":            "todo",
		"todo_write":           "todo",
		"todos":                "todo",
		"bash":                 "bash",
	}
	for in, want := range cases {
		if got := ResolveToolName(in); got != want {
			t.Errorf("ResolveToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Ported upstream: suffix_correction_preserves_unicode_prefix_and_rejects_stale_snapshots.
func TestReplaceTextSuffix(t *testing.T) {
	text := "先前 🦀. raw citation"
	offset, ok := ReplaceTextSuffix(&text, "raw citation", "[Source](https://a.example)")
	if !ok {
		t.Fatal("expected replacement")
	}
	want := len("先前 🦀. ")
	if offset != want {
		t.Errorf("offset = %d, want %d", offset, want)
	}
	if text != "先前 🦀. [Source](https://a.example)" {
		t.Errorf("text = %q", text)
	}
	before := text
	if _, ok := ReplaceTextSuffix(&text, "raw citation", "wrong"); ok {
		t.Error("stale snapshot must be rejected")
	}
	if _, ok := ReplaceTextSuffix(&text, "", "wrong"); ok {
		t.Error("empty old_text must be rejected")
	}
	if text != before {
		t.Errorf("rejected replacements must not mutate: %q", text)
	}
}

func TestToolCallValidation(t *testing.T) {
	if err := (ToolCall{Name: "bash", Input: json.RawMessage(`{"x":1}`)}).ValidationError(); err != nil {
		t.Errorf("valid call rejected: %v", err)
	}
	err := (ToolCall{Input: json.RawMessage(`{}`)}).ValidationError()
	if err == nil || err.Error() != "Invalid tool call: tool name must not be empty." {
		t.Errorf("empty name: %v", err)
	}
	err = (ToolCall{Name: "bash", Input: json.RawMessage(`[1,2]`)}).ValidationError()
	want := "Invalid tool call for 'bash': arguments must be a JSON object, got array."
	if err == nil || err.Error() != want {
		t.Errorf("non-object input: %v, want %q", err, want)
	}
}

func TestToolCallInputParsing(t *testing.T) {
	if got := ParseStreamedInputToObject("   "); string(got) != "{}" {
		t.Errorf("empty: %s", got)
	}
	if got := ParseStreamedInputToObject(`{"a":1}`); string(got) != `{"a":1}` {
		t.Errorf("object kept: %s", got)
	}
	if got := ParseStreamedInputToObject(`[1,2]`); string(got) != `[1,2]` {
		t.Errorf("array kept verbatim: %s", got)
	}
	if got := ParseStreamedInputToObject(`not json`); string(got) != "null" {
		t.Errorf("unparseable → null: %s", got)
	}
	if got := NormalizeInputToObject(json.RawMessage(`[1]`)); string(got) != "{}" {
		t.Errorf("normalize non-object: %s", got)
	}
	if got := NormalizeInputToObject(json.RawMessage(`{"a":1}`)); string(got) != `{"a":1}` {
		t.Errorf("normalize object passthrough: %s", got)
	}
}

func TestIntentFromInput(t *testing.T) {
	intent, ok := IntentFromInput(json.RawMessage(`{"intent":"  fix bug  "}`))
	if !ok || intent != "fix bug" {
		t.Errorf("got %q ok=%v", intent, ok)
	}
	if _, ok := IntentFromInput(json.RawMessage(`{"intent":"   "}`)); ok {
		t.Error("blank intent must be dropped")
	}
	if _, ok := IntentFromInput(json.RawMessage(`{}`)); ok {
		t.Error("missing intent must be absent")
	}
	call := ToolCall{Input: json.RawMessage(`{"intent":"do it"}`)}
	call.RefreshIntentFromInput()
	if call.Intent == nil || *call.Intent != "do it" {
		t.Errorf("refresh: %+v", call.Intent)
	}
}

func TestSanitizeToolID(t *testing.T) {
	cases := map[string]string{
		"":               "unknown",
		"toolu_01-abc":   "toolu_01-abc",
		"call.1":         "call_1",
		"weird/id\\path": "weird_id_path",
		"space name":     "space_name",
	}
	for in, want := range cases {
		if got := SanitizeToolID(in); got != want {
			t.Errorf("SanitizeToolID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[uint64]string{
		0: "0ms", 999: "999ms", 1000: "1.0s", 1500: "1.5s",
		9999: "10.0s", 10000: "10s", 59999: "59s", 60000: "1m",
		61000: "1m 1s", 120000: "2m", 3661000: "61m 1s",
	}
	for in, want := range cases {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestStableHash(t *testing.T) {
	// FNV-1a reference vectors.
	if got := StableHashBytes([]byte("")); got != 0xcbf29ce484222325 {
		t.Errorf("empty hash = %#x", got)
	}
	if got := StableHashBytes([]byte("a")); got != 0xaf63dc4c8601ec8c {
		t.Errorf("'a' hash = %#x", got)
	}
	// extend_stable_hash matches hashing the two little-endian halves.
	a, b := uint64(1), uint64(2)
	var buf [16]byte
	for i := 0; i < 8; i++ {
		buf[i] = byte(a >> (8 * i))
		buf[8+i] = byte(b >> (8 * i))
	}
	if ExtendStableHash(a, b) != StableHashBytes(buf[:]) {
		t.Error("ExtendStableHash mismatch")
	}
}

func TestRoleValidation(t *testing.T) {
	var r Role
	if err := json.Unmarshal([]byte(`"user"`), &r); err != nil || r != RoleUser {
		t.Errorf("user: %v %q", err, r)
	}
	if err := json.Unmarshal([]byte(`"system"`), &r); err == nil {
		t.Error("unknown role must error (serde has no other)")
	}
}
