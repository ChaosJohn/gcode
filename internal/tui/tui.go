// Package tui is the minimal interactive terminal UI (upstream jcode-tui
// is 146k lines; this port covers the interaction core): raw mode via
// stdlib syscall (no cgo), alternate-screen ANSI rendering, a session
// picker, a chat view with streaming + tool activity, and the basic
// keymap (arrows/jk navigate, enter select/send, esc back, ctrl-c quit).
package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ChaosJohn/gcode/internal/agent"
	"github.com/ChaosJohn/gcode/internal/appcore"
	"github.com/ChaosJohn/gcode/internal/sessionstore"
	"github.com/ChaosJohn/gcode/internal/version"
)

// view is the active screen.
type view int

const (
	viewSessionList view = iota
	viewChat
)

// spinnerFrames animate the generating indicator (braille, like upstream).
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Model is the TUI state machine. State is owned by the main loop; agent
// callbacks and the input goroutine communicate through pushTranscript /
// wake / events so renders never race.
type Model struct {
	Stdin  *bufio.Reader
	Stdout *bufio.Writer

	cols, rows int

	mode      view
	sessions  []sessionstore.Session
	selected  int
	statusMsg string

	// Chat state.
	handle      *sessionstore.Store
	chatAgent   *agent.Agent
	modelName   string
	transcript  []string
	input       string
	generating  bool
	scrollBack  int
	streaming   strings.Builder
	spinnerIdx  int
	toolNames   map[string]string // call id -> tool name
	toolLastIdx map[string]int    // call id -> transcript line index

	// Event plumbing: the input goroutine feeds keys; agent callbacks wake
	// the loop for redraws. Render happens on the main loop only.
	events chan tuiEvent
	wakeCh chan struct{}
}

// tuiEvent is one deferred input event.
type tuiEvent struct {
	kind string // "key", "quit"
	key  string
}

// Run starts the TUI; it returns the process exit code. Diagnostics go to
// errOut so hosts can route them (the UI itself always uses os.Stdout).
func Run(errOut io.Writer) int {
	if os.Getenv("GCODE_TUI_TEST") == "" {
		if info, err := os.Stdout.Stat(); err == nil &&
			info.Mode()&os.ModeCharDevice == 0 {
			fmt.Fprintln(errOut, "gcode tui: stdout is not a terminal")
			return 2
		}
	}
	// Raw input needs a terminal fd. When stdin is redirected (launchers,
	// pipes), fall back to the controlling terminal /dev/tty.
	input := os.Stdin
	state, err := enableRaw(input.Fd())
	if err != nil {
		tty, ttyErr := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if ttyErr != nil {
			fmt.Fprintf(errOut, "gcode tui: no terminal for raw input (%v); "+
				"run gcode inside a terminal, or use `gcode run <prompt>` for headless mode\n", err)
			return 2
		}
		defer tty.Close()
		input = tty
		state, err = enableRaw(input.Fd())
		if err != nil {
			fmt.Fprintf(errOut, "gcode tui: raw mode unavailable: %v\n", err)
			return 2
		}
	}
	defer disableRaw(input.Fd(), state)
	m := &Model{
		Stdin:       bufio.NewReader(input),
		Stdout:      bufio.NewWriter(os.Stdout),
		toolNames:   map[string]string{},
		toolLastIdx: map[string]int{},
		events:      make(chan tuiEvent, 64),
		wakeCh:      make(chan struct{}, 1),
	}
	m.enterScreen()
	defer m.exitScreen()
	m.refreshSessions()
	m.render()
	go m.inputLoop()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case ev := <-m.events:
			if ev.kind == "quit" {
				return 0
			}
			m.handleKey(ev.key)
			m.render()
		case <-winch:
			m.render()
		case <-m.wakeCh:
			m.render()
		case <-ticker.C:
			if m.generating {
				m.spinnerIdx = (m.spinnerIdx + 1) % len(spinnerFrames)
				m.render()
			}
		}
	}
}

// wake requests a redraw from any goroutine (non-blocking).
func (m *Model) wake() {
	select {
	case m.wakeCh <- struct{}{}:
	default:
	}
}

// pushTranscript appends one line from any goroutine and wakes the loop.
func (m *Model) pushTranscript(line string) {
	m.transcript = append(m.transcript, line)
	m.wake()
}

// inputLoop parses keys until EOF or quit.
func (m *Model) inputLoop() {
	for {
		kind, key := m.readKey()
		if kind == keyQuit {
			m.events <- tuiEvent{kind: "quit"}
			return
		}
		m.events <- tuiEvent{kind: "key", key: key}
	}
}

// --- screen management ---

func (m *Model) enterScreen() {
	fmt.Fprint(m.Stdout, "\x1b[?1049h\x1b[?25l")
	m.Stdout.Flush()
}

