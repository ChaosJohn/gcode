package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/types"
)

// Responses API SSE decoding (ported from stream.rs).

// responseSseEvent is the subset of Responses SSE fields jcode consumes.
type responseSseEvent struct {
	Type        string          `json:"type"`
	Item        json.RawMessage `json:"item"`
	Delta       *string         `json:"delta"`
	ItemID      *string         `json:"item_id"`
	OutputIndex *uint64         `json:"output_index"`
	CallID      *string         `json:"call_id"`
	Name        *string         `json:"name"`
	Arguments   *string         `json:"arguments"`
	Input       *string         `json:"input"`
	Response    json.RawMessage `json:"response"`
	Error       json.RawMessage `json:"error"`
}

const websocketFallbackNotice = "falling back from websockets to https transport"

var fallbackCounter uint64
var recoveredTextWrappedToolCalls uint64
var normalizedNullToolArguments uint64

func truncatedStreamPayloadContext(data string) string {
	s := strings.ReplaceAll(strings.TrimSpace(data), "\n", "\\n")
	runes := []rune(s)
	if len(runes) > 240 {
		return string(runes[:240])
	}
	return s
}

func isStructuredResponseEvent(data string) bool {
	var value map[string]any
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		return false
	}
	kind, _ := value["type"].(string)
	return strings.HasPrefix(kind, "response.") || kind == "error"
}

func isWebsocketFallbackNotice(data string) bool {
	// The proxy injects the fallback notice as a plain-text control frame. A
	// legitimate response.*/error event can contain this phrase in model
	// output and must still be parsed normally.
	if isStructuredResponseEvent(data) {
		return false
	}
	return strings.Contains(strings.ToLower(data), websocketFallbackNotice)
}

func jsonStrAt(raw json.RawMessage, keys ...string) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	current := value
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = m[key]
		if !ok {
			return "", false
		}
	}
	s, ok := current.(string)
	return s, ok
}

func jsonU64At(raw json.RawMessage, keys ...string) (uint64, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	current := value
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		current, ok = m[key]
		if !ok {
			return 0, false
		}
	}
	if f, ok := current.(float64); ok && f == float64(uint64(f)) {
		return uint64(f), true
	}
	return 0, false
}

// extractErrorWithRetry renders the message and retry hint from an error event.
func extractErrorWithRetry(response, topLevelError json.RawMessage) (string, *uint64) {
	var errorRaw json.RawMessage
	if len(response) > 0 {
		if s, ok := jsonStrAt(response, "error"); ok {
			_ = s
		}
		if errField := rawPath(response, "error"); errField != nil {
			errorRaw = errField
		}
	}
	if errorRaw == nil && len(topLevelError) > 0 {
		errorRaw = topLevelError
	}
	if errorRaw == nil {
		if len(response) > 0 {
			if msg, ok := jsonStrAt(response, "status_message"); ok {
				return msg, nil
			}
			if msg, ok := jsonStrAt(response, "message"); ok {
				return msg, nil
			}
		}
		return "OpenAI response stream error (no error details)", nil
	}

	message, ok := jsonStrAt(errorRaw, "message")
	if !ok {
		message = "OpenAI response stream error (unknown)"
	}
	errorType, hasType := jsonStrAt(errorRaw, "type")
	code, hasCode := jsonStrAt(errorRaw, "code")

	messageLower := strings.ToLower(message)
	switch {
	case hasType && hasCode &&
		!strings.Contains(messageLower, strings.ToLower(errorType)) &&
		!strings.Contains(messageLower, strings.ToLower(code)):
		message = fmt.Sprintf("%s (%s): %s", errorType, code, message)
	case hasType && !strings.Contains(messageLower, strings.ToLower(errorType)):
		message = fmt.Sprintf("%s: %s", errorType, message)
	case hasCode && !strings.Contains(messageLower, strings.ToLower(code)):
		message = fmt.Sprintf("%s: %s", code, message)
	}

	var retryAfter *uint64
	if v, ok := jsonU64At(errorRaw, "retry_after"); ok {
		retryAfter = &v
	} else if v, ok := jsonU64At(response, "retry_after"); ok {
		retryAfter = &v
	}
	return message, retryAfter
}

func rawPath(raw json.RawMessage, keys ...string) json.RawMessage {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	current := value
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = m[key]
		if !ok {
			return nil
		}
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return nil
	}
	return encoded
}

