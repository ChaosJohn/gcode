package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/types"
)

// HTTP transport for the Messages API (ported from stream_response +
// jcode-provider-core transport helpers).

const (
	// APIURLOAuth is the first-party OAuth endpoint (beta query param).
	APIURLOAuth = "https://api.anthropic.com/v1/messages?beta=true"
	// ClaudeCLIUserAgent matches the official Claude Code CLI.
	ClaudeCLIUserAgent = "claude-cli/2.1.280 (external, sdk-cli)"
	// InitialResponseTimeout bounds connection + response-header wait; the
	// same budget covers SSE idle reads (stream_idle_timeout_secs default 180).
	InitialResponseTimeout = 180 * time.Second
)

// TransportConfig configures one request path.
type TransportConfig struct {
	Client     *http.Client
	APIURL     string            // direct API (or gateway) URL
	AuthMode   string            // "bearer" | "header" | "none" | "" (header, x-api-key)
	AuthHeader string            // header name for AuthMode "header"
	Headers    map[string]string // extra headers for gateway transports
	// OAuthURL allows tests to override the OAuth endpoint.
	OAuthURL string
}

func (c *TransportConfig) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return DefaultHTTPClient()
}

// DefaultHTTPClient builds the shared transport (HTTP/2 enabled).
func DefaultHTTPClient() *http.Client {
	transport := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{Transport: transport}
}

// AnthropicBetaHeaderWithThinking appends interleaved thinking when enabled.
func AnthropicBetaHeaderWithThinking(base string, thinkingEnabled bool) string {
	if thinkingEnabled && !strings.Contains(base, "interleaved-thinking-2025-05-14") {
		return base + ",interleaved-thinking-2025-05-14"
	}
	return base
}

// EmitFunc consumes one stream event; return an error to abort the stream.
type EmitFunc func(types.StreamEvent) error

// retryAfterError carries a server-honored retry delay.
type retryAfterError struct {
	msg   string
	retry time.Duration
}

func (e *retryAfterError) Error() string { return e.msg }

// RetryAfterFromError extracts a parsed Retry-After hint, if any.
func RetryAfterFromError(err error) (time.Duration, bool) {
	for e := err; e != nil; {
		if r, ok := e.(*retryAfterError); ok {
			return r.retry, true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return 0, false
}

// MaxRetryAfter bounds honored server hints.
const MaxRetryAfter = 120 * time.Second

func retryAfterHeader(header http.Header) (time.Duration, bool) {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}
	if allDigits(value) {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, false
		}
		if seconds > uint64(MaxRetryAfter.Seconds()) {
			seconds = uint64(MaxRetryAfter.Seconds())
		}
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := time.Until(at)
		if delay < 0 {
			delay = 0
		}
		if delay > MaxRetryAfter {
			delay = MaxRetryAfter
		}
		return delay, true
	}
	return 0, false
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isASCIIDigit(s[i]) {
			return false
		}
	}
	return len(s) > 0
}

