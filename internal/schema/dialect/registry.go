package dialect

import "errors"

func errString(s string) error { return errors.New(s) }

// The dialect table: what each provider is known to accept. Every entry is
// evidence-based: a keyword is listed because jcode observed the provider
// accept it in a live request, not because the spec says it should.

// OpenAI is the OpenAI function-parameters subset.
var OpenAI = DialectSpec{
	ID: "openai",
	SupportedKeywords: []string{
		"description", "default", "examples", "format",
		"anyOf", "oneOf", "allOf",
		"$defs", "definitions", "$ref", "$schema", "const",
		"additionalProperties", "patternProperties", "prefixItems",
		"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
		"multipleOf", "minLength", "maxLength", "pattern",
		"minItems", "maxItems", "nullable",
	},
	// #543: unknown formats such as `uri` fail the strict validator.
	SupportedStringFormats: []string{
		"date-time", "time", "date", "duration", "email", "hostname", "ipv4", "ipv6", "uuid",
	},
	Transforms: DialectTransforms{
		OneOfAsAnyOf: true,
		// OpenAI accepts allOf but does not intersect its branches, so a
		// schema whose properties live only in allOf branches would advertise
		// none of them.
		MergeAllOfBranches: true,
	},
}

// Gemini is the generateContent OpenAPI 3.0 subset (#754, #655).
var Gemini = DialectSpec{
	ID: "gemini",
	SupportedKeywords: []string{
		"description", "default", "format", "anyOf", "nullable",
		"minimum", "maximum", "minItems", "maxItems",
		"minLength", "maxLength", "pattern", "example",
	},
	Transforms: DialectTransforms{
		// Code Assist rejects a root anyOf beside type/properties; nested
		// anyOf inside a property is fine, so only the root is widened.
		FlattenTopLevelCombiners: true,
		PruneDanglingRequired:    true,
		ConstAsEnum:              true,
		OneOfAsAnyOf:             true,
	},
}

// Anthropic accepts full draft 2020-12 inside properties but rejects
// combiners at the input schema's top level.
var Anthropic = DialectSpec{
	ID: "anthropic",
	SupportedKeywords: []string{
		"description", "default", "examples", "format",
		"anyOf", "oneOf", "allOf", "not", "if", "then", "else",
		"$defs", "definitions", "$ref", "$schema", "$comment", "const",
		"additionalProperties", "patternProperties", "propertyNames",
		"prefixItems", "contains", "uniqueItems",
		"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
		"multipleOf", "minLength", "maxLength", "pattern",
		"minItems", "maxItems", "minProperties", "maxProperties",
		"dependentRequired", "dependentSchemas", "nullable",
	},
	Transforms: DialectTransforms{
		FlattenTopLevelCombiners: true,
		// input_schema must be an object schema with a properties map: the
		// API rejects a bare {"type":"object"} and a non-object schema.
		RequirePropertiesOnObjects: true,
	},
}

// OpenRouter forwards to whichever upstream serves the model, so it must
// satisfy the strictest of them (#495, #446).
var OpenRouter = DialectSpec{
	ID:                     "openrouter",
	SupportedKeywords:      OpenAI.SupportedKeywords,
	SupportedStringFormats: nil,
	Transforms: DialectTransforms{
		FlattenTopLevelCombiners:   true,
		RequirePropertiesOnObjects: true,
	},
}

// AntigravityClaude: the Cloud Code backend parses every request as a
// generateContent payload (Gemini subset) and rejects combiners at any depth.
var AntigravityClaude = DialectSpec{
	ID:                     "antigravity-claude",
	SupportedKeywords:      Gemini.SupportedKeywords,
	SupportedStringFormats: nil,
	Transforms: DialectTransforms{
		FlattenAllCombiners:   true,
		PruneDanglingRequired: true,
		ConstAsEnum:           true,
		OneOfAsAnyOf:          true,
	},
}

// AntigravityBridge is the OpenAI-compatible bridge; Gemini's subset minus the
// numeric bounds it corrupts through protobuf int64 round-tripping.
var AntigravityBridge = DialectSpec{
	ID: "antigravity-bridge",
	SupportedKeywords: []string{
		"description", "default", "format", "anyOf", "nullable",
		"minimum", "maximum", "pattern", "example",
	},
	Transforms: DialectTransforms{
		FlattenAllCombiners:   true,
		PruneDanglingRequired: true,
		ConstAsEnum:           true,
		OneOfAsAnyOf:          true,
	},
}

// All is every registered dialect, for conformance sweeps.
var All = []*DialectSpec{
	&OpenAI, &Gemini, &Anthropic, &OpenRouter, &AntigravityClaude, &AntigravityBridge,
}

// ByID looks up a dialect by its stable id.
func ByID(id string) *DialectSpec {
	for _, spec := range All {
		if spec.ID == id {
			return spec
		}
	}
	return nil
}
