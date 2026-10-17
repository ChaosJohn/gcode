// Package agentgrep ports the upstream agentgrep crate (v0.1.7):
// structure-aware workspace search with grep/find/outline modes, an rg fast
// path with a native Go walker fallback, and the shared bounded renderers.
package agentgrep

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// StructureItem is one detected structural region of a file.
type StructureItem struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	LineCount int    `json:"line_count"`
}

// FileStructure is a file's language, role, and structural items.
type FileStructure struct {
	Language string          `json:"language"`
	Role     string          `json:"role"`
	Items    []StructureItem `json:"items"`
}

// DetectLanguage maps an extension to the structure language name.
func DetectLanguage(path string) string {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	switch ext {
	case "rs":
		return "rust"
	case "ts", "tsx":
		return "typescript"
	case "js", "jsx":
		return "javascript"
	case "py":
		return "python"
	case "md":
		return "markdown"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	default:
		return "text"
	}
}

// InferRole classifies a relative path (ported infer_role).
func InferRole(relativePath string) string {
	path := strings.ToLower(relativePath)
	switch {
	case contains(path, "/tests/") || contains(path, "_test") || contains(path, "test_"):
		return "test"
	case contains(path, "/docs/") || strings.HasSuffix(path, ".md"):
		return "docs"
	case contains(path, "/ui/") || contains(path, "/tui/") || contains(path, "view"):
		return "ui"
	case contains(path, "auth"):
		return "auth"
	case contains(path, "provider"):
		return "provider"
	case contains(path, "config"):
		return "config"
	case contains(path, "handler") || contains(path, "router"):
		return "handler"
	case contains(path, "src/"):
		return "implementation"
	default:
		return "generic"
	}
}

// ExtractFileStructure ports extract_file_structure: per-language line scans
// (no regex), then range finalization against the next item.
func ExtractFileStructure(path, relativePath, text string) FileStructure {
	language := DetectLanguage(path)
	var items []StructureItem
	switch language {
	case "rust":
		items = extractRust(text)
	case "typescript", "javascript":
		items = extractTsJs(text)
	case "python":
		items = extractPython(text)
	case "markdown":
		items = extractMarkdown(text)
	default:
		items = extractGeneric(text)
	}
	finalizeRanges(text, items)
	return FileStructure{Language: language, Role: InferRole(relativePath), Items: items}
}

// EnclosingItem finds the smallest item spanning the line.
func EnclosingItem(items []StructureItem, lineNumber int) *StructureItem {
	for i := range items {
		if items[i].StartLine <= lineNumber && lineNumber <= items[i].EndLine {
			return &items[i]
		}
	}
	return nil
}

func extractRust(text string) []StructureItem {
	var items []StructureItem
	for idx, line := range strings.Split(text, "\n") {
		lineNumber := idx + 1
		trimmed := trimLeftSpace(line)
		if label, ok := parseRustKeywordItem(trimmed, "fn"); ok {
			items = append(items, structureItem("function", label, lineNumber))
			continue
		}
		if label, ok := parseRustKeywordItem(trimmed, "struct"); ok {
			items = append(items, structureItem("struct", label, lineNumber))
			continue
		}
		if label, ok := parseRustKeywordItem(trimmed, "enum"); ok {
			items = append(items, structureItem("enum", label, lineNumber))
			continue
		}
		if label, ok := parseRustKeywordItem(trimmed, "trait"); ok {
			items = append(items, structureItem("trait", label, lineNumber))
			continue
		}
		if label, ok := parseRustImplItem(trimmed); ok {
			items = append(items, structureItem("impl", label, lineNumber))
		}
	}
	return items
}

func extractTsJs(text string) []StructureItem {
	var items []StructureItem
	for idx, line := range strings.Split(text, "\n") {
		lineNumber := idx + 1
		trimmed := trimLeftSpace(line)
		if rest, ok := stripKeywordPrefix(trimmed, "export"); ok {
			trimmed = trimLeftSpace(rest)
		}
		if label, ok := parseKeywordIdentifier(trimmed, "function"); ok {
			items = append(items, structureItem("function", label, lineNumber))
			continue
		}
		if label, ok := parseKeywordIdentifier(trimmed, "class"); ok {
			items = append(items, structureItem("class", label, lineNumber))
			continue
		}
		if label, ok := parseKeywordIdentifier(trimmed, "interface"); ok {
			items = append(items, structureItem("interface", label, lineNumber))
			continue
		}
		if label, ok := parseTsArrowItem(trimmed); ok {
			items = append(items, structureItem("function", label, lineNumber))
		}
	}
	return items
}

func extractPython(text string) []StructureItem {
	var items []StructureItem
	for idx, line := range strings.Split(text, "\n") {
		lineNumber := idx + 1
		trimmed := trimLeftSpace(line)
		if label, ok := parseKeywordIdentifier(trimmed, "def"); ok {
			items = append(items, structureItem("function", label, lineNumber))
			continue
		}
		if label, ok := parseKeywordIdentifier(trimmed, "class"); ok {
			items = append(items, structureItem("class", label, lineNumber))
		}
	}
	return items
}

func extractMarkdown(text string) []StructureItem {
	var items []StructureItem
	for idx, line := range strings.Split(text, "\n") {
		level := 0
		for level < len(line) && line[level] == '#' {
			level++
		}
		if level == 0 || level >= len(line) || line[level] != ' ' {
			continue
		}
		label := trimSpace(line[level+1:])
		if label == "" {
			continue
		}
		items = append(items, StructureItem{Kind: fmt.Sprintf("heading%d", level),
			Label: label, StartLine: idx + 1, EndLine: idx + 1, LineCount: 1})
	}
	return items
}

