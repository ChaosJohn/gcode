package types

// ToolOutput is the in-memory tool execution envelope (jcode ToolOutput —
// not serialized upstream; no serde derives).
type ToolOutput struct {
	Output   string
	Title    *string
	Metadata any // JSON value
	Images   []ToolImage
}

// ToolImage is one image attached to a tool output.
type ToolImage struct {
	MediaType string
	Data      string
	Label     *string
}

// NewToolOutput mirrors ToolOutput::new.
func NewToolOutput(output string) ToolOutput { return ToolOutput{Output: output} }

// WithTitle mirrors ToolOutput::with_title.
func (o ToolOutput) WithTitle(title string) ToolOutput { o.Title = &title; return o }

// WithMetadata mirrors ToolOutput::with_metadata.
func (o ToolOutput) WithMetadata(metadata any) ToolOutput { o.Metadata = metadata; return o }

// WithImage mirrors ToolOutput::with_image.
func (o ToolOutput) WithImage(mediaType, data string) ToolOutput {
	o.Images = append(o.Images, ToolImage{MediaType: mediaType, Data: data})
	return o
}

// WithLabeledImage mirrors ToolOutput::with_labeled_image.
func (o ToolOutput) WithLabeledImage(mediaType, data, label string) ToolOutput {
	o.Images = append(o.Images, ToolImage{MediaType: mediaType, Data: data, Label: &label})
	return o
}

// toolNameAliases maps provider-presented aliases to canonical registry
// names, mirroring resolve_tool_name in jcode-tool-types. Keep in sync with
// upstream (including the PascalCase OAuth aliases, issue #486).
var toolNameAliases = map[string]string{
	"communicate":    "swarm",
	"task":           "subagent",
	"task_runner":    "subagent",
	"launch":         "open",
	"shell":          "bash",
	"shell_exec":     "bash",
	"read_file":      "read",
	"file_read":      "read",
	"write_file":     "write",
	"file_write":     "write",
	"edit_file":      "edit",
	"file_edit":      "edit",
	"multiedit":      "edit",
	"MultiEdit":      "edit",
	"multi_edit":     "edit",
	"patch":          "apply_patch",
	"Patch":          "apply_patch",
	"grep":           "agentgrep",
	"file_grep":      "agentgrep",
	"skill":          "skill_manage",
	"Skill":          "skill_manage",
	"discover_tools": "integration_tools",
	"todoread":       "todo",
	"todowrite":      "todo",
	"todo_read":      "todo",
	"todo_write":     "todo",
	"todos":          "todo",
	"Bash":           "bash",
	"Read":           "read",
	"Write":          "write",
	"Edit":           "edit",
	"Grep":           "agentgrep",
	"Agent":          "subagent",
	"ScheduleWakeup": "schedule",
}

// ResolveToolName resolves tool name aliases to canonical internal names.
// A `functions.` transport namespace is stripped first (only that exact
// namespace — e.g. `mcp.functions.bash` is left untouched).
func ResolveToolName(name string) string {
	if rest, ok := cutPrefix(name, "functions."); ok {
		name = rest
	}
	if canonical, ok := toolNameAliases[name]; ok {
		return canonical
	}
	return name
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return s, false
}
