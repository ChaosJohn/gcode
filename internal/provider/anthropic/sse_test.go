package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/types"
)

func sse(t *testing.T, eventType, data string) *SSEEvent {
	t.Helper()
	return &SSEEvent{Event: eventType, Data: data}
}

func TestParseSSEEvent(t *testing.T) {
	buffer := &strings.Builder{}
	buffer.WriteString("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
	event := ParseSSEEvent(buffer)
	if event == nil || event.Event != "message_start" {
		t.Fatalf("event = %+v", event)
	}
	if buffer.Len() != 0 {
		t.Errorf("buffer must be drained: %q", buffer.String())
	}
	if ParseSSEEvent(buffer) != nil {
		t.Error("empty buffer must yield nil")
	}
	// Partial event stays buffered.
	buffer.WriteString("event: ping\ndata: {\"type\":\"ping\"}\n")
	if ParseSSEEvent(buffer) != nil {
		t.Error("incomplete event must stay buffered")
	}
	buffer.WriteString("\n")
	if event = ParseSSEEvent(buffer); event == nil || event.Event != "ping" {
		t.Fatalf("ping = %+v", event)
	}
}

// Ported: test_anthropic_thinking_sse_events.
func TestThinkingSSEEvents(t *testing.T) {
	state := &SseStreamState{}
	events := ProcessSSEEvent(sse(t, "content_block_start",
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":"sig"}}`),
		state, false)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if _, ok := events[0].(types.ThinkingStart); !ok {
		t.Fatalf("want ThinkingStart, got %T", events[0])
	}
	if !state.CurrentThinkingBlock {
		t.Error("state must track thinking")
	}

	events = ProcessSSEEvent(sse(t, "content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning text"}}`),
		state, false)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if delta, ok := events[0].(types.ThinkingDelta); !ok || delta.Delta != "reasoning text" {
		t.Fatalf("want ThinkingDelta, got %+v", events[0])
	}

	events = ProcessSSEEvent(sse(t, "content_block_delta",
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}`),
		state, false)
	if delta, ok := events[0].(types.ThinkingSignatureDelta); !ok || delta.Delta != "signed" {
		t.Fatalf("want ThinkingSignatureDelta, got %+v", events[0])
	}

	events = ProcessSSEEvent(sse(t, "content_block_stop", `{"type":"content_block_stop","index":0}`), state, false)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if _, ok := events[0].(types.ThinkingEnd); !ok {
		t.Fatalf("want ThinkingEnd, got %T", events[0])
	}
	if state.CurrentThinkingBlock {
		t.Error("thinking flag must clear")
	}
}

// Golden: a full text + tool-use stream maps to the upstream event sequence.
func TestTextToolUseSSEGolden(t *testing.T) {
	state := &SseStreamState{}
	var all []types.StreamEvent
	feed := func(eventType, data string) {
		all = append(all, ProcessSSEEvent(sse(t, eventType, data), state, true)...)
	}
	feed("message_start", `{"type":"message_start","message":{"model":"claude-opus-4-8-20260101","usage":{"input_tokens":10,"cache_read_input_tokens":4,"cache_creation_input_tokens":2}}}`)
	feed("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	feed("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Run"}}`)
	feed("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ning"}}`)
	feed("content_block_stop", `{"type":"content_block_stop","index":0}`)
	feed("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01","name":"Bash"}}`)
	feed("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`)
	feed("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`)
	feed("content_block_stop", `{"type":"content_block_stop","index":1}`)
	feed("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":21}}`)
	feed("ping", `{"type":"ping"}`)
	feed("message_stop", `{"type":"message_stop"}`)

	want := []string{
		"UpstreamProvider(anthropic:claude-opus-4-8-20260101)",
		"TextDelta(Run)", "TextDelta(ning)",
		"ToolUseStart(toolu_01,bash)", "ToolInputDelta({\"command\":)", "ToolInputDelta(\"ls\"})", "ToolUseEnd",
		"MessageEnd(tool_use)",
		"ConnectionPhase(streaming)",
	}
	if len(all) != len(want) {
		for i, e := range all {
			t.Logf("%d: %T %+v", i, e, e)
		}
		t.Fatalf("events = %d, want %d", len(all), len(want))
	}
	for i, w := range want {
		got := ""
		switch e := all[i].(type) {
		case types.UpstreamProviderEvent:
			got = "UpstreamProvider(" + e.Provider + ")"
		case types.TextDelta:
			got = "TextDelta(" + e.Text + ")"
		case types.ToolUseStart:
			got = "ToolUseStart(" + e.ID + "," + e.Name + ")"
		case types.ToolInputDelta:
			got = "ToolInputDelta(" + e.Delta + ")"
		case types.ToolUseEnd:
			got = "ToolUseEnd"
		case types.MessageEnd:
			got = "MessageEnd(" + deref(e.StopReason) + ")"
		case types.ConnectionPhaseEvent:
			got = "ConnectionPhase(" + string(e.Phase) + ")"
		default:
			got = "UNKNOWN"
		}
		if got != w {
			t.Errorf("event[%d] = %s, want %s", i, got, w)
		}
	}
	// OAuth demaps the wire name Bash → registry name bash.
	if start, ok := all[3].(types.ToolUseStart); !ok || start.Name != "bash" {
		t.Errorf("oauth tool name demap: %+v", all[3])
	}
	usage := state.FinalTokenUsage()
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 10 ||
		usage.OutputTokens == nil || *usage.OutputTokens != 21 ||
		usage.CacheReadInputTokens == nil || *usage.CacheReadInputTokens != 4 ||
		usage.CacheCreationInputTokens == nil || *usage.CacheCreationInputTokens != 2 {
		t.Errorf("usage = %+v", usage)
	}
}

// The server-tool block accumulates deltas and lands as a verbatim native item.
func TestServerToolBlockSSE(t *testing.T) {
	state := &SseStreamState{}
	var all []types.StreamEvent
	feed := func(eventType, data string) {
		all = append(all, ProcessSSEEvent(sse(t, eventType, data), state, false)...)
	}
	feed("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search"}}`)
	feed("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`)
	feed("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"golang json\"}"}}`)
	feed("content_block_stop", `{"type":"content_block_stop","index":0}`)
	feed("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"Go","url":"https://go.dev"}]}}`)

	if len(all) != 2 {
		t.Fatalf("events = %+v", all)
	}
	native, ok := all[0].(types.ProviderNativeEvent)
	if !ok || native.Provider != types.ProviderNativeAnthropic {
		t.Fatalf("want ProviderNative, got %T", all[0])
	}
	var item struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(native.Item, &item); err != nil {
		t.Fatal(err)
	}
	if item.Type != "server_tool_use" || item.ID != "srvtoolu_1" || item.Name != "web_search" {
		t.Errorf("item = %+v", item)
	}
	if string(item.Input) != `{"query":"golang json"}` {
		t.Errorf("input = %s", item.Input)
	}
	result, ok := all[1].(types.ProviderNativeEvent)
	if !ok {
		t.Fatalf("result event = %T", all[1])
	}
	if !strings.Contains(string(result.Item), "srvtoolu_1") {
		t.Errorf("result item = %s", result.Item)
	}
	// The text fallback renders a summary for display.
	fallback := types.ProviderNativeTextFallback(types.ProviderNativeAnthropic, result.Item, nil)
	if !strings.Contains(fallback, "1. Go") || !strings.Contains(fallback, "https://go.dev") {
		t.Errorf("fallback = %q", fallback)
	}
}

