package agentgrep

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Grep engine (ported from search.rs): rg fast path, native walker fallback,
// structure grouping with dense-file short-circuits.

const (
	denseSkipStructureThreshold   = 24
	denseLimitedGroupingThreshold = 12
	denseGroupsLimit              = 8
	denseOtherSymbolsLimit        = 2
	otherSymbolsLimit             = 4
	maxMatchLineChars             = 240
	matchLinePrefixContextChars   = 80
)

// LineMatch is one matching line.
type LineMatch struct {
	LineNumber int    `json:"line_number"`
	LineText   string `json:"line_text"`
}

// MatchGroup groups match indices under one symbol or file scope.
type MatchGroup struct {
	Kind         string `json:"kind"`
	Label        string `json:"label"`
	StartLine    *int   `json:"start_line"`
	EndLine      *int   `json:"end_line"`
	MatchIndices []int  `json:"-"`
}

// ResolvedMatches returns the LineMatches in the group.
func (g MatchGroup) ResolvedMatches(matches []LineMatch) []LineMatch {
	out := make([]LineMatch, 0, len(g.MatchIndices))
	for _, idx := range g.MatchIndices {
		if idx < len(matches) {
			out = append(out, matches[idx])
		}
	}
	return out
}

// FileMatches is one file's grep result with structure grouping.
type FileMatches struct {
	Path                     string          `json:"path"`
	PathBytes                string          `json:"path_bytes,omitempty"`
	Language                 string          `json:"language"`
	Role                     string          `json:"role"`
	Matches                  []LineMatch     `json:"matches"`
	Groups                   []MatchGroup    `json:"groups"`
	TotalSymbols             int             `json:"total_symbols"`
	MatchedSymbolCount       int             `json:"matched_symbol_count"`
	OtherSymbols             []StructureItem `json:"other_symbols"`
	OtherSymbolsOmittedCount int             `json:"other_symbols_omitted_count"`
}

// GrepResult is the whole grep result.
type GrepResult struct {
	Query        string        `json:"query"`
	Regex        bool          `json:"regex"`
	Root         string        `json:"root"`
	Files        []FileMatches `json:"files"`
	TotalFiles   int           `json:"total_files"`
	TotalMatches int           `json:"total_matches"`
}

// GrepArgs carries the grep request (ported GrepArgs).
type GrepArgs struct {
	Query     string
	Regex     bool
	FileType  string
	JSON      bool
	PathsOnly bool
	Hidden    bool
	NoIgnore  bool
	NoFollow  bool
	Path      string
	Glob      string
}

// SearchScope drives the native walker.
type SearchScope struct {
	Root     string
	FileType string
	Glob     string
	Hidden   bool
	NoIgnore bool
	Follow   bool
}

// FileEntry is one walked file.
type FileEntry struct {
	Path         string
	RelativePath string
	RelativeRaw  []byte
}

// DisplayPath returns the lossy relative path (disambiguated for non-UTF-8).
func (f FileEntry) DisplayPath() string {
	return DisambiguateDisplayPath(f.RelativePath, f.RelativeRaw)
}

// DisambiguateDisplayPath appends hex tokens for invalid UTF-8 runs so every
// emitted display path stays unique and addressable.
func DisambiguateDisplayPath(lossy string, raw []byte) string {
	if raw == nil || utf8Valid(raw) {
		return lossy
	}
	suffix := "#b="
	for _, b := range raw {
		suffix += fmt.Sprintf("%02x", b)
	}
	return lossy + suffix
}

func utf8Valid(raw []byte) bool { return utf8.Valid(raw) }

// NormalizeFileType maps type aliases to extensions.
func NormalizeFileType(fileType string) string {
	switch fileType {
	case "rust":
		return "rs"
	case "javascript":
		return "js"
	case "typescript":
		return "ts"
	default:
		return strings.TrimLeft(fileType, ".")
	}
}

// ReadTextFile returns the file text; nil for binaries (NUL bytes).
func ReadTextFile(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if bytes.ContainsRune(raw, 0) {
		return "", false
	}
	if utf8.Valid(raw) {
		return string(raw), true
	}
	return strings.ToValidUTF8(string(raw), string(rune(0xFFFD))), true
}

