package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/types"
)

func msgOf(role types.Role, blocks ...types.ContentBlock) types.Message {
	return types.Message{Role: role, Content: blocks}
}

func textBlock(s string) types.ContentBlock {
	text := s
	return types.ContentBlock{Type: types.BlockText, Text: &text}
}

func toolUseBlock(id, name, input string) types.ContentBlock {
	idPtr, namePtr := id, name
	return types.ContentBlock{Type: types.BlockToolUse, ID: &idPtr, Name: &namePtr,
		Input: json.RawMessage(input)}
}

func toolResultBlock(id, content string, isErr bool) types.ContentBlock {
	idPtr, contentPtr, errPtr := id, content, isErr
	return types.ContentBlock{Type: types.BlockToolResult, ToolUseID: &idPtr,
		Content: &contentPtr, IsError: &errPtr}
}

func decodeItems(t *testing.T, items []any) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(encoded, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// Ported: build_responses_input_logs_oversized_native_compaction.
func TestOversizedCompactionDowngradesToMessage(t *testing.T) {
	oversized := strings.Repeat("x", EncryptedContentSafeMaxChars+1)
	var logs []string
	items := buildResponsesInputWithLogger(
		[]types.Message{msgOf(types.RoleUser, types.ContentBlock{
			Type: types.BlockOpenAICompaction, EncryptedContent: &oversized,
		})},
		func(level RequestLogLevel, message string) {
			if level == LogWarn {
				logs = append(logs, message)
			}
		})
	found := false
	for _, item := range decodeItems(t, items) {
		if item["type"] == "message" && item["role"] == "user" {
			found = true
		}
	}
	if !found {
		t.Errorf("compaction downgraded to user message: %v", items)
	}
	sawWarn := false
	for _, message := range logs {
		if strings.Contains(message, "Dropping oversized native compaction payload") {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Errorf("warn log missing: %v", logs)
	}
}

// Sendable compaction replays verbatim.
func TestSendableCompactionReplays(t *testing.T) {
	items := BuildResponsesInput([]types.Message{msgOf(types.RoleUser,
		types.ContentBlock{Type: types.BlockOpenAICompaction,
			EncryptedContent: ptr("enc_small")})})
	decoded := decodeItems(t, items)
	if len(decoded) != 1 || decoded[0]["type"] != "compaction" {
		t.Fatalf("items = %v", decoded)
	}
	if decoded[0]["encrypted_content"] != "enc_small" {
		t.Errorf("encrypted_content = %v", decoded[0]["encrypted_content"])
	}
}

// Ported: build_responses_input_replays_openai_reasoning_without_output_only_status.
func TestReasoningReplayWithoutStatus(t *testing.T) {
	id := "rs_123"
	status := "completed"
	items := BuildResponsesInput([]types.Message{msgOf(types.RoleAssistant,
		types.ContentBlock{Type: types.BlockOpenAIReasoning, ID: &id,
			Summary:          []string{"Checked constraints."},
			EncryptedContent: ptr("enc_reasoning"), Status: &status})})
	decoded := decodeItems(t, items)
	if len(decoded) != 1 {
		t.Fatalf("items = %v", decoded)
	}
	item := decoded[0]
	if item["type"] != "reasoning" || item["id"] != "rs_123" {
		t.Fatalf("item = %v", item)
	}
	if item["encrypted_content"] != "enc_reasoning" {
		t.Errorf("encrypted_content = %v", item["encrypted_content"])
	}
	if _, has := item["status"]; has {
		t.Errorf("status must not be replayed: %v", item)
	}
	summary, _ := item["summary"].([]any)
	if len(summary) != 1 {
		t.Fatalf("summary = %v", summary)
	}
	first := summary[0].(map[string]any)
	if first["type"] != "summary_text" || first["text"] != "Checked constraints." {
		t.Errorf("summary item = %v", first)
	}
}

// Ported: build_responses_input_replays_web_search_calls_and_downgrades_foreign_items.
func TestWebSearchReplayAndForeignDowngrade(t *testing.T) {
	webSearchCall := json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"jcode"}}`)
	foreign := json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"s","content":[{"type":"web_search_result","url":"https://a.example","title":"A","encrypted_content":"ENC"}]}`)
	openaiProvider, anthropicProvider := types.ProviderNativeOpenAI, types.ProviderNativeAnthropic
	items := BuildResponsesInput([]types.Message{msgOf(types.RoleAssistant,
		types.ContentBlock{Type: types.BlockProviderNative, Provider: &openaiProvider, Item: webSearchCall},
		types.ContentBlock{Type: types.BlockProviderNative, Provider: &anthropicProvider, Item: foreign},
	)})
	decoded := decodeItems(t, items)
	if len(decoded) != 2 {
		t.Fatalf("items = %v", decoded)
	}
	if decoded[0]["type"] != "web_search_call" || decoded[0]["id"] != "ws_1" {
		t.Errorf("web_search_call replayed verbatim: %v", decoded[0])
	}
	if decoded[1]["type"] != "message" {
		t.Fatalf("foreign downgraded: %v", decoded[1])
	}
	text := decoded[1]["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "https://a.example") {
		t.Errorf("text = %s", text)
	}
	if strings.Contains(text, "ENC") {
		t.Errorf("encrypted content leaked: %s", text)
	}
}

