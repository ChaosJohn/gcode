// Package server ports the harness API surface (jcode-harness-api +
// jcode-harness-api-server, 11.9k lines) as a minimal unix-socket daemon:
// newline-delimited JSON requests with id correlation, streaming events
// carrying session_id, and the upstream event names (text_delta, tool_start,
// tool_end, …). Out of scope: the ~20 unsupported request kinds, presence
// probing, swarm/permission flows — see the batch report.
package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ChaosJohn/gcode/internal/agent"
	"github.com/ChaosJohn/gcode/internal/appcore"
	"github.com/ChaosJohn/gcode/internal/provider"
	"github.com/ChaosJohn/gcode/internal/sessionstore"
	"github.com/ChaosJohn/gcode/internal/types"
)

// APIVersion mirrors the upstream protocol version.
const APIVersion = 1

// Server is the harness API daemon.
type Server struct {
	listener net.Listener
	mu       sync.Mutex
	sessions map[string]*sessionHandle
}

// sessionHandle is one attached session.
type sessionHandle struct {
	ID         string
	WorkingDir string
	Agent      *agent.Agent
	Store      *sessionstore.Store
	mu         sync.Mutex
}

// envelope is one framed JSON line.
type envelope map[string]any

// NewServer prepares the daemon.
func NewServer() *Server {
	return &Server{sessions: map[string]*sessionHandle{}}
}

// SocketPath resolves the runtime socket path (jcode-harness-api sockets.rs
// rules, gcode naming): $GCODE_RUNTIME_DIR | $XDG_RUNTIME_DIR | TMPDIR
// fallback `gcode-<user>/api.sock`.
func SocketPath() string {
	if dir := os.Getenv("GCODE_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "api.sock")
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "gcode-runtime", "api.sock")
	}
	dir := os.Getenv("TMPDIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, fmt.Sprintf("gcode-%s", os.Getenv("USER")), "api.sock")
}

// Listen binds the socket, replacing a stale one.
func (s *Server) Listen() error {
	path := SocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		// A live daemon answers ping; leave it alone.
		if conn, dialErr := net.Dial("unix", path); dialErr == nil {
			conn.Close()
			return fmt.Errorf("another daemon is listening on %s", path)
		}
		_ = os.Remove(path)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	s.listener = listener
	return nil
}

// Serve accepts connections until the listener closes.
func (s *Server) Serve() error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return err
		}
		go s.handleConnection(conn)
	}
}

// Close shuts the listener down and removes the socket.
func (s *Server) Close() {
	if s.listener != nil {
		path := s.listener.Addr().String()
		_ = s.listener.Close()
		_ = os.Remove(path)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()
	writer := bufio.NewWriter(conn)
	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 64*1024), 16*1024*1024)
	writeMu := &sync.Mutex{}
	send := func(reply envelope) {
		encoded, err := json.Marshal(reply)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		writer.Write(encoded)
		writer.WriteByte('\n')
		writer.Flush()
	}
	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		if line == "" {
			continue
		}
		var request envelope
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			send(envelope{"type": "error", "code": "bad_request",
				"message": "invalid JSON line"})
			continue
		}
		requestType, _ := request["type"].(string)
		id, _ := request["id"].(float64)
		if !s.dispatch(requestType, int64(id), request, send) {
			return
		}
	}
}

// dispatch handles one request; false closes the connection.
func (s *Server) dispatch(requestType string, id int64, request envelope,
	send func(envelope)) bool {
	switch requestType {
	case "ping":
		send(envelope{"type": "pong", "id": id,
			"capabilities": []string{"sessions", "history", "send_message"},
			"server":       fmt.Sprintf("gcode/%s", "0.1.0")})
	case "hello":
		send(envelope{"type": "hello_ok", "id": id, "version": APIVersion,
			"server":       "gcode/0.1.0",
			"capabilities": []string{"sessions", "history", "send_message"}})
	case "list_sessions":
		send(envelope{"type": "sessions", "id": id,
			"sessions": s.listSessions(request)})
	case "create_session":
		handle := s.createSession(request)
		send(envelope{"type": "attached", "id": id, "session": sessionInfo(handle)})
	case "attach_session":
		handle, err := s.attachSession(str(request, "session_id"))
		if err != nil {
			send(envelope{"type": "error", "id": id, "code": "not_found",
				"message": err.Error()})
			return true
		}
		send(envelope{"type": "attached", "id": id, "session": sessionInfo(handle)})
	case "get_history":
		handle := s.handleFor(str(request, "session_id"))
		if handle == nil {
			send(envelope{"type": "error", "id": id, "code": "not_found",
				"message": "session not attached"})
			return true
		}
		send(envelope{"type": "history", "id": id,
			"session_id": handle.ID, "messages": historyMessages(handle)})
	case "send_message":
		s.sendMessage(id, request, send)
	case "cancel":
		// Cancellation points do not exist in the agent loop yet.
		send(envelope{"type": "error", "id": id, "code": "unsupported",
			"message": "cancel is not supported in this daemon version"})
	default:
		send(envelope{"type": "error", "id": id, "code": "unsupported",
			"message": fmt.Sprintf("unknown request type %q", requestType)})
	}
	return true
}

func str(request envelope, key string) string {
	value, _ := request[key].(string)
	return value
}

