package types

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestJTimeAutoSi(t *testing.T) {
	cases := map[string]string{
		"2026-08-26T07:49:52.870523Z":      "2026-08-26T07:49:52.870523Z",    // micros, as in real sessions
		"2026-08-26T07:49:52Z":             "2026-08-26T07:49:52Z",           // zero fraction stays bare
		"2026-08-26T07:49:52.5Z":           "2026-08-26T07:49:52.500Z",       // 3 digits
		"2026-08-26T07:49:52.123456789Z":   "2026-08-26T07:49:52.123456789Z", // 9 digits
		"2026-08-26T15:49:52.870523+08:00": "2026-08-26T07:49:52.870523Z",    // offsets normalize to UTC
	}
	for in, want := range cases {
		var jt JTime
		if err := json.Unmarshal([]byte(`"`+in+`"`), &jt); err != nil {
			t.Fatalf("parse %q: %v", in, err)
		}
		out, _ := json.Marshal(jt)
		if string(out) != `"`+want+`"` {
			t.Errorf("AutoSi(%q) = %s, want %q", in, out, want)
		}
	}
	var jt JTime
	if err := json.Unmarshal([]byte(`null`), &jt); err != nil {
		t.Errorf("null: %v", err)
	}
}

func TestSessionFixtureByteRoundTrip(t *testing.T) {
	raw, err := os.ReadFile("testdata/jcode/session_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(s.Messages) != 5 {
		t.Fatalf("messages = %d, want 5", len(s.Messages))
	}
	// The fixture is written in jcode's own serde field order; re-encoding
	// must be byte-identical (no HTML escaping, serde order).
	out, err := MarshalCompact(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(raw) {
		t.Fatalf("round-trip mismatch:\n--- got ---\n%s\n--- want ---\n%s", out, raw)
	}
}

func TestSessionStatusVariants(t *testing.T) {
	// Unit variants serialize as bare PascalCase strings (externally tagged).
	for _, kind := range []string{StatusActive, StatusClosed, StatusReloaded, StatusCompacted, StatusRateLimited} {
		encoded, err := json.Marshal(SessionStatus{Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != `"`+kind+`"` {
			t.Errorf("status %s: got %s", kind, encoded)
		}
		var back SessionStatus
		if err := json.Unmarshal(encoded, &back); err != nil || back.Kind != kind {
			t.Errorf("status %s round-trip: %v %+v", kind, err, back)
		}
	}
	// Struct variants serialize as one-key objects.
	encoded, err := json.Marshal(SessionStatus{Kind: StatusCrashed, Message: strPtr("boom")})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"Crashed":{"message":"boom"}}` {
		t.Errorf("crashed: %s", encoded)
	}
	encoded, _ = json.Marshal(SessionStatus{Kind: StatusError, Message: strPtr("bad")})
	if string(encoded) != `{"Error":{"message":"bad"}}` {
		t.Errorf("error: %s", encoded)
	}
	var back SessionStatus
	if err := json.Unmarshal([]byte(`{"Crashed":{"message":null}}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.Kind != StatusCrashed || back.Message != nil {
		t.Errorf("crashed null message: %+v", back)
	}
	if err := json.Unmarshal([]byte(`"Exploded"`), &back); err == nil {
		t.Error("unknown status must error")
	}
	if got := (SessionStatus{Kind: StatusRateLimited}).Display(); got != "rate limited" {
		t.Errorf("display = %q", got)
	}
	if got := (SessionStatus{Kind: StatusClosed}).Display(); got != "closed" {
		t.Errorf("display = %q", got)
	}
}

func TestSessionImproveModeAliases(t *testing.T) {
	var m SessionImproveMode
	if err := json.Unmarshal([]byte(`"run"`), &m); err != nil || m != ImproveRun {
		t.Errorf("alias run: %v %q", err, m)
	}
	if err := json.Unmarshal([]byte(`"plan"`), &m); err != nil || m != ImprovePlan {
		t.Errorf("alias plan: %v %q", err, m)
	}
	if err := json.Unmarshal([]byte(`"improve_run"`), &m); err != nil || m != ImproveRun {
		t.Errorf("canonical: %v %q", err, m)
	}
	if err := json.Unmarshal([]byte(`"nope"`), &m); err == nil {
		t.Error("unknown improve mode must error")
	}
}

func TestTurnStopReasonUnknownFallback(t *testing.T) {
	var r TurnStopReason
	if err := json.Unmarshal([]byte(`"provider_guardrail"`), &r); err != nil || r != StopProviderGuardrail {
		t.Errorf("known: %v %q", err, r)
	}
	if err := json.Unmarshal([]byte(`"something_new"`), &r); err != nil || r != StopUnknown {
		t.Errorf("serde(other): %v %q", err, r)
	}
}

func TestResumeTargetRoundTrip(t *testing.T) {
	cases := []struct {
		target ResumeTarget
		want   string
	}{
		{ResumeTarget{Kind: "JcodeSession", SessionID: "s1"}, `{"JcodeSession":{"session_id":"s1"}}`},
		{ResumeTarget{Kind: "PiSession", SessionPath: "/p"}, `{"PiSession":{"session_path":"/p"}}`},
		{ResumeTarget{Kind: "CodexSession", SessionID: "s2", SessionPath: "/f"}, `{"CodexSession":{"session_id":"s2","session_path":"/f"}}`},
	}
	for _, tc := range cases {
		out, err := json.Marshal(tc.target)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != tc.want {
			t.Errorf("got %s, want %s", out, tc.want)
		}
		var back ResumeTarget
		if err := json.Unmarshal(out, &back); err != nil || back.Kind != tc.target.Kind || back.StableID() != tc.target.StableID() {
			t.Errorf("round-trip: %v %+v", err, back)
		}
	}
}

func TestReplayEventRoundTrip(t *testing.T) {
	raws := []string{
		`{"timestamp":"2026-08-26T07:50:00Z","event":"display_message","role":"system","content":"hello"}`,
		`{"timestamp":"2026-08-26T07:50:00Z","event":"swarm_status","members":[{"session_id":"s1"}]}`,
		`{"timestamp":"2026-08-26T07:50:00Z","event":"swarm_plan","swarm_id":"w","version":2,"items":[{"id":"i"}],"participants":["s1"],"reason":"r"}`,
	}
	for _, raw := range raws {
		var e StoredReplayEvent
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		out, _ := json.Marshal(e)
		if string(out) != raw {
			t.Errorf("\n got %s\nwant %s", out, raw)
		}
	}
}

func TestStoredMessageHelpers(t *testing.T) {
	msg := StoredMessage{
		ID: "m1", Role: RoleAssistant,
		Content: []ContentBlock{
			TextBlock(""),
			{Type: BlockToolUse, Name: strPtr("bash"), ID: strPtr("t1"), Input: json.RawMessage(`{}`)},
		},
	}
	if got := msg.ContentPreview(); got != "[tool: bash]" {
		t.Errorf("preview = %q", got)
	}
	msg.Content = []ContentBlock{TextBlock("line1\nline2")}
	if got := msg.ContentPreview(); got != "line1 line2" {
		t.Errorf("preview = %q", got)
	}
	msg.Content = nil
	if got := msg.ContentPreview(); got != "(empty)" {
		t.Errorf("preview = %q", got)
	}

	toMsg := msg.ToMessage()
	if toMsg.Role != msg.Role || len(toMsg.Content) != len(msg.Content) {
		t.Error("ToMessage mismatch")
	}
}

func TestPromptTitle(t *testing.T) {
	// Ported upstream tests.
	if got, ok := PromptTitle("  help me   improve the session sidebar\nplease "); !ok || got != "help me improve the session sidebar please" {
		t.Errorf("got %q ok=%v", got, ok)
	}
	long, ok := PromptTitle(repeat("word ", 40))
	if !ok {
		t.Fatal("long prompt must get a title")
	}
	if n := runeCount(long); n != 65 { // 64 chars + ellipsis
		t.Errorf("long title runes = %d, want 65 (64 + ellipsis)", n)
	}
	if got, ok := PromptTitle("<transcription>\nRename the sidebar rows.\n</transcription>"); !ok || got != "Rename the sidebar rows." {
		t.Errorf("transcription title: %q %v", got, ok)
	}
	if _, ok := PromptTitle("<system-reminder>\n# Session Context\n</system-reminder>"); ok {
		t.Error("system-reminder-only must produce no title")
	}
	if got, ok := PromptTitle("<system-reminder>ctx</system-reminder>\nFix the build"); !ok || got != "Fix the build" {
		t.Errorf("mixed: %q %v", got, ok)
	}
	if _, ok := PromptTitle(" /model gpt-5 "); ok {
		t.Error("slash command must produce no title")
	}
	if _, ok := PromptTitle("[Scheduled task]\nrun it"); ok {
		t.Error("scheduled task must produce no title")
	}
	if _, ok := PromptTitle("   "); ok {
		t.Error("blank must produce no title")
	}
}

func TestTranscriptionHelpers(t *testing.T) {
	wrapped := WrapTranscription("  fix the flaky test ")
	if wrapped != "<transcription>\nfix the flaky test\n</transcription>" {
		t.Errorf("wrap: %q", wrapped)
	}
	visible, found := StripTranscription(wrapped)
	if visible != "fix the flaky test" || !found {
		t.Errorf("strip: %q %v", visible, found)
	}
	mixed := "Typed first\n" + WrapTranscription("one") + "\nand " + WrapTranscription("two")
	visible, found = StripTranscription(mixed)
	if !found || visible != "Typed first\none\nand two" {
		t.Errorf("multi: %q %v", visible, found)
	}
	for _, text := range []string{"plain", "<transcription> open only", "close only </transcription>"} {
		visible, found = StripTranscription(text)
		if visible != text || found {
			t.Errorf("unbalanced %q: %q %v", text, visible, found)
		}
	}
}

func TestGoalIDSlugify(t *testing.T) {
	if got := SanitizeGoalID("Fix the Build! (v2)"); got != "fix-the-build-v2" {
		t.Errorf("got %q", got)
	}
	if got := SanitizeGoalID("  ---  "); got != "goal" {
		t.Errorf("empty slug fallback: %q", got)
	}
	// Non-ASCII collapses to dashes which are trimmed → fallback id.
	if got := SanitizeGoalID("中文 标题"); got != "goal" {
		t.Errorf("non-ASCII: %q", got)
	}
}

func TestSemanticStates(t *testing.T) {
	// String form.
	var c ConfidenceState
	if err := json.Unmarshal([]byte(`"validated"`), &c); err != nil || c != ConfidenceValidated {
		t.Errorf("string: %v %q", err, c)
	}
	// Legacy numeric score (88 → plausible, per upstream 60..=95 range).
	if err := json.Unmarshal([]byte(`88`), &c); err != nil || c != ConfidencePlausible {
		t.Errorf("legacy number: %v %q", err, c)
	}
	// Legacy numeric string.
	if err := json.Unmarshal([]byte(`"72"`), &c); err != nil || c != ConfidencePlausible {
		t.Errorf("legacy string: %v %q", err, c)
	}
	if err := json.Unmarshal([]byte(`"bogus"`), &c); err == nil {
		t.Error("invalid state must error")
	}
	if err := json.Unmarshal([]byte(`150`), &c); err == nil {
		t.Error("out-of-range score must error")
	}
	// IntentUnderstanding legacy ranges.
	var i IntentUnderstanding
	if err := json.Unmarshal([]byte(`95`), &i); err != nil || i != IntentPartial {
		t.Errorf("95 → partial: %v %q", err, i)
	}
	if err := json.Unmarshal([]byte(`96`), &i); err != nil || i != IntentClear {
		t.Errorf("96 → clear: %v %q", err, i)
	}
	if err := json.Unmarshal([]byte(`100`), &i); err != nil || i != IntentComplete {
		t.Errorf("100 → complete: %v %q", err, i)
	}
}

func TestTodoItemRoundTrip(t *testing.T) {
	raw := `{"content":"ship it","status":"in_progress","priority":"high","id":"t1","confidence":88,"completion_confidence":"verified","confidence_history":["speculative","plausible"],"blocked_by":["t0"]}`
	var item TodoItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatal(err)
	}
	if item.Confidence == nil || *item.Confidence != ConfidencePlausible {
		t.Errorf("legacy 88 → plausible: %v", item.Confidence)
	}
	out, _ := json.Marshal(item)
	want := `{"content":"ship it","status":"in_progress","priority":"high","id":"t1","confidence":"plausible","completion_confidence":"verified","confidence_history":["speculative","plausible"],"blocked_by":["t0"]}`
	if string(out) != want {
		t.Errorf("\n got %s\nwant %s", out, want)
	}
}

func TestCopilotUsageTracker(t *testing.T) {
	raw := `{"today":{"date":"2026-10-17","requests":3,"premium_requests":1,"input_tokens":10,"output_tokens":20},"month":{"month":"2026-10","requests":30,"premium_requests":10,"input_tokens":100,"output_tokens":200},"all_time":{"requests":300,"premium_requests":100,"input_tokens":1000,"output_tokens":2000}}`
	var tracker CopilotUsageTracker
	if err := json.Unmarshal([]byte(raw), &tracker); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(tracker)
	if string(out) != raw {
		t.Errorf("\n got %s\nwant %s", out, raw)
	}
}

func TestFormatTimestamp(t *testing.T) {
	ts, _ := time.Parse(time.RFC3339, "2026-08-26T07:49:52.870523Z")
	if got := FormatTimestamp(ts); got != "2026-08-26T07:49:52.870Z" {
		t.Errorf("got %q", got)
	}
}

func strPtr(s string) *string { return &s }

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func runeCount(s string) int { return len([]rune(s)) }