// ParseTextWrappedToolCall recovers `to=functions.<name>{...}` from plain text.
// Returns (prefix, toolName, arguments, suffix).
func ParseTextWrappedToolCall(text string) (prefix, toolName, arguments, suffix string, ok bool) {
	const marker = "to=functions."
	markerIdx := strings.Index(text, marker)
	if markerIdx < 0 {
		return "", "", "", "", false
	}
	afterMarker := text[markerIdx+len(marker):]

	toolNameEnd := 0
	for idx, ch := range afterMarker {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_' {
			toolNameEnd = idx + len(string(ch))
		} else {
			break
		}
	}
	if toolNameEnd == 0 {
		return "", "", "", "", false
	}

	toolName = afterMarker[:toolNameEnd]
	remaining := afterMarker[toolNameEnd:]
	var fallback *struct{ p, n, a, s string }
	for braceIdx, ch := range remaining {
		if ch != '{' {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(remaining[braceIdx:]))
		decoder.UseNumber()
		var parsed any
		if err := decoder.Decode(&parsed); err != nil {
			continue
		}
		consumed := decoder.InputOffset()
		if _, isObj := parsed.(map[string]any); !isObj {
			continue
		}
		prefix = strings.TrimRight(text[:markerIdx], " \t\n\r")
		suffix = strings.TrimSpace(remaining[braceIdx+int(consumed):])
		encoded, err := json.Marshal(parsed)
		if err != nil {
			continue
		}
		if suffix == "" {
			return prefix, toolName, string(encoded), "", true
		}
		if fallback == nil {
			fallback = &struct{ p, n, a, s string }{prefix, toolName, string(encoded), suffix}
		}
	}
	if fallback != nil {
		return fallback.p, fallback.n, fallback.a, fallback.s, true
	}
	return "", "", "", "", false
}

func sanitizeRecoveredToolSuffix(suffix string) string {
	trimmed := strings.TrimSpace(suffix)
	if trimmed == "" {
		return ""
	}
	normalized := strings.TrimLeft(trimmed, `"`)
	if strings.HasPrefix(normalized, `,"item_id"`) ||
		strings.HasPrefix(normalized, `,"output_index"`) ||
		strings.HasPrefix(normalized, `,"sequence_number"`) ||
		strings.HasPrefix(normalized, `,"call_id"`) ||
		strings.HasPrefix(normalized, `,"type":"response.`) ||
		(strings.HasPrefix(normalized, ",") &&
			strings.Contains(normalized, `"item_id"`) &&
			(strings.Contains(normalized, `"output_index"`) ||
				strings.Contains(normalized, `"sequence_number"`))) {
		return ""
	}
	return suffix
}

// streamTextOrRecoveredToolCall either emits a plain text delta or expands a
// text-wrapped tool call into a full synthetic call sequence.
func streamTextOrRecoveredToolCall(text string, pending *[]types.StreamEvent) (types.StreamEvent, bool) {
	if text == "" {
		return types.StreamEvent(nil), false
	}
	if prefix, toolName, arguments, suffix, ok := ParseTextWrappedToolCall(text); ok {
		recoveredTextWrappedToolCalls++
		suffix = sanitizeRecoveredToolSuffix(suffix)
		if prefix != "" {
			*pending = append(*pending, types.TextDelta{Text: prefix})
		}
		fallbackCounter++
		id := fmt.Sprintf("fallback_text_call_%d", fallbackCounter)
		*pending = append(*pending, types.ToolUseStart{ID: id, Name: toolName})
		*pending = append(*pending, types.ToolInputDeltaFor{ID: id, Delta: arguments})
		*pending = append(*pending, types.ToolUseEndFor{ID: id})
		if suffix != "" {
			*pending = append(*pending, types.TextDelta{Text: suffix})
		}
		first := (*pending)[0]
		*pending = (*pending)[1:]
		return first, true
	}
	return types.TextDelta{Text: text}, true
}

func normalizeOpenAIToolArguments(rawArguments string) string {
	trimmed := strings.TrimSpace(rawArguments)
	if trimmed == "" || trimmed == "null" {
		normalizedNullToolArguments++
		return "{}"
	}
	return rawArguments
}

func streamingToolItemID(item json.RawMessage) (string, bool) {
	if id, ok := jsonStrAt(item, "id"); ok && strings.TrimSpace(id) != "" {
		return id, true
	}
	if id, ok := jsonStrAt(item, "item_id"); ok && strings.TrimSpace(id) != "" {
		return id, true
	}
	return "", false
}

// streamingToolCallState tracks one in-flight parallel call.
type streamingToolCallState struct {
	identities       []string
	fallbackID       string
	callID           *string
	name             *string
	arguments        string
	emittedID        string
	started          bool
	emittedArguments int
	complete         bool
	order            int
}