// StreamResponse sends one request and pumps its SSE stream into emit.
// responseAccepted is set once the server returned success headers (after
// that, quota/auth errors belong to this runtime's inner handling, not early
// account re-routing).
func StreamResponse(ctx context.Context, cfg TransportConfig, token string, isOAuth bool,
	request ApiRequest, sessionID, modelName string, emit EmitFunc, responseAccepted *bool) error {
	if responseAccepted != nil {
		*responseAccepted = false
	}
	idleTimeout := InitialResponseTimeout

	_ = emit(types.ConnectionPhaseEvent{Phase: types.PhaseSendingRequest})

	url := cfg.APIURL
	if url == "" {
		url = APIURL
	}
	if isOAuth {
		url = APIURLOAuth
		if cfg.OAuthURL != "" {
			url = cfg.OAuthURL
		}
		// The OAuth surface requires the beta query param.
		if !strings.Contains(url, "?") {
			url += "?beta=true"
		}
	}

	body, err := MarshalOrdered(request)
	if err != nil {
		return fmt.Errorf("failed to encode Anthropic request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to build Anthropic request: %w", err)
	}
	req.Header.Set("anthropic-version", APIVersion)
	req.Header.Set("content-type", "application/json")
	if isOAuth {
		req.Header.Set("accept", "application/json")
	} else {
		req.Header.Set("accept", "text/event-stream")
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	if isOAuth {
		// OAuth: Bearer auth (NOT x-api-key), Claude CLI user agent, beta
		// headers, attribution headers.
		beta := AnthropicBetaHeaderWithThinking(OAuthBetaHeadersFor(modelName), request.Thinking != nil)
		beta = WithBindingBeta(beta, request.Thinking)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", ClaudeCLIUserAgent)
		req.Header.Set("anthropic-beta", beta)
		applyOAuthAttributionHeaders(req.Header, sessionID)
	} else {
		beta := "prompt-caching-2024-07-31"
		if strings.HasSuffix(modelName, LongContextSuffix) {
			beta = "prompt-caching-2024-07-31,context-1m-2025-08-07"
		}
		beta = AnthropicBetaHeaderWithThinking(beta, request.Thinking != nil)
		beta = WithBindingBeta(beta, request.Thinking)
		switch cfg.AuthMode {
		case "", "header":
			name := cfg.AuthHeader
			if name == "" {
				name = "x-api-key"
			}
			req.Header.Set(name, token)
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+token)
		case "none":
		default:
			return fmt.Errorf("invalid anthropic auth mode %q (expected bearer, header, or none)", cfg.AuthMode)
		}
		req.Header.Set("anthropic-beta", beta)
	}

	client := cfg.httpClient()
	resp, err := sendWithInitialResponseTimeout(ctx, client, req, idleTimeout)
	if err != nil {
		// net/http surfaces a connection closed before any response byte as a
		// bare EOF; phrase it like the transport classifier expects.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			err = fmt.Errorf("connection closed before response headers: %w", err)
		}
		return fmt.Errorf("failed to send request to Anthropic API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		hint, hasHint := retryAfterHeader(resp.Header)
		errorText := httpErrorBody(resp)
		msg := fmt.Sprintf("Anthropic API error (%s): %s", resp.Status, errorText)
		if hasHint {
			return &retryAfterError{msg: msg, retry: hint}
		}
		return fmt.Errorf("%s", msg)
	}
	if responseAccepted != nil {
		*responseAccepted = true
	}
	_ = emit(types.ConnectionPhaseEvent{Phase: types.PhaseWaitingResponse})

	state := &SseStreamState{
		RequestedModelBase: strings.ToLower(StripLongContextSuffix(request.Model)),
	}

	if err := pumpSSE(resp.Body, idleTimeout, state, isOAuth, emit); err != nil {
		return err
	}

	if usage := state.FinalTokenUsage(); usage != nil {
		_ = emit(*usage)
	}
	return nil
}

func sendWithInitialResponseTimeout(ctx context.Context, client *http.Client, req *http.Request, timeout time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return client.Do(req.WithContext(ctx))
}

// pumpSSE reads the response body line-wise, assembling complete SSE events
// (terminated by a blank line) and mapping them through ProcessSSEEvent.
func pumpSSE(body io.Reader, idleTimeout time.Duration, state *SseStreamState, isOAuth bool, emit EmitFunc) error {
	type result struct {
		line string
		err  error
	}
	lines := make(chan result, 16)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
		for scanner.Scan() {
			lines <- result{line: scanner.Text()}
		}
		if err := scanner.Err(); err != nil {
			lines <- result{err: fmt.Errorf("error reading stream chunk: %w", err)}
		}
	}()

	var pending []string
	handle := func(line string) error {
		if line != "" {
			pending = append(pending, line)
			return nil
		}
		if len(pending) == 0 {
			return nil
		}
		event := linesToSSE(pending)
		pending = pending[:0]
		if event == nil {
			return nil
		}
		for _, streamEvent := range ProcessSSEEvent(event, state, isOAuth) {
			if errEvent, ok := streamEvent.(types.ErrorEvent); ok &&
				IsRetryableError(strings.ToLower(errEvent.Message)) {
				return fmt.Errorf("retryable stream error: %s", errEvent.Message)
			}
			if err := emit(streamEvent); err != nil {
				return err
			}
		}
		return nil
	}

	for {
		select {
		case r, ok := <-lines:
			if !ok {
				return nil
			}
			if r.err != nil {
				return r.err
			}
			if err := handle(r.line); err != nil {
				return err
			}
		case <-time.After(idleTimeout):
			return fmt.Errorf("stream read timeout: no data received for %d seconds",
				int64(idleTimeout.Seconds()))
		}
	}
}

