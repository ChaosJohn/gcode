package gemini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/base/config"
	"github.com/ChaosJohn/gcode/internal/schema/dialect"
	"github.com/ChaosJohn/gcode/internal/types"
)

// Ported: build_contents_replays_thought_signature_on_function_call
func TestBuildContentsReplaysThoughtSignature(t *testing.T) {
	sig := "SIGNATURE_ABC"
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.BlockToolUse, ID: strPtr("call_sig"), Name: strPtr("read"),
				Input: json.RawMessage(`{"path":"README.md"}`), ThoughtSignature: &sig},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.BlockToolUse, ID: strPtr("call_nosig"), Name: strPtr("bash"),
				Input: json.RawMessage(`{"command":"ls"}`)},
		}},
	}
	contents := BuildContents(messages)
	if contents[0].Parts[0].ThoughtSignature == nil || *contents[0].Parts[0].ThoughtSignature != "SIGNATURE_ABC" {
		t.Fatalf("signature must be replayed on the matching function call part")
	}
	if contents[1].Parts[0].ThoughtSignature == nil || *contents[1].Parts[0].ThoughtSignature != "SIGNATURE_ABC" {
		t.Fatalf("an unsigned later call must inherit the most recent real signature")
	}
}

// Ported: build_contents_carries_first_signature_onto_unsigned_same_turn_siblings
// and build_contents_replays_every_signature_across_multi_tool_history.
func TestBuildContentsSignaturesAcrossHistory(t *testing.T) {
	signatures := []string{"SIG_A", "SIG_B", "SIG_C"}
	var messages []types.Message
	for idx, sig := range signatures {
		sigCopy := sig
		messages = append(messages, types.Message{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.BlockToolUse, ID: strPtr("call_" + string(rune('0'+idx))), Name: strPtr("bash"),
				Input: json.RawMessage(`{"command":"echo ` + itoa(idx) + `"}`), ThoughtSignature: &sigCopy},
		}})
		messages = append(messages, types.Message{Role: types.RoleUser, Content: []types.ContentBlock{
			types.ToolResultBlock("call_"+string(rune('0'+idx)), "out "+itoa(idx), false),
		}})
	}
	contents := BuildContents(messages)
	var replayed []string
	for _, content := range contents {
		for _, part := range content.Parts {
			if part.FunctionCall != nil && part.ThoughtSignature != nil {
				replayed = append(replayed, *part.ThoughtSignature)
			}
		}
	}
	want := strings.Join(signatures, ",")
	if got := strings.Join(replayed, ","); got != want {
		t.Fatalf("replayed = %q, want %q", got, want)
	}
}

// Ported: build_contents_leaves_unsigned_calls_unsigned_when_no_prior_signature_exists
func TestBuildContentsUnsignedStaysUnsigned(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.BlockToolUse, ID: strPtr("call_x"), Name: strPtr("read"),
				Input: json.RawMessage(`{"path":"x"}`)},
		}},
	}
	contents := BuildContents(messages)
	if contents[1].Parts[0].ThoughtSignature != nil {
		t.Fatalf("no prior signature exists; the call must stay unsigned")
	}
}

