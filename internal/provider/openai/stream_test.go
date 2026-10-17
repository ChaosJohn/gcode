package openai

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/types"
)

// streamHarness feeds SSE frames through PumpBytes and collects events.
type streamHarness struct {
	t      *testing.T
	state  *ResponsesStreamState
	pump   func([]byte) error
	events []types.StreamEvent
	pos    int
}

func newStreamHarness(t *testing.T) *streamHarness {
	t.Helper()
	h := &streamHarness{t: t, state: NewResponsesStreamState()}
	h.pump = PumpBytes(h.state, func(event types.StreamEvent) error {
		h.events = append(h.events, event)
		return nil
	})
	return h
}

func (h *streamHarness) send(payload string) {
	h.t.Helper()
	if err := h.pump([]byte("data: " + payload + "\n\n")); err != nil {
		h.t.Fatalf("pump: %v", err)
	}
}

func (h *streamHarness) sendJSON(v any) {
	h.t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		h.t.Fatal(err)
	}
	h.send(string(encoded))
}

// next returns the next buffered event.
func (h *streamHarness) next() types.StreamEvent {
	h.t.Helper()
	if h.pos >= len(h.events) {
		h.t.Fatalf("no event buffered (have %d, consumed %d)", len(h.events), h.pos)
	}
	event := h.events[h.pos]
	h.pos++
	return event
}

// idle asserts the pump emitted nothing more.
func (h *streamHarness) idle() {
	h.t.Helper()
	if h.pos < len(h.events) {
		h.t.Fatalf("expected idle, have %d unconsumed events starting %v",
			len(h.events)-h.pos, h.events[h.pos])
	}
}

func (h *streamHarness) assertStart(id, name string) {
	h.t.Helper()
	start, ok := h.next().(types.ToolUseStart)
	if !ok {
		h.t.Fatalf("expected ToolUseStart, got %#v", h.events[h.pos-1])
	}
	if start.ID != id || start.Name != name {
		h.t.Fatalf("start = %s/%s, want %s/%s", start.ID, start.Name, id, name)
	}
}

func (h *streamHarness) assertDelta(id, delta string) {
	h.t.Helper()
	event, ok := h.next().(types.ToolInputDeltaFor)
	if !ok {
		h.t.Fatalf("expected ToolInputDeltaFor, got %#v", h.events[h.pos-1])
	}
	if event.ID != id || event.Delta != delta {
		h.t.Fatalf("delta = %s/%q, want %s/%q", event.ID, event.Delta, id, delta)
	}
}

func (h *streamHarness) assertEnd(id string) {
	h.t.Helper()
	event, ok := h.next().(types.ToolUseEndFor)
	if !ok {
		h.t.Fatalf("expected ToolUseEndFor, got %#v", h.events[h.pos-1])
	}
	if event.ID != id {
		h.t.Fatalf("end id = %s, want %s", event.ID, id)
	}
}

func addedEvent(h *streamHarness, id, name string) {
	h.sendJSON(map[string]any{"type": "response.output_item.added", "item": map[string]any{
		"type": "function_call", "id": id, "call_id": "call_" + id,
		"name": name, "arguments": "",
	}})
}

func deltaEvent(h *streamHarness, id, fragment string) {
	h.sendJSON(map[string]any{"type": "response.function_call_arguments.delta",
		"item_id": id, "delta": fragment})
}

func doneEvent(h *streamHarness, id, arguments string) {
	h.sendJSON(map[string]any{"type": "response.function_call_arguments.done",
		"item_id": id, "arguments": arguments})
}

// Ported: service_tier_metadata_is_provider_reported_and_precedes_completion.
func TestServiceTierPrecedesCompletion(t *testing.T) {
	for _, kind := range []string{"response.created", "response.completed"} {
		for _, tier := range []string{"default", "priority", "ultrafast"} {
			h := newStreamHarness(t)
			h.sendJSON(map[string]any{"type": kind, "response": map[string]any{"service_tier": tier}})
			event, ok := h.next().(types.ServiceTierEvent)
			if !ok || event.Tier != tier || event.Requested != nil {
				t.Fatalf("%s/%s: got %#v", kind, tier, h.events)
			}
			if kind == "response.completed" {
				if _, ok := h.next().(types.MessageEnd); !ok {
					t.Fatalf("completed must end with MessageEnd: %#v", h.events)
				}
			}
			h.idle()
		}
	}
}

