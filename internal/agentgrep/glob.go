package agentgrep

import (
	"path"
	"strings"
)

// globPattern matches slash-separated relative paths with `**`, `*`, `?`,
// and `{a,b}` alternation (the subset the ignore crate's glob covers here).
type globPattern struct {
	segments []globSegment
	// allMatches mirrors `**/*`-style patterns that match everything.
	allMatches bool
}

type globSegment struct {
	literal  string // exact segment (no separators) when wildcard == false
	wildcard string // segment-level pattern like `*.rs`, or `**`
}

// CompileGlob compiles a workspace glob filter.
func CompileGlob(pattern string) (*globPattern, error) {
	pattern = strings.TrimPrefix(pattern, "./")
	if pattern == "" {
		return &globPattern{allMatches: true}, nil
	}
	switch pattern {
	case "*", "**", "**/*", "./*", "./**", "./**/*":
		return &globPattern{allMatches: true}, nil
	}
	compiled := &globPattern{}
	for _, seg := range strings.Split(pattern, "/") {
		switch {
		case seg == "**":
			compiled.segments = append(compiled.segments, globSegment{wildcard: "**"})
		case strings.ContainsAny(seg, "*?[{"):
			compiled.segments = append(compiled.segments, globSegment{wildcard: seg})
		default:
			compiled.segments = append(compiled.segments, globSegment{literal: seg})
		}
	}
	return compiled, nil
}

// compileGlob is the package-internal alias used by the walker.
func compileGlob(pattern string) (*globPattern, error) { return CompileGlob(pattern) }

// matchPath matches a root-relative path.
func (g *globPattern) matchPath(rel string) bool {
	if g.allMatches {
		return true
	}
	return matchSegments(g.segments, strings.Split(rel, "/"), 0, 0)
}

func matchSegments(segs []globSegment, parts []string, si, pi int) bool {
	if si == len(segs) {
		return pi == len(parts)
	}
	seg := segs[si]
	if seg.wildcard == "**" {
		// `**` matches zero or more path segments.
		for skip := pi; skip <= len(parts); skip++ {
			if matchSegments(segs, parts, si+1, skip) {
				return true
			}
		}
		return false
	}
	if pi >= len(parts) {
		return false
	}
	if seg.literal != "" {
		if seg.literal != parts[pi] {
			return false
		}
		return matchSegments(segs, parts, si+1, pi+1)
	}
	if !segmentGlobMatch(seg.wildcard, parts[pi]) {
		return false
	}
	return matchSegments(segs, parts, si+1, pi+1)
}

// segmentGlobMatch matches one path segment against `*`/`?`/literal mixes.
func segmentGlobMatch(pattern, name string) bool {
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == name
	}
	return globSegmentAt(pattern, name)
}

func globSegmentAt(pattern, name string) bool {
	pi, ni := 0, 0
	starP, starN := -1, 0
	for ni < len(name) {
		switch {
		case pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == name[ni]):
			pi++
			ni++
		case pi < len(pattern) && pattern[pi] == '*':
			pi++
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
		pi++
	}
	return pi == len(pattern)
}

// globMatchPath matches a full relative path against a gitignore-style
// pattern (used by the ignore rule evaluator).
func globMatchPath(pattern, rel string) bool {
	if pattern == rel {
		return true
	}
	// Basename-only patterns match at any depth (gitignore semantics).
	if !strings.Contains(pattern, "/") {
		return segmentGlobMatch(pattern, path.Base(rel))
	}
	if strings.HasPrefix(pattern, "/") {
		pattern = strings.TrimPrefix(pattern, "/")
		return matchGlobSegments(pattern, rel)
	}
	return matchGlobSegments(pattern, rel) || matchGlobSegments(pattern, path.Base(rel))
}

func matchGlobSegments(pattern, rel string) bool {
	compiled, err := CompileGlob(pattern)
	if err != nil {
		return false
	}
	return compiled.matchPath(rel)
}
