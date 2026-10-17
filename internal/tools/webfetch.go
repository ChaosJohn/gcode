package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/toolcore"
)

// webfetch tool (ported from tool/webfetch.rs).

const (
	webFetchMaxSize        = 5 * 1024 * 1024
	webFetchMaxOutputChars = 40000
	webFetchMaxURLChars    = 300
	webFetchDefaultTimeout = 30
	webFetchMaxTimeout     = 120
)

// WebFetchTool fetches a URL and converts HTML to text or markdown.
type WebFetchTool struct{}

// NewWebFetchTool builds the webfetch tool.
func NewWebFetchTool() *WebFetchTool { return &WebFetchTool{} }

// Name implements Tool.
func (t *WebFetchTool) Name() string { return "webfetch" }

// Description implements Tool.
func (t *WebFetchTool) Description() string { return "Fetch a URL." }

// ParametersSchema implements Tool.
func (t *WebFetchTool) ParametersSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"required": ["url"],
		"properties": {
			"url": {"type": "string", "description": "URL."},
			"format": {
				"type": "string",
				"enum": ["text", "markdown", "html"],
				"description": "Output format."
			},
			"timeout": {"type": "integer", "description": "Timeout in seconds."}
		}
	}`)
}

type webFetchInput struct {
	URL     string  `json:"url"`
	Format  *string `json:"format"`
	Timeout *uint64 `json:"timeout"`
}

// Execute implements Tool.
func (t *WebFetchTool) Execute(input json.RawMessage, ctx toolcore.ToolContext) (toolcore.ToolOutput, error) {
	var params webFetchInput
	if err := parseInput(input, &params); err != nil {
		return toolcore.ToolOutput{}, err
	}
	if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
		return toolcore.ToolOutput{}, fmt.Errorf("URL must start with http:// or https://")
	}
	timeoutSec := uint64(webFetchDefaultTimeout)
	if params.Timeout != nil {
		timeoutSec = *params.Timeout
		if timeoutSec > webFetchMaxTimeout {
			timeoutSec = webFetchMaxTimeout
		}
	}
	format := "markdown"
	if params.Format != nil {
		format = *params.Format
	}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, params.URL, nil)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; JCode/1.0)")
	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return toolcore.ToolOutput{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return toolcore.ToolOutput{}, fmt.Errorf("HTTP error: %s", response.Status)
	}
	if response.ContentLength > webFetchMaxSize {
		return toolcore.ToolOutput{}, fmt.Errorf("Response too large: %d bytes (max %d bytes)",
			response.ContentLength, webFetchMaxSize)
	}
	contentType := response.Header.Get("Content-Type")

	raw, readErr := io.ReadAll(io.LimitReader(response.Body, webFetchMaxSize+1))
	truncated := false
	if len(raw) > webFetchMaxSize {
		raw = raw[:webFetchMaxSize]
		truncated = true
	}
	if readErr != nil && len(raw) == 0 {
		return toolcore.ToolOutput{}, readErr
	}
	body := strings.ToValidUTF8(string(raw), string(rune(0xFFFD)))
	if truncated {
		body += fmt.Sprintf("...\n\n(truncated, showing first %d bytes)", webFetchMaxSize)
	}

	var output string
	switch format {
	case "html":
		output = body
	case "text":
		output = htmlToText(body)
	default: // markdown
		if strings.Contains(contentType, "text/html") {
			output = htmlToMarkdown(body)
		} else {
			output = body
		}
	}
	fullLen := len(output)
	final, outputTruncated := truncateWebFetchOutput(output)
	note := ""
	if outputTruncated {
		note = fmt.Sprintf("\n\n(output truncated to %d of %d chars; fetch a more specific URL or anchor for the rest)",
			webFetchMaxOutputChars, fullLen)
	}
	return toolcore.NewToolOutput(fmt.Sprintf("Fetched %s (%d bytes)\n\n%s%s",
		params.URL, fullLen, final, note)), nil
}

// truncateWebFetchOutput cuts at a char boundary, preferring the last newline.
func truncateWebFetchOutput(output string) (string, bool) {
	if len(output) <= webFetchMaxOutputChars {
		return output, false
	}
	cut := webFetchMaxOutputChars
	for cut > 0 && !isCharBoundary(output, cut) {
		cut--
	}
	if idx := strings.LastIndex(output[:cut], "\n"); idx > webFetchMaxOutputChars/2 {
		cut = idx
	}
	return output[:cut], true
}

func isCharBoundary(s string, index int) bool {
	if index >= len(s) {
		return true
	}
	return s[index]&0xC0 != 0x80
}

var (
	reScript      = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	reStyle       = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	reTag         = regexp.MustCompile(`(?s)</?[A-Za-z!/][^\s/>]*(?:\s+[^\s=/>]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]*))?)*\s*/?>`)
	reWhitespace  = regexp.MustCompile(`\n\s*\n\s*\n`)
	reEmptyBullet = regexp.MustCompile(`(?m)^[ \t]*-[ \t]*$\n?`)
	reComment     = regexp.MustCompile(`(?s)<!--.*?-->`)
	reLink        = regexp.MustCompile(`(?i)<a[^>]*href=["']([^"']+)["'][^>]*>([^<]*)</a>`)
	reStrong      = regexp.MustCompile(`(?i)<(?:strong|b)>([^<]*)</(?:strong|b)>`)
	reEm          = regexp.MustCompile(`(?i)<(?:em|i)>([^<]*)</(?:em|i)>`)
	reCode        = regexp.MustCompile(`(?i)<code>([^<]*)</code>`)
	rePreCode     = regexp.MustCompile(`(?is)<pre[^>]*><code[^>]*>(.+?)</code></pre>`)
	reLi          = regexp.MustCompile(`(?i)<li[^>]*>`)
	reHeadingOpen = [6]*regexp.Regexp{
		regexp.MustCompile(`(?i)<h1[^>]*>`), regexp.MustCompile(`(?i)<h2[^>]*>`),
		regexp.MustCompile(`(?i)<h3[^>]*>`), regexp.MustCompile(`(?i)<h4[^>]*>`),
		regexp.MustCompile(`(?i)<h5[^>]*>`), regexp.MustCompile(`(?i)<h6[^>]*>`),
	}
	reHeadingClose = [6]*regexp.Regexp{
		regexp.MustCompile(`(?i)</h1>`), regexp.MustCompile(`(?i)</h2>`),
		regexp.MustCompile(`(?i)</h3>`), regexp.MustCompile(`(?i)</h4>`),
		regexp.MustCompile(`(?i)</h5>`), regexp.MustCompile(`(?i)</h6>`),
	}
)

// chromeTagRes covers elements whose spec definition excludes primary content
// (nav/aside/form/...). header/footer are deliberately excluded: they
// commonly wrap article-level content.
var chromeTagNames = []string{"nav", "aside", "form", "noscript", "svg", "iframe",
	"template", "select", "dialog", "canvas"}

var chromeTagRes = func() []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, tag := range chromeTagNames {
		out = append(out, regexp.MustCompile(`(?is)<`+tag+`\b[^>]*>.*?</`+tag+`\s*>`))
	}
	return out
}()

func stripChromeAndInert(html string) string {
	text := reScript.ReplaceAllString(html, "")
	text = reStyle.ReplaceAllString(text, "")
	text = reComment.ReplaceAllString(text, "")
	for _, re := range chromeTagRes {
		text = re.ReplaceAllString(text, "")
	}
	return text
}

func decodeBasicEntities(text string) string {
	replacer := strings.NewReplacer(
		"&nbsp;", " ", "&lt;", "<", "&gt;", ">", "&amp;", "&",
		"&quot;", `"`, "&#39;", "'")
	return replacer.Replace(text)
}