// toolCallState resolves the state for an event via stable aliases: item-only
// and call-only snapshots find the same call when a provider regenerates IDs.
func toolCallState(calls map[string]*streamingToolCallState, completed map[string]struct{},
	outputIndex *uint64, itemID, callID *string) *streamingToolCallState {

	trimOK := func(s *string) (string, bool) {
		if s == nil || strings.TrimSpace(*s) == "" {
			return "", false
		}
		return *s, true
	}
	itemKey, hasItem := trimOK(itemID)
	callKey, hasCall := trimOK(callID)

	var identities []string
	if outputIndex != nil {
		identities = append(identities, fmt.Sprintf("\x00output:%d", *outputIndex))
	}
	if hasItem {
		identities = append(identities, itemKey)
	}
	if hasCall {
		identities = append(identities, "\x00call:"+callKey)
	}
	if len(identities) == 0 {
		return nil
	}
	for _, id := range identities {
		if _, done := completed[id]; done {
			for _, other := range identities {
				completed[other] = struct{}{}
			}
			return nil
		}
	}
	key := identities[0]
	for _, id := range identities {
		found := ""
		for stateKey, state := range calls {
			for _, existing := range state.identities {
				if existing == id {
					found = stateKey
					break
				}
			}
			if found != "" {
				break
			}
		}
		if found != "" {
			key = found
			break
		}
	}
	state, exists := calls[key]
	if !exists {
		order := 0
		for _, existing := range calls {
			if existing.order > order {
				order = existing.order
			}
		}
		order++
		fallbackID := ""
		if hasItem {
			fallbackID = itemKey
		} else if hasCall {
			fallbackID = callKey
		} else {
			fallbackCounter++
			fallbackID = fmt.Sprintf("fallback_call_%d", fallbackCounter)
		}
		state = &streamingToolCallState{fallbackID: fallbackID, order: order}
		calls[key] = state
	}
	state.identities = append(state.identities, identities...)
	return state
}

func updateToolCallFromItem(state *streamingToolCallState, item json.RawMessage, complete bool) bool {
	if id, ok := jsonStrAt(item, "call_id"); ok && strings.TrimSpace(id) != "" {
		state.callID = &id
	}
	if name, ok := jsonStrAt(item, "name"); ok {
		state.name = &name
	}
	var arguments *string
	if raw := rawPath(item, "arguments"); raw != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			arguments = &s
		} else {
			encoded := string(raw)
			arguments = &encoded
		}
	} else if raw := rawPath(item, "input"); raw != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			arguments = &s
		} else {
			encoded := string(raw)
			arguments = &encoded
		}
	}
	if complete {
		return completeToolArguments(state, arguments)
	}
	if state.arguments == "" && arguments != nil {
		state.arguments = *arguments
	}
	return true
}

func inconsistentToolArguments() types.StreamEvent {
	return types.ErrorEvent{
		Message: "OpenAI tool argument snapshot differs from streamed arguments",
	}
}

// streamToolCalls advances every named call independently, in first-seen order.
func streamToolCalls(calls map[string]*streamingToolCallState, completed map[string]struct{},
	pending *[]types.StreamEvent) (types.StreamEvent, bool) {

	type entry struct {
		order int
		key   string
	}
	var ready []entry
	for key, state := range calls {
		if state.started || (state.name != nil && *state.name != "") {
			ready = append(ready, entry{state.order, key})
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].order < ready[j].order })

	for _, item := range ready {
		state := calls[item.key]
		if state.emittedID == "" {
			source := state.fallbackID
			if state.callID != nil && strings.TrimSpace(*state.callID) != "" {
				source = *state.callID
			}
			state.emittedID = types.SanitizeToolID(source)
		}
		id := state.emittedID
		if !state.started {
			*pending = append(*pending, types.ToolUseStart{ID: id, Name: *state.name})
			state.started = true
		}
		if state.complete {
			state.arguments = normalizeOpenAIToolArguments(state.arguments)
		}
		// Hold a possible empty/null value until it can be normalized;
		// ordinary JSON fragments flow through without waiting.
		if state.complete || !strings.HasPrefix("null", strings.TrimSpace(state.arguments)) {
			if state.emittedArguments < len(state.arguments) {
				delta := state.arguments[state.emittedArguments:]
				*pending = append(*pending, types.ToolInputDeltaFor{ID: id, Delta: delta})
				state.emittedArguments = len(state.arguments)
			}
		}
		if !state.complete {
			continue
		}
		*pending = append(*pending, types.ToolUseEndFor{ID: id})
		for _, identity := range state.identities {
			completed[identity] = struct{}{}
		}
		delete(calls, item.key)
	}
	if len(*pending) == 0 {
		return types.StreamEvent(nil), false
	}
	first := (*pending)[0]
	*pending = (*pending)[1:]
	return first, true
}

// completeToolArguments folds a done snapshot: only its unseen suffix may be
// appended, otherwise the consumer parses duplicated JSON.
func completeToolArguments(state *streamingToolCallState, arguments *string) bool {
	if arguments != nil {
		emitted := state.arguments
		if state.emittedArguments < len(state.arguments) {
			emitted = state.arguments[:state.emittedArguments]
		}
		if !strings.HasPrefix(*arguments, emitted) {
			return false
		}
		state.arguments = *arguments
	}
	state.complete = true
	return true
}

// ResponsesStreamState carries the decoder across SSE events.
type ResponsesStreamState struct {
	pending            []types.StreamEvent
	sawTextDelta       bool
	sawThinkingDelta   bool
	streamingToolCalls map[string]*streamingToolCallState
	completedToolItems map[string]struct{}
	fallbackCounter    uint64
}

// NewResponsesStreamState initializes decoder state.
func NewResponsesStreamState() *ResponsesStreamState {
	return &ResponsesStreamState{
		streamingToolCalls: map[string]*streamingToolCallState{},
		completedToolItems: map[string]struct{}{},
	}
}

