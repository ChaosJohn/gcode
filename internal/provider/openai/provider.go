package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/base/config"
	"github.com/ChaosJohn/gcode/internal/provider/transporterr"
	"github.com/ChaosJohn/gcode/internal/types"
)

// Endpoints and transport constants (ported from openai-runtime lib.rs).
const (
	// OpenAIAPIBase is OpenAI's own Responses API base.
	OpenAIAPIBase = "https://api.openai.com/v1"
	// FirstPartyAPIBase is the first-party API-key endpoint constant.
	FirstPartyAPIBase = "https://api.openai.com/v1"
	// ChatGPTAPIBase is the ChatGPT/Codex OAuth backend.
	ChatGPTAPIBase = "https://chatgpt.com/backend-api/codex"
	// ResponsesPath is appended to the base.
	ResponsesPath = "responses"
	// Originator is the Codex CLI originator header.
	Originator = "codex_cli_rs"
	// MaxRetries for transient errors.
	MaxRetries = 3
	// RetryBaseDelayMs is the exponential backoff base.
	RetryBaseDelayMs = 1000
)

// SelectableEfforts is the OpenAI Responses effort ladder (swarm sentinels
// included for UI parity; swarm resolution is a swarm-feature concern).
var SelectableEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "swarm", "swarm-deep"}

// CompleteOptions carries per-request assembly inputs.
type CompleteOptions struct {
	Model       string
	Credentials *CodexCredentials
	System      string
	Input       []any
	Tools       []types.ToolDefinition
	WebSearch   config.WebSearchConfig
	// MaxOutputTokens applies to API-key requests only.
	MaxOutputTokens *uint32
	ReasoningEffort *string
	ServiceTier     *string
	PromptCacheKey  *string
	// PromptCacheRetention is the configured override.
	PromptCacheRetention *string
	// NativeCompactionThreshold, when set, emits context_management.
	NativeCompactionThreshold *int
	HTTPClient                *http.Client
	// Emit receives each stream event.
	Emit func(types.StreamEvent) error
	// OnRetry observes retry attempts (optional).
	OnRetry func(attempt, max uint32)
}

// resolvedReasoningEffort applies the explicit effort or the model default.
func (o *CompleteOptions) resolvedReasoningEffort() *string {
	if o.ReasoningEffort != nil {
		return o.ReasoningEffort
	}
	if effort, ok := DefaultReasoningEffortForModel(o.Model); ok {
		return &effort
	}
	return nil
}

// hostedTools computes the hosted web_search tool for this request.
func (o *CompleteOptions) hostedTools(isChatGPTMode bool) []map[string]any {
	return HostedToolsWithConfig(o.WebSearch, o.Model, FirstPartyBackend(isChatGPTMode), o.Tools)
}

// hostedSearchActive reports whether the hosted tool is attached, so stored
// web_search_call items replay verbatim instead of downgrading.
func (o *CompleteOptions) hostedSearchActive(isChatGPTMode bool) bool {
	return len(o.hostedTools(isChatGPTMode)) > 0
}

// ReasoningPayload is the Responses reasoning block: summaries always on.
func ReasoningPayload(effort string) map[string]any {
	return map[string]any{"effort": effort, "summary": "auto"}
}

// NormalizeReasoningEffort validates a configured effort value.
func NormalizeReasoningEffort(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", false
	}
	for _, known := range SelectableEfforts {
		if known == value {
			return value, true
		}
	}
	return "", false
}

// DefaultReasoningEffortForModel: GPT-5.6 Sol defaults to low; other models
// keep their API-side default.
func DefaultReasoningEffortForModel(model string) (string, bool) {
	key := CanonicalModelID(model)
	if strings.HasPrefix(key, "gpt-5.6-sol") {
		return "low", true
	}
	return "", false
}

// CanonicalModelID lowercases and strips the long-context suffix (ported from
// model_id::canonical's strip_long_context_suffix step used by OpenAI ids).
func CanonicalModelID(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for _, suffix := range []string{"-1m", "-4m"} {
		if strings.HasSuffix(normalized, suffix) {
			trimmed := strings.TrimSuffix(normalized, suffix)
			if strings.Contains(trimmed, "1m") || strings.Contains(normalized, "-1m") {
				return trimmed
			}
		}
	}
	return normalized
}

