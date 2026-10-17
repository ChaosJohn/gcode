package anthropic

import (
	"encoding/json"
	"strconv"

	"github.com/ChaosJohn/gcode/internal/types"
)

// Wire types for the Anthropic Messages API request. Field order in the Go
// structs matches serde's emission order; enums are ordered via jsonBuilder
// where needed.

// CacheControlParam is the prompt-caching breakpoint marker.
type CacheControlParam struct {
	Kind string  `json:"type"`
	TTL  *string `json:"ttl,omitempty"`
}

// Ephemeral builds the standard 5m marker.
func Ephemeral() CacheControlParam { return CacheControlParam{Kind: "ephemeral"} }

// EphemeralTTL builds the marker with an explicit ttl ("1h").
func EphemeralTTL(ttl string) CacheControlParam {
	return CacheControlParam{Kind: "ephemeral", TTL: &ttl}
}

// EphemeralFor picks the ttl per provider config (anthropic_cache_ttl_1h).
func EphemeralFor(ttl1h bool) CacheControlParam {
	if ttl1h {
		return EphemeralTTL("1h")
	}
	return Ephemeral()
}

// ApiImageSource is a base64 image payload.
type ApiImageSource struct {
	Kind      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ToolResultContentBlock is one block inside a blocks-form tool_result.
type ToolResultContentBlock struct {
	Type      string          `json:"type"`
	Text      *string         `json:"text,omitempty"`
	Source    *ApiImageSource `json:"-"`
	ToolName  *string         `json:"-"`
	rawImage  []byte
	rawRefKey string
}

// MarshalJSON emits text/image/tool_reference with exact serde shapes.
func (b ToolResultContentBlock) MarshalJSON() ([]byte, error) {
	bld := &jsonBuilder{}
	bld.strKey("type", b.Type)
	switch b.Type {
	case "text":
		bld.rawKey("text", jsonStringPtr(b.Text))
	case "image":
		if b.Source != nil {
			if src, err := MarshalOrdered(*b.Source); err == nil {
				bld.rawKey("source", string(src))
			}
		}
	case "tool_reference":
		bld.rawKey("tool_name", jsonStringPtr(b.ToolName))
	}
	return bld.bytes(), nil
}

// TextResultBlock builds a text content block of a tool_result.
func TextResultBlock(text string) ToolResultContentBlock {
	return ToolResultContentBlock{Type: "text", Text: &text}
}

// ImageResultBlock builds an image content block of a tool_result.
func ImageResultBlock(source ApiImageSource) ToolResultContentBlock {
	return ToolResultContentBlock{Type: "image", Source: &source}
}

// ToolReferenceBlock builds a deferred-tool reference of a tool_result.
func ToolReferenceBlock(name string) ToolResultContentBlock {
	return ToolResultContentBlock{Type: "tool_reference", ToolName: &name}
}

// ToolResultContent is either a plain string or a block list (serde untagged).
type ToolResultContent struct {
	Text   *string
	Blocks []ToolResultContentBlock
}

// MarshalJSON renders the untagged union.
func (c ToolResultContent) MarshalJSON() ([]byte, error) {
	if c.Text != nil {
		return json.Marshal(*c.Text)
	}
	if c.Blocks != nil {
		return MarshalOrdered(c.Blocks)
	}
	return []byte(`""`), nil
}

// ContentBlock is one Anthropic API content block. Raw carries server-tool
// blocks verbatim (serde untagged Value).
type ContentBlock interface{ blockKind() string }

// TextContent is a text block.
type TextContent struct {
	Text         string
	CacheControl *CacheControlParam
}

// ToolUseContent is a client tool call.
type ToolUseContent struct {
	ID           string
	Name         string
	Input        json.RawMessage
	CacheControl *CacheControlParam
}

// ToolResultContentBlockMsg is a tool_result block.
type ToolResultContentBlockMsg struct {
	ToolUseID string
	Content   ToolResultContent
	IsError   bool
}

// ThinkingContent is a signed reasoning block.
type ThinkingContent struct {
	Thinking  string
	Signature string
}

// ImageContent is a standalone image block.
type ImageContent struct{ Source ApiImageSource }

// RawContent is a provider-native block sent verbatim.
type RawContent struct{ Item json.RawMessage }

func (TextContent) blockKind() string               { return "text" }
func (ToolUseContent) blockKind() string            { return "tool_use" }
func (ToolResultContentBlockMsg) blockKind() string { return "tool_result" }
func (ThinkingContent) blockKind() string           { return "thinking" }
func (ImageContent) blockKind() string              { return "image" }
func (RawContent) blockKind() string                { return "raw" }

// MarshalJSON emits the tagged union with serde field order.
func marshalBlock(b ContentBlock) ([]byte, error) {
	bld := &jsonBuilder{}
	switch v := b.(type) {
	case TextContent:
		bld.strKey("type", "text")
		bld.strKey("text", v.Text)
		if v.CacheControl != nil {
			bld.cache("cache_control", v.CacheControl)
		}
	case ToolUseContent:
		bld.strKey("type", "tool_use")
		bld.strKey("id", v.ID)
		bld.strKey("name", v.Name)
		bld.rawKey("input", rawOrObject(v.Input))
		if v.CacheControl != nil {
			bld.cache("cache_control", v.CacheControl)
		}
	case ToolResultContentBlockMsg:
		bld.strKey("type", "tool_result")
		bld.strKey("tool_use_id", v.ToolUseID)
		if content, err := MarshalOrdered(v.Content); err != nil {
			return nil, err
		} else {
			bld.rawKey("content", string(content))
		}
		if v.IsError {
			bld.rawKey("is_error", "true")
		}
	case ThinkingContent:
		bld.strKey("type", "thinking")
		bld.strKey("thinking", v.Thinking)
		bld.strKey("signature", v.Signature)
	case ImageContent:
		bld.strKey("type", "image")
		if src, err := MarshalOrdered(v.Source); err != nil {
			return nil, err
		} else {
			bld.rawKey("source", string(src))
		}
	case RawContent:
		return v.Item, nil
	default:
		bld.strKey("type", "text")
		bld.strKey("text", "")
	}
	return bld.bytes(), nil
}

func rawOrObject(raw json.RawMessage) string {
	var probe any
	if len(raw) > 0 && json.Unmarshal(raw, &probe) == nil {
		if _, ok := probe.(map[string]any); ok {
			return string(raw)
		}
	}
	return "{}"
}

// ApiMessage is one request message.
type ApiMessage struct {
	Role    string
	Content []ContentBlock
}

// MarshalJSON emits role/content in order; content blocks go through
// marshalBlock (the concrete block types have no json methods).
func (m ApiMessage) MarshalJSON() ([]byte, error) {
	bld := &jsonBuilder{}
	bld.strKey("role", m.Role)
	parts := make([]string, 0, len(m.Content))
	for _, block := range m.Content {
		encoded, err := marshalBlock(block)
		if err != nil {
			return nil, err
		}
		parts = append(parts, string(encoded))
	}
	bld.rawKey("content", "["+joinStrings(parts, ",")+"]")
	return bld.bytes(), nil
}

// ApiTool is a client tool definition.
type ApiTool struct {
	Name         string
	Description  string
	InputSchema  json.RawMessage
	CacheControl *CacheControlParam
	DeferLoading bool
}

// MarshalJSON emits the serde shape (defer_loading only when true).
func (t ApiTool) MarshalJSON() ([]byte, error) {
	bld := &jsonBuilder{}
	bld.strKey("name", t.Name)
	bld.strKey("description", t.Description)
	bld.rawKey("input_schema", rawOrNullJSON(t.InputSchema))
	if t.CacheControl != nil {
		bld.cache("cache_control", t.CacheControl)
	}
	if t.DeferLoading {
		bld.rawKey("defer_loading", "true")
	}
	return bld.bytes(), nil
}

// ApiToolParam is a client tool or a verbatim server tool.
type ApiToolParam struct {
	Custom *ApiTool
	Server json.RawMessage
}

// MarshalJSON emits the untagged union.
func (p ApiToolParam) MarshalJSON() ([]byte, error) {
	if p.Custom != nil {
		return MarshalOrdered(*p.Custom)
	}
	return p.Server, nil
}

// ApiSystemBlock is one system text block.
type ApiSystemBlock struct {
	BlockType    string
	Text         string
	CacheControl *CacheControlParam
}

// MarshalJSON emits the block.
func (b ApiSystemBlock) MarshalJSON() ([]byte, error) {
	bld := &jsonBuilder{}
	bld.strKey("type", b.BlockType)
	bld.strKey("text", b.Text)
	if b.CacheControl != nil {
		bld.cache("cache_control", b.CacheControl)
	}
	return bld.bytes(), nil
}

// ApiMetadata carries the OAuth user id.
type ApiMetadata struct{ UserID string }

// MarshalJSON emits metadata.
func (m ApiMetadata) MarshalJSON() ([]byte, error) {
	bld := &jsonBuilder{}
	bld.strKey("user_id", m.UserID)
	return bld.bytes(), nil
}

// ApiThinking is the thinking request param (tagged union).
type ApiThinking struct {
	// Adaptive thinking: display + optional block binding.
	Adaptive     bool
	Display      string // "" = omit
	BlockBinding bool   // prefix_mismatch_behavior: drop_block
	BudgetTokens int    // Enabled form
	IsEnabled    bool   // false = Adaptive, true = Enabled
}

// MarshalJSON emits the tagged union.
func (t ApiThinking) MarshalJSON() ([]byte, error) {
	bld := &jsonBuilder{}
	if t.IsEnabled {
		bld.strKey("type", "enabled")
		bld.rawKey("budget_tokens", itoa(t.BudgetTokens))
		return bld.bytes(), nil
	}
	bld.strKey("type", "adaptive")
	if t.Display != "" {
		bld.strKey("display", t.Display)
	}
	if t.BlockBinding {
		bld.rawKey("block_binding", `{"prefix_mismatch_behavior":"drop_block"}`)
	}
	return bld.bytes(), nil
}

// ApiOutputConfig carries the effort level.
type ApiOutputConfig struct{ Effort string }

// MarshalJSON emits output_config.
func (c ApiOutputConfig) MarshalJSON() ([]byte, error) {
	bld := &jsonBuilder{}
	bld.strKey("effort", c.Effort)
	return bld.bytes(), nil
}

// ApiSystem is the system param (blocks form).
type ApiSystem []ApiSystemBlock

// MarshalJSON emits the blocks array element-wise (a marshaler must never
// hand its own type back to encoding/json — that recurses).
func (s ApiSystem) MarshalJSON() ([]byte, error) {
	parts := make([]string, 0, len(s))
	for _, block := range s {
		encoded, err := MarshalOrdered(block)
		if err != nil {
			return nil, err
		}
		parts = append(parts, string(encoded))
	}
	return []byte("[" + joinStrings(parts, ",") + "]"), nil
}

// ApiRequest is the full Messages-API request body.
type ApiRequest struct {
	Model        string
	MaxTokens    int
	System       ApiSystem
	Messages     []ApiMessage
	Tools        []ApiToolParam
	Metadata     *ApiMetadata
	Thinking     *ApiThinking
	OutputConfig *ApiOutputConfig
	Temperature  *float32
	ServiceTier  string
	Stream       bool
}

// MarshalJSON emits the request with all skips.
func (r ApiRequest) MarshalJSON() ([]byte, error) {
	bld := &jsonBuilder{}
	bld.strKey("model", r.Model)
	bld.rawKey("max_tokens", itoa(r.MaxTokens))
	if r.System != nil {
		if sys, err := MarshalOrdered(r.System); err != nil {
			return nil, err
		} else {
			bld.rawKey("system", string(sys))
		}
	}
	{
		parts := make([]string, 0, len(r.Messages))
		for _, msg := range r.Messages {
			encoded, err := MarshalOrdered(msg)
			if err != nil {
				return nil, err
			}
			parts = append(parts, string(encoded))
		}
		bld.rawKey("messages", "["+joinStrings(parts, ",")+"]")
	}
	if r.Tools != nil {
		parts := make([]string, 0, len(r.Tools))
		for _, param := range r.Tools {
			encoded, err := MarshalOrdered(param)
			if err != nil {
				return nil, err
			}
			parts = append(parts, string(encoded))
		}
		bld.rawKey("tools", "["+joinStrings(parts, ",")+"]")
	}
	if r.Metadata != nil {
		if meta, err := MarshalOrdered(*r.Metadata); err != nil {
			return nil, err
		} else {
			bld.rawKey("metadata", string(meta))
		}
	}
	if r.Thinking != nil {
		if thinking, err := MarshalOrdered(*r.Thinking); err != nil {
			return nil, err
		} else {
			bld.rawKey("thinking", string(thinking))
		}
	}
	if r.OutputConfig != nil {
		if cfg, err := MarshalOrdered(*r.OutputConfig); err != nil {
			return nil, err
		} else {
			bld.rawKey("output_config", string(cfg))
		}
	}
	if r.Temperature != nil {
		bld.rawKey("temperature", f32String(*r.Temperature))
	}
	if r.ServiceTier != "" {
		bld.strKey("service_tier", r.ServiceTier)
	}
	bld.rawKey("stream", boolString(r.Stream))
	return bld.bytes(), nil
}

// ---- ordered-encoding helpers ----

// jsonBuilder assembles a JSON object with a fixed key order (mirrors the
// types package builder; kept local to avoid exporting encoder internals).
type jsonBuilder struct{ parts []string }

func (b *jsonBuilder) rawKey(key, rawJSON string) {
	b.parts = append(b.parts, `"`+key+`":`+rawJSON)
}

func (b *jsonBuilder) strKey(key, value string) {
	encoded, _ := types.MarshalCompact(value)
	b.parts = append(b.parts, `"`+key+`":`+string(encoded))
}

func (b *jsonBuilder) cache(key string, cc *CacheControlParam) {
	encoded, _ := types.MarshalCompact(cc)
	b.rawKey(key, string(encoded))
}

func (b *jsonBuilder) bytes() []byte {
	return []byte("{" + joinStrings(b.parts, ",") + "}")
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// MarshalOrdered encodes v without HTML escaping (serde_json parity).
func MarshalOrdered(v any) ([]byte, error) { return types.MarshalCompact(v) }

func jsonStringPtr(s *string) string {
	if s == nil {
		return "null"
	}
	out, _ := types.MarshalCompact(*s)
	return string(out)
}

func rawOrNullJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	return string(raw)
}

func itoa(n int) string { return strconv.Itoa(n) }

func f32String(f float32) string { return strconv.FormatFloat(float64(f), 'g', -1, 32) }

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