// Ported: service_tier_metadata_does_not_infer_missing_or_invalid_tiers.
func TestServiceTierNotInferred(t *testing.T) {
	for _, response := range []map[string]any{
		{}, {"service_tier": nil}, {"service_tier": 3}, {"service_tier": " "},
	} {
		h := newStreamHarness(t)
		h.sendJSON(map[string]any{"type": "response.created", "response": response})
		h.idle()
	}
}

// Ported: usage_preserves_inclusive_input_and_optional_cache_details.
func TestUsagePreservesDetails(t *testing.T) {
	cases := []struct {
		details     string
		read, write *uint64
	}{
		{`{"cached_tokens":12000,"cache_write_tokens":3000}`, u64p(12000), u64p(3000)},
		{`{"cached_tokens":0,"cache_write_tokens":15000}`, u64p(0), u64p(15000)},
		{`{"cached_tokens":12000}`, u64p(12000), nil},
		{`{}`, nil, nil},
		{`null`, nil, nil},
		{`{"cached_tokens":-1,"cache_write_tokens":"3000"}`, nil, nil},
	}
	for i, tc := range cases {
		// prompt_tokens_details compatibility key behaves identically.
		for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
			response := fmt.Sprintf(`{"usage":{"input_tokens":15000,"output_tokens":200,"%s":%s}}`, key, tc.details)
			event, ok := extractUsageFromResponse(json.RawMessage(response))
			if !ok {
				t.Fatalf("case %d %s: missing usage", i, key)
			}
			usage := event.(types.TokenUsageEvent)
			if usage.InputTokens == nil || *usage.InputTokens != 15000 ||
				usage.OutputTokens == nil || *usage.OutputTokens != 200 {
				t.Fatalf("case %d %s: totals %#v", i, key, usage)
			}
			if !u64Eq(usage.CacheReadInputTokens, tc.read) ||
				!u64Eq(usage.CacheCreationInputTokens, tc.write) {
				t.Fatalf("case %d %s: details %#v", i, key, usage)
			}
		}
	}
	// Both keys present: input_tokens_details wins per field (find_map order).
	response := `{"usage":{"input_tokens":1,"output_tokens":2,
		"input_tokens_details":{"cached_tokens":null},
		"prompt_tokens_details":{"cached_tokens":12000,"cache_write_tokens":3000}}}`
	event, ok := extractUsageFromResponse(json.RawMessage(response))
	if !ok {
		t.Fatal("both keys: missing usage")
	}
	usage := event.(types.TokenUsageEvent)
	if !u64Eq(usage.CacheReadInputTokens, u64p(12000)) {
		t.Fatalf("both keys: %#v", usage)
	}
}

func u64p(v uint64) *uint64 { return &v }

