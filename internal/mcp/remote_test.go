// Remote transport + OAuth tests: streamable HTTP (JSON and SSE replies),
// session header capture and expiry, legacy SSE fallback, static header
// injection, redirect rejection, and the OAuth discovery/refresh chain.
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func rpcResult(id int64, result any) string {
	encoded, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "result": result,
	})
	return string(encoded)
}

// fakeStreamableServer: initialize assigns a session; tools/list returns
// JSON or SSE depending on the switch; notifications 202.
type fakeStreamableServer struct {
	mu          sync.Mutex
	toolsAsSSE  bool
	seenSession string
	seenAuth    string
	// initSession records the session header on the initialize request only.
	initSession string
	seenHeaders map[string]string
	protocol    string
}

func (f *fakeStreamableServer) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		ID     *int64          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&request)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seenSession = r.Header.Get("Mcp-Session-Id")
	f.seenAuth = r.Header.Get("Authorization")
	if request.Method == "initialize" {
		f.initSession = f.seenSession
	}
	if f.seenHeaders == nil {
		f.seenHeaders = map[string]string{}
	}
	for key := range r.Header {
		f.seenHeaders[strings.ToLower(key)] = r.Header.Get(key)
	}
	if request.Method == "initialize" {
		w.Header().Set("Mcp-Session-Id", "sess-123")
		w.Header().Set("Content-Type", "application/json")
		protocol := "2024-11-05"
		f.protocol = protocol
		fmt.Fprint(w, rpcResult(*request.ID, map[string]any{
			"protocolVersion": protocol,
		}))
		return
	}
	if request.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if request.Method == "tools/list" {
		if f.toolsAsSSE {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", rpcResult(*request.ID, map[string]any{
				"tools": []map[string]any{{
					"name": "remote_echo", "description": "Echo from remote.",
					"inputSchema": map[string]any{"type": "object"},
				}},
			}))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, rpcResult(*request.ID, map[string]any{
			"tools": []map[string]any{{
				"name": "remote_echo", "description": "Echo from remote.",
				"inputSchema": map[string]any{"type": "object"},
			}},
		}))
		return
	}
	if request.Method == "tools/call" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, rpcResult(*request.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "remote says hi"}},
		}))
		return
	}
	w.WriteHeader(http.StatusBadRequest)
}

func TestRemoteStreamableJSON(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	fake := &fakeStreamableServer{}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer server.Close()

	client := NewClient("remote", ServerConfig{URL: server.URL})
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if fake.initSession != "" {
		t.Fatalf("initialize must not carry a stale session: %q", fake.initSession)
	}
	if len(client.Tools) != 1 || client.Tools[0].Name != "remote_echo" {
		t.Fatalf("tools: %+v", client.Tools)
	}
	if client.Protocol != "2024-11-05" {
		t.Fatalf("protocol: %s", client.Protocol)
	}
	// Subsequent calls carry the session.
	result, err := client.CallTool("remote_echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if fake.seenSession != "sess-123" {
		t.Fatalf("session header: %q", fake.seenSession)
	}
	var payload struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Content[0].Text != "remote says hi" {
		t.Fatalf("call: %s", string(result))
	}
}

func TestRemoteStreamableSSEResponse(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	fake := &fakeStreamableServer{toolsAsSSE: true}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer server.Close()
	client := NewClient("remote", ServerConfig{URL: server.URL})
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if len(client.Tools) != 1 {
		t.Fatalf("tools over SSE: %+v", client.Tools)
	}
}

// Static headers reach the server; a static Authorization overrides OAuth.
func TestRemoteStaticHeaders(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	fake := &fakeStreamableServer{}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer server.Close()
	client := NewClient("remote", ServerConfig{URL: server.URL,
		Headers: map[string]string{
			"Authorization": "Bearer static-key",
			"X-Custom":      "custom-value",
		}})
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if fake.seenAuth != "Bearer static-key" {
		t.Fatalf("auth: %q", fake.seenAuth)
	}
	if fake.seenHeaders["x-custom"] != "custom-value" {
		t.Fatalf("custom header: %v", fake.seenHeaders)
	}
	// A transport-controlled header is rejected up front (ported bail).
	blocked := NewClient("blocked", ServerConfig{URL: server.URL,
		Headers: map[string]string{"Mcp-Session-Id": "hijack-attempt"}})
	if err := blocked.Connect(); err == nil ||
		!strings.Contains(err.Error(), "controlled by the MCP transport") {
		t.Fatalf("session header: %v", err)
	}
}

