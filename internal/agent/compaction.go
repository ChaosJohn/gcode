// Compaction (ported from jcode-compaction-core + the base CompactionManager
// reactive path): summarize older turns into a `## Previous Conversation
// Summary` user message so a context-limit run can continue.
package agent

import (
	"fmt"
	"strings"

	"github.com/ChaosJohn/gcode/internal/types"
)

// Upstream compaction constants (jcode-compaction-core).
const (
	// CharsPerToken is the crude char→token ratio used for estimates.
	CharsPerToken = 4
	// RecentTurnsToKeep is the target suffix of user turns kept verbatim.
	RecentTurnsToKeep = 10
	// MinTurnsToKeep is the floor when history is shorter.
	MinTurnsToKeep = 2
	// SummaryToolResultMaxChars bounds per-result text in the prompt.
	SummaryToolResultMaxChars = 500
	// SystemOverheadTokens is the reserved budget for system prompt + tools.
	SystemOverheadTokens = 18000
	// CompactionThreshold is the reactive trigger at 80% context usage.
	CompactionThreshold = 0.80
)

// SummaryPrompt is the upstream verbatim summarization prompt.
const SummaryPrompt = `Summarize our conversation so you can continue this work later.

Write in natural language with these sections:
- **Context:** What we're working on and why (1-2 sentences)
- **What we did:** Key actions taken, files changed, problems solved
- **Current state:** What works, what's broken, what's next
- **User preferences:** Specific requirements or decisions they made

Be concise but preserve important details. You can search the full conversation later if you need exact error messages or code snippets.`

// CompactedSummaryTextBlock wraps a summary as the leading user message text.
func CompactedSummaryTextBlock(summary string) string {
	return fmt.Sprintf("## Previous Conversation Summary\n\n%s\n\n---\n\n", summary)
}

// EstimateTokens is the char/4 estimate for a message list.
func EstimateTokens(messages []types.Message) int {
	chars := 0
	for _, message := range messages {
		chars += messageCharCount(message)
	}
	return chars / CharsPerToken
}

func messageCharCount(message types.Message) int {
	chars := len(string(message.Role))
	for _, block := range message.Content {
		if block.Text != nil {
			chars += len(*block.Text)
		}
		if block.Content != nil {
			chars += len(*block.Content)
		}
		chars += len(block.Input)
	}
	return chars
}

// BuildCompactionConversationText ports build_compaction_conversation_text:
// a flat transcript with prior summary heading, tool calls as one-liners and
// results truncated.
func BuildCompactionConversationText(messages []types.Message, previousSummary string) string {
	var text strings.Builder
	if strings.TrimSpace(previousSummary) != "" {
		text.WriteString("## Previous Summary\n\n")
		text.WriteString(previousSummary)
		text.WriteString("\n\n## New Conversation\n\n")
	}
	for _, message := range messages {
		role := "User"
		if message.Role == types.RoleAssistant {
			role = "Assistant"
		}
		fmt.Fprintf(&text, "**%s:**\n", role)
		for _, block := range message.Content {
			switch {
			case block.Type == types.BlockText && block.Text != nil && *block.Text != "":
				text.WriteString(*block.Text)
				text.WriteString("\n")
			case block.Type == types.BlockToolUse && block.Name != nil:
				input := string(block.Input)
				if len(input) > SummaryToolResultMaxChars {
					input = truncateBoundary(input, SummaryToolResultMaxChars) + "... (truncated)"
				}
				fmt.Fprintf(&text, "[Tool: %s - %s]\n", *block.Name, input)
			case block.Type == types.BlockToolResult && block.Content != nil:
				content := *block.Content
				if len(content) > SummaryToolResultMaxChars {
					content = truncateBoundary(content, SummaryToolResultMaxChars) +
						"... (truncated)"
				}
				fmt.Fprintf(&text, "[Result: %s]\n", content)
			}
		}
		text.WriteString("\n")
	}
	return text.String()
}

