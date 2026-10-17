package agentgrep

import (
	"fmt"
	"strings"
)

// Shared bounded renderers (ported render.rs): both CLI and embedding hosts
// render through these so output stays compact.

const (
	maxRenderedMatchLineChars       = 240
	renderedMatchPrefixContextChars = 80
	maxNonCodeMatchLinesPerFile     = 3
)

// RenderGrepOutput renders grep results with per-group match lines.
func RenderGrepOutput(result GrepResult, args GrepArgs, maxMatches int) string {
	if args.PathsOnly {
		paths := make([]string, 0, len(result.Files))
		for _, file := range result.Files {
			paths = append(paths, file.Path)
		}
		return strings.Join(paths, "\n")
	}

	lines := []string{
		fmt.Sprintf("query: %s", result.Query),
		fmt.Sprintf("matches: %d in %d files", result.TotalMatches, result.TotalFiles),
	}
	displayed := 0
	for i := range result.Files {
		if maxMatches > 0 && displayed >= maxMatches {
			break
		}
		file := &result.Files[i]
		lines = append(lines, "")
		lines = append(lines, file.Path)
		if file.TotalSymbols > 0 {
			lines = append(lines, fmt.Sprintf("  symbols: %d total, %d matched, %d other",
				file.TotalSymbols, file.MatchedSymbolCount,
				file.TotalSymbols-file.MatchedSymbolCount))
		} else {
			lines = append(lines, "  symbols: no structural items detected")
		}
		nonCodeCap := nonCodeMatchCap(file.Language)
		fileDisplayed := 0
		for gi := range file.Groups {
			if maxMatches > 0 && displayed >= maxMatches {
				break
			}
			remainingFile := fileDisplayed + 1000
			if nonCodeCap > 0 {
				remainingFile = nonCodeCap - fileDisplayed
				if remainingFile <= 0 {
					break
				}
			}
			remaining := remainingFile
			if maxMatches > 0 {
				rem := maxMatches - displayed
				if rem < remaining {
					remaining = rem
				}
			}
			if remaining == 0 {
				break
			}
			group := &file.Groups[gi]
			visible := group.ResolvedMatches(file.Matches)
			if len(visible) > remaining {
				visible = visible[:remaining]
			}
			if len(visible) == 0 {
				continue
			}
			if group.StartLine != nil && group.EndLine != nil {
				lines = append(lines, fmt.Sprintf("    - %s %s @ %d-%d",
					group.Kind, group.Label, *group.StartLine, *group.EndLine))
			} else {
				lines = append(lines, fmt.Sprintf("    - %s", group.Label))
			}
			for _, lineMatch := range visible {
				lines = append(lines, fmt.Sprintf("      - @ %d %s",
					lineMatch.LineNumber,
					CompactRenderedMatchLine(lineMatch.LineText, args.Query, args.Regex)))
				fileDisplayed++
				displayed++
			}
		}
	}

	if maxMatches > 0 && result.TotalMatches > displayed {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf(
			"... %d more matches omitted (max_regions=%d)", result.TotalMatches-displayed, maxMatches))
	}
	return strings.Join(lines, "\n")
}

func nonCodeMatchCap(language string) int {
	switch language {
	case "json", "yaml", "markdown", "text", "":
		return maxNonCodeMatchLinesPerFile
	default:
		return 0
	}
}

