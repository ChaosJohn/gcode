// TUI logic tests without a real terminal: key parsing, navigation,
// mode transitions, and render output.
package tui

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/sessionstore"
	"github.com/ChaosJohn/gcode/internal/types"
)

func testModel(t *testing.T) (*Model, *bytes.Buffer) {
	t.Helper()
	t.Setenv("GCODE_HOME", t.TempDir())
	t.Setenv("GCODE_TUI_TEST", "1")
	buf := &bytes.Buffer{}
	m := &Model{
		Stdin:  bufio.NewReader(bytes.NewReader(nil)),
		Stdout: newWriter(buf),
	}
	return m, buf
}

func newWriter(buf *bytes.Buffer) *bufio.Writer { return bufio.NewWriter(buf) }

func TestReadKeySequences(t *testing.T) {
	cases := map[string]string{
		"\x1b[A": "up", "\x1b[B": "down", "\x1b[C": "right", "\x1b[D": "left",
		"\r": "enter", "\x7f": "backspace", "q": "q", "j": "j",
		"你": "你", "\x1b": "esc",
	}
	for input, want := range cases {
		m, _ := testModel(t)
		m.Stdin = bufio.NewReader(strings.NewReader(input))
		kind, key := m.readKey()
		if kind == keyQuit {
			if input != "\x03" && input != "\x04" {
				t.Fatalf("%q: unexpected quit", input)
			}
			continue
		}
		if key != want {
			t.Fatalf("%q: got %q want %q", input, key, want)
		}
	}
	// ctrl-c quits.
	m, _ := testModel(t)
	m.Stdin = bufio.NewReader(strings.NewReader("\x03"))
	if kind, _ := m.readKey(); kind != keyQuit {
		t.Fatal("ctrl-c should quit")
	}
}

func TestSessionListNavigation(t *testing.T) {
	m, buf := testModel(t)
	m.sessions = []sessionstore.Session{{ID: "s1"}, {ID: "s2"}, {ID: "s3"}}
	// Down twice, up once.
	for _, key := range []string{"j", "j", "k"} {
		m.handleKey(key)
	}
	if m.selected != 1 {
		t.Fatalf("selected: %d", m.selected)
	}
	// Boundaries.
	for i := 0; i < 10; i++ {
		m.handleKey("j")
	}
	if m.selected != 2 {
		t.Fatalf("bottom: %d", m.selected)
	}
	for i := 0; i < 10; i++ {
		m.handleKey("k")
	}
	if m.selected != 0 {
		t.Fatalf("top: %d", m.selected)
	}
	// Render contains the session ids and a reverse-video selection.
	m.render()
	out := buf.String()
	for _, id := range []string{"s1", "s2", "s3"} {
		if !strings.Contains(out, id) {
			t.Fatalf("render missing %s:\n%s", id, out)
		}
	}
	if !strings.Contains(out, "\x1b[7m") {
		t.Fatal("no selection highlight")
	}
	if !strings.Contains(out, "gcode") || !strings.Contains(out, "sessions") {
		t.Fatal("missing header")
	}
}

func TestOpenSessionEntersChat(t *testing.T) {
	m, _ := testModel(t)
	// Seed a persisted session.
	store := sessionstore.NewStore(sessionstore.Session{
		ID: "open-1", Status: "active"}, mHome(t))
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	m.refreshSessions()
	if len(m.sessions) != 1 {
		t.Fatalf("sessions: %d", len(m.sessions))
	}
	m.handleKey("enter")
	if m.mode != viewChat {
		t.Fatal("enter should open chat view")
	}
	if m.handle == nil || m.handle.Session.ID != "open-1" {
		t.Fatalf("handle: %+v", m.handle)
	}
	// Esc returns to the session list.
	m.handleKey("esc")
	if m.mode != viewSessionList {
		t.Fatal("esc should return to list")
	}
}

// mHome returns the GCODE_HOME the test model uses.
func mHome(t *testing.T) string {
	t.Helper()
	return os.Getenv("GCODE_HOME")
}