// Ported: build_contents_preserves_tool_calls_and_results
func TestBuildContentsPreservesToolCallsAndResults(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.BlockToolUse, ID: strPtr("call_1"), Name: strPtr("read"),
				Input: json.RawMessage(`{"path":"README.md"}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.ToolResultBlock("call_1", "ok", false),
		}},
	}
	contents := BuildContents(messages)
	if len(contents) != 2 || contents[0].Role != "model" || contents[1].Role != "user" {
		t.Fatalf("roles/contents wrong: %+v", contents)
	}
	if contents[0].Parts[0].FunctionCall.Name != "read" {
		t.Fatalf("function call name")
	}
	if contents[1].Parts[0].FunctionResponse.Name != "read" {
		t.Fatalf("function response name must resolve from the call")
	}
}

// Ported: build_contents_normalizes_non_object_tool_call_args_for_gemini_struct
func TestBuildContentsNormalizesPrimitiveArgs(t *testing.T) {
	messages := []types.Message{{Role: types.RoleAssistant, Content: []types.ContentBlock{
		{Type: types.BlockToolUse, ID: strPtr("call_primitive"), Name: strPtr("read"),
			Input: json.RawMessage(`20`)},
	}}}
	contents := BuildContents(messages)
	if string(contents[0].Parts[0].FunctionCall.Args) != "{}" {
		t.Fatalf("primitive args must normalize to {}, got %s", contents[0].Parts[0].FunctionCall.Args)
	}
}

// Ported: build_tools_uses_function_declarations + build_tools_rewrites_const +
// build_tools_strips_additional_properties.
func TestBuildToolsGeminiDialect(t *testing.T) {
	defs := []types.ToolDefinition{{
		Name: "batch", Description: "Batch tools",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"tool_calls": {"type": "array", "items": {"oneOf": [{
				"type": "object",
				"properties": {"tool": {"type": "string", "const": "read"}, "file_path": {"type": "string"}},
				"required": ["tool", "file_path"],
				"additionalProperties": false
			}]}}}
		}`),
	}}
	tools := BuildTools(defs)
	if tools == nil || len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("one declaration expected")
	}
	var schema map[string]any
	if err := json.Unmarshal(tools[0].FunctionDeclarations[0].Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if schemaContainsKeyString(schema, "const") || schemaContainsKeyString(schema, "oneOf") ||
		schemaContainsKeyString(schema, "additionalProperties") {
		t.Fatalf("gemini dialect must rewrite const/oneOf and strip additionalProperties")
	}
	if !schemaContainsKeyString(schema, "enum") || !schemaContainsKeyString(schema, "anyOf") {
		t.Fatalf("const → enum and oneOf → anyOf expected")
	}
	if BuildTools(nil) != nil {
		t.Fatalf("no tools → nil")
	}
}

// Ported: parses_prompt_feedback_block_reason + parses_candidate_finish_message
func TestResponseDecoding(t *testing.T) {
	var feedback VertexGenerateContentResponse
	if err := json.Unmarshal([]byte(`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT",
		"blockReasonMessage":"Prompt violated policy"}}`), &feedback); err != nil {
		t.Fatal(err)
	}
	if feedback.PromptFeedback.BlockReason == nil || *feedback.PromptFeedback.BlockReason != "PROHIBITED_CONTENT" {
		t.Fatalf("block reason")
	}
	var candidate VertexGenerateContentResponse
	if err := json.Unmarshal([]byte(`{"candidates":[{"finishReason":"SAFETY",
		"finishMessage":"Response blocked by safety filters"}]}`), &candidate); err != nil {
		t.Fatal(err)
	}
	first := candidate.Candidates[0]
	if first.FinishReason == nil || *first.FinishReason != "SAFETY" {
		t.Fatalf("finish reason")
	}
	if first.FinishMessage == nil || *first.FinishMessage != "Response blocked by safety filters" {
		t.Fatalf("finish message")
	}
}

// Ported: fallback_models_skip_current_model
func TestFallbackModelsSkipCurrent(t *testing.T) {
	fallbacks := GeminiFallbackModels("gemini-3.1-pro-preview")
	if len(fallbacks) == 0 || fallbacks[0] != "gemini-pro-latest" {
		t.Fatalf("first fallback: %v", fallbacks)
	}
	for _, model := range fallbacks {
		if asciiEqualFold(model, "gemini-3.1-pro-preview") {
			t.Fatalf("current model must be skipped")
		}
	}
}

// Ported: extract_gemini_model_ids_discovers_nested_models
func TestExtractGeminiModelIDs(t *testing.T) {
	payload := map[string]any{
		"routing": map[string]any{
			"manual": map[string]any{
				"models": []any{
					map[string]any{"id": "gemini-3-pro-preview"},
					map[string]any{"name": "gemini-3.1-pro-preview"},
				},
			},
			"auto": []any{"gemini-3-flash-preview", "not-a-model"},
		},
	}
	models := ExtractGeminiModelIDs(payload)
	want := "gemini-3.1-pro-preview,gemini-3-pro-preview,gemini-3-flash-preview"
	if got := strings.Join(models, ","); got != want {
		t.Fatalf("nested discovery: got %v want %v", models, want)
	}
	if !IsGeminiModelID("gemini-2.5-pro") || IsGeminiModelID("claude-x") || IsGeminiModelID("gemini-2 5") {
		t.Fatalf("model id validation")
	}
}

