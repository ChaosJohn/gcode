package dialect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func norm(t *testing.T, schema string, spec *DialectSpec) map[string]any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(ApplyWithQuirks(decoded, spec, &LearnedQuirks{}))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAllRegisteredDialectsCoherent(t *testing.T) {
	ids := map[string]bool{}
	for _, spec := range All {
		if err := spec.Validate(); err != nil {
			t.Errorf("%s: %v", spec.ID, err)
		}
		if ids[spec.ID] {
			t.Errorf("duplicate dialect id %s", spec.ID)
		}
		ids[spec.ID] = true
	}
	if ByID("openai") == nil || ByID("nope") != nil {
		t.Error("lookup")
	}
}

func TestKeywordRoles(t *testing.T) {
	if KeywordRoleOf("properties") != RoleSubschemaMap ||
		KeywordRoleOf("propertyNames") != RoleSubschema ||
		KeywordRoleOf("anyOf") != RoleSubschemaArray ||
		KeywordRoleOf("enum") != RoleData ||
		KeywordRoleOf("x-vendor-thing") != RoleData {
		t.Error("classification")
	}
	for _, key := range LoadBearingKeywords {
		if IsDroppable(key) {
			t.Errorf("%s must never be droppable", key)
		}
	}
	if !IsDroppable("uniqueItems") || !IsDroppable("propertyNames") {
		t.Error("validation keywords are droppable")
	}
}

// Ported: build_tools_flattens_allof_schema_for_openai.
func TestOpenAIMergesAllOfBranches(t *testing.T) {
	schema := `{
		"allOf": [
			{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]},
			{"type":"object","properties":{"start_line":{"type":"integer"}}}
		]
	}`
	out := norm(t, schema, &OpenAI)
	if _, has := out["allOf"]; has {
		t.Errorf("allOf must be merged: %v", out)
	}
	if out["type"] != "object" {
		t.Errorf("type adopted: %v", out)
	}
	props := out["properties"].(map[string]any)
	if _, ok := props["file_path"]; !ok {
		t.Error("file_path kept")
	}
	if _, ok := props["start_line"]; !ok {
		t.Error("start_line kept")
	}
	required, _ := out["required"].([]any)
	if !containsAny(required, "file_path") {
		t.Errorf("allOf required promoted: %v", out["required"])
	}
}

func containsAny(list []any, want string) bool {
	for _, item := range list {
		if s, ok := item.(string); ok && s == want {
			return true
		}
	}
	return false
}

// Ported: build_tools_strips_unique_items_from_the_request_payload.
func TestOpenAIStripsUnsupportedKeywords(t *testing.T) {
	schema := `{
		"type":"object",
		"properties":{
			"ids":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":50,"uniqueItems":true},
			"fields":{"type":["string","null"]}
		},
		"required":["ids"]
	}`
	out := norm(t, schema, &OpenAI)
	encoded, _ := json.Marshal(out)
	if string(encoded) == "" || jsonContains(encoded, "uniqueItems") {
		t.Errorf("uniqueItems must be stripped: %s", encoded)
	}
	ids := out["properties"].(map[string]any)["ids"].(map[string]any)
	if ids["type"] != "array" {
		t.Error("ids type kept")
	}
	items := ids["items"].(map[string]any)
	if items["type"] != "string" {
		t.Error("items kept")
	}
	if ids["minItems"] != float64(1) || ids["maxItems"] != float64(50) {
		t.Error("constraints survive stripping")
	}
}

func jsonContains(raw []byte, needle string) bool {
	return len(needle) > 0 && len(raw) > 0 &&
		jsonContainsStr(string(raw), needle)
}

