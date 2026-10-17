package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ChaosJohn/gcode/internal/mcp"
	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// mcp_search / mcp_call / mcp tools (ported from tool/mcp.rs).

const (
	maxSearchToolReferences   = 32
	mcpSearchTokenBudget      = 6000
	minSearchDescriptionChars = 120
)

// Manager is the process-wide MCP connection registry; the app injects it.
var MCPManager = mcp.NewManager()

// McpSearchTool searches MCP tools by server, name, or description.
type McpSearchTool struct{}

// NewMcpSearchTool builds the mcp_search tool.
func NewMcpSearchTool() *McpSearchTool { return &McpSearchTool{} }

// Name implements Tool.
func (t *McpSearchTool) Name() string { return "mcp_search" }

// Description implements Tool.
func (t *McpSearchTool) Description() string {
	return "Search available MCP tools by server, name, or description. Returns callable names and input schemas."
}

// ParametersSchema implements Tool.
func (t *McpSearchTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"server": {"type": "string", "description": "Optional exact MCP server name."},
			"query": {"type": "string", "description": "Optional case-insensitive name or description search."}
		}
	}`)
}

type mcpSearchResult struct {
	Name        string          `json:"name"`
	Server      string          `json:"server"`
	Tool        string          `json:"tool"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// matchRank: name matches outrank description matches, so a query like
// "calendar" does not return every tool whose docs mention a calendar.
type matchRank int

const (
	rankDescription matchRank = iota
	rankName
)

func rankMatch(queryTerms []string, name, server, tool, description string) (matchRank, bool) {
	if len(queryTerms) == 0 {
		return rankName, true
	}
	names := strings.ToLower(fmt.Sprintf("%s %s %s", name, server, tool))
	allTerms := func(haystack string) bool {
		for _, term := range queryTerms {
			if !strings.Contains(haystack, term) {
				return false
			}
		}
		return true
	}
	if allTerms(names) {
		return rankName, true
	}
	if allTerms(names + " " + strings.ToLower(description)) {
		return rankDescription, true
	}
	return 0, false
}

// clipDescription truncates with an ellipsis at a rune boundary.
func clipDescription(text string, max int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

// estimateTokens approximates the provider-side token cost.
func estimateTokens(text string) int {
	return (len([]rune(text)) + 3) / 4
}

// Execute implements Tool.
func (t *McpSearchTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params struct {
		Server *string `json:"server"`
		Query  *string `json:"query"`
	}
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	serverFilter := ""
	if params.Server != nil {
		serverFilter = strings.TrimSpace(*params.Server)
	}
	var queryTerms []string
	if params.Query != nil {
		for _, field := range strings.Fields(*params.Query) {
			queryTerms = append(queryTerms, strings.ToLower(field))
		}
	}

	catalog := MCPManager.SearchableTools()
	names := mcp.DispatchNames(catalog)
	type rankedResult struct {
		rank   matchRank
		result mcpSearchResult
	}
	var ranked []rankedResult
	for i, entry := range catalog {
		if serverFilter != "" && serverFilter != entry.Server {
			continue
		}
		rank, ok := rankMatch(queryTerms, names[i], entry.Server, entry.Tool.Name, entry.Tool.Description)
		if !ok {
			continue
		}
		description := entry.Tool.Description
		if description == "" {
			description = "MCP tool"
		}
		ranked = append(ranked, rankedResult{rank: rank, result: mcpSearchResult{
			Name: names[i], Server: entry.Server, Tool: entry.Tool.Name,
			Description: strings.TrimSpace(description), InputSchema: entry.Tool.InputSchema,
		}})
	}

	// Name matches win; description-only matches are noise once any tool
	// matches by name.
	best := rankDescription
	hasName := false
	for _, item := range ranked {
		if item.rank == rankName {
			hasName = true
			break
		}
	}
	if hasName {
		best = rankName
	}
	var matches []mcpSearchResult
	for _, item := range ranked {
		if item.rank == best {
			matches = append(matches, item.result)
		}
	}
	total := len(matches)
	budget := mcpSearchTokenBudget
	if acceptsLargeOutput(input) {
		budget = int(^uint(0) >> 1)
	}
	output, detailed := fitSearchResults(matches, budget)

	references := []string{}
	for i := 0; i < detailed && i < maxSearchToolReferences && i < len(matches); i++ {
		references = append(references, matches[i].Name)
	}
	metadata, _ := json.Marshal(map[string]any{"tool_references": references})
	return toolcore.ToolOutput{Output: output,
		Title: strPtr(fmt.Sprintf("MCP tools (%d)", total)), Metadata: metadata}, nil
}

func strPtr(s string) *string { return &s }

// fitSearchResults renders matches within a token budget: every match in
// full, then shorter descriptions, then full detail for only the leading
// matches with the rest listed by name.
func fitSearchResults(matches []mcpSearchResult, budget int) (string, int) {
	render := func(detailed int, descriptionChars int) string {
		head := make([]mcpSearchResult, detailed)
		for i := 0; i < detailed; i++ {
			entry := matches[i]
			if descriptionChars >= 0 {
				entry.Description = clipDescription(entry.Description, descriptionChars)
			}
			head[i] = entry
		}
		encoded, err := json.Marshal(head)
		out := string(encoded)
		if err != nil {
			out = "[]"
		}
		if detailed < len(matches) {
			rest := make([]string, 0, len(matches)-detailed)
			for _, match := range matches[detailed:] {
				rest = append(rest, match.Name)
			}
			out += fmt.Sprintf("\n\n%d more matches (names only; search for one by name for its schema): %s",
				len(rest), strings.Join(rest, ", "))
		}
		return out
	}
	fits := func(out string) bool { return estimateTokens(out) <= budget }

	full := render(len(matches), -1)
	if fits(full) {
		return full, len(matches)
	}
	longest := 0
	for _, match := range matches {
		if n := len([]rune(match.Description)); n > longest {
			longest = n
		}
	}
	for chars := longest / 2; chars >= minSearchDescriptionChars; chars /= 2 {
		out := render(len(matches), chars)
		if fits(out) {
			return out, len(matches)
		}
	}
	lo, hi := 0, len(matches)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(render(mid, minSearchDescriptionChars)) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return render(lo, minSearchDescriptionChars), lo
}

// McpCallTool invokes one MCP tool.
type McpCallTool struct{}

// NewMcpCallTool builds the mcp_call tool.
func NewMcpCallTool() *McpCallTool { return &McpCallTool{} }

// Name implements Tool.
func (t *McpCallTool) Name() string { return "mcp_call" }

// Description implements Tool.
func (t *McpCallTool) Description() string {
	return "Call an MCP server tool discovered with mcp_search."
}

// ParametersSchema implements Tool.
func (t *McpCallTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"server": {"type": "string", "description": "MCP server name."},
			"tool": {"type": "string", "description": "Raw MCP tool name."},
			"arguments": {
				"type": "object",
				"additionalProperties": true,
				"description": "Arguments matching the input schema returned by mcp_search."
			}
		},
		"required": ["server", "tool", "arguments"]
	}`)
}

// Execute implements Tool.
func (t *McpCallTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params struct {
		Server    string          `json:"server"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	if params.Server == "" || params.Tool == "" {
		return toolcore.ToolOutput{}, fmt.Errorf("mcp_call requires 'server' and 'tool'")
	}
	arguments := params.Arguments
	if len(arguments) == 0 || string(arguments) == "null" {
		arguments = json.RawMessage("{}")
	}
	result, err := MCPManager.CallTool(params.Server, params.Tool, arguments)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	// Render the standard MCP content shape: {content: [{type, text}]}.
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &payload); err == nil && payload.Content != nil {
		var parts []string
		for _, block := range payload.Content {
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		output := strings.Join(parts, "\n")
		if payload.IsError {
			return toolcore.ToolOutput{}, fmt.Errorf("%s", output)
		}
		if output == "" {
			output = "Tool completed with no output"
		}
		return toolcore.NewToolOutput(output).WithTitle(
			fmt.Sprintf("mcp %s/%s", params.Server, params.Tool)), nil
	}
	return toolcore.NewToolOutput(string(result)).WithTitle(
		fmt.Sprintf("mcp %s/%s", params.Server, params.Tool)), nil
}

