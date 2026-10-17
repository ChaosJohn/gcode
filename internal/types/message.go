package types

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Role in conversation. Serialized lowercase ("user"/"assistant"), matching
// serde rename_all = "lowercase" on the Rust enum; unknown values are a
// hard error, also matching serde.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

var _ json.Unmarshaler = (*Role)(nil)

func (r *Role) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch Role(s) {
	case RoleUser, RoleAssistant:
		*r = Role(s)
		return nil
	}
	return fmt.Errorf("unknown message role %q", s)
}

// CacheControl carries Anthropic-style prompt-cache breakpoints.
type CacheControl struct {
	// Kind is the "type" field, always "ephemeral" in practice.
	Kind string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// NewEphemeralCacheControl builds an ephemeral cache-control marker.
func NewEphemeralCacheControl(ttl string) *CacheControl {
	return &CacheControl{Kind: "ephemeral", TTL: ttl}
}

// Block type discriminators, mirroring the serde tag = "type",
// rename_all = "snake_case" content-block enum.
const (
	BlockText             = "text"
	BlockReasoning        = "reasoning"
	BlockReasoningTrace   = "reasoning_trace"
	BlockAnthropicThink   = "anthropic_thinking"
	BlockOpenAIReasoning  = "openai_reasoning"
	BlockToolUse          = "tool_use"
	BlockToolResult       = "tool_result"
	BlockImage            = "image"
	BlockOpenAICompaction = "openai_compaction"
	BlockToolReference    = "tool_reference"
	BlockProviderNative   = "provider_native"
)

// ContentBlock is the flat Go representation of jcode's tagged
// ContentBlock enum: a "type" discriminator plus the variant's fields.
//
// Field declaration order matches the serde declaration order of every
// variant, so re-serialization is byte-compatible with jcode for fields the
// variant actually carries (unknown fields are ignored on both sides, and an
// unknown "type" is an error on both sides — serde has no #[serde(other)]
// fallback on this enum).
//
// Input/Item hold raw JSON (serde_json::Value / raw_value passthrough) and
// serialize as null when absent, exactly like serde_json::Value.
//
// The union is intentionally flat rather than a sealed interface: parity of
// the persisted JSON shape is the contract, and helpers below provide typed
// accessors where the agent loop needs them.
type ContentBlock struct {
	Type string `json:"type"`

	// text / reasoning / reasoning_trace
	Text         *string       `json:"text,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`

	// anthropic_thinking
	Thinking  *string `json:"thinking,omitempty"`
	Signature *string `json:"signature,omitempty"`

	// openai_reasoning / openai_compaction
	ID               *string  `json:"id,omitempty"`
	Summary          []string `json:"summary"`
	EncryptedContent *string  `json:"encrypted_content,omitempty"`
	Status           *string  `json:"status,omitempty"`

	// tool_use
	Name             *string         `json:"name,omitempty"`
	Input            json.RawMessage `json:"input,omitempty"`
	ThoughtSignature *string         `json:"thought_signature,omitempty"`

	// tool_result
	ToolUseID *string `json:"tool_use_id,omitempty"`
	Content   *string `json:"content,omitempty"`
	IsError   *bool   `json:"is_error,omitempty"`

	// image
	MediaType *string `json:"media_type,omitempty"`
	Data      *string `json:"data,omitempty"`

	// tool_reference
	ToolName *string `json:"tool_name,omitempty"`

	// provider_native
	Provider *string         `json:"provider,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
}

var knownBlockTypes = map[string]bool{
	BlockText: true, BlockReasoning: true, BlockReasoningTrace: true,
	BlockAnthropicThink: true, BlockOpenAIReasoning: true,
	BlockToolUse: true, BlockToolResult: true, BlockImage: true,
	BlockOpenAICompaction: true, BlockToolReference: true, BlockProviderNative: true,
}

var _ json.Marshaler = ContentBlock{}
var _ json.Unmarshaler = (*ContentBlock)(nil)

// contentBlockJSON is the method-free twin used for shadow decoding.
type contentBlockJSON ContentBlock

// MarshalJSON emits exactly the fields the variant carries, in serde
// declaration order (type first). Plain Vec/Value fields without skip —
// openai_reasoning.summary, tool_use.input, provider_native.item — are
// always emitted ([] / null when empty), while Option fields are emitted
// only when set.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	bld := &jsonBuilder{}
	bld.strKey("type", b.Type)
	switch b.Type {
	case BlockText, BlockReasoning, BlockReasoningTrace:
		bld.rawKey("text", jsonStringPtr(b.Text))
		if b.Type == BlockText && b.CacheControl != nil {
			if cc, err := MarshalCompact(b.CacheControl); err == nil {
				bld.rawKey("cache_control", string(cc))
			}
		}
	case BlockAnthropicThink:
		bld.rawKey("thinking", jsonStringPtr(b.Thinking))
		bld.rawKey("signature", jsonStringPtr(b.Signature))
	case BlockOpenAIReasoning:
		// id/encrypted_content/status are Options with skip; summary is a
		// plain Vec and is always emitted ([] when empty).
		if b.ID != nil {
			bld.strKey("id", *b.ID)
		}
		summary, err := MarshalCompact(b.Summary)
		if err != nil {
			return nil, err
		}
		bld.rawKey("summary", string(summary))
		if b.EncryptedContent != nil {
			bld.strKey("encrypted_content", *b.EncryptedContent)
		}
		if b.Status != nil {
			bld.strKey("status", *b.Status)
		}
	case BlockToolUse:
		bld.rawKey("id", jsonStringPtr(b.ID))
		bld.rawKey("name", jsonStringPtr(b.Name))
		bld.rawKey("input", rawOrNull(b.Input))
		// thought_signature is an Option with skip: absent stays absent.
		if b.ThoughtSignature != nil {
			bld.strKey("thought_signature", *b.ThoughtSignature)
		}
	case BlockToolResult:
		bld.rawKey("tool_use_id", jsonStringPtr(b.ToolUseID))
		bld.rawKey("content", jsonStringPtr(b.Content))
		// is_error: Option with skip; upstream only ever sets Some(true).
		if b.IsError != nil && *b.IsError {
			bld.rawKey("is_error", "true")
		}
	case BlockImage:
		bld.rawKey("media_type", jsonStringPtr(b.MediaType))
		bld.rawKey("data", jsonStringPtr(b.Data))
	case BlockOpenAICompaction:
		bld.rawKey("encrypted_content", jsonStringPtr(b.EncryptedContent))
	case BlockToolReference:
		bld.rawKey("tool_use_id", jsonStringPtr(b.ToolUseID))
		bld.rawKey("tool_name", jsonStringPtr(b.ToolName))
	case BlockProviderNative:
		bld.rawKey("provider", jsonStringPtr(b.Provider))
		bld.rawKey("item", rawOrNull(b.Item))
	default:
		return nil, fmt.Errorf("unknown content block type %q", b.Type)
	}
	return bld.bytes(), nil
}

func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	type shadow contentBlockJSON
	var s shadow
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if !knownBlockTypes[s.Type] {
		return fmt.Errorf("unknown content block type %q", s.Type)
	}
	*b = ContentBlock(s)
	// serde always emits summary (plain Vec, no skip): nil must round-trip
	// as [] rather than null.
	if b.Summary == nil {
		b.Summary = []string{}
	}
	return nil
}

// AsText returns the text payload of text/reasoning/reasoning_trace blocks.
func (b *ContentBlock) AsText() (string, bool) {
	if b.Text == nil {
		return "", false
	}
	return *b.Text, true
}

// AsToolUse returns (id, name, input) for tool_use blocks.
func (b *ContentBlock) AsToolUse() (id, name string, input json.RawMessage, ok bool) {
	if b.Type != BlockToolUse || b.ID == nil || b.Name == nil {
		return "", "", nil, false
	}
	return *b.ID, *b.Name, b.Input, true
}

// AsToolResult returns (tool_use_id, content, is_error) for tool_result blocks.
func (b *ContentBlock) AsToolResult() (toolUseID, content string, isErr bool, ok bool) {
	if b.Type != BlockToolResult || b.ToolUseID == nil || b.Content == nil {
		return "", "", false, false
	}
	return *b.ToolUseID, *b.Content, b.IsError != nil && *b.IsError, true
}

// Convenience constructors mirroring jcode's helpers.

// TextBlock builds a text block.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockText, Text: &text}
}

// ImageBlock builds an image block.
func ImageBlock(mediaType, data string) ContentBlock {
	return ContentBlock{Type: BlockImage, MediaType: &mediaType, Data: &data}
}

// ToolResultBlock builds a tool_result block; error results carry is_error
// (jcode only sets it to Some(true), never Some(false)).
func ToolResultBlock(toolUseID, content string, isErr bool) ContentBlock {
	b := ContentBlock{Type: BlockToolResult, ToolUseID: &toolUseID, Content: &content}
	if isErr {
		t := true
		b.IsError = &t
	}
	return b
}

// Message is one conversation message (jcode Message).
type Message struct {
	Role           Role           `json:"role"`
	Content        []ContentBlock `json:"content"`
	Timestamp      *JTime         `json:"timestamp,omitempty"`
	ToolDurationMs *uint64        `json:"tool_duration_ms,omitempty"`
}

// NewUserMessage mirrors Message::user.
func NewUserMessage(text string) Message {
	ts := Now()
	return Message{Role: RoleUser, Content: []ContentBlock{TextBlock(text)}, Timestamp: &ts}
}

// NewAssistantTextMessage mirrors Message::assistant_text.
func NewAssistantTextMessage(text string) Message {
	ts := Now()
	return Message{Role: RoleAssistant, Content: []ContentBlock{TextBlock(text)}, Timestamp: &ts}
}

// NewToolResultMessage mirrors Message::tool_result.
func NewToolResultMessage(toolUseID, content string, isErr bool, durationMs *uint64) Message {
	ts := Now()
	return Message{
		Role:           RoleUser,
		Content:        []ContentBlock{ToolResultBlock(toolUseID, content, isErr)},
		Timestamp:      &ts,
		ToolDurationMs: durationMs,
	}
}

// FormatTimestamp renders a timestamp deterministically in UTC for injection
// into model-visible content (millisecond precision, Z suffix).
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// ToolCall is a completed tool call (jcode ToolCall).
type ToolCall struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Input            json.RawMessage `json:"input"`
	Intent           *string         `json:"intent,omitempty"`
	ThoughtSignature *string         `json:"thought_signature,omitempty"`
}

// ToToolUseBlock builds the persisted tool_use ContentBlock for this call,
// preserving thought_signature (upstream regression #gemini-3).
func (c ToolCall) ToToolUseBlock() ContentBlock {
	name := c.Name
	id := c.ID
	in := c.Input
	if in == nil {
		in = json.RawMessage("null")
	}
	return ContentBlock{
		Type: BlockToolUse, ID: &id, Name: &name, Input: in,
		ThoughtSignature: c.ThoughtSignature,
	}
}

// NormalizeInputToObject mirrors ToolCall::normalize_input_to_object:
// non-object inputs collapse to {}.
func NormalizeInputToObject(input json.RawMessage) json.RawMessage {
	var v any
	if len(input) > 0 && json.Unmarshal(input, &v) == nil {
		if _, isObj := v.(map[string]any); isObj {
			return input
		}
	}
	return json.RawMessage("{}")
}

// ParseStreamedInputToObject mirrors ToolCall::parse_streamed_input_to_object:
// empty input → {}, unparseable or non-object input is kept verbatim (null,
// numbers, arrays, strings) so malformed calls surface as validation errors
// instead of silently running with missing arguments.
func ParseStreamedInputToObject(input string) json.RawMessage {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return json.RawMessage("{}")
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(trimmed)
}

// ValidationError mirrors ToolCall::validation_error.
func (c ToolCall) ValidationError() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("Invalid tool call: tool name must not be empty.")
	}
	if !isObjectJSON(c.Input) {
		return fmt.Errorf("Invalid tool call for '%s': arguments must be a JSON object, got %s.",
			c.Name, jsonValueKind(c.Input))
	}
	return nil
}

// IntentFromInput extracts the shared per-call "intent" property.
func IntentFromInput(input json.RawMessage) (string, bool) {
	var obj map[string]json.RawMessage
	if len(input) == 0 || json.Unmarshal(input, &obj) != nil {
		return "", false
	}
	raw, ok := obj["intent"]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

// RefreshIntentFromInput re-derives c.Intent from c.Input.
func (c *ToolCall) RefreshIntentFromInput() {
	if intent, ok := IntentFromInput(c.Input); ok {
		c.Intent = &intent
	} else {
		c.Intent = nil
	}
}

// ToolDefinition is advertised to model providers (serialize-only upstream).
type ToolDefinition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	DeferLoading bool            `json:"defer_loading,omitempty"`
}

const approxCharsPerToken = 4

// EstimateTokens mirrors the jcode chars/4 heuristic.
func EstimateTokens(s string) int { return len([]byte(s)) / approxCharsPerToken }

// DescriptionTokenEstimate approximates the prompt-token cost of the description.
func (d ToolDefinition) DescriptionTokenEstimate() int {
	return EstimateTokens(d.Description)
}

// PromptTokenEstimate approximates the cost of the full definition payload.
func (d ToolDefinition) PromptTokenEstimate() int {
	payload, _ := json.Marshal(struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}{d.Name, d.Description, d.InputSchema})
	return EstimateTokens(string(payload))
}

// InputShellResult mirrors the persisted shape of a bash-style tool result.
type InputShellResult struct {
	Command       string  `json:"command"`
	Cwd           *string `json:"cwd,omitempty"`
	Output        string  `json:"output"`
	ExitCode      *int32  `json:"exit_code,omitempty"`
	DurationMs    uint64  `json:"duration_ms"`
	Truncated     bool    `json:"truncated"`
	FailedToStart bool    `json:"failed_to_start"`
}

// --- helpers ---------------------------------------------------------------

func isObjectJSON(raw json.RawMessage) bool {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return false
	}
	_, ok := v.(map[string]any)
	return ok
}

func jsonValueKind(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return "null"
	}
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "null"
}

// SanitizeToolID makes an id match ^[a-zA-Z0-9_-]+$ (provider switch safety).
func SanitizeToolID(id string) string {
	if id == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
