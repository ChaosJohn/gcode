// Account pool (ported from jcode-base/src/auth/account_pool.rs, core):
// rotation over multi-account OAuth logins. gcode reads the jcode auth
// store read-only; the pool file lives in gcode's own home. The rotation
// applies at session start (upstream semantics): the next member in order
// becomes the credential source for new sessions.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AccountPool is the persisted rotation config.
type AccountPool struct {
	// Members are the auto-switch account keys in rotation order.
	Members []string `json:"members"`
	// Excluded are accounts the user kept out of rotation.
	Excluded []string `json:"excluded,omitempty"`
}

// PoolPath is the pool file under the gcode home (GCODE_HOME respected).
func PoolPath() string {
	home := os.Getenv("GCODE_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".gcode")
	}
	return filepath.Join(home, "account-pool.json")
}

// LoadPool reads the pool file (empty pool when absent).
func LoadPool() AccountPool {
	raw, err := os.ReadFile(PoolPath())
	if err != nil {
		return AccountPool{}
	}
	var pool AccountPool
	_ = json.Unmarshal(raw, &pool)
	return pool
}

// SavePool persists the pool file (0600).
func SavePool(pool AccountPool) error {
	home := filepath.Dir(PoolPath())
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(pool, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PoolPath(), encoded, 0o600)
}

// AccountKey is the stable pool key: provider[:label].
func AccountKey(provider, label string) string {
	if label != "" {
		return provider + ":" + label
	}
	return provider
}

// OAuthLogin is one stored login without token material.
type OAuthLogin struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Email    string `json:"email,omitempty"`
	Active   bool   `json:"active"`
	Expires  int64  `json:"expires_millis,omitempty"`
}

// jcodeAuthAccounts mirrors the jcode auth.json anthropic_accounts table
// (read-only interop with the user's jcode home).
type jcodeAuthAccounts struct {
	ActiveAnthropicAccount string `json:"active_anthropic_account"`
	AnthropicAccounts      []struct {
		Label            string `json:"label"`
		Email            string `json:"email"`
		Expires          int64  `json:"expires"`
		SubscriptionType string `json:"subscription_type"`
	} `json:"anthropic_accounts"`
}

func jcodeAuthPath() string {
	if home := os.Getenv("JCODE_HOME"); home != "" {
		return filepath.Join(home, "auth.json")
	}
	return filepath.Join(os.Getenv("HOME"), ".jcode", "auth.json")
}

// OAuthLogins lists every stored login on the machine in stored order:
// codex (single account, label "codex") then the jcode Claude accounts.
func OAuthLogins() []OAuthLogin {
	logins := []OAuthLogin{}
	// Codex: one login per auth.json.
	if raw, err := os.ReadFile(codexAuthPath()); err == nil {
		var auth struct {
			Tokens struct {
				AccountID string `json:"account_id"`
			} `json:"tokens"`
			LastRefresh string `json:"last_refresh"`
		}
		if json.Unmarshal(raw, &auth) == nil && auth.Tokens.AccountID != "" {
			logins = append(logins, OAuthLogin{Provider: "openai",
				Label: "codex", Email: auth.Tokens.AccountID, Active: true})
		}
	}
	raw, err := os.ReadFile(jcodeAuthPath())
	if err != nil {
		return logins
	}
	var auth jcodeAuthAccounts
	if json.Unmarshal(raw, &auth) != nil {
		return logins
	}
	for _, account := range auth.AnthropicAccounts {
		logins = append(logins, OAuthLogin{Provider: "claude",
			Label: account.Label, Email: account.Email,
			Active:  account.Label == auth.ActiveAnthropicAccount,
			Expires: account.Expires})
	}
	return logins
}

func codexAuthPath() string {
	return filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
}

// RotationState is the persisted rotation cursor (millis epoch of the last
// switch, plus the key that consumed it).
type RotationState struct {
	LastKey string `json:"last_key"`
	Switch  int64  `json:"switch_millis"`
}

func rotationPath() string {
	home := os.Getenv("GCODE_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".gcode")
	}
	return filepath.Join(home, "account-rotation.json")
}

// NextMember ports rotation semantics: the pool member after the last used
// one. With no prior state this is the first member. Returns "" when the
// pool has no members.
func NextMember(pool AccountPool) string {
	if len(pool.Members) == 0 {
		return ""
	}
	raw, err := os.ReadFile(rotationPath())
	if err == nil {
		var state RotationState
		if json.Unmarshal(raw, &state) == nil && state.LastKey != "" {
			for i, member := range pool.Members {
				if member == state.LastKey {
					return pool.Members[(i+1)%len(pool.Members)]
				}
			}
		}
	}
	return pool.Members[0]
}

// RecordRotation persists the rotation cursor after a switch.
func RecordRotation(key string) error {
	state := RotationState{LastKey: key, Switch: time.Now().UnixMilli()}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(rotationPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(rotationPath(), encoded, 0o600)
}

// IsRateLimitError classifies rate-limit/429 responses for auto-rotation
// (ported from the TUI rate-limit parse helpers' trigger condition).
func IsRateLimitError(message string) bool {
	lowered := strings.ToLower(message)
	for _, needle := range []string{
		"429", "rate limit", "rate_limit", "ratelimit", "too many requests",
	} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}

// Describe renders one login line for the accounts listing.
func Describe(login OAuthLogin, pool AccountPool) string {
	key := AccountKey(login.Provider, login.Label)
	marker := " "
	for _, member := range pool.Members {
		if member == key {
			marker = "*"
		}
	}
	for _, excluded := range pool.Excluded {
		if excluded == key {
			marker = "-"
		}
	}
	active := ""
	if login.Active {
		active = " [active]"
	}
	email := login.Email
	if email == "" {
		email = "(no email)"
	}
	expiry := ""
	if login.Expires > 0 {
		expiry = fmt.Sprintf(" expires %s", time.UnixMilli(login.Expires).Format("2006-01-02"))
	}
	return fmt.Sprintf("%s %s/%s %s%s%s", marker, login.Provider, login.Label,
		email, active, expiry)
}

// WireRotation installs the pool rotation into the anthropic credential
// chain (called at app assembly). Imported lazily by the host to avoid a
// package cycle.
var WireRotation = func() {}
