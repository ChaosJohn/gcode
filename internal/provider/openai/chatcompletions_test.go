// Chat-completions wire tests: SSE parsing (text deltas, tool-call starts/
// argument deltas/ends, usage, [DONE]), message conversion (system prefix,
// tool pairing), and named-provider assembly from config.
package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/types"
)

// fakeChatServer replays an SSE stream: text, then one tool call delivered
// across start + argument deltas, then usage, then [DONE].
func fakeChatServer(t *testing.T, captured *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		*captured = body
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"content":"Hello"}}]}`,
			`{"choices":[{"delta":{"content":" world"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"echo","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"text\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hi\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7}}`,
			`[DONE]`,
		}
		for _, chunk := range chunks {
			_, _ = w.Write([]byte("data: " + chunk + "\n\n"))
		}
	}))
}

func collectEvents(t *testing.T, emit func(types.StreamEvent) error) []types.StreamEvent {
	t.Helper()
	return nil
}

func TestChatCompletionsStream(t *testing.T) {
	var captured map[string]any
	server := fakeChatServer(t, &captured)
	defer server.Close()

	var events []types.StreamEvent
	err := ChatCompletionsStream(ChatCompletionsOptions{
		Model: "test-model", BaseURL: server.URL, APIKey: "test-key",
		System: "be brief",
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("hi")}},
		},
		Tools: []types.ToolDefinition{{Name: "echo", Description: "Echo.",
			InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Emit: func(event types.StreamEvent) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	var starts, ends, inputDeltas []string
	var sawUsage, sawEnd bool
	for _, event := range events {
		switch e := event.(type) {
		case types.TextDelta:
			texts = append(texts, e.Text)
		case types.ToolUseStart:
			starts = append(starts, e.ID+":"+e.Name)
		case types.ToolInputDeltaFor:
			inputDeltas = append(inputDeltas, e.ID+"|"+e.Delta)
		case types.ToolUseEndFor:
			ends = append(ends, e.ID)
		case types.TokenUsageEvent:
			if e.InputTokens != nil && *e.InputTokens == 11 &&
				e.OutputTokens != nil && *e.OutputTokens == 7 {
				sawUsage = true
			}
		case types.MessageEnd:
			sawEnd = true
		}
	}
	if strings.Join(texts, "") != "Hello world" {
		t.Fatalf("text deltas: %v", texts)
	}
	if len(starts) != 1 || starts[0] != "call-1:echo" {
		t.Fatalf("tool start: %v", starts)
	}
	joined := strings.Join(inputDeltas, "")
	if !strings.Contains(joined, `call-1|{"text":`) || !strings.Contains(joined, `call-1|"hi"}`) {
		t.Fatalf("argument deltas: %v", inputDeltas)
	}
	if len(ends) != 1 || ends[0] != "call-1" {
		t.Fatalf("tool end: %v", ends)
	}
	if !sawUsage || !sawEnd {
		t.Fatalf("usage=%v end=%v", sawUsage, sawEnd)
	}
	// Request shape: model, system message, tools, stream options.
	if captured["model"] != "test-model" {
		t.Fatalf("model: %v", captured["model"])
	}
	messages := captured["messages"].([]any)
	first := messages[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be brief" {
		t.Fatalf("system message: %v", first)
	}
	tools := captured["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "echo" {
		t.Fatalf("tools: %v", tools)
	}
	if captured["stream"] != true {
		t.Fatalf("stream flag")
	}
}

// Assistant tool calls and tool results round-trip into the chat wire.
func TestChatMessagesToolPairing(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("run echo")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.TextBlock("calling"),
			{Type: types.BlockToolUse, ID: strPtrChat("call-1"), Name: strPtrChat("echo"),
				Input: json.RawMessage(`{"text":"hi"}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.BlockToolResult, ToolUseID: strPtrChat("call-1"),
				Content: strPtrChat("echoed: hi")},
		}},
	}
	encoded, err := json.Marshal(ChatMessages("", messages))
	if err != nil {
		t.Fatal(err)
	}
	joined := string(encoded)
	for _, needle := range []string{
		`"role":"user"`, `"content":"run echo"`,
		`"tool_calls":[{`, `"name":"echo"`, `"arguments":"{\"text\":\"hi\"}"`,
		`"role":"tool"`, `"tool_call_id":"call-1"`, `"content":"echoed: hi"`,
	} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("missing %s in %s", needle, joined)
		}
	}
}

func strPtrChat(s string) *string { return &s }

// HTTP errors surface with body context.
func TestChatCompletionsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()
	err := ChatCompletionsStream(ChatCompletionsOptions{
		Model: "m", BaseURL: server.URL, APIKey: "wrong",
	})
	if err == nil || !strings.Contains(err.Error(), "401") ||
		!strings.Contains(err.Error(), "bad key") {
		t.Fatalf("error: %v", err)
	}
}
