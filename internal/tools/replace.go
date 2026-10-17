package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ChaosJohn/gcode/internal/agentgrep"
	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// replace tool (ported from tool/replace.rs): literal or regex search-
// and-replace across one file or many. All apply or none do.

// ReplaceTool rewrites files with a literal or regex pattern.
type ReplaceTool struct{}

// NewReplaceTool builds the replace tool.
func NewReplaceTool() *ReplaceTool { return &ReplaceTool{} }

// Name implements Tool.
func (t *ReplaceTool) Name() string { return "replace" }

// Description implements Tool.
func (t *ReplaceTool) Description() string {
	return "Literal or regex replace across files. All apply or none do."
}

// ParametersSchema implements Tool.
func (t *ReplaceTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["pattern", "replacement"],
		"properties": {
			"pattern": {"type": "string", "description": "Text to find. Literal unless regex=true."},
			"replacement": {"type": "string", "description": "Replacement. With regex=true, $1 / ${name} expand capture groups (use $$ for a literal $)."},
			"path": {"type": "string", "description": "File or directory to search. Defaults to the working directory."},
			"glob": {"type": "string", "description": "File filter relative to path, e.g. **/*.rs. Respects .gitignore."},
			"regex": {"type": "boolean", "description": "Treat pattern as a regex."},
			"case_insensitive": {"type": "boolean"},
			"multiline": {"type": "boolean", "description": "^/$ match at line boundaries and . matches newlines."},
			"expected_count": {"type": "integer", "minimum": 1, "description": "Total matches expected across all files. Nothing is written if the actual count differs."},
			"dry_run": {"type": "boolean", "description": "Report matches and diff without writing."}
		}
	}`)
}

type replaceMatcher struct {
	regex   *regexp.Regexp
	literal bool
}

func newReplaceMatcher(pattern string, isRegex, caseInsensitive, multiline bool) (*replaceMatcher, error) {
	source := pattern
	if !isRegex {
		source = regexp.QuoteMeta(pattern)
	}
	flags := ""
	if caseInsensitive {
		flags += "i"
	}
	if multiline {
		flags += "m"
	}
	if flags != "" {
		source = "(?" + flags + ")" + source
	}
	compiled, err := regexp.Compile(source)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %s", pattern)
	}
	return &replaceMatcher{regex: compiled, literal: !isRegex}, nil
}

// apply ports Matcher::apply: count matches, then replace all (literal
// mode expands no $-groups).
func (m *replaceMatcher) apply(content, replacement string) (string, int) {
	count := len(m.regex.FindAllStringIndex(content, -1))
	if count == 0 {
		return content, 0
	}
	if m.literal {
		// Literal mode expands no $-groups: quote $ for the regexp engine.
		return m.regex.ReplaceAllLiteralString(content, replacement), count
	}
	return m.regex.ReplaceAllString(content, replacement), count
}

type replaceChange struct {
	display string
	path    string
	before  string
	after   string
	count   int
}

// collectReplaceFiles ports collect_files: a single file, or a walk over
// the directory tree (hidden included, .git skipped, optional glob filter).
func collectReplaceFiles(root, glob string) ([]string, error) {
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		return []string{root}, nil
	}
	var matcher *func(string) (bool, error)
	if glob != "" {
		matcherValue := func(rel string) (bool, error) {
			ok, matchErr := agentgrep.MatchGlobPath(glob, rel)
			if matchErr != nil {
				return false, fmt.Errorf("invalid glob: %s", glob)
			}
			return ok, nil
		}
		matcher = &matcherValue
	}
	var files []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if matcher != nil {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			ok, matchErr := (*matcher)(rel)
			if matchErr != nil || !ok {
				return nil
			}
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, nil
}

// Execute implements Tool.
func (t *ReplaceTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params struct {
		Pattern         string  `json:"pattern"`
		Replacement     string  `json:"replacement"`
		Path            *string `json:"path"`
		Glob            *string `json:"glob"`
		Regex           bool    `json:"regex"`
		CaseInsensitive bool    `json:"case_insensitive"`
		Multiline       bool    `json:"multiline"`
		ExpectedCount   *int    `json:"expected_count"`
		DryRun          bool    `json:"dry_run"`
	}
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	matcher, err := newReplaceMatcher(params.Pattern, params.Regex,
		params.CaseInsensitive, params.Multiline)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	root := ctx.ResolvePath(orDefault(params.Path, "."))
	if _, err := os.Stat(root); err != nil {
		return toolcore.ToolOutput{}, fmt.Errorf("Path not found: %s", root)
	}
	files, err := collectReplaceFiles(root, orDefault(params.Glob, ""))
	if err != nil {
		return toolcore.ToolOutput{}, err
	}

	var changes []replaceChange
	for _, file := range files {
		before, ok := agentgrep.ReadTextFile(file)
		if !ok {
			continue // binary or unreadable
		}
		after, count := matcher.apply(before, params.Replacement)
		if count == 0 {
			continue
		}
		display := displayRelPath(root, file)
		changes = append(changes, replaceChange{display: display, path: file,
			before: before, after: after, count: count})
	}

	total := 0
	for _, change := range changes {
		total += change.count
	}
	if total == 0 {
		return toolcore.ToolOutput{}, fmt.Errorf(
			"No matches for %q under %s. Nothing written.", params.Pattern, root)
	}
	if params.ExpectedCount != nil && *params.ExpectedCount != total {
		return toolcore.ToolOutput{}, fmt.Errorf(
			"Expected %d matches but found %d across %d file(s). Nothing written.\n%s",
			*params.ExpectedCount, total, len(changes), replaceSummaryLines(changes))
	}

	var unified strings.Builder
	for _, change := range changes {
		unified.WriteString(UnifiedDiff(change.display, change.display,
			change.before, change.after))
	}
	verb := "Replaced"
	if params.DryRun {
		verb = "Would replace"
	}
	body := fmt.Sprintf("%s %d match(es) in %d file(s)\n%s\n%s", verb, total,
		len(changes), replaceSummaryLines(changes), unified.String())

	if !params.DryRun {
		for _, change := range changes {
			if err := os.WriteFile(change.path, []byte(change.after), 0o644); err != nil {
				return toolcore.ToolOutput{}, fmt.Errorf("writing %s: %w", change.display, err)
			}
		}
	}
	return toolcore.NewToolOutput(strings.TrimRight(body, "\n")).
		WithTitle(verb), nil
}

func replaceSummaryLines(changes []replaceChange) string {
	lines := make([]string, 0, len(changes))
	for _, change := range changes {
		lines = append(lines, fmt.Sprintf("  %s: %d", change.display, change.count))
	}
	return strings.Join(lines, "\n")
}

func displayRelPath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

func orDefault(value *string, fallback string) string {
	if value != nil && strings.TrimSpace(*value) != "" {
		return *value
	}
	return fallback
}