// ParseOpenAIResponseEvent maps one SSE data payload to zero or more events;
// the first event is returned and the rest queue in state.pending.
func (s *ResponsesStreamState) ParseOpenAIResponseEvent(data string) (types.StreamEvent, bool) {
	if data == "[DONE]" {
		return types.MessageEnd{}, true
	}
	if isWebsocketFallbackNotice(data) {
		return types.StreamEvent(nil), false
	}
	if strings.Contains(strings.ToLower(data), "stream disconnected before completion") &&
		!isStructuredResponseEvent(data) {
		return types.ErrorEvent{Message: data}, true
	}

	var event responseSseEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return types.StreamEvent(nil), false
	}

	switch event.Type {
	case "response.created":
		if tier, ok := extractServiceTier(event.Response); ok {
			return tier, true
		}
	case "response.output_text.delta":
		if event.Delta != nil {
			s.sawTextDelta = true
			return streamTextOrRecoveredToolCall(*event.Delta, &s.pending)
		}
	case "response.reasoning.delta", "response.reasoning_summary_text.delta":
		if event.Delta != nil {
			s.sawThinkingDelta = true
			return types.ThinkingDelta{Delta: *event.Delta}, true
		}
	case "response.reasoning.done", "response.output_item.added":
		if len(event.Item) > 0 {
			itemType, _ := jsonStrAt(event.Item, "type")
			switch itemType {
			case "reasoning":
				return types.ThinkingStart{}, true
			case "function_call", "custom_tool_call":
				itemID, _ := streamingToolItemID(event.Item)
				itemIDCopy := itemID
				var itemIDPtr *string
				if itemID != "" {
					itemIDPtr = &itemIDCopy
				}
				state := toolCallState(s.streamingToolCalls, s.completedToolItems,
					event.OutputIndex, itemIDPtr, strField(event.Item, "call_id"))
				if state == nil {
					return types.StreamEvent(nil), false
				}
				updateToolCallFromItem(state, event.Item, false)
				return streamToolCalls(s.streamingToolCalls, s.completedToolItems, &s.pending)
			}
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		state := toolCallState(s.streamingToolCalls, s.completedToolItems,
			event.OutputIndex, event.ItemID, event.CallID)
		if state == nil {
			return types.StreamEvent(nil), false
		}
		if event.CallID != nil && strings.TrimSpace(*event.CallID) != "" {
			state.callID = event.CallID
		}
		if event.Name != nil {
			state.name = event.Name
		}
		if event.Delta != nil {
			state.arguments += *event.Delta
		}
		return streamToolCalls(s.streamingToolCalls, s.completedToolItems, &s.pending)
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		state := toolCallState(s.streamingToolCalls, s.completedToolItems,
			event.OutputIndex, event.ItemID, event.CallID)
		if state == nil {
			return types.StreamEvent(nil), false
		}
		if event.CallID != nil && strings.TrimSpace(*event.CallID) != "" {
			state.callID = event.CallID
		}
		if event.Name != nil {
			state.name = event.Name
		}
		snapshot := event.Arguments
		if snapshot == nil {
			snapshot = event.Input
		}
		if !completeToolArguments(state, snapshot) {
			return inconsistentToolArguments(), true
		}
		return streamToolCalls(s.streamingToolCalls, s.completedToolItems, &s.pending)
	case "response.output_item.done":
		if len(event.Item) > 0 {
			itemType, _ := jsonStrAt(event.Item, "type")
			if itemType == "function_call" || itemType == "custom_tool_call" {
				hasIdentity := event.OutputIndex != nil
				if itemID, ok := streamingToolItemID(event.Item); ok && itemID != "" {
					hasIdentity = true
				}
				if callID, ok := jsonStrAt(event.Item, "call_id"); ok && strings.TrimSpace(callID) != "" {
					hasIdentity = true
				}
				if hasIdentity {
					itemID, _ := streamingToolItemID(event.Item)
					itemIDCopy := itemID
					var itemIDPtr *string
					if itemID != "" {
						itemIDPtr = &itemIDCopy
					}
					state := toolCallState(s.streamingToolCalls, s.completedToolItems,
						event.OutputIndex, itemIDPtr, strField(event.Item, "call_id"))
					if state == nil {
						return types.StreamEvent(nil), false
					}
					if !updateToolCallFromItem(state, event.Item, true) {
						return inconsistentToolArguments(), true
					}
					return streamToolCalls(s.streamingToolCalls, s.completedToolItems, &s.pending)
				}
			}
			isMessage := itemType == "message"
			first := s.handleOpenAIOutputItem(event.Item)
			if isMessage {
				// output_item.done, not reasoning or response.completed, is
				// the assistant-message boundary.
				s.pending = append(s.pending, types.TextDone{})
				s.sawTextDelta = false
			}
			if first != nil {
				return first, true
			}
			if len(s.pending) > 0 {
				out := s.pending[0]
				s.pending = s.pending[1:]
				return out, true
			}
		}
	case "response.incomplete":
		stopReason := ""
		if len(event.Response) > 0 {
			if reason, ok := extractStopReasonFromResponse(event.Response); ok {
				stopReason = reason
			}
		}
		if stopReason == "" {
			stopReason = "incomplete"
		}
		if len(event.Response) > 0 {
			if usage, ok := extractUsageFromResponse(event.Response); ok {
				s.pending = append(s.pending, usage)
			}
		}
		s.pending = append(s.pending, types.MessageEnd{StopReason: &stopReason})
		out := s.pending[0]
		s.pending = s.pending[1:]
		return out, true
	case "response.completed":
		if len(event.Response) > 0 {
			if tier, ok := extractServiceTier(event.Response); ok {
				s.pending = append(s.pending, tier)
			}
		}
		var stopReason *string
		if len(event.Response) > 0 {
			if reason, ok := extractStopReasonFromResponse(event.Response); ok {
				stopReason = &reason
			}
		}
		if len(event.Response) > 0 {
			if usage, ok := extractUsageFromResponse(event.Response); ok {
				s.pending = append(s.pending, usage)
			}
		}
		s.pending = append(s.pending, types.MessageEnd{StopReason: stopReason})
		out := s.pending[0]
		s.pending = s.pending[1:]
		return out, true
	case "response.failed", "response.error", "error":
		message, retryAfter := extractErrorWithRetry(event.Response, event.Error)
		return types.ErrorEvent{Message: message, RetryAfterSecs: retryAfter}, true
	}
	return types.StreamEvent(nil), false
}

