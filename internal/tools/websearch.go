package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// websearch tool (ported from tool/websearch.rs): local engines with
// fallback ordering and anti-bot detection.

// WebSearchTool searches the web via DuckDuckGo/Bing/SearXNG.
type WebSearchTool struct {
	// SearxngURL overrides the config lookup (injected for tests).
	SearxngURL string
}

// NewWebSearchTool builds the websearch tool.
func NewWebSearchTool() *WebSearchTool { return &WebSearchTool{} }

// Name implements Tool.
func (t *WebSearchTool) Name() string { return "websearch" }

// Description implements Tool.
func (t *WebSearchTool) Description() string { return "Search the web." }

// ParametersSchema implements Tool.
func (t *WebSearchTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["query"],
		"properties": {
			"query": {"type": "string", "description": "Search query."},
			"num_results": {"type": "integer", "description": "Max results."},
			"engine": {
				"type": "string",
				"enum": ["duckduckgo", "bing", "searxng", "custom"],
				"description": "Engine. Defaults to configured engine; custom uses websearch.custom."
			},
			"bing_market": {
				"type": "string",
				"description": "Optional Bing market, e.g. en-US or zh-CN. Defaults to GCODE_BING_MARKET or en-US."
			}
		}
	}`)
}

// SearchResult is one ranked hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type webSearchInput struct {
	Query      string  `json:"query"`
	NumResults *int    `json:"num_results"`
	Engine     *string `json:"engine"`
	BingMarket *string `json:"bing_market"`
}

var searchHTTPClient = &http.Client{Timeout: 30 * time.Second}

func (t *WebSearchTool) engineOrder(preferred string) []string {
	engines := []string{preferred}
	for _, fallback := range strings.Fields(os.Getenv("GCODE_WEBSEARCH_FALLBACKS")) {
		engines = append(engines, fallback)
	}
	if engines[0] == "" || engines[0] == "native" {
		engines[0] = "duckduckgo"
	}
	// `native` is provider-side and never runs locally: local fallbacks run
	// instead, defaulting to DuckDuckGo then Bing.
	var local []string
	seen := map[string]bool{}
	for _, engine := range engines {
		switch engine {
		case "duckduckgo", "bing", "searxng", "custom":
			if !seen[engine] {
				local = append(local, engine)
				seen[engine] = true
			}
		}
	}
	if len(local) == 0 {
		return []string{"duckduckgo", "bing"}
	}
	return local
}

// Execute implements Tool.
func (t *WebSearchTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params webSearchInput
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	numResults := 8
	if params.NumResults != nil {
		numResults = *params.NumResults
		if numResults > 20 {
			numResults = 20
		}
	}
	preferred := "duckduckgo"
	if params.Engine != nil {
		preferred = *params.Engine
	}
	market := strings.TrimSpace(os.Getenv("GCODE_BING_MARKET"))
	if market == "" {
		market = "en-US"
	}
	if params.BingMarket != nil {
		market = *params.BingMarket
	}

	var lastError error
	var results []SearchResult
	for _, engine := range t.engineOrder(preferred) {
		found, err := t.searchWithEngine(engine, params.Query, numResults, market)
		if err != nil {
			lastError = err
			continue
		}
		if len(found) > 0 {
			results = found
			break
		}
	}
	if len(results) == 0 && lastError != nil {
		return toolcore.ToolOutput{}, lastError
	}
	if len(results) == 0 {
		return toolcore.NewToolOutput(fmt.Sprintf(
			"No results found for: %s\n\n"+
				"If results are consistently empty on this machine, the default "+
				"DuckDuckGo/Bing engines may be blocked here by TLS fingerprinting "+
				"or IP reputation (common on Linux/servers). Workaround:\n"+
				"- Point at a SearXNG instance: set `websearch.searxng_url` (or "+
				"GCODE_SEARXNG_URL) and use engine \"searxng\".", params.Query)), nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Search results for: %s\n\n", params.Query)
	for i, result := range results {
		fmt.Fprintf(&out, "%d. **%s**\n   %s\n   %s\n\n", i+1, result.Title, result.URL, result.Snippet)
	}
	return toolcore.NewToolOutput(out.String()), nil
}

func (t *WebSearchTool) searchWithEngine(engine, query string, numResults int, market string) ([]SearchResult, error) {
	switch engine {
	case "duckduckgo":
		return searchDuckDuckGo(query, numResults)
	case "bing":
		return searchBing(query, numResults, market)
	case "searxng":
		return t.searchSearxng(query, numResults)
	case "custom":
		return searchCustom(query, numResults)
	default:
		return nil, fmt.Errorf("unsupported websearch engine: %s", engine)
	}
}

// searchDuckDuckGo submits the query as a POST form the same way the real
// HTML page does (a plain GET now serves an anti-bot challenge).
func searchDuckDuckGo(query string, numResults int) ([]SearchResult, error) {
	form := url.Values{"q": {query}, "kl": {"us-en"}}
	request, err := http.NewRequest(http.MethodPost, "https://html.duckduckgo.com/html/",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := searchHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	body := string(raw)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("Search failed with status: %s", response.Status)
	}
	results := parseDDGResults(body, numResults)
	if len(results) == 0 {
		if reason := detectAntiBotPage(body); reason != "" {
			return nil, fmt.Errorf("DuckDuckGo served an anti-bot challenge page (%s) instead of results. This is commonly caused by TLS fingerprinting or IP reputation on Linux. Falling back to another engine if configured.", reason)
		}
	}
	return results, nil
}

// searchBing prefers the configured API key, else scrapes HTML results.
func searchBing(query string, numResults int, market string) ([]SearchResult, error) {
	if apiKey := strings.TrimSpace(os.Getenv("GCODE_BING_API_KEY")); apiKey != "" {
		return searchBingAPI(query, numResults, market, apiKey)
	}
	return searchBingHTML(query, numResults, market)
}

func searchBingAPI(query string, numResults int, market, apiKey string) ([]SearchResult, error) {
	endpoint := fmt.Sprintf("https://api.bing.microsoft.com/v7.0/search?q=%s&count=%d&mkt=%s",
		url.QueryEscape(query), numResults, url.QueryEscape(market))
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Ocp-Apim-Subscription-Key", apiKey)
	response, err := searchHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var payload struct {
		WebPages struct {
			Value []struct {
				Name    string `json:"name"`
				URL     string `json:"url"`
				Snippet string `json:"snippet"`
			} `json:"value"`
		} `json:"webPages"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	var results []SearchResult
	for _, page := range payload.WebPages.Value {
		if len(results) >= numResults {
			break
		}
		results = append(results, SearchResult{Title: page.Name, URL: page.URL, Snippet: page.Snippet})
	}
	return results, nil
}

