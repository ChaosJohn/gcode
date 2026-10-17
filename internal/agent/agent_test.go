// Ported turn-loop tests (turn_loops_tests.rs subset) with a scripted
// provider: multi-round tool execution, batch nudge, empty-post-tool guard,
// context-limit retry, history cap, validation errors.
package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/provider"
	"github.com/ChaosJohn/gcode/internal/toolcore"
	"github.com/ChaosJohn/gcode/internal/types"
)

// scriptedProvider replays pre-recorded responses in order.
type scriptedProvider struct {
	responses []scriptedResponse
	calls     int
	failures  map[int]string
}

type scriptedResponse struct {
	text      string
	toolCalls []types.ToolCall
}

func (p *scriptedProvider) Name() string              { return "scripted" }
func (p *scriptedProvider) Model() string             { return "scripted-1" }
func (p *scriptedProvider) SetModel(string) error     { return nil }
func (p *scriptedProvider) AvailableModels() []string { return nil }
func (p *scriptedProvider) SupportsImageInput() bool  { return false }
func (p *scriptedProvider) SupportsCompaction() bool  { return false }
func (p *scriptedProvider) Fork() provider.Provider   { return p }
func (p *scriptedProvider) InvalidateCredentials()    {}

func (p *scriptedProvider) Complete(turn provider.TurnRequest, emit func(types.StreamEvent) error) error {
	index := p.calls
	p.calls++
	if message, ok := p.failures[index]; ok {
		return &scriptedError{message}
	}
	if index >= len(p.responses) {
		return &scriptedError{"script exhausted"}
	}
	response := p.responses[index]
	if response.text != "" {
		if err := emit(types.TextDelta{Text: response.text}); err != nil {
			return err
		}
	}
	for _, call := range response.toolCalls {
		_ = emit(types.ToolUseStart{ID: call.ID, Name: call.Name})
		raw, _ := json.Marshal(call.Input)
		_ = emit(types.ToolInputDeltaFor{ID: call.ID, Delta: string(raw)})
		_ = emit(types.ToolUseEndFor{ID: call.ID})
	}
	return emit(types.MessageEnd{})
}

type scriptedError struct{ message string }

func (e *scriptedError) Error() string { return e.message }

// echoTool echoes its input; the recorded provider outputs use it.
type echoTool struct{}

func (echoTool) Name() string                      { return "echo" }
func (echoTool) Description() string               { return "Echo the input." }
func (echoTool) ParametersSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (echoTool) Execute(input json.RawMessage, _ toolcore.ToolContext) (toolcore.ToolOutput, error) {
	return toolcore.NewToolOutput("echoed: " + string(input)), nil
}

// batchTool satisfies the batch-nudge availability check.
type batchTool struct{}

func (batchTool) Name() string                      { return "batch" }
func (batchTool) Description() string               { return "Run tools in parallel." }
func (batchTool) ParametersSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (batchTool) Execute(input json.RawMessage, _ toolcore.ToolContext) (toolcore.ToolOutput, error) {
	return toolcore.NewToolOutput("batched"), nil
}

func newScriptedAgent(responses []scriptedResponse) (*Agent, *scriptedProvider) {
	p := &scriptedProvider{responses: responses}
	registry := NewRegistry()
	registry.Register(echoTool{})
	registry.Register(batchTool{})
	agent := NewAgent(nil, registry)
	agent.Provider = p
	return agent, p
}

// A full multi-round conversation: tool call → result → final text.
func TestRunOnceMultiRound(t *testing.T) {
	first := json.RawMessage(`{"text":"hi"}`)
	agent, _ := newScriptedAgent([]scriptedResponse{
		{text: "Let me check.", toolCalls: []types.ToolCall{
			{ID: "t1", Name: "echo", Input: first}}},
		{text: "All done."},
	})
	final, err := agent.RunOnce("hello")
	if err != nil {
		t.Fatal(err)
	}
	if final != "All done." {
		t.Fatalf("final: %q", final)
	}
	// Transcript: user, assistant(text+tool_use), user(tool_result), assistant.
	if len(agent.Messages) != 4 {
		t.Fatalf("messages: %d", len(agent.Messages))
	}
	result := agent.Messages[2]
	if result.Role != types.RoleUser {
		t.Fatalf("result role: %s", result.Role)
	}
	if result.Content[0].Content == nil || !strings.Contains(*result.Content[0].Content, "echoed") {
		t.Fatalf("tool result: %+v", result.Content[0])
	}
	// The assistant message carries the tool_use block.
	blocks := agent.Messages[1].Content
	if len(blocks) != 2 || blocks[1].Type != types.BlockToolUse {
		t.Fatalf("assistant blocks: %+v", blocks)
	}
}

