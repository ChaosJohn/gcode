package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/ChaosJohn/gcode/internal/types"
)

// ---- caps ----

func TestContextModeClassification(t *testing.T) {
	cases := map[string]ContextMode{
		"claude-opus-4-8":          ContextNative1M,
		"claude-opus-4.8":          ContextNative1M,
		"claude-opus-4-7":          ContextNative1M,
		"claude-opus-4-6":          ContextOptIn1M,
		"claude-opus-4-6[1m]":      ContextOptIn1M,
		"claude-opus-4-5":          ContextStandard,
		"claude-sonnet-4-5":        ContextStandard,
		"claude-sonnet-4-6":        ContextOptIn1M,
		"claude-sonnet-5":          ContextNative1M,
		"claude-haiku-4-5":         ContextStandard,
		"claude-haiku-5":           ContextNative1M,
		"claude-3-7-sonnet":        ContextStandard,
		"gpt-5":                    ContextStandard,
		"claude-opus-4-8-20260101": ContextNative1M, // date suffix stripped
	}
	for model, want := range cases {
		if got := ContextModeOf(model); got != want {
			t.Errorf("ContextModeOf(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestMaxOutputTokens(t *testing.T) {
	cases := map[string]int{
		"claude-opus-4-8":     128_000,
		"claude-sonnet-4-6":   128_000,
		"claude-haiku-4-5":    64_000,
		"claude-sonnet-4-5":   32_768,
		"claude-3-7-sonnet":   32_768,
		"claude-opus-4-6[1m]": 128_000,
	}
	for model, want := range cases {
		if got := MaxOutputTokens(model); got != want {
			t.Errorf("MaxOutputTokens(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestReasoningCaps(t *testing.T) {
	if !ReasoningCapsOf("claude-opus-4-8").AdaptiveThinking {
		t.Error("opus 4.8 must be full ladder")
	}
	if ReasoningCapsOf("claude-opus-4-5").AdaptiveThinking {
		t.Error("opus 4.5 must be manual thinking")
	}
	if !ReasoningCapsOf("claude-opus-4-5").ManualThinking {
		t.Error("opus 4.5 manual")
	}
	if !ReasoningCapsOf("claude-sonnet-3-7").ManualThinking || ReasoningCapsOf("claude-sonnet-3-7").OutputEffort {
		t.Error("sonnet 3.7 manual-only")
	}
	if ReasoningCapsOf("gpt-5").SupportsReasoningEffort() {
		t.Error("non-claude has no caps")
	}
	if !ThinkingAlwaysOn("claude-opus-5-5") || ThinkingAlwaysOn("claude-opus-4-8") {
		t.Error("always-on = opus 5.5 / fable 5.1 only")
	}
}

func TestStripSuffixes(t *testing.T) {
	if got := StripLongContextSuffix("claude-opus-4-6[1m]"); got != "claude-opus-4-6" {
		t.Errorf("strip 1m: %q", got)
	}
	if got := StripDateSuffix("claude-opus-4-8-20260101"); got != "claude-opus-4-8" {
		t.Errorf("strip date: %q", got)
	}
	if got := StripDateSuffix("claude-opus-4-8"); got != "claude-opus-4-8" {
		t.Errorf("no date: %q", got)
	}
}

func TestOAuthToolNameMapping(t *testing.T) {
	for _, pair := range [][2]string{
		{"bash", "Bash"}, {"read", "Read"}, {"write", "Write"}, {"edit", "Edit"},
		{"glob", "Glob"}, {"grep", "Grep"}, {"subagent", "Agent"},
		{"schedule", "ScheduleWakeup"}, {"skill_manage", "Skill"}, {"custom", "custom"},
	} {
		if got := MapToolNameForOAuth(pair[0]); got != pair[1] {
			t.Errorf("map %q: %q", pair[0], got)
		}
		if got := MapToolNameFromOAuth(pair[1]); got != pair[0] {
			t.Errorf("unmap %q: %q", pair[1], got)
		}
	}
}

// ---- tools / system ----

func TestFormatToolsCacheBreakpoint(t *testing.T) {
	tools := []types.ToolDefinition{
		{Name: "read", Description: "r", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "bash", Description: "b", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "websearch", Description: "Search the web.", InputSchema: json.RawMessage(`{}`)},
	}
	out := FormatTools(tools, false, false)
	if len(out) != 3 {
		t.Fatalf("tools = %d", len(out))
	}
	if out[2].CacheControl == nil || out[2].CacheControl.TTL != nil {
		t.Errorf("breakpoint must land on the last eager tool with no ttl: %+v", out[2])
	}
	if out[0].CacheControl != nil || out[1].CacheControl != nil {
		t.Error("earlier tools must be uncached")
	}
	out1h := FormatTools(tools, false, true)
	if out1h[2].CacheControl == nil || out1h[2].CacheControl.TTL == nil || *out1h[2].CacheControl.TTL != "1h" {
		t.Errorf("1h ttl: %+v", out1h[2].CacheControl)
	}
	outOAuth := FormatTools(tools, true, false)
	if outOAuth[0].Name != "Read" || outOAuth[1].Name != "Bash" {
		t.Errorf("oauth rename: %q %q", outOAuth[0].Name, outOAuth[1].Name)
	}
}

func TestFinishToolListAllDeferred(t *testing.T) {
	tools := []ApiTool{
		{Name: "a", DeferLoading: true},
		{Name: "b", DeferLoading: true},
	}
	out := finishToolList(tools, false)
	if len(out) != 2 {
		t.Fatalf("tools = %d", len(out))
	}
	for _, tool := range out {
		if tool.DeferLoading {
			t.Error("all-deferred list must be promoted to eager")
		}
	}
	if out[len(out)-1].CacheControl == nil {
		t.Error("breakpoint on last")
	}
}

func TestBuildSystemParamOAuth(t *testing.T) {
	sys := BuildSystemParamSplit("static prompt", "dynamic status", true, false)
	if len(sys) != 4 {
		t.Fatalf("blocks = %d", len(sys))
	}
	if sys[0].Text != "x-anthropic-billing-header: "+OAuthBillingHeader {
		t.Errorf("billing block: %q", sys[0].Text)
	}
	if sys[1].Text != ClaudeCodeIdentity {
		t.Errorf("identity block: %q", sys[1].Text)
	}
	if sys[2].Text != "static prompt" || sys[2].CacheControl == nil {
		t.Errorf("static block: %+v", sys[2])
	}
	if sys[3].Text != "dynamic status" || sys[3].CacheControl != nil {
		t.Errorf("dynamic block: %+v", sys[3])
	}
	if BuildSystemParam("", false, false) != nil {
		t.Error("empty non-oauth system must be None")
	}
}

func TestBuildSystemParamNonOAuth(t *testing.T) {
	sys := BuildSystemParamSplit("static", "dynamic", false, true)
	if len(sys) != 2 {
		t.Fatalf("blocks = %d", len(sys))
	}
	if sys[0].CacheControl == nil || sys[0].CacheControl.TTL == nil || *sys[0].CacheControl.TTL != "1h" {
		t.Errorf("static cached 1h: %+v", sys[0])
	}
}

// ---- request golden ----

func TestApiRequestGolden(t *testing.T) {
	temp := float32(1.0)
	stop := "end_turn"
	request := ApiRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 32000,
		System:    BuildSystemParamSplit("sys", "", true, true),
		Messages: []ApiMessage{
			{Role: "user", Content: []ContentBlock{TextContent{Text: "hello"}}},
			{Role: "assistant", Content: []ContentBlock{
				ToolUseContent{ID: "toolu_1", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
			}},
			{Role: "user", Content: []ContentBlock{
				ToolResultContentBlockMsg{ToolUseID: "toolu_1", Content: ToolResultContent{Text: strPtr("out")}, IsError: true},
			}},
		},
		Tools: []ApiToolParam{
			{Custom: &ApiTool{Name: "Bash", Description: "b", InputSchema: json.RawMessage(`{"type":"object"}`), CacheControl: ttlPtr()}},
			{Server: json.RawMessage(`{"type":"web_search_20250305","name":"web_search"}`)},
		},
		Metadata:     &ApiMetadata{UserID: "user_abc"},
		Thinking:     AdaptiveThinking("claude-opus-4-8"),
		OutputConfig: &ApiOutputConfig{Effort: "high"},
		Temperature:  &temp,
		Stream:       true,
	}
	got, err := MarshalOrdered(request)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"claude-opus-4-8","max_tokens":32000,` +
		`"system":[{"type":"text","text":"x-anthropic-billing-header: ` + OAuthBillingHeader + `"},{"type":"text","text":"` + ClaudeCodeIdentity + `"},{"type":"text","text":"sys","cache_control":{"type":"ephemeral","ttl":"1h"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"out","is_error":true}]}],` +
		`"tools":[{"name":"Bash","description":"b","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"1h"}},` +
		`{"type":"web_search_20250305","name":"web_search"}],` +
		`"metadata":{"user_id":"user_abc"},` +
		`"thinking":{"type":"adaptive","display":"summarized"},` +
		`"output_config":{"effort":"high"},"temperature":1,"stream":true}`
	if string(got) != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
	_ = stop
}

func ttlPtr() *CacheControlParam {
	cc := EphemeralTTL("1h")
	return &cc
}

// ---- history repair ----

func tu(id string) types.ContentBlock {
	return types.ContentBlock{Type: types.BlockToolUse, ID: &id, Name: strPtr("bash"), Input: json.RawMessage(`{}`)}
}

func tr(id, content string) types.ContentBlock {
	return types.ContentBlock{Type: types.BlockToolResult, ToolUseID: &id, Content: &content}
}

func trErr(id, content string) types.ContentBlock {
	errFlag := true
	return types.ContentBlock{Type: types.BlockToolResult, ToolUseID: &id, Content: &content, IsError: &errFlag}
}

func TestHoistLateToolResults(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{tu("t1")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("intervening question")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{tr("t1", "late result")}},
	}
	// Same-turn-folded results (result one user turn after the call, even
	// with an intervening user message) count as in place; the formatting
	// pipeline merges the user messages and puts the result first.
	out := hoistLateToolResults(msgs)
	if len(out) != 3 {
		t.Fatalf("messages = %d", len(out))
	}
	// The formatting pipeline merges consecutive user turns into one.
	apiMsgs := FormatMessagesWithTools(msgs, false, nil)
	if len(apiMsgs) != 2 {
		t.Fatalf("pipeline messages = %d", len(apiMsgs))
	}
	if tr, ok := apiMsgs[1].Content[0].(ToolResultContentBlockMsg); !ok || tr.ToolUseID != "t1" {
		t.Errorf("merged result must lead: %+v", apiMsgs[1].Content[0])
	}
	if txt, ok := apiMsgs[1].Content[1].(TextContent); !ok || txt.Text != "intervening question" {
		t.Errorf("text follows: %+v", apiMsgs[1].Content[1])
	}

	// A result arriving a full turn later IS late: it moves to directly
	// follow the call's turn.
	late := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{tu("t2")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("question")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.TextBlock("answer")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{tr("t2", "late result")}},
	}
	out2 := hoistLateToolResults(late)
	if len(out2) != 4 {
		t.Fatalf("late messages = %d", len(out2))
	}
	if id, _, _, ok := out2[1].Content[0].AsToolResult(); !ok || id != "t2" {
		t.Errorf("hoisted result must directly follow the call turn: %+v", out2[1].Content[0])
	}
	if out2[1].Role != types.RoleUser {
		t.Errorf("hoisted carrier role = %s", out2[1].Role)
	}
}

func TestDedupeToolResultsPrefersReal(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{tu("t1")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{trErr("t1", types.ToolOutputMissingText)}},
		{Role: types.RoleUser, Content: []types.ContentBlock{tr("t1", "real output")}},
	}
	out := dedupeToolResults(msgs)
	var results []string
	for _, msg := range out {
		for _, block := range msg.Content {
			if id, content, _, ok := block.AsToolResult(); ok {
				results = append(results, id+"="+content)
			}
		}
	}
	if len(results) != 1 || results[0] != "t1=real output" {
		t.Errorf("results = %v", results)
	}
}

func TestDedupeToolUses(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{tu("t1"), tu("t1")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{tr("t1", "result")}},
	}
	out := dedupeToolUses(msgs)
	if len(out[0].Content) != 1 {
		t.Errorf("uses = %d", len(out[0].Content))
	}
}

func TestRepairDanglingServerTools(t *testing.T) {
	serverUse := func(id string) types.ContentBlock {
		return types.ContentBlock{Type: types.BlockProviderNative, Provider: strPtr(types.ProviderNativeAnthropic),
			Item: json.RawMessage(`{"type":"server_tool_use","id":"` + id + `","name":"web_search","input":{}}`)}
	}
	serverResult := func(id string) types.ContentBlock {
		return types.ContentBlock{Type: types.BlockProviderNative, Provider: strPtr(types.ProviderNativeAnthropic),
			Item: json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"` + id + `","content":[]}`)}
	}
	msgs := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{serverUse("s1"), serverUse("s2")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{serverResult("s1")}},
	}
	out := repairDanglingAnthropicServerTools(msgs)
	if len(out[0].Content) != 1 {
		t.Errorf("orphan s2 must be dropped, kept %d", len(out[0].Content))
	}
	if len(out[1].Content) != 1 {
		t.Errorf("result s1 kept, kept %d", len(out[1].Content))
	}
	// pause_turn shape: trailing unmatched server_tool_use is preserved.
	paused := []types.Message{{Role: types.RoleAssistant, Content: []types.ContentBlock{serverUse("s3")}}}
	if out2 := repairDanglingAnthropicServerTools(paused); len(out2[0].Content) != 1 {
		t.Errorf("paused turn preserved")
	}
}

// ---- reasoning ----

func TestBuildReasoningParts(t *testing.T) {
	// Full-ladder model with effort.
	thinking, output, temp := BuildReasoningRequestParts("claude-opus-4-8", true, true, "high", 32000)
	if thinking == nil || !thinking.Adaptive || thinking.Display != "summarized" {
		t.Errorf("adaptive thinking: %+v", thinking)
	}
	if output == nil || output.Effort != "high" {
		t.Errorf("output config: %+v", output)
	}
	if temp != nil {
		t.Errorf("temperature must be omitted with thinking: %v", *temp)
	}
	// Manual-thinking model.
	thinking, output, _ = BuildReasoningRequestParts("claude-opus-4-5", true, true, "high", 32000)
	if thinking == nil || !thinking.IsEnabled || thinking.BudgetTokens != 8192 {
		t.Errorf("manual budget: %+v", thinking)
	}
	if output == nil || output.Effort != "high" {
		t.Errorf("opus 4.5 output effort (MANUAL_WITH_EFFORT): %+v", output)
	}
	// OAuth without effort: temperature 1.0, no thinking unless display on.
	_, _, temp = BuildReasoningRequestParts("claude-opus-4-5", true, false, "", 32000)
	if temp == nil || *temp != 1.0 {
		t.Errorf("oauth fallback temperature: %v", temp)
	}
	// always-on model with none → low, still adaptive.
	thinking, _, _ = BuildReasoningRequestParts("claude-opus-5-5", true, true, "none", 32000)
	if thinking == nil || !thinking.Adaptive || !thinking.BlockBinding {
		t.Errorf("always-on none: %+v", thinking)
	}
}

func TestRecoverRejectedReasoning(t *testing.T) {
	request := &ApiRequest{Thinking: AdaptiveThinking("claude-opus-5-5"), OutputConfig: &ApiOutputConfig{Effort: "high"}}
	if !RecoverRejectedReasoning(request, "claude-opus-5-5", true) {
		t.Fatal("must recover")
	}
	if request.OutputConfig != nil || request.Thinking == nil || !request.Thinking.Adaptive || request.Temperature != nil {
		t.Errorf("recovered request: %+v", request)
	}
	if RecoverRejectedReasoning(request, "claude-opus-5-5", true) {
		t.Error("second recovery must fail (nothing left to drop)")
	}
	// Non-always-on models drop everything and fall back to temperature 1.0.
	request2 := &ApiRequest{Thinking: &ApiThinking{IsEnabled: true, BudgetTokens: 1024}}
	if !RecoverRejectedReasoning(request2, "claude-opus-4-5", true) {
		t.Fatal("manual recovery")
	}
	if request2.Thinking != nil || request2.Temperature == nil || *request2.Temperature != 1.0 {
		t.Errorf("manual recovery shape: %+v", request2)
	}
}

func TestWithBindingBeta(t *testing.T) {
	if got := WithBindingBeta(OAuthBetaHeaders, AdaptiveThinking("claude-opus-5-5")); got != OAuthBetaHeaders+","+bindingBeta {
		t.Errorf("binding beta: %q", got)
	}
	if got := WithBindingBeta(OAuthBetaHeaders, nil); got != OAuthBetaHeaders {
		t.Errorf("no binding: %q", got)
	}
}