// Ported: missing_thought_signature_errors_are_recognized_from_backend_bodies
func TestMissingThoughtSignatureError(t *testing.T) {
	body := `{"error":{"message":"Function call is missing a ThoughtSignature in functionCall parts"}}`
	if !IsMissingThoughtSignatureError(body) {
		t.Fatalf("case-insensitive ThoughtSignature must match")
	}
	if IsMissingThoughtSignatureError("some other failure") {
		t.Fatalf("unrelated errors must not match")
	}
}

// Ported: auth_mode_prefers_api_key_when_present + force_oauth_overrides +
// defaults_to_oauth_without_api_key
func TestAuthMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("GCODE_GEMINI_FORCE_OAUTH", "")
	mode := ResolveAuthMode()
	if !mode.isAPIKey() || *mode.APIKey != "test-key" {
		t.Fatalf("api key preferred when present")
	}

	t.Setenv("GCODE_GEMINI_FORCE_OAUTH", "1")
	if ResolveAuthMode().isAPIKey() {
		t.Fatalf("force oauth must pin the Code Assist route")
	}

	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GCODE_GEMINI_FORCE_OAUTH", "")
	if ResolveAuthMode().isAPIKey() {
		t.Fatalf("no key → oauth")
	}
}

// Ported: developer_api_base_url_defaults_to_generativelanguage + env overrides
func TestDeveloperAPIBaseURL(t *testing.T) {
	if got := DeveloperAPIBaseURL(); got != "https://generativelanguage.googleapis.com/v1beta" {
		t.Fatalf("default: %s", got)
	}
	t.Setenv("GEMINI_API_ENDPOINT", "http://127.0.0.1:1/")
	t.Setenv("GEMINI_API_VERSION", "/v9/")
	if got := DeveloperAPIBaseURL(); got != "http://127.0.0.1:1/v9" {
		t.Fatalf("env overrides: %s", got)
	}
}

// Ported: choose_onboard_tier / validate (ineligible VALIDATION_REQUIRED)
func TestCodeAssistValidation(t *testing.T) {
	url := "https://accounts.google.com/validate"
	res := LoadCodeAssistResponse{IneligibleTiers: []IneligibleTier{{
		ReasonCode: strPtr("VALIDATION_REQUIRED"), ValidationURL: &url,
	}}}
	err := ValidateLoadCodeAssistResponse(&res)
	if err == nil || !strings.Contains(err.Error(), "Complete account validation") {
		t.Fatalf("validation required must surface: %v", err)
	}

	tier := ChooseOnboardTier(&res)
	if tier.ID == nil || *tier.ID != UserTierFree {
		t.Fatalf("no tiers → free tier default, got %+v", tier)
	}
	free := "free-tier"
	def := true
	allowed := LoadCodeAssistResponse{AllowedTiers: []GeminiUserTier{
		{ID: &free, IsDefault: &def},
	}}
	if chosen := ChooseOnboardTier(&allowed); chosen.ID == nil || *chosen.ID != UserTierFree {
		t.Fatalf("default tier must be chosen: %+v", chosen)
	}
}

