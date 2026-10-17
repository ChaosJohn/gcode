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

// Ls tool (ported from tool/ls.rs).

const lsMaxEntries = 100

var lsDefaultIgnore = []string{
	"node_modules", "__pycache__", ".git", "dist", "build", "target",
	".next", ".nuxt", "venv", ".venv", "coverage", ".cache",
}

const lsMaxDepth = 5

// LsTool lists directory contents recursively.
type LsTool struct{}

// NewLsTool builds the ls tool.
func NewLsTool() *LsTool { return &LsTool{} }

// Name implements Tool.
func (t *LsTool) Name() string { return "ls" }

// Description implements Tool.
func (t *LsTool) Description() string { return "List directory contents." }

// ParametersSchema implements Tool.
func (t *LsTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory path."},
			"ignore": {"type": "array", "items": {"type": "string"}, "description": "Ignore patterns."}
		}
	}`)
}

type lsEntry struct {
	name  string
	isDir bool
	depth int
}

type lsInput struct {
	Path   *string  `json:"path"`
	Ignore []string `json:"ignore"`
}

// Execute implements Tool.
func (t *LsTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params lsInput
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	basePath := "."
	if params.Path != nil {
		basePath = *params.Path
	}
	base := ctx.ResolvePath(basePath)

	info, err := os.Stat(base)
	if err != nil {
		return toolcore.ToolOutput{}, fmt.Errorf("Directory not found: %s", basePath)
	}
	if !info.IsDir() {
		return toolcore.ToolOutput{}, fmt.Errorf("Not a directory: %s", basePath)
	}

	ignore := append([]string(nil), lsDefaultIgnore...)
	ignore = append(ignore, params.Ignore...)

	var entries []lsEntry
	collectLsEntries(base, 0, ignore, &entries, lsMaxEntries)
	truncated := len(entries) >= lsMaxEntries

	var out strings.Builder
	fmt.Fprintf(&out, "%s/\n", basePath)
	for _, entry := range entries {
		suffix := ""
		if entry.isDir {
			suffix = "/"
		}
		fmt.Fprintf(&out, "%s%s%s\n", strings.Repeat("  ", entry.depth), entry.name, suffix)
	}
	if truncated {
		fmt.Fprintf(&out, "\n... truncated at %d entries", lsMaxEntries)
	}
	files, dirs := 0, 0
	for _, entry := range entries {
		if entry.isDir {
			dirs++
		} else {
			files++
		}
	}
	fmt.Fprintf(&out, "\n%d files, %d directories", files, dirs)
	return toolcore.NewToolOutput(out.String()), nil
}

func collectLsEntries(dir string, depth int, ignore []string, entries *[]lsEntry, max int) {
	if len(*entries) >= max || depth > lsMaxDepth+1 {
		return
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	sort.Slice(items, func(i, j int) bool {
		aDir := items[i].IsDir()
		bDir := items[j].IsDir()
		if aDir != bDir {
			return aDir
		}
		return items[i].Name() < items[j].Name()
	})
	for _, item := range items {
		if len(*entries) >= max {
			break
		}
		name := item.Name()
		ignored := false
		for _, pattern := range ignore {
			if globMatch(pattern, name) || name == pattern {
				ignored = true
				break
			}
		}
		if ignored || (strings.HasPrefix(name, ".") && name != "." && name != "..") {
			continue
		}
		isDir := item.IsDir()
		*entries = append(*entries, lsEntry{name: name, isDir: isDir, depth: depth + 1})
		if isDir {
			collectLsEntries(filepath.Join(dir, name), depth+1, ignore, entries, max)
		}
	}
}

// globMatch matches a glob pattern against a name, supporting `**`, `*`, `?`,
// and `[class]` (the subset glob::Pattern handles for ignore entries).
func globMatch(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return false
	}
	return globMatchAt(pattern, name)
}

func globMatchAt(pattern, name string) bool {
	// Iterative matcher with ** support.
	pi, ni := 0, 0
	starP, starN := -1, 0
	for ni < len(name) {
		switch {
		case pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == name[ni]):
			pi++
			ni++
		case pi < len(pattern) && pattern[pi] == '*':
			// `**` spans path separators; `*` does not — but for basename
			// matching both span everything.
			if pi+1 < len(pattern) && pattern[pi+1] == '*' {
				pi += 2
				// Skip a following separator.
				if pi < len(pattern) && pattern[pi] == '/' {
					pi++
				}
			} else {
				pi++
			}
			starP = pi
			starN = ni
		case starP >= 0:
			starN++
			ni = starN
			pi = starP
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		if pi+1 < len(pattern) && pattern[pi+1] == '*' {
			pi += 2
		} else {
			pi++
		}
	}
	return pi == len(pattern)
}
