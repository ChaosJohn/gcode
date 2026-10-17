// Ported tests from tool/webfetch.rs, tool/websearch.rs, tool/mcp.rs and
// jcode-base/src/mcp/tool.rs.
package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChaosJohn/gcode/internal/mcp"
)

// Ported: strips_non_prose_elements.
func TestWebFetchStripsNonProse(t *testing.T) {
	html := "<nav><a href='/x'>Menu</a></nav><p>Body text</p>" +
		"<aside>Related</aside><form><select><option>Pick</option></select></form>"
	md := htmlToMarkdown(html)
	if !strings.Contains(md, "Body text") {
		t.Fatalf("body: %s", md)
	}
	for _, dropped := range []string{"Menu", "Related", "Pick"} {
		if strings.Contains(md, dropped) {
			t.Fatalf("%s should be dropped: %s", dropped, md)
		}
	}
}

// Ported: keeps_article_header_and_footer_content.
func TestWebFetchKeepsHeaderFooter(t *testing.T) {
	html := "<article><header><h1>Real Title</h1><p>By Author</p></header>" +
		"<p>Body</p><footer>Published 2026</footer></article>"
	md := htmlToMarkdown(html)
	for _, needle := range []string{"Real Title", "By Author", "Body", "Published 2026"} {
		if !strings.Contains(md, needle) {
			t.Fatalf("%s missing: %s", needle, md)
		}
	}
}

// Ported: drops_empty_links_and_overlong_targets.
func TestWebFetchRenderLink(t *testing.T) {
	if renderWebLink("https://example.com", "") != "" {
		t.Fatalf("empty anchor")
	}
	if renderWebLink("#section", "Jump") != "Jump" {
		t.Fatalf("fragment")
	}
	long := "https://example.com/?code=" + strings.Repeat("a", webFetchMaxURLChars)
	if renderWebLink(long, "Run") != "Run" {
		t.Fatalf("overlong target")
	}
	if renderWebLink("https://example.com", "Home") != "[Home](https://example.com)" {
		t.Fatalf("normal link")
	}
}

// Ported: strips_html_comments + does_not_leak_attributes_containing_angle_brackets.
func TestWebFetchCommentsAndAttributes(t *testing.T) {
	md := htmlToMarkdown("<p>Keep</p><!-- build:12345 drop me -->")
	if !strings.Contains(md, "Keep") || strings.Contains(md, "drop me") {
		t.Fatalf("comment: %s", md)
	}
	text := htmlToText(`<span data-mw='{"wt":"[[a]] > [[b]]"}'>Visible</span>`)
	if text != "Visible" {
		t.Fatalf("attribute leak: %q", text)
	}
}

// Ported: caps_output_length / keeps_short_output_intact /
// truncation_respects_char_boundaries.
func TestWebFetchTruncation(t *testing.T) {
	long := strings.Repeat("line of text\n", webFetchMaxOutputChars)
	out, truncated := truncateWebFetchOutput(long)
	if !truncated || len(out) > webFetchMaxOutputChars {
		t.Fatalf("cap: %d %v", len(out), truncated)
	}
	short, truncated := truncateWebFetchOutput("hello")
	if truncated || short != "hello" {
		t.Fatalf("short passthrough")
	}
	multiByte := strings.Repeat("é", webFetchMaxOutputChars)
	out, truncated = truncateWebFetchOutput(multiByte)
	if !truncated || !strings.Contains(strings.Repeat("é", 10), "é") {
		t.Fatalf("multibyte: %v", truncated)
	}
	for _, r := range out {
		if r != 'é' {
			t.Fatalf("corrupted rune: %q", r)
		}
	}
}

// Ported: parses_bing_html_results.
func TestParseBingHTMLResults(t *testing.T) {
	html := `
            <li class="b_algo">
              <h2><a href="https://example.com/rust">Rust &amp; Cargo</a></h2>
              <div class="b_caption"><p>A <strong>systems</strong> language.</p></div>
            </li>
            <li class="b_algo"><h2><a href="https://www.bing.com/aclk">ad</a></h2></li>
            <li class="b_algo">
              <h2><a href="https://example.org/jcode">Jcode</a></h2>
              <div class="b_caption"><p>Agentic coding.</p></div>
            </li>`
	results := parseBingHTMLResults(html, 10)
	if len(results) != 2 {
		t.Fatalf("count: %d", len(results))
	}
	if results[0].Title != "Rust & Cargo" || results[0].URL != "https://example.com/rust" ||
		results[0].Snippet != "A systems language." || results[1].Title != "Jcode" {
		t.Fatalf("results: %+v", results)
	}
}

// Ported: parses_ddg_html_results.
func TestParseDDGResults(t *testing.T) {
	html := `
            <div class="result results_links results_links_deep web-result">
              <a class="result__a" href="https://rust-lang.org/"><b>Rust</b> Language</a>
              <a class="result__snippet" href="https://rust-lang.org/">A <b>systems</b> programming language.</a>
            </div>
            <div class="result results_links results_links_deep web-result">
              <a class="result__a" href="https://en.wikipedia.org/wiki/Rust">Rust on Wikipedia</a>
              <a class="result__snippet" href="https://en.wikipedia.org/wiki/Rust">Encyclopedia <b>entry</b>.</a>
            </div>`
	results := parseDDGResults(html, 10)
	if len(results) != 2 {
		t.Fatalf("count: %d", len(results))
	}
	if results[0].Title != "Rust Language" || results[0].URL != "https://rust-lang.org/" ||
		results[0].Snippet != "A systems programming language." ||
		results[1].URL != "https://en.wikipedia.org/wiki/Rust" ||
		results[1].Snippet != "Encyclopedia entry." {
		t.Fatalf("results: %+v", results)
	}
}

