// Schema rejection recognition (ported from jcode-schema-dialect rejection.rs).
// The three parse shapes are verbatim from the filed issues, so the parser is
// tested against real provider output rather than invented strings.
package dialect

import (
	"encoding/json"
	"strings"
)

// SchemaRejection is what a provider objected to.
type SchemaRejection struct {
	// Keywords is every offending JSON Schema keyword the provider named.
	// Plural: a real Gemini 400 reports one fieldViolations entry per bad
	// keyword, so learning only the first would cost one failed turn each.
	Keywords []string
	// Format is the offending format value, when the provider names one.
	Format *string
	// Tool is the tool whose schema was rejected, when the provider names one.
	Tool *string
}

// Keyword returns the first named keyword, for callers that only need one.
func (r *SchemaRejection) Keyword() (string, bool) {
	if len(r.Keywords) == 0 {
		return "", false
	}
	return r.Keywords[0], true
}

// IsActionable reports whether the rejection identifies something to strip.
// A rejection we cannot attribute is not worth retrying: the retry would send
// a byte-identical request.
func (r *SchemaRejection) IsActionable() bool {
	return len(r.Keywords) > 0 || r.Format != nil
}

// ClassifyRejection extracts a schema rejection from a provider error message.
// A nil result means the error is not about tool schemas.
func ClassifyRejection(message string) *SchemaRejection {
	tool := extractToolName(message)

	// Gemini / Antigravity (#754):
	//   Invalid JSON payload received. Unknown name "propertyNames" at
	//   'request.tools[0].function_declarations[32].parameters...': Cannot find field.
	if strings.Contains(message, "Cannot find field") {
		names := extractAllQuotedAfter(message, "Unknown name")
		if len(names) > 0 {
			return &SchemaRejection{Keywords: names, Tool: tool}
		}
	}

	// OpenAI (#543): 'uri' is not a valid format.
	if format, ok := extractQuotedBefore(message, "is not a valid format"); ok {
		return &SchemaRejection{Format: &format, Tool: tool}
	}

	// OpenAI (#687): 'uniqueItems' is not permitted.
	if keyword, ok := extractQuotedBefore(message, "is not permitted"); ok {
		return &SchemaRejection{Keywords: []string{keyword}, Tool: tool}
	}

	// Gemini (#655): required fields ['label'] are not defined in the schema
	// properties. Structural, not a keyword, but still actionable via
	// re-normalization with pruning enabled.
	if strings.Contains(message, "are not defined in the schema properties") {
		return &SchemaRejection{Keywords: []string{"required"}, Tool: tool}
	}

	// OpenAI (#713): "schema must have a 'type' key". Structural: nothing can
	// be stripped, so it is recognized-but-unactionable — labeled for the user
	// instead of surfacing a raw 400, and never retried identically.
	if strings.Contains(message, "must have a 'type' key") || strings.Contains(message, "is not of type") {
		return &SchemaRejection{Tool: tool}
	}

	// Anthropic / OpenRouter (#495) and the Antigravity Claude bridge.
	if strings.Contains(message, "does not support oneOf, allOf, or anyOf") ||
		(strings.Contains(message, "input_schema") && strings.Contains(message, "JSON Schema draft 2020-12")) {
		return &SchemaRejection{Keywords: []string{"anyOf"}, Tool: tool}
	}

	return nil
}

// IsSchemaError reports whether an error is *about* tool schemas at all, even
// if unattributable. Used to label a hard failure with a hint, not a raw 400.
func IsSchemaError(message string) bool {
	if ClassifyRejection(message) != nil {
		return true
	}
	lowered := strings.ToLower(message)
	return strings.Contains(lowered, "invalid_function_parameters") ||
		strings.Contains(lowered, "invalid schema for function") ||
		(strings.Contains(lowered, "function_declarations") && strings.Contains(lowered, "invalid"))
}

func extractToolName(message string) *string {
	// OpenAI: Invalid schema for function 'mcp__firecrawl__firecrawl_map': ...
	if tool, ok := extractQuotedAfter(message, "for function"); ok {
		return &tool
	}
	return nil
}

