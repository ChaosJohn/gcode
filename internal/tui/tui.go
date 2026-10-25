// Package tui is the minimal interactive terminal UI (upstream jcode-tui
// is 146k lines; this port covers the interaction core): raw mode via
// stdlib syscall (no cgo), alternate-screen ANSI rendering, a session
// picker, a transcript/chat view with streaming, and the basic keymap
// (arrows/jk navigate, enter select/send, esc back, ctrl-c quit).
package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

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

// Model is the TUI state machine.
type Model struct {
	Stdin  *bufio.Reader
	Stdout *bufio.Writer

	cols, rows int
	refresh    bool

	mode      view
	sessions  []sessionstore.Session
	selected  int
	statusMsg string

	// Chat state.
	handle     *sessionstore.Store
	chatAgent  *agent.Agent
	transcript []string
	input      string
	generating bool
	scrollBack int
}

// Run starts the TUI; it returns the process exit code.
func Run() int {
	if os.Getenv("GCODE_TUI_TEST") == "" {
		if info, err := os.Stdout.Stat(); err == nil &&
			info.Mode()&os.ModeCharDevice == 0 {
			fmt.Fprintln(os.Stderr, "gcode tui: stdout is not a terminal")
			return 2
		}
	}
	model := &Model{
		Stdin:  bufio.NewReader(os.Stdin),
		Stdout: bufio.NewWriter(os.Stdout),
	}
	state, err := enableRaw(os.Stdin.Fd())
	if err != nil {
		fmt.Fprintf(os.Stderr, "gcode tui: raw mode unavailable: %v\n", err)
		return 2
	}
	defer disableRaw(os.Stdin.Fd(), state)
	model.enterScreen()
	defer model.exitScreen()
	model.refreshSessions()
	model.render()
	for {
		kind, payload := model.readKey()
		switch kind {
		case keyQuit:
			return 0
		case keyResize:
			model.render()
		case keyOther:
			model.handleKey(payload)
			model.render()
		}
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
	extra := utf8ByteCount(b)
	for i := 0; i < extra; i++ {
		cont, err := m.Stdin.ReadByte()
		if err != nil {
			break
		}
		raw = append(raw, cont)
	}
	return keyOther, string(raw)
}

// handleKey mutates state for a printable/semantic key.
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
			m.refreshSessions()
		case "up":
			m.scrollBack++
		case "down":
			if m.scrollBack > 0 {
				m.scrollBack--
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
			m.statusMsg = "generating…"
			go m.runTurn(prompt)
		default:
			if key != "" {
				m.input += key
			}
		}
	}
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
	defer func() { m.generating = false }()
	m.transcript = append(m.transcript, "you › "+prompt)
	m.render()
	final, err := ChatTurn(m, prompt)
	if err != nil {
		m.statusMsg = "error: " + err.Error()
	} else {
		m.statusMsg = ""
		if strings.TrimSpace(final) != "" {
			m.transcript = append(m.transcript, "gcode › "+final)
		}
	}
	m.render()
}

// --- rendering ---

func (m *Model) render() {
	m.cols, m.rows = 80, 24
	if c, r, err := windowSize(os.Stdout.Fd()); err == nil {
		m.cols, m.rows = c, r
	}
	var out strings.Builder
	out.WriteString("\x1b[H\x1b[2J") // home + clear (plain, no diffing yet)
	header := fmt.Sprintf(" gcode %s — %s", version.Version,
		map[view]string{viewSessionList: "sessions", viewChat: "chat"}[m.mode])
	out.WriteString("\x1b[7m" + pad(header, m.cols) + "\x1b[0m\r\n")

	bodyRows := m.rows - 3
	switch m.mode {
	case viewSessionList:
		if len(m.sessions) == 0 {
			out.WriteString("  no sessions yet — run `gcode run <prompt>` first\r\n")
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
		lines := m.chatLines(bodyRows - 2)
		for _, line := range lines {
			out.WriteString(pad(line, m.cols) + "\r\n")
		}
	}
	// Status + input rows.
	status := m.statusMsg
	if status == "" && m.mode == viewSessionList {
		status = "↑/k ↓/j move · enter open · r refresh · ctrl-c quit"
	}
	if status == "" && m.mode == viewChat {
		status = "type a message · enter send · esc sessions · ↑↓ scroll"
	}
	out.WriteString("\x1b[2m" + pad(" "+status, m.cols) + "\x1b[0m\r\n")
	if m.mode == viewChat {
		prompt := "you › " + m.input
		if m.generating {
			prompt = "(generating — esc disabled mid-turn)"
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

// chatLines renders the visible transcript tail.
func (m *Model) chatLines(max int) []string {
	if max <= 0 {
		return nil
	}
	lines := m.transcript
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines
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
	return string(runes[:max(0, width-1)]) + "…"
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