// htmlToText strips markup down to rendered text (ported html_to_text).
func htmlToText(html string) string {
	text := stripChromeAndInert(html)
	replacer := strings.NewReplacer(
		"<br>", "\n", "<br/>", "\n", "<br />", "\n",
		"</p>", "\n\n", "</div>", "\n", "</li>", "\n", "</tr>", "\n")
	text = replacer.Replace(text)
	text = reTag.ReplaceAllString(text, "")
	text = decodeBasicEntities(text)
	text = reWhitespace.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// renderLink drops links that cost more context than they convey.
func renderWebLink(href, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") ||
		len([]rune(href)) > webFetchMaxURLChars {
		return text
	}
	return fmt.Sprintf("[%s](%s)", text, href)
}

// htmlToMarkdown converts HTML to a compact markdown approximation
// (ported html_to_markdown).
func htmlToMarkdown(html string) string {
	md := stripChromeAndInert(html)
	for i := 0; i < 6; i++ {
		prefix := strings.Repeat("#", i+1)
		md = reHeadingOpen[i].ReplaceAllString(md, "\n"+prefix+" ")
		md = reHeadingClose[i].ReplaceAllString(md, "\n")
	}
	md = reLink.ReplaceAllStringFunc(md, func(match string) string {
		sub := reLink.FindStringSubmatch(match)
		if sub == nil {
			return match
		}
		return renderWebLink(sub[1], sub[2])
	})
	md = reStrong.ReplaceAllString(md, "**$1**")
	md = reEm.ReplaceAllString(md, "*$1*")
	md = reCode.ReplaceAllString(md, "`$1`")
	md = rePreCode.ReplaceAllString(md, "\n```\n$1\n```\n")
	md = reLi.ReplaceAllString(md, "\n- ")
	replacer := strings.NewReplacer(
		"<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n\n")
	md = replacer.Replace(md)
	md = reTag.ReplaceAllString(md, "")
	md = decodeBasicEntities(md)
	md = reEmptyBullet.ReplaceAllString(md, "")
	md = reWhitespace.ReplaceAllString(md, "\n\n")
	return strings.TrimSpace(md)
}