// A redirect is rejected rather than followed with credentials.
func TestRemoteRejectsRedirect(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer redirected.Close()
	client := NewClient("remote", ServerConfig{URL: redirected.URL})
	if err := client.Connect(); err == nil ||
		!strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect: %v", err)
	}
}

// 404 with an established session reports expiry and clears the session.
func TestRemoteSessionExpired(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	var dropped atomicBool
	fake := &fakeStreamableServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dropped.Load() && r.Header.Get("Mcp-Session-Id") != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fake.handler(w, r)
	}))
	defer server.Close()
	client := NewClient("remote", ServerConfig{URL: server.URL})
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	dropped.Store(true)
	if _, err := client.CallTool("remote_echo", json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "session expired") {
		t.Fatalf("expiry: %v", err)
	}
}

type atomicBool struct {
	mu  sync.Mutex
	val bool
}

func (b *atomicBool) Store(v bool) { b.mu.Lock(); b.val = v; b.mu.Unlock() }
func (b *atomicBool) Load() bool   { b.mu.Lock(); defer b.mu.Unlock(); return b.val }

// Legacy SSE fallback: initialize 405 → GET stream with endpoint event.
func TestRemoteLegacySSEFallback(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	var mu sync.Mutex
	var postedID int64
	endpointPath := "/message-endpoint"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == endpointPath {
			var request struct {
				ID     *int64 `json:"id"`
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			mu.Lock()
			if request.ID != nil {
				postedID = *request.ID
			}
			gotID := postedID
			mu.Unlock()
			if request.ID != nil {
				// Replies ride the SSE stream, not the POST response.
				w.WriteHeader(http.StatusAccepted)
				return
			}
			_ = gotID
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// Streamable POST endpoint is unsupported → legacy fallback.
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: endpoint\ndata: %s\r\n\r\n", endpointPath)
		flusher.Flush()
		// Answer every POSTed request on the stream and keep the stream
		// open until the client disconnects (requests arrive sequentially
		// under the client mutex: initialize, then tools/list).
		answered := map[int64]bool{}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			mu.Lock()
			got := postedID
			mu.Unlock()
			if got != 0 && !answered[got] {
				answered[got] = true
				fmt.Fprintf(w, "event: message\ndata: %s\r\n\r\n",
					rpcResult(got, map[string]any{"tools": []map[string]any{}}))
				flusher.Flush()
			}
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient("legacy", ServerConfig{URL: server.URL})
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
}

// OAuth: discovery chain + refresh grant against fake endpoints.
func TestOAuthDiscoveryAndRefresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GCODE_HOME", home)
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "refresh_token" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.FormValue("refresh_token") != "stale-refresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"fresh-token","refresh_token":"new-refresh","expires_in":3600}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// The issuer's RFC 8414 document, served by the issuer origin.
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"token_endpoint":%q}`, server.URL, server.URL+"/token")
	})
	// A separate origin whose protected-resource metadata points at the
	// issuer (RFC 9728 discovery hop).
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"authorization_server":%q}`, server.URL)
	})
	discoveryServer := httptest.NewServer(mux2)
	defer discoveryServer.Close()

	// Seed an expiring token with a refresh token.
	if err := StoreToken(StoredToken{
		ServerURL: discoveryServer.URL + "/mcp", AccessToken: "old",
		RefreshToken: "stale-refresh", ClientID: "client-1",
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	token := storedAccessToken(discoveryServer.URL + "/mcp")
	if token != "fresh-token" {
		t.Fatalf("refreshed token: %q", token)
	}
	store := LoadAuthStore()
	updated := store[discoveryServer.URL+"/mcp"]
	if updated.RefreshToken != "new-refresh" || updated.AccessToken != "fresh-token" {
		t.Fatalf("store: %+v", updated)
	}
	// A valid token short-circuits without network.
	if err := StoreToken(StoredToken{
		ServerURL: discoveryServer.URL + "/mcp2", AccessToken: "cached",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if storedAccessToken(discoveryServer.URL+"/mcp2") != "cached" {
		t.Fatal("valid token not used")
	}
	// Store file is 0600.
	info, err := os.Stat(filepath.Join(home, "mcp-auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions: %v", info.Mode().Perm())
	}
}