// Ported: fully_unsigned_history + downgrade_policy_removes_every_function_call_part
func TestDowngradePolicy(t *testing.T) {
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.BlockToolUse, ID: strPtr("call_1"), Name: strPtr("read"),
				Input: json.RawMessage(`{"path":"x"}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.ToolResultBlock("call_1", "contents", false),
		}},
	}
	contents := BuildContentsWithSignaturePolicy(messages, DowngradeToolCallsToText)
	for _, content := range contents {
		for _, part := range content.Parts {
			if part.FunctionCall != nil || part.FunctionResponse != nil {
				t.Fatalf("downgrade must remove every function part")
			}
		}
	}
	if !strings.Contains(*contents[0].Parts[0].Text, "[previous tool call] read(") {
		t.Fatalf("call preserved as text: %q", *contents[0].Parts[0].Text)
	}
}

// httptest end-to-end: Developer API mode, one POST, full event sequence.
func TestCompleteDeveloperAPI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("GEMINI_API_ENDPOINT", "")
	t.Setenv("GEMINI_API_VERSION", "")

	var gotPath string
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[
			{"text":"Hello"},{"functionCall":{"name":"read","args":{"path":"x"},"id":"abc"}}]},
			"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`))
	}))
	defer server.Close()
	t.Setenv("GEMINI_API_ENDPOINT", server.URL)
	t.Setenv("GEMINI_API_VERSION", "v1beta")

	client := NewClient()
	var events []types.StreamEvent
	err := client.Complete(CompleteOptions{
		Model:    "gemini-pro-latest",
		Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("hi")}}},
		System:   "You are Jcode.",
		Emit: func(event types.StreamEvent) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1beta/models/gemini-pro-latest:generateContent" {
		t.Fatalf("path: %s", gotPath)
	}
	if gotAuth != "test-key" {
		t.Fatalf("api key header")
	}
	var text, callID, callName, usage bool
	var end types.MessageEnd
	for _, event := range events {
		switch e := event.(type) {
		case types.TextDelta:
			text = text || e.Text == "Hello"
		case types.ToolUseStart:
			callID, callName = e.ID != "", e.Name == "read"
		case types.TokenUsageEvent:
			usage = e.InputTokens != nil && *e.InputTokens == 10
		case types.MessageEnd:
			end = e
		}
	}
	if !text || !callID || !callName || !usage {
		t.Fatalf("missing events: text=%v call=%v/%v usage=%v", text, callID, callName, usage)
	}
	if end.StopReason == nil || *end.StopReason != "stop" {
		t.Fatalf("stop reason lowercased: %v", end.StopReason)
	}
}

// Code Assist OAuth mode end-to-end: loadCodeAssist → generateContent, envelope
// URL `{base}:{method}` and bearer auth.
func TestCompleteCodeAssistOAuth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("CODE_ASSIST_ENDPOINT", "")
	t.Setenv("CODE_ASSIST_API_VERSION", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			project := "proj-123"
			_, _ = w.Write([]byte(`{"currentTier":{"id":"standard-tier"},
				"cloudaicompanionProject":"proj-123"}`))
			_ = project
		case strings.HasSuffix(r.URL.Path, ":generateContent"):
			body, _ := readAll(r.Body)
			var envelope struct {
				Project      string `json:"project"`
				UserPromptID string `json:"user_prompt_id"`
				Request      struct {
					SessionID         *string           `json:"session_id"`
					SystemInstruction *GeminiContent    `json:"systemInstruction"`
					ToolConfig        *GeminiToolConfig `json:"toolConfig"`
				} `json:"request"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Errorf("bad envelope: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if envelope.Project != "proj-123" || envelope.UserPromptID == "" ||
				envelope.Request.SessionID == nil || envelope.Request.ToolConfig == nil {
				t.Errorf("envelope fields missing: %+v", envelope.Request)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model",
				"parts":[{"text":"done"}]},"finishReason":"STOP"}]}}`))
		default:
			t.Errorf("unexpected method path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("CODE_ASSIST_ENDPOINT", server.URL)
	t.Setenv("CODE_ASSIST_API_VERSION", "v1internal")

	// Seed OAuth credentials.
	if err := SaveTokens(&GeminiTokens{AccessToken: "test-access-token",
		RefreshToken: "test-refresh", ExpiresAt: timeNowMS() + 3_600_000}); err != nil {
		t.Fatal(err)
	}

	client := NewClient()
	var events []types.StreamEvent
	err := client.Complete(CompleteOptions{
		Model:    "gemini-pro-latest",
		Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("hi")}}},
		System:   "You are Jcode.",
		Tools:    []types.ToolDefinition{{Name: "read", Description: "Read"}},
		Emit: func(event types.StreamEvent) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ends int
	for _, event := range events {
		if _, ok := event.(types.MessageEnd); ok {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("expected one MessageEnd, got %d (events=%d)", ends, len(events))
	}
}

