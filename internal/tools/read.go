package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ChaosJohn/gcode/internal/imaging"
	"github.com/ChaosJohn/gcode/internal/pdf"
	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// Read tool (ported from tool/read.rs).

const (
	readDefaultLimit = 5000
	readMaxLineLen   = 2000
)

// ReadTool reads text, image, and PDF files.
type ReadTool struct{}

// NewReadTool builds the read tool.
func NewReadTool() *ReadTool { return &ReadTool{} }

// Name implements Tool.
func (t *ReadTool) Name() string { return "read" }

// Description implements Tool.
func (t *ReadTool) Description() string {
	return "Read a file. Supports text files, image files, and PDFs."
}

// ParametersSchema implements Tool.
func (t *ReadTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["file_path"],
		"properties": {
			"file_path": {"type": "string", "description": "Path to a file."},
			"start_line": {"type": "integer", "description": "1-based start line for text files."},
			"limit": {"type": "integer", "description": "Max text lines to read. Default 5000."}
		}
	}`)
}

type readInput struct {
	FilePath  string `json:"file_path"`
	StartLine *int   `json:"start_line"`
	EndLine   *int   `json:"end_line"`
	Offset    *int   `json:"offset"`
	Limit     *int   `json:"limit"`
}

type readRangeStyle int

const (
	readStyleOffsetLimit readRangeStyle = iota
	readStyleStartEnd
)

type readRange struct {
	offset, limit int
	style         readRangeStyle
}

func (r readRange) nextOffset() int    { return r.offset + r.limit }
func (r readRange) nextStartLine() int { return r.nextOffset() + 1 }

// normalizeReadRange ports normalize_read_range: two styles, never mixed.
func normalizeReadRange(params *readInput) (readRange, error) {
	hasStartEnd := params.StartLine != nil || params.EndLine != nil
	hasMixedOffset := false
	switch {
	case params.StartLine != nil && params.Offset != nil:
		hasMixedOffset = *params.StartLine == 0 || *params.Offset+1 != *params.StartLine
	case params.StartLine == nil && params.EndLine != nil && params.Offset != nil:
		hasMixedOffset = *params.Offset != 0
	default:
		hasMixedOffset = params.Offset != nil
	}

	if hasStartEnd && hasMixedOffset {
		return readRange{}, fmt.Errorf("Use either start_line/end_line (1-based) or offset (0-based), not both. `limit` may be used with either style.")
	}

	if hasStartEnd {
		startLine := 1
		if params.StartLine != nil {
			startLine = *params.StartLine
		}
		if startLine == 0 {
			return readRange{}, fmt.Errorf("start_line must be 1 or greater (it is 1-based).")
		}
		limit := 0
		if params.EndLine != nil {
			endLine := *params.EndLine
			if endLine == 0 {
				return readRange{}, fmt.Errorf("end_line must be 1 or greater (it is 1-based).")
			}
			if endLine < startLine {
				return readRange{}, fmt.Errorf("end_line (%d) must be greater than or equal to start_line (%d).", endLine, startLine)
			}
			limit = endLine - startLine + 1
		} else if params.Limit != nil {
			limit = *params.Limit
		} else {
			limit = readDefaultLimit
		}
		return readRange{offset: startLine - 1, limit: limit, style: readStyleStartEnd}, nil
	}

	offset := 0
	if params.Offset != nil {
		offset = *params.Offset
	}
	limit := readDefaultLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	return readRange{offset: offset, limit: limit, style: readStyleOffsetLimit}, nil
}

// Execute implements Tool.
func (t *ReadTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params readInput
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	rng, err := normalizeReadRange(&params)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}

	path := ctx.ResolvePath(params.FilePath)

	if _, statErr := os.Stat(path); statErr != nil {
		suggestions := findSimilarFiles(path)
		if len(suggestions) == 0 {
			return toolcore.ToolOutput{}, fmt.Errorf("File not found: %s", params.FilePath)
		}
		return toolcore.ToolOutput{}, fmt.Errorf("File not found: %s\nDid you mean: %s",
			params.FilePath, strings.Join(suggestions, ", "))
	}

	if isImageFile(path) {
		return handleImageFile(path, params.FilePath)
	}
	if isPDFFile(path) {
		return handlePDFFile(path, params.FilePath)
	}
	if isBinaryFile(path) {
		return toolcore.NewToolOutput(fmt.Sprintf(
			"Binary file detected: %s\nUse appropriate tools to handle binary files.",
			params.FilePath)), nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	text := string(content)

	var out strings.Builder
	totalLines := 0
	truncatedLineCount := 0
	endExclusive := rng.offset + rng.limit
	for i, line := range strings.Split(text, "\n") {
		// strings.Split yields a trailing "" for \n-terminated files; the
		// upstream `lines()` iterator does not count it.
		if line == "" && i == len(strings.Split(text, "\n"))-1 && len(text) > 0 && text[len(text)-1] == '\n' {
			break
		}
		totalLines = i + 1
		if i < rng.offset {
			continue
		}
		if i >= endExclusive {
			continue
		}
		lineNum := i + 1
		if len(line) > readMaxLineLen {
			truncatedLineCount++
			fmt.Fprintf(&out, "%5d\t%s...\n", lineNum, truncateRunes(line, readMaxLineLen))
		} else {
			fmt.Fprintf(&out, "%5d\t%s\n", lineNum, line)
		}
	}
	end := endExclusive
	if end > totalLines {
		end = totalLines
	}

	publishFileTouch(FileTouch{
		SessionID: ctx.SessionID,
		Path:      path,
		Op:        "read",
		Summary:   fmt.Sprintf("read lines %d-%d of %d", rng.offset+1, end, totalLines),
	})

	if end < totalLines {
		hint := fmt.Sprintf("offset=%d", rng.nextOffset())
		if rng.style == readStyleStartEnd {
			hint = fmt.Sprintf("start_line=%d", rng.nextStartLine())
		}
		fmt.Fprintf(&out, "\n... %d more lines (use %s to continue)\n", totalLines-end, hint)
	}

	if out.Len() == 0 {
		return toolcore.NewToolOutput("(empty file)"), nil
	}
	return toolcore.NewToolOutput(out.String()), nil
}

// findSimilarFiles suggests files in the same directory with a simple
// containment similarity check (max 3).
func findSimilarFiles(path string) []string {
	parent := filepath.Dir(path)
	filename := strings.ToLower(filepath.Base(path))
	var suggestions []string
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if contains(name, filename) || contains(filename, name) {
			suggestions = append(suggestions, filepath.Join(parent, entry.Name()))
			if len(suggestions) >= 3 {
				break
			}
		}
	}
	return suggestions
}

var binaryExtensions = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "bmp": true, "ico": true,
	"webp": true, "zip": true, "tar": true, "gz": true, "bz2": true, "xz": true,
	"7z": true, "rar": true, "exe": true, "dll": true, "so": true, "dylib": true,
	"o": true, "a": true, "class": true, "pyc": true, "wasm": true, "mp3": true,
	"mp4": true, "avi": true, "mov": true, "mkv": true, "flac": true, "ogg": true,
	"wav": true,
}

// isBinaryFile checks by extension first, then null-byte density in the
// first 8KB.
func isBinaryFile(path string) bool {
	ext := strings.ToLower(trimPrefix(filepath.Ext(path), "."))
	if binaryExtensions[ext] {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	buf := make([]byte, 8192)
	n, err := file.Read(buf)
	if err != nil || n == 0 {
		return false
	}
	nulls := 0
	for _, b := range buf[:n] {
		if b == 0 {
			nulls++
		}
	}
	return nulls > n/10
}

func isImageFile(path string) bool {
	switch strings.ToLower(trimPrefix(filepath.Ext(path), ".")) {
	case "png", "jpg", "jpeg", "gif", "webp", "bmp", "ico", "tif", "tiff":
		return true
	}
	return false
}

func isPDFFile(path string) bool {
	return strings.ToLower(trimPrefix(filepath.Ext(path), ".")) == "pdf"
}

const maxImageSize = 20 * 1024 * 1024

// handleImageFile returns the image base64-encoded for model vision, or a
// human-readable refusal.
func handleImageFile(path, filePath string) (toolcore.ToolOutput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	fileSize := len(data)
	w, h, hasDims := imaging.ImageDimensions(data)
	dimStr := "unknown"
	if hasDims {
		dimStr = fmt.Sprintf("%dx%d", w, h)
	}
	sizeStr := humanSize(fileSize)

	var output toolcore.ToolOutput
	if fileSize <= maxImageSize {
		image, normErr := imaging.NormalizeImageBytes(data)
		if normErr == nil {
			converted := ""
			if image.ConvertedFrom != "" {
				converted = fmt.Sprintf("Converted from %s to PNG. ", image.ConvertedFrom)
			}
			output = toolcore.NewToolOutput(fmt.Sprintf(
				"Image: %s (%s)\nDimensions: %s\n%sImage sent to model for vision analysis.",
				filePath, sizeStr, dimStr, converted)).
				WithLabeledImage(image.MediaType, base64.StdEncoding.EncodeToString(image.Data), filePath)
		} else {
			output = toolcore.NewToolOutput(fmt.Sprintf(
				"Image: %s (%s)\nDimensions: %s\nNot sent to the model: %s.",
				filePath, sizeStr, dimStr, normErr.Error()))
		}
	} else {
		output = toolcore.NewToolOutput(fmt.Sprintf(
			"Image: %s (%s)\nDimensions: %s\nImage too large for vision (max 20MB).",
			filePath, sizeStr, dimStr))
	}
	return output.WithTitle("📷 " + filePath), nil
}

func humanSize(size int) string {
	switch {
	case size < 1024:
		return fmt.Sprintf("%d bytes", size)
	case size < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(size)/1024.0)
	default:
		return fmt.Sprintf("%.1f MB", float64(size)/1024.0/1024.0)
	}
}

// handlePDFFile extracts text with per-page truncation, falling back to
// metadata on failure.
func handlePDFFile(path, filePath string) (toolcore.ToolOutput, error) {
	info, err := os.Stat(path)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	sizeStr := humanSize(int(info.Size()))

	text, extractErr := pdf.ExtractText(path)
	if extractErr != nil {
		return toolcore.NewToolOutput(fmt.Sprintf(
			"PDF: %s (%s)\nCould not extract text: %s\nThis may be a scanned/image-based PDF.",
			filePath, sizeStr, extractErr.Error())), nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "PDF: %s (%s)\n", filePath, sizeStr)
	out.WriteString(strings.Repeat("=", 60) + "\n")
	pages := strings.Split(text, "\x0c")
	fmt.Fprintf(&out, "Pages: %d\n\n", len(pages))
	for i, page := range pages {
		pageText := trimSpace(page)
		if pageText == "" {
			continue
		}
		fmt.Fprintf(&out, "--- Page %d ---\n", i+1)
		if len(pageText) > 10000 {
			out.WriteString(truncateRunes(pageText, 10000))
			out.WriteString("\n... (page truncated)\n")
		} else {
			out.WriteString(pageText)
		}
		out.WriteString("\n\n")
	}
	return toolcore.NewToolOutput(out.String()), nil
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