// Ported BATCH_NUDGE shape: after 3 sequential single-tool rounds the next
// request carries the reminder user message.
func TestBatchNudgeAfterSequentialRounds(t *testing.T) {
	input := json.RawMessage(`{}`)
	var requests [][]types.Message
	responses := make([]scriptedResponse, 0, 6)
	for i := 0; i < 4; i++ {
		responses = append(responses, scriptedResponse{
			toolCalls: []types.ToolCall{{ID: string(rune('a' + i)), Name: "echo", Input: input}},
		})
	}
	responses = append(responses, scriptedResponse{text: "done"})
	agent, provider := newScriptedAgent(responses)
	agent.Provider = &capturingProvider{inner: provider, capture: &requests}
	if _, err := agent.RunOnce("go"); err != nil {
		t.Fatal(err)
	}
	nudged := false
	for _, request := range requests {
		for _, message := range request {
			if message.Content[0].Text == nil {
				continue
			}
			if strings.Contains(*message.Content[0].Text, "use the batch tool instead") {
				nudged = true
			}
		}
	}
	if !nudged {
		t.Fatalf("batch nudge never injected; requests=%d", len(requests))
	}
}

type capturingProvider struct {
	inner   *scriptedProvider
	capture *[][]types.Message
}

func (c *capturingProvider) Name() string              { return c.inner.Name() }
func (c *capturingProvider) Model() string             { return c.inner.Model() }
func (c *capturingProvider) SetModel(m string) error   { return c.inner.SetModel(m) }
func (c *capturingProvider) AvailableModels() []string { return nil }
func (c *capturingProvider) SupportsImageInput() bool  { return false }
func (c *capturingProvider) SupportsCompaction() bool  { return false }
func (c *capturingProvider) Fork() provider.Provider   { return c }
func (c *capturingProvider) InvalidateCredentials()    {}

func (c *capturingProvider) Complete(turn provider.TurnRequest, emit func(types.StreamEvent) error) error {
	*c.capture = append(*c.capture, turn.Messages)
	return c.inner.Complete(turn, emit)
}

// The empty-post-tool guard retries instead of silently ending the task.
func TestEmptyPostToolGuard(t *testing.T) {
	input := json.RawMessage(`{}`)
	agent, _ := newScriptedAgent([]scriptedResponse{
		{toolCalls: []types.ToolCall{{ID: "t1", Name: "echo", Input: input}}},
		{}, {}, {}, // three empty responses: retried, not terminal
		{text: "recovered"},
	})
	final, err := agent.RunOnce("go")
	if err != nil {
		t.Fatal(err)
	}
	if final != "recovered" {
		t.Fatalf("final: %q", final)
	}
	if agent.Provider.(*scriptedProvider).calls != 5 {
		t.Fatalf("calls: %d", agent.Provider.(*scriptedProvider).calls)
	}
}

// Context-limit errors retry a bounded number of times.
func TestContextLimitRetryBound(t *testing.T) {
	agent, provider := newScriptedAgent([]scriptedResponse{{text: "ok"}})
	provider.failures = map[int]string{}
	for i := 0; i < MaxContextLimitRetries+1; i++ {
		provider.failures[i] = "prompt is too long: 999999 tokens > 200000"
	}
	_, err := agent.RunOnce("go")
	if err == nil || !strings.Contains(err.Error(), "compaction retries") {
		t.Fatalf("retry bound: %v", err)
	}
	if provider.calls != MaxContextLimitRetries+1 {
		t.Fatalf("calls: %d", provider.calls)
	}
}

// Malformed (non-object) tool input surfaces as a user-side error result.
func TestMalformedToolInput(t *testing.T) {
	agent, _ := newScriptedAgent([]scriptedResponse{
		{toolCalls: []types.ToolCall{{ID: "t1", Name: "echo", Input: json.RawMessage(`[1,2]`)}}},
		{text: "handled"},
	})
	if _, err := agent.RunOnce("go"); err != nil {
		t.Fatal(err)
	}
	result := agent.Messages[2].Content[0].Content
	if result == nil || !strings.Contains(*result, "non-object JSON input") {
		t.Fatalf("validation: %v", result)
	}
}

// Huge tool outputs are capped with the upstream notice.
func TestCapToolOutputForHistory(t *testing.T) {
	output := toolcore.NewToolOutput(strings.Repeat("x", maxToolOutputCharsForHistory+1000))
	capped := capToolOutputForHistory("read", output)
	if len([]rune(capped.Output)) > maxToolOutputCharsForHistory+400 {
		t.Fatalf("not capped: %d", len(capped.Output))
	}
	if !strings.Contains(capped.Output, "Tool output truncated by jcode") {
		t.Fatalf("notice missing")
	}
}

// Unknown tools error into the transcript as tool results.
func TestUnknownTool(t *testing.T) {
	agent, _ := newScriptedAgent([]scriptedResponse{
		{toolCalls: []types.ToolCall{{ID: "t1", Name: "nope", Input: json.RawMessage(`{}`)}}},
		{text: "ok"},
	})
	if _, err := agent.RunOnce("go"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*agent.Messages[2].Content[0].Content, "Unknown tool: nope") {
		t.Fatalf("unknown tool result: %v", agent.Messages[2].Content[0].Content)
	}
}

// The registry maps MCP dispatch aliases onto their tools.
func TestRegistryAlias(t *testing.T) {
	registry := NewRegistry()
	registry.Register(echoTool{})
	registry.RegisterAlias("mcp__srv__echo", "echo")
	tool, ok := registry.Get("mcp__srv__echo")
	if !ok || tool.Name() != "echo" {
		t.Fatalf("alias lookup failed")
	}
	if _, ok := registry.Get("missing"); ok {
		t.Fatalf("phantom tool")
	}
}
