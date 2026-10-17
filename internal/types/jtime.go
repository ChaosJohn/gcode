package types

import (
	"encoding/json"
	"fmt"
	"time"
)

// JTime mirrors chrono::DateTime<Utc> as jcode serializes it: RFC 3339 in UTC
// with a "Z" suffix and "AutoSi" fractional seconds — no fraction when the
// nanosecond part is zero, otherwise 3, 6, or 9 digits as precision allows
// (e.g. "2026-08-26T07:49:52.870523Z", exactly as seen in real session files).
type JTime struct{ time.Time }

// Compile-time assertions that JTime satisfies the JSON interfaces.
var (
	_ json.Marshaler   = JTime{}
	_ json.Unmarshaler = &JTime{}
)

// MarshalJSON renders the chrono-compatible RFC 3339 form.
func (t JTime) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte(`null`), nil
	}
	return []byte(`"` + FormatAutoSi(t.Time) + `"`), nil
}

// UnmarshalJSON accepts any RFC 3339 timestamp (with "Z" or an explicit
// offset, with or without a fractional part) and normalizes to UTC.
func (t *JTime) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		t.Time = time.Time{}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("jcode timestamp must be a string: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return fmt.Errorf("invalid jcode timestamp %q: %w", s, err)
	}
	t.Time = parsed.UTC()
	return nil
}

// FormatAutoSi renders t in UTC the way chrono's serde does:
// to_rfc3339_opts(SecondsFormat::AutoSi, true).
func FormatAutoSi(t time.Time) string {
	u := t.UTC()
	nanos := u.Nanosecond()
	var frac string
	switch {
	case nanos == 0:
	case nanos%1_000_000 == 0:
		frac = fmt.Sprintf(".%03d", nanos/1_000_000)
	case nanos%1_000 == 0:
		frac = fmt.Sprintf(".%06d", nanos/1_000)
	default:
		frac = fmt.Sprintf(".%09d", nanos)
	}
	return u.Format("2006-01-02T15:04:05") + frac + "Z"
}

// Now returns the current instant as JTime.
func Now() JTime { return JTime{time.Now().UTC()} }