// listSessions returns persisted sessions newest-first (limit default 20).
func (s *Server) listSessions(request envelope) []envelope {
	limit := 20
	if raw, ok := request["limit"].(float64); ok && raw > 0 {
		limit = int(raw)
	}
	list, err := appcore.ListSessions(appcore.GcodeHome())
	if err != nil {
		return []envelope{}
	}
	sort.SliceStable(list, func(a, b int) bool {
		if list[a].UpdatedAt == nil || list[b].UpdatedAt == nil {
			return list[b].UpdatedAt == nil
		}
		return list[a].UpdatedAt.After(*list[b].UpdatedAt)
	})
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]envelope, 0, len(list))
	for _, session := range list {
		updated := ""
		if session.UpdatedAt != nil {
			updated = session.UpdatedAt.Format("2006-01-02T15:04:05Z")
		}
		out = append(out, envelope{
			"session_id":    session.ID,
			"custom_title":  session.CustomTitle,
			"status":        session.Status,
			"message_count": len(session.Messages),
			"updated_at":    updated,
		})
	}
	return out
}

func (s *Server) createSession(request envelope) *sessionHandle {
	workingDir := str(request, "working_dir")
	if workingDir == "" {
		workingDir, _ = os.Getwd()
	}
	id := appcore.NewSessionID()
	return s.buildHandle(id, workingDir)
}

func (s *Server) attachSession(sessionID string) (*sessionHandle, error) {
	if handle := s.handleFor(sessionID); handle != nil {
		return handle, nil
	}
	store, err := appcore.LoadSessionStore(sessionID)
	if err != nil {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	handle := s.buildHandle(sessionID, store.Session.WorkingDir)
	for _, message := range store.Session.Messages {
		handle.Agent.AddMessage(message.Role, message.Content)
	}
	return handle, nil
}

func (s *Server) handleFor(sessionID string) *sessionHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[sessionID]
}

// BuildProviderHook lets hosts/tests override provider assembly (defaults
// to appcore.LoadConfig + appcore.BuildProvider).
var BuildProviderHook = func(sessionID string) (provider.Provider, error) {
	cfg, cfgErr := appcore.LoadConfig()
	if cfgErr != nil {
		return nil, cfgErr
	}
	return appcore.BuildProvider(cfg, sessionID)
}

// buildHandle assembles the agent + store for one session.
func (s *Server) buildHandle(id, workingDir string) *sessionHandle {
	providerInstance, err := BuildProviderHook(id)
	if err != nil {
		providerInstance = nil
	}
	registry := appcore.BuildRegistry()
	agentInstance := agent.NewAgent(providerInstance, registry)
	agentInstance.System = appcore.CLISystemPrompt
	agentInstance.WorkingDir = workingDir
	agentInstance.SessionID = id
	store := sessionstore.NewStore(sessionstore.Session{ID: id,
		Status: "active", WorkingDir: workingDir}, appcore.GcodeHome())
	handle := &sessionHandle{ID: id, WorkingDir: workingDir,
		Agent: agentInstance, Store: store}
	s.mu.Lock()
	s.sessions[id] = handle
	s.mu.Unlock()
	return handle
}

// sendMessage runs one agent turn, streaming events on the connection.
func (s *Server) sendMessage(id int64, request envelope, send func(envelope)) {
	handle, err := s.attachSession(str(request, "session_id"))
	if err != nil {
		// send_message on an unknown id creates a fresh session
		// (upstream create-then-send semantics for headless clients).
		handle = s.createSession(request)
	}
	message := str(request, "message")
	if strings.TrimSpace(message) == "" {
		send(envelope{"type": "error", "id": id, "code": "bad_request",
			"message": "message required"})
		return
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	agentInstance := handle.Agent
	agentInstance.Hooks.OnEvent = func(event types.StreamEvent) error {
		switch e := event.(type) {
		case types.TextDelta:
			send(envelope{"type": "text_delta", "session_id": handle.ID,
				"text": e.Text})
		case types.ToolUseStart:
			send(envelope{"type": "tool_start", "session_id": handle.ID,
				"call_id": e.ID, "name": e.Name})
		case types.ToolUseEndFor:
			send(envelope{"type": "tool_end", "session_id": handle.ID,
				"call_id": e.ID})
		}
		return nil
	}
	agentInstance.Hooks.OnToolEnd = func(call types.ToolCall, summary string, isErr bool) {
		send(envelope{"type": "tool_result", "session_id": handle.ID,
			"call_id": call.ID, "is_error": isErr, "summary": summary})
	}
	final, runErr := agentInstance.RunOnce(message)
	agentInstance.Hooks.OnEvent = nil
	if runErr != nil {
		send(envelope{"type": "error", "id": id, "code": "run_failed",
			"message": runErr.Error()})
		return
	}
	send(envelope{"type": "text_done", "session_id": handle.ID})
	send(envelope{"type": "message_complete", "id": id,
		"session_id": handle.ID, "text": final})
}

// sessionInfo renders one attached session.
func sessionInfo(handle *sessionHandle) envelope {
	status := "active"
	title := ""
	if handle.Store != nil {
		if handle.Store.Session.CustomTitle != nil {
			title = *handle.Store.Session.CustomTitle
		}
		if handle.Store.Session.Status != "" {
			status = handle.Store.Session.Status
		}
	}
	return envelope{"session_id": handle.ID, "status": status,
		"custom_title": title, "message_count": len(handle.Agent.Messages),
		"working_dir": handle.WorkingDir}
}

// historyMessages flattens the transcript for the API.
func historyMessages(handle *sessionHandle) []envelope {
	out := []envelope{}
	for _, message := range handle.Agent.Messages {
		text := ""
		for _, block := range message.Content {
			if block.Text != nil {
				text += *block.Text
			}
		}
		out = append(out, envelope{"role": string(message.Role), "text": text})
	}
	return out
}
