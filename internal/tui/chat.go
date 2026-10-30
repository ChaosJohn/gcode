// Chat agent assembly for the TUI: in-process agent with streaming into
// the transcript view (streaming deltas, tool activity lines, spinner).
package tui

import (
	"fmt"
	"strings"

	"github.com/ChaosJohn/gcode/internal/agent"
	"github.com/ChaosJohn/gcode/internal/appcore"
	"github.com/ChaosJohn/gcode/internal/sessionstore"
	"github.com/ChaosJohn/gcode/internal/types"
)

// BuildChatAgent attaches a fresh agent to an existing session store.
func BuildChatAgent(m *Model, store *sessionstore.Store) {
	cfg, cfgErr := appcore.LoadConfig()
	providerInstance, err := appcore.BuildProvider(cfg, store.Session.ID)
	if err != nil || cfgErr != nil {
		providerInstance = nil
	}
	registry := appcore.BuildRegistry()
	agentInstance := agent.NewAgent(providerInstance, registry)
	agentInstance.System = appcore.CLISystemPrompt
	agentInstance.WorkingDir = store.Session.WorkingDir
	agentInstance.SessionID = store.Session.ID
	for _, message := range store.Session.Messages {
		text := ""
		for _, block := range message.Content {
			if block.Text != nil {
				text += *block.Text
			}
		}
		agentInstance.AddMessage(message.Role,
			[]types.ContentBlock{{Type: "text", Text: &text}})
		if text != "" {
			prefix := "gcode › "
			if message.Role == types.RoleUser {
				prefix = "you › "
			}
			m.transcript = append(m.transcript, prefix+text)
		}
	}
	if providerInstance != nil {
		m.modelName = providerInstance.Model()
	}
	// Streaming deltas accumulate into the live pending line; the main
	// loop redraws on wake.
	agentInstance.Hooks.OnEvent = func(event types.StreamEvent) error {
		switch e := event.(type) {
		case types.TextDelta:
			m.streaming.WriteString(e.Text)
			m.wake()
		}
		return nil
	}
	// Tool activity: "⚙ name …" on start, then the completion line.
	agentInstance.Hooks.OnToolStart = func(call types.ToolCall) {
		m.pushTranscript(fmt.Sprintf("  ⚙ %s …", call.Name))
	}
	agentInstance.Hooks.OnToolEnd = func(call types.ToolCall, summary string, isErr bool) {
		marker := "✓"
		if isErr {
			marker = "✗"
		}
		detail := strings.TrimSpace(summary)
		runes := []rune(detail)
		if len(runes) > 80 {
			detail = string(runes[:79]) + "…"
		}
		m.pushTranscript(fmt.Sprintf("  ⚙ %s %s %s", call.Name, marker, detail))
	}
	agentInstance.Hooks.OnStatus = func(message string) {
		m.statusMsg = message
		m.wake()
	}
	m.handle = store
	m.chatAgent = agentInstance
}

// ChatTurn runs one turn and persists the transcript.
func ChatTurn(m *Model, prompt string) (string, error) {
	if m.chatAgent == nil {
		return "", errNoAgent
	}
	final, err := m.chatAgent.RunOnce(prompt)
	if m.handle != nil && err == nil {
		_ = m.chatAgent.PersistTranscript()
	}
	return final, err
}

type tuiError string

func (e tuiError) Error() string { return string(e) }

const errNoAgent = tuiError("no chat agent attached")