// CollectFileEntries walks the scope with gitignore-style filtering and
// byte-sorted determinism (native fallback path).
func CollectFileEntries(scope SearchScope) []FileEntry {
	fileType := ""
	if scope.FileType != "" {
		fileType = NormalizeFileType(scope.FileType)
	}
	var glob *globPattern
	if scope.Glob != "" {
		if compiled, err := compileGlob(scope.Glob); err == nil {
			glob = compiled
		}
	}
	var entries []FileEntry
	root := scope.Root
	ignores := collectIgnoreRules(root, scope.NoIgnore)
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		name := d.Name()
		if !scope.Hidden && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if !scope.NoIgnore && defaultIgnores[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if !scope.NoIgnore && (defaultIgnores[name] || ignores.ignored(rel, false)) {
			return nil
		}
		if !scope.Follow && (d.Type()&os.ModeSymlink) != 0 {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		if fileType != "" && strings.TrimPrefix(filepath.Ext(name), ".") != fileType {
			return nil
		}
		if glob != nil && !glob.matchPath(rel) {
			return nil
		}
		entries = append(entries, FileEntry{Path: path, RelativePath: rel})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

var defaultIgnores = map[string]bool{
	".git": true, "node_modules": true, "target": true, "dist": true,
	"build": true, "__pycache__": true, ".venv": true, "venv": true,
}

// ignoreRules applies .gitignore/.rgignore/.ignore rules found under the
// root; a rule applies to files inside its own directory subtree.
type ignoreRules struct {
	rulesByDir map[string][]ignoreRule
}

type ignoreRule struct {
	pattern string
	negate  bool
	dirOnly bool
}

func collectIgnoreRules(root string, disabled bool) *ignoreRules {
	ir := &ignoreRules{rulesByDir: map[string][]ignoreRule{}}
	if disabled {
		return ir
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if name != ".gitignore" && name != ".rgignore" && name != ".ignore" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var rules []ignoreRule
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			rule := ignoreRule{pattern: line}
			if strings.HasPrefix(rule.pattern, "!") {
				rule.negate = true
				rule.pattern = rule.pattern[1:]
			}
			if strings.HasSuffix(rule.pattern, "/") {
				rule.dirOnly = true
				rule.pattern = strings.TrimSuffix(rule.pattern, "/")
			}
			rule.pattern = strings.TrimPrefix(rule.pattern, "./")
			if rule.pattern != "" {
				rules = append(rules, rule)
			}
		}
		if len(rules) > 0 {
			// Key by the directory relative to the search root so lookups from
			// relative paths line up.
			relDir, relErr := filepath.Rel(root, filepath.Dir(path))
			if relErr != nil {
				return nil
			}
			ir.rulesByDir[relDir] = rules
		}
		return nil
	})
	return ir
}

func (ir *ignoreRules) ignored(rel string, isDir bool) bool {
	ignored := false
	dir := filepath.Dir(rel)
	for {
		rules, ok := ir.rulesByDir[dir]
		if ok {
			for _, rule := range rules {
				matched := globMatchPath(rule.pattern, rel) ||
					globMatchPath(rule.pattern, filepath.Base(rel))
				if matched && rule.dirOnly && !isDir {
					matched = false
				}
				if matched {
					ignored = !rule.negate
				}
			}
		}
		if dir == "." || dir == "/" {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ignored
}

// matcher wraps literal or regex matching (ported Matcher).
type matcher struct {
	kind    int // 0 literal, 1 regex
	literal string
	re      *regexp.Regexp
}

func newMatcher(query string, regex bool) (*matcher, error) {
	if regex {
		compiled, err := regexp.Compile(query)
		if err != nil {
			return nil, fmt.Errorf(
				"invalid regex: %v\n\nhint: omit regex=true (or escape special characters) to search for this text literally",
				err)
		}
		return &matcher{kind: 1, re: compiled}, nil
	}
	return &matcher{kind: 0, literal: query}, nil
}

func (m *matcher) isMatch(line string) bool {
	if m.kind == 0 {
		return contains(line, m.literal)
	}
	return m.re.MatchString(line)
}

func (m *matcher) firstMatchSpan(line string) (int, int, bool) {
	if m.kind == 0 {
		if idx := strings.Index(line, m.literal); idx >= 0 {
			return idx, idx + len(m.literal), true
		}
		return 0, 0, false
	}
	if found := m.re.FindStringIndex(line); found != nil {
		return found[0], found[1], true
	}
	return 0, 0, false
}

// RunGrep shells to rg when available, else walks natively.
func RunGrep(root string, args GrepArgs) (GrepResult, error) {
	result, err := runGrepWithRg(root, args)
	if err == nil {
		return result, nil
	}
	return runGrepNative(root, args)
}

func runGrepNative(root string, args GrepArgs) (GrepResult, error) {
	m, err := newMatcher(args.Query, args.Regex)
	if err != nil {
		return GrepResult{}, err
	}
	entries := CollectFileEntries(SearchScope{
		Root: root, FileType: args.FileType, Glob: args.Glob,
		Hidden: args.Hidden, NoIgnore: args.NoIgnore, Follow: !args.NoFollow,
	})
	result := GrepResult{Query: args.Query, Regex: args.Regex, Root: root}
	for _, entry := range entries {
		matches := grepFileMatches(entry.Path, m)
		if len(matches) == 0 {
			continue
		}
		if file := processFileMatches(entry, matches); file != nil {
			result.TotalMatches += len(file.Matches)
			result.Files = append(result.Files, *file)
		}
	}
	result.TotalFiles = len(result.Files)
	return result, nil
}

func grepFileMatches(path string, m *matcher) []LineMatch {
	text, ok := ReadTextFile(path)
	if !ok {
		return nil
	}
	var matches []LineMatch
	for idx, line := range textLines(text) {
		if line == "" && idx == strings.Count(text, "\n") {
			break // trailing empty from the final newline
		}
		if m.isMatch(line) {
			matches = append(matches, LineMatch{LineNumber: idx + 1, LineText: line})
		}
	}
	return matches
}

func processFileMatches(entry FileEntry, matches []LineMatch) *FileMatches {
	if len(matches) >= denseSkipStructureThreshold {
		return buildDenseFileMatches(entry, matches)
	}
	text, ok := ReadTextFile(entry.Path)
	if !ok {
		return nil
	}
	structure := ExtractFileStructure(entry.Path, entry.RelativePath, text)
	grouping := groupMatches(structure.Items, matches)
	displayPath := entry.DisplayPath()
	pathBytes := ""
	if entry.RelativeRaw != nil && !utf8Valid(entry.RelativeRaw) {
		pathBytes = hex.EncodeToString(entry.RelativeRaw)
	}
	return &FileMatches{
		Path: displayPath, PathBytes: pathBytes,
		Language: structure.Language, Role: structure.Role,
		Matches: matches, Groups: grouping.groups,
		TotalSymbols:             len(structure.Items),
		MatchedSymbolCount:       grouping.matchedSymbolCount,
		OtherSymbols:             grouping.otherSymbols,
		OtherSymbolsOmittedCount: grouping.otherSymbolsOmitted,
	}
}

func buildDenseFileMatches(entry FileEntry, matches []LineMatch) *FileMatches {
	indices := make([]int, len(matches))
	for i := range matches {
		indices[i] = i
	}
	return &FileMatches{
		Path:     entry.DisplayPath(),
		Language: DetectLanguage(entry.Path),
		Role:     InferRole(entry.RelativePath),
		Matches:  matches,
		Groups:   []MatchGroup{fileScopeGroup(indices)},
	}
}

func fileScopeGroup(matchIndices []int) MatchGroup {
	return MatchGroup{Kind: "file-scope", Label: "<file scope>",
		MatchIndices: matchIndices}
}

type groupingResult struct {
	groups              []MatchGroup
	matchedSymbolCount  int
	otherSymbols        []StructureItem
	otherSymbolsOmitted int
}

// groupMatches assigns each match to its enclosing symbol, in file order,
// with a file-scope group first (ported group_matches).
func groupMatches(items []StructureItem, matches []LineMatch) groupingResult {
	maxGroups := -1 // unbounded
	otherLimit := otherSymbolsLimit
	if len(matches) >= denseLimitedGroupingThreshold {
		maxGroups = denseGroupsLimit
		otherLimit = denseOtherSymbolsLimit
	}

	var symbolGroups []MatchGroup
	var matchedIndices []int
	var fileScopeMatches []int
	itemIdx := 0
	lastGroupedItemIdx := -1

	for matchIdx, lineMatch := range matches {
		for itemIdx < len(items) && items[itemIdx].EndLine < lineMatch.LineNumber {
			itemIdx++
		}
		if itemIdx < len(items) && items[itemIdx].StartLine <= lineMatch.LineNumber &&
			lineMatch.LineNumber <= items[itemIdx].EndLine {
			if len(matchedIndices) == 0 || matchedIndices[len(matchedIndices)-1] != itemIdx {
				matchedIndices = append(matchedIndices, itemIdx)
				if maxGroups < 0 || len(symbolGroups) < maxGroups {
					start := items[itemIdx].StartLine
					end := items[itemIdx].EndLine
					symbolGroups = append(symbolGroups, MatchGroup{
						Kind: items[itemIdx].Kind, Label: items[itemIdx].Label,
						StartLine: &start, EndLine: &end,
						MatchIndices: []int{matchIdx},
					})
					lastGroupedItemIdx = itemIdx
				} else {
					fileScopeMatches = append(fileScopeMatches, matchIdx)
					lastGroupedItemIdx = -1
				}
			} else if lastGroupedItemIdx == itemIdx {
				group := &symbolGroups[len(symbolGroups)-1]
				group.MatchIndices = append(group.MatchIndices, matchIdx)
			} else {
				fileScopeMatches = append(fileScopeMatches, matchIdx)
			}
		} else {
			fileScopeMatches = append(fileScopeMatches, matchIdx)
		}
	}

	var groups []MatchGroup
	if len(fileScopeMatches) > 0 {
		groups = append(groups, fileScopeGroup(fileScopeMatches))
	}
	groups = append(groups, symbolGroups...)

	result := groupingResult{groups: groups, matchedSymbolCount: len(matchedIndices)}
	// Positional pass matching the upstream peek-iterator semantics.
	matchedPos := 0
	for idx := range items {
		if matchedPos < len(matchedIndices) && matchedIndices[matchedPos] == idx {
			matchedPos++
			continue
		}
		if len(result.otherSymbols) < otherLimit {
			result.otherSymbols = append(result.otherSymbols, items[idx])
		} else {
			result.otherSymbolsOmitted++
		}
	}
	return result
}

var _ = strconv.Itoa
