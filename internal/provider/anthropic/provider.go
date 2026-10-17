package anthropic

import (
	"context"

	"github.com/ChaosJohn/gcode/internal/base/config"
	"github.com/ChaosJohn/gcode/internal/types"
)

// Provider assembles the Anthropic request from a session transcript and
// drives the retrying stream (ported from AnthropicProvider::complete).

// ProviderOptions carries one completion request's inputs.
type ProviderOptions struct {
	Messages      []types.Message
	Tools         []types.ToolDefinition
	System        string
	DynamicSystem string
	Model         string
	MaxTokens     int // 0 = model default
	// SessionID drives OAuth attribution headers and metadata.device_id.
	SessionID string
	// FirstParty marks a direct api.anthropic.com path (server tools allowed).
	FirstParty bool
	// WebSearch configures the native web_search server tool.
	WebSearch         config.WebSearchConfig
	MaxTokensOverride *int
	ServiceTier       string
}

// CompleteConfig is the resolved request-time configuration.
type CompleteConfig struct {
	IsOAuth    bool
	CacheTTL1h bool
	Transport  TransportConfig
	// ResolveToken returns the current access token (OAuth) or API key.
	ResolveToken func(forceRefresh bool) (string, error)
	// OnModelSwitch persists a fallback-model switch.
	OnModelSwitch func(string)
	// LoadCredentials resolves OAuth credentials when needed.
	LoadCredentials func() (ClaudeCredentials, error)
}

// Complete streams one assistant turn, emitting StreamEvents.
func Complete(ctx context.Context, cfg CompleteConfig, opts ProviderOptions, settings RetrySettings, emit EmitFunc) error {
	isOAuth := cfg.IsOAuth

	// Server tools replace the local websearch tool when attached.
	serverTools := ServerToolsForRequest(opts.WebSearch, opts.FirstParty, opts.Tools)
	tools := WithoutLocalWebsearch(opts.Tools, serverTools)

	apiTools := FormatTools(tools, isOAuth, cfg.CacheTTL1h)
	messages := FormatMessagesWithNative(opts.Messages, isOAuth, apiTools, len(serverTools) > 0)

	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = MaxOutputTokens(opts.Model)
	}
	if opts.MaxTokensOverride != nil {
		maxTokens = *opts.MaxTokensOverride
	}
	thinking, outputConfig, temperature := BuildReasoningRequestPartsForBudget(
		opts.Model, isOAuth, settings.ShowThinking, settings.Effort, maxTokens)

	toolsParam := RequestTools(apiTools, serverTools)
	metadata := &ApiMetadata{}
	if isOAuth {
		*metadata = OAuthRequestMetadata(opts.SessionID)
	} else {
		metadata = nil
	}

	request := ApiRequest{
		Model:        StripLongContextSuffix(opts.Model),
		MaxTokens:    maxTokens,
		System:       BuildSystemParamSplit(opts.System, opts.DynamicSystem, isOAuth, cfg.CacheTTL1h),
		Messages:     messages,
		Tools:        toolsParam,
		Metadata:     metadata,
		Thinking:     thinking,
		OutputConfig: outputConfig,
		Temperature:  temperature,
		ServiceTier:  opts.ServiceTier,
		Stream:       true,
	}
	if request.ServiceTier != "" && !ModelSupportsPriorityServiceTier(opts.Model) {
		request.ServiceTier = ""
	}

	return RunStreamWithRetries(
		Options{
			Transport:     cfg.Transport,
			IsOAuth:       isOAuth,
			SessionID:     opts.SessionID,
			ModelName:     opts.Model,
			Ctx:           ctx,
			ResolveToken:  cfg.ResolveToken,
			OnModelSwitch: cfg.OnModelSwitch,
		},
		request, settings, emit)
}

// DefaultRetrySettings snapshots the settings for a fresh turn.
func DefaultRetrySettings(effort string, showThinking bool, maxTokensOverride *int, serviceTier string) RetrySettings {
	return RetrySettings{
		Effort: effort, ShowThinking: showThinking,
		MaxTokensOverride: maxTokensOverride, ServiceTier: serviceTier,
	}
}

// marshalDebugRequest renders a request for JCODE_ANTHROPIC_DEBUG-style logs.
func marshalDebugRequest(request ApiRequest) string {
	encoded, err := MarshalOrdered(request)
	if err != nil {
		return ""
	}
	return string(encoded)
}
