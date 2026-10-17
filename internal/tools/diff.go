package tools

import (
	"fmt"
	"strings"
)

// Line diff machinery (ported from the file_diff.rs uses of `similar`):
// an LCS line diff plus the unified renderer with context radius 3.

// diffOp is one diff step.
type diffOp struct {
	kind byte // ' ', '-', '+'
	text string
	// oldLine/newLine are 1-based positions (0 when not applicable).
	oldLine, newLine int
}

// splitLines ports similar's line splitting: trailing newline separates
// lines; a final line without a newline is still a line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	// SplitAfter keeps a trailing "" element when s ends with \n.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffLines computes the op list between two texts and renumbers both sides
// globally so every op carries its 1-based position.
func diffLines(old, new string) []diffOp {
	a := splitLines(old)
	b := splitLines(new)
	n, m := len(a), len(b)
	// Trim common prefix/suffix to keep the LCS table small.
	prefix := 0
	for prefix < n && prefix < m && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < n-prefix && suffix < m-prefix && a[n-1-suffix] == b[m-1-suffix] {
		suffix++
	}

	ops := make([]diffOp, 0, n+m)
	for i := 0; i < prefix; i++ {
		ops = append(ops, diffOp{kind: ' ', text: a[i]})
	}
	ops = append(ops, lcsOps(a[prefix:n-suffix], b[prefix:m-suffix])...)
	for i := n - suffix; i < n; i++ {
		ops = append(ops, diffOp{kind: ' ', text: a[i]})
	}
	line := 0
	for i := range ops {
		if ops[i].kind != '-' {
			line++
			ops[i].newLine = line
		}
	}
	line = 0
	for i := range ops {
		if ops[i].kind != '+' {
			line++
			ops[i].oldLine = line
		}
	}
	return ops
}

// lcsOps diffs the cores with dynamic programming (LCS on lines).
func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n == 0 {
		ops := make([]diffOp, 0, m)
		for _, line := range b {
			ops = append(ops, diffOp{kind: '+', text: line})
		}
		return ops
	}
	if m == 0 {
		ops := make([]diffOp, 0, n)
		for _, line := range a {
			ops = append(ops, diffOp{kind: '-', text: line})
		}
		return ops
	}
	// lcs[i][j] = LCS length of a[i:], b[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{kind: ' ', text: a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{kind: '-', text: a[i]})
			i++
		default:
			ops = append(ops, diffOp{kind: '+', text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{kind: '-', text: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{kind: '+', text: b[j]})
	}
	return ops
}

// window is a candidate hunk range (0-based op indices).
type window struct{ start, end int }

// buildHunks groups ops into hunks with `radius` context, merging hunks whose
// context windows touch (gap ≤ 2·radius+1), matching similar/GNU behavior.
func buildHunks(ops []diffOp, radius int) []window {
	var changed []int
	for i, op := range ops {
		if op.kind != ' ' {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	var windows []window
	for _, index := range changed {
		start := index - radius
		if start < 0 {
			start = 0
		}
		end := index + radius
		if end >= len(ops) {
			end = len(ops) - 1
		}
		if len(windows) > 0 && start <= windows[len(windows)-1].end+radius+1 {
			if end > windows[len(windows)-1].end {
				windows[len(windows)-1].end = end
			}
		} else {
			windows = append(windows, window{start: start, end: end})
		}
	}
	return windows
}

// hunkOut is one rendered hunk.
type hunkOut struct {
	oldStart, oldCount int // 1-based start; count
	newStart, newCount int
	lines              []string
}

// renderHunks converts windows into positioned hunks.
func renderHunks(ops []diffOp, windows []window) []hunkOut {
	out := make([]hunkOut, 0, len(windows))
	for _, w := range windows {
		h := hunkOut{}
		first := true
		for _, op := range ops[w.start : w.end+1] {
			h.lines = append(h.lines, string(op.kind)+op.text)
			if first {
				h.oldStart, h.newStart = op.oldLine, op.newLine
				first = false
			}
			switch op.kind {
			case ' ':
				h.oldCount++
				h.newCount++
			case '-':
				h.oldCount++
			case '+':
				h.newCount++
			}
		}
		out = append(out, h)
	}
	return out
}

// formatHunkHeader ports GNU/similar numbering: `@@ -s,c +s,c @@` with `,1`
// omitted for single-line ranges and empty ranges starting one line back.
func formatHunkHeader(h hunkOut) string {
	oldStart, newStart := h.oldStart, h.newStart
	if h.oldCount == 0 && oldStart > 0 {
		oldStart--
	}
	if h.newCount == 0 && newStart > 0 {
		newStart--
	}
	return fmt.Sprintf("@@ -%s +%s @@", rangePart(oldStart, h.oldCount), rangePart(newStart, h.newCount))
}

func rangePart(start, count int) string {
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

// UnifiedDiff renders the unified diff between old and new with the given
// headers (already prefixed "a/…"/"b/…"), context radius 3.
func UnifiedDiff(oldHeader, newHeader, old, new string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", oldHeader, newHeader)
	if old == new {
		return out.String()
	}
	ops := diffLines(old, new)
	windows := buildHunks(ops, 3)
	if len(windows) == 0 {
		return out.String()
	}
	for _, h := range renderHunks(ops, windows) {
		out.WriteString(formatHunkHeader(h))
		out.WriteString("\n")
		for _, line := range h.lines {
			out.WriteString(strings.TrimSuffix(line, "\n"))
			out.WriteString("\n")
			if !strings.HasSuffix(line, "\n") {
				out.WriteString("\\ No newline at end of file\n")
			}
		}
	}
	return out.String()
}

// quoteDiffHeader ports file_diff.rs's header(): "a/<path>" with control
// characters and quotes escaped into a quoted string.
func quoteDiffHeader(path, prefix string) string {
	full := prefix + "/" + path
	if path == "/dev/null" {
		full = path
	}
	needsQuoting := false
	for _, r := range full {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			needsQuoting = true
			break
		}
	}
	if !needsQuoting {
		return full
	}
	var quoted strings.Builder
	quoted.WriteByte('"')
	for i := 0; i < len(full); i++ {
		b := full[i]
		switch {
		case b == '\n':
			quoted.WriteString("\\n")
		case b == '\r':
			quoted.WriteString("\\r")
		case b == '\t':
			quoted.WriteString("\\t")
		case b == '\\':
			quoted.WriteString("\\\\")
		case b == '"':
			quoted.WriteString("\\\"")
		case b <= 31 || b >= 127:
			quoted.WriteString(fmt.Sprintf("\\%03o", b))
		default:
			quoted.WriteByte(b)
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}