// Model-not-found walks the fallback ladder and pins the replacement.
func TestCompleteModelFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "gemini-9.9-missing") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"status":"NOT_FOUND"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},
			"finishReason":"STOP"}]}`))
	}))
	defer server.Close()
	t.Setenv("GEMINI_API_ENDPOINT", server.URL)
	t.Setenv("GEMINI_API_VERSION", "v1beta")

	client := NewClient()
	var switched string
	err := client.Complete(CompleteOptions{
		Model:         "gemini-9.9-missing",
		Messages:      []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("hi")}}},
		Emit:          func(types.StreamEvent) error { return nil },
		OnModelSwitch: func(model string) { switched = model },
	})
	if err != nil {
		t.Fatal(err)
	}
	if switched != "gemini-pro-latest" {
		t.Fatalf("first fallback expected, got %q", switched)
	}
}

// A missing thought_signature 400 is NOT schema-related for the plain Gemini
// runtime (the downgrade policy lives in the Antigravity runtime, which gcode
// does not target in M0-M4), so the error surfaces after one request.
func TestCompleteSignatureErrorSurfaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "test-key")

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Function call is missing a thought_signature in functionCall parts"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},
			"finishReason":"STOP"}]}`))
	}))
	defer server.Close()
	t.Setenv("GEMINI_API_ENDPOINT", server.URL)
	t.Setenv("GEMINI_API_VERSION", "v1beta")

	client := NewClient()
	messages := []types.Message{
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.BlockToolUse, ID: strPtr("call_1"), Name: strPtr("read"),
				Input: json.RawMessage(`{"path":"x"}`)},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			types.ToolResultBlock("call_1", "contents", false),
		}},
	}
	err := client.Complete(CompleteOptions{
		Model: "gemini-pro-latest", Messages: messages,
		System: "sys",
		Emit:   func(types.StreamEvent) error { return nil },
	})
	if err == nil || !IsMissingThoughtSignatureError(err.Error()) {
		t.Fatalf("signature 400 must surface: %v", err)
	}
	if requests != 1 {
		t.Fatalf("no retry expected, got %d requests", requests)
	}
}

// Rejected schema keywords are learned and stripped on the retry.
func TestCompleteSchemaRejectionRecovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "test-key")
	dialect.SetStorePathForTest(t.TempDir())

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid JSON payload received. Unknown name \"propertyNames\" at 'request.tools[0].function_declarations[0].parameters': Cannot find field."}}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},
			"finishReason":"STOP"}]}`))
	}))
	defer server.Close()
	t.Setenv("GEMINI_API_ENDPOINT", server.URL)
	t.Setenv("GEMINI_API_VERSION", "v1beta")

	client := NewClient()
	err := client.Complete(CompleteOptions{
		Model:    "gemini-pro-latest",
		Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("hi")}}},
		Tools: []types.ToolDefinition{{Name: "t", Description: "d",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}},"propertyNames":{"type":"string"}}`)}},
		Emit: func(types.StreamEvent) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("schema rejection must retry once, got %d requests", requests)
	}
}

