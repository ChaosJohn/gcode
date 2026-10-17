package agentgrep

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// find.rs + outline.rs ports.

// FindArgs carries the find request.
type FindArgs struct {
	QueryParts []string
	FileType   string
	JSON       bool
	PathsOnly  bool
	DebugScore bool
	MaxFiles   int
	Hidden     bool
	NoIgnore   bool
	NoFollow   bool
	Path       string
	Glob       string
}

// FindFile is one scored file result.
type FindFile struct {
	Path      string        `json:"path"`
	PathBytes string        `json:"path_bytes,omitempty"`
	Role      string        `json:"role"`
	Language  string        `json:"language"`
	Score     int           `json:"score"`
	Why       []string      `json:"why"`
	Structure FindStructure `json:"structure"`
}

// FindStructure shows the first items plus an omitted count.
type FindStructure struct {
	Items        []StructureItem `json:"items"`
	OmittedCount int             `json:"omitted_count"`
}

// FindResult is the whole find result.
type FindResult struct {
	Query string     `json:"query"`
	Root  string     `json:"root"`
	Files []FindFile `json:"files"`
}

// RunFind ranks files by path/symbol/text evidence.
func RunFind(root string, args FindArgs) FindResult {
	query := strings.Join(args.QueryParts, " ")
	queryLower := strings.ToLower(query)
	queryTokens := tokenizeQuery(query)

	scope := SearchScope{Root: root, FileType: args.FileType, Glob: args.Glob,
		Hidden: args.Hidden, NoIgnore: args.NoIgnore, Follow: !args.NoFollow}

	var files []FindFile
	for _, entry := range CollectFileEntries(scope) {
		relativeLower := strings.ToLower(entry.RelativePath)
		if !hasPathEvidence(queryLower, queryTokens, relativeLower) {
			continue
		}
		text, ok := ReadTextFile(entry.Path)
		if !ok {
			continue
		}
		structure := ExtractFileStructure(entry.Path, entry.RelativePath, text)
		score, why := scoreFile(queryLower, queryTokens, relativeLower, &structure, text)
		if score <= 0 {
			continue
		}
		shown := structure.Items
		omitted := 0
		if len(shown) > 8 {
			shown = shown[:8]
			omitted = len(structure.Items) - 8
		}
		files = append(files, FindFile{
			Path: entry.DisplayPath(), Role: structure.Role,
			Language: structure.Language, Score: score, Why: why,
			Structure: FindStructure{Items: shown, OmittedCount: omitted},
		})
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Score != files[j].Score {
			return files[i].Score > files[j].Score
		}
		return files[i].Path < files[j].Path
	})
	if args.MaxFiles > 0 && len(files) > args.MaxFiles {
		files = files[:args.MaxFiles]
	}
	return FindResult{Query: query, Root: root, Files: files}
}

func hasPathEvidence(queryLower string, queryTokens []string, relativeLower string) bool {
	if contains(relativeLower, queryLower) {
		return true
	}
	for _, token := range queryTokens {
		if contains(relativeLower, token) {
			return true
		}
	}
	return false
}

func scoreFile(queryLower string, queryTokens []string, relativeLower string,
	structure *FileStructure, text string) (int, []string) {
	score := 0
	var why []string
	evidenceHits := 0

	if contains(relativeLower, queryLower) {
		score += 120
		why = append(why, "path contains full query")
		evidenceHits++
	}

	matchedTokens := 0
	for _, token := range queryTokens {
		if contains(relativeLower, token) {
			matchedTokens++
		}
	}
	if matchedTokens > 0 {
		score += matchedTokens * 25
		why = append(why, fmt.Sprintf("path token matches: %d", matchedTokens))
		evidenceHits += matchedTokens
	}

	structureHits := 0
	for _, item := range structure.Items {
		labelLower := strings.ToLower(item.Label)
		for _, token := range queryTokens {
			if contains(labelLower, token) {
				structureHits++
				break
			}
		}
	}
	hasEvidence := evidenceHits > 0
	if structureHits > 0 && hasEvidence {
		capped := structureHits
		if capped > 4 {
			capped = 4
		}
		score += capped * 8
		why = append(why, fmt.Sprintf("symbol/outline hits: %d", structureHits))
		evidenceHits += capped
	}

	textLower := strings.ToLower(text)
	textHits := 0
	for _, token := range queryTokens {
		if contains(textLower, token) {
			textHits++
		}
	}
	if textHits > 0 && hasEvidence {
		score += textHits * 4
		why = append(why, fmt.Sprintf("supporting text hits: %d", textHits))
	}

	for _, token := range queryTokens {
		if contains(structure.Role, token) {
			score += 20
			why = append(why, "role matched: "+structure.Role)
			break
		}
	}

	switch structure.Role {
	case "implementation", "auth", "provider", "ui", "handler":
		if evidenceHits > 0 {
			score += 20
			why = append(why, "code role boost: "+structure.Role)
		}
	case "docs":
		score -= 25
		why = append(why, "docs penalty")
	case "test":
		score -= 15
		why = append(why, "test penalty")
	}

	if evidenceHits == 0 {
		return 0, nil
	}
	return score, why
}

func tokenizeQuery(query string) []string {
	var tokens []string
	current := strings.Builder{}
	for _, ch := range query {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' {
			current.WriteRune(unicode.ToLower(ch))
		} else if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// OutlineArgs carries the outline request.
type OutlineArgs struct {
	File     string
	JSON     bool
	MaxItems int
	Path     string
}

// OutlineResult is one file's structure outline.
type OutlineResult struct {
	Root           string           `json:"root"`
	Path           string           `json:"path"`
	Language       string           `json:"language"`
	Role           string           `json:"role"`
	TotalLines     int              `json:"total_lines"`
	Structure      OutlineStructure `json:"structure"`
	ContextApplied string           `json:"context_applied,omitempty"`
}

// OutlineStructure shows the items plus an omitted count.
type OutlineStructure struct {
	Items        []StructureItem `json:"items"`
	OmittedCount int             `json:"omitted_count"`
}

// RunOutline extracts the structure of one file.
func RunOutline(root string, args OutlineArgs) (OutlineResult, error) {
	filePath := args.File
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(root, filePath)
	}
	if info, err := os.Stat(filePath); err != nil || info.IsDir() {
		return OutlineResult{}, fmt.Errorf("file not found: %s", args.File)
	}
	text, ok := ReadTextFile(filePath)
	if !ok {
		return OutlineResult{}, fmt.Errorf("file is binary or unreadable: %s", filePath)
	}
	displayPath, err := filepath.Rel(root, filePath)
	if err != nil {
		displayPath = filePath
	}
	structure := ExtractFileStructure(filePath, displayPath, text)
	items := structure.Items
	omitted := 0
	if args.MaxItems > 0 && len(items) > args.MaxItems {
		omitted = len(items) - args.MaxItems
		items = items[:args.MaxItems]
	}
	totalLines := strings.Count(text, "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		totalLines++
	}
	return OutlineResult{Root: root, Path: displayPath, Language: structure.Language,
		Role: structure.Role, TotalLines: totalLines,
		Structure: OutlineStructure{Items: items, OmittedCount: omitted}}, nil
}
