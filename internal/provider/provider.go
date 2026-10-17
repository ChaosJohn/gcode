// Package provider ports the jcode-provider-core Provider trait surface that
// the agent loop consumes: model state, catalog, capability flags, and one
// Complete entry point emitting StreamEvents. The heavy per-provider request
// logic lives in the anthropic/openai/gemini packages; the adapters here only
// hold runtime state (current model, credentials wiring) and assemble options.
package provider

import (
	"errors"
	"fmt"

	"github.com/ChaosJohn/gcode/internal/types"
)

// ErrUnknownModel is returned by SetModel for a model outside the catalog.
var ErrUnknownModel = errors.New("model is not in the provider catalog")

// Provider is the provider-core trait (core subset; OAuth account mutators,
// route selection, and catalog sharing identities arrive with the account
// work after M4).
type Provider interface {
	// Name is the stable machine-facing identifier ("claude", "gemini", "codex").
	Name() string
	// Model returns the current model.
	Model() string
	// SetModel switches the model; validates against the catalog when one is
	// advertised.
	SetModel(model string) error
	// AvailableModels lists the static catalog.
	AvailableModels() []string
	// SupportsImageInput reports image block support.
	SupportsImageInput() bool
	// SupportsCompaction reports native compaction support.
	SupportsCompaction() bool
	// Fork clones the provider state for a sub-agent.
	Fork() Provider
	// InvalidateCredentials drops cached OAuth state.
	InvalidateCredentials()
	// Complete streams one assistant turn.
	Complete(turn TurnRequest, emit func(types.StreamEvent) error) error
}

// TurnRequest is one completion call.
type TurnRequest struct {
	Messages []types.Message
	Tools    []types.ToolDefinition
	System   string
	// DynamicSystem is the split system prompt tail (cache-unfriendly parts).
	DynamicSystem   string
	ResumeSessionID string
	// MaxTokensOverride adjusts the request token budget.
	MaxTokensOverride *int
	// ShowThinking forwards the reasoning visibility setting.
	ShowThinking bool
	// Effort forwards the reasoning effort selection.
	Effort string
}

// ModelCatalog is the optional static model list a provider advertises.
func modelContains(models []string, model string) bool {
	for _, candidate := range models {
		if candidate == model {
			return true
		}
	}
	return false
}

// ValidateModel guards SetModel against catalog misses.
func ValidateModel(models []string, model string) error {
	if len(models) == 0 || modelContains(models, model) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnknownModel, model)
}

// MessagesWithDynamicSystemContext ports provider-core
// messages_with_dynamic_system_context: the dynamic tail becomes an in-band
// system-reminder user message ahead of the transcript.
func MessagesWithDynamicSystemContext(messages []types.Message, dynamicSystem string) []types.Message {
	if dynamicSystem == "" {
		return messages
	}
	text := "<system_context>\n" + dynamicSystem + "\n</system_context>"
	out := make([]types.Message, 0, len(messages)+1)
	out = append(out, types.Message{Role: types.RoleUser,
		Content: []types.ContentBlock{types.TextBlock(text)}})
	return append(out, messages...)
}
