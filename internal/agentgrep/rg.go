package agentgrep

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// rg fast path (ported run_grep_with_rg): shells to ripgrep with --no-config
// so user config cannot skew results, then falls back to the native walker.

func rgAvailable() bool {
	_, err := exec.LookPath("rg")
	return err == nil
}

func runGrepWithRg(root string, args GrepArgs) (GrepResult, error) {
	if !rgAvailable() {
		return GrepResult{}, fmt.Errorf("rg not found")
	}
	var lines []string
	var err error
	if args.PathsOnly {
		lines, err = rgPathsOnly(root, args)
	} else {
		lines, err = rgMatchLines(root, args)
	}
	if err != nil {
		return GrepResult{}, err
	}

	if args.PathsOnly {
		files := make([]FileMatches, 0, len(lines))
		for _, display := range lines {
			files = append(files, FileMatches{Path: display})
		}
		return GrepResult{Query: args.Query, Regex: args.Regex, Root: root,
			TotalFiles: len(files), Files: files}, nil
	}

	// Group match lines by path (already sorted by rg's own traversal; we
	// re-sort deterministically at the end).
	m, err := newMatcher(args.Query, args.Regex)
	if err != nil {
		return GrepResult{}, err
	}
	type pathMatches struct {
		display string
		raw     string
		matches []LineMatch
	}
	var order []string
	byPath := map[string]*pathMatches{}
	for _, line := range lines {
		path, lineNum, text, ok := parseRgMatchLine(line)
		if !ok {
			continue
		}
		entry, exists := byPath[path]
		if !exists {
			entry = &pathMatches{display: path, raw: path}
			byPath[path] = entry
			order = append(order, path)
		}
		entry.matches = append(entry.matches, LineMatch{LineNumber: lineNum, LineText: text})
	}
	_ = m
	var files []FileMatches
	for _, key := range order {
		entry := byPath[key]
		fileEntry := FileEntry{Path: filepath.Join(root, entry.raw), RelativePath: entry.raw}
		if file := processFileMatches(fileEntry, entry.matches); file != nil {
			files = append(files, *file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	total := 0
	for _, file := range files {
		total += len(file.Matches)
	}
	return GrepResult{Query: args.Query, Regex: args.Regex, Root: root,
		Files: files, TotalFiles: len(files), TotalMatches: total}, nil
}

func buildRgCommand(root string, args GrepArgs, extra ...string) *exec.Cmd {
	command := exec.Command("rg")
	command.Dir = root
	command.Args = append(command.Args, "--no-config")
	if args.Regex {
		command.Args = append(command.Args, "-e", args.Query)
	} else {
		command.Args = append(command.Args, "--fixed-strings", "-e", args.Query)
	}
	if !args.NoFollow {
		command.Args = append(command.Args, "--follow")
	}
	command.Args = append(command.Args, "--no-messages")
	if args.Hidden {
		command.Args = append(command.Args, "--hidden")
	}
	if args.NoIgnore {
		command.Args = append(command.Args, "--no-ignore")
	}
	if args.Glob != "" {
		command.Args = append(command.Args, "-g", args.Glob)
	}
	if args.FileType != "" {
		ext := NormalizeFileType(args.FileType)
		// A custom type instead of a second -g: multiple -g globs are OR'd,
		// which would union the file-type filter with the glob instead of
		// intersecting.
		command.Args = append(command.Args, "--type-add", "agcustom:*."+ext, "-t", "agcustom")
	}
	command.Args = append(command.Args, extra...)
	command.Args = append(command.Args, ".")
	return command
}

func rgPathsOnly(root string, args GrepArgs) ([]string, error) {
	command := buildRgCommand(root, args, "--files-with-matches", "--null")
	stdout, exitCode, err := runRg(command)
	if err != nil {
		return nil, err
	}
	switch exitCode {
	case 0, 1:
	case 2:
		if len(stdout) == 0 {
			return nil, fmt.Errorf("rg failed")
		}
	default:
		return nil, fmt.Errorf("rg failed with exit code %d", exitCode)
	}
	var raws [][]byte
	for _, part := range splitNul(stdout) {
		if len(part) == 0 {
			continue
		}
		raws = append(raws, part)
	}
	sort.Slice(raws, func(i, j int) bool { return compareBytes(raws[i], raws[j]) < 0 })
	var paths []string
	for _, raw := range raws {
		paths = append(paths, normalizeRgPath(string(raw)))
	}
	return paths, nil
}

func rgMatchLines(root string, args GrepArgs) ([]string, error) {
	command := buildRgCommand(root, args, "--line-number", "--column",
		"--with-filename", "--color", "never", "--no-heading")
	stdout, exitCode, err := runRg(command)
	if err != nil {
		return nil, err
	}
	switch exitCode {
	case 0, 1:
	case 2:
		if len(stdout) == 0 {
			return nil, fmt.Errorf("rg failed")
		}
	default:
		return nil, fmt.Errorf("rg failed with exit code %d", exitCode)
	}
	var lines []string
	for _, line := range strings.Split(string(stdout), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func runRg(command *exec.Cmd) ([]byte, int, error) {
	output, err := command.Output()
	if err == nil {
		return output, 0, nil
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return nil, -1, fmt.Errorf("failed to execute rg: %v", err)
	}
	return output, exitErr.ExitCode(), nil
}

func splitNul(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == 0 {
			out = append(out, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

func compareBytes(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

// normalizeRgPath strips the "./" prefix rg emits for the "." search root.
func normalizeRgPath(raw string) string {
	return strings.TrimPrefix(raw, "./")
}

// parseRgMatchLine parses `path:line:column:text`, splitting on the last
// valid numeric triple so paths containing colons survive.
func parseRgMatchLine(line string) (string, int, string, bool) {
	// Expected shape: <path>:<line>:<col>:<text>
	first := strings.Index(line, ":")
	if first < 0 {
		return "", 0, "", false
	}
	rest := line[first+1:]
	second := strings.Index(rest, ":")
	if second < 0 {
		return "", 0, "", false
	}
	third := strings.Index(rest[second+1:], ":")
	if third < 0 {
		return "", 0, "", false
	}
	path := line[:first]
	lineNumStr := rest[:second]
	// The greedy left split can misfire when the path itself has colons; walk
	// forward until the two fields after the path are both numeric.
	for {
		num, err := strconv.Atoi(lineNumStr)
		if err == nil {
			colAndText := rest[second+1:]
			colIdx := strings.Index(colAndText, ":")
			if colIdx >= 0 {
				if _, err := strconv.Atoi(colAndText[:colIdx]); err == nil {
					text := colAndText[colIdx+1:]
					return normalizeRgPath(path), num, text, true
				}
			}
		}
		next := strings.Index(rest, ":")
		if next < 0 {
			return "", 0, "", false
		}
		path += ":" + rest[:next]
		rest = rest[next+1:]
		second = strings.Index(rest, ":")
		if second < 0 {
			return "", 0, "", false
		}
		lineNumStr = rest[:second]
	}
}

// GrepFileMatches is exported for tests.
func GrepFileMatches(path string, m *matcher) []LineMatch {
	return grepFileMatches(path, m)
}
