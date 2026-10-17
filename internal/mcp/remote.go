// Remote MCP transport (ported from jcode-base/src/mcp/remote.rs): the
// streamable-HTTP wire with legacy HTTP+SSE fallback. Requests are POSTed
// with Accept: application/json, text/event-stream; responses may be plain
// JSON or an SSE stream. The `mcp-session-id` response header on initialize
// pins the session; a 404 with an established session means the server
// dropped it. Redirects are never followed (static headers may carry
// credentials to a server-selected destination).
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	mcpSessionHeader  = "Mcp-Session-Id"
	mcpProtocolHeader = "Mcp-Protocol-Version"
	// maxRemoteResponseBytes caps JSON and SSE response bodies.
	maxRemoteResponseBytes = 16 << 20
)

// remoteState carries one remote connection (guarded by the owning
// Client's mutex — the stdio client is fully serialized).
type remoteState struct {
	name      string
	rawURL    string
	headers   map[string]string
	hasAuth   bool
	bearer    string
	sessionID string
	protocol  string
	// legacy fallback state: one GET SSE stream dispatches responses to
	// waiting calls; requests POST to the endpoint event's URL.
	legacy     bool
	endpoint   string
	endpointCh chan string
	sseBody    io.ReadCloser
	pendingMu  sync.Mutex
	ssePending map[int64]chan jsonRPCResponse
	sseNextID  int64
	client     *http.Client
}

// connectRemoteLocked starts a remote connection and runs the initialize
// handshake + tools/list (called with the Client mutex held).
func (c *Client) connectRemoteLocked() error {
	state, err := newRemoteState(c.name, c.config)
	if err != nil {
		return err
	}
	c.remote = state
	if err := state.initialize(); err != nil {
		c.remote = nil
		return fmt.Errorf("mcp server %s initialize failed: %w", c.name, err)
	}
	c.Protocol = state.protocol
	if err := state.notify("notifications/initialized", map[string]any{}); err != nil {
		c.remote = nil
		return err
	}
	result, err := state.call("tools/list", map[string]any{})
	if err != nil {
		c.remote = nil
		return fmt.Errorf("mcp server %s tools/list failed: %w", c.name, err)
	}
	var listing struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(result, &listing); err != nil {
		return err
	}
	c.Tools = listing.Tools
	return nil
}

// newRemoteState validates the URL and builds the HTTP client (no
// redirects, 10s connect budget).
func newRemoteState(name string, config ServerConfig) (*remoteState, error) {
	raw := strings.TrimSpace(config.URL)
	if raw == "" {
		return nil, fmt.Errorf("mcp server '%s' has no url", name)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid MCP url '%s'", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("MCP url must be http(s): %s", raw)
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("MCP URL must not contain userinfo or a fragment")
	}
	state := &remoteState{
		name: name, rawURL: raw,
		headers:    map[string]string{},
		endpointCh: make(chan string, 1),
		ssePending: map[int64]chan jsonRPCResponse{},
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return fmt.Errorf("MCP server '%s' redirected; redirects are not followed", name)
			},
			Timeout: 60 * time.Second,
		},
	}
	for key, value := range config.Headers {
		normalized := strings.ToLower(strings.TrimSpace(key))
		switch normalized {
		case "host", "content-length", "transfer-encoding",
			strings.ToLower(mcpSessionHeader), strings.ToLower(mcpProtocolHeader):
			return nil, fmt.Errorf("header '%s' is controlled by the MCP transport", key)
		}
		if normalized == "authorization" {
			state.hasAuth = true
		}
		state.headers[normalized] = strings.TrimSpace(value)
	}
	if !state.hasAuth {
		// Stored OAuth token for this server URL (may be empty; the server
		// answers 401 and the call fails with the re-auth hint).
		state.bearer = storedAccessToken(raw)
	}
	return state, nil
}

// applyHeaders ports RemoteState::request: static headers, bearer, session,
// negotiated protocol version.
func (s *remoteState) applyHeaders(request *http.Request, withSession bool) {
	for key, value := range s.headers {
		request.Header.Set(key, value)
	}
	if s.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+s.bearer)
	}
	if withSession && s.sessionID != "" {
		request.Header.Set(mcpSessionHeader, s.sessionID)
	}
	if s.protocol != "" {
		request.Header.Set(mcpProtocolHeader, s.protocol)
	}
}

// post sends one JSON-RPC message and returns the response.
func (s *remoteState) post(body []byte, isInitialize bool) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodPost, s.rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	s.applyHeaders(request, !isInitialize)
	return s.client.Do(request)
}