func strField(raw json.RawMessage, key string) *string {
	if s, ok := jsonStrAt(raw, key); ok {
		return &s
	}
	return nil
}

func extractServiceTier(response json.RawMessage) (types.StreamEvent, bool) {
	tier, ok := jsonStrAt(response, "service_tier")
	if !ok || strings.TrimSpace(tier) == "" {
		return nil, false
	}
	return types.ServiceTierEvent{Tier: tier}, true
}

func extractLastAssistantMessagePhase(response json.RawMessage) (string, bool) {
	output := rawPath(response, "output")
	if output == nil {
		return "", false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(output, &items); err != nil {
		return "", false
	}
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if t, _ := jsonStrAt(item, "type"); t != "message" {
			continue
		}
		if role, _ := jsonStrAt(item, "role"); role != "assistant" {
			continue
		}
		if phase, ok := jsonStrAt(item, "phase"); ok {
			return phase, true
		}
		return "", false
	}
	return "", false
}

func extractStopReasonFromResponse(response json.RawMessage) (string, bool) {
	status, hasStatus := jsonStrAt(response, "status")
	if hasStatus && status == "completed" {
		if phase, ok := extractLastAssistantMessagePhase(response); ok && phase == "commentary" {
			return "commentary", true
		}
		return "", false
	}
	if reason, ok := jsonStrAt(response, "incomplete_details", "reason"); ok {
		return reason, true
	}
	if hasStatus && status != "" {
		return status, true
	}
	return "", false
}

