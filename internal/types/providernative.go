package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Provider-native server tool items (jcode-message-types/src/provider_native.rs):
// blocks the provider executes itself, stored verbatim and replayed or
// downgraded to text summaries when the server tool is not attached.

const (
	// ProviderNativeAnthropic tags Anthropic server-tool items.
	ProviderNativeAnthropic = "anthropic"
	// ProviderNativeOpenAI tags OpenAI server-tool items.
	ProviderNativeOpenAI = "openai"
	// ProviderNativeWebSearchTool is the display name of server web search.
	ProviderNativeWebSearchTool = "web_search"
	// LocalWebsearchTool is the local tool name the server tool replaces.
	LocalWebsearchTool = "websearch"
	// LocalWebsearchDescription is the exact description of the local tool.
	LocalWebsearchDescription = "Search the web."
)

// AnthropicServerToolBlockTypes are the block types replayed verbatim.
var AnthropicServerToolBlockTypes = []string{"server_tool_use", "web_search_tool_result"}

// IsBuiltinLocalWebsearch reports whether a tool definition is exactly the
// built-in local websearch tool (matched by name AND description).
func IsBuiltinLocalWebsearch(tool ToolDefinition) bool {
	return tool.Name == LocalWebsearchTool && tool.Description == LocalWebsearchDescription
}

// IsAnthropicServerToolBlock reports whether a block type must be kept.
func IsAnthropicServerToolBlock(blockType string) bool {
	for _, t := range AnthropicServerToolBlockTypes {
		if t == blockType {
			return true
		}
	}
	return false
}

// IsProviderNativeToolID reports whether an id belongs to a provider-executed
// tool (Anthropic srvtoolu_*, OpenAI ws_*).
func IsProviderNativeToolID(id string) bool {
	return strings.HasPrefix(id, "srvtoolu_") || strings.HasPrefix(id, "ws_")
}

// ProviderNativeDisplay is the provider-agnostic view of a native item.
type ProviderNativeDisplay struct {
	ID      string
	Name    string
	Input   json.RawMessage
	Output  *string
	IsError bool
}

// JSONStr extracts a string field from a raw JSON object ("" when absent).
func JSONStr(value json.RawMessage, key string) string { return jsonStr(value, key) }

func jsonStr(value json.RawMessage, key string) string {
	var probe map[string]any
	if json.Unmarshal(value, &probe) != nil {
		return ""
	}
	if s, ok := probe[key].(string); ok {
		return s
	}
	return ""
}

func jsonField(value json.RawMessage, key string) json.RawMessage {
	var probe map[string]any
	if json.Unmarshal(value, &probe) != nil {
		return nil
	}
	raw, err := json.Marshal(probe[key])
	if err != nil {
		return nil
	}
	return raw
}

func jsonRawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

// ProviderNativeDisplayOf describes a native item; nil when not user-visible.
func ProviderNativeDisplayOf(provider string, item json.RawMessage) *ProviderNativeDisplay {
	blockType := jsonStr(item, "type")
	switch {
	case provider == ProviderNativeAnthropic && blockType == "server_tool_use":
		id := jsonStr(item, "id")
		if id == "" {
			return nil
		}
		name := jsonStr(item, "name")
		if name == "" {
			name = ProviderNativeWebSearchTool
		}
		input := jsonField(item, "input")
		if input == nil {
			input = json.RawMessage("null")
		}
		return &ProviderNativeDisplay{ID: id, Name: name, Input: input}
	case provider == ProviderNativeAnthropic && blockType == "web_search_tool_result":
		id := jsonStr(item, "tool_use_id")
		if id == "" {
			return nil
		}
		content := jsonField(item, "content")
		output, isErr := "", false
		var results []any
		if err := json.Unmarshal(content, &results); err == nil {
			output = formatNativeResults(results)
		} else {
			code := jsonStr(content, "error_code")
			if code == "" {
				code = "unknown_error"
			}
			output = "Web search failed: " + code
			isErr = true
		}
		return &ProviderNativeDisplay{ID: id, Name: ProviderNativeWebSearchTool, Output: &output, IsError: isErr}
	case provider == ProviderNativeOpenAI && blockType == "web_search_call":
		action := jsonField(item, "action")
		query := jsonStr(action, "query")
		if query == "" {
			var queries []any
			if json.Unmarshal(jsonField(action, "queries"), &queries) == nil {
				var parts []string
				for _, q := range queries {
					if s, ok := q.(string); ok {
						parts = append(parts, s)
					}
				}
				query = strings.Join(parts, ", ")
			}
		}
		if query == "" {
			query = jsonStr(action, "url")
		}
		status := jsonStr(item, "status")
		if status == "" {
			status = "completed"
		}
		isErr := status == "failed"
		output := "Web search failed"
		if !isErr {
			switch jsonStr(action, "type") {
			case "open_page":
				output = "Opened page: " + query
			case "find_in_page", "find":
				output = "Searched page: " + query
			default:
				output = "Searched: " + query
			}
		}
		var sources []any
		if json.Unmarshal(jsonField(action, "sources"), &sources) == nil && len(sources) > 0 {
			output += "\n\n" + formatNativeResults(sources)
		}
		return &ProviderNativeDisplay{
			ID: jsonStr(item, "id"), Name: ProviderNativeWebSearchTool,
			Input:   json.RawMessage(fmt.Sprintf(`{"query":%q}`, query)),
			Output:  &output,
			IsError: isErr,
		}
	default:
		return nil
	}
}