// initialize runs the handshake, taking the session header and switching to
// legacy SSE when the streamable endpoint is unsupported.
func (s *remoteState) initialize() error {
	s.nextInitializeID()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": s.sseNextID, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "gcode", "version": "0.1.0"},
		},
	})
	if err != nil {
		return err
	}
	response, err := s.post(body, true)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusBadRequest ||
		response.StatusCode == http.StatusNotFound ||
		response.StatusCode == http.StatusMethodNotAllowed {
		// Legacy HTTP+SSE transport (2025-03-26 spec predecessor).
		return s.legacyInitialize()
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("HTTP %s: %s", response.Status, readSnippet(response.Body))
	}
	if session := response.Header.Get(mcpSessionHeader); session != "" {
		if !validSessionID(session) {
			return fmt.Errorf("MCP server returned an invalid session ID")
		}
		s.sessionID = session
	}
	result, err := decodeRPCResponse(response)
	if err != nil {
		return err
	}
	var handshake struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(result, &handshake)
	s.protocol = handshake.ProtocolVersion
	return nil
}

func (s *remoteState) nextInitializeID() {
	s.sseNextID++
}

// decodeRPCResponse reads JSON or SSE until the response object arrives.
func decodeRPCResponse(response *http.Response) (json.RawMessage, error) {
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(
		response.Header.Get("Content-Type"), ";", 2)[0]))
	switch {
	case strings.HasPrefix(contentType, "text/event-stream"):
		events, err := readSSEUntil(response.Body, 0, nil)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			return nil, fmt.Errorf("MCP SSE stream ended without a response")
		}
		return events[0], nil
	case contentType == "application/json" || contentType == "":
		raw, err := io.ReadAll(io.LimitReader(response.Body, maxRemoteResponseBytes))
		if err != nil {
			return nil, err
		}
		var envelope jsonRPCResponse
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("MCP returned invalid JSON: %w", err)
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("mcp error %d: %s", envelope.Error.Code, envelope.Error.Message)
		}
		return envelope.Result, nil
	default:
		return nil, fmt.Errorf("MCP server returned unsupported Content-Type: %s", contentType)
	}
}

// call sends a request over streamable HTTP (or the legacy SSE bridge).
func (s *remoteState) call(method string, params any) (json.RawMessage, error) {
	if s.legacy {
		return s.legacyCall(method, params)
	}
	s.sseNextID++
	id := s.sseNextID
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	response, err := s.post(body, false)
	if err != nil {
		return nil, fmt.Errorf("HTTP request to MCP server '%s' failed: %w", s.name, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf(
			"MCP server '%s' rejected the request (401): run `gcode mcp-login %s`",
			s.name, s.rawURL)
	}
	if response.StatusCode == http.StatusNotFound && s.sessionID != "" {
		// Server dropped the session: clear it so the next Connect
		// reinitializes (ported close-with-session-expired).
		s.sessionID = ""
		return nil, fmt.Errorf(
			"MCP server '%s' session expired; reconnect to start a new session", s.name)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("MCP server '%s' returned HTTP %s: %s", s.name,
			response.Status, readSnippet(response.Body))
	}
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent {
		return nil, fmt.Errorf("MCP server accepted a request without a JSON-RPC response")
	}
	return decodeRPCResponse(response)
}

// notify sends a notification (no id, no response expected).
func (s *remoteState) notify(method string, params any) error {
	if s.legacy {
		_, err := s.legacyPost(map[string]any{
			"jsonrpc": "2.0", "method": method, "params": params,
		})
		return err
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	response, err := s.post(body, false)
	if err != nil {
		return fmt.Errorf("HTTP request to MCP server '%s' failed: %w", s.name, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("MCP server '%s' returned HTTP %s: %s", s.name,
			response.Status, readSnippet(response.Body))
	}
	return nil
}

func (s *remoteState) close() {
	if s.sseBody != nil {
		_ = s.sseBody.Close()
		s.sseBody = nil
	}
}

func validSessionID(session string) bool {
	if session == "" {
		return false
	}
	for _, b := range []byte(session) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}

func readSnippet(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, 512))
	if err != nil {
		return "(unreadable)"
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}

// sseEvent is one parsed server-sent event.
type sseEvent struct {
	event string
	data  string
}

// sseParser is the incremental text/event-stream parser.
type sseParser struct {
	line strings.Builder
	kind string
	data strings.Builder
}

// push consumes a chunk and returns completed events.
func (p *sseParser) push(chunk []byte) ([]sseEvent, error) {
	var events []sseEvent
	for _, raw := range strings.Split(string(chunk), "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case line == "":
			if p.data.Len() > 0 {
				events = append(events, sseEvent{event: p.kind, data: p.data.String()})
			}
			p.kind, p.data = "", strings.Builder{}
		case strings.HasPrefix(line, ":"):
			// comment
		case strings.HasPrefix(line, "event:"):
			p.kind = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if p.data.Len() > 0 {
				p.data.WriteString("\n")
			}
			p.data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return events, nil
}

// readSSEUntil parses SSE frames until filter accepts a message event with
// the requested id (or the stream ends). Returns accepted results in order.
func readSSEUntil(body io.Reader, wantID int64, accept func(jsonRPCResponse) bool) ([]json.RawMessage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxRemoteResponseBytes)
	parser := &sseParser{}
	var results []json.RawMessage
	for scanner.Scan() {
		events, err := parser.push(scanner.Bytes())
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			if event.event != "" && event.event != "message" {
				continue
			}
			var envelope jsonRPCResponse
			if err := json.Unmarshal([]byte(event.data), &envelope); err != nil {
				continue
			}
			if envelope.Error != nil {
				return nil, fmt.Errorf("mcp error %d: %s",
					envelope.Error.Code, envelope.Error.Message)
			}
			if wantID == 0 || envelope.ID == wantID ||
				(accept != nil && accept(envelope)) {
				results = append(results, envelope.Result)
				if wantID != 0 {
					return results, nil
				}
			}
		}
	}
	return results, scanner.Err()
}

// legacyInitialize ports the old HTTP+SSE handshake: GET an SSE stream,
// the `endpoint` event names the POST target.
func (s *remoteState) legacyInitialize() error {
	request, err := http.NewRequest(http.MethodGet, s.rawURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	s.applyHeaders(request, false)
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("legacy SSE connect failed: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		defer response.Body.Close()
		return fmt.Errorf("legacy SSE connect returned HTTP %s: %s",
			response.Status, readSnippet(response.Body))
	}
	s.legacy = true
	s.sseBody = response.Body
	s.ssePending = map[int64]chan jsonRPCResponse{}
	go s.legacyReadLoop()
	var endpoint string
	select {
	case endpoint = <-s.endpointCh:
	case <-time.After(10 * time.Second):
	}
	if endpoint == "" {
		s.close()
		return fmt.Errorf("MCP server '%s' legacy SSE stream sent no endpoint event", s.name)
	}
	resolved, err := url.Parse(s.rawURL)
	if err != nil {
		return err
	}
	target, err := resolved.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid legacy endpoint %s: %w", endpoint, err)
	}
	s.endpoint = target.String()
	if err := s.legacyInitializeHandshake(); err != nil {
		return err
	}
	return nil
}

func (s *remoteState) legacyInitializeHandshake() error {
	s.sseNextID++
	id := s.sseNextID
	result, err := s.legacyCallID(id, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "gcode", "version": "0.1.0"},
	})
	if err != nil {
		return err
	}
	var handshake struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(result, &handshake)
	s.protocol = handshake.ProtocolVersion
	_, err = s.legacyPost(map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	})
	return err
}