// Unknown block types and events are ignored; the stream stays intact.
func TestUnknownSSEIgnored(t *testing.T) {
	state := &SseStreamState{}
	if events := ProcessSSEEvent(sse(t, "content_block_start",
		`{"type":"content_block_start","index":0,"content_block":{"type":"brand_new_block"}}`), state, false); len(events) != 0 {
		t.Errorf("unknown block: %+v", events)
	}
	if events := ProcessSSEEvent(sse(t, "futuristic_event", `{}`), state, false); len(events) != 0 {
		t.Errorf("unknown event: %+v", events)
	}
	if events := ProcessSSEEvent(sse(t, "content_block_stop", `{"type":"content_block_stop","index":0}`), state, false); len(events) != 0 {
		t.Errorf("dangling stop: %+v", events)
	}
}

// Ported: cache-breakpoint behavior tests.
func TestAddMessageCacheBreakpoint(t *testing.T) {
	// No messages: no panic.
	var empty []ApiMessage
	AddMessageCacheBreakpoint(empty, false)

	// Too few messages: no markers.
	few := []ApiMessage{
		{Role: "user", Content: []ContentBlock{TextContent{Text: "Hello"}}},
		{Role: "user", Content: []ContentBlock{TextContent{Text: "World"}}},
	}
	AddMessageCacheBreakpoint(few, false)
	for _, msg := range few {
		for _, block := range msg.Content {
			if txt, ok := block.(TextContent); ok && txt.CacheControl != nil {
				t.Error("no markers under 3 messages")
			}
		}
	}

	// One assistant message: marker on it only.
	msgs := []ApiMessage{
		{Role: "user", Content: []ContentBlock{TextContent{Text: "Identity"}}},
		{Role: "user", Content: []ContentBlock{TextContent{Text: "Hello"}}},
		{Role: "assistant", Content: []ContentBlock{TextContent{Text: "Hi there!"}}},
		{Role: "user", Content: []ContentBlock{TextContent{Text: "How are you?"}}},
	}
	AddMessageCacheBreakpoint(msgs, false)
	if txt, ok := msgs[2].Content[0].(TextContent); !ok || txt.CacheControl == nil {
		t.Errorf("assistant marker: %+v", msgs[2].Content[0])
	}
	for i, msg := range msgs {
		if i == 2 {
			continue
		}
		for _, block := range msg.Content {
			if txt, ok := block.(TextContent); ok && txt.CacheControl != nil {
				t.Errorf("message %d must be uncached", i)
			}
		}
	}

	// Two assistant messages: newest WRITE, older READ.
	two := []ApiMessage{
		{Role: "user", Content: []ContentBlock{TextContent{Text: "q1"}}},
		{Role: "assistant", Content: []ContentBlock{TextContent{Text: "a1"}}},
		{Role: "user", Content: []ContentBlock{TextContent{Text: "q2"}}},
		{Role: "assistant", Content: []ContentBlock{ToolUseContent{ID: "t", Name: "Bash", Input: json.RawMessage(`{}`)}}},
		{Role: "user", Content: []ContentBlock{TextContent{Text: "q3"}}},
	}
	AddMessageCacheBreakpoint(two, false)
	if txt, ok := two[1].Content[0].(TextContent); !ok || txt.CacheControl == nil {
		t.Error("older assistant READ marker")
	}
	if tu, ok := two[3].Content[0].(ToolUseContent); !ok || tu.CacheControl == nil {
		t.Error("newest assistant WRITE marker on trailing tool_use")
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
