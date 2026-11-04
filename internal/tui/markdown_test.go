// Markdown rendering + wrapping tests: truncation fix (long replies wrap
// to multiple rows), the markdown subset, and escape-aware measuring.
package tui

import (
	"strings"
	"testing"
)

func TestWrapLineBreaksLongText(t *testing.T) {
	long := strings.Repeat("word ", 60) // 300 chars
	lines := wrapLine(long, 40)
	if len(lines) < 6 {
		t.Fatalf("expected >=6 rows, got %d", len(lines))
	}
	for i, line := range lines {
		if visibleLen(line) > 40 {
			t.Fatalf("row %d overflows: %d cols: %q", i, visibleLen(line), line)
		}
	}
	// No words lost.
	joined := strings.Join(lines, " ")
	if strings.Count(joined, "word") != 60 {
		t.Fatalf("words lost: %d", strings.Count(joined, "word"))
	}
}

func TestPushResponseWrapsInsteadOfTruncating(t *testing.T) {
	m, buf := testModel(t)
	m.mode = viewChat
	m.cols = 40 // push-time window (render re-queries its own size)
	long := strings.Repeat("sentence words here. ", 15)
	m.pushResponse(long)
	// Transcript rows respect the push-time width including the prefix.
	for i, line := range m.transcript {
		if visibleLen(line) > 40 {
			t.Fatalf("transcript row %d = %d cols: %q", i, visibleLen(line), line)
		}
	}
	// The full reply survives: rows break at spaces, so compare
	// space-collapsed content.
	collapse := func(s string) string {
		return strings.Join(strings.Fields(stripANSI(s)), " ")
	}
	joined := collapse(strings.Join(m.transcript, "\n"))
	joined = strings.ReplaceAll(joined, "gcode › ", "")
	if !strings.Contains(joined, collapse(long)) {
		t.Fatal("reply content lost — still truncated")
	}
	// Rendered rows (80-col default window in tests) stay inside 80.
	m.render()
	for _, line := range strings.Split(buf.String(), "\r\n") {
		if visibleLen(stripANSI(line)) > 80 {
			t.Fatalf("rendered row overflows 80: %q", line)
		}
	}
}

func stripANSI(s string) string {
	var out strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == 0x1b {
			if i+1 < len(runes) && runes[i+1] == '[' {
				i += 2
				for i < len(runes) && !(runes[i] >= 0x40 && runes[i] <= 0x7e) {
					i++
				}
				continue
			}
			continue
		}
		out.WriteRune(runes[i])
	}
	return out.String()
}

func TestRenderMarkdownSubset(t *testing.T) {
	text := "# Title\n\nNormal **bold** and *soft* and `code` text.\n- one\n- two\n3. third\n\n```go\nfmt.Println(1)\n```\n"
	lines := RenderMarkdownLines(text, 60)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "\x1b[1mTitle\x1b[0m") {
		t.Fatalf("heading not bold: %v", lines)
	}
	if !strings.Contains(joined, "\x1b[1mbold\x1b[22m") ||
		!strings.Contains(joined, "\x1b[3msoft\x1b[23m") ||
		!strings.Contains(joined, "\x1b[7mcode\x1b[27m") {
		t.Fatalf("inline styles missing: %v", lines)
	}
	if !strings.Contains(joined, "• one") || !strings.Contains(joined, "• two") ||
		!strings.Contains(joined, "3. third") {
		t.Fatalf("lists missing: %v", lines)
	}
	if !strings.Contains(joined, "│ fmt.Println(1)") ||
		!strings.Contains(joined, "\x1b[2m") {
		t.Fatalf("code block not dimmed: %v", lines)
	}
	if strings.Contains(joined, "```") {
		t.Fatalf("fence markers leaked: %v", lines)
	}
}

func TestVisibleLenSkipsANSI(t *testing.T) {
	if got := visibleLen("\x1b[1mbold\x1b[0m"); got != 4 {
		t.Fatalf("visibleLen: %d", got)
	}
	if got := visibleLen("\x1b[2m│ dim\x1b[0m"); got != 5 {
		t.Fatalf("visibleLen gutter: %d", got)
	}
}

func TestUTF8TruncateKeepsANSIIntact(t *testing.T) {
	styled := "\x1b[1m" + strings.Repeat("x", 50) + "\x1b[0m"
	cut := utf8Truncate(styled, 20)
	if !strings.HasPrefix(cut, "\x1b[1m") || !strings.HasSuffix(cut, "\x1b[0m…") {
		t.Fatalf("ANSI broken by truncate: %q", cut)
	}
	if visibleLen(cut) > 21 {
		t.Fatalf("cut too wide: %d", visibleLen(cut))
	}
}

func TestNumberedAndCodeWrap(t *testing.T) {
	// A long code line wraps inside the gutter.
	text := "```\n" + strings.Repeat("code ", 30) + "\n```"
	lines := RenderMarkdownLines(text, 30)
	if len(lines) < 3 {
		t.Fatalf("code wrap rows: %v", lines)
	}
	for _, line := range lines {
		if visibleLen(line) > 30 {
			t.Fatalf("code row overflow: %q", line)
		}
	}
	// Long list items wrap with hanging indent.
	item := "- " + strings.Repeat("item ", 20)
	lines = RenderMarkdownLines(item, 30)
	if !strings.HasPrefix(lines[0], "  • ") {
		t.Fatalf("bullet: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "    ") {
		t.Fatalf("hanging indent: %q", lines[1])
	}
}
