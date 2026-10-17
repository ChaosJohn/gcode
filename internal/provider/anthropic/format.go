package anthropic

import (
	"encoding/json"
	"sort"

	"github.com/ChaosJohn/gcode/internal/schema/dialect"
	"github.com/ChaosJohn/gcode/internal/types"
)

// InputSchema normalizes a tool schema for Anthropic's input_schema: the API
// accepts draft 2020-12 inside properties but rejects top-level combiners and
// non-object schemas (ported from anthropic_input_schema).
func InputSchema(raw json.RawMessage) json.RawMessage {
	return dialect.NormalizeSchemaJSON(raw, &dialect.Anthropic)
}

// FormatTools converts tool definitions to Anthropic API format: schemas are
// normalized for the Anthropic dialect, OAuth renames a few tools to
// Claude-Code builtin names, the last eager tool carries the prompt-cache
// breakpoint, deferred tools move to the end uncached.
func FormatTools(tools []types.ToolDefinition, isOAuth bool, cacheTTL1h bool) []ApiTool {
	out := make([]ApiTool, 0, len(tools))
	for _, tool := range tools {
		name := tool.Name
		if isOAuth {
			name = MapToolNameForOAuth(name)
		}
		out = append(out, ApiTool{
			Name:         name,
			Description:  tool.Description,
			InputSchema:  InputSchema(tool.InputSchema),
			DeferLoading: tool.DeferLoading,
		})
	}
	return finishToolList(out, cacheTTL1h)
}

// finishToolList orders eager tools before deferred ones and puts the cache
// breakpoint on the last eager tool. A list of only deferred tools is sent
// eagerly instead (the API requires at least one non-deferred tool).
func finishToolList(tools []ApiTool, cacheTTL1h bool) []ApiTool {
	var out, deferred []ApiTool
	for _, tool := range tools {
		if tool.DeferLoading {
			deferred = append(deferred, tool)
		} else {
			out = append(out, tool)
		}
	}
	if len(out) == 0 {
		for i := range deferred {
			deferred[i].DeferLoading = false
		}
		out, deferred = deferred, nil
	}
	if len(out) > 0 {
		cc := EphemeralFor(cacheTTL1h)
		out[len(out)-1].CacheControl = &cc
	}
	return append(out, deferred...)
}