// BuildCompactionPrompt ports build_compaction_prompt with a byte budget.
func BuildCompactionPrompt(messages []types.Message, previousSummary string,
	maxPromptChars int) string {
	conversation := BuildCompactionConversationText(messages, previousSummary)
	overhead := len(SummaryPrompt) + 50
	if len(conversation)+overhead > maxPromptChars && maxPromptChars > overhead {
		budget := maxPromptChars - overhead
		conversation = truncateBoundary(conversation, budget) +
			"\n\n... [earlier conversation truncated to fit context window]\n"
	}
	return conversation + "\n---\n\n" + SummaryPrompt
}

// truncateBoundary cuts at a UTF-8 boundary (ports truncate_str_boundary).
func truncateBoundary(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	cut := value[:maxBytes]
	for len(cut) > 0 && (cut[len(cut)-1]&0xC0) == 0x80 {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// SafeCompactionCutoff ports safe_compaction_cutoff: grow the kept suffix
// until every kept tool result has its tool use inside it; 0 = cannot.
func SafeCompactionCutoff(messages []types.Message, initialCutoff int) int {
	cutoff := initialCutoff
	if cutoff > len(messages) {
		cutoff = len(messages)
	}
	available := map[string]bool{}
	missing := map[string]bool{}
	scan := func(message types.Message) {
		for _, block := range message.Content {
			switch {
			case block.Type == types.BlockToolUse && block.ID != nil:
				available[*block.ID] = true
				delete(missing, *block.ID)
			case block.Type == types.BlockToolResult && block.ToolUseID != nil:
				if !available[*block.ToolUseID] {
					missing[*block.ToolUseID] = true
				}
			}
		}
	}
	for _, message := range messages[cutoff:] {
		scan(message)
	}
	if len(missing) == 0 {
		return cutoff
	}
	for idx := cutoff - 1; idx >= 0; idx-- {
		scan(messages[idx])
		if len(missing) == 0 {
			return idx
		}
	}
	return 0
}

// chooseCompactionCutoff picks the message index where compaction splits:
// keep the last RecentTurnsToKeep user turns (floor MinTurnsToKeep), then
// repair for tool pairing.
func chooseCompactionCutoff(messages []types.Message) int {
	turns := 0
	cutoff := 0
	for idx := len(messages) - 1; idx >= 0; idx-- {
		if messages[idx].Role == types.RoleUser {
			turns++
			if turns >= RecentTurnsToKeep {
				cutoff = idx
				break
			}
		}
	}
	if turns < RecentTurnsToKeep {
		if turns <= MinTurnsToKeep {
			return 0 // not enough history to compact
		}
		cutoff = 0
	}
	if cutoff == 0 {
		return 0
	}
	return SafeCompactionCutoff(messages, cutoff)
}

// CompactOutcome is one compaction application.
type CompactOutcome struct {
	SummaryUserMessage types.Message
	Kept               []types.Message
	Dropped            int
}

// BuildCompactionOutcome summarizes messages[0:cutoff] via summarize and
// returns the replacement message list parts. cutoff must be > 0.
func BuildCompactionOutcome(messages []types.Message, cutoff int,
	summarize func(prompt string) (string, error)) (*CompactOutcome, error) {
	previous := ""
	// A prior compaction summary in the prefix feeds the new one.
	for _, block := range messages[0].Content {
		if block.Type == types.BlockText && block.Text != nil &&
			strings.HasPrefix(*block.Text, "## Previous Conversation Summary") {
			previous = strings.TrimPrefix(*block.Text, "## Previous Conversation Summary\n\n")
			previous = strings.TrimSuffix(strings.TrimSpace(previous), "\n---")
			break
		}
	}
	prompt := BuildCompactionPrompt(messages[:cutoff], previous, 200_000)
	summary, err := summarize(prompt)
	if err != nil {
		return nil, err
	}
	outcome := &CompactOutcome{
		SummaryUserMessage: types.Message{Role: types.RoleUser,
			Content: []types.ContentBlock{types.TextBlock(CompactedSummaryTextBlock(summary))}},
		Kept:    append([]types.Message(nil), messages[cutoff:]...),
		Dropped: cutoff,
	}
	return outcome, nil
}
