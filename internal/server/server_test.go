// Server tests over a real unix socket: ping/hello, list/create/attach,
// send_message streaming with a scripted provider, and history.
package server

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/ChaosJohn/gcode/internal/provider"
	"github.com/ChaosJohn/gcode/internal/sessionstore"
	"github.com/ChaosJohn/gcode/internal/types"
)

func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	t.Setenv("GCODE_HOME", t.TempDir())
	// macOS sun_path caps unix socket paths at 104 bytes; keep the runtime
	// dir short.
	shortRT, err := os.MkdirTemp("", "gcode-rt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(shortRT) })
	t.Setenv("GCODE_RUNTIME_DIR", shortRT)
	// Scripted provider for send_message runs.
	saved := BuildProviderHook
	BuildProviderHook = func(sessionID string) (provider.Provider, error) {
		return &serverScripted{responses: []scriptedReply{
			{text: "hello from gcode"},
		}}, nil
	}
	t.Cleanup(func() { BuildProviderHook = saved })
	srv := NewServer()
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Close)
	time.Sleep(50 * time.Millisecond)
	return srv, SocketPath()
}

type scriptedReply struct {
	text string
}

// serverScripted is a tiny provider for the daemon tests.
type serverScripted struct {
	responses []scriptedReply
	calls     int
}

func (p *serverScripted) Name() string              { return "scripted" }
func (p *serverScripted) Model() string             { return "scripted-1" }
func (p *serverScripted) SetModel(string) error     { return nil }
func (p *serverScripted) AvailableModels() []string { return nil }
func (p *serverScripted) SupportsImageInput() bool  { return false }
func (p *serverScripted) SupportsCompaction() bool  { return false }
func (p *serverScripted) Fork() provider.Provider   { return p }
func (p *serverScripted) InvalidateCredentials()    {}
func (p *serverScripted) Complete(turn provider.TurnRequest,
	emit func(types.StreamEvent) error) error {
	response := p.responses[min(p.calls, len(p.responses)-1)]
	p.calls++
	if response.text != "" {
		_ = emit(types.TextDelta{Text: response.text})
	}
	return emit(types.MessageEnd{})
}

// client is a line-framed test client.
type client struct {
	conn   net.Conn
	reader *bufio.Reader
}

func dial(t *testing.T, path string) *client {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &client{conn: conn, reader: bufio.NewReader(conn)}
}

func (c *client) send(request map[string]any) {
	encoded, _ := json.Marshal(request)
	c.conn.Write(append(encoded, '\n'))
}

// receive reads one envelope, skipping streaming events when a specific id
// is requested.
func (c *client) receive(wantID int64, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for {
		c.conn.SetReadDeadline(deadline)
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			continue
		}
		if wantID < 0 {
			return envelope, nil
		}
		if id, ok := envelope["id"].(float64); ok && int64(id) == wantID {
			return envelope, nil
		}
	}
}

func TestServerPingHelloList(t *testing.T) {
	_, path := testServer(t)
	c := dial(t, path)
	c.send(map[string]any{"type": "ping", "id": 0})
	reply, err := c.receive(0, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reply["type"] != "pong" {
		t.Fatalf("ping: %v", reply)
	}
	caps, _ := reply["capabilities"].([]any)
	if len(caps) == 0 {
		t.Fatalf("capabilities: %v", reply)
	}
	c.send(map[string]any{"type": "list_sessions", "id": 1})
	reply, err = c.receive(1, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reply["type"] != "sessions" {
		t.Fatalf("list: %v", reply)
	}
	// Unknown type is an error envelope, not a crash.
	c.send(map[string]any{"type": "teleport", "id": 2})
	reply, err = c.receive(2, 2*time.Second)
	if err != nil || reply["type"] != "error" {
		t.Fatalf("unknown: %v %v", reply, err)
	}
}

func TestServerSendMessageStream(t *testing.T) {
	_, path := testServer(t)
	c := dial(t, path)
	c.send(map[string]any{"type": "create_session", "id": 10,
		"working_dir": t.TempDir()})
	attached, err := c.receive(10, 2*time.Second)
	if err != nil || attached["type"] != "attached" {
		t.Fatalf("create: %v %v", attached, err)
	}
	session, _ := attached["session"].(map[string]any)
	sessionID, _ := session["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("session id: %v", session)
	}
	c.send(map[string]any{"type": "send_message", "id": 11,
		"session_id": sessionID, "message": "say hi"})
	var final map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		envelope, err := c.receive(-1, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if envelope["type"] == "text_delta" {
			if envelope["session_id"] != sessionID || envelope["text"] != "hello from gcode" {
				t.Fatalf("delta: %v", envelope)
			}
			continue
		}
		if envelope["type"] == "message_complete" {
			final = envelope
			break
		}
	}
	if final == nil {
		t.Fatal("no message_complete")
	}
	if final["text"] != "hello from gcode" || final["session_id"] != sessionID {
		t.Fatalf("complete: %v", final)
	}
	// get_history reflects the transcript.
	c.send(map[string]any{"type": "get_history", "id": 12, "session_id": sessionID})
	history, err := c.receive(12, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	messages, _ := history["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("history: %v", history)
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "user" || first["text"] != "say hi" {
		t.Fatalf("first: %v", first)
	}
}

func TestServerAttachExistingSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GCODE_HOME", home)
	shortRT, err := os.MkdirTemp("", "gcode-rt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(shortRT) })
	t.Setenv("GCODE_RUNTIME_DIR", shortRT)
	// Persist one session with a user turn through the real store.
	store := sessionstore.NewStore(sessionstore.Session{
		ID: "existing-1", Status: "active"}, home)
	userText := "earlier work"
	store.Session.Messages = append(store.Session.Messages,
		types.StoredMessage{Role: types.RoleUser,
			Content: []types.ContentBlock{{Type: "text", Text: &userText}}})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	saved := BuildProviderHook
	BuildProviderHook = func(sessionID string) (provider.Provider, error) {
		return &serverScripted{responses: []scriptedReply{{text: "ok"}}}, nil
	}
	t.Cleanup(func() { BuildProviderHook = saved })
	srv := NewServer()
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(srv.Close)
	time.Sleep(50 * time.Millisecond)
	path := SocketPath()

	c := dial(t, path)
	// Attach an unknown id errors cleanly.
	c.send(map[string]any{"type": "attach_session", "id": 20,
		"session_id": "nope"})
	reply, err := c.receive(20, 2*time.Second)
	if err != nil || reply["type"] != "error" {
		t.Fatalf("attach missing: %v %v", reply, err)
	}
	// Attach the persisted session loads its transcript.
	c.send(map[string]any{"type": "attach_session", "id": 21,
		"session_id": "existing-1"})
	attached, err := c.receive(21, 2*time.Second)
	if err != nil || attached["type"] != "attached" {
		t.Fatalf("attach: %v %v", attached, err)
	}
	session, _ := attached["session"].(map[string]any)
	if session["session_id"] != "existing-1" {
		t.Fatalf("session: %v", session)
	}
	c.send(map[string]any{"type": "get_history", "id": 22,
		"session_id": "existing-1"})
	history, err := c.receive(22, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	messages, _ := history["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("history: %v", history)
	}
	first, _ := messages[0].(map[string]any)
	if first["text"] != "earlier work" {
		t.Fatalf("text: %v", first)
	}
}