func u64Eq(a, b *uint64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Ported: terminal_responses_emit_cache_write_only_usage_before_message_end.
func TestCacheWriteOnlyUsageBeforeEnd(t *testing.T) {
	for _, kind := range []string{"response.completed", "response.incomplete"} {
		h := newStreamHarness(t)
		h.sendJSON(map[string]any{"type": kind, "response": map[string]any{
			"usage": map[string]any{"input_tokens_details": map[string]any{"cache_write_tokens": 1024}},
		}})
		event, ok := h.next().(types.TokenUsageEvent)
		if !ok || event.InputTokens != nil || event.OutputTokens != nil ||
			event.CacheReadInputTokens != nil ||
			event.CacheCreationInputTokens == nil || *event.CacheCreationInputTokens != 1024 {
			t.Fatalf("%s: got %#v", kind, h.events)
		}
		if _, ok := h.next().(types.MessageEnd); !ok {
			t.Fatalf("%s: MessageEnd missing", kind)
		}
		h.idle()
	}
}

// Ported: missing_usage_does_not_report_a_cache_miss.
func TestMissingUsageNoCacheMiss(t *testing.T) {
	for _, response := range []string{
		`{}`, `{"usage":null}`, `{"usage":{}}`, `{"usage":{"input_tokens_details":{}}}`,
	} {
		if _, ok := extractUsageFromResponse(json.RawMessage(response)); ok {
			t.Errorf("usage reported for %s", response)
		}
	}
}

// Ported: structured_stream_read_error_is_extracted_and_classified_as_transient.
func TestStructuredStreamReadError(t *testing.T) {
	message, retryAfter := extractErrorWithRetry(nil,
		json.RawMessage(`{"type":"upstream_error","code":"stream_read_error"}`))
	want := "upstream_error (stream_read_error): OpenAI response stream error (unknown)"
	if message != want || retryAfter != nil {
		t.Fatalf("message = %q retry = %v", message, retryAfter)
	}
}

// Ported: parse_text_wrapped_tool_call_rejects_non_object_json.
func TestTextWrappedRejectsNonObject(t *testing.T) {
	if _, _, _, _, ok := ParseTextWrappedToolCall("prefix to=functions.read [1,2,3]"); ok {
		t.Fatal("array must be rejected")
	}
}

// Ported: parse_openai_response_event_ignores_malformed_json_chunks.
func TestMalformedJSONIgnored(t *testing.T) {
	h := newStreamHarness(t)
	h.send("{not-json}")
	h.idle()
}

// Ported: response_completed_emits_message_end_even_when_payload_mentions_fallback.
func TestCompletedWithFallbackPhraseInPayload(t *testing.T) {
	h := newStreamHarness(t)
	h.sendJSON(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
			"output": []any{map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{
					"type": "output_text",
					"text": "falling back from websockets to https transport",
				}},
			}},
		},
	})
	if _, ok := h.next().(types.MessageEnd); !ok {
		t.Fatalf("expected MessageEnd, got %#v", h.events)
	}
}

// Ported: function_call_arguments_with_fallback_phrase_still_emit_tool_call.
func TestArgumentsWithFallbackPhrase(t *testing.T) {
	h := newStreamHarness(t)
	h.sendJSON(map[string]any{
		"type": "response.function_call_arguments.done", "item_id": "fc_1",
		"call_id": "call_1", "name": "bash",
		"arguments": `{"command":"echo falling back from websockets to https transport"}`,
	})
	h.assertStart("call_1", "bash")
	h.assertDelta("call_1", `{"command":"echo falling back from websockets to https transport"}`)
	h.assertEnd("call_1")
	h.idle()
}

// Ported: plain_text_fallback_notice_is_still_dropped.
func TestPlainTextFallbackDropped(t *testing.T) {
	h := newStreamHarness(t)
	h.send("falling back from websockets to https transport")
	h.idle()
}

// Ported: tool_name_and_argument_fragments_are_visible_before_done.
func TestFragmentsVisibleBeforeDone(t *testing.T) {
	for _, name := range []string{"read", "batch", "multi_tool_use.parallel"} {
		h := newStreamHarness(t)
		addedEvent(h, "a", name)
		h.assertStart("call_a", name)
		h.idle()
		deltaEvent(h, "a", `{"intent":"Read files",`)
		h.assertDelta("call_a", `{"intent":"Read files",`)
		h.idle()
		deltaEvent(h, "a", `"path":"文档"}`)
		h.assertDelta("call_a", `"path":"文档"}`)
		h.idle()
		doneEvent(h, "a", `{"intent":"Read files","path":"文档"}`)
		h.assertEnd("call_a")
		h.idle()
	}
}

// Ported: done_snapshots_only_emit_unseen_suffix_and_never_duplicate_calls.
func TestDoneSnapshotOnlyUnseenSuffix(t *testing.T) {
	h := newStreamHarness(t)
	addedEvent(h, "a", "read")
	h.assertStart("call_a", "read")
	deltaEvent(h, "a", `{"path":`)
	h.assertDelta("call_a", `{"path":`)
	doneEvent(h, "a", `{"path":"README.md"}`)
	h.assertDelta("call_a", `"README.md"}`)
	h.assertEnd("call_a")
	for i := 0; i < 2; i++ {
		doneEvent(h, "a", `{"path":"README.md"}`)
		h.sendJSON(map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"id": "a", "type": "function_call", "call_id": "call_a", "name": "read",
			"arguments": `{"path":"README.md"}`,
		}})
		h.idle()
	}
}

