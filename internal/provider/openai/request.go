package openai

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ChaosJohn/gcode/internal/types"
)

// Responses API request shaping (ported from request.rs build_responses_input).

// OpenAI encrypted-content replay limits.
const (
	// EncryptedContentProviderMaxChars is the provider hard limit.
	EncryptedContentProviderMaxChars = 10_485_760
	// EncryptedContentSafeMaxChars keeps JSON escaping below the hard limit.
	EncryptedContentSafeMaxChars = 9_500_000
)

// MissingOutputText is the synthetic output for a dangling call.
const MissingOutputText = "[Error] " + types.ToolOutputMissingText

// EncryptedContentIsSendable reports replay safety.
func EncryptedContentIsSendable(encryptedContent string) bool {
	return len(encryptedContent) <= EncryptedContentSafeMaxChars
}

// EncryptedContentFallbackSummary explains a dropped compaction payload.
func EncryptedContentFallbackSummary(encryptedContentLen int) string {
	return fmt.Sprintf(
		"OpenAI native compaction state was discarded because its encrypted payload was %d chars, above Jcode's safe replay limit of %d chars (provider hard limit: %d chars). Earlier compacted details may be unavailable; use the recent visible messages and session search/tools if exact prior details are needed.",
		encryptedContentLen, EncryptedContentSafeMaxChars, EncryptedContentProviderMaxChars)
}

// IsEncryptedContentTooLargeError detects the provider rejection.
func IsEncryptedContentTooLargeError(errText string) bool {
	lower := strings.ToLower(errText)
	return strings.Contains(lower, "encrypted_content") &&
		(strings.Contains(lower, "string_above_max_length") ||
			strings.Contains(lower, "string too long") ||
			strings.Contains(lower, "maximum length") ||
			strings.Contains(lower, "largestringparam"))
}

// RequestLogLevel mirrors the upstream logger levels.
type RequestLogLevel int

const (
	LogInfo RequestLogLevel = iota
	LogWarn
)

// RequestLogger receives shaping diagnostics.
type RequestLogger func(level RequestLogLevel, message string)

func nopLogger(RequestLogLevel, string) {}

// insertAdditionalTools adds Responses additional_tools items for
// ToolReference blocks right after the matching function_call_output, so the
// cached prefix stays untouched. References to unknown tools are dropped.
func insertAdditionalTools(input []any, messages []types.Message, tools []types.ToolDefinition) []any {
	deferredByCall := map[string][]types.ToolDefinition{}
	var callOrder []string
	for mi := range messages {
		for i := range messages[mi].Content {
			block := messages[mi].Content[i]
			if block.Type != types.BlockToolReference {
				continue
			}
			toolUseID, toolName := "", ""
			if block.ToolUseID != nil {
				toolUseID = *block.ToolUseID
			}
			if block.ToolName != nil {
				toolName = *block.ToolName
			}
			for ti := range tools {
				if tools[ti].Name == toolName && tools[ti].DeferLoading {
					if _, seen := deferredByCall[toolUseID]; !seen {
						callOrder = append(callOrder, toolUseID)
					}
					deferredByCall[toolUseID] = append(deferredByCall[toolUseID], tools[ti])
				}
			}
		}
	}
	for _, callID := range callOrder {
		defs := deferredByCall[callID]
		pos := -1
		for i, item := range input {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "function_call_output" && m["call_id"] == callID {
				pos = i
				break
			}
		}
		if pos < 0 {
			continue
		}
		toolItems := make([]any, 0, len(defs))
		for _, def := range defs {
			var decoded any
			if err := json.Unmarshal(BuildTool(def), &decoded); err == nil {
				toolItems = append(toolItems, decoded)
			}
		}
		additional := map[string]any{
			"type":  "additional_tools",
			"role":  "developer",
			"tools": toolItems,
		}
		input = append(input, nil)
		copy(input[pos+2:], input[pos+1:])
		input[pos+1] = additional
	}
	return input
}

// orphanToolOutputToUserMessage rewrites an unpaired output into a user
// message; nil when it carries nothing worth keeping.
func orphanToolOutputToUserMessage(item map[string]any, missingOutput string) map[string]any {
	outputValue, present := item["output"]
	if !present {
		return nil
	}
	var output string
	if text, ok := outputValue.(string); ok {
		output = strings.TrimSpace(text)
	} else {
		encoded, err := json.Marshal(outputValue)
		if err != nil {
			return nil
		}
		output = string(encoded)
	}
	if output == "" || output == missingOutput {
		return nil
	}
	callID := "unknown_call"
	if s, ok := item["call_id"].(string); ok && s != "" {
		callID = s
	}
	return map[string]any{
		"type": "message",
		"role": "user",
		"content": []any{map[string]any{
			"type": "input_text",
			"text": fmt.Sprintf("[Recovered orphaned tool output: %s]\n%s", callID, output),
		}},
	}
}

