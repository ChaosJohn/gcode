package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ChaosJohn/gcode/internal/provider"
	"github.com/ChaosJohn/gcode/internal/sessionstore"
	"github.com/ChaosJohn/gcode/internal/toolcore"
	"github.com/ChaosJohn/gcode/internal/types"
)

// Turn-loop constants ported from agent/turn_loops.rs.
const (
	MaxContextLimitRetries               = 5
	MaxIncompleteContinuationAttempts    = 3
	MaxEmptyPostToolContinuationAttempts = 5
	sequentialToolRoundsBeforeBatchNudge = 3
	BatchNudge                           = "<system-reminder>Several tool calls have been made one at a time. If the next independent operations can run concurrently, use the batch tool instead of making more sequential calls. Keep sequential calls when one result is required to decide the next operation.</system-reminder>"
)

// maxToolOutputCharsForHistory ports MAX_TOOL_OUTPUT_CHARS_FOR_HISTORY.
const maxToolOutputCharsForHistory = 512 * 1024

// ToolHooks lets the host application observe tool lifecycle (the upstream
// bus ToolEvent surface, reduced to callbacks).
type ToolHooks struct {
	// OnToolStart fires before execution.
	OnToolStart func(call types.ToolCall)
	// OnToolEnd fires after execution with a one-line summary.
	OnToolEnd func(call types.ToolCall, summary string, isErr bool)
	// OnEvent observes every stream event (CLI/TUI rendering hooks in here;
	// errors from the callback abort the stream).
	OnEvent func(event types.StreamEvent) error
	// OnStatus reports non-stream state changes (compaction, retries).
	OnStatus func(text string)
}

// Agent is the turn loop (ported subset of agent.rs): conversation history,
// locked tool surface, system prompt, and run_turn/run_once.
type Agent struct {
	Provider   provider.Provider
	Registry   *Registry
	System     string
	WorkingDir string
	SessionID  string
	Hooks      ToolHooks
	// MaxTurns bounds the loop for safety (0 = unbounded, upstream default).
	MaxTurns int

	Messages []types.Message
	// ToolCallIDs and ToolResultIDs track transcript ids for repair logic.
	ToolCallIDs   map[string]bool
	ToolResultIDs map[string]bool
	lockedTools   []types.ToolDefinition
	// LastUsage is the final usage report of the last turn.
	LastUsage types.StoredTokenUsage
	// markupNudges counts re-issue reminders for unparseable markup.
	markupNudges    int
	recoveryCounter int
	// persistStore is the session persistence sink when wired (M4).
	persistStore *sessionstore.Store
}

// NewAgent wires the loop.
func NewAgent(p provider.Provider, registry *Registry) *Agent {
	return &Agent{
		Provider: p, Registry: registry,
		ToolCallIDs: map[string]bool{}, ToolResultIDs: map[string]bool{},
		SessionID: "local",
	}
}

// AddMessage appends a message to the in-memory transcript.
func (a *Agent) AddMessage(role types.Role, blocks []types.ContentBlock) {
	a.Messages = append(a.Messages, types.Message{Role: role, Content: blocks})
}

// LockTools freezes the tool list once the first request is sent (cache
// stability while MCP tools arrive asynchronously). Cleared by UnlockTools.
func (a *Agent) LockTools() {
	if a.lockedTools == nil {
		a.lockedTools = a.Registry.Definitions()
	}
}

// UnlockTools rebuilds the snapshot on the next turn (compaction/reset).
func (a *Agent) UnlockTools() { a.lockedTools = nil }

func (a *Agent) toolDefinitions() []types.ToolDefinition {
	a.LockTools()
	return a.lockedTools
}

// capToolOutputForHistory ports cap_tool_output_for_history: huge outputs
// get a truncation notice that protects the remote protocol and cache.
func capToolOutputForHistory(toolName string, output toolcore.ToolOutput) toolcore.ToolOutput {
	runes := []rune(output.Output)
	if len(runes) <= maxToolOutputCharsForHistory {
		return output
	}
	original := len(runes)
	output.Output = fmt.Sprintf(
		"%s\n\n[Tool output truncated by jcode: tool `%s` produced %d chars; kept first %d chars to protect the remote protocol, session history, and prompt cache. Redirect large logs to a file and read targeted sections.]",
		string(runes[:maxToolOutputCharsForHistory]), toolName, original,
		maxToolOutputCharsForHistory)
	return output
}