func (m *Model) exitScreen() {
	fmt.Fprint(m.Stdout, "\x1b[?25h\x1b[?1049l")
	m.Stdout.Flush()
}

func (m *Model) refreshSessions() {
	sessions, _ := appcore.ListSessions(appcore.GcodeHome())
	m.sessions = sessions
}

// --- input ---

type keyKind int

const (
	keyOther keyKind = iota
	keyQuit
	keyResize
)

// readKey parses one key event (blocking, escape-sequence aware).
func (m *Model) readKey() (keyKind, string) {
	b, err := m.Stdin.ReadByte()
	if err != nil {
		return keyQuit, ""
	}
	switch b {
	case 3: // ctrl-c
		return keyQuit, ""
	case 4: // ctrl-d
		return keyQuit, ""
	case '\r', '\n':
		return keyOther, "enter"
	case 127, 8:
		return keyOther, "backspace"
	case 27: // ESC — bracketed sequences and bare escape
		if m.Stdin.Buffered() == 0 {
			return keyOther, "esc"
		}
		seq, _ := m.Stdin.Peek(m.Stdin.Buffered())
		switch {
		case len(seq) >= 2 && seq[0] == '[':
			m.Stdin.ReadByte()
			next, _ := m.Stdin.ReadByte()
			switch next {
			case 'A':
				return keyOther, "up"
			case 'B':
				return keyOther, "down"
			case 'C':
				return keyOther, "right"
			case 'D':
				return keyOther, "left"
			case '5':
				m.Stdin.ReadByte() // trailing ~
				return keyOther, "pgup"
			case '6':
				m.Stdin.ReadByte() // trailing ~
				return keyOther, "pgdn"
			default:
				return keyOther, ""
			}
		default:
			return keyOther, "esc"
		}
	}
	if b < 32 {
		return keyOther, ""
	}
	// Multi-byte UTF-8: collect the raw bytes and decode the sequence.
	raw := []byte{b}
	for i := 0; i < utf8ByteCount(b); i++ {
		cont, err := m.Stdin.ReadByte()
		if err != nil {
			break
		}
		raw = append(raw, cont)
	}
	return keyOther, string(raw)
}

// utf8ByteCount returns continuation bytes expected after a lead byte.
func utf8ByteCount(b byte) int {
	switch {
	case b >= 0xF0:
		return 3
	case b >= 0xE0:
		return 2
	case b >= 0xC0:
		return 1
	}
	return 0
}

// handleKey mutates state for a printable/semantic key (main loop only).
func (m *Model) handleKey(key string) {
	switch m.mode {
	case viewSessionList:
		switch key {
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected < len(m.sessions)-1 {
				m.selected++
			}
		case "r":
			m.refreshSessions()
			m.statusMsg = fmt.Sprintf("%d sessions", len(m.sessions))
		case "n":
			m.newChat()
		case "enter":
			m.openSelected()
		case "esc":
			m.statusMsg = ""
		}
	case viewChat:
		switch key {
		case "esc":
			if m.generating {
				m.statusMsg = "cannot interrupt mid-turn in this version"
				return
			}
			m.mode = viewSessionList
			m.chatAgent = nil
			m.handle = nil
			m.streaming.Reset()
			m.refreshSessions()
		case "up":
			m.scrollBack++
		case "down":
			if m.scrollBack > 0 {
				m.scrollBack--
			}
		case "pgup":
			m.scrollBack += m.rows / 2
		case "pgdn":
			m.scrollBack -= m.rows / 2
			if m.scrollBack < 0 {
				m.scrollBack = 0
			}
		case "backspace":
			if len(m.input) > 0 {
				runes := []rune(m.input)
				m.input = string(runes[:len(runes)-1])
			}
		case "enter":
			if m.generating || m.chatAgent == nil ||
				strings.TrimSpace(m.input) == "" {
				return
			}
			prompt := m.input
			m.input = ""
			m.generating = true
			m.streaming.Reset()
			m.scrollBack = 0
			m.statusMsg = ""
			m.pushTranscript("you › " + prompt)
			go m.runTurn(prompt)
		default:
			if key != "" {
				m.input += key
			}
		}
	}
}

// newChat attaches a fresh empty session.
func (m *Model) newChat() {
	store := sessionstore.NewStore(sessionstore.Session{
		ID: appcore.NewSessionID(), Status: "active"}, appcore.GcodeHome())
	if workingDir, err := os.Getwd(); err == nil {
		store.Session.WorkingDir = workingDir
	}
	BuildChatAgent(m, store)
	m.mode = viewChat
	m.scrollBack = 0
	m.statusMsg = "new session (persisted on first message)"
}