// APIReasoningEffort passes configured efforts through; swarm sentinels are
// resolved by the swarm feature and rejected here (documented deviation).
func APIReasoningEffort(effort *string) (string, bool) {
	if effort == nil {
		return "", false
	}
	if *effort == "swarm" || *effort == "swarm-deep" {
		return "", false
	}
	return *effort, true
}

// ModelSupportsAdditionalTools: Responses additional_tools / deferred loading
// requires gpt-5.4 or newer; unknown families stay eager.
func ModelSupportsAdditionalTools(modelID string) bool {
	lower := strings.ToLower(modelID)
	rest, ok := strings.CutPrefix(lower, "gpt-")
	if !ok {
		return false
	}
	version := ""
	for _, ch := range rest {
		if (ch >= '0' && ch <= '9') || ch == '.' {
			version += string(ch)
		} else {
			break
		}
	}
	parts := strings.Split(version, ".")
	major, err1 := strconv.Atoi(parts[0])
	if err1 != nil || version == "" {
		return false
	}
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	return major > 5 || (major == 5 && minor >= 4)
}

// ModelSupportsImageGeneration: codex models reject unknown hosted tools.
func ModelSupportsImageGeneration(modelID string) bool {
	return !strings.Contains(strings.ToLower(modelID), "codex")
}

// ModelSupportsWebSearch mirrors the image_generation rule.
func ModelSupportsWebSearch(modelID string) bool {
	return ModelSupportsImageGeneration(modelID)
}

// WebSearchNativeEnabled mirrors WebSearchConfig::native_enabled:
// prefer_native || engine == native.
func WebSearchNativeEnabled(cfg config.WebSearchConfig) bool {
	return cfg.PreferNative || cfg.Engine == config.WebSearchNative
}

// HostedWebSearchTool builds the hosted web_search definition from config.
func HostedWebSearchTool(cfg config.WebSearchConfig) map[string]any {
	tool := map[string]any{"type": "web_search"}
	var allowed []string
	for _, domain := range cfg.NativeAllowedDomains {
		if trimmed := strings.TrimSpace(domain); trimmed != "" {
			allowed = append(allowed, trimmed)
		}
	}
	if len(allowed) > 0 {
		tool["filters"] = map[string]any{"allowed_domains": allowed}
	}
	return tool
}

// FirstPartyBackend reports whether requests reach a backend implementing
// hosted tools: ChatGPT OAuth or the first-party API endpoint.
func FirstPartyBackend(isChatGPTMode bool) bool {
	return isChatGPTMode ||
		strings.TrimSuffix(ResolveAPIBase(), "/") == FirstPartyAPIBase
}

// SessionOffersWebsearch: the hosted tool only replaces the local websearch
// tool, so a session without it never gets provider-side search.
func SessionOffersWebsearch(tools []types.ToolDefinition) bool {
	for _, tool := range tools {
		if types.IsBuiltinLocalWebsearch(tool) {
			return true
		}
	}
	return false
}

// HostedToolsWithConfig gates the hosted tool on config, backend, model and
// session policy.
func HostedToolsWithConfig(cfg config.WebSearchConfig, modelID string, firstParty bool, tools []types.ToolDefinition) []map[string]any {
	if WebSearchNativeEnabled(cfg) && firstParty && ModelSupportsWebSearch(modelID) &&
		SessionOffersWebsearch(tools) {
		return []map[string]any{HostedWebSearchTool(cfg)}
	}
	return nil
}

// WithoutLocalWebsearch drops jcode's local search tool when hosted replaces it.
func WithoutLocalWebsearch(tools []types.ToolDefinition, hosted []map[string]any) []types.ToolDefinition {
	if len(hosted) == 0 {
		return tools
	}
	out := make([]types.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if !types.IsBuiltinLocalWebsearch(tool) {
			out = append(out, tool)
		}
	}
	return out
}

