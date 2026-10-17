// Package mcp ports jcode-base/src/mcp: the MCP client core (JSON-RPC 2.0
// over stdio and streamable HTTP with legacy SSE fallback), dispatch naming,
// and the searchable tool catalog. OAuth token storage follows RFC 9728/8414
// discovery (mcp/oauth.go).
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// MaxDispatchNameLen is the longest tool name every supported provider
// accepts (OpenAI caps at 64, Anthropic at 128).
const MaxDispatchNameLen = 64

// ToolDef is one MCP tool exposed by a server.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ServerConfig is one [mcp_servers.NAME] entry.
type ServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// dispatchName is the model-facing name for an MCP tool: mcp__server__tool
// with every character outside [A-Za-z0-9_] normalized to '_', hyphens
// included (historical spelling), and over-long names truncated with a
// stable hash suffix.
func DispatchName(serverName, toolName string) string {
	raw := fmt.Sprintf("mcp__%s__%s", serverName, toolName)
	var name strings.Builder
	for _, c := range raw {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			name.WriteRune(c)
		} else {
			name.WriteByte('_')
		}
	}
	result := name.String()
	if len(result) > MaxDispatchNameLen {
		suffix := fmt.Sprintf("_%08x", stableDispatchHash(serverName, toolName))
		result = result[:MaxDispatchNameLen-len(suffix)] + suffix
	}
	return result
}

// stableDispatchHash is FNV-1a over server\0tool (ported byte-for-byte).
func stableDispatchHash(serverName, toolName string) uint32 {
	hash := uint32(0x811c9dc5)
	for _, b := range append([]byte(serverName), append([]byte{0}, []byte(toolName)...)...) {
		hash ^= uint32(b)
		hash *= 0x01000193
	}
	return hash
}

// DispatchNames assigns unique registry keys to a full tool surface: the
// base spelling when unique, a `-2`-style suffix for collisions.
func DispatchNames(tools []CatalogEntry) []string {
	bases := make([]string, len(tools))
	counts := map[string]int{}
	for i, entry := range tools {
		bases[i] = DispatchName(entry.Server, entry.Tool.Name)
		counts[bases[i]]++
	}
	ordered := make([]int, len(tools))
	for i := range ordered {
		ordered[i] = i
	}
	sort.Slice(ordered, func(a, b int) bool {
		if tools[ordered[a]].Server != tools[ordered[b]].Server {
			return tools[ordered[a]].Server < tools[ordered[b]].Server
		}
		return tools[ordered[a]].Tool.Name < tools[ordered[b]].Tool.Name
	})
	names := make([]string, len(tools))
	used := map[string]bool{}
	for _, index := range ordered {
		base := bases[index]
		if counts[base] == 1 && !used[base] {
			names[index] = base
			used[base] = true
			continue
		}
		suffix := 2
		for {
			candidate := fmt.Sprintf("%s-%d", base, suffix)
			if !used[candidate] {
				names[index] = candidate
				used[candidate] = true
				break
			}
			suffix++
		}
	}
	return names
}

// CatalogEntry pairs a server name with its tool definition.
type CatalogEntry struct {
	Server string  `json:"server"`
	Tool   ToolDef `json:"tool"`
}

// jsonRPCRequest is one JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Client is one MCP server connection over stdio (newline-delimited
// JSON-RPC 2.0, the MCP stdio transport).
type Client struct {
	name     string
	config   ServerConfig
	cmd      *exec.Cmd
	stdin    *json.Encoder
	stdout   *bufio.Reader
	mu       sync.Mutex
	nextID   int64
	Tools    []ToolDef
	Protocol string
	// remote is set for URL-configured servers (streamable HTTP transport
	// with legacy SSE fallback); nil means stdio.
	remote *remoteState
}

// NewClient prepares (but does not start) a stdio connection.
func NewClient(name string, config ServerConfig) *Client {
	return &Client{name: name, config: config}
}

// Connect spawns the server process and performs the initialize handshake.
// URL-configured servers use the remote HTTP transport instead.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config.URL != "" {
		return c.connectRemoteLocked()
	}
	if c.config.Command == "" {
		return fmt.Errorf("mcp server %s has no command", c.name)
	}
	cmd := exec.Command(c.config.Command, c.config.Args...)
	cmd.Env = os.Environ()
	for key, value := range c.config.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp server %s failed to start: %w", c.name, err)
	}
	c.cmd = cmd
	c.stdin = json.NewEncoder(stdinPipe)
	c.stdout = bufio.NewReader(stdoutPipe)

	result, err := c.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "gcode", "version": "0.1.0"},
	})
	if err != nil {
		_ = c.cmd.Process.Kill()
		return fmt.Errorf("mcp server %s initialize failed: %w", c.name, err)
	}
	var handshake struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(result, &handshake)
	c.Protocol = handshake.ProtocolVersion
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		return err
	}

	toolsResult, err := c.call("tools/list", map[string]any{})
	if err != nil {
		return fmt.Errorf("mcp server %s tools/list failed: %w", c.name, err)
	}
	var listing struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(toolsResult, &listing); err != nil {
		return err
	}
	c.Tools = listing.Tools
	return nil
}

