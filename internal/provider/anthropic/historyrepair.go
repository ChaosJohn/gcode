package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/ChaosJohn/gcode/internal/types"
)

// History repair pipeline (ported from history_repair.rs): makes any persisted
// conversation sendable to the Anthropic API, which rejects unpaired or
// duplicated tool calls/results with a 400.

// hoistLateToolResults moves tool_results that are not in the message right
// after their call up to directly follow it (turn-aware).
func hoistLateToolResults(messages []types.Message) []types.Message {
	callAt := map[string]int{}
	for mi := range messages {
		if messages[mi].Role != types.RoleAssistant {
			continue
		}
		for i := range messages[mi].Content {
			if id, _, _, ok := messages[mi].Content[i].AsToolUse(); ok {
				callAt[id] = mi
			}
		}
	}

	// Turn index: consecutive same-role messages merge into one turn later.
	turn := make([]int, len(messages))
	t := 0
	prev := -1 // -1 unset, 0 assistant, 1 user
	for mi := range messages {
		isUser := 0
		if messages[mi].Role == types.RoleUser {
			isUser = 1
		}
		if prev >= 0 && prev != isUser {
			t++
		}
		prev = isUser
		turn[mi] = t
	}

	type key struct{ mi, bi int }
	late := map[key]struct{}{}
	moved := map[int][]types.ContentBlock{}
	for mi := range messages {
		for bi := range messages[mi].Content {
			toolUseID, _, _, ok := messages[mi].Content[bi].AsToolResult()
			if !ok {
				continue
			}
			call, paired := callAt[toolUseID]
			if !paired {
				continue
			}
			if turn[mi] == turn[call]+1 {
				continue // in place
			}
			anchor := call
			for i := call; i < len(messages) && turn[i] == turn[call]; i++ {
				anchor = i
			}
			indices := append([]int{bi}, attachedToResult(messages[mi].Content, bi)...)
			for _, j := range indices {
				late[key{mi, j}] = struct{}{}
				moved[anchor] = append(moved[anchor], messages[mi].Content[j])
			}
		}
	}
	if len(late) == 0 {
		return messages
	}
	out := make([]types.Message, 0, len(messages)+len(moved))
	for mi := range messages {
		var content []types.ContentBlock
		for bi := range messages[mi].Content {
			if _, skip := late[key{mi, bi}]; skip {
				continue
			}
			content = append(content, messages[mi].Content[bi])
		}
		if len(content) > 0 {
			out = append(out, types.Message{
				Role: messages[mi].Role, Content: content,
				Timestamp: messages[mi].Timestamp, ToolDurationMs: messages[mi].ToolDurationMs,
			})
		}
		if results, ok := moved[mi]; ok {
			out = append(out, types.Message{Role: types.RoleUser, Content: results, Timestamp: messages[mi].Timestamp})
		}
	}
	return out
}

const imageLabelPrefix = "[Attached image associated with the preceding tool result:"

// attachedToResult returns indices of blocks belonging to the tool_result at
// result_idx: its images, their labels, and its tool references.
func attachedToResult(blocks []types.ContentBlock, resultIdx int) []int {
	toolUseID, _, _, ok := blocks[resultIdx].AsToolResult()
	if !ok {
		return nil
	}
	isOwnReference := func(block types.ContentBlock) bool {
		if block.Type != types.BlockToolReference {
			return false
		}
		return block.ToolUseID != nil && *block.ToolUseID == toolUseID
	}
	var attached []int
	afterImage := false
	end := resultIdx + 1
	for end < len(blocks) {
		block := blocks[end]
		belongs := false
		switch block.Type {
		case types.BlockImage:
			belongs = true
		case types.BlockText:
			text := ""
			if block.Text != nil {
				text = *block.Text
			}
			belongs = afterImage && strings.HasPrefix(text, imageLabelPrefix)
		default:
			belongs = isOwnReference(block)
		}
		if !belongs {
			break
		}
		afterImage = block.Type == types.BlockImage
		attached = append(attached, end)
		end++
	}
	for i := end; i < len(blocks); i++ {
		if isOwnReference(blocks[i]) {
			attached = append(attached, i)
		}
	}
	return attached
}

// repairDanglingAnthropicServerTools drops orphaned server-tool blocks: an
// interrupted turn can persist server_tool_use without its result (or vice
// versa), which the API rejects on replay. A final assistant turn ending on an
// unmatched server_tool_use is preserved (pause_turn resume shape).
func repairDanglingAnthropicServerTools(messages []types.Message) []types.Message {
	starts := map[string]struct{}{}
	results := map[string]struct{}{}
	for mi := range messages {
		for i := range messages[mi].Content {
			block := messages[mi].Content[i]
			if block.Type != types.BlockProviderNative {
				continue
			}
			provider := ""
			if block.Provider != nil {
				provider = *block.Provider
			}
			if provider != types.ProviderNativeAnthropic {
				continue
			}
			switch types.JSONStr(block.Item, "type") {
			case "server_tool_use":
				if id := types.JSONStr(block.Item, "id"); id != "" {
					starts[id] = struct{}{}
				}
			case "web_search_tool_result":
				if id := types.JSONStr(block.Item, "tool_use_id"); id != "" {
					results[id] = struct{}{}
				}
			}
		}
	}

	finalPausedID := ""
	if n := len(messages); n > 0 && messages[n-1].Role == types.RoleAssistant {
		msg := messages[n-1]
		if last := len(msg.Content) - 1; last >= 0 && msg.Content[last].Type == types.BlockProviderNative {
			block := msg.Content[last]
			provider := ""
			if block.Provider != nil {
				provider = *block.Provider
			}
			if provider == types.ProviderNativeAnthropic && types.JSONStr(block.Item, "type") == "server_tool_use" {
				finalPausedID = types.JSONStr(block.Item, "id")
			}
		}
	}

	repaired := make([]types.Message, 0, len(messages))
	for mi := range messages {
		var content []types.ContentBlock
		for i := range messages[mi].Content {
			block := messages[mi].Content[i]
			keep := true
			if block.Type == types.BlockProviderNative {
				provider := ""
				if block.Provider != nil {
					provider = *block.Provider
				}
				if provider == types.ProviderNativeAnthropic {
					switch types.JSONStr(block.Item, "type") {
					case "server_tool_use":
						id := types.JSONStr(block.Item, "id")
						_, hasResult := results[id]
						if id == "" || (!hasResult && id != finalPausedID) {
							keep = false
						}
					case "web_search_tool_result":
						id := types.JSONStr(block.Item, "tool_use_id")
						_, hasStart := starts[id]
						if id == "" || !hasStart {
							keep = false
						}
					}
				}
			}
			if keep {
				content = append(content, block)
			}
		}
		if len(content) > 0 {
			repaired = append(repaired, types.Message{
				Role: messages[mi].Role, Content: content,
				Timestamp: messages[mi].Timestamp, ToolDurationMs: messages[mi].ToolDurationMs,
			})
		}
	}
	return repaired
}