// Ported: output_item_done_finishes_started_call_without_arguments_done.
func TestOutputItemDoneFinishesCall(t *testing.T) {
	h := newStreamHarness(t)
	addedEvent(h, "a", "read")
	h.assertStart("call_a", "read")
	deltaEvent(h, "a", "{")
	h.assertDelta("call_a", "{")
	h.sendJSON(map[string]any{"type": "response.output_item.done", "item": map[string]any{
		"id": "a", "type": "function_call", "call_id": "call_a", "name": "read", "arguments": "{}",
	}})
	h.assertDelta("call_a", "}")
	h.assertEnd("call_a")
	h.idle()
	if len(h.state.streamingToolCalls) != 0 {
		t.Fatalf("calls remain: %v", h.state.streamingToolCalls)
	}
}

// Ported: stalled_first_call_does_not_block_other_names_arguments_or_completion.
func TestStalledCallDoesNotBlock(t *testing.T) {
	h := newStreamHarness(t)
	addedEvent(h, "a", "read")
	h.assertStart("call_a", "read")
	h.idle()
	addedEvent(h, "b", "bash")
	h.assertStart("call_b", "bash")
	h.idle()
	deltaEvent(h, "b", `{"command":`)
	h.assertDelta("call_b", `{"command":`)
	addedEvent(h, "c", "ls")
	h.assertStart("call_c", "ls")
	h.idle()
	deltaEvent(h, "a", `{"path":`)
	h.assertDelta("call_a", `{"path":`)
	doneEvent(h, "c", "")
	h.assertDelta("call_c", "{}")
	h.assertEnd("call_c")
	doneEvent(h, "b", `{"command":"pwd"}`)
	h.assertDelta("call_b", `"pwd"}`)
	h.assertEnd("call_b")
	h.idle()
	doneEvent(h, "a", `{"path":"README.md"}`)
	h.assertDelta("call_a", `"README.md"}`)
	h.assertEnd("call_a")
	h.idle()
}

// Ported: emitted_id_stays_stable_when_call_id_arrives_late.
func TestEmittedIDStableWhenCallIDLate(t *testing.T) {
	h := newStreamHarness(t)
	h.sendJSON(map[string]any{"type": "response.output_item.added", "item": map[string]any{
		"type": "function_call", "id": "item.a", "name": "read", "arguments": "",
	}})
	start, ok := h.next().(types.ToolUseStart)
	if !ok || start.ID != "item_a" { // sanitize_tool_id maps '.' to '_'
		t.Fatalf("start = %#v", start)
	}
	h.sendJSON(map[string]any{"type": "response.function_call_arguments.delta",
		"item_id": "item.a", "call_id": "late_call", "delta": "{"})
	delta, ok := h.next().(types.ToolInputDeltaFor)
	if !ok || delta.ID != "item_a" || delta.Delta != "{" {
		t.Fatalf("delta = %#v", delta)
	}
	h.sendJSON(map[string]any{"type": "response.output_item.done", "item": map[string]any{
		"type": "function_call", "id": "item.a", "call_id": "different_call",
		"name": "read", "arguments": "{}",
	}})
	delta, ok = h.next().(types.ToolInputDeltaFor)
	if !ok || delta.ID != "item_a" || delta.Delta != "}" {
		t.Fatalf("suffix = %#v", delta)
	}
	end, ok := h.next().(types.ToolUseEndFor)
	if !ok || end.ID != "item_a" {
		t.Fatalf("end = %#v", end)
	}
	h.idle()
}

// Ported: late_name_releases_accumulated_arguments_without_waiting_for_done.
func TestLateNameReleasesArguments(t *testing.T) {
	h := newStreamHarness(t)
	deltaEvent(h, "a", "{")
	h.idle()
	h.sendJSON(map[string]any{"type": "response.function_call_arguments.delta",
		"item_id": "a", "call_id": "call_a", "name": "read", "delta": `"path":`})
	h.assertStart("call_a", "read")
	h.assertDelta("call_a", `{"path":`)
	h.idle()
}

