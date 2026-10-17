package cmdrisk

import (
	"strings"
)

// tokenize.rs port: a minimal shell tokenizer, tuned for risk analysis rather
// than execution. Deliberately fails loud rather than quiet: when it cannot
// understand something the caller escalates.

// Token is one shell word plus the classification bits the assessor needs.
type Token struct {
	Text string
	// ReceivesPipe: this segment's stdin comes from a pipe, so its operands
	// are partly supplied at runtime by the previous command.
	ReceivesPipe bool
	// IsTruncatingRedirectTarget: `>` / `>|` destinations, truncated on open.
	IsTruncatingRedirectTarget bool
	// IsOperator: control operators like `&&`, never path targets.
	IsOperator bool
}

// Basename is the command name without its directory, so /bin/rm matches rm.
func (t Token) Basename() string {
	if idx := strings.LastIndex(t.Text, "/"); idx >= 0 {
		return t.Text[idx+1:]
	}
	return t.Text
}

// IsFlag reports `-x` style flags (a lone "-" is not a flag).
func (t Token) IsFlag() bool {
	return strings.HasPrefix(t.Text, "-") && len(t.Text) > 1
}

// IsRecursiveFlag reports flags requesting recursion, including bundles
// like `-rf`.
func (t Token) IsRecursiveFlag() bool {
	if !t.IsFlag() {
		return false
	}
	if strings.HasPrefix(t.Text, "--") {
		return t.Text == "--recursive"
	}
	return strings.Contains(t.Text, "r") || strings.Contains(t.Text, "R")
}

// SplitSegments splits a command line into individual command segments, each
// tokenized. `rm -rf a && rm -rf b` yields two segments so both are assessed.
func SplitSegments(command string) [][]Token {
	tokens := Tokenize(command)
	var segments [][]Token
	var current []Token
	nextReceivesPipe := false
	for _, token := range tokens {
		if token.IsOperator && isSegmentSeparator(token.Text) {
			if len(current) > 0 {
				segments = append(segments, current)
				current = nil
			}
			// The command after `|` consumes the previous one's output as
			// operands, which this parser cannot see (#604 review).
			nextReceivesPipe = token.Text == "|"
			continue
		}
		token.ReceivesPipe = nextReceivesPipe
		current = append(current, token)
	}
	if len(current) > 0 {
		segments = append(segments, current)
	}
	return segments
}

func isSegmentSeparator(text string) bool {
	switch text {
	case "&&", "||", ";", "|", "\n", "(", ")":
		return true
	default:
		return false
	}
}

// Tokenize resolves quotes so `rm "$HOME"` and `rm $HOME` produce the same
// target text. `$VAR` stays intact; the path layer treats an unexpanded
// variable as unknown-and-therefore-risky.
func Tokenize(command string) []Token {
	command = withoutHeredocBodies(command)
	var tokens []Token
	var current strings.Builder
	hasContent := false
	pendingRedirect := false
	runes := []rune(command)
	pos := 0

	flush := func() {
		if hasContent {
			token := Token{Text: current.String()}
			if pendingRedirect {
				token.IsTruncatingRedirectTarget = true
				pendingRedirect = false
			}
			tokens = append(tokens, token)
			current.Reset()
			hasContent = false
		}
	}
	pushOperator := func(text string) {
		flush()
		tokens = append(tokens, Token{Text: text, IsOperator: true})
	}

	for pos < len(runes) {
		c := runes[pos]
		pos++
		switch c {
		case '\'':
			hasContent = true
			for pos < len(runes) {
				q := runes[pos]
				pos++
				if q == '\'' {
					break
				}
				current.WriteRune(q)
			}
		case '"':
			hasContent = true
			for pos < len(runes) {
				q := runes[pos]
				pos++
				if q == '"' {
					break
				}
				if q == '\\' && pos < len(runes) {
					next := runes[pos]
					if next == '"' || next == '\\' || next == '$' || next == '`' {
						current.WriteRune(next)
						pos++
						continue
					}
				}
				current.WriteRune(q)
			}
		case '\\':
			if pos < len(runes) {
				hasContent = true
				current.WriteRune(runes[pos])
				pos++
			}
		case ' ', '\t':
			flush()
		case '\n', ';':
			pushOperator(string(c))
		case '&', '|':
			text := string(c)
			if pos < len(runes) && runes[pos] == c {
				text += string(c)
				pos++
			}
			pushOperator(text)
		case '(', ')':
			pushOperator(string(c))
		case '>':
			flush()
			// `>>` appends and does not truncate; only a single `>` clobbers.
			if pos < len(runes) && runes[pos] == '>' {
				pos++
			} else {
				if pos < len(runes) && runes[pos] == '|' {
					pos++
				}
				pendingRedirect = true
			}
		case '<':
			flush()
		default:
			hasContent = true
			current.WriteRune(c)
		}
	}
	flush()
	return tokens
}