// Blocked prompt and blocked candidate render the finish message.
func TestCompleteBlockedOutputs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT",
			"blockReasonMessage":"Prompt violated policy"}}`))
	}))
	defer server.Close()
	t.Setenv("GEMINI_API_ENDPOINT", server.URL)
	t.Setenv("GEMINI_API_VERSION", "v1beta")

	client := NewClient()
	err := client.Complete(CompleteOptions{
		Model:    "gemini-pro-latest",
		Messages: []types.Message{{Role: types.RoleUser, Content: []types.ContentBlock{types.TextBlock("hi")}}},
		Emit:     func(types.StreamEvent) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "Gemini blocked the prompt (PROHIBITED_CONTENT)") {
		t.Fatalf("blocked prompt error: %v", err)
	}
}

func TestRetryPolicy(t *testing.T) {
	if geminiHTTPRetryDelay("onboardUser", 500, "", 0, nil) != nil {
		t.Fatalf("onboarding must not auto-replay")
	}
	if geminiHTTPRetryDelay("generateContent", 429, "quota will reset after 0s", 0, nil) == nil {
		t.Fatal("burst 429 retries")
	}
	if geminiHTTPRetryDelay("generateContent", 429, "daily quota exhausted", 0, nil) != nil {
		t.Fatalf("daily quota does not retry")
	}
	if geminiHTTPRetryDelay("generateContent", 500, "", 0, nil) == nil {
		t.Fatal("5xx retries")
	}
	if geminiHTTPRetryDelay("generateContent", 400, "", 0, nil) != nil {
		t.Fatalf("4xx does not retry")
	}
	hint := 7 * timeSecond
	if delay := geminiHTTPRetryDelay("generateContent", 503, "", 0, &hint); delay == nil || *delay != hint {
		t.Fatalf("server hint wins: %v", delay)
	}
}

func TestGeminiUserAgent(t *testing.T) {
	t.Setenv("GCODE_GEMINI_USER_AGENT", "")
	ua := GeminiUserAgent()
	if !strings.HasPrefix(ua, "GeminiCLI-tui/0.57.0/gemini-pro-latest (") {
		t.Fatalf("cli identity: %s", ua)
	}
	t.Setenv("GCODE_GEMINI_USER_AGENT", "custom/1")
	if GeminiUserAgent() != "custom/1" {
		t.Fatalf("env override")
	}
}

func TestMergeModelLists(t *testing.T) {
	merged := MergeGeminiModelLists([]string{" gemini-zzz ", "gemini-2.5-pro", "gemini-aaa"})
	if merged[0] != "gemini-2.5-pro" || merged[1] != "gemini-aaa" || merged[2] != "gemini-zzz" {
		t.Fatalf("known catalog entries first, then sorted extras: %v", merged)
	}
	withDefault := MergeGeminiModelLists([]string{"gemini-2.5-pro", "gemini-pro-latest"})
	if withDefault[0] != "gemini-pro-latest" {
		t.Fatalf("catalog order beats input order: %v", withDefault)
	}
}

// Config wiring: provider.gemini_force_oauth / gemini_project.
func TestAuthSourceFromConfig(t *testing.T) {
	cfg, loadErr := config.LoadString(`
[provider]
gemini_force_oauth = true
gemini_project = "my-project"
`)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	SetAuthSource(configAuthSource{cfg})
	t.Cleanup(func() { SetAuthSource(envAuthSource{}) })
	if !ForceOAuth() {
		t.Fatalf("config force oauth")
	}
	if CloudProject() != "my-project" {
		t.Fatalf("config project")
	}
}

// helpers -------------------------------------------------------------------

func schemaContainsKeyString(value any, key string) bool {
	switch v := value.(type) {
	case map[string]any:
		for k, item := range v {
			if k == key || schemaContainsKeyString(item, key) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if schemaContainsKeyString(item, key) {
				return true
			}
		}
	}
	return false
}

// Model discovery via ListModels (Developer API) and loadCodeAssist (Code
// Assist), with the persisted catalog cache.
func TestRefreshAvailableModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-pro"},
			{"name":"models/gemini-zzz-custom"}]}`))
	}))
	defer server.Close()
	t.Setenv("GEMINI_API_ENDPOINT", server.URL)
	t.Setenv("GEMINI_API_VERSION", "v1beta")

	client := NewClient()
	models, err := client.RefreshAvailableModels()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "gemini-2.5-pro,gemini-zzz-custom" {
		t.Fatalf("discovered: %v", models)
	}
	if joined := strings.Join(client.FetchedModels(), ","); joined != "gemini-2.5-pro,gemini-zzz-custom" {
		t.Fatalf("cached: %v", client.FetchedModels())
	}
	// The catalog persisted to disk.
	cached := loadPersistedCatalog()
	if len(cached) != 2 || cached[0] != "gemini-2.5-pro" {
		t.Fatalf("persisted: %v", cached)
	}
	// A fresh client seeds from the cache.
	fresh := NewClient()
	fresh.SeedCachedCatalog()
	if len(fresh.FetchedModels()) != 2 {
		t.Fatalf("seed: %v", fresh.FetchedModels())
	}
}
