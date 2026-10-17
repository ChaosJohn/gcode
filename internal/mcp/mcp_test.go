// End-to-end stdio MCP client test against a fake server, plus manager and
// config-table parsing coverage (port of jcode-base/src/mcp protocol/client
// happy paths).
package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeServerScript speaks the MCP stdio transport: initialize handshake,
// tools/list with two tools, tools/call echo.
const fakeServerScript = `#!/bin/sh
while IFS= read -r line; do
  ID=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *initialize*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}\n' "$ID" ;;
    *tools/list*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo","description":"Echo back","inputSchema":{"type":"object"}},{"name":"hire.create","description":"Create a job","inputSchema":{"type":"object"}}]}}\n' "$ID" ;;
    *tools/call*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"called"}],"isError":false}}\n' "$ID" ;;
    *)
      printf '{"jsonrpc":"2.0","method":"notifications/unknown"}\n' ;;
  esac
done
`

func TestStdioClientEndToEnd(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake_mcp.sh")
	if err := os.WriteFile(script, []byte(fakeServerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	client := NewClient("fake", ServerConfig{Command: "/bin/sh", Args: []string{script}})
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.Protocol != "2024-11-05" {
		t.Fatalf("protocol: %s", client.Protocol)
	}
	if len(client.Tools) != 2 || client.Tools[0].Name != "echo" {
		t.Fatalf("tools: %+v", client.Tools)
	}
	if got := DispatchName("fake", "echo"); got != "mcp__fake__echo" {
		t.Fatalf("dispatch: %s", got)
	}
	if got := DispatchName("fake", "hire.create"); got != "mcp__fake__hire_create" {
		t.Fatalf("dots: %s", got)
	}
	result, err := client.CallTool("echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "called") {
		t.Fatalf("call result: %s", string(result))
	}
}

func TestManagerConfigParsing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GCODE_HOME", home)
	config := `[model]
name = "test"

[mcp_servers.github]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]

[mcp_servers.remote]
url = "https://mcp.example.test/sse"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	servers := loadConfiguredServers()
	if len(servers) != 2 {
		t.Fatalf("servers: %+v", servers)
	}
	if servers["github"].Command != "npx" || len(servers["github"].Args) != 2 {
		t.Fatalf("github: %+v", servers["github"])
	}
	if servers["remote"].URL != "https://mcp.example.test/sse" {
		t.Fatalf("remote: %+v", servers["remote"])
	}
}

func TestManagerConnectAndCatalog(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake_mcp.sh")
	if err := os.WriteFile(script, []byte(fakeServerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("GCODE_HOME", home)
	config := "[mcp_servers.fake]\ncommand = \"/bin/sh\"\nargs = [\"" + script + "\"]\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	tools, err := manager.Connect("fake")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools: %d", len(tools))
	}
	catalog := manager.SearchableTools()
	names := DispatchNames(catalog)
	if len(names) != 2 || names[0] != "mcp__fake__echo" {
		t.Fatalf("catalog names: %v", names)
	}
	result, err := manager.CallTool("fake", "echo", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(string(result), "called") {
		t.Fatalf("manager call: %v %s", err, string(result))
	}
	if err := manager.Disconnect("fake"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disconnect("fake"); err == nil {
		t.Fatalf("double disconnect must error")
	}
	// The JSON-RPC request framing is newline-delimited with id/params.
	var request struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	line := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo"}}`
	if err := json.Unmarshal([]byte(line), &request); err != nil {
		t.Fatal(err)
	}
	if request.JSONRPC != "2.0" || request.ID != 7 {
		t.Fatalf("framing: %+v", request)
	}
}
