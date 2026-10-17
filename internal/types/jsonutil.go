package types

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// jsonBuilder assembles a JSON object with a fixed key order (Go maps
// randomize order; serde emits fields in declaration order and byte parity
// matters for stable hashes and fixture round-trips).
type jsonBuilder struct{ parts []string }

func (b *jsonBuilder) rawKey(key, rawJSON string) {
	b.parts = append(b.parts, `"`+key+`":`+rawJSON)
}

func (b *jsonBuilder) strKey(key, value string) {
	encoded, _ := MarshalCompact(value)
	b.parts = append(b.parts, `"`+key+`":`+string(encoded))
}

func (b *jsonBuilder) bytes() []byte {
	return []byte("{" + strings.Join(b.parts, ",") + "}")
}

func jsonStringPtr(s *string) string {
	if s == nil {
		return "null"
	}
	out, _ := MarshalCompact(*s)
	return string(out)
}

// rawOrNull renders a raw JSON value, mapping empty to null (a Go zero
// json.RawMessage must serialize like serde_json::Value::Null).
func rawOrNull(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	return string(raw)
}

func jsonUintPtr(v *uint64) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatUint(*v, 10)
}

// MarshalCompact encodes v without HTML escaping, matching serde_json's
// output byte-for-byte (encoding/json escapes <, >, & by default; jcode's
// persisted JSON does not). Trailing newline added by the encoder is trimmed.
//
// All gcode persistence paths and hash inputs must go through this instead
// of json.Marshal.
func MarshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	return bytes.TrimRight(out, "\n"), nil
}
