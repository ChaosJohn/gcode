// Package openai ports jcode-provider-openai: the OpenAI Responses API
// request shaping, tool schema compatibility, and stream decoding.
package openai

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/ChaosJohn/gcode/internal/schema/dialect"
	"github.com/ChaosJohn/gcode/internal/types"
)

// OpenAICompatibleSchema normalizes a tool-parameter schema for the OpenAI
// function-parameters subset (provider-core/src/openai_schema.rs).
func OpenAICompatibleSchema(schema any) any {
	if raw, ok := schema.(json.RawMessage); ok {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return raw
		}
		return dialect.Apply(decoded, &dialect.OpenAI)
	}
	return dialect.Apply(schema, &dialect.OpenAI)
}

// openAISupportedStringFormats: format values accepted by strict structured
// output; unknown formats (e.g. "uri") are stripped.
var openAISupportedStringFormats = []string{
	"date-time", "time", "date", "duration", "email", "hostname", "ipv4", "ipv6", "uuid",
}

func isSupportedStringFormat(value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	for _, format := range openAISupportedStringFormats {
		if s == format {
			return true
		}
	}
	return false
}

// schemaHasTypeInfo: an "empty" schema accepts anything in JSON Schema, but
// OpenAI's strict subset requires a concrete type keyword.
func schemaHasTypeInfo(schema any) bool {
	switch v := schema.(type) {
	case bool:
		return false
	case map[string]any:
		for _, key := range []string{"type", "enum", "const", "$ref", "anyOf", "oneOf", "allOf", "properties", "items"} {
			if _, ok := v[key]; ok {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// SchemaSupportsStrict reports whether a schema clears OpenAI strict mode.
func SchemaSupportsStrict(schema any) bool {
	switch v := schema.(type) {
	case map[string]any:
		return checkStrictMap(v)
	case []any:
		for _, item := range v {
			if !SchemaSupportsStrict(item) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func checkStrictMap(m map[string]any) bool {
	isObjectTyped := false
	switch t := m["type"].(type) {
	case string:
		isObjectTyped = t == "object"
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && s == "object" {
				isObjectTyped = true
			}
		}
	}
	hasProperties := false
	if props, ok := m["properties"].(map[string]any); ok && len(props) > 0 {
		hasProperties = true
	}

	if isObjectTyped && !hasProperties {
		return false
	}
	if isObjectTyped {
		if addProps, ok := m["additionalProperties"]; ok {
			switch v := addProps.(type) {
			case bool:
				if v {
					return false
				}
			case map[string]any:
				return false
			}
		}
	}
	// A declared property that says nothing about its type is legal JSON
	// Schema, but OpenAI's strict validator rejects the whole catalog (#713).
	if props, ok := m["properties"].(map[string]any); ok {
		for _, property := range props {
			if !declaresAType(property) {
				return false
			}
		}
	}
	// A combiner branch without a concrete type fails the catalog (#711).
	for _, combiner := range []string{"anyOf", "oneOf", "allOf"} {
		if branches, ok := m[combiner].([]any); ok {
			for _, branch := range branches {
				if !branchNamesAType(branch) {
					return false
				}
			}
		}
	}
	// An array whose items are unconstrained (#711).
	isArrayTyped := false
	switch t := m["type"].(type) {
	case string:
		isArrayTyped = t == "array"
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && s == "array" {
				isArrayTyped = true
			}
		}
	}
	if isArrayTyped {
		items, ok := m["items"]
		if !ok || !declaresAType(items) {
			return false
		}
	}
	// A surviving $ref cannot resolve (strict normalization has no $defs
	// resolution guarantee) (#711).
	if _, ok := m["$ref"]; ok {
		return false
	}
	// A property with no type info becomes an untyped anyOf branch under
	// strict normalization and fails the catalog (#713).
	if props, ok := m["properties"].(map[string]any); ok {
		for _, prop := range props {
			if !schemaHasTypeInfo(prop) {
				return false
			}
		}
	}

	for _, value := range m {
		if !SchemaSupportsStrict(value) {
			return false
		}
	}
	return true
}

// declaresAType: `true` for boolean schemas (complete JSON Schemas), for
// objects only when a type-bearing keyword is present.
func declaresAType(schema any) bool {
	m, ok := schema.(map[string]any)
	if !ok {
		_, isBool := schema.(bool)
		return isBool
	}
	for _, key := range []string{"type", "enum", "const", "anyOf", "oneOf", "allOf", "$ref", "properties", "items"} {
		if _, present := m[key]; present {
			return true
		}
	}
	return false
}

// branchNamesAType: stricter than declaresAType — a combiner branch must name
// a type (or $ref / nested combiner) for OpenAI strict mode (#711).
func branchNamesAType(branch any) bool {
	m, ok := branch.(map[string]any)
	if !ok {
		_, isBool := branch.(bool)
		return isBool
	}
	if _, ok := m["type"]; ok {
		return true
	}
	if _, ok := m["$ref"]; ok {
		return true
	}
	for _, combiner := range []string{"anyOf", "oneOf", "allOf"} {
		if _, ok := m[combiner]; ok {
			return true
		}
	}
	return false
}

// MakeSchemaNullable widens a schema to also accept null.
func MakeSchemaNullable(schema any) any {
	switch v := schema.(type) {
	case map[string]any:
		if t, ok := v["type"].(string); ok {
			if t != "null" {
				v["type"] = []any{t, "null"}
			}
			return v
		}
		if types, ok := v["type"].([]any); ok {
			hasNull := false
			for _, item := range types {
				if s, isStr := item.(string); isStr && s == "null" {
					hasNull = true
				}
			}
			if !hasNull {
				v["type"] = append(types, "null")
			}
			return v
		}
		if anyOf, ok := v["anyOf"].([]any); ok {
			hasNull := false
			for _, item := range anyOf {
				if m, isMap := item.(map[string]any); isMap {
					if s, isStr := m["type"].(string); isStr && s == "null" {
						hasNull = true
					}
				}
			}
			if !hasNull {
				v["anyOf"] = append(anyOf, map[string]any{"type": "null"})
			}
			return v
		}
		return map[string]any{"anyOf": []any{v, map[string]any{"type": "null"}}}
	default:
		return map[string]any{"anyOf": []any{schema, map[string]any{"type": "null"}}}
	}
}

// StrictNormalizeSchema converts a schema into OpenAI strict structured-output
// shape: every property required, optional ones made nullable, unknown formats
// dropped, additionalProperties:false on objects.
func StrictNormalizeSchema(schema any) any {
	switch v := schema.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, value := range v {
			if key == "format" && !isSupportedStringFormat(value) {
				continue
			}
			out[key] = normalizeStrictKeyword(key, value)
		}
		isObjectTyped := objectTyped(out)
		normalizeRequiredProperties(out)
		if isObjectTyped {
			if _, hasProps := out["properties"]; hasProps {
				out["additionalProperties"] = false
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, StrictNormalizeSchema(item))
		}
		return out
	default:
		return schema
	}
}

func objectTyped(m map[string]any) bool {
	switch t := m["type"].(type) {
	case string:
		return t == "object"
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && s == "object" {
				return true
			}
		}
	}
	return false
}

func normalizeStrictKeyword(key string, value any) any {
	switch key {
	case "properties", "$defs", "definitions", "patternProperties":
		if children, ok := value.(map[string]any); ok {
			out := map[string]any{}
			for childKey, childValue := range children {
				out[childKey] = StrictNormalizeSchema(childValue)
			}
			return out
		}
		return StrictNormalizeSchema(value)
	case "allOf", "anyOf", "oneOf", "prefixItems":
		if items, ok := value.([]any); ok {
			out := make([]any, 0, len(items))
			for _, item := range items {
				out = append(out, StrictNormalizeSchema(item))
			}
			return out
		}
		return StrictNormalizeSchema(value)
	default:
		return StrictNormalizeSchema(value)
	}
}

// normalizeRequiredProperties: strict mode requires all properties listed as
// required; optional properties become nullable instead.
func normalizeRequiredProperties(m map[string]any) {
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	existing := map[string]bool{}
	if required, ok := m["required"].([]any); ok {
		for _, item := range required {
			if s, isStr := item.(string); isStr {
				existing[s] = true
			}
		}
	}
	for name, propSchema := range props {
		if !existing[name] {
			props[name] = MakeSchemaNullable(propSchema)
		}
	}
	required := make([]any, 0, len(names))
	for _, name := range names {
		required = append(required, name)
	}
	m["required"] = required
}

// toolJSON emits the function definition with fixed key order.
type toolJSON struct {
	Type        string
	Name        string
	Description string
	Strict      bool
	Parameters  any
}

// MarshalJSON renders the tool definition in serde order.
func (t toolJSON) MarshalJSON() ([]byte, error) {
	bld := &jsonBuilder{}
	bld.strKey("type", t.Type)
	bld.strKey("name", t.Name)
	bld.strKey("description", t.Description)
	if t.Strict {
		bld.rawKey("strict", "true")
	} else {
		bld.rawKey("strict", "false")
	}
	params, err := types.MarshalCompact(t.Parameters)
	if err != nil {
		return nil, err
	}
	bld.rawKey("parameters", string(params))
	return bld.bytes(), nil
}

// jsonBuilder assembles objects with fixed key order.
type jsonBuilder struct{ parts []string }

func (b *jsonBuilder) rawKey(key, rawJSON string) {
	b.parts = append(b.parts, `"`+key+`":`+rawJSON)
}

func (b *jsonBuilder) strKey(key, value string) {
	encoded, _ := types.MarshalCompact(value)
	b.parts = append(b.parts, `"`+key+`":`+string(encoded))
}

func (b *jsonBuilder) bytes() []byte {
	return []byte("{" + strings.Join(b.parts, ",") + "}")
}

// BuildTool builds the Responses API function definition for one tool
// (request.rs build_tool). Deferred tools are omitted from the top-level
// tools array (see BuildTools).
func BuildTool(definition types.ToolDefinition) json.RawMessage {
	compatible := OpenAICompatibleSchema(json.RawMessage(definition.InputSchema))
	supportsStrict := SchemaSupportsStrict(compatible)
	var parameters any
	if supportsStrict {
		parameters = StrictNormalizeSchema(compatible)
	} else {
		parameters = compatible
	}
	body, err := types.MarshalCompact(toolJSON{
		Type:        "function",
		Name:        definition.Name,
		Description: definition.Description,
		Strict:      supportsStrict,
		Parameters:  parameters,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return body
}

// BuildTools returns eager tool definitions in Responses format.
func BuildTools(tools []types.ToolDefinition) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		if tool.DeferLoading {
			continue
		}
		out = append(out, BuildTool(tool))
	}
	return out
}