// usesPromptCacheOptions: gpt-5.6+ models use the newer cache options block
// instead of prompt_cache_retention.
func usesPromptCacheOptions(modelID string) bool {
	model := strings.ToLower(strings.TrimSpace(modelID))
	rest, ok := strings.CutPrefix(model, "gpt-")
	if !ok {
		return false
	}
	version := strings.SplitN(rest, "-", 2)[0]
	majorStr, minorStr, found := strings.Cut(version, ".")
	if !found {
		minorStr = "0"
	}
	major, err1 := strconv.Atoi(majorStr)
	minor, err2 := strconv.Atoi(minorStr)
	if err1 != nil || err2 != nil {
		return false
	}
	return major > 5 || (major == 5 && minor >= 6)
}

// supportsExtendedPromptCacheRetention lists models accepting 24h retention.
func supportsExtendedPromptCacheRetention(modelID string) bool {
	model := strings.ToLower(strings.TrimSpace(modelID))
	if model == "gpt-5.4-1m" {
		model = "gpt-5.4"
	}
	for _, known := range []string{
		"gpt-5.5", "gpt-5.5-pro", "gpt-5.4", "gpt-5.2", "gpt-5.1-codex-max",
		"gpt-5.1", "gpt-5.1-codex", "gpt-5.1-codex-mini",
	} {
		if model == known {
			return true
		}
	}
	return false
}

// NormalizePromptCacheRetention accepts the legacy override values.
func NormalizePromptCacheRetention(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case "in_memory":
		return "in_memory", true
	case "24h":
		return "24h", true
	}
	return "", false
}

// EffectivePromptCacheRetention: gpt-5.6+ omits the field (30m default);
// otherwise the normalized override or 24h for extended-retention models.
func EffectivePromptCacheRetention(modelID string, configured *string) (string, bool) {
	if usesPromptCacheOptions(modelID) {
		return "", false
	}
	if configured != nil {
		if normalized, ok := NormalizePromptCacheRetention(*configured); ok {
			return normalized, true
		}
	}
	if supportsExtendedPromptCacheRetention(modelID) {
		return "24h", true
	}
	return "", false
}

// BuildResponseRequest assembles the Responses request body
// (ported from build_response_request).
func BuildResponseRequest(
	modelID string,
	instructions string,
	input []any,
	apiTools []json.RawMessage,
	isChatGPTMode bool,
	maxOutputTokens *uint32,
	reasoningEffort *string,
	serviceTier *string,
	promptCacheKey *string,
	promptCacheRetention *string,
	nativeCompactionThreshold *int,
) map[string]any {
	tools := make([]any, 0, len(apiTools)+1)
	for _, tool := range apiTools {
		var decoded any
		if json.Unmarshal(tool, &decoded) == nil {
			tools = append(tools, decoded)
		}
	}
	// The hosted image_generation tool is general-models only; Codex models
	// reject unknown hosted tools.
	if isChatGPTMode && ModelSupportsImageGeneration(modelID) {
		tools = append(tools, map[string]any{"type": "image_generation"})
	}

	request := map[string]any{
		"model":               modelID,
		"instructions":        instructions,
		"input":               input,
		"tools":               tools,
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
		"stream":              true,
		"store":               false,
		"include":             []any{"reasoning.encrypted_content"},
	}

	if !isChatGPTMode && maxOutputTokens != nil {
		request["max_output_tokens"] = *maxOutputTokens
	}
	if reasoningEffort != nil {
		request["reasoning"] = ReasoningPayload(*reasoningEffort)
	}
	// A saved override or previous model's tier must not leak onto an
	// unsupported model.
	if serviceTier != nil && (*serviceTier != "ultrafast" || openaiModelSupportsUltrafast(modelID)) {
		request["service_tier"] = *serviceTier
	}
	if nativeCompactionThreshold != nil {
		request["context_management"] = []any{map[string]any{
			"type": "compaction", "compact_threshold": *nativeCompactionThreshold,
		}}
	}
	if !isChatGPTMode {
		if promptCacheKey != nil {
			request["prompt_cache_key"] = *promptCacheKey
		}
		if retention, ok := EffectivePromptCacheRetention(modelID, promptCacheRetention); ok {
			request["prompt_cache_retention"] = retention
		}
	}
	return request
}

// openaiModelSupportsUltrafast: tier support is reported live by the API; the
// static gate accepts every model that is not a codex variant (documented
// approximation of service_tier::openai_model_supports_ultrafast's catalog
// check, which depends on the model catalog snapshot).
func openaiModelSupportsUltrafast(modelID string) bool {
	return !strings.Contains(strings.ToLower(modelID), "codex")
}