func jsonContainsStr(s, needle string) bool {
	return len(s) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(s); i++ {
			if s[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// Ported: tool_reference / oneOf-as-anyOf rename keeps branches on OpenAI.
func TestOneOfRenamedToAnyOf(t *testing.T) {
	schema := `{"oneOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"type":"object","properties":{"b":{"type":"integer"}}}]}`
	out := norm(t, schema, &OpenAI)
	if _, has := out["oneOf"]; has {
		t.Error("oneOf renamed")
	}
	branches, ok := out["anyOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Errorf("anyOf = %v", out["anyOf"])
	}
}

// Anthropic: top-level combiner flattened, properties map guaranteed.
func TestAnthropicFlattensRootCombiner(t *testing.T) {
	schema := `{"oneOf":[
		{"type":"object","properties":{"mode":{"type":"string"}},"required":["mode"]},
		{"type":"object","properties":{"count":{"type":"integer"}}}
	]}`
	out := norm(t, schema, &Anthropic)
	if _, has := out["oneOf"]; has {
		t.Errorf("root combiner must flatten: %v", out)
	}
	if out["type"] != "object" {
		t.Errorf("type = %v", out["type"])
	}
	props := out["properties"].(map[string]any)
	if _, ok := props["mode"]; !ok {
		t.Error("mode unioned")
	}
	if _, ok := props["count"]; !ok {
		t.Error("count unioned")
	}
	// Only allOf required is promoted; anyOf branch required is dropped.
	if _, has := out["required"]; has {
		t.Errorf("anyOf branch required must not promote: %v", out["required"])
	}
}

// Anthropic: bare object schema gains an empty properties map.
func TestAnthropicRequireProperties(t *testing.T) {
	out := norm(t, `{"type":"object"}`, &Anthropic)
	if _, ok := out["properties"].(map[string]any); !ok {
		t.Errorf("properties injected: %v", out)
	}
	out = norm(t, `{}`, &Anthropic)
	if out["type"] != "object" {
		t.Errorf("empty schema becomes object: %v", out)
	}
}

// Gemini: dangling required pruned, const → enum, root combiner flattened.
func TestGeminiTransforms(t *testing.T) {
	schema := `{"type":"object","properties":{"a":{"type":"string","const":"x"}},"required":["a","ghost"]}`
	out := norm(t, schema, &Gemini)
	req, _ := out["required"].([]any)
	if containsAny(req, "ghost") {
		t.Errorf("dangling required kept: %v", out["required"])
	}
	a := out["properties"].(map[string]any)["a"].(map[string]any)
	enum, ok := a["enum"].([]any)
	if !ok || len(enum) != 1 || enum[0] != "x" {
		t.Errorf("const as enum: %v", a)
	}
}

// Format filtering: OpenAI drops unknown formats, keeps known ones.
func TestOpenAIFormatFilter(t *testing.T) {
	schema := `{"type":"object","properties":{"when":{"type":"string","format":"date-time"},"url":{"type":"string","format":"uri"}}}`
	out := norm(t, schema, &OpenAI)
	props := out["properties"].(map[string]any)
	when := props["when"].(map[string]any)
	if when["format"] != "date-time" {
		t.Error("date-time kept")
	}
	url := props["url"].(map[string]any)
	if _, has := url["format"]; has {
		t.Errorf("uri dropped: %v", url)
	}
}

// Unknown keywords are data and survive dialects that support them
// (Gemini has no $ref in its list → dropped; OpenAI keeps it).
func TestRefSupportDiffers(t *testing.T) {
	schema := `{"type":"object","properties":{"x":{"$ref":"#/definitions/y"}},"$defs":{"y":{"type":"string"}}}`
	openaiOut := norm(t, schema, &OpenAI)
	if _, ok := openaiOut["$defs"]; !ok {
		t.Error("openai keeps $defs")
	}
	geminiOut := norm(t, schema, &Gemini)
	if _, ok := geminiOut["$defs"]; ok {
		t.Error("gemini drops $defs")
	}
	x := geminiOut["properties"].(map[string]any)["x"].(map[string]any)
	if _, has := x["$ref"]; has {
		t.Error("gemini drops $ref")
	}
}

// Quirks: learned rejections persist and apply.
func TestQuirksLearnAndApply(t *testing.T) {
	dir := t.TempDir()
	SetStorePathForTest(filepath.Join(dir, "schema-quirks.json"))
	defer SetStorePathForTest("")
	os.Unsetenv("GCODE_SCHEMA_QUIRKS_PATH")

	if got := LearnedFor("openai"); len(got.RejectedKeywords) != 0 {
		t.Fatalf("expected empty store, got %v", got)
	}
	if !RecordKeyword("openai", "minItems") {
		t.Fatal("first learn must report true")
	}
	if RecordKeyword("openai", "minItems") {
		t.Fatal("duplicate learn must report false")
	}
	learned := LearnedFor("openai")
	if !reflect.DeepEqual(learned.RejectedKeywords, []string{"minItems"}) {
		t.Fatalf("learned = %v", learned)
	}
	// Re-read from disk (fresh process semantics).
	learned = LearnedFor("openai")
	if len(learned.RejectedKeywords) != 1 {
		t.Fatalf("persisted = %v", learned)
	}

	schema := `{"type":"object","properties":{"ids":{"type":"array","minItems":1}}}`
	var decoded any
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(Normalize(decoded, &OpenAI))
	if err != nil {
		t.Fatal(err)
	}
	if jsonContains(out, "minItems") {
		t.Errorf("learned keyword must be dropped: %s", out)
	}
}

// Quirks never drop load-bearing keywords.
func TestQuirksCannotDropLoadBearing(t *testing.T) {
	dir := t.TempDir()
	SetStorePathForTest(filepath.Join(dir, "q.json"))
	defer SetStorePathForTest("")
	RecordKeyword("openai", "type")
	schema := `{"type":"object","properties":{"a":{"type":"string"}}}`
	out := norm(t, schema, &OpenAI)
	if out["type"] != "object" {
		t.Errorf("load-bearing survived quirk: %v", out)
	}
}
