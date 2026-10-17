package toolcore

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
)

// Tool output shape (ported from jcode-tool-types ToolOutput).

// ToolImage is one image attached to a tool result.
type ToolImage struct {
	MediaType string  `json:"media_type"`
	Data      string  `json:"data"`
	Label     *string `json:"label,omitempty"`
}

// ToolOutput is a tool's rendered result.
type ToolOutput struct {
	Output   string          `json:"output"`
	Title    *string         `json:"title,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Images   []ToolImage     `json:"images,omitempty"`
}

// NewToolOutput builds a plain output.
func NewToolOutput(output string) ToolOutput { return ToolOutput{Output: output} }

// WithTitle attaches a display title.
func (o ToolOutput) WithTitle(title string) ToolOutput {
	o.Title = &title
	return o
}

// WithMetadata attaches structured metadata.
func (o ToolOutput) WithMetadata(metadata json.RawMessage) ToolOutput {
	o.Metadata = metadata
	return o
}

// WithImage attaches an image result.
func (o ToolOutput) WithImage(mediaType, data string) ToolOutput {
	o.Images = append(o.Images, ToolImage{MediaType: mediaType, Data: data})
	return o
}

// WithLabeledImage attaches a labeled image result.
func (o ToolOutput) WithLabeledImage(mediaType, data, label string) ToolOutput {
	o.Images = append(o.Images, ToolImage{MediaType: mediaType, Data: data, Label: &label})
	return o
}

// ResolveToolName maps provider aliases to canonical internal names (ported
// from jcode-tool-types resolve_tool_name).
func ResolveToolName(name string) string {
	// Some function-calling APIs expose a recipient such as `functions.bash`.
	if trimmed, ok := strings.CutPrefix(name, "functions."); ok {
		name = trimmed
	}
	switch name {
	case "communicate":
		return "swarm"
	case "task", "task_runner":
		return "subagent"
	case "launch":
		return "open"
	case "shell", "shell_exec":
		return "bash"
	case "read_file", "file_read":
		return "read"
	case "write_file", "file_write":
		return "write"
	case "edit_file", "file_edit":
		return "edit"
	// `multiedit` merged into `edit`; `patch` merged into `apply_patch`.
	case "multiedit", "MultiEdit", "multi_edit":
		return "edit"
	case "patch", "Patch":
		return "apply_patch"
	// The native grep tool became agentgrep; models still call `grep` and
	// OAuth's `file_grep`. agentgrep's grep mode accepts `pattern` as a
	// `query` alias, so these resolve and work as-is.
	case "grep", "file_grep":
		return "agentgrep"
	case "skill", "Skill":
		return "skill_manage"
	case "discover_tools":
		return "integration_tools"
	case "todoread", "todowrite", "todo_read", "todo_write", "todos":
		return "todo"
	// Anthropic OAuth PascalCase aliases (issue #486): nested batch subcall
	// names resolve here.
	case "Bash":
		return "bash"
	case "Read":
		return "read"
	case "Write":
		return "write"
	case "Edit":
		return "edit"
	case "Grep":
		return "agentgrep"
	case "Agent":
		return "subagent"
	case "ScheduleWakeup":
		return "schedule"
	default:
		return name
	}
}

// Schema vocabulary shared by every tool.

// ToolIntentDescription is the intent property description.
const ToolIntentDescription = "Required short label shown in the UI: why this call is being made."

// AcceptLargeOutputKey is the input key a caller sets to accept the token cost
// of an oversized result.
const AcceptLargeOutputKey = "accept_large_output"

// AcceptLargeOutputDescription is deliberately terse: it rides on every tool
// schema on every request.
const AcceptLargeOutputDescription = "Re-run accepting the stated token cost of a withheld result."

func intentSchemaProperty() json.RawMessage {
	return json.RawMessage(`{"type":"string","description":` + jsonString(ToolIntentDescription) + `}`)
}

func acceptLargeOutputSchemaProperty() json.RawMessage {
	return json.RawMessage(`{"type":"boolean","description":` + jsonString(AcceptLargeOutputDescription) + `}`)
}

func jsonString(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

// EnsureIntentInSchema ensures a tool parameter schema declares the shared
// `intent` property (required) and the optional `accept_large_output` escape
// hatch. Applied centrally when converting tools to provider definitions so
// every tool — including MCP proxies — asks the model for an intent without
// wiring it manually. Non-object schemas pass through untouched.
func EnsureIntentInSchema(schema json.RawMessage) json.RawMessage {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(schema, &doc); err != nil {
		return schema
	}
	// Only touch object-shaped parameter schemas.
	isObject := false
	if rawType, ok := doc["type"]; ok {
		var typeString string
		if json.Unmarshal(rawType, &typeString) == nil && typeString == "object" {
			isObject = true
		}
	} else if _, hasProperties := doc["properties"]; hasProperties {
		isObject = true
	}
	if !isObject {
		return schema
	}

	properties := map[string]json.RawMessage{}
	if raw, ok := doc["properties"]; ok {
		if err := json.Unmarshal(raw, &properties); err != nil {
			return schema
		}
	}
	if _, has := properties["intent"]; !has {
		properties["intent"] = intentSchemaProperty()
	}
	// Optional by design: requiring it would make the model answer a token
	// budget question on every call.
	if _, has := properties[AcceptLargeOutputKey]; !has {
		properties[AcceptLargeOutputKey] = acceptLargeOutputSchemaProperty()
	}
	encodedProperties, err := json.Marshal(properties)
	if err != nil {
		return schema
	}
	doc["properties"] = encodedProperties

	required := []string{}
	if raw, ok := doc["required"]; ok {
		_ = json.Unmarshal(raw, &required)
	}
	hasIntent := false
	for _, key := range required {
		if key == "intent" {
			hasIntent = true
			break
		}
	}
	if !hasIntent {
		required = append(required, "intent")
	}
	encodedRequired, err := json.Marshal(required)
	if err != nil {
		return schema
	}
	doc["required"] = encodedRequired

	encoded, err := json.Marshal(doc)
	if err != nil {
		return schema
	}
	return encoded
}

// StdinInputRequest asks the UI for stdin input from a running command.
type StdinInputRequest struct {
	RequestID  string
	Prompt     string
	IsPassword bool
	// Respond delivers the user's answer; the tool blocks until it arrives.
	Respond func(string)
}

// ExecutionMode distinguishes agent-turn calls from direct invocations.
type ExecutionMode int

const (
	// ExecutionAgentTurn is the normal agent path.
	ExecutionAgentTurn ExecutionMode = iota
	// ExecutionDirect is a direct (CLI/doctor) invocation.
	ExecutionDirect
)

// ToolContext carries per-call identity and I/O collaborators.
type ToolContext struct {
	SessionID  string
	MessageID  string
	ToolCallID string
	// WorkingDir resolves relative paths; empty means the process cwd.
	WorkingDir string
	// StdinRequest delivers stdin prompts to the UI; nil disables stdin.
	StdinRequest func(StdinInputRequest)
	// ShutdownSignal closes when a graceful shutdown is requested.
	ShutdownSignal <-chan struct{}
	// Mode distinguishes agent-turn from direct execution.
	Mode ExecutionMode
}

// ForSubcall derives a child context; the sub-call id is parent-scoped and
// retry-stable (batch labels repeat, so the parent id disambiguates).
func (c ToolContext) ForSubcall(toolCallID string) ToolContext {
	child := c
	child.ToolCallID = itoa(len(c.ToolCallID)) + ":" + c.ToolCallID + ":" + toolCallID
	return child
}

// ResolvePath resolves a path against the working dir.
func (c ToolContext) ResolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if c.WorkingDir != "" {
		return filepath.Join(c.WorkingDir, path)
	}
	return path
}

// Tool is an executable tool (ported from tool-core Tool trait).
type Tool interface {
	// Name is the canonical name sent to the API.
	Name() string
	// Description is the model-facing description.
	Description() string
	// ParametersSchema is the JSON Schema for the input.
	ParametersSchema() json.RawMessage
	// Execute runs the tool.
	Execute(input json.RawMessage, ctx ToolContext) (ToolOutput, error)
}

// Definition renders the provider tool definition with the shared intent
// property injected.
func Definition(tool Tool) (name, description string, schema []byte) {
	return tool.Name(), tool.Description(), EnsureIntentInSchema(tool.ParametersSchema())
}

func itoa(n int) string { return strconv.Itoa(n) }
