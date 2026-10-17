package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// Edit tool (ported from tool/edit.rs): exact replacements, all-or-nothing.

// EditTool edits a file by exact replacement.
type EditTool struct{}

// NewEditTool builds the edit tool.
func NewEditTool() *EditTool { return &EditTool{} }

// Name implements Tool.
func (t *EditTool) Name() string { return "edit" }

// Description implements Tool.
func (t *EditTool) Description() string {
	return "Edit a file by exact replacement. All edits apply or none do."
}

// ParametersSchema implements Tool.
func (t *EditTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["file_path"],
		"properties": {
			"file_path": {"type": "string", "description": "File path."},
			"edits": {
				"type": "array",
				"description": "Replacements applied in order. Each old_string must match exactly once unless replace_all is set.",
				"items": {
					"type": "object",
					"required": ["old_string", "new_string"],
					"properties": {
						"old_string": {"type": "string", "description": "Exact text to replace."},
						"new_string": {"type": "string", "description": "Replacement text."},
						"replace_all": {"type": "boolean", "description": "Replace every match."}
					}
				}
			},
			"old_string": {"type": "string", "description": "Single-edit shorthand: exact text to replace. Omit when using edits."},
			"new_string": {"type": "string", "description": "Single-edit shorthand: replacement text."},
			"replace_all": {"type": "boolean", "description": "Single-edit shorthand: replace every match."}
		}
	}`)
}

type editOperation struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type editInput struct {
	FilePath   string          `json:"file_path"`
	Edits      []editOperation `json:"edits"`
	OldString  *string         `json:"old_string"`
	NewString  *string         `json:"new_string"`
	ReplaceAll bool            `json:"replace_all"`
}

// operations accepts either the edits array or the single-edit shorthand,
// never both (ported from EditInput::operations).
func (e *editInput) operations() ([]editOperation, error) {
	single := e.OldString != nil || e.NewString != nil
	switch {
	case e.Edits != nil && single:
		return nil, fmt.Errorf("Use either `edits` or `old_string`/`new_string`, not both.")
	case e.Edits != nil && len(e.Edits) == 0:
		return nil, fmt.Errorf("`edits` must contain at least one edit.")
	case e.Edits != nil:
		return e.Edits, nil
	case single:
		if e.OldString == nil {
			return nil, fmt.Errorf("`old_string` is required with `new_string`.")
		}
		if e.NewString == nil {
			return nil, fmt.Errorf("`new_string` is required with `old_string`.")
		}
		return []editOperation{{OldString: *e.OldString, NewString: *e.NewString,
			ReplaceAll: e.ReplaceAll}}, nil
	default:
		return nil, fmt.Errorf("Provide `edits` (array of old_string/new_string) or `old_string` and `new_string`.")
	}
}

type appliedEdit struct {
	occurrences int
	startLine   int
}

// applyEdits applies every edit in order to an in-memory copy. Any failure
// aborts the whole call so the file is never left half-edited.
func applyEdits(original string, edits []editOperation, filePath string) (string, []appliedEdit, error) {
	content := original
	var applied []appliedEdit
	var failures []string
	label := func(index int) string {
		if len(edits) == 1 {
			return ""
		}
		return fmt.Sprintf("Edit %d: ", index+1)
	}

	for index, edit := range edits {
		if edit.OldString == edit.NewString {
			failures = append(failures, label(index)+"old_string and new_string must be different")
			continue
		}
		if edit.OldString == "" {
			failures = append(failures, label(index)+"old_string must not be empty")
			continue
		}
		occurrences := strings.Count(content, edit.OldString)
		if occurrences == 0 {
			failures = append(failures, label(index)+flexibleMatchHint(content, edit.OldString, filePath))
			continue
		}
		if occurrences > 1 && !edit.ReplaceAll {
			failures = append(failures, fmt.Sprintf(
				"%sold_string found %d times. Either:\n1. Provide more context to make it unique, or\n2. Set replace_all: true to replace all occurrences",
				label(index), occurrences))
			continue
		}
		startLine := findLineNumber(content, edit.OldString)
		if edit.ReplaceAll {
			content = strings.ReplaceAll(content, edit.OldString, edit.NewString)
		} else {
			content = strings.Replace(content, edit.OldString, edit.NewString, 1)
		}
		applied = append(applied, appliedEdit{occurrences: occurrences, startLine: startLine})
	}

	if len(failures) == 0 {
		return content, applied, nil
	}
	if len(edits) == 1 {
		return "", nil, fmt.Errorf("%s", failures[0])
	}
	return "", nil, fmt.Errorf(
		"No changes written to %s. %d of %d edits failed:\n%s\n Edits apply in order, so later edits see the result of earlier ones.",
		filePath, len(failures), len(edits), joinMapped(failures, func(f string) string { return "  ✗ " + f }, "\n"))
}

// Execute implements Tool.
func (t *EditTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params editInput
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	edits, err := params.operations()
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	path := ctx.ResolvePath(params.FilePath)
	if _, statErr := os.Stat(path); statErr != nil {
		return toolcore.ToolOutput{}, fmt.Errorf("File not found: %s", params.FilePath)
	}

	unlock := lockPath(path)
	defer unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	content := string(raw)
	newContent, applied, err := applyEdits(content, edits, params.FilePath)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return toolcore.ToolOutput{}, err
	}

	intent := trimmedIntent(input)
	var body string
	if len(edits) == 1 {
		edit := applied[0]
		diff := generateEditDiff(edits[0].OldString, edits[0].NewString, edit.startLine)
		newLines := countLines(edits[0].NewString)
		endLine := edit.startLine
		if newLines > 1 {
			endLine += newLines - 1
		}
		publishFileTouch(FileTouch{
			SessionID: ctx.SessionID,
			Path:      path,
			Op:        "edit",
			Intent:    intent,
			Summary: fmt.Sprintf("edited lines %d-%d (%d occurrence%s)", edit.startLine, endLine,
				edit.occurrences, pluralS(edit.occurrences)),
			Detail: buildFileTouchPreview(diff),
		})
		ctxStart, ctxEnd, contextText := extractContext(newContent, edit.startLine, endLine, 3)
		body = fmt.Sprintf(
			"Edited %s: replaced %d occurrence(s)\n%s\n\nContext after edit (lines %d-%d):\n%s",
			params.FilePath, edit.occurrences, diff, ctxStart, ctxEnd, contextText)
	} else {
		diff := generateDiffSummary(content, newContent, 30)
		replaced := 0
		for _, edit := range applied {
			replaced += edit.occurrences
		}
		publishFileTouch(FileTouch{
			SessionID: ctx.SessionID,
			Path:      path,
			Op:        "edit",
			Intent:    intent,
			Summary:   fmt.Sprintf("applied %d edits (%d replacement%s)", len(edits), replaced, pluralS(replaced)),
			Detail:    buildFileTouchPreview(diff),
		})
		var out strings.Builder
		fmt.Fprintf(&out, "Edited %s: applied %d edits\n", params.FilePath, len(edits))
		for index, edit := range applied {
			fmt.Fprintf(&out, "  ✓ Edit %d: replaced %d occurrence%s at line %d\n",
				index+1, edit.occurrences, pluralS(edit.occurrences), edit.startLine)
		}
		if diff != "" {
			out.WriteString("\nDiff:\n")
			out.WriteString(diff)
		}
		body = out.String()
	}
	body += ConfigEditNotice(path, content, newContent)

	return attachFileDiff(toolcore.NewToolOutput(body).WithTitle(params.FilePath),
		unifiedFileDiff(params.FilePath, params.FilePath, content, newContent)), nil
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func joinMapped(items []string, fn func(string) string, sep string) string {
	mapped := make([]string, len(items))
	for i, item := range items {
		mapped[i] = fn(item)
	}
	return strings.Join(mapped, sep)
}

// findLineNumber finds the 1-based line where a substring starts.
func findLineNumber(content, substring string) int {
	if pos := strings.Index(content, substring); pos >= 0 {
		return strings.Count(content[:pos], "\n") + 1
	}
	return 1
}

// generateEditDiff ports edit.rs generate_diff: "42- old" / "42+ new" starting
// at the edit's line.
func generateEditDiff(old, new string, startLine int) string {
	ops := diffLines(old, new)
	var out strings.Builder
	oldLine, newLine := startLine, startLine
	for _, op := range ops {
		content := trimSpace(op.text)
		switch op.kind {
		case '-':
			num := oldLine
			oldLine++
			if content == "" {
				continue
			}
			fmt.Fprintf(&out, "%d- %s\n", num, content)
		case '+':
			num := newLine
			newLine++
			if content == "" {
				continue
			}
			fmt.Fprintf(&out, "%d+ %s\n", num, content)
		}
	}
	result := out.String()
	if result == "" {
		return ""
	}
	return trimTrailing(result)
}

// extractContext returns lines around the edited region as "%4d│ text".
func extractContext(content string, editStart, editEnd, padding int) (int, int, string) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}
	totalLines := len(lines)
	start := editStart - padding - 1
	if start < 0 {
		start = 0
	}
	end := editEnd + padding
	if end > totalLines {
		end = totalLines
	}
	var contextLines []string
	for i := start; i < end; i++ {
		contextLines = append(contextLines, fmt.Sprintf("%4d│ %s", start+i+1, lines[i]))
	}
	return start + 1, end, strings.Join(contextLines, "\n")
}

// flexibleMatchHint diagnoses near-miss replacements (ported verbatim).
func flexibleMatchHint(content, oldString, filePath string) string {
	trimmed := trimSpace(oldString)
	if trimmed != "" && trimmed != oldString && contains(content, trimmed) {
		return "old_string not found exactly, but found after trimming whitespace. " +
			"Use the exact string from the file, including leading/trailing whitespace."
	}
	oldLines := strings.Split(oldString, "\n")
	contentLines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(oldLines) > 0 && len(oldLines) <= len(contentLines) {
		for i := 0; i+len(oldLines) <= len(contentLines); i++ {
			match := true
			for j := 0; j < len(oldLines); j++ {
				if trimSpace(contentLines[i+j]) != trimSpace(oldLines[j]) {
					match = false
					break
				}
			}
			if match {
				return fmt.Sprintf("old_string not found exactly, but found with different indentation around line %d. "+
					"Preserve the exact whitespace from the file.", i+1)
			}
		}
	}
	return fmt.Sprintf("old_string not found in %s. Use the read tool to see the current file contents.", filePath)
}
