package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude OAuth credentials (ported from jcode-base auth/claude.rs + oauth.rs).
//
// Token sources, in priority order:
//  1. ~/.claude/.credentials.json (Claude Code native store)
//  2. ~/.claude.json's oauthAccount (legacy inline storage)
//  3. ~/.jcode/auth.json multi-account store
//  4. CLAUDE_CODE_OAUTH_TOKEN environment variable

// OAuth endpoints (oauth.rs mod claude).
const (
	ClaudeOAuthClientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	ClaudeOAuthTokenURL    = "https://platform.claude.com/v1/oauth/token"
	ClaudeOAuthScopes      = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	ClaudeOAuthRefreshHost = "claude.ai"
)

// ClaudeCredentials is the resolved credential set.
type ClaudeCredentials struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAtMillis  int64
	SubscriptionType string
	AccountLabel     string
}

// Expired reports token expiry (60s safety margin like the runtime's probe).
func (c ClaudeCredentials) Expired() bool {
	return c.ExpiresAtMillis <= time.Now().UnixMilli()+60_000
}

func userHomePath(rel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rel), nil
}

// claudeCodeNativeCredentials parses ~/.claude/.credentials.json.
func claudeCodeNativeCredentials() (ClaudeCredentials, bool) {
	path, err := userHomePath(".claude/.credentials.json")
	if err != nil {
		return ClaudeCredentials{}, false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return ClaudeCredentials{}, false
	}
	var parsed struct {
		ClaudeAiOauth struct {
			AccessToken      string `json:"accessToken"`
			RefreshToken     string `json:"refreshToken"`
			ExpiresAt        int64  `json:"expiresAt"`
			Scopes           []string
			SubscriptionType string `json:"subscriptionType"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(content, &parsed) != nil || parsed.ClaudeAiOauth.AccessToken == "" {
		return ClaudeCredentials{}, false
	}
	return ClaudeCredentials{
		AccessToken:      parsed.ClaudeAiOauth.AccessToken,
		RefreshToken:     parsed.ClaudeAiOauth.RefreshToken,
		ExpiresAtMillis:  parsed.ClaudeAiOauth.ExpiresAt,
		SubscriptionType: parsed.ClaudeAiOauth.SubscriptionType,
	}, true
}

// jcodeAuthFile mirrors jcode's own auth.json (multi-account, with the legacy
// single-account `{"anthropic": {...}}` layout still readable).
type jcodeAuthFile struct {
	AnthropicAccounts []struct {
		Label            string `json:"label"`
		Access           string `json:"access"`
		Refresh          string `json:"refresh"`
		Expires          int64  `json:"expires"`
		Email            string `json:"email"`
		SubscriptionType string `json:"subscription_type"`
	} `json:"anthropic_accounts"`
	ActiveAnthropicAccount string `json:"active_anthropic_account"`
	Anthropic              *struct {
		Access  string `json:"access"`
		Refresh string `json:"refresh"`
		Expires int64  `json:"expires"`
	} `json:"anthropic"`
}

func loadJcodeAuthFile() (jcodeAuthFile, bool) {
	path, err := userHomePath(".jcode/auth.json")
	if err != nil {
		return jcodeAuthFile{}, false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return jcodeAuthFile{}, false
	}
	var auth jcodeAuthFile
	if json.Unmarshal(content, &auth) != nil {
		return jcodeAuthFile{}, false
	}
	return auth, true
}

// jcodeCredentials resolves the active jcode-store account.
func jcodeCredentials() (ClaudeCredentials, bool) {
	auth, ok := loadJcodeAuthFile()
	if !ok {
		return ClaudeCredentials{}, false
	}
	if len(auth.AnthropicAccounts) > 0 {
		label := auth.ActiveAnthropicAccount
		if label == "" {
			label = auth.AnthropicAccounts[0].Label
		}
		// Account-pool rotation (ported account_for_route semantics): when
		// a pool with members exists, new sessions take the next member.
		if member := NextPoolMemberHook(); member != "" {
			label = strings.TrimPrefix(member, "claude:")
		}
		for _, account := range auth.AnthropicAccounts {
			if account.Label == label && account.Access != "" {
				return ClaudeCredentials{
					AccessToken: account.Access, RefreshToken: account.Refresh,
					ExpiresAtMillis: account.Expires, SubscriptionType: account.SubscriptionType,
					AccountLabel: account.Label,
				}, true
			}
		}
		// Fall back to the first account with a token.
		for _, account := range auth.AnthropicAccounts {
			if account.Access != "" {
				return ClaudeCredentials{
					AccessToken: account.Access, RefreshToken: account.Refresh,
					ExpiresAtMillis: account.Expires, SubscriptionType: account.SubscriptionType,
					AccountLabel: account.Label,
				}, true
			}
		}
		return ClaudeCredentials{}, false
	}
	if auth.Anthropic != nil && auth.Anthropic.Access != "" {
		return ClaudeCredentials{
			AccessToken: auth.Anthropic.Access, RefreshToken: auth.Anthropic.Refresh,
			ExpiresAtMillis: auth.Anthropic.Expires, AccountLabel: "claude",
		}, true
	}
	return ClaudeCredentials{}, false
}

// claudeCodeEnvCredentials reads CLAUDE_CODE_OAUTH_TOKEN (never expires from
// the env's perspective; treated as long-lived).
func claudeCodeEnvCredentials() (ClaudeCredentials, bool) {
	token := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"))
	if token == "" {
		return ClaudeCredentials{}, false
	}
	return ClaudeCredentials{
		AccessToken:     token,
		ExpiresAtMillis: time.Now().Add(365 * 24 * time.Hour).UnixMilli(),
		AccountLabel:    "claude-native",
	}, true
}

// LoadCredentials resolves Claude OAuth credentials from the known stores.
func LoadCredentials() (ClaudeCredentials, error) {
	now := time.Now().UnixMilli()
	if creds, ok := claudeCodeNativeCredentials(); ok {
		if creds.ExpiresAtMillis > now {
			return creds, nil
		}
	}
	if creds, ok := claudeCodeEnvCredentials(); ok && creds.ExpiresAtMillis > now {
		return creds, nil
	}
	if creds, ok := jcodeCredentials(); ok {
		if creds.ExpiresAtMillis > now {
			return creds, nil
		}
		// Expired stored token with a refresh token: still useful, the caller
		// refreshes through RefreshCredentials.
		if creds.RefreshToken != "" {
			return creds, nil
		}
	}
	return ClaudeCredentials{}, fmt.Errorf("no Claude OAuth credentials found; run `gcode login --provider claude` or import from Claude Code")
}

// ClaudeTokenRefresher is the refresh result.
type ClaudeTokenRefresher struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

// claudeRefreshResponse is the token endpoint payload.
type claudeRefreshResponse struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken *string `json:"refresh_token"`
	ExpiresIn    int64   `json:"expires_in"`
	Scope        *string `json:"scope"`
}

// claudeRefreshError mirrors the endpoint's error envelope.
type claudeRefreshError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// RefreshClaudeTokens exchanges a refresh token at the OAuth token endpoint.
// Claude Code scopes are requested first; legacy tokens that reject them are
// retried once without an explicit scope.
func RefreshClaudeTokens(ctx context.Context, refreshToken string) (ClaudeTokenRefresher, error) {
	tokens, scopedErr := sendClaudeRefreshRequest(ctx, refreshToken, ClaudeOAuthRefreshScopes())
	if scopedErr != nil && claudeRefreshErrorIsInvalidScope(scopedErr) {
		retried, fallbackErr := sendClaudeRefreshRequest(ctx, refreshToken, "")
		if fallbackErr != nil {
			return ClaudeTokenRefresher{}, fmt.Errorf(
				"claude token refresh fallback without scope failed: %v; scoped refresh error: %v",
				fallbackErr, scopedErr)
		}
		tokens = retried
	} else if scopedErr != nil {
		return ClaudeTokenRefresher{}, scopedErr
	}
	return ClaudeTokenRefresher{
		AccessToken:  tokens.AccessToken,
		RefreshToken: orDefault(tokens.RefreshToken, refreshToken),
		ExpiresAt:    time.Now().UnixMilli() + tokens.ExpiresIn*1000,
	}, nil
}

// ClaudeOAuthRefreshScopes is the refresh-time scope list (no org key minting).
func ClaudeOAuthRefreshScopes() string {
	return "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
}

func orDefault(s *string, fallback string) string {
	if s != nil && *s != "" {
		return *s
	}
	return fallback
}

func newRequestWithTimeout(ctx context.Context, url string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	return req, nil
}

func sendClaudeRefreshRequest(ctx context.Context, refreshToken, scope string) (claudeRefreshResponse, error) {
	payload := map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     ClaudeOAuthClientID,
	}
	if scope != "" {
		payload["scope"] = scope
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return claudeRefreshResponse{}, err
	}
	req, err := newRequestWithTimeout(ctx, ClaudeOAuthTokenURL, body)
	if err != nil {
		return claudeRefreshResponse{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return claudeRefreshResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return claudeRefreshResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var refreshErr claudeRefreshError
		_ = json.Unmarshal(raw, &refreshErr)
		if refreshErr.Error != "" {
			return claudeRefreshResponse{}, fmt.Errorf("oauth refresh error %s: %s",
				refreshErr.Error, refreshErr.ErrorDescription)
		}
		return claudeRefreshResponse{}, fmt.Errorf("token refresh failed: HTTP %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var parsed claudeRefreshResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return claudeRefreshResponse{}, fmt.Errorf("token refresh response was not valid JSON: %w", err)
	}
	if parsed.AccessToken == "" {
		return claudeRefreshResponse{}, fmt.Errorf("token refresh response carried no access_token")
	}
	return parsed, nil
}

func claudeRefreshErrorIsInvalidScope(err error) bool {
	return strings.Contains(err.Error(), "invalid_scope") ||
		strings.Contains(err.Error(), "does not meet scope requirement")
}

// NextPoolMemberHook lets the host inject account-pool rotation without an
// import cycle (the pool lives in internal/auth). Returns an account key
// like "claude:work" or "".
var NextPoolMemberHook = func() string { return "" }