// RequestTools builds the request tools array: eager client tools, then server
// tools, then deferred client tools. Server tools sit behind the tool-cache
// breakpoint so loading deferred tools never shifts them.
func RequestTools(custom []ApiTool, server []json.RawMessage) []ApiToolParam {
	var eager, deferred []ApiTool
	for _, tool := range custom {
		if tool.DeferLoading {
			deferred = append(deferred, tool)
		} else {
			eager = append(eager, tool)
		}
	}
	out := make([]ApiToolParam, 0, len(eager)+len(server)+len(deferred))
	for i := range eager {
		out = append(out, ApiToolParam{Custom: &eager[i]})
	}
	for _, s := range server {
		out = append(out, ApiToolParam{Server: s})
	}
	for i := range deferred {
		out = append(out, ApiToolParam{Custom: &deferred[i]})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// BuildSystemParam builds the system param for a single static prompt.
func BuildSystemParam(system string, isOAuth, cacheTTL1h bool) ApiSystem {
	return BuildSystemParamSplit(system, "", isOAuth, cacheTTL1h)
}

// BuildSystemParamSplit builds the split static/dynamic system param. OAuth
// prepends the billing header and Claude-Code identity blocks; the static part
// is cached, the dynamic part is not.
func BuildSystemParamSplit(staticPart, dynamicPart string, isOAuth, cacheTTL1h bool) ApiSystem {
	if isOAuth {
		blocks := []ApiSystemBlock{
			{BlockType: "text", Text: "x-anthropic-billing-header: " + OAuthBillingHeader},
			{BlockType: "text", Text: ClaudeCodeIdentity},
		}
		if staticPart != "" {
			cc := EphemeralFor(cacheTTL1h)
			blocks = append(blocks, ApiSystemBlock{BlockType: "text", Text: staticPart, CacheControl: &cc})
		}
		if dynamicPart != "" {
			blocks = append(blocks, ApiSystemBlock{BlockType: "text", Text: dynamicPart})
		}
		return blocks
	}
	var blocks []ApiSystemBlock
	if staticPart != "" {
		cc := EphemeralFor(cacheTTL1h)
		blocks = append(blocks, ApiSystemBlock{BlockType: "text", Text: staticPart, CacheControl: &cc})
	}
	if dynamicPart != "" {
		blocks = append(blocks, ApiSystemBlock{BlockType: "text", Text: dynamicPart})
	}
	return blocks
}

// AddMessageCacheBreakpoint anchors a sliding two-marker window on the two
// most recent assistant messages (newest = WRITE, older = READ): budget is
// system (1) + tools (1) + messages (up to 2) = 4, within Anthropic's limit.
func AddMessageCacheBreakpoint(messages []ApiMessage, cacheTTL1h bool) {
	if len(messages) < 3 {
		return
	}
	var assistantIndices []int
	for i := len(messages) - 1; i >= 0 && len(assistantIndices) < 2; i-- {
		if messages[i].Role == "assistant" {
			assistantIndices = append(assistantIndices, i)
		}
	}
	if len(assistantIndices) == 0 {
		return
	}
	for _, idx := range assistantIndices {
		for i := len(messages[idx].Content) - 1; i >= 0; i-- {
			switch block := messages[idx].Content[i].(type) {
			case TextContent:
				cc := EphemeralFor(cacheTTL1h)
				block.CacheControl = &cc
				messages[idx].Content[i] = block
			case ToolUseContent:
				cc := EphemeralFor(cacheTTL1h)
				block.CacheControl = &cc
				messages[idx].Content[i] = block
			default:
				continue
			}
			break
		}
	}
}

// FormatMessagesWithIdentity adds conversation-cache breakpoints.
func FormatMessagesWithIdentity(messages []ApiMessage, cacheTTL1h bool) []ApiMessage {
	AddMessageCacheBreakpoint(messages, cacheTTL1h)
	return messages
}

// FormatMessagesWithTools runs the full history-shaping pipeline.
func FormatMessagesWithTools(messages []types.Message, isOAuth bool, apiTools []ApiTool) []ApiMessage {
	return FormatMessagesWithNative(messages, isOAuth, apiTools, false)
}

// FormatMessagesWithNative additionally controls provider-native block replay
// (server_tool_use / web_search_tool_result sent verbatim when the matching
// server tool is attached, downgraded to text summaries otherwise).
func FormatMessagesWithNative(messages []types.Message, isOAuth bool, apiTools []ApiTool, nativeReplay bool) []ApiMessage {
	available := map[string]struct{}{}
	for _, tool := range apiTools {
		available[tool.Name] = struct{}{}
	}
	repaired := repairDanglingAnthropicServerTools(messages)
	repaired = dedupeToolUses(repaired)
	repaired = dedupeToolResults(repaired)
	repaired = hoistLateToolResults(repaired)

	// Dangling tool_uses (no matching tool_result anywhere) get a synthetic
	// error result injected directly after their assistant message.
	dangling := map[string]struct{}{}
	answered := map[string]struct{}{}
	for _, msg := range repaired {
		for i := range msg.Content {
			block := msg.Content[i]
			if id, _, _, ok := block.AsToolUse(); ok {
				dangling[id] = struct{}{}
			} else if toolUseID, _, _, ok := block.AsToolResult(); ok {
				answered[toolUseID] = struct{}{}
			}
		}
	}
	for id := range answered {
		delete(dangling, id)
	}

	var result []ApiMessage
	for _, msg := range repaired {
		role := "assistant"
		if msg.Role == types.RoleUser {
			role = "user"
		}
		content := formatContentBlocksWithNative(msg.Content, isOAuth, nativeReplay)
		applyToolReferences(&content, msg.Content, isOAuth, available)

		if len(content) > 0 {
			result = append(result, ApiMessage{Role: role, Content: content})
		}

		if msg.Role == types.RoleAssistant {
			var synthetic []ContentBlock
			for i := range msg.Content {
				if id, _, _, ok := msg.Content[i].AsToolUse(); ok {
					if _, isDangling := dangling[id]; isDangling {
						synthetic = append(synthetic, ToolResultContentBlockMsg{
							ToolUseID: types.SanitizeToolID(id),
							Content:   ToolResultContent{Text: strPtr(InterruptedToolResultText)},
							IsError:   true,
						})
					}
				}
			}
			if len(synthetic) > 0 {
				result = append(result, ApiMessage{Role: "user", Content: synthetic})
			}
		}
	}

	// Third pass: merge consecutive same-role messages (the API requires
	// strictly alternating roles).
	merged := make([]ApiMessage, 0, len(result))
	for _, msg := range result {
		if len(merged) > 0 && merged[len(merged)-1].Role == msg.Role {
			merged[len(merged)-1].Content = append(merged[len(merged)-1].Content, msg.Content...)
			continue
		}
		merged = append(merged, msg)
	}

	// tool_results answering the previous assistant turn must lead the user
	// message: stable-partition results first.
	for i := range merged {
		if merged[i].Role != "user" {
			continue
		}
		firstNonResult := -1
		for j, block := range merged[i].Content {
			if _, ok := block.(ToolResultContentBlockMsg); !ok {
				firstNonResult = j
				break
			}
		}
		if firstNonResult < 0 {
			continue
		}
		needsReorder := false
		for _, block := range merged[i].Content[firstNonResult:] {
			if _, ok := block.(ToolResultContentBlockMsg); ok {
				needsReorder = true
				break
			}
		}
		if needsReorder {
			var results, rest []ContentBlock
			for _, block := range merged[i].Content {
				if _, ok := block.(ToolResultContentBlockMsg); ok {
					results = append(results, block)
				} else {
					rest = append(rest, block)
				}
			}
			merged[i].Content = append(results, rest...)
		}
	}

	rewriteOrphanedToolResults(merged)
	answerUnpairedToolUses(&merged)

	// A trailing assistant turn (jcode never prefills) is always an upstream
	// accident; append a continuation user turn to avoid a 400 — unless the
	// turn is a paused server-tool turn that must be resent verbatim.
	if len(merged) > 0 && merged[len(merged)-1].Role == "assistant" && !isPausedServerToolTurn(merged[len(merged)-1]) {
		merged = append(merged, ApiMessage{
			Role:    "user",
			Content: []ContentBlock{TextContent{Text: ContinuationUserTurn}},
		})
	}

	return merged
}

// isPausedServerToolTurn reports an assistant turn ending on a raw server-tool
// block (pause_turn resume shape).
func isPausedServerToolTurn(message ApiMessage) bool {
	if len(message.Content) == 0 {
		return false
	}
	_, ok := message.Content[len(message.Content)-1].(RawContent)
	return ok
}

func strPtr(s string) *string { return &s }

// formatContentBlocksWithNative converts session blocks to API blocks.
func formatContentBlocksWithNative(blocks []types.ContentBlock, isOAuth, nativeReplay bool) []ContentBlock {
	var result []ContentBlock
	nativeCallInputs := map[string]json.RawMessage{}
	for i := range blocks {
		block := blocks[i]
		switch block.Type {
		case types.BlockProviderNative:
			item := block.Item
			provider := ""
			if block.Provider != nil {
				provider = *block.Provider
			}
			if display := types.ProviderNativeDisplayOf(provider, item); display != nil &&
				display.Output == nil && len(display.Input) > 0 {
				nativeCallInputs[display.ID] = display.Input
			}
			var callInput json.RawMessage
			if display := types.ProviderNativeDisplayOf(provider, item); display != nil {
				callInput = nativeCallInputs[display.ID]
			}
			blockType := types.JSONStr(item, "type")
			isAnthropicBlock := provider == types.ProviderNativeAnthropic &&
				types.IsAnthropicServerToolBlock(blockType)
			if nativeReplay && isAnthropicBlock {
				result = append(result, RawContent{Item: item})
			} else if text := types.ProviderNativeTextFallback(provider, item, callInput); text != "" {
				result = append(result, TextContent{Text: text})
			}
		case types.BlockText:
			// Fold the image-attachment label into the preceding tool_result so
			// tool_results stay contiguous (the API rejects interleaved text).
			text := ""
			if block.Text != nil {
				text = *block.Text
			}
			if len(result) > 0 {
				if tr, ok := result[len(result)-1].(ToolResultContentBlockMsg); ok && trHasImage(tr) {
					tr.Content.Blocks = append(tr.Content.Blocks, TextResultBlock(text))
					result[len(result)-1] = tr
					continue
				}
			}
			result = append(result, TextContent{Text: text})
		case types.BlockAnthropicThink:
			thinking, signature := "", ""
			if block.Thinking != nil {
				thinking = *block.Thinking
			}
			if block.Signature != nil {
				signature = *block.Signature
			}
			result = append(result, ThinkingContent{Thinking: thinking, Signature: signature})
		case types.BlockToolUse:
			id, name, input, _ := block.AsToolUse()
			if isOAuth {
				name = MapToolNameForOAuth(name)
			}
			result = append(result, ToolUseContent{
				ID:    types.SanitizeToolID(id),
				Name:  name,
				Input: json.RawMessage(normalizeObject(input)),
			})
		case types.BlockToolResult:
			toolUseID, content, isErr, _ := block.AsToolResult()
			result = append(result, ToolResultContentBlockMsg{
				ToolUseID: types.SanitizeToolID(toolUseID),
				Content:   ToolResultContent{Text: &content},
				IsError:   isErr,
			})
		case types.BlockImage:
			mediaType, data := "", ""
			if block.MediaType != nil {
				mediaType = *block.MediaType
			}
			if block.Data != nil {
				data = *block.Data
			}
			img := ImageContent{Source: ApiImageSource{Kind: "base64", MediaType: mediaType, Data: data}}
			if len(result) > 0 {
				if tr, ok := result[len(result)-1].(ToolResultContentBlockMsg); ok {
					switch {
					case tr.Content.Text != nil:
						text := *tr.Content.Text
						tr.Content = ToolResultContent{Blocks: []ToolResultContentBlock{
							TextResultBlock(text), ImageResultBlock(img.Source),
						}}
					default:
						tr.Content.Blocks = append(tr.Content.Blocks, ImageResultBlock(img.Source))
					}
					result[len(result)-1] = tr
					continue
				}
			}
			result = append(result, img)
		}
	}
	return result
}

func trHasImage(tr ToolResultContentBlockMsg) bool {
	for _, b := range tr.Content.Blocks {
		if b.Type == "image" {
			return true
		}
	}
	return false
}

func normalizeObject(raw json.RawMessage) string {
	var probe any
	if len(raw) > 0 && json.Unmarshal(raw, &probe) == nil {
		if _, ok := probe.(map[string]any); ok {
			return string(raw)
		}
	}
	return "{}"
}

// applyToolReferences folds ToolReference blocks into their tool_result: the
// referencing tool_result carries only references, its text moves to a sibling
// text block right after the tool_results.
func applyToolReferences(content *[]ContentBlock, blocks []types.ContentBlock, isOAuth bool, available map[string]struct{}) {
	refs := map[string][]string{}
	var order []string
	for i := range blocks {
		block := blocks[i]
		if block.Type != types.BlockToolReference {
			continue
		}
		toolUseID := ""
		if block.ToolUseID != nil {
			toolUseID = *block.ToolUseID
		}
		toolName := ""
		if block.ToolName != nil {
			toolName = *block.ToolName
		}
		if isOAuth {
			toolName = MapToolNameForOAuth(toolName)
		}
		if _, ok := available[toolName]; !ok {
			continue
		}
		id := types.SanitizeToolID(toolUseID)
		if _, seen := refs[id]; !seen {
			order = append(order, id)
		}
		if !containsString(refs[id], toolName) {
			refs[id] = append(refs[id], toolName)
		}
	}
	if len(refs) == 0 {
		return
	}
	var movedText []ContentBlock
	for i, block := range *content {
		tr, ok := block.(ToolResultContentBlockMsg)
		if !ok {
			continue
		}
		names, ok := refs[tr.ToolUseID]
		if !ok {
			continue
		}
		delete(refs, tr.ToolUseID)
		var previous ToolResultContent
		if tr.Content.Text != nil {
			previous = ToolResultContent{Text: tr.Content.Text}
		} else {
			previous = ToolResultContent{Blocks: tr.Content.Blocks}
		}
		var refBlocks []ToolResultContentBlock
		for _, name := range names {
			refBlocks = append(refBlocks, ToolReferenceBlock(name))
		}
		tr.Content = ToolResultContent{Blocks: refBlocks}
		(*content)[i] = tr
		var texts []string
		if previous.Text != nil {
			texts = append(texts, *previous.Text)
		}
		for _, b := range previous.Blocks {
			if b.Type == "text" && b.Text != nil {
				texts = append(texts, *b.Text)
			}
		}
		for _, text := range texts {
			if trimSpace(text) != "" {
				movedText = append(movedText, TextContent{Text: text})
			}
		}
	}
	if len(movedText) == 0 {
		return
	}
	insertAt := 0
	for i := len(*content) - 1; i >= 0; i-- {
		if _, ok := (*content)[i].(ToolResultContentBlockMsg); ok {
			insertAt = i + 1
			break
		}
	}
	*content = append((*content)[:insertAt], append(movedText, (*content)[insertAt:]...)...)
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpaceByte(s[start]) {
		start++
	}
	for end > start && isSpaceByte(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// rewriteOrphanedToolResults rewrites tool_results not answered by the
// immediately preceding assistant message into plain text blocks.
func rewriteOrphanedToolResults(messages []ApiMessage) {
	rewritten := 0
	for i := range messages {
		if messages[i].Role != "user" {
			continue
		}
		expected := map[string]struct{}{}
		if i > 0 && messages[i-1].Role == "assistant" {
			for _, block := range messages[i-1].Content {
				if tu, ok := block.(ToolUseContent); ok {
					expected[tu.ID] = struct{}{}
				}
			}
		}
		hasOrphan := false
		for _, block := range messages[i].Content {
			if tr, ok := block.(ToolResultContentBlockMsg); ok {
				if _, answered := expected[tr.ToolUseID]; !answered {
					hasOrphan = true
					break
				}
			}
		}
		if !hasOrphan {
			continue
		}
		var paired, other []ContentBlock
		for _, block := range messages[i].Content {
			tr, isResult := block.(ToolResultContentBlockMsg)
			if !isResult {
				other = append(other, block)
				continue
			}
			if _, answered := expected[tr.ToolUseID]; answered {
				paired = append(paired, block)
				continue
			}
			rewritten++
			label := "Recovered orphaned tool output"
			if tr.IsError {
				label = "Recovered orphaned tool error"
			}
			if tr.Content.Text != nil {
				other = append(other, TextContent{Text: "[" + label + ": " + tr.ToolUseID + "]\n" + *tr.Content.Text})
			} else {
				other = append(other, TextContent{Text: "[" + label + ": " + tr.ToolUseID + "]"})
				for _, b := range tr.Content.Blocks {
					switch {
					case b.Type == "text" && b.Text != nil:
						other = append(other, TextContent{Text: *b.Text})
					case b.Type == "image" && b.Source != nil:
						other = append(other, ImageContent{Source: *b.Source})
					case b.Type == "tool_reference" && b.ToolName != nil:
						other = append(other, TextContent{Text: "[tool reference: " + *b.ToolName + "]"})
					}
				}
			}
		}
		messages[i].Content = append(paired, other...)
	}
	if rewritten > 0 {
		// logged via warn in upstream; gcode logging comes with the transport
	}
}

// answerUnpairedToolUses guarantees every tool_use is answered in the next
// user message (or a new one), with a synthetic error result.
func answerUnpairedToolUses(messages *[]ApiMessage) {
	injected := 0
	for i := 0; i < len(*messages); {
		if (*messages)[i].Role != "assistant" {
			i++
			continue
		}
		var toolUseIDs []string
		for _, block := range (*messages)[i].Content {
			if tu, ok := block.(ToolUseContent); ok {
				toolUseIDs = append(toolUseIDs, tu.ID)
			}
		}
		if len(toolUseIDs) == 0 {
			i++
			continue
		}
		nextIsUser := i+1 < len(*messages) && (*messages)[i+1].Role == "user"
		answered := map[string]struct{}{}
		if nextIsUser {
			for _, block := range (*messages)[i+1].Content {
				if tr, ok := block.(ToolResultContentBlockMsg); ok {
					answered[tr.ToolUseID] = struct{}{}
				}
			}
		}
		var synthetic []ContentBlock
		for _, id := range toolUseIDs {
			if _, ok := answered[id]; !ok {
				synthetic = append(synthetic, ToolResultContentBlockMsg{
					ToolUseID: id,
					Content:   ToolResultContent{Text: strPtr(DisplacedToolResultText)},
					IsError:   true,
				})
			}
		}
		if len(synthetic) > 0 {
			injected += len(synthetic)
			if nextIsUser {
				content := (*messages)[i+1].Content
				insertAt := len(content)
				for idx, block := range content {
					if _, ok := block.(ToolResultContentBlockMsg); !ok {
						insertAt = idx
						break
					}
				}
				merged := append([]ContentBlock{}, content[:insertAt]...)
				merged = append(merged, synthetic...)
				merged = append(merged, content[insertAt:]...)
				(*messages)[i+1].Content = merged
			} else {
				*messages = append((*messages)[:i+1], append([]ApiMessage{{Role: "user", Content: synthetic}}, (*messages)[i+1:]...)...)
			}
		}
		i += 2
	}
	if injected > 0 {
		// upstream warns; transport-level logging lands with the HTTP layer
	}
}

// sortedKeys is used by tests to stabilize map iteration.
func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