// RunOnce appends the user message and drives turns until the model stops
// calling tools (ported run_once + run_turn).
func (a *Agent) RunOnce(userMessage string) (string, error) {
	if strings.TrimSpace(userMessage) != "" {
		a.AddMessage(types.RoleUser, []types.ContentBlock{types.TextBlock(userMessage)})
	}
	finalText := ""
	contextLimitRetries := 0
	emptyPostToolContinuations := 0
	sequentialSingleToolRounds := 0
	batchNudgePending := false

	turns := 0
	for {
		if a.MaxTurns > 0 && turns >= a.MaxTurns {
			return finalText, fmt.Errorf("agent turn limit reached (%d)", a.MaxTurns)
		}
		turns++

		tools := a.toolDefinitions()
		requestMessages := append([]types.Message(nil), a.Messages...)
		if batchNudgePending {
			hasBatch := false
			for _, tool := range tools {
				if tool.Name == "batch" {
					hasBatch = true
				}
			}
			if hasBatch {
				requestMessages = append(requestMessages,
					types.Message{Role: types.RoleUser,
						Content: []types.ContentBlock{types.TextBlock(BatchNudge)}})
			}
			batchNudgePending = false
			sequentialSingleToolRounds = 0
		}

		// Stream one assistant turn.
		var textContent strings.Builder
		var toolCalls []types.ToolCall
		streamingTools := map[string]*strings.Builder{}
		currentTool := ""
		sawMessageEnd := false
		var assistantUsage types.StoredTokenUsage
		_ = sawMessageEnd

		turn := provider.TurnRequest{
			Messages: requestMessages, Tools: tools, System: a.System,
			ShowThinking: true,
		}
		err := a.Provider.Complete(turn, func(event types.StreamEvent) error {
			if a.Hooks.OnEvent != nil {
				if err := a.Hooks.OnEvent(event); err != nil {
					return err
				}
			}
			switch e := event.(type) {
			case types.TextDelta:
				textContent.WriteString(e.Text)
			case types.TextSuffixReplace:
				text := textContent.String()
				if strings.HasSuffix(text, e.OldText) {
					textContent.Reset()
					textContent.WriteString(text[:len(text)-len(e.OldText)] + e.NewText)
				}
			case types.ToolUseStart:
				if _, exists := streamingTools[e.ID]; exists {
					return nil
				}
				currentTool = e.ID
				streamingTools[e.ID] = &strings.Builder{}
				toolCalls = append(toolCalls, types.ToolCall{
					ID: e.ID, Name: e.Name, Input: json.RawMessage("null"),
				})
			case types.ToolInputDelta:
				if builder := streamingTools[currentTool]; builder != nil {
					builder.WriteString(e.Delta)
				}
			case types.ToolInputDeltaFor:
				if builder := streamingTools[e.ID]; builder != nil {
					builder.WriteString(e.Delta)
				}
			case types.ToolUseEnd:
				a.finalizeToolCall(&toolCalls, currentTool, streamingTools)
			case types.ToolUseEndFor:
				a.finalizeToolCall(&toolCalls, e.ID, streamingTools)
			case types.MessageEnd:
				sawMessageEnd = true
			case types.TokenUsageEvent:
				if e.InputTokens != nil {
					assistantUsage.InputTokens = *e.InputTokens
				}
				if e.OutputTokens != nil {
					assistantUsage.OutputTokens = *e.OutputTokens
				}
				if e.CacheReadInputTokens != nil {
					value := *e.CacheReadInputTokens
					assistantUsage.CacheReadInputTokens = &value
				}
				if e.CacheCreationInputTokens != nil {
					value := *e.CacheCreationInputTokens
					assistantUsage.CacheCreationInputTokens = &value
				}
			}
			return nil
		})
		if err != nil {
			// Ported try_auto_compact_after_context_limit: on a context-limit
			// error, summarize the older turns and retry with the compacted
			// history instead of failing the run.
			if isContextLimitError(err.Error()) {
				contextLimitRetries++
				if contextLimitRetries > MaxContextLimitRetries {
					return "", fmt.Errorf("Context limit exceeded after %d compaction retries",
						MaxContextLimitRetries)
				}
				outcome, compactErr := a.autoCompact()
				if compactErr != nil {
					return "", fmt.Errorf("context limit exceeded and auto-compaction failed: %w", compactErr)
				}
				if outcome == nil {
					// Nothing to drop (history too short): plain retry.
					continue
				}
				a.Messages = append([]types.Message{outcome.SummaryUserMessage}, outcome.Kept...)
				if a.Hooks.OnStatus != nil {
					a.Hooks.OnStatus(fmt.Sprintf(
						"[compaction] summarized %d messages; retrying", outcome.Dropped))
				}
				continue
			}
			return "", err
		}
		a.LastUsage = assistantUsage
		contextLimitRetries = 0

		assistant := types.Message{Role: types.RoleAssistant,
			Content: []types.ContentBlock{types.TextBlock(textContent.String())}}
		if len(toolCalls) > 0 {
			for _, call := range toolCalls {
				assistant.Content = append(assistant.Content, call.ToToolUseBlock())
				a.ToolCallIDs[call.ID] = true
			}
		}
		if len(toolCalls) == 0 {
			// Ported empty-post-tool guard: a transient empty response right
			// after tool results must not silently end the task. The empty
			// assistant message is not appended, so the next request still
			// ends with the tool result.
			if strings.TrimSpace(textContent.String()) == "" && a.transcriptEndsWithToolResult() {
				emptyPostToolContinuations++
				if emptyPostToolContinuations > MaxEmptyPostToolContinuationAttempts {
					return textContent.String(), nil
				}
				continue
			}
			// Ported recover_text_wrapped_tool_call: the model printed its
			// tool-call envelope as text (issue #1702). Execute the recovered
			// calls, or nudge the model to re-issue them natively.
			recovery := RecoverTextToolCalls(textContent.String())
			switch recovery.Status {
			case "recovered":
				recoveredCalls := make([]types.ToolCall, 0, len(recovery.Calls))
				for i, call := range recovery.Calls {
					id := fmt.Sprintf("fallback_text_call_%d_%d", a.recoveryCounter, i+1)
					recoveredCalls = append(recoveredCalls,
						types.ToolCall{ID: id, Name: call.Name, Input: call.Arguments})
				}
				a.recoveryCounter++
				textContent.Reset()
				textContent.WriteString(recovery.Sanitized)
				assistant = types.Message{Role: types.RoleAssistant,
					Content: []types.ContentBlock{types.TextBlock(recovery.Sanitized)}}
				toolCalls = recoveredCalls
				if a.Hooks.OnStatus != nil {
					names := make([]string, 0, len(recoveredCalls))
					for _, call := range recoveredCalls {
						names = append(names, call.Name)
					}
					a.Hooks.OnStatus(fmt.Sprintf(
						"[tool-call recovery] recovered %d call(s) from text markup: %s",
						len(recoveredCalls), strings.Join(names, ", ")))
				}
			case "unparseable":
				if a.markupNudges < MaxUnparsedToolMarkupNudges {
					a.markupNudges++
					if a.Hooks.OnStatus != nil {
						a.Hooks.OnStatus(UnparsedMarkupNotice(recovery.Reason))
					}
					a.AddMessage(types.RoleUser,
						[]types.ContentBlock{types.TextBlock(UnparsedMarkupReminder(recovery.Reason))})
					continue
				}
				// Nudges exhausted: end the turn with the raw text.
			}
			if len(toolCalls) == 0 {
				a.AddMessage(assistant.Role, assistant.Content)
				if a.persistStore != nil {
					_ = a.PersistTranscript()
				}
				finalText = textContent.String()
				return finalText, nil
			}
		}
		a.AddMessage(assistant.Role, assistant.Content)

		// Execute tools and append user-side tool results.
		usedBatch := false
		for i := range toolCalls {
			if toolCalls[i].Name == "batch" {
				usedBatch = true
			}
			result, isErr := a.executeToolCall(&toolCalls[i])
			a.AddMessage(types.RoleUser, []types.ContentBlock{
				types.ToolResultBlock(toolCalls[i].ID, result, isErr)})
			a.ToolResultIDs[toolCalls[i].ID] = true
		}
		if len(toolCalls) == 1 && !usedBatch {
			sequentialSingleToolRounds++
		} else {
			sequentialSingleToolRounds = 0
		}
		if sequentialSingleToolRounds >= sequentialToolRoundsBeforeBatchNudge {
			batchNudgePending = true
		}
		// Persist after every tool round so a crash keeps completed work.
		if a.persistStore != nil {
			_ = a.PersistTranscript()
		}
	}
}

