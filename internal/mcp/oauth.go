// Remote MCP OAuth (ported from jcode-base/src/mcp/oauth.rs, storage +
// discovery + refresh paths): tokens live in ~/.gcode/mcp-auth.json keyed
// by server URL. Discovery follows RFC 9728 protected-resource metadata to
// an RFC 8414 authorization-server document; refresh uses the stored
// refresh token. The interactive authorization-code + PKCE flow lives in
// AuthorizationURL/CompleteAuthorization for the login command.
package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StoredToken is one server's OAuth state.
type StoredToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	ServerURL    string    `json:"server_url"`
}

// AuthHome is the token-store directory (GCODE_HOME or ~/.gcode).
func AuthHome() string {
	if home := os.Getenv("GCODE_HOME"); home != "" {
		return home
	}
	return filepath.Join(os.Getenv("HOME"), ".gcode")
}

func authStorePath() string { return filepath.Join(AuthHome(), "mcp-auth.json") }

// LoadAuthStore reads every stored server token.
func LoadAuthStore() map[string]StoredToken {
	out := map[string]StoredToken{}
	raw, err := os.ReadFile(authStorePath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// SaveAuthStore persists the token store (0600 — bearer secrets).
func SaveAuthStore(store map[string]StoredToken) error {
	if err := os.MkdirAll(AuthHome(), 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(authStorePath(), encoded, 0o600)
}

// StoreToken upserts one server's token.
func StoreToken(token StoredToken) error {
	store := LoadAuthStore()
	store[token.ServerURL] = token
	return SaveAuthStore(store)
}

// storedAccessToken returns a valid cached bearer for the server URL,
// refreshing via the stored refresh token when close to expiry. Empty
// string means "no token; try anonymous".
func storedAccessToken(serverURL string) string {
	store := LoadAuthStore()
	token, ok := store[serverURL]
	if !ok || token.AccessToken == "" {
		return ""
	}
	if time.Now().Add(60 * time.Second).Before(token.ExpiresAt) {
		return token.AccessToken
	}
	if token.RefreshToken == "" {
		return ""
	}
	refreshed, err := refreshAccessToken(token)
	if err != nil {
		return ""
	}
	_ = StoreToken(refreshed)
	return refreshed.AccessToken
}

// AuthorizationServerMetadata is the RFC 8414 document subset used here.
type AuthorizationServerMetadata struct {
	Issuer                string   `json:"issuer"`
	TokenEndpoint         string   `json:"token_endpoint"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	GrantTypesSupported   []string `json:"grant_types_supported,omitempty"`
}

// DiscoverAuthorizationServer ports the discovery chain: the server URL's
// RFC 9728 protected-resource metadata names the issuer; the issuer's
// RFC 8414 well-known document carries the endpoints. Falls back to using
// the server origin itself as the issuer (common single-server setups).
func DiscoverAuthorizationServer(serverURL string) (*AuthorizationServerMetadata, error) {
	issuer := discoverProtectedResourceIssuer(serverURL)
	if issuer == "" {
		parsed, err := urlOrigin(serverURL)
		if err != nil {
			return nil, err
		}
		issuer = parsed
	}
	for _, wellKnown := range []string{
		"/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration",
	} {
		metadata, err := fetchAuthorizationMetadata(issuer + wellKnown)
		if err == nil && metadata.TokenEndpoint != "" {
			return metadata, nil
		}
	}
	return nil, fmt.Errorf("no authorization server metadata found for %s", serverURL)
}

// discoverProtectedResourceIssuer follows RFC 9728 path-aware then
// origin-level well-known lookups.
func discoverProtectedResourceIssuer(serverURL string) string {
	parsed, err := urlOrigin(serverURL)
	if err != nil {
		return ""
	}
	base := strings.TrimSuffix(serverURL, "/")
	candidates := []string{
		base[:strings.LastIndex(base, "/")] + "/.well-known/oauth-protected-resource",
		parsed + "/.well-known/oauth-protected-resource",
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for _, candidate := range candidates {
		response, err := client.Get(candidate)
		if err != nil {
			continue
		}
		var payload struct {
			AuthorizationServer string `json:"authorization_server"`
		}
		err = json.NewDecoder(response.Body).Decode(&payload)
		response.Body.Close()
		if err == nil && payload.AuthorizationServer != "" {
			return payload.AuthorizationServer
		}
	}
	return ""
}

func fetchAuthorizationMetadata(endpoint string) (*AuthorizationServerMetadata, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("metadata endpoint %s: HTTP %s", endpoint, response.Status)
	}
	var metadata AuthorizationServerMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		return nil, err
	}
	return &metadata, nil
}

func urlOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	origin := parsed.Scheme + "://" + parsed.Host
	return origin, nil
}

// RefreshAccessToken exchanges a refresh token at the token endpoint
// (grant_type=refresh_token) and returns updated storage.
func refreshAccessToken(token StoredToken) (StoredToken, error) {
	metadata, err := DiscoverAuthorizationServer(token.ServerURL)
	if err != nil {
		return StoredToken{}, err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {token.RefreshToken},
		"client_id":     {token.ClientID},
	}.Encode()
	request, err := http.NewRequest(http.MethodPost, metadata.TokenEndpoint,
		strings.NewReader(form))
	if err != nil {
		return StoredToken{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token.ClientSecret != "" {
		request.SetBasicAuth(token.ClientID, token.ClientSecret)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return StoredToken{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return StoredToken{}, fmt.Errorf("token refresh: HTTP %s", response.Status)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return StoredToken{}, err
	}
	if payload.AccessToken == "" {
		return StoredToken{}, fmt.Errorf("token refresh returned no access_token")
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	updated := token
	updated.AccessToken = payload.AccessToken
	if payload.RefreshToken != "" {
		updated.RefreshToken = payload.RefreshToken
	}
	updated.ExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return updated, nil
}

// LoginOutcome is the result of an interactive authorization.
type LoginOutcome struct {
	AuthorizationURL string
	Code             string
}

// RegisterClient performs RFC 7591 dynamic client registration.
func RegisterClient(metadata *AuthorizationServerMetadata, redirectURI string) (string, error) {
	if metadata.RegistrationEndpoint == "" {
		return "", fmt.Errorf("authorization server has no registration_endpoint")
	}
	body, err := json.Marshal(map[string]any{
		"client_name":                "gcode",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequest(http.MethodPost, metadata.RegistrationEndpoint,
		strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return "", fmt.Errorf("client registration: HTTP %s", response.Status)
	}
	var payload struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.ClientID == "" {
		return "", fmt.Errorf("client registration returned no client_id")
	}
	return payload.ClientID, nil
}

// BuildAuthorizationURL assembles the authorization-code URL with PKCE
// (RFC 7636, S256) and a state nonce.
func BuildAuthorizationURL(metadata *AuthorizationServerMetadata, clientID,
	redirectURI, state, codeVerifier string) (string, error) {
	sum := sha256.Sum256([]byte(codeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	parsed, err := url.Parse(metadata.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// PKCEVerifier generates a 64-char code verifier.
func PKCEVerifier() (string, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// ExchangeAuthorizationCode swaps the code for tokens and stores them.
func ExchangeAuthorizationCode(serverURL string, metadata *AuthorizationServerMetadata,
	clientID, redirectURI, code, codeVerifier string) (StoredToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {codeVerifier},
	}.Encode()
	request, err := http.NewRequest(http.MethodPost, metadata.TokenEndpoint,
		strings.NewReader(form))
	if err != nil {
		return StoredToken{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return StoredToken{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return StoredToken{}, fmt.Errorf("authorization code exchange: HTTP %s", response.Status)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return StoredToken{}, err
	}
	if payload.AccessToken == "" {
		return StoredToken{}, fmt.Errorf("token exchange returned no access_token")
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	token := StoredToken{
		ServerURL: serverURL, AccessToken: payload.AccessToken,
		RefreshToken: payload.RefreshToken, ClientID: clientID,
		ExpiresAt: time.Now().Add(time.Duration(expiresIn) * time.Second),
	}
	if err := StoreToken(token); err != nil {
		return StoredToken{}, err
	}
	return token, nil
}
