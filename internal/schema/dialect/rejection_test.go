// Rejection classification + recovery tests (ported from
// jcode-schema-dialect tests and tests/recovery_coverage.rs).
package dialect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gemini754Error = `Invalid JSON payload received. Unknown name "propertyNames" at ` +
	`'request.tools[0].function_declarations[32].parameters.properties[0].value': Cannot find field.`

// Ported: classify (Gemini #754) + every_dialect_is_accounted_for
func TestClassifyGeminiUnknownName(t *testing.T) {
	rejection := ClassifyRejection(gemini754Error)
	if rejection == nil {
		t.Fatal("754 shape must classify")
	}
	if strings.Join(rejection.Keywords, ",") != "propertyNames" {
		t.Fatalf("keywords: %v", rejection.Keywords)
	}
	if !rejection.IsActionable() {
		t.Fatal("keyword rejection is actionable")
	}
	// Embedded-JSON quoting artifacts are cleaned.
	nested := `error: Unknown name \"propertyNames\" at 'x': Cannot find field.`
	if rejection := ClassifyRejection(nested); rejection == nil ||
		rejection.Keywords[0] != "propertyNames" {
		t.Fatalf("escaped quoting must be cleaned: %+v", rejection)
	}
}

// Multiple fieldViolations entries all get learned, deduplicated.
func TestClassifyCollectsAllKeywords(t *testing.T) {
	message := `Invalid JSON payload received. Unknown name "dependentRequired" at 'a': Cannot find field. ` +
		`Unknown name "unevaluatedItems" at 'b': Cannot find field. ` +
		`Unknown name "dependentRequired" at 'c': Cannot find field.`
	rejection := ClassifyRejection(message)
	if strings.Join(rejection.Keywords, ",") != "dependentRequired,unevaluatedItems" {
		t.Fatalf("dedup across violations: %v", rejection.Keywords)
	}
}

// Ported: OpenAI #543 / #687 shapes.
func TestClassifyOpenAIShapes(t *testing.T) {
	rejection := ClassifyRejection(`Invalid schema for function 'map': 'uri' is not a valid format.`)
	if rejection == nil || rejection.Format == nil || *rejection.Format != "uri" {
		t.Fatalf("format rejection: %+v", rejection)
	}
	if rejection.Tool == nil || *rejection.Tool != "map" {
		t.Fatalf("tool extraction: %+v", rejection.Tool)
	}
	rejection = ClassifyRejection(`Invalid schema for function 't': 'uniqueItems' is not permitted.`)
	if rejection == nil || strings.Join(rejection.Keywords, ",") != "uniqueItems" {
		t.Fatalf("not-permitted rejection: %+v", rejection)
	}
}

// Ported: Gemini #655 / OpenAI #713 / Anthropic #495 shapes.
func TestClassifyStructuralShapes(t *testing.T) {
	rejection := ClassifyRejection(`required fields ['label'] are not defined in the schema properties`)
	if rejection == nil || strings.Join(rejection.Keywords, ",") != "required" {
		t.Fatalf("dangling required: %+v", rejection)
	}
	rejection = ClassifyRejection(`'f' is not of type 'object'`)
	if rejection == nil || rejection.IsActionable() {
		t.Fatalf("type mismatch is recognized but unactionable: %+v", rejection)
	}
	rejection = ClassifyRejection(`tools.0.input_schema: JSON Schema draft 2020-12 is not supported`)
	if rejection == nil || strings.Join(rejection.Keywords, ",") != "anyOf" {
		t.Fatalf("combiner rejection: %+v", rejection)
	}
	if ClassifyRejection("connection reset by peer") != nil {
		t.Fatalf("operational failure is not schema-related")
	}
}

// Ported: is_schema_error.
func TestIsSchemaError(t *testing.T) {
	if !IsSchemaError("400 invalid_function_parameters") {
		t.Fatal("invalid_function_parameters")
	}
	if !IsSchemaError("Invalid schema for function 'x'") {
		t.Fatal("invalid schema for function")
	}
	if !IsSchemaError("function_declarations: invalid value") {
		t.Fatal("function_declarations + invalid")
	}
	if IsSchemaError("connection refused") {
		t.Fatal("operational failure")
	}
}

