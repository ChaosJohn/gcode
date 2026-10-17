package gemini

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Gemini auth (ported from jcode-base auth::gemini + google_oauth).

const (
	// GoogleAuthorizeURL is Google's OAuth authorize endpoint.
	GoogleAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	// GoogleTokenURL is Google's OAuth token endpoint.
	GoogleTokenURL = "https://oauth2.googleapis.com/token"
	// GoogleUserinfoURL is Google's userinfo endpoint.
	GoogleUserinfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
	// GeminiManualRedirectURI is the manual-flow redirect target.
	GeminiManualRedirectURI = "https://codeassist.google.com/authcode"
	// GeminiCLIAuthSourceID marks CLI-imported credentials.
	GeminiCLIAuthSourceID = "gemini_cli_oauth_creds"

	// GeminiClientID is Google's official Gemini CLI desktop-app client. The
	// secret is safe to embed for installed-app OAuth.
	GeminiClientID = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	// GeminiClientSecret pairs with the desktop-app client id.
	GeminiClientSecret = "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"

	// GeminiAPIKeyEnvFile persists a saved Developer API key.
	GeminiAPIKeyEnvFile = "gemini.env"
)

// GeminiAPIKeyEnvVars are checked in order for a Developer API key.
var GeminiAPIKeyEnvVars = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}

// GeminiScopes is the Code Assist OAuth scope set.
var GeminiScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

// GeminiTokens is the stored OAuth identity.
type GeminiTokens struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresAt    int64   `json:"expires_at"`
	Email        *string `json:"email,omitempty"`
}

// IsExpired mirrors the 60s early-refresh window.
func (t *GeminiTokens) IsExpired() bool {
	return t.ExpiresAt <= time.Now().UnixMilli()+60_000
}

func gcodeDir() string {
	if home := os.Getenv("GCODE_HOME"); home != "" {
		return home
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ".gcode"
	}
	return filepath.Join(userHome, ".gcode")
}

// TokensPath is the native token store under the gcode dir.
func TokensPath() string { return filepath.Join(gcodeDir(), "gemini_oauth.json") }

// GeminiCLIOAuthPath is the official Gemini CLI credential file.
func GeminiCLIOAuthPath() string {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".gemini", "oauth_creds.json")
	}
	return filepath.Join(userHome, ".gemini", "oauth_creds.json")
}

// APIKeyEnvFile returns the gemini.env path under the gcode dir.
func APIKeyEnvFile() string { return filepath.Join(gcodeDir(), GeminiAPIKeyEnvFile) }

// APIKey resolves a Developer API key from the env or gemini.env.
func APIKey() (string, bool) {
	for _, envVar := range GeminiAPIKeyEnvVars {
		if value := strings.TrimSpace(os.Getenv(envVar)); value != "" {
			return value, true
		}
		// gemini.env is a KEY=VALUE file persisted next to the other config.
		if value := readEnvFileValue(APIKeyEnvFile(), envVar); value != "" {
			return value, true
		}
	}
	return "", false
}

// AuthSource supplies the provider config the auth route depends on. The app
// injects the loaded config; the default reads GCODE_* env overrides so the
// provider stays usable standalone (tests, doctor).
type AuthSource interface {
	// ForceOAuth pins Code Assist OAuth even when an API key exists
	// (provider.gemini_force_oauth / JCODE_GEMINI_FORCE_OAUTH).
	ForceOAuth() bool
	// CloudProject is provider.gemini_project / GOOGLE_CLOUD_PROJECT.
	CloudProject() string
}

var authSource AuthSource = envAuthSource{}

// SetAuthSource wires the app config in.
func SetAuthSource(source AuthSource) { authSource = source }

type envAuthSource struct{}

func (envAuthSource) ForceOAuth() bool {
	value := strings.TrimSpace(os.Getenv("GCODE_GEMINI_FORCE_OAUTH"))
	return value == "1" || strings.EqualFold(value, "true")
}

func (envAuthSource) CloudProject() string {
	for _, variable := range []string{"GCODE_GEMINI_PROJECT", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_PROJECT_ID"} {
		if value := strings.TrimSpace(os.Getenv(variable)); value != "" {
			return value
		}
	}
	return ""
}

// ForceOAuth reports the provider.gemini_force_oauth setting.
func ForceOAuth() bool { return authSource.ForceOAuth() }

// CloudProject returns the configured Code Assist project.
func CloudProject() string {
	if project := authSource.CloudProject(); project != "" {
		return project
	}
	return GoogleCloudProjectFromEnv()
}

// UsesAPIKey reports which route the gemini provider will take.
func UsesAPIKey() bool { return !ForceOAuth() && hasAPIKey() }

func hasAPIKey() bool {
	_, ok := APIKey()
	return ok
}

func readEnvFileValue(path, key string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(name) != key {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// SaveAPIKey persists a Developer API key under the canonical name.
func SaveAPIKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errString("Gemini API key cannot be empty")
	}
	path := APIKeyEnvFile()
	content := ""
	if existing, err := os.ReadFile(path); err == nil {
		content = string(existing)
	}
	lines := strings.Split(content, "\n")
	replaced := false
	for i, line := range lines {
		name, _, found := strings.Cut(strings.TrimSpace(line), "=")
		if found && strings.TrimSpace(name) == GeminiAPIKeyEnvVars[0] {
			lines[i] = GeminiAPIKeyEnvVars[0] + "=" + key
			replaced = true
			break
		}
	}
	if !replaced {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		lines = append(lines, GeminiAPIKeyEnvVars[0]+"="+key)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600)
}

