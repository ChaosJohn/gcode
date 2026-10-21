// memory tool (ported from tool/memory.rs, actions subset): remember,
// recall, search, list, forget, tag across project/global scopes. link/
// related require the memory graph + embeddings (sidecar) — documented
// deviation.
package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ChaosJohn/gcode/internal/memory"
	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// MemoryTool stores and recalls cross-session information.
type MemoryTool struct{}

// NewMemoryTool builds the memory tool.
func NewMemoryTool() *MemoryTool { return &MemoryTool{} }

// Name implements Tool.
func (t *MemoryTool) Name() string { return "memory" }

// Description implements Tool.
func (t *MemoryTool) Description() string { return "Manage memory." }

// ParametersSchema implements Tool.
func (t *MemoryTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["action"],
		"properties": {
			"action": {"type": "string", "enum": ["remember", "recall", "search", "list", "forget", "tag"], "description": "Action."},
			"content": {"type": "string", "description": "For remember: the memory text."},
			"category": {"type": "string", "enum": ["fact", "preference", "entity", "correction"]},
			"query": {"type": "string", "description": "For search: text to find."},
			"id": {"type": "string", "description": "For forget/tag: memory id."},
			"tags": {"type": "array", "items": {"type": "string"}},
			"scope": {"type": "string", "enum": ["project", "global", "all"], "description": "Defaults: project for remember, all otherwise."},
			"limit": {"type": "integer", "minimum": 0, "description": "Max results for recall, search, or list. Recall defaults to 10."}
		}
	}`)
}

func parseMemoryScope(scope string, projectDir string) (memory.Scope, error) {
	if strings.TrimSpace(scope) == "" {
		if projectDir != "" {
			return memory.ScopeProject, nil
		}
		return memory.ScopeGlobal, nil
	}
	return memory.ParseScope(scope)
}

func renderMemoryLines(entries []memory.Entry) string {
	var out strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&out, "- [%s] %s\n  id: %s\n", entry.Category,
			strings.TrimSpace(entry.Content), entry.ID)
		if len(entry.Tags) > 0 {
			fmt.Fprintf(&out, "  tags: %s\n", strings.Join(entry.Tags, ", "))
		}
	}
	return out.String()
}

// Execute implements Tool.
func (t *MemoryTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params struct {
		Action   string   `json:"action"`
		Content  *string  `json:"content"`
		Category *string  `json:"category"`
		Query    *string  `json:"query"`
		ID       *string  `json:"id"`
		Tags     []string `json:"tags"`
		Scope    *string  `json:"scope"`
		Limit    *int     `json:"limit"`
	}
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	manager := memory.NewManager().WithProjectDir(ctx.WorkingDir)
	scopeValue := ""
	if params.Scope != nil {
		scopeValue = *params.Scope
	}
	scope, err := parseMemoryScope(scopeValue, ctx.WorkingDir)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}

	switch params.Action {
	case "remember":
		content := ""
		if params.Content != nil {
			content = strings.TrimSpace(*params.Content)
		}
		if content == "" {
			return toolcore.ToolOutput{}, fmt.Errorf("content required")
		}
		category := memory.CategoryFact
		if params.Category != nil && *params.Category != "" {
			if !memory.ValidCategory(*params.Category) {
				return toolcore.ToolOutput{}, fmt.Errorf(
					"Unknown category: %s. Use fact, preference, entity, or correction",
					*params.Category)
			}
			category = memory.Category(*params.Category)
		}
		entry := memory.Entry{Category: category, Content: content, Tags: params.Tags}
		id, err := manager.Remember(scope, entry)
		if err != nil {
			return toolcore.ToolOutput{}, err
		}
		return toolcore.NewToolOutput(fmt.Sprintf(
			"Remembered %s (%s): \"%s\" [id: %s]", category, scope, content, id)), nil

	case "recall":
		if limit <= 0 {
			limit = 10
		}
		entries := manager.Recent(scope, limit)
		if len(entries) == 0 {
			return toolcore.NewToolOutput("No memories stored yet."), nil
		}
		prompt := memory.FormatForPrompt(entries, limit)
		if prompt == "" {
			return toolcore.NewToolOutput("No memories stored yet."), nil
		}
		return toolcore.NewToolOutput("Recent memories:\n" + prompt), nil

	case "search":
		query := ""
		if params.Query != nil {
			query = *params.Query
		}
		results := manager.Search(scope, query)
		if len(results) == 0 {
			return toolcore.NewToolOutput(fmt.Sprintf("No memories matching '%s'", query)), nil
		}
		if limit > 0 && len(results) > limit {
			results = results[:limit]
		}
		return toolcore.NewToolOutput(fmt.Sprintf("Found %d memories:\n\n%s",
			len(results), renderMemoryLines(results))), nil

	case "list":
		entries := manager.Recent(scope, 0)
		if len(entries) == 0 {
			return toolcore.NewToolOutput("No memories stored."), nil
		}
		return toolcore.NewToolOutput(fmt.Sprintf("Memories (%d):\n\n%s",
			len(entries), renderMemoryLines(entries))), nil

	case "forget":
		if params.ID == nil || strings.TrimSpace(*params.ID) == "" {
			return toolcore.ToolOutput{}, fmt.Errorf("id required")
		}
		if manager.Forget(scope, *params.ID) {
			return toolcore.NewToolOutput(fmt.Sprintf("Forgot: %s", *params.ID)), nil
		}
		return toolcore.NewToolOutput(fmt.Sprintf("Not found: %s", *params.ID)), nil

	case "tag":
		if params.ID == nil || strings.TrimSpace(*params.ID) == "" {
			return toolcore.ToolOutput{}, fmt.Errorf("id required")
		}
		if len(params.Tags) == 0 {
			return toolcore.ToolOutput{}, fmt.Errorf("At least one tag required")
		}
		if !manager.Tag(scope, *params.ID, params.Tags) {
			return toolcore.NewToolOutput(fmt.Sprintf("Not found: %s", *params.ID)), nil
		}
		return toolcore.NewToolOutput(fmt.Sprintf("Tagged memory %s with: %s",
			*params.ID, strings.Join(params.Tags, ", "))), nil

	default:
		return toolcore.ToolOutput{}, fmt.Errorf("Unknown action: %s", params.Action)
	}
}
