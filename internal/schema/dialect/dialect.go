package dialect

import (
	"bytes"
	"encoding/json"
)

// DialectTransforms are the structural rewrites a dialect can request: changes
// of shape, not key removals.
type DialectTransforms struct {
	// FlattenTopLevelCombiners collapses anyOf/oneOf/allOf at the schema root
	// into one object whose properties union every branch (Anthropic,
	// OpenRouter: #495).
	FlattenTopLevelCombiners bool
	// FlattenAllCombiners collapses every combiner anywhere to its first
	// branch, merging siblings (Antigravity bridge).
	FlattenAllCombiners bool
	// RequirePropertiesOnObjects inserts `properties: {}` into object-typed
	// schemas lacking it (LM Studio: #446).
	RequirePropertiesOnObjects bool
	// PruneDanglingRequired drops required entries naming an undeclared
	// property (Gemini: #655).
	PruneDanglingRequired bool
	// ConstAsEnum rewrites `const: X` as `enum: [X]`.
	ConstAsEnum bool
	// OneOfAsAnyOf rewrites `oneOf` as `anyOf`.
	OneOfAsAnyOf bool
	// MergeAllOfBranches merges each allOf branch's properties into the
	// enclosing object and drops the allOf.
	MergeAllOfBranches bool
}

// DialectSpec is a provider's accepted subset plus the rewrites to reach it.
type DialectSpec struct {
	// ID is the stable quirk-persistence key, e.g. "gemini".
	ID string
	// SupportedKeywords are known-accepted keywords; everything else drops.
	SupportedKeywords []string
	// SupportedStringFormats are accepted format values; empty = any.
	SupportedStringFormats []string
	Transforms             DialectTransforms
}

// universalKeywords every dialect gets for free.
var universalKeywords = []string{"type", "properties", "items", "required", "enum", "title"}

// Supports reports whether the dialect keeps key.
func (s *DialectSpec) Supports(key string) bool {
	return contains(universalKeywords, key) || contains(s.SupportedKeywords, key)
}

// SupportsFormat reports whether a format value survives.
func (s *DialectSpec) SupportsFormat(format string) bool {
	return len(s.SupportedStringFormats) == 0 || contains(s.SupportedStringFormats, format)
}

// Validate checks the spec is coherent: every load-bearing keyword supported,
// no redundant universal keywords.
func (s *DialectSpec) Validate() error {
	for _, key := range LoadBearingKeywords {
		if !s.Supports(key) {
			return errString("dialect `" + s.ID + "` does not support load-bearing keyword `" + key + "`")
		}
	}
	for _, key := range s.SupportedKeywords {
		if contains(universalKeywords, key) {
			return errString("dialect `" + s.ID + "` redundantly lists universal keyword `" + key + "`")
		}
	}
	return nil
}

// LearnedQuirks are keywords/formats learned from live rejections.
type LearnedQuirks struct {
	RejectedKeywords []string
	RejectedFormats  []string
}

// IsEmpty reports nothing learned.
func (q *LearnedQuirks) IsEmpty() bool {
	return len(q.RejectedKeywords) == 0 && len(q.RejectedFormats) == 0
}

// Apply normalizes a tool-parameter schema into spec's dialect.
func Apply(schema any, spec *DialectSpec) any {
	return ApplyWithQuirks(schema, spec, &LearnedQuirks{})
}

// ApplyWithQuirks additionally honors keywords learned from live rejections.
func ApplyWithQuirks(schema any, spec *DialectSpec, quirks *LearnedQuirks) any {
	out := walk(schema, spec, quirks)

	if spec.Transforms.FlattenAllCombiners {
		out = flattenAllCombiners(out)
	} else if spec.Transforms.FlattenTopLevelCombiners {
		flattenTopLevelCombiners(out)
	}

	// A tool with no parameters is `{}` or a non-object in some MCP servers;
	// strict validators want an object schema (#446).
	if spec.Transforms.RequirePropertiesOnObjects {
		m, ok := out.(map[string]any)
		switch {
		case ok && len(m) == 0:
			m["type"] = "object"
			m["properties"] = map[string]any{}
		case !ok:
			out = map[string]any{"type": "object", "properties": map[string]any{}}
		}
	}
	return out
}

