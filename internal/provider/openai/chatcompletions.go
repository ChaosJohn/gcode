// Chat-completions wire support for OpenAI-compatible endpoints (DeepSeek,
// Moonshot, vLLM, gateways): /v1/chat/completions SSE streaming. Upstream
// jcode speaks only the Responses API for compatible routes; this wire is a
// gcode-side extension for endpoints that do not implement /responses, so it
// is gated behind config (`api = "chat-completions"`) rather than auto-
// detected.
package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ChaosJohn/gcode/internal/types"
)

// OpenAI wire modes (shared with the adapter).
const (
	// OpenAIWireResponses is the native Responses API.
	OpenAIWireResponses = "responses"
	// OpenAIWireChatCompletions is the /chat/completions compatible wire.
	OpenAIWireChatCompletions = "chat-completions"
)

// ChatCompletionsOptions is one chat-completions stream call.
type ChatCompletionsOptions struct {
	Model       string
	BaseURL     string // e.g. https://api.deepseek.com/v1
	APIKey      string
	System      string
	Messages    []types.Message
	Tools       []types.ToolDefinition
	HTTPClient  *http.Client
	Emit        func(types.StreamEvent) error
	MaxTokens   *uint32
	Temperature *float64
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Index    *int   `json:"index,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// ChatMessages converts the transcript into chat-completions messages,
// including assistant tool_calls and role:tool results (the paired
// tool_use/tool_result blocks collapse into their wire equivalents).
func ChatMessages(system string, messages []types.Message) []chatMessage {
	out := []chatMessage{}
	if strings.TrimSpace(system) != "" {
		out = append(out, chatMessage{Role: "system", Content: system})
	}
	pendingCalls := map[string]bool{}
	// First pass: collect assistant tool calls so tool results can pair.
	assistantCalls := []chatToolCall{}
	flushAssistant := func() {
		if len(assistantCalls) > 0 {
			out = append(out, chatMessage{Role: "assistant", ToolCalls: assistantCalls})
			for _, call := range assistantCalls {
				pendingCalls[call.ID] = true
			}
			assistantCalls = nil
		}
	}
	for _, message := range messages {
		for _, block := range message.Content {
			switch {
			case block.Type == types.BlockText && block.Text != nil && *block.Text != "":
				flushAssistant()
				out = append(out, chatMessage{Role: string(message.Role), Content: *block.Text})
			case block.Type == types.BlockToolUse && block.Name != nil && block.ID != nil:
				call := chatToolCall{ID: *block.ID, Type: "function"}
				call.Function.Name = *block.Name
				call.Function.Arguments = string(block.Input)
				assistantCalls = append(assistantCalls, call)
			case block.Type == types.BlockToolResult && block.ToolUseID != nil && block.Content != nil:
				flushAssistant()
				out = append(out, chatMessage{Role: "tool", ToolCallID: *block.ToolUseID,
					Content: *block.Content})
				delete(pendingCalls, *block.ToolUseID)
			}
		}
	}
	flushAssistant()
	return out
}

func chatTools(tools []types.ToolDefinition) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		schema := json.RawMessage(tool.InputSchema)
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  schema,
			},
		})
	}
	return out
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   *string        `json:"content"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     uint64 `json:"prompt_tokens"`
		CompletionTokens uint64 `json:"completion_tokens"`
	} `json:"usage"`
}

// ChatCompletionsStream runs one streamed /chat/completions request.
func ChatCompletionsStream(options ChatCompletionsOptions) error {
	payload := map[string]any{
		"model":          options.Model,
		"messages":       ChatMessages(options.System, options.Messages),
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if toolDefs := chatTools(options.Tools); toolDefs != nil {
		payload["tools"] = toolDefs
	}
	if options.MaxTokens != nil {
		payload["max_tokens"] = *options.MaxTokens
	}
	if options.Temperature != nil {
		payload["temperature"] = *options.Temperature
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(options.BaseURL, "/") + "/chat/completions"
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+options.APIKey)
	request.Header.Set("Accept", "text/event-stream")
	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("Failed to send request to OpenAI-compatible API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("OpenAI-compatible API error: %s %s", response.Status,
			strings.TrimSpace(string(body)))
	}

	// Chat-completions has no explicit per-call end: a call closes when the
	// next one starts or the message finishes. Track the open call.
	currentToolID := ""
	endCurrentTool := func() {
		if currentToolID != "" {
			_ = options.Emit(types.ToolUseEndFor{ID: currentToolID})
			currentToolID = ""
		}
	}
	var usage *chatStreamChunk
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil && (chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
			usage = &chunk
		}
		for _, choice := range chunk.Choices {
			delta := choice.Delta
			if delta.Content != nil && *delta.Content != "" {
				if err := options.Emit(types.TextDelta{Text: *delta.Content}); err != nil {
					return err
				}
			}
			for _, call := range delta.ToolCalls {
				if call.ID != "" && call.Function.Name != "" {
					endCurrentTool()
					currentToolID = call.ID
					if err := options.Emit(types.ToolUseStart{ID: call.ID, Name: call.Function.Name}); err != nil {
						return err
					}
				}
				if call.Function.Arguments != "" {
					if call.ID != "" {
						if err := options.Emit(types.ToolInputDeltaFor{ID: call.ID,
							Delta: call.Function.Arguments}); err != nil {
							return err
						}
					} else if currentToolID != "" {
						if err := options.Emit(types.ToolInputDeltaFor{ID: currentToolID,
							Delta: call.Function.Arguments}); err != nil {
							return err
						}
					}
				}
			}
			if choice.FinishReason != nil {
				endCurrentTool()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("stream read error: %w", err)
	}
	endCurrentTool()
	if usage != nil {
		options.Emit(types.TokenUsageEvent{
			InputTokens:  &usage.Usage.PromptTokens,
			OutputTokens: &usage.Usage.CompletionTokens,
		})
	}
	return options.Emit(types.MessageEnd{})
}