// handleOpenAIOutputItem converts a completed output item into events.
func (s *ResponsesStreamState) handleOpenAIOutputItem(item json.RawMessage) types.StreamEvent {
	itemType, ok := jsonStrAt(item, "type")
	if !ok {
		return nil
	}
	switch itemType {
	case "compaction":
		encrypted, ok := jsonStrAt(item, "encrypted_content")
		if !ok {
			return nil
		}
		return types.CompactionEvent{
			Trigger:                "openai_native_auto",
			OpenAIEncryptedContent: &encrypted,
		}
	case "function_call", "custom_tool_call":
		callID := ""
		if s, ok := jsonStrAt(item, "call_id"); ok {
			callID = s
		}
		name := ""
		if s, ok := jsonStrAt(item, "name"); ok {
			name = s
		}
		rawArguments := "{}"
		if s, ok := jsonStrAt(item, "arguments"); ok {
			rawArguments = s
		} else if raw := rawPath(item, "input"); raw != nil {
			var value any
			if json.Unmarshal(raw, &value) == nil {
				switch v := value.(type) {
				case map[string]any, []any:
					encoded, _ := json.Marshal(v)
					rawArguments = string(encoded)
				case string:
					rawArguments = v
				}
			}
		}
		arguments := normalizeOpenAIToolArguments(rawArguments)
		s.pending = append(s.pending, types.ToolUseStart{ID: callID, Name: name})
		s.pending = append(s.pending, types.ToolInputDeltaFor{ID: callID, Delta: arguments})
		s.pending = append(s.pending, types.ToolUseEndFor{ID: callID})
		out := s.pending[0]
		s.pending = s.pending[1:]
		return out
	case "image_generation_call":
		return s.handleImageGenerationItem(item)
	case "web_search_call":
		// Hosted web search already ran on OpenAI's side; keep the item
		// verbatim so it replays in later requests.
		return types.ProviderNativeEvent{Provider: types.ProviderNativeOpenAI, Item: item}
	case "message":
		text := ""
		rendered := ""
		content := rawPath(item, "content")
		if content != nil {
			var entries []json.RawMessage
			if err := json.Unmarshal(content, &entries); err == nil {
				for _, entry := range entries {
					entryType, _ := jsonStrAt(entry, "type")
					if entryType != "output_text" && entryType != "text" {
						continue
					}
					if textPart, ok := jsonStrAt(entry, "text"); ok {
						text += textPart
						rendered += RenderAnnotatedText(textPart, rawPath(entry, "annotations"))
					}
				}
			}
		}
		if s.sawTextDelta {
			// URL annotations arrive after raw deltas; correct only this
			// completed message, before TextDone closes the render frame.
			if rendered != text {
				return types.TextSuffixReplace{OldText: text, NewText: rendered}
			}
			return nil
		}
		event, _ := streamTextOrRecoveredToolCall(rendered, &s.pending)
		return event
	case "reasoning":
		id := ""
		if s, ok := jsonStrAt(item, "id"); ok {
			id = s
		}
		var summary []string
		if summaryRaw := rawPath(item, "summary"); summaryRaw != nil {
			var entries []json.RawMessage
			if err := json.Unmarshal(summaryRaw, &entries); err == nil {
				for _, entry := range entries {
					if t, _ := jsonStrAt(entry, "type"); t == "summary_text" {
						if text, ok := jsonStrAt(entry, "text"); ok {
							summary = append(summary, text)
						}
					}
				}
			}
		}
		var encryptedContent *string
		if enc, ok := jsonStrAt(item, "encrypted_content"); ok {
			encryptedContent = &enc
		}
		status := strField(item, "status")

		if id != "" && (encryptedContent != nil || len(summary) > 0) {
			s.pending = append(s.pending, types.OpenAIReasoningItem{
				ID: id, Summary: summary, EncryptedContent: encryptedContent, Status: status,
			})
		}
		// Replay the full summary only when it was NOT already streamed live;
		// otherwise the thinking text renders twice.
		if len(summary) > 0 && !s.sawThinkingDelta {
			s.pending = append(s.pending, types.ThinkingStart{})
			s.pending = append(s.pending, types.ThinkingDelta{Delta: strings.Join(summary, "\n")})
			s.pending = append(s.pending, types.ThinkingEnd{})
		}
		if len(s.pending) == 0 {
			return nil
		}
		out := s.pending[0]
		s.pending = s.pending[1:]
		return out
	}
	return nil
}

var controlChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// handleImageGenerationItem decodes and saves a provider-generated image.
func (s *ResponsesStreamState) handleImageGenerationItem(item json.RawMessage) types.StreamEvent {
	resultB64, ok := jsonStrAt(item, "result")
	if !ok || resultB64 == "" {
		return nil
	}
	imageBytes, err := base64.StdEncoding.DecodeString(resultB64)
	if err != nil {
		return types.TextDelta{Text: "\n[Generated image received, but Jcode could not decode it.]\n"}
	}

	outputFormat := "png"
	if f, ok := jsonStrAt(item, "output_format"); ok {
		outputFormat = f
	}
	extension := "png"
	switch outputFormat {
	case "jpeg", "jpg":
		extension = "jpg"
	case "webp":
		extension = "webp"
	}
	itemID := "image"
	if s, ok := jsonStrAt(item, "id"); ok && s != "" {
		itemID = s
	}
	var safeID strings.Builder
	for _, ch := range itemID {
		if len(safeID.String()) >= 80 {
			break
		}
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			safeID.WriteRune(ch)
		}
	}
	safeIDStr := safeID.String()
	if safeIDStr == "" {
		safeIDStr = "image"
	}
	timestampMs := time.Now().UnixMilli()
	cwd, err := os.Getwd()
	if err != nil {
		cwd = os.TempDir()
	}
	dir := filepath.Join(cwd, ".gcode", "generated-images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return types.TextDelta{
			Text: fmt.Sprintf("\n[Generated image received (%d bytes), but Jcode could not save it.]\n", len(imageBytes)),
		}
	}
	filename := fmt.Sprintf("%d-%s.%s", timestampMs, safeIDStr, extension)
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, imageBytes, 0o644); err != nil {
		return types.TextDelta{Text: "\n[Generated image received, but Jcode could not save it.]\n"}
	}

	metadataPathString := ""
	var responseItem map[string]any
	if json.Unmarshal(item, &responseItem) == nil {
		delete(responseItem, "result")
	}
	revisedPrompt := strField(item, "revised_prompt")
	metadata := map[string]any{
		"schema_version":     1,
		"provider":           "openai",
		"native_tool":        "image_generation",
		"id":                 itemID,
		"status":             strField(item, "status"),
		"created_at_unix_ms": timestampMs,
		"image_path":         path,
		"output_format":      outputFormat,
		"byte_count":         len(imageBytes),
		"revised_prompt":     revisedPrompt,
		"response_item":      responseItem,
	}
	if encoded, err := json.MarshalIndent(metadata, "", "  "); err == nil {
		metadataPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
		if err := os.WriteFile(metadataPath, encoded, 0o644); err == nil {
			metadataPathString = metadataPath
		}
	}

	markdown := fmt.Sprintf("\n![Generated image](%s)\n\nGenerated image saved to `%s`.", path, path)
	if metadataPathString != "" {
		markdown += fmt.Sprintf("\nMetadata saved to `%s`.", metadataPathString)
	}
	markdown += "\n"
	s.pending = append(s.pending, types.TextDelta{Text: markdown})

	return types.GeneratedImage{
		ID:            itemID,
		Path:          path,
		MetadataPath:  strPtrOrNil(metadataPathString),
		OutputFormat:  outputFormat,
		RevisedPrompt: revisedPrompt,
	}
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func extractInputTokenDetail(usage json.RawMessage, field string) (*uint64, bool) {
	for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
		if v, ok := jsonU64At(usage, key, field); ok {
			return &v, true
		}
	}
	return nil, false
}