var (
	reDDGResultLink    = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	reDDGResultSnippet = regexp.MustCompile(`(?s)<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)
	reSearchTag        = regexp.MustCompile(`<[^>]+>`)
	reBingBlock        = regexp.MustCompile(`(?s)<li[^>]*class="[^"]*\bb_algo\b[^"]*"[^>]*>(.*?)</li>`)
	reBingLink         = regexp.MustCompile(`(?s)<h2[^>]*>\s*<a[^>]*href="([^"]+)"[^>]*>(.*?)</a>\s*</h2>`)
	reBingCaption      = regexp.MustCompile(`(?s)<div[^>]*class="[^"]*\bb_caption\b[^"]*"[^>]*>.*?<p[^>]*>(.*?)</p>`)
)

func searchBingHTML(query string, numResults int, market string) ([]SearchResult, error) {
	endpoint := fmt.Sprintf("https://www.bing.com/search?q=%s&mkt=%s",
		url.QueryEscape(query), url.QueryEscape(market))
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	response, err := searchHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return parseBingHTMLResults(string(raw), numResults), nil
}

func searchSearxng(base, query string, numResults int) ([]SearchResult, error) {
	endpoint := strings.TrimRight(base, "/") + "/search"
	request, err := http.NewRequest(http.MethodGet,
		endpoint+"?q="+url.QueryEscape(query)+"&format=json", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")
	request.Header.Set("Accept", "application/json")
	response, err := searchHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	var results []SearchResult
	for _, hit := range payload.Results {
		if len(results) >= numResults {
			break
		}
		results = append(results, SearchResult{Title: hit.Title, URL: hit.URL, Snippet: hit.Content})
	}
	return results, nil
}

func (t *WebSearchTool) searchSearxng(query string, numResults int) ([]SearchResult, error) {
	base := t.SearxngURL
	if base == "" {
		base = strings.TrimSpace(os.Getenv("GCODE_SEARXNG_URL"))
	}
	if base == "" {
		return nil, fmt.Errorf("SearXNG engine selected but no instance URL configured. Set `websearch.searxng_url` in your config or the GCODE_SEARXNG_URL environment variable to a SearXNG base URL (e.g. https://searx.example.org).")
	}
	return searchSearxng(base, query, numResults)
}

// searchCustom runs the GCODE_WEBSEARCH_CUSTOM_URL template (?query= and
// ?count= placeholders) expecting SearXNG-shaped JSON.
func searchCustom(query string, numResults int) ([]SearchResult, error) {
	template := strings.TrimSpace(os.Getenv("GCODE_WEBSEARCH_CUSTOM_URL"))
	if template == "" {
		return nil, fmt.Errorf("custom websearch engine selected but websearch.custom is not configured (GCODE_WEBSEARCH_CUSTOM_URL)")
	}
	endpoint := strings.ReplaceAll(template, "{query}", url.QueryEscape(query))
	endpoint = strings.ReplaceAll(endpoint, "{count}", fmt.Sprintf("%d", numResults))
	response, err := searchHTTPClient.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return parseSearxngShape(raw, numResults)
}

func parseSearxngShape(raw []byte, numResults int) ([]SearchResult, error) {
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	var results []SearchResult
	for _, hit := range payload.Results {
		if len(results) >= numResults {
			break
		}
		results = append(results, SearchResult{Title: hit.Title, URL: hit.URL, Snippet: hit.Content})
	}
	return results, nil
}

func parseDDGResults(html string, maxResults int) []SearchResult {
	links := reDDGResultLink.FindAllStringSubmatch(html, -1)
	snippets := reDDGResultSnippet.FindAllStringSubmatch(html, -1)
	var results []SearchResult
	for i, link := range links {
		if len(results) >= maxResults {
			break
		}
		linkURL := decodeDDGURL(link[1])
		title := htmlDecodeEntities(reSearchTag.ReplaceAllString(link[2], ""))
		if !strings.HasPrefix(linkURL, "http") || strings.Contains(linkURL, "duckduckgo.com") {
			continue
		}
		snippet := ""
		if i < len(snippets) {
			snippet = htmlDecodeEntities(reSearchTag.ReplaceAllString(snippets[i][1], ""))
		}
		results = append(results, SearchResult{Title: title, URL: linkURL, Snippet: snippet})
	}
	return results
}

func parseBingHTMLResults(html string, maxResults int) []SearchResult {
	var results []SearchResult
	for _, block := range reBingBlock.FindAllStringSubmatch(html, -1) {
		if len(results) >= maxResults {
			break
		}
		link := reBingLink.FindStringSubmatch(block[1])
		if link == nil {
			continue
		}
		linkURL := htmlDecodeEntities(link[1])
		if !strings.HasPrefix(linkURL, "http") || strings.Contains(linkURL, "bing.com") {
			continue
		}
		title := htmlDecodeEntities(reSearchTag.ReplaceAllString(link[2], ""))
		snippet := ""
		if caption := reBingCaption.FindStringSubmatch(block[1]); caption != nil {
			snippet = htmlDecodeEntities(reSearchTag.ReplaceAllString(caption[1], ""))
		}
		results = append(results, SearchResult{Title: title, URL: linkURL, Snippet: snippet})
	}
	return results
}

// detectAntiBotPage reports why a body looks like a challenge page; these
// arrive with HTTP 200 so a zero-result parse alone is ambiguous.
func detectAntiBotPage(html string) string {
	lowered := strings.ToLower(html)
	markers := []struct{ needle, reason string }{
		{"anomaly-modal", "anomaly challenge"},
		{"anomaly.js", "anomaly challenge"},
		{"dpn=1", "anomaly challenge"},
		{"captcha", "captcha"},
		{"g-recaptcha", "recaptcha"},
		{"are you a robot", "bot check"},
		{"unusual traffic", "bot check"},
		{"verify you are human", "human verification"},
		{"challenge-platform", "cloudflare challenge"},
		{"cf-challenge", "cloudflare challenge"},
	}
	for _, marker := range markers {
		if strings.Contains(lowered, marker.needle) {
			return marker.reason
		}
	}
	return ""
}

// decodeDDGURL unwraps //duckduckgo.com/l/?uddg=ACTUAL_URL&... forms.
func decodeDDGURL(linkURL string) string {
	idx := strings.Index(linkURL, "uddg=")
	if idx < 0 {
		return linkURL
	}
	start := idx + 5
	end := strings.Index(linkURL[start:], "&")
	if end >= 0 {
		end += start
	} else {
		end = len(linkURL)
	}
	encoded := linkURL[start:end]
	if decoded, err := url.QueryUnescape(encoded); err == nil {
		return decoded
	}
	return encoded
}

var entityReplacer = strings.NewReplacer(
	"&nbsp;", " ", "&lt;", "<", "&gt;", ">", "&amp;", "&",
	"&quot;", `"`, "&#39;", "'", "&#x27;", "'", "&apos;", "'")

func htmlDecodeEntities(s string) string {
	return strings.TrimSpace(entityReplacer.Replace(s))
}
