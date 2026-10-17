package openai

import (
	"encoding/json"
	"testing"

	"github.com/ChaosJohn/gcode/internal/types"
)

func strict(t *testing.T, schema string) map[string]any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(StrictNormalizeSchema(decoded))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// Ported: strict_normalize_schema_drops_uri_format.
func TestStrictDropsUnknownFormats(t *testing.T) {
	out := strict(t, `{
		"type":"object",
		"properties":{
			"url":{"type":"string","format":"uri"},
			"when":{"type":"string","format":"date-time"},
			"format":{"type":"string"}
		},
		"required":["url","when","format"]
	}`)
	url := out["properties"].(map[string]any)["url"].(map[string]any)
	if _, has := url["format"]; has {
		t.Errorf("uri dropped: %v", url)
	}
	when := out["properties"].(map[string]any)["when"].(map[string]any)
	if when["format"] != "date-time" {
		t.Errorf("date-time kept: %v", when)
	}
	// A property literally named "format" is untouched.
	format := out["properties"].(map[string]any)["format"].(map[string]any)
	if _, has := format["format"]; has {
		t.Errorf("format property must not be filtered: %v", format)
	}
}

// Ported: strict_normalize_schema_strips_nested_unsupported_formats.
func TestStrictStripsNestedFormats(t *testing.T) {
	out := strict(t, `{
		"type":"object",
		"properties":{"links":{"type":"array","items":{"type":"string","format":"uri"}}},
		"required":["links"]
	}`)
	links := out["properties"].(map[string]any)["links"].(map[string]any)
	items := links["items"].(map[string]any)
	if _, has := items["format"]; has {
		t.Errorf("nested uri dropped: %v", items)
	}
}

// Ported: strict_normalize_schema_recurses_through_nested_object_keywords.
func TestStrictRecursesNested(t *testing.T) {
	out := strict(t, `{
		"type":"object",
		"properties":{"child":{"type":"object","properties":{"name":{"type":"string"}}}}
	}`)
	child := out["properties"].(map[string]any)["child"].(map[string]any)
	if child["additionalProperties"] != false {
		t.Errorf("nested object gets additionalProperties:false: %v", child)
	}
}

// Strict: all properties required, optional ones nullable.
func TestStrictRequiresAllAndNullablesOptionals(t *testing.T) {
	out := strict(t, `{
		"type":"object",
		"properties":{"a":{"type":"string"},"b":{"type":"integer"}},
		"required":["a"]
	}`)
	required, _ := out["required"].([]any)
	if len(required) != 2 {
		t.Errorf("all properties required: %v", out["required"])
	}
	props := out["properties"].(map[string]any)
	// Required property keeps its type; optional becomes [type, null].
	a := props["a"].(map[string]any)
	if a["type"] != "string" {
		t.Errorf("required keeps type: %v", a)
	}
	b := props["b"].(map[string]any)
	bTypes, _ := b["type"].([]any)
	if len(bTypes) != 2 || bTypes[1] != "null" {
		t.Errorf("optional made nullable: %v", b)
	}
}

// Ported: strict_normalize_schema_preserves_existing_nullability.
func TestMakeSchemaNullablePreserves(t *testing.T) {
	schema := map[string]any{"anyOf": []any{
		map[string]any{"type": "string"}, map[string]any{"type": "null"},
	}}
	out := MakeSchemaNullable(schema)
	if len(out.(map[string]any)["anyOf"].([]any)) != 2 {
		t.Errorf("existing null kept: %v", out)
	}
}

// Ported: schema_supports_strict cases.
func TestSchemaSupportsStrict(t *testing.T) {
	cases := map[string]bool{
		`{"type":"object","properties":{"a":{"type":"string"}}}`: true,
		`{"type":"object"}`:                                               false, // object without properties
		`{"type":"object","additionalProperties":true}`:                   false,
		`{"type":"object","properties":{"a":{"description":"x"}}}`:        false, // no type info (#713)
		`{"type":"object","properties":{"a":{"anyOf":[{"enum":["x"]}]}}}`: false, // branch without type (#711)
		`{"type":"array","items":{"type":"string"}}`:                      true,
		`{"type":"array"}`:                                                false, // unconstrained items
		`{"$ref":"#/definitions/x"}`:                                      false,
		`{"type":"object","properties":{"a":{"type":["string","null"]}}}`: true,
	}
	for schema, want := range cases {
		var decoded any
		if err := json.Unmarshal([]byte(schema), &decoded); err != nil {
			t.Fatal(err)
		}
		if got := SchemaSupportsStrict(decoded); got != want {
			t.Errorf("strict(%s) = %v, want %v", schema, got, want)
		}
	}
}

// BuildTool output shape.
func TestBuildTool(t *testing.T) {
	definition := toolDef("bash", "Run a shell command", `{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
	out := string(BuildTool(definition))
	var probe map[string]any
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		t.Fatal(err)
	}
	if probe["type"] != "function" || probe["name"] != "bash" || probe["strict"] != true {
		t.Errorf("tool = %s", out)
	}
	params := probe["parameters"].(map[string]any)
	if params["additionalProperties"] != false {
		t.Errorf("strict parameters: %v", params)
	}
	required, _ := params["required"].([]any)
	if len(required) != 1 || required[0] != "command" {
		t.Errorf("required = %v", required)
	}

	// Untyped property → non-strict, schema still sent.
	definition = toolDef("flex", "accepts anything", `{"type":"object","properties":{"value":{}}}`)
	probe = nil
	if err := json.Unmarshal(BuildTool(definition), &probe); err != nil {
		t.Fatal(err)
	}
	if probe["strict"] != false {
		t.Errorf("untyped property must disable strict: %s", string(mustJSON(probe)))
	}
}

func mustJSON(v any) []byte {
	out, _ := json.Marshal(v)
	return out
}

func toolDef(name, description, schema string) types.ToolDefinition {
	return types.ToolDefinition{Name: name, Description: description, InputSchema: json.RawMessage(schema)}
}
