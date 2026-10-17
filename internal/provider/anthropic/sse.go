package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/ChaosJohn/gcode/internal/types"
)

// SSE decoding (ported from sse_types.rs + process_sse_event): one SSE event
// in, zero or more StreamEvents out.

// SSEEvent is one parsed server-sent event.
type SSEEvent struct {
	Event string
	Data  string
}

// ParseSSEEvent extracts the next complete event from the buffer, consuming
// it. Returns nil when no complete event is buffered yet.
func ParseSSEEvent(buffer *strings.Builder) *SSEEvent {
	data := buffer.String()
	for {
		idx := strings.Index(data, "\n\n")
		if idx < 0 {
			// Also accept \r\n\r\n.
			if idx = strings.Index(data, "\r\n\r\n"); idx < 0 {
				return nil
			}
			event := data[:idx]
			rest := data[idx+4:]
			buffer.Reset()
			buffer.WriteString(rest)
			return parseSSEBlock(event)
		}
		event := data[:idx]
		rest := data[idx+2:]
		buffer.Reset()
		buffer.WriteString(rest)
		if parsed := parseSSEBlock(event); parsed != nil {
			return parsed
		}
		// Comments / keepalives without data are skipped; keep scanning.
	}
}

func parseSSEBlock(block string) *SSEEvent {
	var eventType, data strings.Builder
	hasData := false
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "event:")))
		case strings.HasPrefix(line, "data:"):
			if hasData {
				data.WriteString("\n")
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			hasData = true
		}
	}
	if !hasData {
		return nil
	}
	return &SSEEvent{Event: eventType.String(), Data: data.String()}
}

// UsageInfo is the usage payload of message_start / message_delta.
type UsageInfo struct {
	InputTokens              *uint64 `json:"input_tokens"`
	OutputTokens             *uint64 `json:"output_tokens"`
	CacheReadInputTokens     *uint64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *uint64 `json:"cache_creation_input_tokens"`
	ServiceTier              *string `json:"service_tier"`
}

// SseStreamState accumulates state across events of one stream.
type SseStreamState struct {
	RequestedModelBase   string
	CurrentToolUse       bool
	CurrentThinkingBlock bool
	CurrentServerBlock   *ServerBlockAccumulator
	InputTokens          *uint64
	OutputTokens         *uint64
	CacheRead            *uint64
	CacheCreation        *uint64
	ServiceTier          *string
}

// ServerBlockAccumulator collects a streamed server-tool block verbatim.
type ServerBlockAccumulator struct {
	Type       string
	Header     map[string]any
	InputParts []string
}

// PushInput appends an input_json_delta fragment.
func (a *ServerBlockAccumulator) PushInput(fragment string) {
	a.InputParts = append(a.InputParts, fragment)
}

