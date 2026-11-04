// Escape-aware wrapping and a light markdown renderer for the TUI
// transcript (upstream renders full markdown via ratatui widgets; this
// port covers the visible subset: fenced code, headings, bullets, bold,
// italic, inline code).
package tui

import (
	"strings"
	"unicode/utf8"
)

// visibleLen measures the printed width of a string, skipping ANSI CSI
// escape sequences.
func visibleLen(text string) int {
	width := 0
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] == 0x1b {
			// CSI: ESC [ params final-byte(@-~)
			if i+1 < len(runes) && runes[i+1] == '[' {
				i += 2
				for i < len(runes) && !(runes[i] >= 0x40 && runes[i] <= 0x7e) {
					i++
				}
				continue
			}
			continue
		}
		width++
	}
	return width
}

// wrapLine breaks one logical line into physical lines of at most width
// visible columns, breaking on spaces and hard-splitting overlong words.
func wrapLine(line string, width int) []string {
	if width < 4 {
		width = 4
	}
	if visibleLen(line) <= width {
		return []string{line}
	}
	var out []string
	current := ""
	for _, word := range strings.Split(line, " ") {
		for visibleLen(word) > width {
			// Hard-split the overlong word.
			if current != "" {
				out = append(out, current)
				current = ""
			}
			take := width
			runes := []rune(word)
			if len(runes) > take {
				word, runes = string(runes[take:]), runes[:take]
			} else {
				word = ""
			}
			out = append(out, string(runes))
			if word == "" {
				break
			}
		}
		if word == "" {
			continue
		}
		if current == "" {
			current = word
		} else if visibleLen(current)+1+visibleLen(word) <= width {
			current += " " + word
		} else {
			out = append(out, current)
			current = word
		}
	}
	if current != "" || len(out) == 0 {
		out = append(out, current)
	}
	return out
}

// inlineMarkdown applies the inline subset: `code`, **bold**, *italic*.
// Styles never span wrap points because this runs per physical line.
func inlineMarkdown(line string) string {
	line = replaceSpan(line, "**", "\x1b[1m", "\x1b[22m")
	line = replaceSpan(line, "*", "\x1b[3m", "\x1b[23m")
	line = replaceSpan(line, "`", "\x1b[7m", "\x1b[27m")
	return line
}

// replaceSpan alternates between the ANSI on/off codes for each pair of
// delimiters (odd occurrences stay literal).
func replaceSpan(line, delimiter, on, off string) string {
	parts := strings.Split(line, delimiter)
	if len(parts) < 3 {
		return line
	}
	var out strings.Builder
	for i, part := range parts {
		if i > 0 {
			if i%2 == 1 {
				out.WriteString(on)
			} else {
				out.WriteString(off)
			}
		}
		out.WriteString(part)
	}
	return out.String()
}

// RenderMarkdownLines converts a markdown response into styled physical
// transcript lines: fenced code blocks render dim with a gutter, headings
// render bold, bullets use •, and the inline subset styles per line.
func RenderMarkdownLines(text string, width int) []string {
	if width < 10 {
		width = 10
	}
	var out []string
	inCode := false
	for _, logical := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		trimmed := strings.TrimSpace(logical)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			for _, phys := range wrapLine(strings.TrimRight(logical, " "), width-5) {
				out = append(out, "  \x1b[2m│ "+phys+"\x1b[0m")
			}
			continue
		}
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		// Headings: strip markers, render bold.
		if strings.HasPrefix(trimmed, "#") {
			content := strings.TrimLeft(trimmed, "# ")
			for _, phys := range wrapLine(content, width-1) {
				out = append(out, "\x1b[1m"+phys+"\x1b[0m")
			}
			continue
		}
		// Bullets.
		bullet := ""
		content := logical
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			bullet = "  • "
			content = trimmed[2:]
		}
		// Numbered lists keep their marker.
		if marker, rest, ok := splitNumbered(trimmed); ok {
			bullet = "  " + marker + " "
			content = rest
		}
		avail := width - visibleLen(bullet) - 2
		for i, phys := range wrapLine(content, avail) {
			styled := inlineMarkdown(phys)
			if i == 0 {
				out = append(out, bullet+styled)
			} else {
				out = append(out, strings.Repeat(" ", visibleLen(bullet))+styled)
			}
		}
	}
	return out
}

// splitNumbered recognizes "3. text" list markers.
func splitNumbered(line string) (marker, rest string, ok bool) {
	dot := strings.Index(line, ". ")
	if dot <= 0 || dot > 4 {
		return "", "", false
	}
	numerals := line[:dot]
	for _, r := range numerals {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return numerals + ".", line[dot+2:], true
}

// utf8Truncate cuts a string to at most width visible runes without
// splitting ANSI sequences (they pass through untouched).
func utf8Truncate(text string, width int) string {
	if visibleLen(text) <= width {
		return text
	}
	var out strings.Builder
	visible := 0
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == 0x1b {
			out.WriteRune(r)
			if i+1 < len(runes) && runes[i+1] == '[' {
				out.WriteRune(runes[i+1])
				i += 2
				for i < len(runes) && !(runes[i] >= 0x40 && runes[i] <= 0x7e) {
					out.WriteRune(runes[i])
					i++
				}
				if i < len(runes) {
					out.WriteRune(runes[i])
				}
			}
			continue
		}
		if visible >= width-1 {
			continue // skip the rest of the visible text
		}
		out.WriteRune(r)
		visible++
	}
	_ = utf8.RuneLen('…')
	out.WriteString("…")
	return out.String()
}
