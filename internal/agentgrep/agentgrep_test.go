// Ported tests from agentgrep crate (structure.rs, find.rs, render.rs) and
// jcode tool/agentgrep_tests.rs.
package agentgrep

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported: extracts_rust_functions_and_structs.
func TestExtractsRustFunctionsAndStructs(t *testing.T) {
	text := "pub struct AuthStatus {}\n\npub fn auth_status() {}\nfn render_status_bar() {}\n"
	structure := ExtractFileStructure("src/auth/mod.rs", "src/auth/mod.rs", text)
	if structure.Language != "rust" || structure.Role != "auth" {
		t.Fatalf("language/role: %+v", structure)
	}
	for _, label := range []string{"AuthStatus", "auth_status", "render_status_bar"} {
		found := false
		for _, item := range structure.Items {
			if item.Label == label {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s: %+v", label, structure.Items)
		}
	}
}

// Ported: extracts_rust_pub_crate_async_and_impl_items.
func TestExtractsRustPubCrateAsyncImpl(t *testing.T) {
	text := "pub(crate) async fn render_status_bar() {}\nimpl<T> StatusView<T> {}\npub trait Renderable {}\n"
	structure := ExtractFileStructure("src/ui/view.rs", "src/ui/view.rs", text)
	for _, label := range []string{"render_status_bar", "StatusView<T>", "Renderable"} {
		found := false
		for _, item := range structure.Items {
			if item.Label == label {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s: %+v", label, structure.Items)
		}
	}
}

// Ported: extracts_markdown_headings_without_regex.
func TestExtractsMarkdownHeadings(t *testing.T) {
	structure := ExtractFileStructure("docs/test.md", "docs/test.md", "# Title\ntext\n## Subtitle\n")
	if structure.Language != "markdown" {
		t.Fatalf("language: %+v", structure)
	}
	labels := []string{}
	for _, item := range structure.Items {
		labels = append(labels, item.Label)
	}
	if !contains(strings.Join(labels, ","), "Title") || !contains(strings.Join(labels, ","), "Subtitle") {
		t.Fatalf("headings: %v", labels)
	}
}

// Ported: finalize ranges — a function spans to the next item or EOF.
func TestFinalizeRanges(t *testing.T) {
	text := "fn a() {\n}\nfn b() {\n}\n"
	structure := ExtractFileStructure("x.rs", "x.rs", text)
	if structure.Items[0].StartLine != 1 || structure.Items[0].EndLine != 2 {
		t.Fatalf("item 0 range: %+v", structure.Items[0])
	}
	if structure.Items[1].StartLine != 3 || structure.Items[1].EndLine != 4 {
		t.Fatalf("item 1 range: %+v", structure.Items[1])
	}
	if structure.Items[0].LineCount != 2 {
		t.Fatalf("line count: %+v", structure.Items[0])
	}
}

// Ported: ranked_find_prefers_matching_paths_and_symbols.
func TestRankedFindPrefersMatchingPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src/auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src/tui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src/auth/mod.rs"),
		[]byte("pub struct AuthStatus {}\npub fn auth_status() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src/tui/app.rs"),
		[]byte("fn render_status_bar() {}\nfn draw_header() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := RunFind(dir, FindArgs{
		QueryParts: []string{"auth", "status"}, FileType: "rs",
		MaxFiles: 5, NoIgnore: true,
	})
	if len(result.Files) == 0 {
		t.Fatalf("no files")
	}
	if result.Files[0].Path != filepath.Join("src", "auth", "mod.rs") {
		t.Fatalf("top file: %s", result.Files[0].Path)
	}
}

// Ported: find without any evidence scores nothing.
func TestFindEmptyQueryNoScopeRejectedByTool(t *testing.T) {
	// Engine level: an empty query scores nothing.
	result := RunFind(t.TempDir(), FindArgs{})
	if len(result.Files) != 0 {
		t.Fatalf("expected no files: %v", result.Files)
	}
}

// Renderers: compaction, non-code cap, max_regions (ported render tests).
func TestRenderGrepOutputBounded(t *testing.T) {
	huge := strings.Repeat("x", 80) + strings.Repeat("y", 300)
	compact := CompactRenderedMatchLine(huge, "yyy", false)
	if len([]rune(compact)) > 320 || !strings.Contains(compact, "[truncated") {
		t.Fatalf("compaction: %q", compact[:60])
	}
	short := "fn small() {}"
	if CompactRenderedMatchLine(short, "", false) != short {
		t.Fatalf("short line must pass through")
	}

	// max_regions limits rendered excerpts but the header reports the total.
	result := GrepResult{Query: "status_notice", Regex: false, Root: ".",
		TotalFiles: 1, TotalMatches: 3}
	matches := []LineMatch{
		{LineNumber: 1, LineText: "fn one() { status_notice(); }"},
		{LineNumber: 2, LineText: "fn two() { status_notice(); }"},
		{LineNumber: 3, LineText: "fn three() { status_notice(); }"},
	}
	indices := []int{0, 1, 2}
	start, end := 1, 3
	result.Files = []FileMatches{{
		Path: "src/app.rs", Language: "rust", Role: "implementation",
		Matches: matches,
		Groups: []MatchGroup{{Kind: "function", Label: "one", StartLine: &start,
			EndLine: &end, MatchIndices: indices}},
		TotalSymbols: 1, MatchedSymbolCount: 1,
	}}
	out := RenderGrepOutput(result, GrepArgs{Query: "status_notice"}, 2)
	if !contains(out, "matches: 3 in 1 files") || !contains(out, "more matches omitted (max_regions=2)") {
		t.Fatalf("max regions render: %s", out)
	}

	// Non-code files cap at 3 match lines per file.
	result.Files[0].Language = "text"
	result.Files[0].Groups = []MatchGroup{fileScopeGroup(indices)}
	out = RenderGrepOutput(result, GrepArgs{Query: "status_notice"}, 0)
	if strings.Count(out, "- @ ") != 3 {
		t.Fatalf("non-code cap: %s", out)
	}
}