// Ported: recovery learns once, retries once, then refuses to loop.
func TestRecoverLearnsOnceThenRefuses(t *testing.T) {
	dir := t.TempDir()
	SetStorePathForTest(dir)
	spec := &Gemini

	action := RecoverFromError(gemini754Error, spec)
	if action.Kind != RecoveryRetryWithoutConstruct {
		t.Fatalf("first encounter learns and retries: %+v", action)
	}
	if !strings.Contains(action.Description, "propertyNames") {
		t.Fatalf("description names the construct: %+v", action.Description)
	}
	if learned := LearnedFor(spec.ID); len(learned.RejectedKeywords) != 1 {
		t.Fatalf("quirk persisted: %+v", learned)
	}

	action = RecoverFromError(gemini754Error, spec)
	if action.Kind != RecoveryUnrecoverable {
		t.Fatalf("already-stripped rejection refuses to retry: %+v", action)
	}
}

// Ported: no_dialect_absorbs_a_load_bearing_rejection.
func TestRecoverRefusesLoadBearing(t *testing.T) {
	SetStorePathForTest(t.TempDir())
	spec := &Gemini
	action := RecoverFromError(
		`Unknown name "properties" at 'x': Cannot find field.`, spec)
	if action.Kind != RecoveryUnrecoverable || !strings.Contains(action.Hint, "load-bearing") {
		t.Fatalf("load-bearing keyword must not be absorbed: %+v", action)
	}
}

// Ported: no_dialect_treats_an_operational_failure_as_a_schema_rejection.
func TestRecoverIgnoresOperationalFailures(t *testing.T) {
	for _, spec := range []*DialectSpec{&Gemini, &OpenAI, &Anthropic} {
		if action := RecoverFromError("connection reset by peer", spec); action.Kind != RecoveryNotSchemaRelated {
			t.Fatalf("operational failure misclassified for %s: %+v", spec.ID, action)
		}
	}
}

// Unattributable schema errors label the failure instead of retrying.
func TestRecoverUnattributable(t *testing.T) {
	SetStorePathForTest(t.TempDir())
	action := RecoverFromError("Invalid schema for function 'x'", &Gemini)
	if action.Kind != RecoveryUnrecoverable {
		t.Fatalf("unattributable: %+v", action)
	}
	if !strings.Contains(action.Hint, "did not name the construct") {
		t.Fatalf("hint: %+v", action.Hint)
	}
}

// Ported: schema_contains_keyword.
func TestSchemaContainsKeyword(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"array","items":{"oneOf":[]}}}}`)
	if !SchemaContainsKeyword(schema, "oneOf") {
		t.Fatal("deep keyword found")
	}
	if SchemaContainsKeyword(schema, "propertyNames") {
		t.Fatal("absent keyword")
	}
	if SchemaContainsKeyword(json.RawMessage(`not json`), "oneOf") {
		t.Fatal("unparsable schema contains nothing")
	}
}

// Ported: issue_754_playwright_schema_is_accepted_by_the_gemini_dialect
// (normalize leaves nothing the 754 error names).
func TestIssue754SchemaAccepted(t *testing.T) {
	SetStorePathForTest(t.TempDir())
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"data": {"type": "object", "propertyNames": {"type": "string"},
				"properties": {"url": {"type": "string", "format": "uri"}}},
			"ids": {"type": "array", "items": {"type": "string", "uniqueItems": true}, "uniqueItems": true}
		},
		"required": ["data", "missing"]
	}`)
	out := NormalizeSchemaJSON(schema, &Gemini)
	if SchemaContainsKeyword(out, "propertyNames") || SchemaContainsKeyword(out, "uniqueItems") {
		t.Fatalf("754 keywords must be stripped: %s", out)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	// Upstream asserts out["required"] == ["data"]: the dangling name is
	// pruned, the valid one survives.
	if got, _ := json.Marshal(decoded["required"]); string(got) != `["data"]` {
		t.Fatalf("required pruning: %s", got)
	}
}

// The quirk store round-trips through disk.
func TestQuirkStorePersistence(t *testing.T) {
	dir := t.TempDir()
	SetStorePathForTest(dir)
	if !RecordKeyword(Gemini.ID, "propertyNames") {
		t.Fatal("first learn reports true")
	}
	if RecordKeyword(Gemini.ID, "propertyNames") {
		t.Fatal("second learn of the same keyword reports false")
	}
	if !RecordFormat(Gemini.ID, "uri") {
		t.Fatal("format learn")
	}
	content, err := os.ReadFile(filepath.Join(dir, "schema-quirks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "propertyNames") {
		t.Fatalf("persisted: %s", content)
	}
}