// Close terminates the server process.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remote != nil {
		c.remote.close()
		c.remote = nil
		return nil
	}
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
	c.cmd = nil
	return nil
}

func (c *Client) call(method string, params any) (json.RawMessage, error) {
	if c.remote != nil {
		return c.remote.call(method, params)
	}
	c.nextID++
	request := jsonRPCRequest{JSONRPC: "2.0", ID: c.nextID, Method: method, Params: params}
	if err := c.stdin.Encode(request); err != nil {
		return nil, err
	}
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("mcp server %s closed mid-response: %w", c.name, err)
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var response jsonRPCResponse
		if err := json.Unmarshal(line, &response); err != nil {
			continue // notifications and foreign lines are skipped
		}
		if response.Error != nil {
			return nil, fmt.Errorf("mcp error %d: %s", response.Error.Code, response.Error.Message)
		}
		return response.Result, nil
	}
}

func (c *Client) notify(method string, params any) error {
	if c.remote != nil {
		return c.remote.notify(method, params)
	}
	return c.stdin.Encode(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	})
}

// CallTool invokes one tool and returns its content blocks.
func (c *Client) CallTool(toolName string, arguments json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if arguments == nil {
		arguments = json.RawMessage("{}")
	}
	return c.call("tools/call", map[string]any{
		"name": toolName, "arguments": json.RawMessage(arguments),
	})
}

// Manager owns the configured server connections.
type Manager struct {
	mu       sync.Mutex
	servers  map[string]*Client
	order    []string
	LoadFunc func() map[string]ServerConfig
}

// NewManager builds a manager that loads [mcp_servers.*] from the config.
func NewManager() *Manager {
	return &Manager{servers: map[string]*Client{}, LoadFunc: loadConfiguredServers}
}

// loadConfiguredServers reads gcode config.toml [mcp_servers] tables.
func loadConfiguredServers() map[string]ServerConfig {
	out := map[string]ServerConfig{}
	home := os.Getenv("GCODE_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".gcode")
	}
	raw, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return out
	}
	// Minimal TOML table scan: [mcp_servers.NAME] with command/args/url keys.
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.Trim(trimmed, "[]")
			continue
		}
		if !strings.HasPrefix(section, "mcp_servers.") {
			continue
		}
		name := strings.TrimPrefix(section, "mcp_servers.")
		entry := out[name]
		key, value, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"`)
		switch key {
		case "command":
			entry.Command = value
		case "url":
			entry.URL = value
		case "args":
			var args []string
			if err := json.Unmarshal([]byte(value), &args); err == nil {
				entry.Args = args
			}
		case "headers":
			var headers map[string]string
			if err := json.Unmarshal([]byte(value), &headers); err == nil {
				entry.Headers = headers
			}
		}
		out[name] = entry
	}
	return out
}

// Connect starts one server and caches its tool list.
func (m *Manager) Connect(name string) ([]ToolDef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if client, ok := m.servers[name]; ok {
		return client.Tools, nil
	}
	configs := m.LoadFunc()
	config, ok := configs[name]
	if !ok {
		return nil, fmt.Errorf("unknown MCP server: %s", name)
	}
	client := NewClient(name, config)
	if err := client.Connect(); err != nil {
		return nil, err
	}
	m.servers[name] = client
	m.order = append(m.order, name)
	return client.Tools, nil
}

// Disconnect stops one server.
func (m *Manager) Disconnect(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	client, ok := m.servers[name]
	if !ok {
		return fmt.Errorf("MCP server '%s' is not connected", name)
	}
	_ = client.Close()
	delete(m.servers, name)
	for i, existing := range m.order {
		if existing == name {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return nil
}

// DisconnectAll stops every server (session teardown).
func (m *Manager) DisconnectAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, client := range m.servers {
		_ = client.Close()
		delete(m.servers, name)
	}
	m.order = nil
}

// Connected lists connected server names in connect order.
func (m *Manager) Connected() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.order...)
}

// CallTool routes a call to a connected server.
func (m *Manager) CallTool(server, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	m.mu.Lock()
	client, ok := m.servers[server]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("MCP server '%s' is not connected; connect it first", server)
	}
	return client.CallTool(tool, arguments)
}

// SearchableTools returns the connected catalog in connect order.
func (m *Manager) SearchableTools() []CatalogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var catalog []CatalogEntry
	for _, name := range m.order {
		for _, tool := range m.servers[name].Tools {
			catalog = append(catalog, CatalogEntry{Server: name, Tool: tool})
		}
	}
	return catalog
}