// McpTool manages server connections.
type McpTool struct{}

// NewMcpTool builds the mcp management tool.
func NewMcpTool() *McpTool { return &McpTool{} }

// Name implements Tool.
func (t *McpTool) Name() string { return "mcp" }

// Description implements Tool.
func (t *McpTool) Description() string {
	return "Manage MCP servers: list, connect, disconnect."
}

// ParametersSchema implements Tool.
func (t *McpTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["action"],
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list", "connect", "disconnect"],
				"description": "Management action."
			},
			"server": {"type": "string", "description": "MCP server name for connect/disconnect."}
		}
	}`)
}

// Execute implements Tool.
func (t *McpTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params struct {
		Action string  `json:"action"`
		Server *string `json:"server"`
	}
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	switch params.Action {
	case "list":
		configs := MCPManager.LoadFunc()
		var out strings.Builder
		out.WriteString("Configured MCP servers:\n")
		if len(configs) == 0 {
			out.WriteString("  (none; add [mcp_servers.NAME] tables to config.toml)\n")
		}
		connected := map[string]bool{}
		for _, name := range MCPManager.Connected() {
			connected[name] = true
		}
		for name, config := range configs {
			state := "disconnected"
			if connected[name] {
				state = "connected"
			}
			detail := config.Command
			if config.URL != "" {
				detail = config.URL
			}
			fmt.Fprintf(&out, "  %s (%s) %s\n", name, state, detail)
		}
		return toolcore.NewToolOutput(out.String()).WithTitle("MCP servers"), nil
	case "connect":
		if params.Server == nil || *params.Server == "" {
			return toolcore.ToolOutput{}, fmt.Errorf("mcp connect requires 'server'")
		}
		tools, err := MCPManager.Connect(*params.Server)
		if err != nil {
			return toolcore.ToolOutput{}, err
		}
		return toolcore.NewToolOutput(fmt.Sprintf(
			"Connected to MCP server '%s' (%d tools available via mcp_search).",
			*params.Server, len(tools))).WithTitle("MCP connect"), nil
	case "disconnect":
		if params.Server == nil || *params.Server == "" {
			return toolcore.ToolOutput{}, fmt.Errorf("mcp disconnect requires 'server'")
		}
		if err := MCPManager.Disconnect(*params.Server); err != nil {
			return toolcore.ToolOutput{}, err
		}
		return toolcore.NewToolOutput(fmt.Sprintf("Disconnected from MCP server '%s'.",
			*params.Server)).WithTitle("MCP disconnect"), nil
	default:
		return toolcore.ToolOutput{}, fmt.Errorf(
			"Unsupported mcp action: %s. Use list, connect, or disconnect.", params.Action)
	}
}

// acceptsLargeOutput mirrors toolcore's escape hatch for direct input checks.
func acceptsLargeOutput(input json.RawMessage) bool {
	var params struct {
		AcceptLargeOutput bool `json:"accept_large_output"`
	}
	_ = parseInput(input, &params)
	return params.AcceptLargeOutput
}