// linesToSSE assembles one SSE event from its lines (nil when data-less).
func linesToSSE(lines []string) *SSEEvent {
	var eventType, data strings.Builder
	hasData := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "event:")))
		case strings.HasPrefix(line, "data:"):
			if hasData {
				data.WriteString("\n")
			}
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimPrefix(payload, " ")
			data.WriteString(payload)
			hasData = true
		}
	}
	if !hasData {
		return nil
	}
	return &SSEEvent{Event: eventType.String(), Data: data.String()}
}

func httpErrorBody(resp *http.Response) string {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(body) == 0 {
		return "HTTP error"
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "HTTP error"
	}
	return text
}

// ---- OAuth client metadata + attribution headers ----

// OAuthClientMetadata mirrors ~/.claude.json's official-client fields.
type OAuthClientMetadata struct {
	DeviceID         string
	AccountUUID      string
	OrganizationUUID string
	EmailAddress     string
}

// LoadOfficialClaudeClientMetadata reads the Claude CLI's stored client
// metadata; absent fields fall back to derived values.
func LoadOfficialClaudeClientMetadata() OAuthClientMetadata {
	home, err := os.UserHomeDir()
	if err != nil {
		return OAuthClientMetadata{}
	}
	content, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return OAuthClientMetadata{}
	}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(content, &parsed) != nil {
		return OAuthClientMetadata{}
	}
	meta := OAuthClientMetadata{}
	if raw, ok := parsed["userID"]; ok {
		_ = json.Unmarshal(raw, &meta.DeviceID)
	}
	var oauth map[string]json.RawMessage
	if raw, ok := parsed["oauthAccount"]; ok && json.Unmarshal(raw, &oauth) == nil {
		_ = json.Unmarshal(oauth["accountUuid"], &meta.AccountUUID)
		_ = json.Unmarshal(oauth["organizationUuid"], &meta.OrganizationUUID)
		_ = json.Unmarshal(oauth["emailAddress"], &meta.EmailAddress)
	}
	return meta
}

// namespaceDNS is RFC 4122's DNS namespace for UUIDv5.
var namespaceDNS = [16]byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}

// uuidV5Simple computes UUIDv5 (SHA-1 based) in the simple (dash-free)
// hexadecimal format the official client metadata uses.
func uuidV5Simple(namespace [16]byte, name string) string {
	h := sha1.New()
	h.Write(namespace[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50 // version 5
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x", u[:])
}

// OAuthRequestMetadata builds metadata.user_id for the official client shape:
// device_id (stored or UUIDv5-of-session), account uuid, session id.
func OAuthRequestMetadata(sessionID string) ApiMetadata {
	official := LoadOfficialClaudeClientMetadata()
	deviceID := official.DeviceID
	if deviceID == "" {
		deviceID = uuidV5Simple(namespaceDNS, sessionID)
	}
	accountUUID := official.AccountUUID
	if accountUUID == "" {
		accountUUID = "unknown-account"
	}
	// Field order matters for byte parity: device_id, account_uuid, session_id.
	userID := fmt.Sprintf(`{"device_id":%s,"account_uuid":%s,"session_id":%s}`,
		jsonString(&deviceID), jsonString(&accountUUID), jsonString(&sessionID))
	return ApiMetadata{UserID: userID}
}

func jsonString(s *string) string {
	encoded, _ := types.MarshalCompact(*s)
	return string(encoded)
}

// applyOAuthAttributionHeaders adds the official CLI attribution headers.
func applyOAuthAttributionHeaders(header http.Header, sessionID string) {
	header.Set("x-client-request-id", newOAuthRequestID())
	header.Set("x-app", "cli")
	header.Set("X-Claude-Code-Session-Id", sessionID)
	header.Set("X-Stainless-Arch", StainlessArch())
	header.Set("X-Stainless-Lang", "js")
	header.Set("X-Stainless-OS", StainlessOS())
	header.Set("X-Stainless-Package-Version", "0.81.0")
	header.Set("X-Stainless-Retry-Count", "0")
	header.Set("X-Stainless-Runtime", "node")
	header.Set("X-Stainless-Runtime-Version", "v24.3.0")
	header.Set("X-Stainless-Timeout", "600")
	header.Set("anthropic-dangerous-direct-browser-access", "true")
}

func newOAuthRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