// cleanToken trims quoting artifacts a provider embedded inside its own error
// string (that is how the real Antigravity 400 arrives).
func cleanToken(token string) (string, bool) {
	token = strings.TrimSpace(strings.Trim(strings.TrimSpace(token), `\`))
	token = strings.TrimSpace(token)
	return token, token != ""
}

// extractQuotedAfter finds the first single- or double-quoted token after marker.
func extractQuotedAfter(message, marker string) (string, bool) {
	index := strings.Index(message, marker)
	if index < 0 {
		return "", false
	}
	rest := message[index+len(marker):]
	quoteIndex := strings.IndexAny(rest, `'"`)
	if quoteIndex < 0 {
		return "", false
	}
	quoteChar := rest[quoteIndex]
	after := rest[quoteIndex+1:]
	end := strings.IndexByte(after, quoteChar)
	if end < 0 {
		return "", false
	}
	return cleanToken(after[:end])
}

// extractAllQuotedAfter collects every distinct quoted token following any
// occurrence of marker. A Gemini 400 repeats "Unknown name X" once per
// fieldViolations entry and the top-level message duplicates the first one.
func extractAllQuotedAfter(message, marker string) []string {
	var found []string
	seen := map[string]bool{}
	cursor := 0
	for {
		offset := strings.Index(message[cursor:], marker)
		if offset < 0 {
			break
		}
		start := cursor + offset
		if token, ok := extractQuotedAfter(message[start:], marker); ok && !seen[token] {
			seen[token] = true
			found = append(found, token)
		}
		cursor = start + len(marker)
	}
	return found
}

// extractQuotedBefore finds the last single- or double-quoted token before marker.
func extractQuotedBefore(message, marker string) (string, bool) {
	end := strings.Index(message, marker)
	if end < 0 {
		return "", false
	}
	head := strings.TrimRight(message[:end], " \t\r\n")
	closeIndex := strings.LastIndexAny(head, `'"`)
	if closeIndex < 0 {
		return "", false
	}
	quoteChar := head[closeIndex]
	open := strings.LastIndexByte(head[:closeIndex], quoteChar)
	if open < 0 {
		return "", false
	}
	return cleanToken(head[open+1 : closeIndex])
}

// SchemaContainsKeyword reports whether a schema still contains the construct
// a provider rejected (guards a pointless retry when the rejection names
// something we did not send).
func SchemaContainsKeyword(schema json.RawMessage, keyword string) bool {
	var value any
	if err := json.Unmarshal(schema, &value); err != nil {
		return false
	}
	return valueContainsKeyword(value, keyword)
}

func valueContainsKeyword(value any, keyword string) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == keyword || valueContainsKeyword(item, keyword) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if valueContainsKeyword(item, keyword) {
				return true
			}
		}
	}
	return false
}

// RecoveryAction is what recover from a schema error should do.
type RecoveryAction struct {
	// Kind: notSchemaRelated / unrecoverable / retryWithoutConstruct.
	Kind RecoveryKind
	// Hint is the diagnostic for unrecoverable actions.
	Hint string
	// Description narrates the retry for RetryWithoutConstruct.
	Description string
}

// RecoveryKind enumerates recovery outcomes.
type RecoveryKind int

const (
	// RecoveryNotSchemaRelated means the error is unrelated to tool schemas.
	RecoveryNotSchemaRelated RecoveryKind = iota
	// RecoveryUnrecoverable means the error is a schema failure but nothing
	// can be stripped.
	RecoveryUnrecoverable
	// RecoveryRetryWithoutConstruct means the construct was learned and the
	// caller should re-send the turn.
	RecoveryRetryWithoutConstruct
)

// RecoverFromError parses a provider error, learns the rejected construct into
// the quirk store, and reports the retry decision (ported from
// recover_from_error).
func RecoverFromError(message string, spec *DialectSpec) RecoveryAction {
	rejection := ClassifyRejection(message)
	if rejection == nil {
		if IsSchemaError(message) {
			return RecoveryAction{Kind: RecoveryUnrecoverable, Hint: "provider `" + spec.ID +
				"` rejected the tool schemas but did not name the construct; the raw error is the only diagnostic available"}
		}
		return RecoveryAction{Kind: RecoveryNotSchemaRelated}
	}

	learned := false
	var described []string

	// Learn every keyword the response named, not just the first.
	for _, keyword := range rejection.Keywords {
		// A load-bearing keyword cannot be dropped without destroying the
		// tool's meaning: a real bug, not a quirk to absorb.
		if !IsDroppable(keyword) {
			return RecoveryAction{Kind: RecoveryUnrecoverable, Hint: "provider `" + spec.ID +
				"` rejected the load-bearing keyword `" + keyword + "`, which cannot be stripped " +
				"without changing what the tool accepts; the dialect's transforms need a real fix"}
		}
		if RecordKeyword(spec.ID, keyword) {
			learned = true
			described = append(described, "keyword `"+keyword+"`")
		}
	}
	if rejection.Format != nil && RecordFormat(spec.ID, *rejection.Format) {
		learned = true
		described = append(described, "format `"+*rejection.Format+"`")
	}

	if learned {
		tool := ""
		if rejection.Tool != nil {
			tool = " (from tool `" + *rejection.Tool + "`)"
		}
		return RecoveryAction{Kind: RecoveryRetryWithoutConstruct, Description: "provider `" +
			spec.ID + "` rejected " + strings.Join(described, " and ") + tool +
			"; stripping it and retrying, and remembering it for future requests"}
	}

	firstConstruct := "a schema construct"
	if keyword, ok := rejection.Keyword(); ok {
		firstConstruct = keyword
	} else if rejection.Format != nil {
		firstConstruct = *rejection.Format
	}
	hint := ""
	if rejection.IsActionable() {
		hint = "provider `" + spec.ID + "` rejected " + firstConstruct +
			" again after it was already being stripped, so the construct is not what actually failed"
	} else {
		hint = "provider `" + spec.ID + "` reported a schema error naming no actionable construct"
	}
	return RecoveryAction{Kind: RecoveryUnrecoverable, Hint: hint}
}