// withoutHeredocBodies removes heredoc payloads before tokenizing: a heredoc
// body is input data, not shell source. Prose containing `time`, or a script
// containing `rm`, must not trip the gate (#922). Newlines are retained so a
// command after the terminator remains a separate segment.
func withoutHeredocBodies(command string) string {
	lines := splitInclusive(command)
	var output strings.Builder
	index := 0
	for index < len(lines) {
		line := lines[index]
		output.WriteString(line)
		delimiters := heredocDelimiters(strings.TrimSuffix(line, "\n"))
		index++
		for _, d := range delimiters {
			for index < len(lines) {
				candidate := strings.TrimRight(lines[index], "\r\n")
				if d.stripTabs {
					candidate = strings.TrimLeft(candidate, "\t")
				}
				index++
				if candidate == d.delimiter {
					output.WriteString("\n")
					break
				}
			}
		}
	}
	return output.String()
}

type heredocDelim struct {
	delimiter string
	stripTabs bool
	quoted    bool
}

// heredocDelimiters parses heredoc declarations with per-context quote
// state: `"$(cat <<'EOF'` opens a new shell context inside the double
// quotes, so a heredoc declared there is real and its body must still be
// stripped. `<<` inside single quotes is inert.
func heredocDelimiters(line string) []heredocDelim {
	var found []heredocDelim
	runes := []rune(line)
	// Quote state per command-substitution level.
	quotes := []rune{0} // 0 = none, else the open quote char
	index := 0
	for index+1 < len(runes) {
		ch := runes[index]
		quote := quotes[len(quotes)-1]
		if quote != '\'' && ch == '$' && runes[index+1] == '(' {
			quotes = append(quotes, 0)
			index += 2
			continue
		}
		if quote == 0 && ch == ')' && len(quotes) > 1 {
			quotes = quotes[:len(quotes)-1]
			index++
			continue
		}
		if quote != 0 {
			if ch == '\\' && quote == '"' {
				index += 2
				continue
			}
			if ch == quote {
				quotes[len(quotes)-1] = 0
			}
			index++
			continue
		}
		if ch == '\'' || ch == '"' {
			quotes[len(quotes)-1] = ch
			index++
			continue
		}
		if ch == '\\' {
			index += 2
			continue
		}
		if ch != '<' || runes[index+1] != '<' {
			index++
			continue
		}
		index += 2
		// `<<<` is a here-string, not a multiline heredoc.
		if index < len(runes) && runes[index] == '<' {
			index++
			continue
		}
		stripTabs := false
		if index < len(runes) && runes[index] == '-' {
			stripTabs = true
			index++
		}
		for index < len(runes) && isSpaceRune(runes[index]) {
			index++
		}
		var delimiter strings.Builder
		delimiterQuote := rune(0)
		quoted := false
		for index < len(runes) {
			ch := runes[index]
			if ch == '\'' || ch == '"' || ch == '\\' {
				quoted = true
			}
			if delimiterQuote != 0 {
				if ch == delimiterQuote {
					delimiterQuote = 0
				} else if ch == '\\' && delimiterQuote == '"' && index+1 < len(runes) {
					index++
					delimiter.WriteRune(runes[index])
				} else {
					delimiter.WriteRune(ch)
				}
			} else if ch == '\'' || ch == '"' {
				delimiterQuote = ch
			} else if ch == '\\' && index+1 < len(runes) {
				index++
				delimiter.WriteRune(runes[index])
			} else if isSpaceRune(ch) || ch == ';' || ch == '&' || ch == '|' {
				break
			} else {
				delimiter.WriteRune(ch)
			}
			index++
		}
		if delimiter.Len() > 0 {
			found = append(found, heredocDelim{delimiter: delimiter.String(),
				stripTabs: stripTabs, quoted: quoted})
		}
	}
	return found
}