func extractUsageFromResponse(response json.RawMessage) (types.StreamEvent, bool) {
	usage := rawPath(response, "usage")
	if usage == nil {
		return nil, false
	}
	var inputTokens, outputTokens *uint64
	if v, ok := jsonU64At(usage, "input_tokens"); ok {
		inputTokens = &v
	}
	if v, ok := jsonU64At(usage, "output_tokens"); ok {
		outputTokens = &v
	}
	// OpenAI input_tokens is the total including cache subsets; missing
	// details mean unknown, not zero.
	cacheRead, hasRead := extractInputTokenDetail(usage, "cached_tokens")
	cacheWrite, hasWrite := extractInputTokenDetail(usage, "cache_write_tokens")
	if inputTokens != nil || outputTokens != nil || hasRead || hasWrite {
		return types.TokenUsageEvent{
			InputTokens:              inputTokens,
			OutputTokens:             outputTokens,
			CacheReadInputTokens:     cacheRead,
			CacheCreationInputTokens: cacheWrite,
		}, true
	}
	return nil, false
}

// utf8StreamDecoder carries incomplete multi-byte sequences across chunk
// boundaries so split characters are not dropped (#609).
type utf8StreamDecoder struct {
	buf []byte
}

func (d *utf8StreamDecoder) Decode(chunk []byte) string {
	d.buf = append(d.buf, chunk...)
	n := len(d.buf)
	cut := n
scan:
	for i := 1; i <= 3 && i <= n; i++ {
		b := d.buf[n-i]
		if b < 0x80 {
			break
		}
		var need int
		switch {
		case b&0xE0 == 0xC0:
			need = 2
		case b&0xF0 == 0xE0:
			need = 3
		case b&0xF8 == 0xF0:
			need = 4
		default:
			break scan // invalid lead byte: emit replacement now
		}
		if i < need {
			cut = n - i
		} else {
			break
		}
	}
	text := string(d.buf[:cut])
	d.buf = append(d.buf[:0], d.buf[cut:]...)
	return text
}

// PumpBytes drives the SSE framing over raw HTTP chunks: split on blank
// lines, join data: lines with newline, parse each payload.
func PumpBytes(state *ResponsesStreamState, emit func(types.StreamEvent) error) func([]byte) error {
	var buffer strings.Builder
	utf8 := &utf8StreamDecoder{}
	return func(chunk []byte) error {
		text := utf8.Decode(chunk)
		buffer.WriteString(text)
		for {
			accumulated := buffer.String()
			pos := strings.Index(accumulated, "\n\n")
			if pos < 0 {
				return nil
			}
			eventStr := accumulated[:pos]
			buffer.Reset()
			buffer.WriteString(accumulated[pos+2:])

			var dataLines []string
			for _, line := range strings.Split(eventStr, "\n") {
				if data, ok := sseDataLine(line); ok {
					dataLines = append(dataLines, data)
				}
			}
			if len(dataLines) == 0 {
				continue
			}
			data := strings.Join(dataLines, "\n")
			// One parse per SSE payload; its queued side events drain in order.
			event, ok := state.ParseOpenAIResponseEvent(data)
			if ok && event != nil {
				if err := emit(event); err != nil {
					return err
				}
			}
			for len(state.pending) > 0 {
				next := state.pending[0]
				state.pending = state.pending[1:]
				if err := emit(next); err != nil {
					return err
				}
			}
		}
	}
}

func sseDataLine(line string) (string, bool) {
	if strings.HasPrefix(line, "data:") {
		rest := line[len("data:"):]
		rest = strings.TrimPrefix(rest, " ")
		return rest, true
	}
	return "", false
}