// openSelected attaches the highlighted session.
func (m *Model) openSelected() {
	if m.selected >= len(m.sessions) {
		return
	}
	session := m.sessions[m.selected]
	store, err := appcore.LoadSessionStore(session.ID)
	if err != nil {
		m.statusMsg = fmt.Sprintf("cannot open %s: %v", session.ID, err)
		return
	}
	BuildChatAgent(m, store)
	m.mode = viewChat
	m.scrollBack = 0
	m.statusMsg = ""
}

// runTurn executes one agent turn off the input loop.
func (m *Model) runTurn(prompt string) {
	defer func() {
		m.generating = false
		m.wake()
	}()
	final, err := ChatTurn(m, prompt)
	if err != nil {
		m.statusMsg = "error: " + err.Error()
	} else {
		m.statusMsg = ""
		if text := strings.TrimSpace(final); text != "" {
			m.pushTranscript("gcode › " + text)
		}
	}
}

// --- rendering ---

func (m *Model) render() {
	m.cols, m.rows = 80, 24
	if c, r, err := windowSize(os.Stdout.Fd()); err == nil && c >= 10 && r >= 4 {
		m.cols, m.rows = c, r
	}
	var out strings.Builder
	out.WriteString("\x1b[H\x1b[2J") // home + clear (plain, no diffing yet)
	model := ""
	if m.mode == viewChat && m.modelName != "" {
		model = " · " + m.modelName
	}
	header := fmt.Sprintf(" gcode %s — %s%s", version.Version,
		map[view]string{viewSessionList: "sessions", viewChat: "chat"}[m.mode],
		model)
	out.WriteString("\x1b[7m" + pad(header, m.cols) + "\x1b[0m\r\n")

	bodyRows := m.rows - 3
	switch m.mode {
	case viewSessionList:
		if len(m.sessions) == 0 {
			out.WriteString("  no sessions yet — n for a new chat\r\n")
		}
		for i, session := range m.sessions {
			if i >= bodyRows-1 {
				break
			}
			title := session.ID
			if session.CustomTitle != nil && *session.CustomTitle != "" {
				title = *session.CustomTitle
			} else if session.Title != nil && *session.Title != "" {
				title = *session.Title
			}
			updated := ""
			if session.UpdatedAt != nil {
				updated = session.UpdatedAt.Format("01-02 15:04")
			}
			line := fmt.Sprintf("  %-38s %3d msgs  %s", title,
				len(session.Messages), updated)
			if i == m.selected {
				out.WriteString("\x1b[7m" + pad(line, m.cols) + "\x1b[0m\r\n")
			} else {
				out.WriteString(pad(line, m.cols) + "\r\n")
			}
		}
	case viewChat:
		for _, line := range m.chatLines(bodyRows - 2) {
			out.WriteString(pad(line, m.cols) + "\r\n")
		}
		if pending := m.streaming.String(); pending != "" {
			out.WriteString(pad(truncate(pending, m.cols-1), m.cols) + "\r\n")
		}
	}
	// Status + input rows.
	status := m.statusMsg
	switch {
	case m.generating:
		status = spinnerFrames[m.spinnerIdx] + " " + status
	case status == "" && m.mode == viewSessionList:
		status = "↑/k ↓/j move · enter open · n new chat · r refresh · ctrl-c quit"
	case status == "" && m.mode == viewChat:
		status = "type a message · enter send · esc sessions · ↑↓ scroll"
	}
	out.WriteString("\x1b[2m" + pad(" "+status, m.cols) + "\x1b[0m\r\n")
	if m.mode == viewChat {
		prompt := "you › " + m.input
		if m.generating {
			prompt = "(generating — esc re-enables after this turn)"
		}
		out.WriteString("\x1b[1m" + pad(truncate(prompt, m.cols-1), m.cols) + "\x1b[0m")
	} else {
		out.WriteString(strings.Repeat(" ", m.cols))
	}
	if !m.generating && m.mode == viewChat {
		out.WriteString("\x1b[?25h")
	} else {
		out.WriteString("\x1b[?25l")
	}
	fmt.Fprint(m.Stdout, out.String())
	m.Stdout.Flush()
}

// chatLines renders the visible transcript window (scrollBack aware).
func (m *Model) chatLines(maxRows int) []string {
	if maxRows <= 0 {
		return nil
	}
	lines := m.transcript
	end := len(lines) - m.scrollBack
	if end < 0 {
		// Scrolled past the top: pin to the first page.
		end = maxRows
	}
	if end > len(lines) {
		end = len(lines)
	}
	start := end - maxRows
	if start < 0 {
		start = 0
	}
	return lines[start:end]
}

func pad(text string, width int) string {
	if len(text) >= width {
		return truncate(text, width)
	}
	return text + strings.Repeat(" ", width-len(text))
}

func truncate(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	return string(runes[:maxInt(0, width-1)]) + "…"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
