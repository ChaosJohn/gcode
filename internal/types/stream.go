package types

import "encoding/json"

// StreamEvent is the in-memory streaming event contract between providers
// and the agent loop (jcode StreamEvent enum). It is never serialized, so
// the Go port uses a sealed interface with concrete event structs instead
// of a JSON-shaped union. Variant names mirror the Rust enum 1:1.
type StreamEvent interface{ streamEvent() }

// TextDelta is a text content delta.
type TextDelta struct{ Text string }

// TextSuffixReplace corrects a completed message using late provider
// metadata: replace an exact trailing match only.
type TextSuffixReplace struct{ OldText, NewText string }

// TextDone marks the end of an assistant text message within a response.
type TextDone struct{}

// ToolUseStart signals a tool use started.
type ToolUseStart struct{ ID, Name string }

// ToolInputDelta carries a JSON fragment of tool input.
type ToolInputDelta struct{ Delta string }

// ToolInputDeltaFor keys the fragment, so parallel calls stream independently.
type ToolInputDeltaFor struct{ ID, Delta string }

// ToolUseEnd completes a tool use.
type ToolUseEnd struct{}

// ToolUseEndFor completes a specific parallel call.
type ToolUseEndFor struct{ ID string }

// ToolUseSignature carries a Gemini-3 thought signature for the most recent
// tool call, emitted right after the matching start/end events.
type ToolUseSignature struct{ Signature string }

// ToolUseSignatureFor carries a signature for a specific (interleaved) call.
type ToolUseSignatureFor struct{ ID, Signature string }

// ToolResultEvent reports a provider-executed tool result.
type ToolResultEvent struct {
	ToolUseID string
	Content   string
	IsError   bool
}

// GeneratedImage reports a provider-native image generation result.
type GeneratedImage struct {
	ID            string
	Path          string
	MetadataPath  *string
	OutputFormat  string
	RevisedPrompt *string
}

// ThinkingStart marks extended-thinking start.
type ThinkingStart struct{}

// ThinkingDelta carries reasoning content.
type ThinkingDelta struct{ Delta string }

// ThinkingSignatureDelta carries the provider signature for the thinking block.
type ThinkingSignatureDelta struct{ Delta string }

// OpenAIReasoningItem is a native OpenAI Responses reasoning item for
// future-turn replay.
type OpenAIReasoningItem struct {
	ID               string
	Summary          []string
	EncryptedContent *string
	Status           *string
}

// ThinkingEnd marks extended-thinking end.
type ThinkingEnd struct{}

// ThinkingDone completes extended thinking with duration.
type ThinkingDone struct{ DurationSecs float64 }

// MessageEnd completes a message, possibly with a stop reason.
type MessageEnd struct{ StopReason *string }

// RetryRollback signals a transient transport fault; consumers must discard
// partial output for the current attempt before the replayed stream arrives.
type RetryRollback struct{ Attempt, Max uint32 }

// TokenUsageEvent reports token usage.
type TokenUsageEvent struct {
	InputTokens              *uint64
	OutputTokens             *uint64
	CacheReadInputTokens     *uint64
	CacheCreationInputTokens *uint64
}

// ConnectionTypeEvent reports the active transport/connection type.
type ConnectionTypeEvent struct{ Connection string }

// ConnectionPhase enumerates connection phases for status bar transparency.
type ConnectionPhase string

const (
	PhaseAuthenticating  ConnectionPhase = "authenticating"
	PhaseConnecting      ConnectionPhase = "connecting"
	PhaseSendingRequest  ConnectionPhase = "sending request"
	PhaseWaitingResponse ConnectionPhase = "waiting for response"
	PhaseStreaming       ConnectionPhase = "streaming"
	PhaseRetrying        ConnectionPhase = "retrying"
)

// ConnectionPhaseEvent reports a phase update; Retrying carries attempt/max.
type ConnectionPhaseEvent struct {
	Phase        ConnectionPhase
	RetryAttempt *uint32
	RetryMax     *uint32
}

// StatusDetail carries provider-supplied human-readable transport detail.
type StatusDetailEvent struct{ Detail string }

// ServiceTierEvent reports the effective tier, never inferred from config.
type ServiceTierEvent struct {
	Tier      string
	Requested *string
}

// ErrorEvent signals an error; RetryAfterSecs is set for rate limits.
type ErrorEvent struct {
	Message        string
	RetryAfterSecs *uint64
}

// SessionIDEvent reports the provider session id (for resume).
type SessionIDEvent struct{ ID string }

// CompactionEvent signals that context was summarized.
type CompactionEvent struct {
	Trigger                string
	PreTokens              *uint64
	OpenAIEncryptedContent *string
}

// UpstreamProviderEvent reports which provider OpenRouter routed to.
type UpstreamProviderEvent struct{ Provider string }

// NativeToolCall is a provider-bridge tool call that jcode must execute.
type NativeToolCall struct {
	RequestID string
	ToolName  string
	Input     json.RawMessage
}

// ProviderNativeEvent is a complete provider-native tool item (e.g. Anthropic
// server_tool_use), stored verbatim as a ProviderNative ContentBlock.
type ProviderNativeEvent struct {
	Provider string
	Item     json.RawMessage
}

func (TextDelta) streamEvent()              {}
func (TextSuffixReplace) streamEvent()      {}
func (TextDone) streamEvent()               {}
func (ToolUseStart) streamEvent()           {}
func (ToolInputDelta) streamEvent()         {}
func (ToolInputDeltaFor) streamEvent()      {}
func (ToolUseEnd) streamEvent()             {}
func (ToolUseEndFor) streamEvent()          {}
func (ToolUseSignature) streamEvent()       {}
func (ToolUseSignatureFor) streamEvent()    {}
func (ToolResultEvent) streamEvent()        {}
func (GeneratedImage) streamEvent()         {}
func (ThinkingStart) streamEvent()          {}
func (ThinkingDelta) streamEvent()          {}
func (ThinkingSignatureDelta) streamEvent() {}
func (OpenAIReasoningItem) streamEvent()    {}
func (ThinkingEnd) streamEvent()            {}
func (ThinkingDone) streamEvent()           {}
func (MessageEnd) streamEvent()             {}
func (RetryRollback) streamEvent()          {}
func (TokenUsageEvent) streamEvent()        {}
func (ConnectionTypeEvent) streamEvent()    {}
func (ConnectionPhaseEvent) streamEvent()   {}
func (StatusDetailEvent) streamEvent()      {}
func (ServiceTierEvent) streamEvent()       {}
func (ErrorEvent) streamEvent()             {}
func (SessionIDEvent) streamEvent()         {}
func (CompactionEvent) streamEvent()        {}
func (UpstreamProviderEvent) streamEvent()  {}
func (NativeToolCall) streamEvent()         {}
func (ProviderNativeEvent) streamEvent()    {}

// PhaseDisplay mirrors ConnectionPhase::Display for the status line.
func (p ConnectionPhase) PhaseDisplay() string { return string(p) }