// Ported: done_only_call_keeps_compatibility.
func TestDoneOnlyCall(t *testing.T) {
	h := newStreamHarness(t)
	h.sendJSON(map[string]any{"type": "response.function_call_arguments.done",
		"item_id": "a", "call_id": "call_a", "name": "read", "arguments": "{}"})
	h.assertStart("call_a", "read")
	h.assertDelta("call_a", "{}")
	h.assertEnd("call_a")
	h.idle()
}

// Ported: null_and_empty_arguments_are_normalized_without_delaying_start.
func TestNullArgumentsNormalized(t *testing.T) {
	for _, arguments := range []string{"", " ", "null", " null "} {
		h := newStreamHarness(t)
		addedEvent(h, "a", "ls")
		h.assertStart("call_a", "ls")
		for _, ch := range arguments {
			deltaEvent(h, "a", string(ch))
			h.idle()
		}
		doneEvent(h, "a", arguments)
		h.assertDelta("call_a", "{}")
		h.assertEnd("call_a")
		h.idle()
	}
}

// Ported: mismatched_done_arguments_fail_instead_of_corrupting_tool_input.
func TestMismatchedDoneFails(t *testing.T) {
	h := newStreamHarness(t)
	addedEvent(h, "a", "read")
	h.assertStart("call_a", "read")
	deltaEvent(h, "a", `{"path":"文档`)
	h.assertDelta("call_a", `{"path":"文档`)
	doneEvent(h, "a", "{}")
	if _, ok := h.next().(types.ErrorEvent); !ok {
		t.Fatalf("expected error, got %#v", h.events)
	}
	h.idle()
}

// Ported: custom_tool_input_events_stream_before_completion.
func TestCustomToolInputStreams(t *testing.T) {
	h := newStreamHarness(t)
	h.sendJSON(map[string]any{"type": "response.output_item.added", "item": map[string]any{
		"id": "a", "type": "custom_tool_call", "call_id": "call_a",
		"name": "apply_patch", "input": "",
	}})
	h.assertStart("call_a", "apply_patch")
	h.sendJSON(map[string]any{"type": "response.custom_tool_call_input.delta",
		"item_id": "a", "delta": "*** Begin Patch\n"})
	h.assertDelta("call_a", "*** Begin Patch\n")
	h.idle()
	h.sendJSON(map[string]any{"type": "response.custom_tool_call_input.done",
		"item_id": "a", "input": "*** Begin Patch\n*** End Patch"})
	h.assertDelta("call_a", "*** End Patch")
	h.assertEnd("call_a")
	h.idle()
}

// Ported: unnamed_first_call_does_not_block_a_named_call.
func TestUnnamedCallDoesNotBlock(t *testing.T) {
	h := newStreamHarness(t)
	deltaEvent(h, "a", "{")
	h.idle()
	addedEvent(h, "b", "ls")
	h.assertStart("call_b", "ls")
	doneEvent(h, "b", "{}")
	h.assertDelta("call_b", "{}")
	h.assertEnd("call_b")
	h.idle()
	h.sendJSON(map[string]any{"type": "response.function_call_arguments.done",
		"item_id": "a", "call_id": "call_a", "name": "read", "arguments": "{}"})
	h.assertStart("call_a", "read")
	h.assertDelta("call_a", "{}")
	h.assertEnd("call_a")
	h.idle()
}

// Ported: recovered_tool_call_uses_its_own_id_while_another_call_is_open.
func TestRecoveredToolCallOwnID(t *testing.T) {
	h := newStreamHarness(t)
	addedEvent(h, "a", "read")
	h.assertStart("call_a", "read")
	h.sendJSON(map[string]any{"type": "response.output_text.delta",
		"delta": "to=functions.ls {}"})
	start, ok := h.next().(types.ToolUseStart)
	if !ok || start.Name != "ls" || !strings.HasPrefix(start.ID, "fallback_text_call_") {
		t.Fatalf("start = %#v", start)
	}
	delta, ok := h.next().(types.ToolInputDeltaFor)
	if !ok || delta.ID != start.ID || delta.Delta != "{}" {
		t.Fatalf("delta = %#v", delta)
	}
	end, ok := h.next().(types.ToolUseEndFor)
	if !ok || end.ID != start.ID {
		t.Fatalf("end = %#v", end)
	}
	doneEvent(h, "a", "{}")
	h.assertDelta("call_a", "{}")
	h.assertEnd("call_a")
	h.idle()
}

