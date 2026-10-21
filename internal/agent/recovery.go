// Text-wrapped tool-call recovery (ported from response_recovery.rs +
// tool_markup_recovery.rs, issue #1702): OpenAI-compatible and DeepSeek
// endpoints occasionally print their tool-call envelope as assistant text —
// either `to=functions.NAME {json}` or an XML/DSML `invoke`/`parameter`
// envelope. Only runs when the response carried no structured tool calls, so
// structured arguments are never inspected or rewritten.
package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// MaxUnparsedToolMarkupNudges caps the re-issue reminder before ending the
// turn (upstream MAX_UNPARSED_TOOL_MARKUP_NUDGES).
const MaxUnparsedToolMarkupNudges = 2

// MarkupToolCall is one tool call recovered from text.
type MarkupToolCall struct {
	Name      string
	Arguments json.RawMessage
}

// TextRecovery is the outcome of scanning assistant text.
type TextRecovery struct {
	Status    string // "none" | "recovered" | "unparseable"
	Calls     []MarkupToolCall
	Sanitized string
	Reason    string
}

var (
	// tagRE matches tool-markup tags with an optional mangling prefix:
	// `｜DSML｜`, `|DSML|`, `DSML ` or `antml:`.
	tagRE = regexp.MustCompile(`(?i)<\s*(/)?\s*(?:[｜|]\s*)?(?:DSML\s*(?:[｜|]\s*)?)?(?:antml:)?(function_calls|tool_calls|calls|invoke|parameter)\b([^<>]*)>`)
	// attrRE matches quoted attributes inside a tag.
	attrRE = regexp.MustCompile(`([A-Za-z_][\w-]*)\s*=\s*"([^"]*)"`)
)

type tagKind int

const (
	tagWrapper tagKind = iota
	tagInvoke
	tagParameter
)

type markupTag struct {
	start, end int
	closing    bool
	kind       tagKind
	attrs      string
}

// fencedCodeRanges returns byte ranges inside ``` or ~~~ fences; markup
// inside them is documentation, not calls.
func fencedCodeRanges(text string) [][2]int {
	var ranges [][2]int
	var open *[2]int
	var openFence string
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		var fence string
		switch {
		case strings.HasPrefix(trimmed, "```"):
			fence = "```"
		case strings.HasPrefix(trimmed, "~~~"):
			fence = "~~~"
		}
		switch {
		case open == nil && fence != "":
			open = &[2]int{offset, 0}
			openFence = fence
		case open != nil && fence == openFence:
			ranges = append(ranges, [2]int{open[0], offset + len(line)})
			open = nil
		}
		offset += len(line)
	}
	if open != nil {
		ranges = append(ranges, [2]int{open[0], len(text)})
	}
	return ranges
}

func collectTags(text string) []markupTag {
	fences := fencedCodeRanges(text)
	var tags []markupTag
	for _, match := range tagRE.FindAllStringSubmatchIndex(text, -1) {
		start := match[0]
		insideFence := false
		for _, r := range fences {
			if start >= r[0] && start < r[1] {
				insideFence = true
				break
			}
		}
		if insideFence {
			continue
		}
		// `<invoke>` quoted inline with backticks is prose.
		if strings.HasSuffix(text[:start], "`") {
			continue
		}
		closing := match[2] >= 0
		name := strings.ToLower(text[match[4]:match[5]])
		attrs := ""
		if match[6] >= 0 {
			attrs = text[match[6]:match[7]]
		}
		kind := tagWrapper
		switch name {
		case "invoke":
			kind = tagInvoke
		case "parameter":
			kind = tagParameter
		}
		tags = append(tags, markupTag{start: start, end: match[1],
			closing: closing, kind: kind, attrs: attrs})
	}
	return tags
}

func markupAttr(attrs, key string) (string, bool) {
	for _, match := range attrRE.FindAllStringSubmatch(attrs, -1) {
		if strings.EqualFold(match[1], key) {
			return match[2], true
		}
	}
	return "", false
}

