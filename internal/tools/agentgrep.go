package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/agentgrep"
	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// agentgrep tool (ported from tool/agentgrep.rs + agentgrep/args.rs).

// AgentGrepForegroundBudget mirrors the upstream 5s foreground budget.
const AgentGrepForegroundBudget = 5 * time.Second

// DefaultGrepMaxRegions bounds rendered grep matches by default.
const DefaultGrepMaxRegions = 200

// AgentGrepTool is the structure-aware search tool.
type AgentGrepTool struct{}

// NewAgentGrepTool builds the agentgrep tool.
func NewAgentGrepTool() *AgentGrepTool { return &AgentGrepTool{} }

// Name implements Tool.
func (t *AgentGrepTool) Name() string { return "agentgrep" }

// Description implements Tool.
func (t *AgentGrepTool) Description() string {
	return "Search code and file names. Defaults to grep mode when mode is omitted."
}

// ParametersSchema implements Tool.
func (t *AgentGrepTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"mode": {
				"type": "string",
				"enum": ["grep", "find", "outline", "trace"],
				"description": "Mode: grep (default), find (file names), outline (one file), trace (relationship DSL)."
			},
			"query": {
				"type": "string",
				"description": "Search query. Required for grep (literal unless regex=true); optional ranking terms for find."
			},
			"file": {
				"type": "string",
				"description": "Single file to inspect. Required for outline."
			},
			"terms": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Trace DSL terms, e.g. [\"subject:auth_status\", \"relation:rendered\"]. Not for grep/find; use query."
			},
			"regex": {
				"type": "boolean",
				"description": "In grep mode, treat query as a regex. Defaults to false (literal)."
			},
			"path": {
				"type": "string",
				"description": "Directory or file to search, relative to the workspace. Omit to search the whole workspace."
			},
			"glob": {
				"type": "string",
				"description": "Optional file glob filter such as **/*.rs. Omit to search everything."
			},
			"type": {
				"type": "string",
				"description": "Optional ripgrep file type filter, such as rs, py, js, ts, or md."
			},
			"max_files": {
				"type": "integer",
				"description": "Maximum number of files to return for find/trace-style modes."
			},
			"max_regions": {
				"type": "integer",
				"description": "Maximum number of matching regions to return."
			},
			"paths_only": {
				"type": "boolean",
				"description": "Return only matching paths instead of match excerpts where supported."
			}
		}
	}`)
}

// AgentGrepInput is the decoded tool input.
type AgentGrepInput struct {
	Mode       string   `json:"mode"`
	Query      *string  `json:"query"`
	File       *string  `json:"file"`
	Terms      []string `json:"terms"`
	Regex      *bool    `json:"regex"`
	Path       *string  `json:"path"`
	Glob       *string  `json:"glob"`
	FileType   *string  `json:"type"`
	MaxFiles   *int     `json:"max_files"`
	MaxRegions *int     `json:"max_regions"`
	PathsOnly  *bool    `json:"paths_only"`
}

// resolvedSearchScope ports resolved_search_scope: `file` scopes the search
// to one exact file (root=parent, glob=basename); a directory `path` becomes
// the root.
func resolvedSearchScope(ctx toolcore.ToolContext, path, file, glob *string) (string, string) {
	effective := path
	if effective == nil {
		effective = file
	}
	if effective == nil || *effective == "" {
		return "", normalizedAgentGrepGlob(glob)
	}
	resolved := ctx.ResolvePath(*effective)
	if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
		return filepath.Dir(resolved), filepath.Base(resolved)
	}
	return resolved, normalizedAgentGrepGlob(glob)
}

// normalizedAgentGrepGlob drops empty and match-all globs.
func normalizedAgentGrepGlob(glob *string) string {
	if glob == nil {
		return ""
	}
	trimmed := trimSpace(*glob)
	if trimmed == "" {
		return ""
	}
	switch trimmed {
	case "*", "**", "**/*", "./*", "./**", "./**/*":
		return ""
	default:
		return trimmed
	}
}

// buildGrepArgs ports build_grep_args.
func buildGrepArgs(params *AgentGrepInput, ctx toolcore.ToolContext) (agentgrep.GrepArgs, error) {
	if params.Query == nil {
		return agentgrep.GrepArgs{}, fmt.Errorf("agentgrep grep requires 'query'")
	}
	root, glob := resolvedSearchScope(ctx, params.Path, params.File, params.Glob)
	fileType := ""
	if params.FileType != nil {
		fileType = *params.FileType
	}
	return agentgrep.GrepArgs{
		Query:     *params.Query,
		Regex:     derefBool(params.Regex),
		FileType:  fileType,
		PathsOnly: derefBool(params.PathsOnly),
		Path:      root, Glob: glob,
	}, nil
}

// buildFindArgs ports build_find_args, including the unscoped-empty-query
// rejection.
func buildFindArgs(params *AgentGrepInput, ctx toolcore.ToolContext) (agentgrep.FindArgs, error) {
	query := derefString(params.Query)
	if trimSpace(query) == "" &&
		(params.Path == nil || *params.Path == "") &&
		(params.File == nil || *params.File == "") &&
		normalizedAgentGrepGlob(params.Glob) == "" &&
		(params.FileType == nil || *params.FileType == "") {
		return agentgrep.FindArgs{}, fmt.Errorf(
			"agentgrep find requires 'query' unless path, glob, or type narrows the search")
	}
	root, glob := resolvedSearchScope(ctx, params.Path, params.File, params.Glob)
	maxFiles := 10
	if params.MaxFiles != nil {
		maxFiles = *params.MaxFiles
	}
	fileType := ""
	if params.FileType != nil {
		fileType = *params.FileType
	}
	return agentgrep.FindArgs{
		QueryParts: strings.Fields(query),
		FileType:   fileType,
		PathsOnly:  derefBool(params.PathsOnly),
		MaxFiles:   maxFiles,
		Path:       root, Glob: glob,
	}, nil
}

// buildOutlineArgs ports build_outline_args: a file-valued `path` is the
// outline target; `file` is otherwise required (legacy query/first term).
func buildOutlineArgs(params *AgentGrepInput, ctx toolcore.ToolContext) (agentgrep.OutlineArgs, error) {
	if params.Path != nil && *params.Path != "" {
		resolved := ctx.ResolvePath(*params.Path)
		if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
			return agentgrep.OutlineArgs{File: resolved}, nil
		}
	}
	file := ""
	switch {
	case params.File != nil:
		file = *params.File
	case params.Query != nil:
		file = *params.Query
	case len(params.Terms) > 0:
		file = params.Terms[0]
	default:
		return agentgrep.OutlineArgs{}, fmt.Errorf(
			"agentgrep outline requires 'file' (or legacy 'query' / first term)")
	}
	return agentgrep.OutlineArgs{File: ctx.ResolvePath(file)}, nil
}

// Execute implements Tool.
func (t *AgentGrepTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params AgentGrepInput
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	if params.Mode == "" {
		params.Mode = "grep"
	}
	if ctx.WorkingDir == "" {
		explicit := params.Path != nil && *params.Path != "" || params.File != nil && *params.File != ""
		if !explicit || (params.Path != nil && filepath.IsAbs(*params.Path)) ||
			(params.File != nil && filepath.IsAbs(*params.File)) {
			if !explicit {
				return toolcore.ToolOutput{}, fmt.Errorf(
					"agentgrep requires a session working directory unless an absolute path is provided")
			}
		}
	}

	started := time.Now()
	output, err := t.executeMode(&params, ctx)
	elapsed := time.Since(started)
	if err != nil {
		detail := truncateRunes(trimSpace(err.Error()), 600)
		return toolcore.ToolOutput{}, fmt.Errorf("agentgrep %s failed after %dms: %s",
			params.Mode, elapsed.Milliseconds(), detail)
	}
	return output, nil
}

func (t *AgentGrepTool) executeMode(params *AgentGrepInput, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	exactFile := exactSearchFilePath(ctx, params.Path)
	switch params.Mode {
	case "grep":
		args, err := buildGrepArgs(params, ctx)
		if err != nil {
			return toolcore.ToolOutput{}, err
		}
		result, err := agentgrep.RunGrep(resolveSearchRoot(ctx, args.Path), args)
		if err != nil {
			return toolcore.ToolOutput{}, err
		}
		result = filterGrepResultToExactFile(result, exactFile)
		maxRegions := DefaultGrepMaxRegions
		if params.MaxRegions != nil {
			maxRegions = *params.MaxRegions
		}
		return toolcore.NewToolOutput(
			agentgrep.RenderGrepOutput(result, args, maxRegions)).WithTitle("agentgrep grep"), nil
	case "find":
		args, err := buildFindArgs(params, ctx)
		if err != nil {
			return toolcore.ToolOutput{}, err
		}
		result := agentgrep.RunFind(resolveSearchRoot(ctx, args.Path), args)
		result = filterFindResultToExactFile(result, exactFile)
		return toolcore.NewToolOutput(agentgrep.RenderFindOutput(result, args)).
			WithTitle("agentgrep find"), nil
	case "outline":
		args, err := buildOutlineArgs(params, ctx)
		if err != nil {
			return toolcore.ToolOutput{}, err
		}
		result, err := agentgrep.RunOutline(resolveSearchRoot(ctx, args.Path), args)
		if err != nil {
			return toolcore.ToolOutput{}, err
		}
		return toolcore.NewToolOutput(agentgrep.RenderOutlineOutput(result)).
			WithTitle("agentgrep outline"), nil
	case "trace", "smart":
		terms := params.Terms
		if len(terms) == 0 && params.Mode == "smart" && params.Query != nil {
			terms = strings.Fields(*params.Query)
		}
		if len(terms) == 0 {
			fieldHint := "non-empty 'terms'"
			if params.Mode == "smart" {
				fieldHint = "non-empty 'terms' or 'query'"
			}
			return toolcore.ToolOutput{}, fmt.Errorf("agentgrep %s requires %s", params.Mode, fieldHint)
		}
		return toolcore.ToolOutput{}, fmt.Errorf(
			"agentgrep trace is not available in this build yet. Use grep, find, or outline.")
	default:
		return toolcore.ToolOutput{}, fmt.Errorf(
			"Unsupported agentgrep mode: %s. Use grep, find, outline, or trace.", params.Mode)
	}
}

func resolveSearchRoot(ctx toolcore.ToolContext, path string) string {
	if path != "" {
		return path
	}
	return ctx.WorkingDir
}

func exactSearchFilePath(ctx toolcore.ToolContext, path *string) string {
	if path == nil || *path == "" {
		return ""
	}
	resolved := ctx.ResolvePath(*path)
	if info, err := os.Stat(resolved); err != nil || info.IsDir() {
		return ""
	}
	return filepath.Base(resolved)
}

func filterGrepResultToExactFile(result agentgrep.GrepResult, exactFile string) agentgrep.GrepResult {
	if exactFile == "" {
		return result
	}
	var kept []agentgrep.FileMatches
	for _, file := range result.Files {
		if file.Path == exactFile {
			kept = append(kept, file)
		}
	}
	result.Files = kept
	result.TotalFiles = len(kept)
	total := 0
	for _, file := range kept {
		total += len(file.Matches)
	}
	result.TotalMatches = total
	return result
}

func filterFindResultToExactFile(result agentgrep.FindResult, exactFile string) agentgrep.FindResult {
	if exactFile == "" {
		return result
	}
	var kept []agentgrep.FindFile
	for _, file := range result.Files {
		if file.Path == exactFile {
			kept = append(kept, file)
		}
	}
	result.Files = kept
	return result
}

func derefBool(value *bool) bool { return value != nil && *value }
