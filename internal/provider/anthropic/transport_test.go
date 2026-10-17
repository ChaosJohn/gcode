package anthropic

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ChaosJohn/gcode/internal/base/config"
	"github.com/ChaosJohn/gcode/internal/types"
)

// ---- oauth metadata ----

func TestOAuthRequestMetadataUUIDv5(t *testing.T) {
	// Isolate from any real ~/.claude.json on this machine.
	t.Setenv("HOME", t.TempDir())
	// UUIDv5(NAMESPACE_DNS, session) in simple format, independent of any
	// stored ~/.claude.json.
	h := sha1.New()
	h.Write(namespaceDNS[:])
	h.Write([]byte("test-session"))
	sum := h.Sum(nil)
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	wantDevice := fmt.Sprintf("%x", u[:])

	meta := OAuthRequestMetadata("test-session")
	var parsed struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(meta.UserID), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.DeviceID != wantDevice {
		t.Errorf("device_id = %s, want %s", parsed.DeviceID, wantDevice)
	}
	if parsed.AccountUUID != "unknown-account" {
		t.Errorf("account_uuid = %s", parsed.AccountUUID)
	}
	if parsed.SessionID != "test-session" {
		t.Errorf("session_id = %s", parsed.SessionID)
	}
}

// ---- native web search ----

func TestNativeWebSearchServerTool(t *testing.T) {
	// Upstream: default config → basic version with max_uses 5.
	webSearch := config.Default().WebSearch
	tool := string(NativeWebSearchServerTool(webSearch))
	want := `{"type":"web_search_20250305","name":"web_search","max_uses":5}`
	if tool != want {
		t.Errorf("\n got %s\nwant %s", tool, want)
	}

	// Newer versions pin direct callers and drop max_uses when nil.
	newer := webSearch
	newer.NativeAnthropicToolVersion = "web_search_20260209"
	newer.NativeMaxUses = nil
	tool = string(NativeWebSearchServerTool(newer))
	if !strings.Contains(tool, `"type":"web_search_20260209"`) ||
		!strings.Contains(tool, `"allowed_callers":["direct"]`) ||
		strings.Contains(tool, "max_uses") {
		t.Errorf("newer tool = %s", tool)
	}

	// Allowed domains win over blocked.
	both := webSearch
	both.NativeAllowedDomains = []string{" docs.rs ", ""}
	both.NativeBlockedDomains = []string{"example.com"}
	tool = string(NativeWebSearchServerTool(both))
	if !strings.Contains(tool, `"allowed_domains":["docs.rs"]`) || strings.Contains(tool, "blocked_domains") {
		t.Errorf("allowed wins: %s", tool)
	}
	both.NativeAllowedDomains = nil
	tool = string(NativeWebSearchServerTool(both))
	if !strings.Contains(tool, `"blocked_domains":["example.com"]`) {
		t.Errorf("blocked: %s", tool)
	}
}