func (a *Agent) finalizeToolCall(toolCalls *[]types.ToolCall, id string,
	streamingTools map[string]*strings.Builder) {
	builder := streamingTools[id]
	if builder == nil {
		return
	}
	raw := builder.String()
	for i := range *toolCalls {
		if (*toolCalls)[i].ID == id {
			if strings.TrimSpace(raw) == "" {
				(*toolCalls)[i].Input = json.RawMessage("{}")
			} else {
				(*toolCalls)[i].Input = json.RawMessage(raw)
			}
		}
	}
	delete(streamingTools, id)
}

func (a *Agent) executeToolCall(call *types.ToolCall) (string, bool) {
	if message := ValidationMessage(call.Name, call.Input); message != "" {
		return message, true
	}
	var input any
	if len(call.Input) > 0 && string(call.Input) != "null" {
		_ = json.Unmarshal(call.Input, &input)
	}
	encoded, _ := json.Marshal(input)
	ctx := toolcore.ToolContext{
		SessionID: a.SessionID, ToolCallID: call.ID, WorkingDir: a.WorkingDir,
		Mode: toolcore.ExecutionAgentTurn,
	}
	if a.Hooks.OnToolStart != nil {
		a.Hooks.OnToolStart(*call)
	}
	output, err := a.Registry.Execute(call.Name, json.RawMessage(encoded), ctx)
	output = capToolOutputForHistory(call.Name, output)
	summary := firstLine(output.Output)
	if a.Hooks.OnToolEnd != nil {
		a.Hooks.OnToolEnd(*call, summary, err != nil)
	}
	if err != nil {
		if output.Output != "" {
			return output.Output + "\n\nError: " + err.Error(), true
		}
		return err.Error(), true
	}
	if output.Output == "" {
		return "Tool completed with no output", false
	}
	return output.Output, false
}

