package provider

import (
	"context"
	"sync"

	"github.com/ChaosJohn/gcode/internal/base/config"
	"github.com/ChaosJohn/gcode/internal/provider/anthropic"
	"github.com/ChaosJohn/gcode/internal/provider/gemini"
	"github.com/ChaosJohn/gcode/internal/provider/openai"
	"github.com/ChaosJohn/gcode/internal/types"
)

// AnthropicOptions assembles the claude provider per turn.
type AnthropicOptions struct {
	// CompleteConfig carries auth + transport resolution; the app owns the
	// credential chain, the adapter only forwards it.
	CompleteConfig anthropic.CompleteConfig
	// RetrySettings carries effort/thinking/tier preferences.
	RetrySettings anthropic.RetrySettings
	// SessionID drives OAuth attribution.
	SessionID  string
	FirstParty bool
	WebSearch  config.WebSearchConfig
}

// Anthropic is the claude provider adapter.
type Anthropic struct {
	mu      sync.RWMutex
	model   string
	options AnthropicOptions
}

// NewAnthropic builds the claude adapter.
func NewAnthropic(model string, options AnthropicOptions) *Anthropic {
	return &Anthropic{model: model, options: options}
}

// Name implements Provider.
func (a *Anthropic) Name() string { return "claude" }

// Model implements Provider.
func (a *Anthropic) Model() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.model
}

// SetModel implements Provider; the claude catalog is open (model strings are
// not restricted), matching upstream's no-op validation.
func (a *Anthropic) SetModel(model string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	return nil
}

// AvailableModels implements Provider.
func (a *Anthropic) AvailableModels() []string { return anthropic.AllClaudeModels }

// SupportsImageInput implements Provider.
func (a *Anthropic) SupportsImageInput() bool { return true }

// SupportsCompaction implements Provider.
func (a *Anthropic) SupportsCompaction() bool { return true }

// Fork implements Provider.
func (a *Anthropic) Fork() Provider {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return NewAnthropic(a.model, a.options)
}

// InvalidateCredentials implements Provider (no cached state in the adapter).
func (a *Anthropic) InvalidateCredentials() {}

// Complete implements Provider.
func (a *Anthropic) Complete(turn TurnRequest, emit func(types.StreamEvent) error) error {
	a.mu.RLock()
	model := a.model
	options := a.options
	a.mu.RUnlock()
	if turn.ResumeSessionID == "" {
		turn.ResumeSessionID = options.SessionID
	}
	providerOptions := anthropic.ProviderOptions{
		Messages:          turn.Messages,
		Tools:             turn.Tools,
		System:            turn.System,
		DynamicSystem:     turn.DynamicSystem,
		Model:             model,
		SessionID:         turn.ResumeSessionID,
		FirstParty:        options.FirstParty,
		WebSearch:         options.WebSearch,
		MaxTokensOverride: turn.MaxTokensOverride,
	}
	settings := options.RetrySettings
	settings.MaxTokensOverride = turn.MaxTokensOverride
	settings.ShowThinking = turn.ShowThinking
	settings.Effort = turn.Effort
	return anthropic.Complete(context.Background(), options.CompleteConfig,
		providerOptions, settings, emit)
}

// OpenAIOptions assembles the codex (OpenAI) provider.
type OpenAIOptions struct {
	// Credentials resolve per turn (fresh refresh on demand).
	Credentials func() (*openai.CodexCredentials, error)
	WebSearch   config.WebSearchConfig
	// Model controls effort defaults and hosted-tool gating.
	ReasoningEffort *string
	ServiceTier     *string
	PromptCacheKey  *string
}

// OpenAI is the codex provider adapter.
type OpenAI struct {
	mu      sync.RWMutex
	model   string
	options OpenAIOptions
}

// NewOpenAI builds the codex adapter.
func NewOpenAI(model string, options OpenAIOptions) *OpenAI {
	return &OpenAI{model: model, options: options}
}

// Name implements Provider.
func (o *OpenAI) Name() string { return "codex" }

// Model implements Provider.
func (o *OpenAI) Model() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.model
}

// SetModel implements Provider.
func (o *OpenAI) SetModel(model string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.model = model
	return nil
}