func isSpaceRune(ch rune) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

// EmbeddedSubstitutions returns command sources the shell runs through
// `$(...)` or backticks but that Tokenize flattens into a single word (#1753).
// Single-quoted text and quoted heredoc bodies are inert and skipped.
func EmbeddedSubstitutions(command string) []string {
	lines := splitInclusive(command)
	var shellSource strings.Builder
	var found []string
	index := 0
	for index < len(lines) {
		line := lines[index]
		shellSource.WriteString(line)
		index++
		for _, d := range heredocDelimiters(strings.TrimSuffix(line, "\n")) {
			var body strings.Builder
			for index < len(lines) {
				raw := lines[index]
				candidate := strings.TrimRight(raw, "\r\n")
				if d.stripTabs {
					candidate = strings.TrimLeft(candidate, "\t")
				}
				index++
				if candidate == d.delimiter {
					shellSource.WriteString("\n")
					break
				}
				body.WriteString(raw)
			}
			// An unquoted heredoc body still expands $(...) and backticks.
			if !d.quoted {
				scanSubstitutions(body.String(), false, &found)
			}
		}
	}
	scanSubstitutions(shellSource.String(), true, &found)
	return found
}

func scanSubstitutions(text string, shellQuotes bool, found *[]string) {
	var current strings.Builder
	inSingle := false
	inDouble := false
	runes := []rune(text)
	for pos := 0; pos < len(runes); pos++ {
		ch := runes[pos]
		switch {
		case shellQuotes && ch == '\'' && !inDouble:
			inSingle = !inSingle
			continue
		case shellQuotes && ch == '"' && !inSingle:
			inDouble = !inDouble
			continue
		case shellQuotes && ch == '\\' && pos+1 < len(runes) && (inDouble || !inSingle):
			current.WriteRune(ch)
			pos++
			current.WriteRune(runes[pos])
			continue
		case !inSingle && ch == '$' && pos+1 < len(runes) && runes[pos+1] == '(':
			// Find the matching close paren.
			depth := 0
			start := pos + 1
			end := -1
			for scan := pos; scan < len(runes); scan++ {
				if runes[scan] == '(' {
					depth++
				} else if runes[scan] == ')' {
					depth--
					if depth == 0 {
						end = scan
						break
					}
				}
			}
			if end >= 0 {
				inner := string(runes[start+1 : end])
				if inner != "" {
					*found = append(*found, inner)
				}
				current.WriteString("$(")
				pos = end
				continue
			}
			current.WriteRune(ch)
		case !inSingle && ch == '`':
			if closeIdx := indexOfRune(runes[pos+1:], '`'); closeIdx >= 0 {
				inner := string(runes[pos+1 : pos+1+closeIdx])
				if strings.TrimSpace(inner) != "" {
					*found = append(*found, inner)
				}
				current.WriteString("`")
				pos += closeIdx + 1
				continue
			}
			current.WriteRune(ch)
		default:
			current.WriteRune(ch)
		}
	}
}

func indexOfRune(runes []rune, target rune) int {
	for idx, ch := range runes {
		if ch == target {
			return idx
		}
	}
	return -1
}

func splitInclusive(s string) []string {
	var out []string
	start := 0
	for idx, ch := range s {
		if ch == '\n' {
			out = append(out, s[start:idx+1])
			start = idx + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
