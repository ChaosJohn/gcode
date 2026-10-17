package anthropic

import (
	"encoding/json"

	"github.com/ChaosJohn/gcode/internal/base/config"
	"github.com/ChaosJohn/gcode/internal/types"
)

// Anthropic server-side web_search tool (ported from native_web_search.rs).

// basicWebSearchToolVersion is the first tool version; later versions default
// `allowed_callers` to code execution and must be pinned to direct calls.
const basicWebSearchToolVersion = "web_search_20250305"

// NativeWebSearchServerTool builds the server tool definition from config.
func NativeWebSearchServerTool(webSearch config.WebSearchConfig) json.RawMessage {
	version := webSearch.NativeAnthropicToolVersion
	if version == "" {
		version = basicWebSearchToolVersion
	}
	bld := &jsonBuilder{}
	bld.strKey("type", version)
	bld.strKey("name", "web_search")
	if webSearch.NativeMaxUses != nil && *webSearch.NativeMaxUses > 0 {
		bld.rawKey("max_uses", itoa(int(*webSearch.NativeMaxUses)))
	}
	// The API rejects a request that sets both lists, so allowed wins.
	allowed := cleanDomains(webSearch.NativeAllowedDomains)
	blocked := cleanDomains(webSearch.NativeBlockedDomains)
	if len(allowed) > 0 {
		if raw, err := types.MarshalCompact(allowed); err == nil {
			bld.rawKey("allowed_domains", string(raw))
		}
	} else if len(blocked) > 0 {
		if raw, err := types.MarshalCompact(blocked); err == nil {
			bld.rawKey("blocked_domains", string(raw))
		}
	}
	// Newer versions call through code execution by default, which 400s on
	// models without programmatic tool calling. Direct calls work everywhere.
	if version != basicWebSearchToolVersion {
		bld.rawKey("allowed_callers", `["direct"]`)
	}
	return json.RawMessage(bld.bytes())
}

func cleanDomains(domains []string) []string {
	var out []string
	for _, domain := range domains {
		trimmed := trimSpace(domain)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// NativeEnabled mirrors WebSearchConfig::native_enabled.
func NativeEnabled(webSearch config.WebSearchConfig) bool {
	return webSearch.PreferNative || webSearch.Engine == config.WebSearchNative
}

// ServerToolsWithConfig returns the server tools to attach (native engine +
// first-party API only).
func ServerToolsWithConfig(webSearch config.WebSearchConfig, firstParty bool) []json.RawMessage {
	if NativeEnabled(webSearch) && firstParty {
		return []json.RawMessage{NativeWebSearchServerTool(webSearch)}
	}
	return nil
}

// SessionOffersWebsearch reports whether the session's tool list includes the
// built-in local search tool.
func SessionOffersWebsearch(tools []types.ToolDefinition) bool {
	for _, tool := range tools {
		if types.IsBuiltinLocalWebsearch(tool) {
			return true
		}
	}
	return false
}

// ServerToolsForRequest attaches the server tool only when the session offers
// websearch at all (the server tool replaces it, never adds search).
func ServerToolsForRequest(webSearch config.WebSearchConfig, firstParty bool, tools []types.ToolDefinition) []json.RawMessage {
	if !SessionOffersWebsearch(tools) {
		return nil
	}
	return ServerToolsWithConfig(webSearch, firstParty)
}

// WithoutLocalWebsearch drops jcode's local search tool when the server tool
// replaces it, so the model has exactly one way to search. Done before
// formatting so the prompt-cache breakpoint lands on the last remaining tool.
func WithoutLocalWebsearch(tools []types.ToolDefinition, serverTools []json.RawMessage) []types.ToolDefinition {
	if len(serverTools) == 0 {
		return tools
	}
	out := make([]types.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if !types.IsBuiltinLocalWebsearch(tool) {
			out = append(out, tool)
		}
	}
	return out
}
