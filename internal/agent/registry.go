// Package agent ports the jcode-app-core agent: the turn loop that streams
// one assistant turn, executes tool calls through a registry, and repeats
// until the model stops calling tools. The upstream Recovery/nudge/retry
// ladder is ported at constant level; the TUI bus events become output
// callbacks.
package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ChaosJohn/gcode/internal/toolcore"
	"github.com/ChaosJohn/gcode/internal/types"
)

// Registry is the tool registry (ported from tool/registry.rs): exact-name
// lookup, definitions for providers, and the alias map for MCP dispatch
// names. Registration order is preserved for deterministic tool lists.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]toolcore.Tool
	order []string
	// aliases maps dispatch names (e.g. mcp__server__tool) to the registry
	// key that executes them.
	aliases map[string]string
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]toolcore.Tool{}, aliases: map[string]string{}}
}

// Register adds a tool; the first registration of a name wins (upstream
// semantics for duplicate SDK tools).
func (r *Registry) Register(tool toolcore.Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[tool.Name()]; !exists {
		r.order = append(r.order, tool.Name())
	}
	r.tools[tool.Name()] = tool
}

// RegisterAlias routes a second name to an already-registered tool.
func (r *Registry) RegisterAlias(alias, target string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aliases[alias] = target
}

// Get returns the tool registered under a name or alias.
func (r *Registry) Get(name string) (toolcore.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if tool, ok := r.tools[name]; ok {
		return tool, true
	}
	if target, ok := r.aliases[name]; ok {
		tool, ok := r.tools[target]
		return tool, ok
	}
	return nil, false
}

// Names lists registered tools in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// Definitions renders provider tool definitions in registration order.
func (r *Registry) Definitions() []types.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]types.ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		tool := r.tools[name]
		defName, description, schema := toolcore.Definition(tool)
		raw := json.RawMessage(schema)
		out = append(out, types.ToolDefinition{
			Name: defName, Description: description, InputSchema: raw,
		})
	}
	return out
}

// Execute runs one tool by name or alias.
func (r *Registry) Execute(name string, input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	tool, ok := r.Get(name)
	if !ok {
		return toolcore.ToolOutput{}, fmt.Errorf("Unknown tool: %s", name)
	}
	return tool.Execute(input, ctx)
}

// SortedNames returns the registration order sorted (upstream sorts for
// provider definitions to stabilize cache prefixes).
func (r *Registry) SortedNames() []string {
	names := r.Names()
	sort.Strings(names)
	return names
}

// ValidationMessage ports ToolCall::validation_error: a call without a JSON
// object input is surfaced to the model as a user-side error result.
func ValidationMessage(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return fmt.Sprintf("Tool %s received malformed JSON input: %v", name, err)
	}
	if _, isObject := value.(map[string]any); value != nil && !isObject {
		if string(input) == "null" {
			return ""
		}
		return fmt.Sprintf("Tool %s received non-object JSON input: %s", name,
			strings.TrimSpace(string(input)))
	}
	return ""
}