// dedupeToolUses keeps only the first tool_use block per id.
func dedupeToolUses(messages []types.Message) []types.Message {
	seen := map[string]struct{}{}
	duplicate := false
	for mi := range messages {
		for i := range messages[mi].Content {
			if id, _, _, ok := messages[mi].Content[i].AsToolUse(); ok {
				if _, dup := seen[id]; dup {
					duplicate = true
				} else {
					seen[id] = struct{}{}
				}
			}
		}
	}
	if !duplicate {
		return messages
	}
	kept := map[string]struct{}{}
	out := make([]types.Message, 0, len(messages))
	for mi := range messages {
		msg := messages[mi]
		var content []types.ContentBlock
		for i := range msg.Content {
			block := msg.Content[i]
			if id, _, _, ok := block.AsToolUse(); ok {
				if _, dup := kept[id]; dup {
					continue
				}
				kept[id] = struct{}{}
			}
			content = append(content, block)
		}
		msg.Content = content
		out = append(out, msg)
	}
	return out
}

// isPlaceholderToolResult reports synthetic placeholders injected by repairs.
func isPlaceholderToolResult(content string, isError bool) bool {
	return isError &&
		(strings.Contains(content, types.ToolOutputMissingText) ||
			strings.Contains(content, InterruptedToolResultText))
}

// dedupeToolResults keeps one tool_result per id, preferring real output over
// a synthetic placeholder.
func dedupeToolResults(messages []types.Message) []types.Message {
	type pos struct{ mi, bi int }
	winner := map[string]pos{}
	winnerIsReal := map[string]bool{}
	duplicate := false
	for mi := range messages {
		for bi := range messages[mi].Content {
			toolUseID, content, isErr, ok := messages[mi].Content[bi].AsToolResult()
			if !ok {
				continue
			}
			real := !isPlaceholderToolResult(content, isErr)
			if _, seen := winnerIsReal[toolUseID]; !seen {
				winner[toolUseID] = pos{mi, bi}
				winnerIsReal[toolUseID] = real
			} else if !winnerIsReal[toolUseID] && real {
				winner[toolUseID] = pos{mi, bi}
				winnerIsReal[toolUseID] = true
				duplicate = true
			} else {
				duplicate = true
			}
		}
	}
	if !duplicate {
		return messages
	}
	out := make([]types.Message, 0, len(messages))
	for mi := range messages {
		msg := messages[mi]
		var content []types.ContentBlock
		for bi := range msg.Content {
			block := msg.Content[bi]
			if toolUseID, _, _, ok := block.AsToolResult(); ok {
				w, ok := winner[toolUseID]
				if !ok || w != (pos{mi, bi}) {
					continue
				}
			}
			content = append(content, block)
		}
		msg.Content = content
		out = append(out, msg)
	}
	return out
}

// downgradeNativeReplay replaces every replayed server tool block with its
// plain-text summary; used after an invalid-native-replay 400. Reports change.
func downgradeNativeReplay(messages []ApiMessage) bool {
	changed := false
	for mi := range messages {
		callInputs := map[string]json.RawMessage{}
		var out []ContentBlock
		for _, block := range messages[mi].Content {
			raw, ok := block.(RawContent)
			if !ok {
				out = append(out, block)
				continue
			}
			changed = true
			display := types.ProviderNativeDisplayOf(types.ProviderNativeAnthropic, raw.Item)
			if display != nil && display.Output == nil && len(display.Input) > 0 {
				callInputs[display.ID] = display.Input
			}
			var callInput json.RawMessage
			if display != nil {
				callInput = callInputs[display.ID]
			}
			if text := types.ProviderNativeTextFallback(types.ProviderNativeAnthropic, raw.Item, callInput); text != "" {
				out = append(out, TextContent{Text: text})
			}
		}
		if len(out) == 0 {
			out = append(out, TextContent{Text: "[earlier provider-side search]"})
		}
		messages[mi].Content = out
	}
	return changed
}

// IsInvalidNativeReplayError detects Anthropic rejecting replayed server tool
// payloads (e.g. after an account switch).
func IsInvalidNativeReplayError(errText string) bool {
	lower := strings.ToLower(errText)
	return strings.Contains(lower, "encrypted_content") &&
		(strings.Contains(lower, "invalid") || strings.Contains(lower, "search_result"))
}