// postOpenAIRefresh performs the token refresh HTTP call.
func postOpenAIRefresh(tokenURL, body string) (*CodexCredentials, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(tokenURL, "application/x-www-form-urlencoded",
		strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("OpenAI token refresh request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("OpenAI token refresh response body failed (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OpenAI token refresh failed: %s", string(raw))
	}
	var parsed openaiTokenResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("OpenAI token refresh returned malformed JSON: %w", err)
	}
	return &CodexCredentials{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		ExpiresAt:    time.Now().UnixMilli() + parsed.ExpiresIn*1000,
		IDToken:      parsed.IDToken,
	}, nil
}

// StreamComplete sends one Responses request and pumps SSE events into emit,
// retrying transient transport faults with fresh connections.
func StreamComplete(options CompleteOptions) error {
	var lastErr error
	for attempt := uint32(0); attempt <= MaxRetries; attempt++ {
		if attempt > 0 {
			if options.OnRetry != nil {
				options.OnRetry(attempt, MaxRetries)
			}
			// Consumers must discard partial output from the failed attempt.
			_ = options.Emit(types.RetryRollback{Attempt: attempt, Max: MaxRetries})
			delay := retryDelay(attempt)
			select {
			case <-time.After(delay):
			}
		}
		err := streamOnce(options)
		if err == nil {
			return nil
		}
		lastErr = err
		if !transporterr.IsTransientTransportError(err.Error()) {
			return err
		}
	}
	return lastErr
}

func retryDelay(attempt uint32) time.Duration {
	return time.Duration(RetryBaseDelayMs*(1<<(attempt-1))) * time.Millisecond
}

func streamOnce(options CompleteOptions) error {
	credentials := options.Credentials
	isChatGPTMode := credentials.IsChatGPTMode()
	// Downgrade hosted-search history when the request does not declare the
	// hosted web_search tool.
	input := options.Input
	if !options.hostedSearchActive(isChatGPTMode) {
		DowngradeWebSearchCalls(input)
	}

	tools := options.Tools
	hosted := options.hostedTools(isChatGPTMode)
	tools = WithoutLocalWebsearch(tools, hosted)
	apiTools := BuildTools(tools)
	for _, hostedTool := range hosted {
		encoded, err := json.Marshal(hostedTool)
		if err != nil {
			return err
		}
		apiTools = append(apiTools, encoded)
	}

	effort := options.resolvedReasoningEffort()
	apiEffort := effort
	if value, ok := APIReasoningEffort(effort); ok {
		apiEffort = &value
	} else {
		apiEffort = nil
	}

	request := BuildResponseRequest(
		options.Model, options.System, input, apiTools, isChatGPTMode,
		options.MaxOutputTokens, apiEffort, options.ServiceTier,
		options.PromptCacheKey, options.PromptCacheRetention,
		options.NativeCompactionThreshold,
	)

	body, err := json.Marshal(request)
	if err != nil {
		return err
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 180 * time.Second}
	}
	endpoint := ResponsesURL(credentials)
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	if isChatGPTMode {
		req.Header.Set("originator", Originator)
		if credentials.AccountID != nil {
			req.Header.Set("chatgpt-account-id", *credentials.AccountID)
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		// A connection dropped before response headers surfaces as a bare
		// EOF in net/http; wrap it so the transient classifier sees a
		// connection-closed fault (same treatment as the Anthropic transport).
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("Failed to send request to OpenAI API: connection closed before response headers")
		}
		return fmt.Errorf("Failed to send request to OpenAI API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		message := fmt.Sprintf("OpenAI API error (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		if IsEncryptedContentTooLargeError(string(raw)) {
			// Not transient: the encrypted replay payload itself is oversized.
			return fmt.Errorf("%s", message)
		}
		return fmt.Errorf("%s", message)
	}

	state := NewResponsesStreamState()
	_ = options.Emit(types.ConnectionPhaseEvent{Phase: types.PhaseStreaming})
	pump := PumpBytes(state, options.Emit)
	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if err := pump(buf[:n]); err != nil {
				return err
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return fmt.Errorf("Stream error: %w", readErr)
		}
	}
}
