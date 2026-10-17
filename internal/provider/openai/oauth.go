package openai

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Codex OAuth configuration (ported from jcode-base auth::oauth openai module).
const (
	// CodexClientID is the Codex CLI OAuth client.
	CodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// CodexAuthorizeURL is the OAuth authorize endpoint.
	CodexAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	// CodexTokenURL is the OAuth token endpoint.
	CodexTokenURL = "https://auth.openai.com/oauth/token"
	// CodexDefaultPort is the local callback port.
	CodexDefaultPort = 1455
	// CodexCallbackPath is the local callback path.
	CodexCallbackPath = "/auth/callback"
	// CodexScopes is the requested scope string.
	CodexScopes = "openid profile email offline_access api.connectors.read api.connectors.invoke"
)

// CodexRedirectURI builds the local callback URL for a port.
func CodexRedirectURI(port int) string {
	return fmt.Sprintf("http://localhost:%d%s", port, CodexCallbackPath)
}

// CodexDefaultRedirectURI is the default callback URL.
func CodexDefaultRedirectURI() string { return CodexRedirectURI(CodexDefaultPort) }

// CodexCredentials carries one OpenAI auth identity. A non-empty refresh
// token or an id token means ChatGPT/Codex OAuth mode; otherwise the access
// token is a platform API key.
type CodexCredentials struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresAt    int64   `json:"expires_at,omitempty"`
	IDToken      *string `json:"id_token,omitempty"`
	AccountID    *string `json:"account_id,omitempty"`
	// LastRefresh is provider metadata, not part of the wire identity.
	LastRefresh string `json:"last_refresh,omitempty"`
}

// IsChatGPTMode reports OAuth backend usage (ported from is_chatgpt_mode).
func (c *CodexCredentials) IsChatGPTMode() bool {
	return c.RefreshToken != "" || c.IDToken != nil
}

// openaiTokenResponse is the refresh endpoint's JSON shape.
type openaiTokenResponse struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresIn    int64   `json:"expires_in"`
	IDToken      *string `json:"id_token"`
}

// RefreshOpenAITokens performs one refresh grant against tokenURL. The remote
// may rotate the refresh token; the caller decides what to persist.
func RefreshOpenAITokens(tokenURL, refreshToken string) (*CodexCredentials, error) {
	body := fmt.Sprintf("grant_type=refresh_token&client_id=%s&refresh_token=%s",
		CodexClientID, urlQueryEscape(refreshToken))
	creds, err := postOpenAIRefresh(tokenURL, body)
	if err != nil {
		return nil, err
	}
	return creds, nil
}

// urlQueryEscape matches urlencoding::encode: %20 for spaces, tilde unescaped.
func urlQueryEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "+", "%20"), "%7E", "~")
}

// ResolveAPIBase resolves the Responses API base for API-key usage
// (ported from resolve_api_base). Checked in order: GCODE_OPENAI_API_BASE,
// OPENAI_BASE_URL, OPENAI_API_BASE. A /responses suffix is trimmed so callers
// can append it without doubling.
func ResolveAPIBase() string {
	overrideVars := []string{"GCODE_OPENAI_API_BASE", "OPENAI_BASE_URL", "OPENAI_API_BASE"}
	for _, variable := range overrideVars {
		raw := os.Getenv(variable)
		if raw == "" {
			continue
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
			continue
		}
		normalized := strings.TrimSuffix(trimmed, "/")
		normalized = strings.TrimSuffix(normalized, "/responses")
		normalized = strings.TrimSuffix(normalized, "/")
		if normalized == "" {
			continue
		}
		return normalized
	}
	return OpenAIAPIBase
}

// ResponsesURL returns the /responses endpoint for the credential kind: the
// ChatGPT/Codex OAuth backend is fixed; a custom base applies only to API keys.
func ResponsesURL(credentials *CodexCredentials) string {
	base := ""
	if credentials.IsChatGPTMode() {
		base = ChatGPTAPIBase
	} else {
		base = ResolveAPIBase()
	}
	return strings.TrimSuffix(base, "/") + "/" + ResponsesPath
}
