package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/ChaosJohn/gcode/internal/agent"
	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// batch tool (ported from tool/batch.rs): run independent tool calls in
// parallel, results reported in original order with a per-subcall budget.

// BatchTool fans independent calls out across the registry.
type BatchTool struct{}

// NewBatchTool builds the batch tool.
func NewBatchTool() *BatchTool { return &BatchTool{} }

// Name implements Tool.
func (t *BatchTool) Name() string { return "batch" }

// Description implements Tool.
func (t *BatchTool) Description() string {
	return `Run independent tool calls in parallel instead of making them sequentially. Example: {"tool_calls":[{"tool":"read","parameters":{"path":"src/a.rs"}},{"tool":"read","parameters":{"path":"src/b.rs"}}]}. Only for independent calls; sequential results stay sequential.`
}

// ParametersSchema implements Tool.
func (t *BatchTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["tool_calls"],
		"properties": {
			"tool_calls": {
				"type": "array",
				"description": "Independent calls to run in parallel.",
				"items": {
					"type": "object",
					"required": ["tool", "parameters"],
					"properties": {
						"tool": {"type": "string", "description": "Tool name."},
						"parameters": {"type": "object", "additionalProperties": true, "description": "Tool input."}
					}
				}
			}
		}
	}`)
}

// Execute implements Tool.
func (t *BatchTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params struct {
		ToolCalls []struct {
			Tool       string          `json:"tool"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tool_calls"`
	}
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	numTools := len(params.ToolCalls)
	if numTools == 0 {
		return toolcore.ToolOutput{}, fmt.Errorf("batch requires at least one tool_call")
	}

	type batchResult struct {
		index    int
		toolName string
		output   toolcore.ToolOutput
		err      error
	}
	results := make([]batchResult, numTools)
	var wg sync.WaitGroup
	for i, call := range params.ToolCalls {
		wg.Add(1)
		go func(index int, call struct {
			Tool       string          `json:"tool"`
			Parameters json.RawMessage `json:"parameters"`
		}) {
			defer wg.Done()
			toolName := call.Tool
			subCtx := ctx.ForSubcall(fmt.Sprintf("batch-%d-%s", index+1, toolName))
			parameters := call.Parameters
			if len(parameters) == 0 || string(parameters) == "null" {
				parameters = json.RawMessage("{}")
			}
			output, execErr := batchRegistry().Execute(toolName, parameters, subCtx)
			results[index] = batchResult{index: index, toolName: toolName,
				output: output, err: execErr}
		}(i, call)
	}
	wg.Wait()

	var output strings.Builder
	successCount, errorCount := 0, 0
	var failedTools []string
	maxPerTool := 50000 / numTools
	for _, result := range results {
		fmt.Fprintf(&output, "--- [%d] %s ---\n", result.index+1, result.toolName)
		if result.err != nil {
			errorCount++
			failedTools = append(failedTools, result.toolName)
			if result.output.Output != "" {
				fmt.Fprintf(&output, "%s\nError: %v", result.output.Output, result.err)
			} else {
				fmt.Fprintf(&output, "Error: %v", result.err)
			}
		} else {
			successCount++
			if len(result.output.Output) > maxPerTool {
				output.WriteString(truncateRunes(result.output.Output, maxPerTool))
				output.WriteString("...\n(truncated)")
			} else {
				output.WriteString(result.output.Output)
			}
		}
		output.WriteString("\n\n")
	}
	fmt.Fprintf(&output, "Completed: %d succeeded, %d failed", successCount, errorCount)
	return toolcore.NewToolOutput(output.String()).WithTitle("batch"), nil
}

// batchRegistry is injected by the host (the registry containing batch
// itself); a plain package variable keeps the tool constructor dependency-
// free.
var batchRegistryHolder = func() *agent.Registry { return nil }

// SetBatchRegistry wires the registry used for subcalls (called at app
// assembly, after the registry is built).
func SetBatchRegistry(registry *agent.Registry) {
	batchRegistryHolder = func() *agent.Registry { return registry }
}

func batchRegistry() *agent.Registry { return batchRegistryHolder() }