// parameterValue converts a raw parameter body into JSON: `string="true"`
// forces a string; without the hint, anything that parses as JSON keeps its
// type (parsed strings fall back to the raw bytes unless string="false").
func parameterValue(raw, attrs string) json.RawMessage {
	hint, _ := markupAttr(attrs, "string")
	hint = strings.ToLower(hint)
	if hint == "true" {
		encoded, _ := json.Marshal(raw)
		return encoded
	}
	trimmed := strings.TrimSpace(raw)
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil && trimmed != "" {
		if _, isString := parsed.(string); isString && hint != "false" {
			encoded, _ := json.Marshal(raw)
			return encoded
		}
		return json.RawMessage(trimmed)
	}
	encoded, _ := json.Marshal(raw)
	return encoded
}

// ScanToolCallMarkup ports scan_tool_call_markup.
func ScanToolCallMarkup(text string) TextRecovery {
	tags := collectTags(text)
	relevant := 0
	for _, tag := range tags {
		if tag.kind != tagWrapper {
			relevant++
		}
	}
	// A single stray tag is far more likely to be prose than a call.
	if relevant < 2 {
		return TextRecovery{Status: "none"}
	}

	var calls []MarkupToolCall
	problem := ""
	note := func(reason string) {
		if problem == "" {
			problem = reason
		}
	}
	var currentName string
	haveCurrent := false
	currentArgs := map[string]json.RawMessage{}
	for i := 0; i < len(tags); i++ {
		tag := tags[i]
		switch {
		case tag.kind == tagWrapper:
			// Envelope wrapper: ignored.
		case tag.kind == tagInvoke && !tag.closing:
			if haveCurrent {
				note("nested or unterminated invoke")
			}
			name, _ := markupAttr(tag.attrs, "name")
			currentName = strings.TrimSpace(name)
			haveCurrent = true
			currentArgs = map[string]json.RawMessage{}
		case tag.kind == tagInvoke && tag.closing:
			if !haveCurrent {
				note("closing invoke without a matching opening invoke")
			} else if currentName == "" {
				note("invoke without a tool name")
			} else {
				arguments, _ := json.Marshal(currentArgs)
				calls = append(calls, MarkupToolCall{Name: currentName,
					Arguments: arguments})
			}
			haveCurrent = false
			currentName = ""
		case tag.kind == tagParameter && !tag.closing:
			closeIdx := -1
			for j := i + 1; j < len(tags); j++ {
				if tags[j].kind == tagParameter && tags[j].closing {
					closeIdx = j
					break
				}
			}
			if closeIdx < 0 {
				note("unterminated parameter")
				i = len(tags)
				break
			}
			body := text[tag.end:tags[closeIdx].start]
			name, _ := markupAttr(tag.attrs, "name")
			name = strings.TrimSpace(name)
			switch {
			case haveCurrent && name != "":
				currentArgs[name] = parameterValue(body, tag.attrs)
			case haveCurrent:
				note("parameter without a name")
			default:
				note("parameter outside of an invoke block")
			}
			i = closeIdx
		case tag.kind == tagParameter && tag.closing:
			note("closing parameter without an opening")
		}
	}
	if haveCurrent {
		note("unterminated invoke")
	}
	if problem != "" {
		return TextRecovery{Status: "unparseable", Reason: problem}
	}
	if len(calls) == 0 {
		return TextRecovery{Status: "unparseable", Reason: "no complete invoke block"}
	}

	first := text
	last := len(text)
	if len(tags) > 0 {
		first = text[:tags[0].start]
		last = tags[len(tags)-1].end
	}
	prefix := strings.TrimRight(first, " \t\n\r")
	suffix := strings.TrimSpace(text[last:])
	sanitized := prefix
	if suffix != "" {
		if sanitized != "" {
			sanitized += "\n"
		}
		sanitized += suffix
	}
	return TextRecovery{Status: "recovered", Calls: calls, Sanitized: sanitized}
}