const maxDisplayResults = 10

func formatNativeResults(results []any) string {
	out := ""
	count, total := 0, 0
	for _, r := range results {
		total++
		if count >= maxDisplayResults {
			continue
		}
		raw, err := json.Marshal(r)
		if err != nil {
			continue
		}
		url := jsonStr(raw, "url")
		if url == "" {
			continue
		}
		count++
		title := jsonStr(raw, "title")
		if strings.TrimSpace(title) != "" {
			out += fmt.Sprintf("%d. %s\n   %s\n", count, title, url)
		} else {
			out += fmt.Sprintf("%d. %s\n", count, url)
		}
	}
	if count == 0 {
		return "No results"
	}
	if total > count {
		out += fmt.Sprintf("... and %d more\n", total-count)
	}
	return strings.TrimRight(out, " \t\n")
}

const providerNativeSummaryNote = "(summary of an earlier provider-side search; page contents are no longer available)"

// ProviderNativeTextFallback renders the plain-text stand-in for a native item
// that cannot be replayed; "" when nothing is worth keeping. callInput is the
// matching call start's input, for results that don't repeat the query.
func ProviderNativeTextFallback(provider string, item, callInput json.RawMessage) string {
	display := ProviderNativeDisplayOf(provider, item)
	if display == nil || display.Output == nil {
		return ""
	}
	input := display.Input
	if len(input) == 0 || string(input) == "null" {
		input = callInput
	}
	query := jsonStr(input, "query")
	if query != "" {
		return fmt.Sprintf("[%s for %q] %s\n%s", display.Name, query, providerNativeSummaryNote, *display.Output)
	}
	return fmt.Sprintf("[%s results] %s\n%s", display.Name, providerNativeSummaryNote, *display.Output)
}

// ProviderNativeTracker pairs native call starts with their results so display
// consumers can render one tool row per call.
type ProviderNativeTracker struct {
	started map[string]startedNative
}

type startedNative struct {
	name  string
	input json.RawMessage
}

// NewProviderNativeTracker builds an empty tracker.
func NewProviderNativeTracker() *ProviderNativeTracker {
	return &ProviderNativeTracker{started: map[string]startedNative{}}
}

// ProviderNativeToolEvent describes what one observed item means.
type ProviderNativeToolEvent struct {
	Started   bool
	ID        string
	Name      string
	Input     json.RawMessage
	Output    string
	IsError   bool
	Completed bool
}

// Observe one item and describe what happened, if anything.
func (t *ProviderNativeTracker) Observe(provider string, item json.RawMessage) *ProviderNativeToolEvent {
	display := ProviderNativeDisplayOf(provider, item)
	if display == nil {
		return nil
	}
	if display.Output == nil {
		t.started[display.ID] = startedNative{name: display.Name, input: display.Input}
		return &ProviderNativeToolEvent{Started: true, ID: display.ID, Name: display.Name, Input: display.Input}
	}
	input := display.Input
	if len(input) == 0 || string(input) == "null" {
		input = t.CallInput(display.ID)
	}
	delete(t.started, display.ID)
	return &ProviderNativeToolEvent{
		Started: false, Completed: true, ID: display.ID, Name: display.Name,
		Input: input, Output: *display.Output, IsError: display.IsError,
	}
}

// IsStarted reports whether a call start was seen.
func (t *ProviderNativeTracker) IsStarted(id string) bool {
	_, ok := t.started[id]
	return ok
}

// Clear forgets all in-flight calls.
func (t *ProviderNativeTracker) Clear() { t.started = map[string]startedNative{} }

// CallInput returns the recorded input of a started call.
func (t *ProviderNativeTracker) CallInput(id string) json.RawMessage {
	if s, ok := t.started[id]; ok {
		return s.input
	}
	return nil
}