func TestServerToolsAttachRules(t *testing.T) {
	builtin := types.ToolDefinition{
		Name: "websearch", Description: types.LocalWebsearchDescription,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
	bash := types.ToolDefinition{Name: "bash", InputSchema: json.RawMessage(`{}`)}
	webSearch := config.Default().WebSearch

	if ServerToolsForRequest(webSearch, true, []types.ToolDefinition{bash}) != nil {
		t.Error("no websearch in session → no server tool")
	}
	if got := ServerToolsForRequest(webSearch, true, []types.ToolDefinition{bash, builtin}); len(got) != 1 {
		t.Errorf("websearch offered → server tool: %d", len(got))
	}
	if got := ServerToolsForRequest(webSearch, false, []types.ToolDefinition{bash, builtin}); len(got) != 0 {
		t.Errorf("non-first-party → no server tool: %d", len(got))
	}
	// SDK custom websearch (different description) is never replaced.
	custom := builtin
	custom.Description = "Search our internal index."
	if got := ServerToolsForRequest(webSearch, true, []types.ToolDefinition{bash, custom}); len(got) != 0 {
		t.Errorf("custom websearch must not be replaced: %d", len(got))
	}
	// Local tool dropped only when the server tool is attached.
	tools := []types.ToolDefinition{bash, builtin}
	if kept := WithoutLocalWebsearch(tools, nil); len(kept) != 2 {
		t.Errorf("no server tools → keep both: %d", len(kept))
	}
	if kept := WithoutLocalWebsearch(tools, ServerToolsWithConfig(webSearch, true)); len(kept) != 1 || kept[0].Name != "bash" {
		t.Errorf("server tool attached → drop local: %+v", kept)
	}
}

// ---- error classification ----

func TestErrorClassification(t *testing.T) {
	if !IsRetryableError("Connection reset by peer") {
		t.Error("connection reset is retryable")
	}
	if !IsRetryableError("Anthropic API error (503): overloaded") {
		t.Error("503/overloaded is retryable")
	}
	if !IsRetryableError("api_error: boom") {
		t.Error("api_error is retryable")
	}
	if IsRetryableError("400 bad request: invalid input") {
		t.Error("bad request is not retryable")
	}
	if !IsAccountLimitError("Rate limit exceeded") || IsAccountLimitError("500 internal") {
		t.Error("account limit classifier")
	}
	if !IsModelNotFoundError(strings.ToLower(`Anthropic API error (404 Not Found): {"type":"not_found_error","message":"model not found"}`)) {
		t.Error("model not found")
	}
	if !IsReasoningUnsupportedError("400 bad request: adaptive thinking is not supported on this model") {
		t.Error("reasoning unsupported")
	}
	if !IsOAuthAuthError("oauth token has expired") {
		t.Error("oauth auth error")
	}
	if !IsTransientTransportError("http2 error: stream error received") {
		t.Error("http2 stream error")
	}
	if !IsTransientTransportError("received fatal alert: bad_record_mac") {
		t.Error("bad record mac")
	}
}

func TestRecommendedModelFromError(t *testing.T) {
	err := "claude fable 5 is not available. please use opus 4.8."
	if got := AnthropicRecommendedModelFromError(err); got != "claude-opus-4-8" {
		t.Errorf("recommended = %q", got)
	}
	if got := AnthropicRecommendedModelFromError("totally unrelated"); got != "" {
		t.Errorf("no hint → empty, got %q", got)
	}
}

func TestFallbackModelSkipsRetriedAndRetired(t *testing.T) {
	tried := []string{"claude-opus-5-5", DefaultClaudeModel}
	if got := AnthropicFallbackModel(tried, "model not found"); got != "claude-opus-5" {
		t.Errorf("next best = %q", got)
	}
	if got := AnthropicFallbackModel([]string{"claude-mythos-x"}, "please use claude-mythos-9"); got == "" ||
		AnthropicModelIsRetired(got) {
		t.Errorf("retired must not be picked: %q", got)
	}
}

func TestBestAvailableOpusModel(t *testing.T) {
	got := BestAvailableOpusModel("claude-fable-5")
	if !strings.Contains(got, "claude-opus") {
		t.Errorf("opus fallback = %q", got)
	}
	if got == "claude-fable-5" {
		t.Error("must exclude the current model")
	}
}

// ---- SSE pump + transport over httptest ----

func TestStreamResponseEndToEnd(t *testing.T) {
	var seenHeaders http.Header
	var seenBody map[string]any
	ssePayload := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"model":"claude-opus-4-8-20260101","usage":{"input_tokens":7,"output_tokens":null}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		"",
		": keepalive comment without data",
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
		"",
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Errorf("request body: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer server.Close()

	temp := float32(1.0)
	request := ApiRequest{
		Model: "claude-opus-4-8", MaxTokens: 100, Stream: true, Temperature: &temp,
		Messages: []ApiMessage{{Role: "user", Content: []ContentBlock{TextContent{Text: "hey"}}}},
	}
	var events []types.StreamEvent
	cfg := TransportConfig{APIURL: server.URL, AuthMode: "header"}
	err := StreamResponse(context.Background(), cfg, "sk-test", false, request,
		"s-1", "claude-opus-4-8",
		func(event types.StreamEvent) error { events = append(events, event); return nil },
		nil)
	if err != nil {
		t.Fatal(err)
	}
	if seenHeaders.Get("x-api-key") != "sk-test" {
		t.Errorf("api key header = %q", seenHeaders.Get("x-api-key"))
	}
	if seenHeaders.Get("anthropic-beta") != "prompt-caching-2024-07-31" {
		t.Errorf("beta header = %q", seenHeaders.Get("anthropic-beta"))
	}
	if got, _ := seenBody["temperature"].(float64); got != 1.0 {
		t.Errorf("temperature = %v", seenBody["temperature"])
	}
	want := []string{
		"ConnectionPhase(sending request)", "ConnectionPhase(waiting for response)",
		"UpstreamProvider(anthropic:claude-opus-4-8-20260101)", "TextDelta(hi)",
		"MessageEnd(end_turn)", "TokenUsage(7,3)",
	}
	if len(events) != len(want) {
		for i, e := range events {
			t.Logf("%d: %T %+v", i, e, e)
		}
		t.Fatalf("events = %d, want %d", len(events), len(want))
	}
	for i, w := range want {
		got := ""
		switch e := events[i].(type) {
		case types.ConnectionPhaseEvent:
			got = "ConnectionPhase(" + string(e.Phase) + ")"
		case types.UpstreamProviderEvent:
			got = "UpstreamProvider(" + e.Provider + ")"
		case types.TextDelta:
			got = "TextDelta(" + e.Text + ")"
		case types.MessageEnd:
			got = "MessageEnd(" + deref(e.StopReason) + ")"
		case types.TokenUsageEvent:
			got = fmt.Sprintf("TokenUsage(%d,%d)", derefU64(e.InputTokens), derefU64(e.OutputTokens))
		default:
			got = fmt.Sprintf("UNKNOWN %T", e)
		}
		if got != w {
			t.Errorf("event[%d] = %s, want %s", i, got, w)
		}
	}
}

// OAuth path: bearer auth, beta headers, attribution headers, ?beta=true.
func TestStreamResponseOAuthHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.RawQuery, "beta=true") {
			t.Errorf("url = %s", r.URL.String())
		}
		if got := r.Header.Get("authorization"); got != "Bearer oauth-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("user-agent"); got != ClaudeCLIUserAgent {
			t.Errorf("user-agent = %q", got)
		}
		beta := r.Header.Get("anthropic-beta")
		if !strings.HasPrefix(beta, OAuthBetaHeaders) {
			t.Errorf("beta = %q", beta)
		}
		if !strings.Contains(beta, "context-1m-2025-08-07") {
			t.Errorf("1m beta missing: %q", beta)
		}
		for _, header := range []string{"x-app", "x-client-request-id", "x-claude-code-session-id", "x-stainless-arch", "x-stainless-os", "anthropic-dangerous-direct-browser-access"} {
			if r.Header.Get(header) == "" {
				t.Errorf("missing attribution header %s", header)
			}
		}
		if r.Header.Get("x-claude-code-session-id") != "sess-9" {
			t.Errorf("session id = %q", r.Header.Get("x-claude-code-session-id"))
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	request := ApiRequest{Model: "claude-opus-4-6[1m]", MaxTokens: 10, Stream: true}
	cfg := TransportConfig{APIURL: server.URL, OAuthURL: server.URL}
	err := StreamResponse(context.Background(), cfg, "oauth-token", true, request,
		"sess-9", "claude-opus-4-6[1m]", func(types.StreamEvent) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
}

// HTTP errors surface with status; Retry-After is preserved for the retry.
func TestStreamResponseHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"rate_limit_error","message":"Rate limit exceeded"}`))
	}))
	defer server.Close()

	request := ApiRequest{Model: "claude-opus-4-8", MaxTokens: 10, Stream: true}
	err := StreamResponse(context.Background(), TransportConfig{APIURL: server.URL}, "k", false, request,
		"s", "claude-opus-4-8", func(types.StreamEvent) error { return nil }, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if hint, ok := RetryAfterFromError(err); !ok || hint != 7*time.Second {
		t.Errorf("retry-after hint = %v (%v)", hint, ok)
	}
	if !IsRetryableError(strings.ToLower(err.Error())) {
		t.Errorf("rate limit error must be retryable: %s", err)
	}
}

// A retryable SSE error event aborts the attempt for the retry loop.
func TestStreamResponseRetryableSSEError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"internal server error\"}}\n\n"))
	}))
	defer server.Close()

	request := ApiRequest{Model: "claude-opus-4-8", MaxTokens: 10, Stream: true}
	err := StreamResponse(context.Background(), TransportConfig{APIURL: server.URL}, "k", false, request,
		"s", "claude-opus-4-8", func(types.StreamEvent) error { return nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "retryable stream error") {
		t.Fatalf("err = %v", err)
	}
}

// The retry loop: first attempt fails transiently, second succeeds; a partial
// first attempt triggers a rollback event.
func TestRunStreamWithRetriesRecovers(t *testing.T) {
	attempt := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		w.Header().Set("content-type", "text/event-stream")
		if attempt == 1 {
			// Partial output (flushed so it reaches the client) then a
			// transport fault.
			_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"part\"}}\n\n"))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
	}))
	defer server.Close()

	var events []types.StreamEvent
	request := ApiRequest{Model: "claude-opus-4-8", MaxTokens: 10, Stream: true}
	err := RunStreamWithRetries(
		Options{
			Transport:    TransportConfig{APIURL: server.URL},
			SessionID:    "s",
			ModelName:    "claude-opus-4-8",
			ResolveToken: func(bool) (string, error) { return "k", nil },
		},
		request, RetrySettings{},
		func(event types.StreamEvent) error { events = append(events, event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, 0, len(events))
	for _, e := range events {
		kinds = append(kinds, fmt.Sprintf("%T", e))
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "types.RetryRollback") {
		t.Errorf("partial output must trigger rollback: %s", joined)
	}
	if !strings.Contains(joined, "types.ConnectionPhaseEvent") {
		t.Errorf("retry phase must be reported: %s", joined)
	}
	last := events[len(events)-1]
	if _, ok := last.(types.MessageEnd); !ok {
		t.Errorf("final event = %T", last)
	}
	if attempt != 2 {
		t.Errorf("attempts = %d", attempt)
	}
}

// Non-retryable errors surface after the first attempt.
func TestRunStreamWithRetriesSurfaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"invalid_request_error","message":"bad input"}`))
	}))
	defer server.Close()

	err := RunStreamWithRetries(
		Options{
			Transport:    TransportConfig{APIURL: server.URL},
			SessionID:    "s",
			ModelName:    "claude-opus-4-8",
			ResolveToken: func(bool) (string, error) { return "k", nil },
		},
		ApiRequest{Model: "claude-opus-4-8", MaxTokens: 10, Stream: true}, RetrySettings{},
		func(types.StreamEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "bad input") {
		t.Fatalf("err = %v", err)
	}
}