func (a *Agent) transcriptEndsWithToolResult() bool {
	if len(a.Messages) == 0 {
		return false
	}
	last := a.Messages[len(a.Messages)-1]
	if last.Role != types.RoleUser || len(last.Content) == 0 {
		return false
	}
	for _, block := range last.Content {
		if block.Type == types.BlockToolResult {
			return true
		}
	}
	return false
}

func firstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	runes := []rune(text)
	if len(runes) > 200 {
		text = string(runes[:200]) + "..."
	}
	return text
}

// autoCompact summarizes everything except the recent-turn suffix. Returns
// nil when the history is too short to compact.
func (a *Agent) autoCompact() (*CompactOutcome, error) {
	cutoff := chooseCompactionCutoff(a.Messages)
	if cutoff == 0 {
		return nil, nil
	}
	return BuildCompactionOutcome(a.Messages, cutoff, func(prompt string) (string, error) {
		var summary strings.Builder
		request := provider.TurnRequest{System: "You are a precise summarizer.",
			Messages: []types.Message{{Role: types.RoleUser,
				Content: []types.ContentBlock{types.TextBlock(prompt)}}}}
		err := a.Provider.Complete(request, func(event types.StreamEvent) error {
			if delta, ok := event.(types.TextDelta); ok {
				summary.WriteString(delta.Text)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(summary.String())
		if text == "" {
			return "", fmt.Errorf("summarizer returned empty output")
		}
		return text, nil
	})
}

// isContextLimitError matches the upstream context-limit error classifier.
func isContextLimitError(message string) bool {
	lowered := strings.ToLower(message)
	for _, needle := range []string{
		"context length", "context_length", "prompt is too long",
		"exceeds the context", "maximum context", "too many tokens",
		"request too large", "payload too large",
	} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}
