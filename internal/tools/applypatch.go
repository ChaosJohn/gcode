package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// apply_patch tool (ported from tool/apply_patch.rs): multi-file patches in
// the `*** Begin Patch` format with Add/Delete/Update File hunks, @@ context,
// ' '/'-'/'+' diff lines, `*** End of File` anchoring and `*** Move to:` renames.

// ApplyPatchTool applies a multi-file patch atomically per file.
type ApplyPatchTool struct{}

// NewApplyPatchTool builds the tool.
func NewApplyPatchTool() *ApplyPatchTool { return &ApplyPatchTool{} }

// Name implements Tool.
func (t *ApplyPatchTool) Name() string { return "apply_patch" }

// Description implements Tool.
func (t *ApplyPatchTool) Description() string {
	return "Apply a multi-file patch: create, delete, move, and edit files in one call."
}

// ParametersSchema implements Tool.
func (t *ApplyPatchTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["patch_text"],
		"properties": {
			"patch_text": {
				"type": "string",
				"description": "Patch in the *** Begin Patch / *** End Patch format: *** Add File:, *** Delete File:, *** Update File: (with optional *** Move to:), @@ context lines, ' '/'-'/'+' diff lines, *** End of File anchor."
			}
		}
	}`)
}

type updateFileChunk struct {
	changeContext *string
	oldLines      []string
	newLines      []string
	isEndOfFile   bool
}

type patchHunk struct {
	kind     string // add | delete | update
	path     string
	moveTo   *string
	contents string // add
	chunks   []updateFileChunk
}

// parseApplyPatch ports parse_apply_patch.
func parseApplyPatch(input string) ([]patchHunk, error) {
	lines := strings.Split(input, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "*** Begin Patch" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("Patch must contain *** Begin Patch")
	}
	var hunks []patchHunk
	i := start + 1
	for i < len(lines) {
		line := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(line) == "*** End Patch" {
			break
		}
		if path, ok := strings.CutPrefix(line, "*** Add File: "); ok {
			i++
			var contents strings.Builder
			for i < len(lines) {
				current := lines[i]
				if strings.HasPrefix(current, "*** ") {
					break
				}
				if added, ok := strings.CutPrefix(current, "+"); ok {
					contents.WriteString(added)
					contents.WriteString("\n")
				}
				i++
			}
			hunks = append(hunks, patchHunk{kind: "add", path: strings.TrimSpace(path),
				contents: contents.String()})
			continue
		}
		if path, ok := strings.CutPrefix(line, "*** Delete File: "); ok {
			hunks = append(hunks, patchHunk{kind: "delete", path: strings.TrimSpace(path)})
			i++
			continue
		}
		if path, ok := strings.CutPrefix(line, "*** Update File: "); ok {
			i++
			var moveTo *string
			if i < len(lines) {
				if target, ok := strings.CutPrefix(strings.TrimRight(lines[i], "\r"), "*** Move to: "); ok {
					trimmed := strings.TrimSpace(target)
					moveTo = &trimmed
					i++
				}
			}
			var chunks []updateFileChunk
			isFirstChunk := true
			for i < len(lines) {
				current := strings.TrimRight(lines[i], "\r")
				if strings.HasPrefix(current, "*** ") && current != "*** End of File" {
					break
				}
				if strings.TrimSpace(current) == "" && !strings.HasPrefix(current, " ") &&
					!strings.HasPrefix(current, "+") && !strings.HasPrefix(current, "-") {
					i++
					continue
				}
				var changeContext *string
				if current == "@@" {
					i++
				} else if ctx, ok := strings.CutPrefix(current, "@@ "); ok {
					trimmed := ctx
					changeContext = &trimmed
					i++
				} else if isFirstChunk {
					// Context-less first chunk.
				} else {
					break
				}
				var oldLines, newLines []string
				isEndOfFile := false
				hadDiffLines := false
				for i < len(lines) {
					cl := lines[i]
					if cl == "*** End of File" || cl == "*** End of File\r" {
						isEndOfFile = true
						i++
						break
					}
					trimmed := strings.TrimRight(cl, "\r")
					if strings.HasPrefix(trimmed, "*** ") || strings.HasPrefix(trimmed, "@@") {
						break
					}
					if content, ok := strings.CutPrefix(trimmed, " "); ok {
						oldLines = append(oldLines, content)
						newLines = append(newLines, content)
						hadDiffLines = true
					} else if content, ok := strings.CutPrefix(trimmed, "+"); ok {
						newLines = append(newLines, content)
						hadDiffLines = true
					} else if content, ok := strings.CutPrefix(trimmed, "-"); ok {
						oldLines = append(oldLines, content)
						hadDiffLines = true
					} else if trimmed == "" {
						oldLines = append(oldLines, "")
						newLines = append(newLines, "")
						hadDiffLines = true
					} else {
						if hadDiffLines {
							break
						}
						i++
						continue
					}
					i++
				}
				if hadDiffLines || changeContext != nil {
					chunks = append(chunks, updateFileChunk{changeContext: changeContext,
						oldLines: oldLines, newLines: newLines, isEndOfFile: isEndOfFile})
				}
				isFirstChunk = false
			}
			if len(chunks) == 0 {
				return nil, fmt.Errorf("Update file hunk for '%s' has no changes", path)
			}
			hunks = append(hunks, patchHunk{kind: "update", path: strings.TrimSpace(path),
				moveTo: moveTo, chunks: chunks})
			continue
		}
		i++
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("No valid patch directives found")
	}
	return hunks, nil
}

// seekSequence ports seek_sequence: forward search from start (or anchored
// to the end for EOF mode), exact first then trim-end tolerant.
func seekSequence(lines []string, pattern []string, start int, eof bool) int {
	if len(pattern) == 0 {
		return start
	}
	if len(pattern) > len(lines) {
		return -1
	}
	searchStart := start
	if eof && len(lines) >= len(pattern) {
		searchStart = len(lines) - len(pattern)
	}
	for i := searchStart; i+len(pattern) <= len(lines); i++ {
		if equalSlices(lines[i:i+len(pattern)], pattern) {
			return i
		}
	}
	for i := searchStart; i+len(pattern) <= len(lines); i++ {
		ok := true
		for j, pat := range pattern {
			if strings.TrimRight(lines[i+j], " \t") != strings.TrimRight(pat, " \t") {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type lineReplacement struct {
	start int
	count int
	lines []string
}

// computeReplacements ports compute_replacements.
func computeReplacements(originalLines []string, display string, chunks []updateFileChunk) ([]lineReplacement, error) {
	var replacements []lineReplacement
	lineIndex := 0
	for _, chunk := range chunks {
		if chunk.changeContext != nil {
			if idx := seekSequence(originalLines, []string{*chunk.changeContext}, lineIndex, false); idx >= 0 {
				lineIndex = idx + 1
			} else {
				return nil, fmt.Errorf("Failed to find context '%s' in %s", *chunk.changeContext, display)
			}
		}
		if len(chunk.oldLines) == 0 {
			insertionIdx := len(originalLines)
			if len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
				insertionIdx = len(originalLines) - 1
			}
			replacements = append(replacements, lineReplacement{start: insertionIdx, count: 0,
				lines: chunk.newLines})
			continue
		}
		pattern := chunk.oldLines
		newSlice := chunk.newLines
		found := seekSequence(originalLines, pattern, lineIndex, chunk.isEndOfFile)
		if found < 0 && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(newSlice) > 0 && newSlice[len(newSlice)-1] == "" {
				newSlice = newSlice[:len(newSlice)-1]
			}
			found = seekSequence(originalLines, pattern, lineIndex, chunk.isEndOfFile)
		}
		if found >= 0 {
			replacements = append(replacements, lineReplacement{start: found,
				count: len(pattern), lines: newSlice})
			lineIndex = found + len(pattern)
		} else {
			return nil, fmt.Errorf("Failed to find expected lines in %s:\n%s", display,
				strings.Join(chunk.oldLines, "\n"))
		}
	}
	sort.Slice(replacements, func(a, b int) bool {
		return replacements[a].start < replacements[b].start
	})
	return replacements, nil
}

func applyReplacements(originalLines []string, replacements []lineReplacement) []string {
	var out []string
	index := 0
	for _, replacement := range replacements {
		out = append(out, originalLines[index:replacement.start]...)
		out = append(out, replacement.lines...)
		index = replacement.start + replacement.count
	}
	out = append(out, originalLines[index:]...)
	return out
}

// applyUpdateChunks ports apply_update_chunks.
func applyUpdateChunks(path, display string, chunks []updateFileChunk) (string, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	originalContents := string(raw)
	originalLines := strings.Split(originalContents, "\n")
	if len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
		originalLines = originalLines[:len(originalLines)-1]
	}
	replacements, err := computeReplacements(originalLines, display, chunks)
	if err != nil {
		return "", "", err
	}
	newLines := applyReplacements(originalLines, replacements)
	if len(newLines) == 0 || newLines[len(newLines)-1] != "" {
		newLines = append(newLines, "")
	}
	return originalContents, strings.Join(newLines, "\n"), nil
}

// Execute implements Tool.
func (t *ApplyPatchTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params struct {
		PatchText string `json:"patch_text"`
	}
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	hunks, err := parseApplyPatch(params.PatchText)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}

	// Conflicting hunks against the same file are rejected up front
	// (ported capture of whole-file states).
	seen := map[string]string{}
	for _, hunk := range hunks {
		if previous, exists := seen[hunk.path]; exists && previous != hunk.kind {
			return toolcore.ToolOutput{}, fmt.Errorf(
				"Conflicting patch hunks for %s (%s and %s)", hunk.path, previous, hunk.kind)
		}
		seen[hunk.path] = hunk.kind
	}

	type appliedChange struct {
		display string
		path    string
		before  string
		after   string
	}
	var changes []appliedChange
	var pendingWrites []appliedChange
	for _, hunk := range hunks {
		path := ctx.ResolvePath(hunk.path)
		display := hunk.path
		switch hunk.kind {
		case "add":
			if _, err := os.Stat(path); err == nil {
				return toolcore.ToolOutput{}, fmt.Errorf("Add File %s: already exists", hunk.path)
			}
			pendingWrites = append(pendingWrites, appliedChange{display: display,
				path: path, before: "", after: hunk.contents})
		case "delete":
			if _, err := os.Stat(path); err != nil {
				return toolcore.ToolOutput{}, fmt.Errorf("Delete File %s: %v", hunk.path, err)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return toolcore.ToolOutput{}, readErr
			}
			pendingWrites = append(pendingWrites, appliedChange{display: display,
				path: path, before: string(raw), after: ""})
		case "update":
			if _, err := os.Stat(path); err != nil {
				return toolcore.ToolOutput{}, fmt.Errorf("Update File %s: not found", hunk.path)
			}
			before, after, err := applyUpdateChunks(path, display, hunk.chunks)
			if err != nil {
				return toolcore.ToolOutput{}, err
			}
			target := path
			if hunk.moveTo != nil {
				target = ctx.ResolvePath(*hunk.moveTo)
			}
			pendingWrites = append(pendingWrites, appliedChange{display: display,
				path: target, before: before, after: after})
		}
		changes = append(changes, appliedChange{display: display, path: path})
	}
	_ = changes

	var unified strings.Builder
	for _, change := range pendingWrites {
		unified.WriteString(UnifiedDiff(change.display, change.display,
			change.before, change.after))
	}
	for _, change := range pendingWrites {
		if change.after == "" {
			if err := os.Remove(change.path); err != nil &&
				!os.IsNotExist(err) {
				return toolcore.ToolOutput{}, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(change.path), 0o755); err != nil {
			return toolcore.ToolOutput{}, err
		}
		if err := os.WriteFile(change.path, []byte(change.after), 0o644); err != nil {
			return toolcore.ToolOutput{}, fmt.Errorf("writing %s: %w", change.display, err)
		}
	}
	verb := fmt.Sprintf("Applied patch to %d file(s)", len(pendingWrites))
	body := fmt.Sprintf("%s\n%s", verb, strings.TrimRight(unified.String(), "\n"))
	return toolcore.NewToolOutput(body).WithTitle("apply_patch"), nil
}