// parseTextWrappedToolCall ports parse_text_wrapped_tool_call: the
// `to=functions.NAME {json}` marker form.
func parseTextWrappedToolCall(text string) (prefix, name string, arguments json.RawMessage, suffix string, ok bool) {
	const marker = "to=functions."
	markerIdx := strings.Index(text, marker)
	if markerIdx < 0 {
		return "", "", nil, "", false
	}
	afterMarker := text[markerIdx+len(marker):]
	nameEnd := 0
	for idx, ch := range afterMarker {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_' {
			nameEnd = idx + 1
		} else {
			break
		}
	}
	if nameEnd == 0 {
		return "", "", nil, "", false
	}
	name = afterMarker[:nameEnd]
	remaining := afterMarker[nameEnd:]
	var fallback []byte
	for braceIdx := 0; braceIdx < len(remaining); braceIdx++ {
		if remaining[braceIdx] != '{' {
			continue
		}
		slice := remaining[braceIdx:]
		decoder := json.NewDecoder(strings.NewReader(slice))
		var parsed any
		if err := decoder.Decode(&parsed); err != nil {
			continue
		}
		if _, isObject := parsed.(map[string]any); !isObject {
			continue
		}
		consumed := decoder.InputOffset()
		if consumed <= 0 {
			continue
		}
		candidate := json.RawMessage(strings.TrimRight(slice[:consumed], " \t\n\r"))
		newPrefix := strings.TrimRight(text[:markerIdx], " \t\n\r")
		newSuffix := strings.TrimSpace(remaining[braceIdx+int(consumed):])
		if newSuffix == "" {
			return newPrefix, name, candidate, "", true
		}
		if fallback == nil {
			fallback = candidate
			fallbackPrefix, fallbackSuffix = newPrefix, newSuffix
		}
	}
	if fallback != nil {
		return fallbackPrefix, name, fallback, fallbackSuffix, true
	}
	return "", "", nil, "", false
}

// fallbackPrefix / fallbackSuffix carry the accepted candidate's prose when
// only a mid-text call (with trailing prose) parsed.
var (
	fallbackPrefix string
	fallbackSuffix string
)

// RecoverTextToolCalls ports recover_text_tool_calls: only when there are
// no structured tool calls.
func RecoverTextToolCalls(text string) TextRecovery {
	if strings.TrimSpace(text) == "" {
		return TextRecovery{Status: "none"}
	}
	if prefix, name, arguments, suffix, ok := parseTextWrappedToolCall(text); ok {
		sanitized := prefix
		if suffix != "" {
			if sanitized != "" {
				sanitized += "\n"
			}
			sanitized += suffix
		}
		return TextRecovery{Status: "recovered",
			Calls:     []MarkupToolCall{{Name: name, Arguments: arguments}},
			Sanitized: sanitized}
	}
	return ScanToolCallMarkup(text)
}

// UnparsedMarkupReminder is the model-facing re-issue nudge.
func UnparsedMarkupReminder(reason string) string {
	return fmt.Sprintf(
		"<system-reminder>Your previous response contained a tool call written as plain-text markup (invoke/parameter envelope) instead of a native structured tool call. It could not be parsed (%s), so nothing was executed. Re-issue the intended tool call now using the native tool-calling interface, with the tool name and a complete JSON object of arguments. Do not print tool-call markup as text and do not repeat completed work.</system-reminder>",
		reason)
}

// UnparsedMarkupNotice is the user-visible diagnostic.
func UnparsedMarkupNotice(reason string) string {
	return fmt.Sprintf(
		"[tool-call recovery] The model printed a tool call as text markup that could not be parsed (%s). Nothing was executed. Asking the model to re-issue it as a native tool call.",
		reason)
}