// The model-not-found path switches models via the fallback ladder.
func TestRunStreamWithRetriesModelFallback(t *testing.T) {
	attempt := 0
	var seenModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenModels = append(seenModels, body.Model)
		if body.Model == "claude-opus-5-5" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"not_found_error","message":"model claude-opus-5-5 not found. Please use Opus 4.8."}`))
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
	}))
	defer server.Close()

	var switchedTo string
	err := RunStreamWithRetries(
		Options{
			Transport:     TransportConfig{APIURL: server.URL},
			SessionID:     "s",
			ModelName:     "claude-opus-5-5",
			ResolveToken:  func(bool) (string, error) { return "k", nil },
			OnModelSwitch: func(m string) { switchedTo = m },
		},
		ApiRequest{Model: "claude-opus-5-5", MaxTokens: 10, Stream: true}, RetrySettings{},
		func(types.StreamEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if switchedTo != "claude-opus-4-8" {
		t.Errorf("switched to %q", switchedTo)
	}
	if len(seenModels) != 2 || seenModels[1] != "claude-opus-4-8" {
		t.Errorf("models = %v", seenModels)
	}
}

// ---- retry backoff ----

func TestRetryBackoffDelay(t *testing.T) {
	base := uint64(1000)
	for attempt := 1; attempt <= 3; attempt++ {
		delay := RetryBackoffDelay(attempt, base)
		exp := base << uint(attempt-1)
		if delay < time.Duration(exp*8/10)*time.Millisecond ||
			delay > time.Duration(exp*12/10)*time.Millisecond {
			t.Errorf("attempt %d delay = %v, want ~%vms", attempt, delay, exp)
		}
	}
}

func derefU64(v *uint64) uint64 {
	if v == nil {
		return 0
	}
	return *v
}
