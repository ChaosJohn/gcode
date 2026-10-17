package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// Write tool (ported from tool/write.rs).

const (
	fileTouchPreviewMaxLines = 6
	fileTouchPreviewMaxBytes = 240
)

// WriteTool writes a file.
type WriteTool struct{}

// NewWriteTool builds the write tool.
func NewWriteTool() *WriteTool { return &WriteTool{} }

// Name implements Tool.
func (t *WriteTool) Name() string { return "write" }

// Description implements Tool.
func (t *WriteTool) Description() string { return "Write a file." }

// ParametersSchema implements Tool.
func (t *WriteTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["file_path", "content"],
		"properties": {
			"file_path": {"type": "string", "description": "File path."},
			"content": {"type": "string", "description": "File content."}
		}
	}`)
}

type writeInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// Execute implements Tool.
func (t *WriteTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params writeInput
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	path := ctx.ResolvePath(params.FilePath)

	if parent := filepath.Dir(path); parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return toolcore.ToolOutput{}, err
		}
	}

	unlock := lockPath(path)
	defer unlock()
	existed := false
	if _, err := os.Stat(path); err == nil {
		existed = true
	}
	var oldContent *string
	if existed {
		if raw, err := os.ReadFile(path); err == nil {
			text := string(raw)
			oldContent = &text
		}
	}

	if err := os.WriteFile(path, []byte(params.Content), 0o644); err != nil {
		return toolcore.ToolOutput{}, err
	}

	lineCount := countLines(params.Content)
	var diff string
	if oldContent != nil {
		diff = generateDiffSummary(*oldContent, params.Content, 20)
	} else {
		diff = generateDiffSummary("", params.Content, 20)
	}
	detail := buildFileTouchPreview(diff)

	publishFileTouch(FileTouch{
		SessionID: ctx.SessionID,
		Path:      path,
		Op:        "write",
		Intent:    trimmedIntent(input),
		Summary: map[bool]string{true: fmt.Sprintf("overwrote file (%d lines)", lineCount),
			false: fmt.Sprintf("created new file (%d lines)", lineCount)}[existed],
		Detail: detail,
	})

	var body string
	if existed {
		separator := ""
		if diff != "" {
			separator = ":"
		}
		body = fmt.Sprintf("Updated %s (%d lines)%s\n%s", params.FilePath, lineCount, separator, diff)
	} else {
		body = fmt.Sprintf("Created %s (%d lines):\n%s", params.FilePath, lineCount, diff)
	}
	body += ConfigEditNotice(path, derefString(oldContent), params.Content)

	output := toolcore.NewToolOutput(body).WithTitle(params.FilePath)
	// Do not claim an authoritative diff when the old file was unreadable.
	if !existed || oldContent != nil {
		oldPath := params.FilePath
		if !existed {
			oldPath = "/dev/null"
		}
		output = attachFileDiff(output, unifiedFileDiff(oldPath, params.FilePath, derefString(oldContent), params.Content))
	}
	return output, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	count := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		count++
	}
	return count
}

// generateDiffSummary ports write.rs's compact diff: "42- old" / "42+ new",
// trimmed content, empty lines skipped, capped at maxLines.
func generateDiffSummary(old, new string, maxLines int) string {
	ops := diffLines(old, new)
	var out strings.Builder
	linesShown := 0
	for _, op := range ops {
		content := trimSpace(op.text)
		switch op.kind {
		case ' ':
			continue
		case '-':
			if content == "" {
				continue
			}
			if linesShown >= maxLines {
				out.WriteString("...\n")
				return trimTrailing(out.String())
			}
			fmt.Fprintf(&out, "%d- %s\n", op.oldLine, content)
			linesShown++
		case '+':
			if content == "" {
				continue
			}
			if linesShown >= maxLines {
				out.WriteString("...\n")
				return trimTrailing(out.String())
			}
			fmt.Fprintf(&out, "%d+ %s\n", op.newLine, content)
			linesShown++
		}
	}
	return trimTrailing(out.String())
}

func trimTrailing(s string) string { return strings.TrimRight(s, "\n") }

// buildFileTouchPreview caps the diff preview for the bus event.
func buildFileTouchPreview(diff string) string {
	trimmed := trimSpace(diff)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	preview := strings.Join(lines, "\n")
	truncated := false
	if len(lines) > fileTouchPreviewMaxLines {
		preview = strings.Join(lines[:fileTouchPreviewMaxLines], "\n")
		truncated = true
	}
	if len(preview) > fileTouchPreviewMaxBytes {
		preview = trimSpace(truncateRunes(preview, fileTouchPreviewMaxBytes))
		truncated = true
	}
	if truncated {
		preview += "\n…"
	}
	return preview
}

// unifiedFileDiff ports file_diff.rs unified().
func unifiedFileDiff(oldPath, newPath, old, new string) string {
	return UnifiedDiff(quoteDiffHeader(oldPath, "a"), quoteDiffHeader(newPath, "b"), old, new)
}

// attachFileDiff appends the fenced diff block and metadata (file_diff.rs
// attach). Empty diffs mean "unknown" or no-op and attach nothing.
func attachFileDiff(output toolcore.ToolOutput, diff string) toolcore.ToolOutput {
	if diff == "" {
		return output
	}
	output.Output += "\n\nFile diff:\n```diff\n" + diff
	if !strings.HasSuffix(diff, "\n") {
		output.Output += "\n"
	}
	output.Output += "```\n"
	metadata := map[string]any{"diff": diff}
	encoded, err := json.Marshal(metadata)
	if err == nil {
		output.Metadata = encoded
	}
	return output
}