func walk(schema any, spec *DialectSpec, quirks *LearnedQuirks) any {
	switch v := schema.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, value := range v {
			// Renames happen before the support check: the dialect supports
			// the renamed keyword, not the source one.
			outKey := key
			if key == "oneOf" && spec.Transforms.OneOfAsAnyOf {
				outKey = "anyOf"
			}
			droppedByQuirk := contains(quirks.RejectedKeywords, key)
			// Load-bearing keywords survive even a learned rejection.
			if (!spec.Supports(outKey) || droppedByQuirk) && IsDroppable(key) {
				if key == "const" && spec.Transforms.ConstAsEnum {
					out["enum"] = []any{value}
				}
				continue
			}
			if key == "format" {
				str, isStr := value.(string)
				if !isStr || !spec.SupportsFormat(str) || contains(quirks.RejectedFormats, str) {
					continue
				}
				out[key] = value
				continue
			}

			var normalized any
			switch KeywordRoleOf(key) {
			case RoleSubschemaMap:
				if children, ok := value.(map[string]any); ok {
					walked := map[string]any{}
					for name, child := range children {
						walked[name] = walk(child, spec, quirks)
					}
					normalized = walked
				} else {
					normalized = value
				}
			case RoleSubschemaArray:
				if items, ok := value.([]any); ok {
					walked := make([]any, 0, len(items))
					for _, item := range items {
						walked = append(walked, walk(item, spec, quirks))
					}
					normalized = walked
				} else {
					normalized = walk(value, spec, quirks)
				}
			case RoleSubschema:
				normalized = walk(value, spec, quirks)
			default:
				normalized = value
			}
			if existing, present := out[outKey]; present {
				// oneOf renamed onto an existing anyOf: merge branches rather
				// than clobbering one of them.
				if existingArr, ok := existing.([]any); ok {
					if incomingArr, ok := normalized.([]any); ok {
						out[outKey] = append(existingArr, incomingArr...)
						continue
					}
				}
				out[outKey] = normalized
			} else {
				out[outKey] = normalized
			}
		}

		if spec.Transforms.PruneDanglingRequired {
			pruneDanglingRequired(out)
		}
		if spec.Transforms.MergeAllOfBranches {
			mergeAllOfBranches(out)
		}
		if spec.Transforms.RequirePropertiesOnObjects && isObjectTyped(out) {
			if _, present := out["properties"]; !present {
				out["properties"] = map[string]any{}
			}
		}
		return out
	case []any:
		walked := make([]any, 0, len(v))
		for _, item := range v {
			walked = append(walked, walk(item, spec, quirks))
		}
		return walked
	default:
		return schema
	}
}

func isObjectTyped(m map[string]any) bool {
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

// pruneDanglingRequired drops required names the same object does not
// declare (#655).
func pruneDanglingRequired(out map[string]any) {
	properties, ok := out["properties"].(map[string]any)
	if !ok {
		return
	}
	defined := sortedStrings(mapKeys(properties))
	required, ok := out["required"].([]any)
	if !ok {
		return
	}
	var kept []any
	for _, name := range required {
		if s, isStr := name.(string); isStr && contains(defined, s) {
			kept = append(kept, name)
		}
	}
	if len(kept) == 0 {
		delete(out, "required")
	} else {
		out["required"] = kept
	}
}

// flattenTopLevelCombiners collapses root combiners into one object schema
// whose properties union every branch.
func flattenTopLevelCombiners(schema any) {
	output, ok := schema.(map[string]any)
	if !ok {
		return
	}
	mergedProperties := map[string]any{}
	if props, ok := output["properties"].(map[string]any); ok {
		for k, v := range props {
			mergedProperties[k] = v
		}
	}
	var allOfRequired []string
	sawCombiner := false

	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		raw, present := output[keyword]
		if !present {
			continue
		}
		delete(output, keyword)
		branches, ok := raw.([]any)
		if !ok {
			continue
		}
		sawCombiner = true
		for _, branchAny := range branches {
			branch, ok := branchAny.(map[string]any)
			if !ok {
				continue
			}
			if properties, ok := branch["properties"].(map[string]any); ok {
				for name, property := range properties {
					if _, exists := mergedProperties[name]; !exists {
						mergedProperties[name] = property
					}
				}
			}
			// Only allOf branches all apply, so only their required is safe
			// to promote.
			if keyword == "allOf" {
				if required, ok := branch["required"].([]any); ok {
					for _, nameAny := range required {
						if name, isStr := nameAny.(string); isStr && !contains(allOfRequired, name) {
							allOfRequired = append(allOfRequired, name)
						}
					}
				}
			}
		}
	}

	if !sawCombiner {
		return
	}

	output["type"] = "object"
	output["properties"] = mergedProperties
	if len(allOfRequired) > 0 {
		existing, _ := output["required"].([]any)
		for _, name := range allOfRequired {
			dup := false
			for _, item := range existing {
				if s, isStr := item.(string); isStr && s == name {
					dup = true
					break
				}
			}
			if !dup {
				existing = append(existing, name)
			}
		}
		output["required"] = existing
	}
	pruneDanglingRequired(output)
}

