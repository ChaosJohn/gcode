package toolcore

import (
	"encoding/json"
	"testing"
)

// Ported: resolve_tool_name_strips_function_namespace_before_alias_resolution
// + pascalcase + merge aliases.
func TestResolveToolName(t *testing.T) {
	cases := [][2]string{
		{"functions.bash", "bash"},
		{"functions.shell_exec", "bash"},
		{"functions.file_grep", "agentgrep"},
		{"functions.Read", "read"},
		{"shell", "bash"},
		{"read_file", "read"},
		{"multiedit", "edit"},
		{"MultiEdit", "edit"},
		{"patch", "apply_patch"},
		{"Grep", "agentgrep"},
		{"grep", "agentgrep"},
		{"Agent", "subagent"},
		{"ScheduleWakeup", "schedule"},
		{"Skill", "skill_manage"},
		{"communicate", "swarm"},
		// Unrecognized namespaces are not stripped.
		{"mcp.functions.bash", "mcp.functions.bash"},
		{"custom_tool", "custom_tool"},
	}
	for _, c := range cases {
		if got := ResolveToolName(c[0]); got != c[1] {
			t.Fatalf("ResolveToolName(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// Ported: ensure_intent_adds_property_and_required + creates_required +
// preserves_existing + skips_non_object.
func TestEnsureIntentInSchema(t *testing.T) {
	out := EnsureIntentInSchema(json.RawMessage(`{
		"type": "object",
		"required": ["command"],
		"properties": {"command": {"type": "string"}}
	}`))
	var doc struct {
		Properties struct {
			Intent json.RawMessage `json:"intent"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Properties.Intent) == 0 {
		t.Fatalf("intent property added: %s", out)
	}
	if !contains(doc.Required, "command") || !contains(doc.Required, "intent") {
		t.Fatalf("required: %v", doc.Required)
	}

	out = EnsureIntentInSchema(json.RawMessage(`{"type":"object","properties":{}}`))
	if !hasRequired(out, "intent") {
		t.Fatalf("required array created: %s", out)
	}

	out = EnsureIntentInSchema(json.RawMessage(`{
		"type": "object",
		"required": ["intent"],
		"properties": {"intent": {"type": "string", "description": "custom"}}
	}`))
	if !jsonContains(out, `"description":"custom"`) {
		t.Fatalf("existing intent preserved: %s", out)
	}
	if countRequired(out, "intent") != 1 {
		t.Fatalf("intent not duplicated: %s", out)
	}

	schema := json.RawMessage(`{"type": "string"}`)
	if string(EnsureIntentInSchema(schema)) != string(schema) {
		t.Fatalf("non-object schema untouched: %s", EnsureIntentInSchema(schema))
	}
}

// Ported: escape_hatch_tests — the flag rides on every object schema,
// optional, and never overwrites a tool's own declaration.
func TestEnsureIntentEscapeHatch(t *testing.T) {
	out := EnsureIntentInSchema(json.RawMessage(`{
		"type": "object",
		"required": ["path"],
		"properties": {"path": {"type": "string"}}
	}`))
	var doc struct {
		Properties struct {
			AcceptLargeOutput struct {
				Type string `json:"type"`
			} `json:"accept_large_output"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Properties.AcceptLargeOutput.Type != "boolean" {
		t.Fatalf("escape hatch advertised: %s", out)
	}
	if contains(doc.Required, AcceptLargeOutputKey) {
		t.Fatalf("escape hatch is optional: %v", doc.Required)
	}

	out = EnsureIntentInSchema(json.RawMessage(`{
		"type": "object",
		"properties": {"accept_large_output": {"type": "boolean", "description": "custom"}}
	}`))
	if !jsonContains(out, `"description":"custom"`) {
		t.Fatalf("tool's own declaration survives: %s", out)
	}
	if AcceptLargeOutputKey != "accept_large_output" {
		t.Fatalf("schema key must match the guard reader")
	}
}

// Ported: subcall_ids_are_parent_scoped_and_retry_stable.
func TestForSubcall(t *testing.T) {
	context := ToolContext{SessionID: "same-session", MessageID: "same-message",
		ToolCallID: "first-batch", Mode: ExecutionAgentTurn}
	label := "batch-1-compile_remote"
	first := context.ForSubcall(label)
	if first.ToolCallID != context.ForSubcall(label).ToolCallID {
		t.Fatalf("subcall ids are retry-stable")
	}
	if first.ToolCallID == context.ForSubcall("batch-2-compile_remote").ToolCallID {
		t.Fatalf("different labels differ")
	}
	context.ToolCallID = "second-batch"
	second := context.ForSubcall(label)
	if first.ToolCallID == second.ToolCallID {
		t.Fatalf("parent-scoped: %q", first.ToolCallID)
	}
	if first.SessionID != second.SessionID || first.MessageID != second.MessageID {
		t.Fatalf("identity preserved")
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func hasRequired(schema json.RawMessage, key string) bool { return countRequired(schema, key) > 0 }

func countRequired(schema json.RawMessage, key string) int {
	var doc struct {
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(schema, &doc)
	count := 0
	for _, item := range doc.Required {
		if item == key {
			count++
		}
	}
	return count
}

func jsonContains(data json.RawMessage, fragment string) bool {
	return len(fragment) > 0 && indexOf(string(data), fragment) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
