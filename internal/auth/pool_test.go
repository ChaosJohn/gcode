// Account pool tests: rotation cursor, login listing from a jcode store,
// markers, and the rate-limit classifier.
package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNextMemberRotation(t *testing.T) {
	t.Setenv("GCODE_HOME", t.TempDir())
	pool := AccountPool{Members: []string{"claude:work", "claude:personal", "openai"}}
	// No cursor: first member.
	if got := NextMember(pool); got != "claude:work" {
		t.Fatalf("first: %q", got)
	}
	if err := RecordRotation("claude:work"); err != nil {
		t.Fatal(err)
	}
	if got := NextMember(pool); got != "claude:personal" {
		t.Fatalf("second: %q", got)
	}
	_ = RecordRotation("claude:personal")
	if got := NextMember(pool); got != "openai" {
		t.Fatalf("third: %q", got)
	}
	_ = RecordRotation("openai")
	if got := NextMember(pool); got != "claude:work" {
		t.Fatalf("wrap: %q", got)
	}
	// Unknown last key restarts from the head.
	_ = RecordRotation("claude:ghost")
	if got := NextMember(pool); got != "claude:work" {
		t.Fatalf("ghost: %q", got)
	}
	// Empty pool yields "".
	if got := NextMember(AccountPool{}); got != "" {
		t.Fatalf("empty: %q", got)
	}
	// Pool file round-trips.
	if err := SavePool(pool); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(PoolPath())
	if err != nil {
		t.Fatal(err)
	}
	var parsed AccountPool
	_ = json.Unmarshal(raw, &parsed)
	if len(parsed.Members) != 3 {
		t.Fatalf("round-trip: %+v", parsed)
	}
}

func TestOAuthLoginsFromJcodeStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // isolate the real ~/.codex/auth.json
	t.Setenv("JCODE_HOME", home)
	_ = os.MkdirAll(home, 0o755)
	authFile := map[string]any{
		"active_anthropic_account": "work",
		"anthropic_accounts": []map[string]any{
			{"label": "work", "email": "work@example.com", "expires": 1900000000000},
			{"label": "personal", "email": "personal@example.com"},
		},
	}
	encoded, _ := json.Marshal(authFile)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	logins := OAuthLogins()
	if len(logins) != 2 {
		t.Fatalf("logins: %+v", logins)
	}
	if logins[0].Label != "work" || !logins[0].Active || logins[0].Email != "work@example.com" {
		t.Fatalf("work: %+v", logins[0])
	}
	if logins[1].Active {
		t.Fatalf("personal should be inactive: %+v", logins[1])
	}
	// Markers: pool member star, excluded dash.
	pool := AccountPool{Members: []string{"claude:work"}, Excluded: []string{"claude:personal"}}
	work := Describe(logins[0], pool)
	if !strings.HasPrefix(work, "* claude/work") || !strings.Contains(work, "[active]") {
		t.Fatalf("describe work: %q", work)
	}
	personal := Describe(logins[1], pool)
	if !strings.HasPrefix(personal, "- claude/personal") {
		t.Fatalf("describe personal: %q", personal)
	}
}

func TestIsRateLimitError(t *testing.T) {
	cases := map[string]bool{
		"HTTP 429: too many requests": true,
		"rate limit exceeded":         true,
		"RateLimitError":              true,
		"connection refused":          false,
		"context length exceeded":     false,
	}
	for message, want := range cases {
		if IsRateLimitError(message) != want {
			t.Fatalf("%q: want %v", message, want)
		}
	}
}
