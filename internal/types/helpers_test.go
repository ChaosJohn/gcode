package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWithTimestampsUserText(t *testing.T) {
	ts := JTime{time.Date(2026, 8, 26, 7, 49, 56, 816612000, time.UTC)}
	msgs := []Message{{Role: RoleUser, Content: []ContentBlock{TextBlock("who are you")}, Timestamp: &ts}}
	tagged := WithTimestamps(msgs)
	want := "[2026-08-26T07:49:56.816Z] who are you"
	if got := *tagged[0].Content[0].Text; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Input must not be mutated (history keeps raw timestamps).
	if got := *msgs[0].Content[0].Text; got != "who are you" {
		t.Errorf("input mutated: %q", got)
	}
}

func TestWithTimestampsToolResult(t *testing.T) {
	ts := JTime{time.Date(2026, 8, 26, 7, 50, 13, 96111000, time.UTC)}
	dur := uint64(812)
	msgs := []Message{{Role: RoleUser, Content: []ContentBlock{ToolResultBlock("t1", "done", false)}, Timestamp: &ts, ToolDurationMs: &dur}}
	tagged := WithTimestamps(msgs)
	want := "[tool timing: start=2026-08-26T07:50:12.284Z finish=2026-08-26T07:50:13.096Z duration=812ms] done"
	// start = finish - 812ms
	got := *tagged[0].Content[0].Content
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestWithTimestampsSkipsSystemRemindersAndAssistant(t *testing.T) {
	ts := Now()
	reminder := Message{Role: RoleUser, Content: []ContentBlock{TextBlock("<system-reminder>\n# Session Context\n</system-reminder>")}, Timestamp: &ts}
	assistant := Message{Role: RoleAssistant, Content: []ContentBlock{TextBlock("hi")}, Timestamp: &ts}
	tagged := WithTimestamps([]Message{reminder, assistant})
	if *tagged[0].Content[0].Text != "<system-reminder>\n# Session Context\n</system-reminder>" {
		t.Errorf("reminder must be untouched: %q", *tagged[0].Content[0].Text)
	}
	if *tagged[1].Content[0].Text != "hi" {
		t.Errorf("assistant must be untouched: %q", *tagged[1].Content[0].Text)
	}
}

func TestEndsWithFreshUserTurn(t *testing.T) {
	ts := Now()
	if EndsWithFreshUserTurn(nil) {
		t.Error("empty transcript has no fresh turn")
	}
	if EndsWithFreshUserTurn([]Message{{Role: RoleAssistant, Content: []ContentBlock{TextBlock("hi")}, Timestamp: &ts}}) {
		t.Error("assistant tail is not fresh")
	}
	toolMsg := Message{Role: RoleUser, Content: []ContentBlock{ToolResultBlock("t1", "out", false)}, Timestamp: &ts}
	if EndsWithFreshUserTurn([]Message{toolMsg}) {
		t.Error("tool-result tail is not fresh")
	}
	fresh := NewUserMessage("go build it")
	if !EndsWithFreshUserTurn([]Message{toolMsg, fresh}) {
		t.Error("user prompt tail is fresh")
	}
	reminder := Message{Role: RoleUser, Content: []ContentBlock{TextBlock("<system-reminder>x</system-reminder>")}, Timestamp: &ts}
	if EndsWithFreshUserTurn([]Message{toolMsg, reminder}) {
		t.Error("system-reminder tail is not fresh")
	}
}

func TestMessagesWithDynamicSystemContext(t *testing.T) {
	first := NewUserMessage("first task")
	second := NewUserMessage("second task")
	toolMsg := Message{Role: RoleUser, Content: []ContentBlock{ToolResultBlock("t1", "out", false)}}
	out := MessagesWithDynamicSystemContext([]Message{first, toolMsg, second}, "repo map here")
	if len(out) != 4 {
		t.Fatalf("len = %d", len(out))
	}
	// Dynamic context must be inserted after the latest fresh user prompt.
	last := out[len(out)-1]
	text, _ := last.Content[0].AsText()
	if text != "<system-reminder>\nrepo map here\n</system-reminder>" {
		t.Errorf("tail = %q", text)
	}
	if out[2].Content[0].Text == nil || *out[2].Content[0].Text != "second task" {
		t.Errorf("fresh prompt displaced: %+v", out[2])
	}
	// Empty context is a no-op.
	if got := MessagesWithDynamicSystemContext([]Message{first}, "   "); len(got) != 1 {
		t.Errorf("empty context must be a no-op, got %d", len(got))
	}
}

// Ported upstream: cache_relevant_hashes_ignore_non_transmitted_metadata.
func TestCacheRelevantHashesIgnoreNonTransmittedMetadata(t *testing.T) {
	ts := Now()
	base := Message{Role: RoleAssistant, Content: []ContentBlock{TextBlock("stable body")}, Timestamp: &ts}
	variants := []Message{base, base, base, base, base, base}
	variants[1].Timestamp = nil
	d := uint64(42)
	variants[2].ToolDurationMs = &d
	variants[3].Content = []ContentBlock{{Type: BlockReasoningTrace, Text: strPtr("internal trace")}, TextBlock("stable body")}
	variants[4].Content = []ContentBlock{{Type: BlockText, Text: strPtr("stable body"), CacheControl: NewEphemeralCacheControl("5m")}}
	// Not a text block; cache_control on it is provider-specific for the
	// current request and must still affect the cache key.
	variants[5].Content = []ContentBlock{TextBlock("stable body"), {Type: BlockReasoning, Text: strPtr("visible reasoning"), CacheControl: NewEphemeralCacheControl("5m")}}

	hashes := CacheRelevantMessageHashes(variants)
	// timestamp / tool_duration_ms / reasoning_trace / text cache_control
	// must not change the projection hash.
	for i := 1; i <= 4; i++ {
		if hashes[i] != hashes[0] {
			t.Errorf("variant %d hash differs: %#x vs %#x", i, hashes[i], hashes[0])
		}
	}
	// Non-text cache_control must change it.
	if hashes[5] == hashes[0] {
		t.Error("non-text cache_control must affect the cache key")
	}
}

func TestCacheRelevantProjectionShape(t *testing.T) {
	ts := JTime{time.Date(2026, 8, 26, 7, 0, 0, 0, time.UTC)}
	m := Message{
		Role: RoleUser, Timestamp: &ts,
		Content: []ContentBlock{{Type: BlockText, Text: strPtr("hi"), CacheControl: NewEphemeralCacheControl("5m")}},
	}
	proj := CacheRelevantMessages([]Message{m})[0]
	want := `{"role":"user","content":[{"type":"text","text":"hi"}]}`
	if string(proj) != want {
		t.Errorf("\n got %s\nwant %s", proj, want)
	}
}

func TestToolOutputEnvelope(t *testing.T) {
	out := NewToolOutput("body").WithTitle("t").WithMetadata(map[string]any{"k": 1}).WithImage("image/png", "AAA").WithLabeledImage("image/png", "BBB", "chart")
	if out.Title == nil || *out.Title != "t" {
		t.Error("title")
	}
	if len(out.Images) != 2 || out.Images[1].Label == nil || *out.Images[1].Label != "chart" {
		t.Errorf("images: %+v", out.Images)
	}
	if out.Metadata == nil {
		t.Error("metadata")
	}
}

func TestTelemetryCategories(t *testing.T) {
	cases := map[string]TelemetryToolCategory{
		"read": CategoryReadSearch, "agentgrep": CategoryReadSearch, "session_search": CategoryReadSearch,
		"apply_patch": CategoryWrite, "replace": CategoryWrite,
		"bash": CategoryShell, "bg": CategoryShell, "schedule": CategoryShell,
		"mcp_call": CategoryOther,
	}
	for name, want := range cases {
		if got := ClassifyTelemetryToolCategory(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestStreamEventContract(t *testing.T) {
	// All events implement the marker; a smoke check of representative ones.
	var events []StreamEvent = []StreamEvent{
		TextDelta{Text: "x"}, TextSuffixReplace{OldText: "a", NewText: "b"}, TextDone{},
		ToolUseStart{ID: "t", Name: "bash"}, ToolInputDelta{Delta: "{"}, ToolInputDeltaFor{ID: "t", Delta: "{"},
		ToolUseEnd{}, ToolUseEndFor{ID: "t"}, ToolUseSignature{Signature: "s"}, ToolUseSignatureFor{ID: "t", Signature: "s"},
		ToolResultEvent{ToolUseID: "t", Content: "c", IsError: true},
		GeneratedImage{ID: "g", Path: "/tmp/x.png", OutputFormat: "png"},
		ThinkingStart{}, ThinkingDelta{Delta: "…"}, ThinkingSignatureDelta{Delta: "s"},
		OpenAIReasoningItem{ID: "r", Summary: []string{"s"}},
		ThinkingEnd{}, ThinkingDone{DurationSecs: 1.5},
		MessageEnd{}, RetryRollback{Attempt: 1, Max: 3},
		TokenUsageEvent{InputTokens: u64Ptr(1)},
		ConnectionTypeEvent{Connection: "https"}, ConnectionPhaseEvent{Phase: PhaseStreaming},
		StatusDetailEvent{Detail: "d"}, ServiceTierEvent{Tier: "priority"},
		ErrorEvent{Message: "rate limited", RetryAfterSecs: u64Ptr(30)},
		SessionIDEvent{ID: "s"}, CompactionEvent{Trigger: "auto"},
		UpstreamProviderEvent{Provider: "openai"}, NativeToolCall{ToolName: "bash", Input: json.RawMessage(`{}`)},
		ProviderNativeEvent{Provider: "anthropic", Item: json.RawMessage(`{}`)},
	}
	if len(events) != 31 {
		t.Fatalf("event count = %d", len(events))
	}
	for _, e := range events {
		if e == nil {
			t.Fatal("nil event")
		}
	}
}

func u64Ptr(v uint64) *uint64 { return &v }