// legacyReadLoop dispatches SSE messages to waiting calls.
func (s *remoteState) legacyReadLoop() {
	scanner := bufio.NewScanner(s.sseBody)
	scanner.Buffer(make([]byte, 64*1024), maxRemoteResponseBytes)
	parser := &sseParser{}
	for scanner.Scan() {
		events, err := parser.push(scanner.Bytes())
		if err != nil {
			break
		}
		for _, event := range events {
			if event.event == "endpoint" {
				select {
				case s.endpointCh <- strings.TrimSpace(event.data):
				default:
				}
				continue
			}
			if event.event != "" && event.event != "message" {
				continue
			}
			var envelope jsonRPCResponse
			if err := json.Unmarshal([]byte(event.data), &envelope); err != nil {
				continue
			}
			id := envelope.ID
			s.pendingMu.Lock()
			wait, ok := s.ssePending[id]
			if ok {
				delete(s.ssePending, id)
			}
			s.pendingMu.Unlock()
			if ok {
				wait <- envelope
				close(wait)
			}
		}
	}
	// Stream closed: fail every waiter.
	s.pendingMu.Lock()
	for id, wait := range s.ssePending {
		close(wait)
		delete(s.ssePending, id)
	}
	s.pendingMu.Unlock()
}

// legacyPost sends one message to the endpoint URL; responses arrive on
// the SSE stream, so the POST body is drained and discarded.
func (s *remoteState) legacyPost(message map[string]any) (*http.Response, error) {
	body, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequest(http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	s.applyHeaders(request, false)
	return s.client.Do(request)
}

// legacyCall registers a waiter, posts, and waits for the SSE response.
func (s *remoteState) legacyCallID(id int64, method string, params any) (json.RawMessage, error) {
	wait := make(chan jsonRPCResponse, 1)
	s.pendingMu.Lock()
	s.ssePending[id] = wait
	s.pendingMu.Unlock()
	if _, err := s.legacyPost(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		s.pendingMu.Lock()
		delete(s.ssePending, id)
		s.pendingMu.Unlock()
		return nil, fmt.Errorf("HTTP request to MCP server '%s' failed: %w", s.name, err)
	}
	select {
	case envelope, ok := <-wait:
		if !ok {
			return nil, fmt.Errorf("MCP server '%s' closed the SSE stream without answering", s.name)
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("mcp error %d: %s", envelope.Error.Code, envelope.Error.Message)
		}
		return envelope.Result, nil
	case <-time.After(60 * time.Second):
		s.pendingMu.Lock()
		delete(s.ssePending, id)
		s.pendingMu.Unlock()
		return nil, fmt.Errorf("MCP server '%s' request timed out", s.name)
	}
}

func (s *remoteState) legacyCall(method string, params any) (json.RawMessage, error) {
	s.sseNextID++
	return s.legacyCallID(s.sseNextID, method, params)
}