func TestChatInputEditing(t *testing.T) {
	m, _ := testModel(t)
	m.mode = viewChat
	m.chatAgent = nil // no agent; typing only
	for _, key := range []string{"h", "i", "你"} {
		m.handleKey(key)
	}
	if m.input != "hi你" {
		t.Fatalf("input: %q", m.input)
	}
	m.handleKey("backspace")
	if m.input != "hi" {
		t.Fatalf("after backspace: %q", m.input)
	}
	m.handleKey("backspace")
	m.handleKey("backspace")
	if m.input != "" {
		t.Fatalf("cleared: %q", m.input)
	}
	// Empty enter does nothing (no agent anyway).
	m.handleKey("enter")
	if m.generating {
		t.Fatal("empty enter should not generate")
	}
	// Scroll clamps at zero.
	m.handleKey("down")
	m.handleKey("down")
	if m.scrollBack != 0 {
		t.Fatalf("scroll: %d", m.scrollBack)
	}
}

func TestStreamingAndToolActivity(t *testing.T) {
	m, buf := testModel(t)
	home := mHome(t)
	store := sessionstore.NewStore(sessionstore.Session{
		ID: "stream-1", Status: "active"}, home)
	BuildChatAgent(m, store)
	m.mode = viewChat

	// Streaming deltas accumulate into the pending line.
	m.streaming.WriteString("partial answer")
	m.render()
	if out := buf.String(); !strings.Contains(out, "partial answer") {
		t.Fatalf("streaming render:\n%s", out)
	}
	// Tool start/end hooks append activity lines.
	call := types.ToolCall{ID: "c1", Name: "bash"}
	m.chatAgent.Hooks.OnToolStart(call)
	m.chatAgent.Hooks.OnToolEnd(call, "listed 3 files", false)
	m.chatAgent.Hooks.OnStatus("compacting…")
	if !strings.Contains(strings.Join(m.transcript, "\n"), "⚙ bash …") ||
		!strings.Contains(strings.Join(m.transcript, "\n"), "⚙ bash ✓ listed 3 files") {
		t.Fatalf("tool lines: %v", m.transcript)
	}
	if m.statusMsg != "compacting…" {
		t.Fatalf("status: %q", m.statusMsg)
	}
	// Spinner shows while generating.
	m.generating = true
	m.render()
	if out := buf.String(); !strings.Contains(out, spinnerFrames[0]) {
		t.Fatal("no spinner while generating")
	}
}

func TestScrollBackWindow(t *testing.T) {
	m, _ := testModel(t)
	m.mode = viewChat
	for i := 0; i < 50; i++ {
		m.transcript = append(m.transcript, fmt.Sprintf("line-%02d", i))
	}
	window := m.chatLines(10)
	if len(window) != 10 || window[0] != "line-40" || window[9] != "line-49" {
		t.Fatalf("tail window: %v", window)
	}
	// scrollBack shifts the window up.
	m.scrollBack = 5
	window = m.chatLines(10)
	if window[0] != "line-35" || window[9] != "line-44" {
		t.Fatalf("scrolled window: %v", window)
	}
	// Clamped at the top.
	m.scrollBack = 1000
	window = m.chatLines(10)
	if window[0] != "line-00" {
		t.Fatalf("clamped window: %v", window)
	}
	// pgup/pgdn move by half a screen.
	m.scrollBack = 0
	m.rows = 24
	m.handleKey("pgup")
	if m.scrollBack != 12 {
		t.Fatalf("pgup: %d", m.scrollBack)
	}
	m.handleKey("pgdn")
	if m.scrollBack != 0 {
		t.Fatalf("pgdn: %d", m.scrollBack)
	}
}

func TestNewChatKey(t *testing.T) {
	m, _ := testModel(t)
	m.handleKey("n")
	if m.mode != viewChat || m.chatAgent == nil || m.handle == nil {
		t.Fatal("n should open a fresh chat")
	}
	if m.handle.Session.ID == "" || m.handle.Session.WorkingDir == "" {
		t.Fatalf("session: %+v", m.handle.Session)
	}
}