// Ported: build_tools_omits_deferred_definitions.
func TestBuildToolsOmitsDeferred(t *testing.T) {
	eager := types.ToolDefinition{Name: "bash", Description: "run", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}
	deferred := types.ToolDefinition{Name: "mcp__weather__forecast", Description: "weather", DeferLoading: true,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}
	tools := BuildTools([]types.ToolDefinition{eager, deferred})
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	var probe map[string]any
	if err := json.Unmarshal(tools[0], &probe); err != nil {
		t.Fatal(err)
	}
	if probe["name"] != "bash" {
		t.Errorf("name = %v", probe["name"])
	}
}

// Ported: tool_reference_inserts_additional_tools_after_its_output.
func TestToolReferenceInsertsAdditionalTools(t *testing.T) {
	messages := []types.Message{
		msgOf(types.RoleAssistant, toolUseBlock("call_1", "load_tool", "{}")),
		msgOf(types.RoleUser, toolResultBlock("call_1", "loaded", false),
			func() types.ContentBlock {
				id, name := "call_1", "mcp__weather__forecast"
				return types.ContentBlock{Type: types.BlockToolReference, ToolUseID: &id, ToolName: &name}
			}()),
		msgOf(types.RoleUser, textBlock("go")),
	}
	tools := []types.ToolDefinition{deferredDef("mcp__weather__forecast")}
	input := BuildResponsesInput(messages)
	before := len(input)
	input = insertAdditionalTools(input, messages, tools)

	if len(input) != before+1 {
		t.Fatalf("input = %d items (before %d)", len(input), before)
	}
	pos := -1
	decoded := decodeItems(t, input)
	for i, item := range decoded {
		if item["type"] == "additional_tools" {
			pos = i
		}
	}
	if pos < 0 {
		t.Fatalf("no additional_tools: %v", decoded)
	}
	if decoded[pos-1]["type"] != "function_call_output" {
		t.Errorf("inserted after output: %v", decoded[pos-1]["type"])
	}
	if decoded[pos]["role"] != "developer" {
		t.Errorf("role = %v", decoded[pos]["role"])
	}
	loaded := []string{}
	for _, tool := range decoded[pos]["tools"].([]any) {
		loaded = append(loaded, tool.(map[string]any)["name"].(string))
	}
	if len(loaded) != 1 || loaded[0] != "mcp__weather__forecast" {
		t.Errorf("loaded = %v", loaded)
	}
}

// Ported: tool_reference_without_deferred_definition_is_ignored.
func TestToolReferenceWithoutDeferredIgnored(t *testing.T) {
	id, name := "call_1", "mcp__weather__forecast"
	messages := []types.Message{msgOf(types.RoleUser,
		types.ContentBlock{Type: types.BlockToolReference, ToolUseID: &id, ToolName: &name})}
	input := BuildResponsesInput(messages)
	before := len(input)
	input = insertAdditionalTools(input, messages, nil)
	if len(input) != before {
		t.Fatalf("input changed: %v", decodeItems(t, input))
	}
}

