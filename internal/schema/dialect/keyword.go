// Package dialect ports jcode-schema-dialect: provider JSON Schema
// compatibility as data. Each provider declares the subset it accepts
// (allow-list); unknown constructs are dropped instead of forwarded, so an
// unseen keyword is inert rather than fatal. One recursion (Apply) serves every
// dialect; adding provider knowledge is a table change.
package dialect

import "sort"

// KeywordRole classifies a schema keyword by what its value contains.
type KeywordRole int

const (
	// RoleSubschema: the value is one subschema (items, not, propertyNames);
	// it may also legally be a boolean.
	RoleSubschema KeywordRole = iota
	// RoleSubschemaMap: name -> subschema (properties, $defs). Keys are
	// user-controlled names, never keywords.
	RoleSubschemaMap
	// RoleSubschemaArray: array of subschemas (anyOf, prefixItems).
	RoleSubschemaArray
	// RoleData: plain data that must never be walked as a schema (enum,
	// const, required, default, examples) — or an unknown keyword.
	RoleData
)

var (
	subschemaMapKeywords   = []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"}
	subschemaKeywords      = []string{"items", "additionalItems", "additionalProperties", "contains", "propertyNames", "not", "if", "then", "else", "unevaluatedItems", "unevaluatedProperties", "contentSchema"}
	subschemaArrayKeywords = []string{"allOf", "anyOf", "oneOf", "prefixItems"}
	dataKeywords           = []string{"enum", "const", "default", "examples", "required"}
)

// KeywordRoleOf classifies a keyword.
func KeywordRoleOf(key string) KeywordRole {
	switch {
	case contains(subschemaMapKeywords, key):
		return RoleSubschemaMap
	case contains(subschemaKeywords, key):
		return RoleSubschema
	case contains(subschemaArrayKeywords, key):
		return RoleSubschemaArray
	default:
		// Unknown keywords (vendor extensions, future drafts) are data:
		// walking into them can only corrupt them, and a dialect that does
		// not support them drops them wholesale anyway.
		return RoleData
	}
}

// LoadBearingKeywords carry the meaning of a schema; a dialect that omits one
// silently changes what the tool accepts.
var LoadBearingKeywords = []string{"type", "properties", "items", "required", "enum", "description"}

// IsDroppable reports whether a keyword may be removed without changing which
// instances the tool accepts at execution time: removing a validation keyword
// only widens what the model is told it may send; the tool still validates the
// real call.
func IsDroppable(key string) bool { return !contains(LoadBearingKeywords, key) }

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func sortedStrings(list []string) []string {
	out := append([]string(nil), list...)
	sort.Strings(out)
	return out
}