func extractGeneric(text string) []StructureItem {
	var items []StructureItem
	for idx, line := range strings.Split(text, "\n") {
		trimmed := trimSpace(line)
		if isGenericSection(trimmed) {
			items = append(items, structureItem("section", trimmed, idx+1))
		}
	}
	return items
}

func parseRustKeywordItem(line, keyword string) (string, bool) {
	rest, _ := stripRustVisibility(line)
	rest = trimLeftSpace(rest)
	if keyword == "fn" {
		if stripped, ok := stripKeywordPrefix(rest, "async"); ok {
			rest = trimLeftSpace(stripped)
		}
	}
	return parseKeywordIdentifier(rest, keyword)
}

func parseRustImplItem(line string) (string, bool) {
	rest, ok := stripPrefix(line, "impl")
	if !ok {
		return "", false
	}
	if rest != "" {
		runes := []rune(rest)
		if !isSpaceRune(runes[0]) && runes[0] != '<' {
			return "", false
		}
	}
	rest = trimLeftSpace(rest)
	if strings.HasPrefix(rest, "<") {
		skipped, ok := skipBalanced(rest, '<', '>')
		if !ok {
			return "", false
		}
		rest = trimLeftSpace(skipped)
	}
	return takeIdentifierLike(rest)
}

func parseTsArrowItem(line string) (string, bool) {
	for _, keyword := range []string{"const", "let", "var"} {
		rest, ok := stripKeywordPrefix(line, keyword)
		if !ok {
			continue
		}
		name, after, ok := takeIdentifier(trimLeftSpace(rest))
		if !ok {
			continue
		}
		after = trimLeftSpace(after)
		if !strings.HasPrefix(after, "=") || !contains(after, "=>") {
			continue
		}
		return name, true
	}
	return "", false
}

func parseKeywordIdentifier(line, keyword string) (string, bool) {
	rest, ok := stripKeywordPrefix(line, keyword)
	if !ok {
		return "", false
	}
	identifier, _, ok := takeIdentifier(trimLeftSpace(rest))
	return identifier, ok
}

func stripRustVisibility(line string) (string, bool) {
	rest, ok := stripPrefix(line, "pub")
	if !ok {
		return line, true
	}
	if rest != "" {
		next := []rune(rest)[0]
		if !isSpaceRune(next) && next != '(' {
			return line, true
		}
	}
	rest = trimLeftSpace(rest)
	if strings.HasPrefix(rest, "(") {
		if skipped, ok := skipBalanced(rest, '(', ')'); ok {
			return skipped, true
		}
	}
	return rest, true
}

func stripKeywordPrefix(line, keyword string) (string, bool) {
	rest, ok := stripPrefix(line, keyword)
	if !ok {
		return "", false
	}
	if rest == "" {
		return rest, true
	}
	if isSpaceRune([]rune(rest)[0]) {
		return rest, true
	}
	return "", false
}

func takeIdentifier(input string) (string, string, bool) {
	runes := []rune(input)
	end := len(runes)
	for idx, ch := range runes {
		if idx == 0 {
			if !isIdentifierStart(ch) {
				return "", "", false
			}
			continue
		}
		if !isIdentifierContinue(ch) {
			end = idx
			break
		}
	}
	if end == 0 {
		return "", "", false
	}
	return string(runes[:end]), string(runes[end:]), true
}

func takeIdentifierLike(input string) (string, bool) {
	runes := []rune(input)
	end := len(runes)
	for idx, ch := range runes {
		if isSpaceRune(ch) || ch == '{' {
			end = idx
			break
		}
	}
	token := strings.TrimSuffix(strings.TrimSuffix(string(runes[:end]), ":"), ",")
	if token == "" {
		return "", false
	}
	return token, true
}

func skipBalanced(input string, open, close rune) (string, bool) {
	depth := 0
	for idx, ch := range input {
		if ch == open {
			depth++
		} else if ch == close {
			depth--
			if depth < 0 {
				return "", false
			}
			if depth == 0 {
				_, size := utf8.DecodeRuneInString(input[idx:])
				return input[idx+size:], true
			}
		}
	}
	return "", false
}

func isIdentifierStart(ch rune) bool {
	return ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isIdentifierContinue(ch rune) bool {
	return isIdentifierStart(ch) || (ch >= '0' && ch <= '9')
}

func isSpaceRune(ch rune) bool { return unicode.IsSpace(ch) }

func isGenericSection(line string) bool {
	if len(line) < 4 {
		return false
	}
	runes := []rune(line)
	first := runes[0]
	if first < 'A' || first > 'Z' {
		return false
	}
	for _, ch := range runes {
		if (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			ch == '_' || ch == '-' || ch == ' ' {
			continue
		}
		return false
	}
	return true
}

func structureItem(kind, label string, lineNumber int) StructureItem {
	return StructureItem{Kind: kind, Label: label, StartLine: lineNumber,
		EndLine: lineNumber, LineCount: 1}
}

func finalizeRanges(text string, items []StructureItem) {
	totalLines := strings.Count(text, "\n")
	if trimmed := trimSpace(text); trimmed == "" {
		totalLines = 0
	} else if !strings.HasSuffix(text, "\n") {
		totalLines++
	}
	if totalLines < 1 {
		totalLines = 1
	}
	for idx := range items {
		end := totalLines
		if idx+1 < len(items) {
			end = items[idx+1].StartLine - 1
			if end < items[idx].StartLine {
				end = items[idx].StartLine
			}
		}
		items[idx].EndLine = end
		items[idx].LineCount = end - items[idx].StartLine + 1
	}
}

// stripPrefix is strings.TrimPrefix reporting whether it matched.
func stripPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// sortedUniqueLines splits text into lines.
func textLines(text string) []string {
	return strings.Split(text, "\n")
}

var _ = sort.Strings
var _ = os.ReadFile