// DowngradeWebSearchCalls replaces stored hosted-search web_search_call items
// with labelled text summaries, for requests that do not declare the hosted
// web_search tool.
func DowngradeWebSearchCalls(input []any) {
	for i, itemAny := range input {
		item, ok := itemAny.(map[string]any)
		if !ok || item["type"] != "web_search_call" {
			continue
		}
		raw, err := json.Marshal(item)
		if err != nil {
			continue
		}
		text := types.ProviderNativeTextFallback(types.ProviderNativeOpenAI, raw, nil)
		if text == "" {
			text = "[web_search] (earlier provider-side search)"
		}
		input[i] = map[string]any{
			"type": "message",
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "output_text", "text": text,
			}},
		}
	}
}

// BuildResponsesInput converts a session transcript into Responses input items.
func BuildResponsesInput(messages []types.Message) []any {
	return buildResponsesInputWithLogger(messages, nopLogger)
}

func blockID(b types.ContentBlock) string {
	if b.ID == nil {
		return ""
	}
	return *b.ID
}

func blockString(b types.ContentBlock) string {
	if b.Text == nil {
		return ""
	}
	return *b.Text
}

func buildResponsesInputWithLogger(messages []types.Message, logger RequestLogger) []any {
	missingOutput := MissingOutputText

	toolResultLastPos := map[string]int{}
	for idx := range messages {
		if messages[idx].Role != types.RoleUser {
			continue
		}
		for i := range messages[idx].Content {
			block := messages[idx].Content[i]
			if toolUseID, _, _, ok := block.AsToolResult(); ok {
				toolResultLastPos[toolUseID] = idx
			}
		}
	}

	var items []any
	openCalls := map[string]struct{}{}
	pendingOutputs := map[string]string{}
	usedOutputs := map[string]struct{}{}
	skippedResults, delayedResults, injectedMissing := 0, 0, 0

	pushParts := func(role string, parts *[]any) {
		if len(*parts) == 0 {
			return
		}
		items = append(items, map[string]any{
			"type": "message", "role": role, "content": *parts,
		})
		*parts = nil
	}

	for idx := range messages {
		msg := messages[idx]
		switch msg.Role {
		case types.RoleUser:
			var parts []any
			for i := range msg.Content {
				block := msg.Content[i]
				switch block.Type {
				case types.BlockImage:
					mediaType, data := "", ""
					if block.MediaType != nil {
						mediaType = *block.MediaType
					}
					if block.Data != nil {
						data = *block.Data
					}
					parts = append(parts, map[string]any{
						"type":      "input_image",
						"image_url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
					})
				case types.BlockText:
					parts = append(parts, map[string]any{
						"type": "input_text", "text": blockString(block),
					})
				case types.BlockOpenAICompaction:
					pushParts("user", &parts)
					encrypted := ""
					if block.EncryptedContent != nil {
						encrypted = *block.EncryptedContent
					}
					if EncryptedContentIsSendable(encrypted) {
						items = append(items, map[string]any{
							"type": "compaction", "encrypted_content": encrypted,
						})
					} else {
						logger(LogWarn, fmt.Sprintf(
							"[openai] Dropping oversized native compaction payload before request build (%d chars > safe limit %d chars)",
							len(encrypted), EncryptedContentSafeMaxChars))
						items = append(items, map[string]any{
							"type": "message", "role": "user",
							"content": []any{map[string]any{
								"type": "input_text",
								"text": EncryptedContentFallbackSummary(len(encrypted)),
							}},
						})
					}
				case types.BlockToolResult:
					pushParts("user", &parts)
					toolUseID, content, isErr, ok := block.AsToolResult()
					if !ok {
						continue
					}
					if _, used := usedOutputs[toolUseID]; used {
						skippedResults++
						continue
					}
					output := content
					if isErr {
						output = "[Error] " + content
					}
					if _, open := openCalls[toolUseID]; open {
						items = append(items, map[string]any{
							"type":    "function_call_output",
							"call_id": types.SanitizeToolID(toolUseID),
							"output":  output,
						})
						delete(openCalls, toolUseID)
						usedOutputs[toolUseID] = struct{}{}
					} else if _, pending := pendingOutputs[toolUseID]; pending {
						skippedResults++
					} else {
						pendingOutputs[toolUseID] = output
						delayedResults++
					}
				}
			}
			pushParts("user", &parts)
		case types.RoleAssistant:
			for i := range msg.Content {
				block := msg.Content[i]
				switch block.Type {
				case types.BlockProviderNative:
					item := block.Item
					provider := ""
					if block.Provider != nil {
						provider = *block.Provider
					}
					if provider == types.ProviderNativeOpenAI &&
						types.JSONStr(item, "type") == "web_search_call" {
						var decoded any
						if json.Unmarshal(item, &decoded) == nil {
							items = append(items, decoded)
						}
						continue
					}
					if text := types.ProviderNativeTextFallback(provider, item, nil); text != "" {
						items = append(items, assistantOutputText(text))
					}
				case types.BlockText:
					items = append(items, assistantOutputText(blockString(block)))
				case types.BlockOpenAIReasoning:
					id := ""
					if block.ID != nil {
						id = *block.ID
					}
					summaryItems := make([]any, 0, len(block.Summary))
					for _, text := range block.Summary {
						summaryItems = append(summaryItems, map[string]any{
							"type": "summary_text", "text": text,
						})
					}
					item := map[string]any{
						"type": "reasoning", "id": id, "summary": summaryItems,
					}
					if block.EncryptedContent != nil {
						item["encrypted_content"] = *block.EncryptedContent
					}
					items = append(items, item)
				case types.BlockToolUse:
					id, name, input, _ := block.AsToolUse()
					arguments := "{}"
					var probe any
					if json.Unmarshal(input, &probe) == nil {
						if _, isObj := probe.(map[string]any); isObj {
							arguments = string(input)
						}
					}
					items = append(items, map[string]any{
						"type":      "function_call",
						"name":      name,
						"arguments": arguments,
						"call_id":   types.SanitizeToolID(id),
					})

					if output, pending := pendingOutputs[id]; pending {
						delete(pendingOutputs, id)
						items = append(items, map[string]any{
							"type":    "function_call_output",
							"call_id": types.SanitizeToolID(id),
							"output":  output,
						})
						usedOutputs[id] = struct{}{}
					} else {
						futurePos, known := toolResultLastPos[id]
						if known && futurePos > idx {
							openCalls[id] = struct{}{}
						} else {
							injectedMissing++
							items = append(items, map[string]any{
								"type":    "function_call_output",
								"call_id": types.SanitizeToolID(id),
								"output":  missingOutput,
							})
							usedOutputs[id] = struct{}{}
						}
					}
				}
			}
		}
	}

	// Drain calls whose output arrives later in the transcript.
	for callID := range openCalls {
		if _, used := usedOutputs[callID]; used {
			continue
		}
		if output, pending := pendingOutputs[callID]; pending {
			delete(pendingOutputs, callID)
			items = append(items, map[string]any{
				"type": "function_call_output", "call_id": types.SanitizeToolID(callID), "output": output,
			})
		} else {
			injectedMissing++
			items = append(items, map[string]any{
				"type": "function_call_output", "call_id": types.SanitizeToolID(callID), "output": missingOutput,
			})
		}
	}

	if delayedResults > 0 {
		logger(LogInfo, fmt.Sprintf("[openai] Delayed %d tool output(s) to preserve call ordering", delayedResults))
	}

	// Leftover pending outputs: rewrite as user messages (orphans).
	rewrittenPendingOrphans := 0
	if len(pendingOutputs) > 0 {
		callIDs := make([]string, 0, len(pendingOutputs))
		for callID := range pendingOutputs {
			callIDs = append(callIDs, callID)
		}
		sort.Strings(callIDs)
		for _, callID := range callIDs {
			output := pendingOutputs[callID]
			orphan := map[string]any{
				"type": "function_call_output", "call_id": types.SanitizeToolID(callID), "output": output,
			}
			if messageItem := orphanToolOutputToUserMessage(orphan, missingOutput); messageItem != nil {
				items = append(items, messageItem)
				rewrittenPendingOrphans++
			} else {
				skippedResults++
			}
		}
	}
	if injectedMissing > 0 {
		logger(LogInfo, fmt.Sprintf("[openai] Injected %d synthetic tool output(s) to prevent API error", injectedMissing))
	}
	if rewrittenPendingOrphans > 0 {
		logger(LogInfo, fmt.Sprintf("[openai] Rewrote %d pending orphaned tool output(s) as user messages", rewrittenPendingOrphans))
	}
	if skippedResults > 0 {
		logger(LogInfo, fmt.Sprintf("[openai] Filtered %d orphaned tool result(s) to prevent API error", skippedResults))
	}

	// Safety pass: every call is followed by an output.
	outputIDs := map[string]struct{}{}
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok || item["type"] != "function_call_output" {
			continue
		}
		if callID, ok := item["call_id"].(string); ok {
			outputIDs[callID] = struct{}{}
		}
	}
	var normalized []any
	extraInjected := 0
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			normalized = append(normalized, itemAny)
			continue
		}
		kind, _ := item["type"].(string)
		callID, _ := item["call_id"].(string)
		normalized = append(normalized, item)
		isCall := kind == "function_call" || kind == "custom_tool_call"
		if isCall && callID != "" {
			if _, has := outputIDs[callID]; !has {
				extraInjected++
				outputIDs[callID] = struct{}{}
				normalized = append(normalized, map[string]any{
					"type": "function_call_output", "call_id": callID, "output": missingOutput,
				})
			}
		}
	}
	if extraInjected > 0 {
		logger(LogInfo, fmt.Sprintf("[openai] Safety-injected %d missing tool output(s) at request build", extraInjected))
	}

	// Reorder: each call carries its output directly after it. The first real
	// (non-missing) output per call wins.
	outputMap := map[string]map[string]any{}
	for _, itemAny := range normalized {
		item, ok := itemAny.(map[string]any)
		if !ok || item["type"] != "function_call_output" {
			continue
		}
		callID, ok := item["call_id"].(string)
		if !ok {
			continue
		}
		outputStr, _ := item["output"].(string)
		isMissing := outputStr == missingOutput
		existing, seen := outputMap[callID]
		if !seen {
			outputMap[callID] = item
		} else {
			existingOutput, _ := existing["output"].(string)
			if existingOutput == missingOutput && !isMissing {
				outputMap[callID] = item
			}
		}
	}

	var ordered []any
	usedOutputs2 := map[string]struct{}{}
	injectedOrdered, droppedDuplicates, rewrittenOrphans, skippedEmptyOrphans := 0, 0, 0, 0

	for _, itemAny := range normalized {
		item, ok := itemAny.(map[string]any)
		if !ok {
			ordered = append(ordered, itemAny)
			continue
		}
		kind, _ := item["type"].(string)
		callID, _ := item["call_id"].(string)
		isCall := kind == "function_call" || kind == "custom_tool_call"
		if isCall {
			ordered = append(ordered, item)
			if callID != "" {
				if outputItem, has := outputMap[callID]; has {
					ordered = append(ordered, outputItem)
					usedOutputs2[callID] = struct{}{}
				} else {
					injectedOrdered++
					ordered = append(ordered, map[string]any{
						"type": "function_call_output", "call_id": callID, "output": missingOutput,
					})
					usedOutputs2[callID] = struct{}{}
				}
			}
			continue
		}
		if kind == "function_call_output" {
			if _, used := usedOutputs2[callID]; used {
				droppedDuplicates++
				continue
			}
			if messageItem := orphanToolOutputToUserMessage(item, missingOutput); messageItem != nil {
				ordered = append(ordered, messageItem)
				rewrittenOrphans++
			} else {
				skippedEmptyOrphans++
			}
			continue
		}
		ordered = append(ordered, item)
	}

	if injectedOrdered > 0 {
		logger(LogInfo, fmt.Sprintf("[openai] Inserted %d tool output(s) to enforce call ordering", injectedOrdered))
	}
	if droppedDuplicates > 0 {
		logger(LogInfo, fmt.Sprintf("[openai] Dropped %d duplicate tool output(s) during re-ordering", droppedDuplicates))
	}
	if rewrittenOrphans > 0 {
		logger(LogInfo, fmt.Sprintf("[openai] Rewrote %d orphaned tool output(s) as user messages", rewrittenOrphans))
	}
	if skippedEmptyOrphans > 0 {
		logger(LogInfo, fmt.Sprintf("[openai] Skipped %d empty orphaned tool output(s) during re-ordering", skippedEmptyOrphans))
	}
	return ordered
}

func assistantOutputText(text string) map[string]any {
	return map[string]any{
		"type": "message", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
}