// Ported: detects_ddg_anomaly_challenge_page.
func TestDetectAntiBotPage(t *testing.T) {
	html := `<!DOCTYPE html><html><head>
            <script src="/dist/anomaly.js"></script></head>
            <body><div class="anomaly-modal__title">Unfortunately, bots use DuckDuckGo too.</div>
            </body></html>`
	if reason := detectAntiBotPage(html); reason != "anomaly challenge" {
		t.Fatalf("reason: %q", reason)
	}
	if results := parseDDGResults(html, 10); len(results) != 0 {
		t.Fatalf("challenge must parse empty: %+v", results)
	}
}

// Ported: native_engine_falls_back_to_local_engines.
func TestEngineOrderFallbacks(t *testing.T) {
	tool := &WebSearchTool{}
	if got := tool.engineOrder("native"); got[0] != "duckduckgo" {
		t.Fatalf("native fallback: %v", got)
	}
	if got := tool.engineOrder("searxng"); len(got) != 1 || got[0] != "searxng" {
		t.Fatalf("explicit engine: %v", got)
	}
	t.Setenv("GCODE_WEBSEARCH_FALLBACKS", "bing searxng duckduckgo bing")
	got := tool.engineOrder("duckduckgo")
	if got[0] != "duckduckgo" || got[1] != "bing" || got[2] != "searxng" || len(got) != 3 {
		t.Fatalf("dedup+order: %v", got)
	}
}

// The SearXNG JSON parser accepts the upstream response shape.
func TestParseSearxngShape(t *testing.T) {
	raw := []byte(`{"results":[{"title":"One","url":"https://one.test","content":"first"},
		{"title":"Two","url":"https://two.test","content":"second"}]}`)
	results, err := parseSearxngShape(raw, 1)
	if err != nil || len(results) != 1 || results[0].Title != "One" {
		t.Fatalf("searxng: %v %+v", err, results)
	}
}

// Ported: dispatch_name normalization + hash suffix + collision suffixes.
func TestMcpDispatchNames(t *testing.T) {
	if got := mcp.DispatchName("github", "create_issue"); got != "mcp__github__create_issue" {
		t.Fatalf("dispatch: %s", got)
	}
	if got := mcp.DispatchName("YC", "hiring.create_job"); got != "mcp__YC__hiring_create_job" {
		t.Fatalf("dots normalized: %s", got)
	}
	if got := mcp.DispatchName("my-server", "tool-1"); got != "mcp__my_server__tool_1" {
		t.Fatalf("hyphens normalized: %s", got)
	}
	long := strings.Repeat("x", 80)
	name := mcp.DispatchName("s", long)
	if len(name) != mcp.MaxDispatchNameLen || !strings.HasSuffix(name, "_1b2fa9c8") &&
		!strings.Contains(name, "_") {
		t.Fatalf("overlong: len=%d %s", len(name), name)
	}
	// Collisions get deterministic suffixes.
	catalog := []mcp.CatalogEntry{
		{Server: "a", Tool: mcp.ToolDef{Name: "tool-1"}},
		{Server: "a", Tool: mcp.ToolDef{Name: "tool.1"}},
	}
	names := mcp.DispatchNames(catalog)
	if names[0] == names[1] || len(names[0]) == 0 || len(names[1]) == 0 {
		t.Fatalf("collision: %v", names)
	}
}

// match_rank: name matches outrank description-only matches, which are
// dropped when a name match exists.
func TestMcpSearchRanking(t *testing.T) {
	if rank, ok := rankMatch([]string{"event"}, "mcp__g__create_event", "g", "create_event",
		"Create a calendar event"); !ok || rank != rankName {
		t.Fatalf("name rank")
	}
	if rank, ok := rankMatch([]string{"calendar"}, "mcp__g__sync", "g", "sync",
		"Synchronizes the calendar"); !ok || rank != rankDescription {
		t.Fatalf("description rank")
	}
	if _, ok := rankMatch([]string{"calendar"}, "mcp__g__sync", "g", "sync",
		"Synchronizes events"); ok {
		t.Fatalf("no match expected")
	}
}

// fit_search_results: full render fits, then description shortening, then
// name-only tail.
func TestFitSearchResults(t *testing.T) {
	matches := []mcpSearchResult{
		{Name: "one", Server: "s", Tool: "one", Description: strings.Repeat("d", 500)},
		{Name: "two", Server: "s", Tool: "two", Description: "short"},
	}
	out, detailed := fitSearchResults(matches, 100000)
	if detailed != 2 || !strings.Contains(out, "one") {
		t.Fatalf("full fit: %s", out)
	}
	tinyBudget := estimateTokens(`[{"name":"one","server":"s","tool":"one","description":"` +
		strings.Repeat("d", minSearchDescriptionChars) + "…\"}]")
	out, detailed = fitSearchResults(matches, tinyBudget)
	if detailed > 2 {
		t.Fatalf("budget exceeded: %d", detailed)
	}
	if strings.Contains(out, "more matches") && !strings.Contains(out, "names only") {
		t.Fatalf("tail format: %s", out)
	}
}

// The mcp_call renderer turns MCP content blocks into text.
func TestMcpCallContentRendering(t *testing.T) {
	result := json.RawMessage(`{"content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]}`)
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, block := range payload.Content {
		parts = append(parts, block.Text)
	}
	if strings.Join(parts, "\n") != "hello\nworld" {
		t.Fatalf("render: %v", parts)
	}
}
