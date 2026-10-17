package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/base/config"
	"github.com/ChaosJohn/gcode/internal/types"
)

// Complete() builds the wire request end to end: tools, system blocks,
// metadata, reasoning, then streams.
func TestCompleteEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var seenBody map[string]any
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Errorf("body: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			"event: message_start",
			`data: {"type":"message_start","message":{"model":"claude-opus-4-8-20260101","usage":{"input_tokens":11}}}`,
			"",
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash"}}`,
			"",
			"event: content_block_delta",
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":0}`,
			"",
			"event: message_delta",
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
			"",
			"",
		}, "\n")))
	}))
	defer server.Close()

	tools := []types.ToolDefinition{
		{Name: "bash", Description: "Run a shell command", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "websearch", Description: types.LocalWebsearchDescription, InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	messages := []types.Message{
		msg(types.RoleUser, txt("list files")),
	}
	webSearch := config.Default().WebSearch
	var events []types.StreamEvent
	cfg := CompleteConfig{
		IsOAuth:      true,
		Transport:    TransportConfig{OAuthURL: server.URL},
		ResolveToken: func(bool) (string, error) { return "tok", nil },
	}
	settings := DefaultRetrySettings("high", true, nil, "")
	err := Complete(context.Background(), cfg, ProviderOptions{
		Messages: messages, Tools: tools, System: "You are helpful.",
		Model: "claude-opus-4-8", SessionID: "sess-1",
		FirstParty: true, WebSearch: webSearch,
	}, settings, func(e types.StreamEvent) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}

	// OAuth attribution + bearer.
	if seenHeaders.Get("authorization") != "Bearer tok" {
		t.Errorf("authorization = %q", seenHeaders.Get("authorization"))
	}
	// Request shape: model stripped, tools include server web_search, OAuth
	// system has billing/identity blocks, metadata present, thinking set.
	if got := seenBody["model"]; got != "claude-opus-4-8" {
		t.Errorf("model = %v", got)
	}
	toolsArr, _ := seenBody["tools"].([]any)
	if len(toolsArr) != 2 {
		t.Fatalf("tools = %d (%v)", len(toolsArr), seenBody["tools"])
	}
	first, _ := toolsArr[0].(map[string]any)
	if first["name"] != "Bash" {
		t.Errorf("first tool = %v", first["name"])
	}
	second, _ := toolsArr[1].(map[string]any)
	if second["type"] != "web_search_20250305" {
		t.Errorf("server tool = %v", second)
	}
	systemArr, _ := seenBody["system"].([]any)
	// billing + identity + static (dynamic part empty → omitted).
	if len(systemArr) != 3 {
		t.Fatalf("system blocks = %d", len(systemArr))
	}
	billing, _ := systemArr[0].(map[string]any)
	if !strings.Contains(billing["text"].(string), "x-anthropic-billing-header") {
		t.Errorf("billing block missing: %v", billing)
	}
	thinking, _ := seenBody["thinking"].(map[string]any)
	if thinking == nil || thinking["type"] != "adaptive" || thinking["display"] != "summarized" {
		t.Errorf("thinking = %v", seenBody["thinking"])
	}
	output, _ := seenBody["output_config"].(map[string]any)
	if output == nil || output["effort"] != "high" {
		t.Errorf("output_config = %v", seenBody["output_config"])
	}
	if _, hasTemp := seenBody["temperature"]; hasTemp {
		t.Error("temperature must be omitted when thinking is active")
	}
	metadata, _ := seenBody["metadata"].(map[string]any)
	if metadata == nil {
		t.Fatal("oauth metadata missing")
	}

	// Events: tool demapped to bash, usage emitted.
	kinds := []string{}
	for _, e := range events {
		kinds = append(kinds, fmt.Sprintf("%T", e))
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "types.ToolUseStart") || !strings.Contains(joined, "types.TokenUsageEvent") {
		t.Errorf("events = %s", joined)
	}
	for _, e := range events {
		if start, ok := e.(types.ToolUseStart); ok && start.Name != "bash" {
			t.Errorf("tool demap = %q", start.Name)
		}
	}
}