// LoadTokens reads the native store, falling back to the Gemini CLI's OAuth
// credentials when present.
func LoadTokens() (*GeminiTokens, error) {
	if content, err := os.ReadFile(TokensPath()); err == nil {
		var tokens GeminiTokens
		if err := json.Unmarshal(content, &tokens); err != nil {
			return nil, fmt.Errorf("Failed to read %s: %w", TokensPath(), err)
		}
		return &tokens, nil
	}
	cliPath := GeminiCLIOAuthPath()
	if content, err := os.ReadFile(cliPath); err == nil {
		var imported struct {
			AccessToken  string  `json:"access_token"`
			RefreshToken *string `json:"refresh_token"`
			IDToken      *string `json:"id_token"`
			ExpiryDate   *string `json:"expiry_date"`
		}
		if err := json.Unmarshal(content, &imported); err != nil {
			return nil, fmt.Errorf("Failed to parse %s: %w", cliPath, err)
		}
		if imported.RefreshToken == nil || strings.TrimSpace(*imported.RefreshToken) == "" {
			return nil, errString("Gemini CLI credentials carry no refresh token")
		}
		tokens := GeminiTokens{
			AccessToken:  imported.AccessToken,
			RefreshToken: strings.TrimSpace(*imported.RefreshToken),
		}
		if imported.ExpiryDate != nil {
			if ms, ok := parseExpiry(*imported.ExpiryDate); ok {
				tokens.ExpiresAt = ms
			}
		}
		return &tokens, nil
	}
	return nil, errString("Gemini OAuth credentials are not configured; run the login flow first")
}

func parseExpiry(raw string) (int64, bool) {
	// The CLI writes epoch milliseconds as a number or string.
	trimmed := strings.TrimSpace(strings.Trim(raw, `"`))
	var ms int64
	if _, err := fmt.Sscanf(trimmed, "%d", &ms); err != nil {
		return 0, false
	}
	return ms, true
}

// SaveTokens persists refreshed credentials.
func SaveTokens(tokens *GeminiTokens) error {
	if err := os.MkdirAll(filepath.Dir(TokensPath()), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(TokensPath(), encoded, 0o600)
}

// LoadOrRefreshTokens returns unexpired credentials, refreshing once if needed.
func LoadOrRefreshTokens() (*GeminiTokens, error) {
	tokens, err := LoadTokens()
	if err != nil {
		return nil, err
	}
	if !tokens.IsExpired() {
		return tokens, nil
	}
	return RefreshTokens(tokens)
}

var refreshMu sync.Mutex

// RefreshTokens performs the refresh grant and persists the result.
func RefreshTokens(tokens *GeminiTokens) (*GeminiTokens, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	// Re-read: a concurrent caller may have refreshed already.
	if stored, err := LoadTokens(); err == nil && !stored.IsExpired() {
		return stored, nil
	}
	refreshed, err := RefreshGoogleAccessToken("Gemini", geminiClientID(), geminiClientSecret(), tokens.RefreshToken)
	if err != nil {
		return nil, err
	}
	updated := GeminiTokens{
		AccessToken:  refreshed.AccessToken,
		RefreshToken: refreshed.RefreshToken,
		ExpiresAt:    refreshed.ExpiresAtMS,
		Email:        tokens.Email,
	}
	if err := SaveTokens(&updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

func geminiClientID() string {
	if value := strings.TrimSpace(os.Getenv("GEMINI_CLIENT_ID")); value != "" {
		return value
	}
	return GeminiClientID
}

func geminiClientSecret() string {
	if value := strings.TrimSpace(os.Getenv("GEMINI_CLIENT_SECRET")); value != "" {
		return value
	}
	return GeminiClientSecret
}

// RefreshedGoogleToken is the refresh grant result.
type RefreshedGoogleToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresAtMS  int64
}

// RefreshGoogleAccessToken performs the Google refresh grant (ported from
// google_oauth::refresh_access_token). A missing refresh_token in the
// response keeps the previous one.
func RefreshGoogleAccessToken(label, clientID, clientSecret, refreshToken string) (*RefreshedGoogleToken, error) {
	form := strings.NewReader(fmt.Sprintf(
		"grant_type=refresh_token&client_id=%s&client_secret=%s&refresh_token=%s",
		urlQueryEscape(clientID), urlQueryEscape(clientSecret), urlQueryEscape(refreshToken)))
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(GoogleTokenURL, "application/x-www-form-urlencoded", form)
	if err != nil {
		return nil, fmt.Errorf("Failed to refresh %s OAuth token: %w", label, err)
	}
	defer resp.Body.Close()
	raw, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Failed to refresh %s OAuth token: %w", label, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s token refresh failed: %s", label, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken *string `json:"refresh_token"`
		ExpiresIn    int64   `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("Failed to parse %s refresh response: %w", label, err)
	}
	newRefresh := refreshToken
	if parsed.RefreshToken != nil && *parsed.RefreshToken != "" {
		newRefresh = *parsed.RefreshToken
	}
	return &RefreshedGoogleToken{
		AccessToken:  parsed.AccessToken,
		RefreshToken: newRefresh,
		ExpiresAtMS:  time.Now().UnixMilli() + parsed.ExpiresIn*1000,
	}, nil
}