// Ported: unstable_item_ids_stream_incrementally_and_deduplicate_done_snapshots.
func TestUnstableItemIDsDedupe(t *testing.T) {
	for _, custom := range []bool{false, true} {
		for _, itemDoneOnly := range []bool{false, true} {
			h := newStreamHarness(t)
			name := "read"
			fragments := []string{`{"path":`, `"文档`, `"}`}
			if custom {
				name = "apply_patch"
				fragments = []string{"*** Begin Patch\n", "*** End", " Patch"}
			}
			full := strings.Join(fragments, "")
			indexedItem(h, 0, "added", custom, false, "")
			h.assertStart("call_0", name)
			h.idle()
			for i := 0; i < 2; i++ {
				indexedArguments(h, 0, fmt.Sprintf("delta_%d", i), custom, false, fragments[i])
				h.assertDelta("call_0", fragments[i])
				h.idle()
			}
			if itemDoneOnly {
				indexedItem(h, 0, "item_done", custom, true, full)
			} else {
				indexedArguments(h, 0, "arguments_done", custom, true, full)
			}
			h.assertDelta("call_0", fragments[2])
			h.assertEnd("call_0")
			indexedItem(h, 0, "another_item_done", custom, true, full)
			indexedArguments(h, 0, "another_arguments_done", custom, true, full)
			h.idle()
			if len(h.state.streamingToolCalls) != 0 {
				t.Fatalf("calls remain: %v", h.state.streamingToolCalls)
			}
		}
	}
}

// Ported: unstable_item_ids_keep_interleaved_calls_separate.
func TestUnstableIDsInterleaved(t *testing.T) {
	h := newStreamHarness(t)
	indexedItem(h, 0, "a_added", false, false, "")
	h.assertStart("call_0", "read")
	indexedArguments(h, 0, "a_delta", false, false, "{")
	h.assertDelta("call_0", "{")
	indexedItem(h, 1, "b_added", true, false, "")
	h.assertStart("call_1", "apply_patch")
	indexedArguments(h, 1, "b_delta", true, false, "*** Begin Patch\n")
	h.assertDelta("call_1", "*** Begin Patch\n")
	indexedArguments(h, 1, "b_done", true, true, "*** Begin Patch\n*** End Patch")
	h.assertDelta("call_1", "*** End Patch")
	h.assertEnd("call_1")
	indexedItem(h, 1, "b_item_done", true, true, "*** Begin Patch\n*** End Patch")
	h.idle()
	indexedItem(h, 0, "a_item_done", false, true, "{}")
	h.assertDelta("call_0", "}")
	h.assertEnd("call_0")
	indexedItem(h, 0, "a_duplicate", false, true, "{}")
	indexedItem(h, 1, "b_duplicate", true, true, "*** Begin Patch\n*** End Patch")
	h.idle()
	if len(h.state.streamingToolCalls) != 0 {
		t.Fatalf("calls remain: %v", h.state.streamingToolCalls)
	}
}

func indexedItem(h *streamHarness, index uint64, id string, custom, complete bool, input string) {
	h.t.Helper()
	itemType, nameField, name := "function_call", "arguments", "read"
	if custom {
		itemType, nameField, name = "custom_tool_call", "input", "apply_patch"
	}
	kind := "response.output_item.added"
	if complete {
		kind = "response.output_item.done"
	}
	h.sendJSON(map[string]any{"type": kind, "output_index": index,
		"item": map[string]any{
			"type": itemType, "id": id, "call_id": fmt.Sprintf("call_%d", index),
			"name": name, nameField: input,
		}})
}

func indexedArguments(h *streamHarness, index uint64, id string, custom, complete bool, input string) {
	h.t.Helper()
	kind := "function_call_arguments"
	if custom {
		kind = "custom_tool_call_input"
	}
	suffix := "delta"
	payload := map[string]any{"type": fmt.Sprintf("response.%s.%s", kind, suffix),
		"output_index": index, "item_id": id, "delta": input}
	if complete {
		payload["type"] = fmt.Sprintf("response.%s.done", kind)
		delete(payload, "delta")
		if custom {
			payload["input"] = input
		} else {
			payload["arguments"] = input
		}
	}
	h.sendJSON(payload)
}