// AvailableModels implements Provider (static catalog arrives with the model
// selection work after M4; the runtime accepts any model string).
func (o *OpenAI) AvailableModels() []string { return nil }

// SupportsImageInput implements Provider.
func (o *OpenAI) SupportsImageInput() bool { return true }

// SupportsCompaction implements Provider.
func (o *OpenAI) SupportsCompaction() bool { return true }

// Fork implements Provider.
func (o *OpenAI) Fork() Provider {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return NewOpenAI(o.model, o.options)
}

// InvalidateCredentials implements Provider.
func (o *OpenAI) InvalidateCredentials() {}

// Complete implements Provider.
func (o *OpenAI) Complete(turn TurnRequest, emit func(types.StreamEvent) error) error {
	o.mu.RLock()
	model := o.model
	options := o.options
	o.mu.RUnlock()
	credentials, err := options.Credentials()
	if err != nil {
		return err
	}
	completeOptions := openai.CompleteOptions{
		Model:           model,
		Credentials:     credentials,
		System:          turn.System,
		Input:           openai.BuildResponsesInput(turn.Messages),
		Tools:           turn.Tools,
		WebSearch:       options.WebSearch,
		ReasoningEffort: options.ReasoningEffort,
		ServiceTier:     options.ServiceTier,
		PromptCacheKey:  options.PromptCacheKey,
		Emit:            emit,
	}
	if turn.ResumeSessionID != "" {
		key := turn.ResumeSessionID
		completeOptions.PromptCacheKey = &key
	}
	if turn.MaxTokensOverride != nil {
		budget := uint32(*turn.MaxTokensOverride)
		completeOptions.MaxOutputTokens = &budget
	}
	return openai.StreamComplete(completeOptions)
}

// GeminiOptions assembles the gemini provider.
type GeminiOptions struct {
	Client *gemini.Client
}

// Gemini is the gemini provider adapter.
type Gemini struct {
	mu      sync.RWMutex
	model   string
	client  *gemini.Client
	options GeminiOptions
}

// NewGemini builds the gemini adapter with the default model.
func NewGemini(model string, options GeminiOptions) *Gemini {
	if model == "" {
		model = gemini.DefaultModel
	}
	client := options.Client
	if client == nil {
		client = gemini.NewClient()
		client.SeedCachedCatalog()
	}
	return &Gemini{model: model, client: client, options: GeminiOptions{Client: client}}
}

// Name implements Provider.
func (g *Gemini) Name() string { return "gemini" }

// Model implements Provider.
func (g *Gemini) Model() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.model
}

// SetModel implements Provider; any gemini-* id is accepted (discovered
// catalogs extend the static list).
func (g *Gemini) SetModel(model string) error {
	if !gemini.IsGeminiModelID(model) {
		return ErrUnknownModel
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.model = model
	return nil
}

// AvailableModels implements Provider.
func (g *Gemini) AvailableModels() []string {
	return append([]string(nil), gemini.AvailableModels...)
}

// DiscoveredModels lists models found via ListModels/loadCodeAssist.
func (g *Gemini) DiscoveredModels() []string { return g.client.FetchedModels() }

// SupportsImageInput implements Provider.
func (g *Gemini) SupportsImageInput() bool { return true }

// SupportsCompaction implements Provider.
func (g *Gemini) SupportsCompaction() bool { return false }

// Fork implements Provider.
func (g *Gemini) Fork() Provider {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return &Gemini{model: g.model, client: g.client,
		options: GeminiOptions{Client: g.client}}
}

// InvalidateCredentials drops the cached Code Assist state so the next turn
// re-runs the handshake.
func (g *Gemini) InvalidateCredentials() { g.client.InvalidateState() }

// Complete implements Provider.
func (g *Gemini) Complete(turn TurnRequest, emit func(types.StreamEvent) error) error {
	g.mu.RLock()
	model := g.model
	g.mu.RUnlock()
	return g.client.Complete(gemini.CompleteOptions{
		Model:           model,
		Messages:        turn.Messages,
		Tools:           turn.Tools,
		System:          turn.System,
		ResumeSessionID: turn.ResumeSessionID,
		Emit:            emit,
		OnModelSwitch: func(switched string) {
			_ = g.SetModel(switched)
		},
	})
}