// RenderAnnotatedText renders Responses URL citations into Markdown links.
// Indices are character offsets, not byte offsets; the completed item is
// authoritative.
func RenderAnnotatedText(text string, annotations json.RawMessage) string {
	if annotations == nil {
		return text
	}
	var list []json.RawMessage
	if err := json.Unmarshal(annotations, &list); err != nil {
		return text
	}

	runes := []rune(text)
	if len(runes) == 0 {
		return text
	}
	// Byte offsets of every character boundary (range over the string, not
	// the rune slice: indices are byte offsets).
	boundaries := make([]int, 0, len(runes)+1)
	for idx := range text {
		boundaries = append(boundaries, idx)
	}
	boundaries = append(boundaries, len(text))

	type citation struct {
		start, end int
		link       string
	}
	// rawURLs maps rendered link -> original URL for dedupe checks.
	rawURLs := map[string]string{}
	var citations []citation
	for _, annotation := range list {
		if t, _ := jsonStrAt(annotation, "type"); t != "url_citation" {
			continue
		}
		startIdx, okStart := jsonU64At(annotation, "start_index")
		endIdx, okEnd := jsonU64At(annotation, "end_index")
		linkURL, okURL := jsonStrAt(annotation, "url")
		if !okStart || !okEnd || !okURL {
			continue
		}
		if startIdx >= uint64(len(boundaries)) || endIdx >= uint64(len(boundaries)) {
			continue
		}
		start, end := boundaries[startIdx], boundaries[endIdx]
		if start > end {
			continue
		}
		parsed, err := url.Parse(linkURL)
		if err != nil {
			continue
		}
		scheme := parsed.Scheme
		if (scheme != "http" && scheme != "https") || parsed.Hostname() == "" ||
			controlChars.MatchString(linkURL) {
			continue
		}
		title := linkURL
		if t, ok := jsonStrAt(annotation, "title"); ok && strings.TrimSpace(t) != "" {
			title = t
		}
		var label strings.Builder
		for _, ch := range title {
			if ch < 0x20 || ch == 0x7f {
				label.WriteRune(' ')
				continue
			}
			if isASCIIPunct(ch) {
				label.WriteByte('\\')
			}
			label.WriteRune(ch)
		}
		destination := strings.NewReplacer(" ", "%20", "<", "%3C", ">", "%3E", "\\", "%5C").Replace(linkURL)
		link := fmt.Sprintf("[%s](<%s>)", label.String(), destination)
		rawURLs[link] = destination
		citations = append(citations, citation{start, end, link})
	}
	if len(citations) == 0 {
		return text
	}

	// The OAuth backend can already return a Markdown citation covering the
	// annotation's URL; keep that source rather than appending a second link.
	kept := make([]citation, 0, len(citations))
	for _, c := range citations {
		destination := strings.NewReplacer(" ", "%20", "<", "%3C", ">", "%3E", "\\", "%5C").Replace(rawURLs[c.link])
		cited := text[c.start:c.end]
		if strings.Contains(cited, "]("+destination+")") ||
			strings.Contains(cited, "](<"+destination+">)") {
			continue
		}
		kept = append(kept, c)
	}
	citations = kept
	if len(citations) == 0 {
		return text
	}

	sort.SliceStable(citations, func(i, j int) bool {
		if citations[i].start != citations[j].start {
			return citations[i].start < citations[j].start
		}
		return citations[i].end < citations[j].end
	})

	var rendered strings.Builder
	cursor := 0
	index := 0
	for index < len(citations) {
		start, end := citations[index].start, citations[index].end
		var links []string
		for index < len(citations) && citations[index].start == start && citations[index].end == end {
			link := citations[index].link
			duplicate := false
			for _, existing := range links {
				if existing == link {
					duplicate = true
				}
			}
			if !duplicate {
				links = append(links, link)
			}
			index++
		}
		if start < cursor {
			// Ambiguous overlapping spans must not eat rendered prose.
			continue
		}
		rendered.WriteString(text[cursor:start])
		cited := text[start:end]
		trimmed := strings.TrimSpace(cited)
		markerStart := "\ue200cite\ue202"
		markerEnd := "\ue201"
		isMarker := strings.HasPrefix(trimmed, markerStart) &&
			strings.HasSuffix(trimmed, markerEnd) &&
			!strings.Contains(trimmed[:len(trimmed)-len(markerEnd)], markerEnd)
		if isMarker {
			rendered.WriteString(cited[:len(cited)-len(strings.TrimLeft(cited, " \t\n\r"))])
		} else {
			rendered.WriteString(cited)
			if len(rendered.String()) > 0 {
				last := []rune(rendered.String())[len([]rune(rendered.String()))-1]
				if !isWhitespace(last) {
					rendered.WriteByte(' ')
				}
			}
		}
		for i, link := range links {
			if i > 0 {
				rendered.WriteByte(' ')
			}
			rendered.WriteString(link)
		}
		if isMarker {
			trimmedRight := strings.TrimRight(cited, " \t\n\r")
			rendered.WriteString(cited[len(trimmedRight):])
		}
		cursor = end
	}
	rendered.WriteString(text[cursor:])
	return rendered.String()
}

func isASCIIPunct(ch rune) bool {
	switch ch {
	case '!', '"', '#', '$', '%', '&', '\'', '(', ')', '*', '+', ',', '-', '.', '/', ':', ';', '<', '=', '>', '?', '@', '[', '\\', ']', '^', '_', '`', '{', '|', '}', '~':
		return true
	}
	return false
}

func isWhitespace(ch rune) bool {
	switch ch {
	case ' ', '\t', '\n', '\r', 0x0b, 0x0c:
		return true
	}
	return false
}