// Ported: grep grouping under a file-scope symbol with dense short-circuit.
func TestGroupingAndDenseSkip(t *testing.T) {
	items := []StructureItem{
		{Kind: "function", Label: "one", StartLine: 1, EndLine: 2, LineCount: 2},
	}
	matches := []LineMatch{{LineNumber: 1, LineText: "x"}, {LineNumber: 2, LineText: "y"},
		{LineNumber: 5, LineText: "z"}}
	grouping := groupMatches(items, matches)
	if grouping.matchedSymbolCount != 1 || len(grouping.groups) != 2 {
		t.Fatalf("grouping: %+v", grouping)
	}
	if grouping.groups[0].Kind != "file-scope" || len(grouping.groups[0].MatchIndices) != 1 {
		t.Fatalf("file scope group: %+v", grouping.groups[0])
	}
	if grouping.groups[1].Kind != "function" || len(grouping.groups[1].MatchIndices) != 2 {
		t.Fatalf("symbol group: %+v", grouping.groups[1])
	}

	// >=24 matches skips structure extraction entirely.
	dense := processFileMatches(FileEntry{Path: "dense.go", RelativePath: "dense.go"},
		make([]LineMatch, 24))
	if dense.TotalSymbols != 0 || len(dense.Groups) != 1 ||
		dense.Groups[0].Kind != "file-scope" {
		t.Fatalf("dense: %+v", dense)
	}
}

// Ported: glob set semantics.
func TestGlobCompileAndMatch(t *testing.T) {
	g, err := CompileGlob("**/*.rs")
	if err != nil {
		t.Fatal(err)
	}
	if !g.matchPath("src/main.rs") || g.matchPath("src/main.go") {
		t.Fatalf("double star glob")
	}
	g, _ = CompileGlob("*")
	if !g.allMatches {
		t.Fatalf("match-all glob")
	}
	g, _ = CompileGlob("src/*.rs")
	if !g.matchPath("src/main.rs") || g.matchPath("src/deep/main.rs") {
		t.Fatalf("single star must not cross separators")
	}
}

// The native walker honors gitignore and default ignores.
func TestWalkerIgnores(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target", "blob.rs"), []byte("fn x(){}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.rs"), []byte("fn y(){}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("skipme.rs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skipme.rs"), []byte("fn z(){}"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := CollectFileEntries(SearchScope{Root: dir, Follow: true})
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.RelativePath)
	}
	joined := strings.Join(names, ",")
	if contains(joined, "target") || contains(joined, "skipme.rs") {
		t.Fatalf("ignored entries leaked: %s", joined)
	}
	if !contains(joined, "keep.rs") {
		t.Fatalf("missing keep.rs: %s", joined)
	}
}

// The rg fast path and the native fallback agree on match content.
func TestRgFastPathParityWithNative(t *testing.T) {
	if !rgAvailable() {
		t.Skip("rg not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.rs"),
		[]byte("fn alpha() {}\nfn beta() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := GrepArgs{Query: "alpha", Path: dir}
	rgResult, err := runGrepWithRg(dir, args)
	if err != nil {
		t.Fatal(err)
	}
	nativeResult, err := runGrepNative(dir, args)
	if err != nil {
		t.Fatal(err)
	}
	if rgResult.TotalMatches != 1 || nativeResult.TotalMatches != 1 {
		t.Fatalf("rg=%d native=%d", rgResult.TotalMatches, nativeResult.TotalMatches)
	}
	if rgResult.Files[0].Matches[0].LineNumber != 1 ||
		nativeResult.Files[0].Matches[0].LineNumber != 1 {
		t.Fatalf("line numbers differ")
	}
}

// Literal queries starting with '-' must not be parsed as rg flags.
func TestQueryStartingWithDash(t *testing.T) {
	if !rgAvailable() {
		t.Skip("rg not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flags.txt"),
		[]byte("--features fast\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunGrep(dir, GrepArgs{Query: "--features"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalMatches != 1 {
		t.Fatalf("dashed query: %+v", result)
	}
}

// Invalid regex reports the upstream hint.
func TestInvalidRegexHint(t *testing.T) {
	_, err := RunGrep(t.TempDir(), GrepArgs{Query: "([", Regex: true})
	if err == nil || !contains(err.Error(), "omit regex=true") {
		t.Fatalf("regex hint: %v", err)
	}
}

// Outline renders structure for one file.
func TestOutline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lib.rs")
	if err := os.WriteFile(path, []byte("fn one() {}\nfn two() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunOutline(dir, OutlineArgs{File: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Structure.Items) != 2 || result.TotalLines != 2 {
		t.Fatalf("outline: %+v", result)
	}
	out := RenderOutlineOutput(result)
	if !contains(out, "  - function one @ 1-1 (1 lines)") {
		t.Fatalf("render: %s", out)
	}
}