// Deferred tool output delayed until its call, then attached after it.
func TestDelayedOutputAttachesAfterCall(t *testing.T) {
	messages := []types.Message{
		msgOf(types.RoleAssistant, toolUseBlock("call_1", "bash", `{"command":"ls"}`)),
		msgOf(types.RoleUser, textBlock("meanwhile")),
		msgOf(types.RoleUser, toolResultBlock("call_1", "file-a", false)),
	}
	decoded := decodeItems(t, BuildResponsesInput(messages))
	if len(decoded) != 3 {
		t.Fatalf("items = %v", decoded)
	}
	if decoded[0]["type"] != "function_call" || decoded[1]["type"] != "function_call_output" {
		t.Errorf("call then output: %v %v", decoded[0]["type"], decoded[1]["type"])
	}
	if decoded[1]["output"] != "file-a" {
		t.Errorf("output = %v", decoded[1]["output"])
	}
	if decoded[2]["type"] != "message" {
		t.Errorf("interleaved text kept: %v", decoded[2]["type"])
	}
}

// A call with no output anywhere gets a synthetic missing output.
func TestMissingOutputInjected(t *testing.T) {
	messages := []types.Message{
		msgOf(types.RoleAssistant, toolUseBlock("call_x", "bash", `{}`)),
	}
	decoded := decodeItems(t, BuildResponsesInput(messages))
	if len(decoded) != 2 {
		t.Fatalf("items = %v", decoded)
	}
	if decoded[1]["type"] != "function_call_output" || decoded[1]["output"] != MissingOutputText {
		t.Errorf("synthetic output: %v", decoded[1])
	}
}

// An orphan output (no call) is rewritten into a user message.
func TestOrphanOutputRewritten(t *testing.T) {
	messages := []types.Message{
		msgOf(types.RoleUser, toolResultBlock("call_orphan", "precious output", false)),
	}
	decoded := decodeItems(t, BuildResponsesInput(messages))
	if len(decoded) != 1 || decoded[0]["type"] != "message" || decoded[0]["role"] != "user" {
		t.Fatalf("items = %v", decoded)
	}
	text := decoded[0]["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Recovered orphaned tool output: call_orphan") ||
		!strings.Contains(text, "precious output") {
		t.Errorf("text = %s", text)
	}
}

// An orphan error output equal to the missing placeholder is dropped.
func TestOrphanMissingPlaceholderDropped(t *testing.T) {
	messages := []types.Message{
		msgOf(types.RoleUser, toolResultBlock("call_orphan", types.ToolOutputMissingText, true)),
	}
	decoded := decodeItems(t, BuildResponsesInput(messages))
	if len(decoded) != 0 {
		t.Fatalf("items = %v", decoded)
	}
}

// Duplicate outputs: once an output attaches to its call, later duplicates
// are skipped — the placeholder that arrived first stays (upstream
// used_outputs semantics; the upgrade path in the reorder pass is defensive).
func TestDuplicateOutputsSkipAfterFirst(t *testing.T) {
	messages := []types.Message{
		msgOf(types.RoleAssistant, toolUseBlock("call_1", "bash", `{}`)),
		msgOf(types.RoleUser, toolResultBlock("call_1", types.ToolOutputMissingText, true)),
		msgOf(types.RoleUser, toolResultBlock("call_1", "real output", false)),
	}
	decoded := decodeItems(t, BuildResponsesInput(messages))
	var output any
	for _, item := range decoded {
		if item["type"] == "function_call_output" {
			output = item["output"]
		}
	}
	if output != MissingOutputText {
		t.Errorf("output = %v (items %v)", output, decoded)
	}
}

func deferredDef(name string) types.ToolDefinition {
	schema := `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`
	return types.ToolDefinition{Name: name, Description: "deferred", DeferLoading: true,
		InputSchema: json.RawMessage(schema)}
}

func ptr(s string) *string { return &s }