// Finish assembles the stored block item.
func (a *ServerBlockAccumulator) Finish() json.RawMessage {
	item := map[string]any{}
	for k, v := range a.Header {
		item[k] = v
	}
	if len(a.InputParts) > 0 {
		joined := strings.Join(a.InputParts, "")
		var parsed any
		if json.Unmarshal([]byte(joined), &parsed) == nil {
			item["input"] = parsed
		} else {
			item["input"] = joined
		}
	} else if _, ok := item["input"]; !ok {
		item["input"] = map[string]any{}
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

// ServerBlockStartOf classifies an unrecognized content_block_start.
func ServerBlockStartOf(data string) (*ServerBlockAccumulator, json.RawMessage, bool) {
	var probe struct {
		ContentBlock json.RawMessage `json:"content_block"`
	}
	if json.Unmarshal([]byte(data), &probe) != nil || len(probe.ContentBlock) == 0 {
		return nil, nil, false
	}
	blockType := typesJSONStr(probe.ContentBlock, "type")
	switch blockType {
	case "server_tool_use":
		acc := &ServerBlockAccumulator{Type: "server_tool_use", Header: map[string]any{}}
		var header struct {
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Item json.RawMessage `json:"-"`
		}
		if json.Unmarshal(probe.ContentBlock, &header) == nil {
			acc.Header["id"] = header.ID
			acc.Header["name"] = header.Name
			acc.Header["type"] = "server_tool_use"
		}
		return acc, nil, true
	case "web_search_tool_result":
		return nil, probe.ContentBlock, true
	default:
		return nil, nil, false
	}
}

func typesJSONStr(raw json.RawMessage, key string) string {
	var probe map[string]any
	if json.Unmarshal(raw, &probe) != nil {
		return ""
	}
	if s, ok := probe[key].(string); ok {
		return s
	}
	return ""
}

// ProcessSSEEvent maps one SSE event to zero or more StreamEvents.
// isOAuth controls the tool-name demapping. isRetryable, when non-nil, marks
// the returned error as retryable so the caller can restart the attempt.
func ProcessSSEEvent(event *SSEEvent, state *SseStreamState, isOAuth bool) []types.StreamEvent {
	var events []types.StreamEvent
	switch event.Event {
	case "message_start":
		var parsed struct {
			Message struct {
				Model string     `json:"model"`
				Usage *UsageInfo `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(event.Data), &parsed) == nil {
			if parsed.Message.Usage != nil {
				state.InputTokens = parsed.Message.Usage.InputTokens
				state.CacheRead = parsed.Message.Usage.CacheReadInputTokens
				state.CacheCreation = parsed.Message.Usage.CacheCreationInputTokens
				state.ServiceTier = parsed.Message.Usage.ServiceTier
			}
			if parsed.Message.Model != "" {
				events = append(events, types.UpstreamProviderEvent{Provider: "anthropic:" + parsed.Message.Model})
			}
		}
	case "content_block_start":
		var parsed struct {
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		}
		if json.Unmarshal([]byte(event.Data), &parsed) != nil {
			return events
		}
		switch parsed.ContentBlock.Type {
		case "text":
			// Nothing to emit until the first delta.
		case "thinking", "redacted_thinking":
			state.CurrentThinkingBlock = true
			events = append(events, types.ThinkingStart{})
		case "tool_use":
			name := parsed.ContentBlock.Name
			if isOAuth {
				name = MapToolNameFromOAuth(name)
			}
			state.CurrentToolUse = true
			events = append(events, types.ToolUseStart{ID: parsed.ContentBlock.ID, Name: name})
		default:
			if acc, complete, isServer := ServerBlockStartOf(event.Data); isServer {
				if acc != nil {
					state.CurrentServerBlock = acc
				} else if complete != nil {
					events = append(events, types.ProviderNativeEvent{
						Provider: types.ProviderNativeAnthropic, Item: complete,
					})
				}
			}
			// Other newer block types are ignored; the stream stays intact.
		}
	case "content_block_delta":
		var parsed struct {
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
			} `json:"delta"`
		}
		if json.Unmarshal([]byte(event.Data), &parsed) != nil {
			return events
		}
		switch parsed.Delta.Type {
		case "text_delta":
			events = append(events, types.TextDelta{Text: parsed.Delta.Text})
		case "input_json_delta":
			if state.CurrentServerBlock != nil {
				state.CurrentServerBlock.PushInput(parsed.Delta.PartialJSON)
				return events
			}
			if state.CurrentToolUse {
				// The accumulator mirrors upstream's ToolUseAccumulator.
			}
			events = append(events, types.ToolInputDelta{Delta: parsed.Delta.PartialJSON})
		case "thinking_delta":
			events = append(events, types.ThinkingDelta{Delta: parsed.Delta.Thinking})
		case "signature_delta":
			events = append(events, types.ThinkingSignatureDelta{Delta: parsed.Delta.Signature})
		}
	case "content_block_stop":
		if state.CurrentServerBlock != nil {
			block := state.CurrentServerBlock
			state.CurrentServerBlock = nil
			events = append(events, types.ProviderNativeEvent{
				Provider: types.ProviderNativeAnthropic, Item: block.Finish(),
			})
			return events
		}
		if state.CurrentToolUse {
			state.CurrentToolUse = false
			events = append(events, types.ToolUseEnd{})
		} else if state.CurrentThinkingBlock {
			state.CurrentThinkingBlock = false
			events = append(events, types.ThinkingEnd{})
		}
	case "message_delta":
		var parsed struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage *UsageInfo `json:"usage"`
		}
		if json.Unmarshal([]byte(event.Data), &parsed) == nil {
			if parsed.Usage != nil {
				state.OutputTokens = parsed.Usage.OutputTokens
			}
			if parsed.Delta.StopReason != "" {
				events = append(events, types.MessageEnd{StopReason: &parsed.Delta.StopReason})
			}
		}
	case "message_stop":
		// Final stop; MessageEnd already went out via message_delta.
	case "ping":
		// Surface keepalives so stall guards see traffic (issue #451).
		events = append(events, types.ConnectionPhaseEvent{Phase: types.PhaseStreaming})
	case "error":
		events = append(events, types.ErrorEvent{Message: event.Data})
	default:
		// Unknown event type: ignore.
	}
	return events
}

// FinalTokenUsage builds the terminal TokenUsage event, if any usage arrived.
func (s *SseStreamState) FinalTokenUsage() *types.TokenUsageEvent {
	if s.InputTokens == nil && s.OutputTokens == nil {
		return nil
	}
	// service_tier is tracked separately (ServiceTierEvent), not part of
	// the usage variant.
	_ = s.ServiceTier
	return &types.TokenUsageEvent{
		InputTokens:              s.InputTokens,
		OutputTokens:             s.OutputTokens,
		CacheReadInputTokens:     s.CacheRead,
		CacheCreationInputTokens: s.CacheCreation,
	}
}

func u64FromUint(n uint64) *uint64 { return &n }