// mergeProperty combines two declarations of the same property name, keeping
// every key either side declares.
func mergeProperty(parent, branch map[string]any) map[string]any {
	merged := map[string]any{}
	for k, v := range branch {
		merged[k] = v
	}
	for k, v := range parent {
		if _, exists := merged[k]; !exists {
			merged[k] = v
		}
	}
	return merged
}

// flattenAllCombiners collapses every combiner, at any depth, into the schema
// that contains it: the branch is chosen (first) but its siblings are merged,
// not discarded.
func flattenAllCombiners(schema any) any {
	switch v := schema.(type) {
	case map[string]any:
		for _, combiner := range []string{"anyOf", "oneOf", "allOf"} {
			branches, ok := v[combiner].([]any)
			if !ok || len(branches) == 0 {
				continue
			}
			flattenedAny := flattenAllCombiners(branches[0])
			flattened, ok := flattenedAny.(map[string]any)
			if !ok {
				return flattenedAny
			}
			for key, value := range v {
				if key == combiner {
					continue
				}
				sibling := flattenAllCombiners(value)
				existing, present := flattened[key]
				if present {
					if existingMap, ok := existing.(map[string]any); ok {
						if siblingMap, ok := sibling.(map[string]any); ok && key == "properties" {
							for name, property := range siblingMap {
								if parent, exists := existingMap[name].(map[string]any); exists {
									if prop, isMap := property.(map[string]any); isMap {
										existingMap[name] = mergeProperty(parent, prop)
									} else {
										existingMap[name] = property
									}
								} else {
									existingMap[name] = property
								}
							}
							continue
						}
					}
					// Parent's non-property keys win.
					continue
				}
				flattened[key] = sibling
			}
			pruneDanglingRequired(flattened)
			return flattened
		}
		out := make(map[string]any, len(v))
		for k, value := range v {
			out[k] = flattenAllCombiners(value)
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, flattenAllCombiners(item))
		}
		return out
	default:
		return schema
	}
}

// mergeAllOfBranches merges each allOf branch into the enclosing object and
// drops the allOf. Only object-shaped branches contribute.
func mergeAllOfBranches(out map[string]any) {
	branchesAny, present := out["allOf"]
	if !present {
		return
	}
	delete(out, "allOf")
	branches, ok := branchesAny.([]any)
	if !ok {
		return
	}

	properties := map[string]any{}
	if props, ok := out["properties"].(map[string]any); ok {
		for k, v := range props {
			properties[k] = v
		}
	}
	var required []string
	if reqAny, ok := out["required"].([]any); ok {
		for _, name := range reqAny {
			if s, isStr := name.(string); isStr {
				required = append(required, s)
			}
		}
	}

	for _, branchAny := range branches {
		branch, ok := branchAny.(map[string]any)
		if !ok {
			continue
		}
		// Adopt the branch's type when the enclosing object declares none:
		// a typeless tool schema is rejected outright.
		if _, hasType := out["type"]; !hasType {
			if branchType, has := branch["type"]; has {
				out["type"] = branchType
			}
		}
		if branchProperties, ok := branch["properties"].(map[string]any); ok {
			for name, property := range branchProperties {
				// The enclosing object's own declaration wins: it is the more
				// specific one, and a branch usually only narrows.
				if _, exists := properties[name]; !exists {
					properties[name] = property
				}
			}
		}
		if branchRequired, ok := branch["required"].([]any); ok {
			for _, nameAny := range branchRequired {
				if name, isStr := nameAny.(string); isStr && !contains(required, name) {
					required = append(required, name)
				}
			}
		}
	}

	if len(properties) > 0 {
		out["properties"] = properties
	}
	if len(required) == 0 {
		delete(out, "required")
	} else {
		reqAny := make([]any, 0, len(required))
		for _, name := range required {
			reqAny = append(reqAny, name)
		}
		out["required"] = reqAny
	}
	pruneDanglingRequired(out)
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// NormalizeAny is a convenience wrapper for json.RawMessage inputs.
func NormalizeAny(raw json.RawMessage, spec *DialectSpec) (json.RawMessage, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return raw, err
	}
	normalized, err := marshalStable(ApplyWithQuirks(decoded, spec, &LearnedQuirks{}))
	if err != nil {
		return raw, err
	}
	return normalized, nil
}

// marshalStable encodes with sorted keys so equal schemas compare equal.
func marshalStable(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(trimLastNewline(buf.String())), nil
}

func trimLastNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}