// CompactRenderedMatchLine caps a match line, keeping context around the
// first match of the query.
func CompactRenderedMatchLine(line, query string, regex bool) string {
	runes := []rune(line)
	if len(runes) <= maxRenderedMatchLineChars {
		return line
	}
	matchStart := 0
	if !regex && query != "" {
		if idx := strings.Index(line, query); idx >= 0 {
			matchStart = len([]rune(line[:idx]))
		}
	}
	start := matchStart - renderedMatchPrefixContextChars
	if start < 0 {
		start = 0
	}
	end := start + maxRenderedMatchLineChars
	if end > len(runes) {
		end = len(runes)
	}
	if end-maxRenderedMatchLineChars < start {
		start = end - maxRenderedMatchLineChars
	}
	if start < 0 {
		start = 0
	}
	snippet := string(runes[start:end])
	omittedPrefix, omittedSuffix := start, len(runes)-end
	switch {
	case omittedPrefix > 0 && omittedSuffix > 0:
		return fmt.Sprintf("…%s … [truncated: %d chars before, %d chars after]",
			snippet, omittedPrefix, omittedSuffix)
	case omittedPrefix > 0:
		return fmt.Sprintf("…%s [truncated: %d chars before]", snippet, omittedPrefix)
	case omittedSuffix > 0:
		return fmt.Sprintf("%s … [truncated: %d chars after]", snippet, omittedSuffix)
	default:
		return snippet
	}
}

// CompactRegionBodyLine caps a region body line without match centering.
func CompactRegionBodyLine(line string) string {
	runes := []rune(line)
	if len(runes) <= maxRenderedMatchLineChars {
		return line
	}
	return fmt.Sprintf("%s … [truncated: %d chars after]",
		string(runes[:maxRenderedMatchLineChars]), len(runes)-maxRenderedMatchLineChars)
}

// RenderFindOutput renders ranked find results.
func RenderFindOutput(result FindResult, args FindArgs) string {
	if args.PathsOnly {
		paths := make([]string, 0, len(result.Files))
		for _, file := range result.Files {
			paths = append(paths, file.Path)
		}
		return strings.Join(paths, "\n")
	}
	lines := []string{
		fmt.Sprintf("query: %s", result.Query),
		fmt.Sprintf("top files: %d", len(result.Files)),
	}
	for idx, file := range result.Files {
		lines = append(lines, "", fmt.Sprintf("%d. %s", idx+1, file.Path),
			fmt.Sprintf("   role: %s", file.Role), "   why:")
		for _, reason := range file.Why {
			lines = append(lines, fmt.Sprintf("     - %s", reason))
		}
		if args.DebugScore {
			lines = append(lines, fmt.Sprintf("   score: %d", file.Score))
		}
		lines = append(lines, "   structure:")
		for _, item := range file.Structure.Items {
			lines = append(lines, fmt.Sprintf("     - %s %s @ %d-%d (%d lines)",
				item.Kind, item.Label, item.StartLine, item.EndLine, item.LineCount))
		}
		if file.Structure.OmittedCount > 0 {
			lines = append(lines, fmt.Sprintf("     ... %d more symbols",
				file.Structure.OmittedCount))
		}
	}
	return strings.Join(lines, "\n")
}

// RenderOutlineOutput renders a single-file outline.
func RenderOutlineOutput(result OutlineResult) string {
	symbolCount := len(result.Structure.Items) + result.Structure.OmittedCount
	lines := []string{
		fmt.Sprintf("file: %s", result.Path),
		fmt.Sprintf("language: %s", result.Language),
		fmt.Sprintf("role: %s", result.Role),
		fmt.Sprintf("lines: %d", result.TotalLines),
		fmt.Sprintf("symbols: %d", symbolCount),
		"",
		"structure:",
	}
	if len(result.Structure.Items) == 0 {
		lines = append(lines, "  (no structural items detected)")
	} else {
		for _, item := range result.Structure.Items {
			lines = append(lines, fmt.Sprintf("  - %s %s @ %d-%d (%d lines)",
				item.Kind, item.Label, item.StartLine, item.EndLine, item.LineCount))
		}
		if result.Structure.OmittedCount > 0 {
			lines = append(lines, fmt.Sprintf("  ... %d more symbols",
				result.Structure.OmittedCount))
		}
	}
	if result.ContextApplied != "" {
		lines = append(lines, "", fmt.Sprintf("context: %s", result.ContextApplied))
	}
	return strings.Join(lines, "\n")
}
