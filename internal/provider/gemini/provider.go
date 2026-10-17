package gemini

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ChaosJohn/gcode/internal/provider/transporterr"
	"github.com/ChaosJohn/gcode/internal/schema/dialect"
	"github.com/ChaosJohn/gcode/internal/types"
)

// Gemini runtime (ported from jcode-provider-gemini-runtime lib.rs).
// Gemini is a non-streaming provider: one generateContent POST, then the whole
// response maps to events in order.

const (
	// MaxHTTPRetries bounds transient 5xx/429 retries.
	MaxHTTPRetries = 5
	// HTTPRetryBaseMS is the retry backoff base.
	HTTPRetryBaseMS = 1500
	// SendRetryDelayMS is the single transient-transport retry delay.
	SendRetryDelayMS = 250
)

// HTTP retry helpers ---------------------------------------------------------

// retryDelay mirrors provider-core retry_delay: server hint wins, else
// exponential backoff with jitter.
func retryDelay(attempt int, baseMS uint64, serverHint *time.Duration) time.Duration {
	if serverHint != nil {
		return *serverHint
	}
	return backoffDelay(attempt, baseMS)
}

func backoffDelay(attempt int, baseMS uint64) time.Duration {
	shift := attempt - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 16 {
		shift = 16
	}
	exp := baseMS << uint(shift)
	// ±20% jitter, deterministic seed per attempt (portable without math/rand).
	jitter := uint64(0.8 * float64(exp))
	return time.Duration(jitter + (exp-jitter)/2)
}

// geminiHTTPRetryDelay decides whether an HTTP failure retries.
func geminiHTTPRetryDelay(method string, status int, body string, attempt int, retryAfter *time.Duration) *time.Duration {
	if attempt >= MaxHTTPRetries || (method != "generateContent" && method != "loadCodeAssist") {
		return nil
	}
	if status == http.StatusTooManyRequests {
		lower := asciiLower(body)
		for _, reason := range []string{
			"daily", "per day", "per_day", "perday", "billing", "insufficient_quota", "quota_exhausted",
		} {
			if contains(lower, reason) {
				return nil
			}
		}
	} else if status != 500 && status != 502 && status != 503 && status != 504 {
		return nil
	}
	delay := retryDelay(attempt, HTTPRetryBaseMS, retryAfter)
	return &delay
}

func retryAfterFromHeader(header string) *time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	var seconds int
	if _, err := fmt.Sscanf(header, "%d", &seconds); err != nil {
		return nil
	}
	if seconds < 0 {
		return nil
	}
	delay := time.Duration(seconds) * time.Second
	return &delay
}

// isVPCSCError detects VPC-SC rejections (treated as a standard tier).
func isVPCSCError(message string) bool { return contains(message, "SECURITY_POLICY_VIOLATED") }

// newUUID produces a random v4-shaped UUID for user_prompt_id/session ids.
func newUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	h := hex.EncodeToString(buf)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// IsGeminiModelNotFoundError recognizes the 404 model rejection.
func IsGeminiModelNotFoundError(message string) bool {
	lower := asciiLower(message)
	return contains(lower, "http 404") ||
		contains(lower, `"status": "not_found"`) ||
		contains(lower, "requested entity was not found")
}

// GeminiUserAgent matches the official Gemini CLI (quota identity).
func GeminiUserAgent() string {
	if ua := strings.TrimSpace(os.Getenv("GCODE_GEMINI_USER_AGENT")); ua != "" {
		return ua
	}
	osName := runtime.GOOS
	arch := runtime.GOARCH
	if arch == "aarch64" {
		arch = "arm64"
	} else if arch == "x86_64" {
		arch = "x64"
	}
	return fmt.Sprintf("GeminiCLI-tui/0.57.0/%s (%s; %s; terminal)", DefaultModel, osName, arch)
}

// Client is the Gemini HTTP transport.
type Client struct {
	HTTP          *http.Client
	mu            sync.Mutex
	state         *RuntimeState
	stateKey      string
	fetchedModels []string
}

// NewClient builds the transport with the Code Assist identity headers.
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 90 * time.Second}}
}

func (c *Client) setState(state RuntimeState, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stateCopy := state
	c.state = &stateCopy
	c.stateKey = key
}

func (c *Client) cachedState(key string) *RuntimeState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != nil && c.stateKey == key {
		return c.state
	}
	return nil
}

func (c *Client) setFetchedModels(models []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetchedModels = models
}

// FetchedModels returns the discovered catalog.
func (c *Client) FetchedModels() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.fetchedModels...)
}

// BaseURL is the Code Assist endpoint with its API version.
func BaseURL() string {
	endpoint := os.Getenv("CODE_ASSIST_ENDPOINT")
	if endpoint == "" {
		endpoint = CodeAssistEndpoint
	}
	version := os.Getenv("CODE_ASSIST_API_VERSION")
	if version == "" {
		version = CodeAssistAPIVersion
	}
	return endpoint + "/" + version
}

// DeveloperAPIBaseURL is the official Developer API base.
func DeveloperAPIBaseURL() string {
	endpoint := os.Getenv("GEMINI_API_ENDPOINT")
	if endpoint == "" {
		endpoint = GeminiAPIEndpoint
	}
	version := os.Getenv("GEMINI_API_VERSION")
	if version == "" {
		version = GeminiAPIVersion
	}
	return strings.TrimSuffix(endpoint, "/") + "/" + strings.Trim(version, "/")
}

// AuthMode is the resolved authentication route.
type AuthMode struct {
	// APIKey set → Developer API; nil → Code Assist OAuth.
	APIKey *string
}

// ResolveAuthMode: an API key wins unless OAuth is pinned.
func ResolveAuthMode() AuthMode {
	if !ForceOAuth() {
		if key, ok := APIKey(); ok {
			return AuthMode{APIKey: &key}
		}
	}
	return AuthMode{}
}

func (m AuthMode) isAPIKey() bool { return m.APIKey != nil }

func stateKeyOf(mode AuthMode) string {
	if mode.isAPIKey() {
		return "api-key"
	}
	return "oauth:" + CloudProject()
}

// ensureState resolves (and caches) the Code Assist project + session.
func (c *Client) ensureState() (*RuntimeState, error) {
	mode := ResolveAuthMode()
	key := stateKeyOf(mode)
	if cached := c.cachedState(key); cached != nil {
		return cached, nil
	}
	var state RuntimeState
	if mode.isAPIKey() {
		// The Developer API path is stateless.
		state = RuntimeState{ProjectID: "", SessionID: newUUID()}
	} else {
		resolved, err := c.setupRuntimeState()
		if err != nil {
			return nil, err
		}
		state = *resolved
	}
	c.setState(state, key)
	return &state, nil
}

// setupRuntimeState runs the Code Assist handshake.
func (c *Client) setupRuntimeState() (*RuntimeState, error) {
	projectEnv := CloudProject()
	var projectPtr *string
	if projectEnv != "" {
		projectPtr = &projectEnv
	}
	metadata := ClientMetadataFor(projectEnv)
	loadRes, err := postJSON[LoadCodeAssistResponse](c, "loadCodeAssist",
		LoadCodeAssistRequestFor(projectPtr, metadata))
	if err != nil {
		if isVPCSCError(err.Error()) {
			standard := "standard-tier"
			loadRes = LoadCodeAssistResponse{CurrentTier: &GeminiUserTier{ID: &standard}}
		} else {
			return nil, fmt.Errorf("Gemini Code Assist setup failed during loadCodeAssist: %w", err)
		}
	}
	if err := ValidateLoadCodeAssistResponse(&loadRes); err != nil {
		return nil, err
	}

	projectID := ""
	if loadRes.CurrentTier != nil {
		switch {
		case loadRes.CloudaicompanionProject != nil:
			projectID = *loadRes.CloudaicompanionProject
		case projectEnv != "":
			projectID = projectEnv
		default:
			return nil, IneligibleOrProjectError(&loadRes)
		}
	} else {
		tier := ChooseOnboardTier(&loadRes)
		onboardReq := OnboardUserRequest{TierID: tier.ID}
		if tier.ID != nil && *tier.ID == UserTierFree {
			onboardReq.Metadata = &ClientMetadata{IDEType: "IDE_UNSPECIFIED",
				Platform: "PLATFORM_UNSPECIFIED", PluginType: "GEMINI"}
		} else {
			metadataCopy := metadata
			onboardReq.Metadata = &metadataCopy
			onboardReq.CloudaicompanionProject = projectPtr
		}
		lro, err := postJSON[LongRunningOperationResponse](c, "onboardUser", onboardReq)
		if err != nil {
			return nil, fmt.Errorf("Gemini Code Assist onboarding failed: %w", err)
		}
		for lro.Done == nil || !*lro.Done {
			if lro.Name == nil || *lro.Name == "" {
				return nil, errString("Gemini onboarding returned no operation name")
			}
			time.Sleep(5 * time.Second)
			lro, err = getJSON[LongRunningOperationResponse](c, *lro.Name)
			if err != nil {
				return nil, fmt.Errorf("Gemini onboarding polling failed: %w", err)
			}
		}
		if lro.Response != nil && lro.Response.CloudaicompanionProject != nil &&
			lro.Response.CloudaicompanionProject.ID != nil {
			projectID = *lro.Response.CloudaicompanionProject.ID
		} else if projectEnv != "" {
			projectID = projectEnv
		} else {
			return nil, IneligibleOrProjectError(&loadRes)
		}
	}
	return &RuntimeState{ProjectID: projectID, SessionID: newUUID()}, nil
}

// HTTP transport with the two-attempt transient retry -----------------------

func (c *Client) doWithRetry(build func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", GeminiUserAgent())
		req.Header.Set("x-goog-api-client", "gl-node/26.4.0")
		resp, err := c.HTTP.Do(req)
		if err == nil {
			return resp, nil
		}
		if attempt == 0 && transporterr.IsTransientTransportError(err.Error()) {
			lastErr = err
			time.Sleep(SendRetryDelayMS * time.Millisecond)
			continue
		}
		return nil, fmt.Errorf("Gemini request failed: %w", err)
	}
	return nil, fmt.Errorf("Gemini request failed: %w", lastErr)
}

func readAll(r io.Reader) ([]byte, error) {
	var out []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, err
		}
	}
}

// postJSON sends one Code Assist method call with transient-HTTP retries.
func postJSON[T any](c *Client, method string, body any) (T, error) {
	var zero T
	tokens, err := LoadOrRefreshTokens()
	if err != nil {
		return zero, err
	}
	endpoint := fmt.Sprintf("%s:%s", BaseURL(), method)
	encoded, err := json.Marshal(body)
	if err != nil {
		return zero, fmt.Errorf("Failed to serialize Gemini request body: %w", err)
	}

	attempt := 0
	for {
		resp, err := c.doWithRetry(func() (*http.Request, error) {
			req, err := http.NewRequest("POST", endpoint, strings.NewReader(string(encoded)))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
			req.Header.Set("Content-Type", "application/json")
			return req, nil
		})
		if err != nil {
			return zero, err
		}
		raw, readErr := readAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return zero, fmt.Errorf("Gemini request %s failed: %w", method, readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			bodyText := strings.TrimSpace(string(raw))
			hint := retryAfterFromHeader(resp.Header.Get("Retry-After"))
			if delay := geminiHTTPRetryDelay(method, resp.StatusCode, bodyText, attempt, hint); delay != nil {
				attempt++
				time.Sleep(*delay)
				continue
			}
			return zero, fmt.Errorf("Gemini request %s failed (HTTP %d): %s", method, resp.StatusCode, bodyText)
		}
		var parsed T
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return zero, fmt.Errorf("Failed to parse Gemini %s response: %w", method, err)
		}
		return parsed, nil
	}
}

// getJSON performs a GET against the Code Assist endpoint.
func getJSON[T any](c *Client, name string) (T, error) {
	var zero T
	tokens, err := LoadOrRefreshTokens()
	if err != nil {
		return zero, err
	}
	endpoint := fmt.Sprintf("%s/%s", BaseURL(), strings.TrimPrefix(name, "/"))
	resp, err := c.doWithRetry(func() (*http.Request, error) {
		req, err := http.NewRequest("GET", endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
	if err != nil {
		return zero, err
	}
	raw, readErr := readAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return zero, fmt.Errorf("Gemini operation lookup failed: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, fmt.Errorf("Gemini operation lookup failed (HTTP %d): %s",
			resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed T
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return zero, fmt.Errorf("Failed to parse Gemini operation response: %w", err)
	}
	return parsed, nil
}

// GenerateContent sends one turn (ported from generate_content).
func (c *Client) GenerateContent(state *RuntimeState, model string, messages []types.Message,
	tools []types.ToolDefinition, system, resumeSessionID string) (*CodeAssistGenerateResponse, error) {

	sessionID := state.SessionID
	if trimmed := strings.TrimSpace(resumeSessionID); trimmed != "" {
		sessionID = trimmed
	}
	hasTools := len(tools) > 0
	request := CodeAssistGenerateRequest{
		Model:        model,
		Project:      state.ProjectID,
		UserPromptID: newUUID(),
		Request: VertexGenerateContentRequest{
			Contents:          BuildContents(messages),
			SystemInstruction: BuildSystemInstructionWithToolGuard(system, hasTools),
			Tools:             BuildTools(tools),
			SessionID:         &sessionID,
		},
	}
	if hasTools {
		request.Request.ToolConfig = &GeminiToolConfig{
			FunctionCallingConfig: GeminiFunctionCallingConfig{Mode: "AUTO"},
		}
	}

	mode := ResolveAuthMode()
	if mode.isAPIKey() {
		// The Developer API consumes the inner body directly, no envelope.
		inner := request.Request
		inner.SessionID = nil
		endpoint := fmt.Sprintf("%s/models/%s:generateContent", DeveloperAPIBaseURL(), model)
		response, err := postDeveloperJSON[VertexGenerateContentResponse](c, endpoint, *mode.APIKey, inner)
		if err != nil {
			return nil, fmt.Errorf("Gemini generateContent failed: %w", err)
		}
		return &CodeAssistGenerateResponse{Response: &response}, nil
	}
	response, err := postJSON[CodeAssistGenerateResponse](c, "generateContent", request)
	if err != nil {
		return nil, fmt.Errorf("Gemini generateContent failed: %w", err)
	}
	return &response, nil
}

// postDeveloperJSON authenticates with x-goog-api-key.
func postDeveloperJSON[T any](c *Client, endpoint, apiKey string, body any) (T, error) {
	var zero T
	encoded, err := json.Marshal(body)
	if err != nil {
		return zero, err
	}
	attempt := 0
	for {
		resp, err := c.doWithRetry(func() (*http.Request, error) {
			req, err := http.NewRequest("POST", endpoint, strings.NewReader(string(encoded)))
			if err != nil {
				return nil, err
			}
			req.Header.Set("x-goog-api-key", apiKey)
			req.Header.Set("Content-Type", "application/json")
			return req, nil
		})
		if err != nil {
			return zero, err
		}
		raw, readErr := readAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return zero, fmt.Errorf("Gemini generateContent failed: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			bodyText := strings.TrimSpace(string(raw))
			hint := retryAfterFromHeader(resp.Header.Get("Retry-After"))
			if delay := geminiHTTPRetryDelay("generateContent", resp.StatusCode, bodyText, attempt, hint); delay != nil {
				attempt++
				time.Sleep(*delay)
				continue
			}
			return zero, fmt.Errorf("Gemini generateContent failed (HTTP %d): %s", resp.StatusCode, bodyText)
		}
		var parsed T
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return zero, fmt.Errorf("Failed to parse Gemini generateContent response: %w", err)
		}
		return parsed, nil
	}
}

func urlQueryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// Complete drives one turn and emits the full event sequence (ported from
// Provider::complete for the Gemini runtime). Model fallback and schema
// rejection recovery included.
func (c *Client) Complete(options CompleteOptions) error {
	model := options.Model
	_ = options.Emit(types.ConnectionTypeEvent{Connection: "https"})
	_ = options.Emit(types.ConnectionPhaseEvent{Phase: types.PhaseAuthenticating})

	state, err := c.ensureState()
	if err != nil {
		return err
	}
	if options.ResumeSessionID == "" {
		_ = options.Emit(types.SessionIDEvent{ID: state.SessionID})
	} else {
		_ = options.Emit(types.SessionIDEvent{ID: options.ResumeSessionID})
	}
	_ = options.Emit(types.ConnectionPhaseEvent{Phase: types.PhaseSendingRequest})
	_ = options.Emit(types.ConnectionPhaseEvent{Phase: types.PhaseWaitingResponse})

	response, err := c.GenerateContent(state, model, options.Messages, options.Tools,
		options.System, options.ResumeSessionID)
	if err != nil {
		if IsGeminiModelNotFoundError(err.Error()) {
			// Walk the fallback ladder; the first success pins the model.
			var lastErr error = err
			recovered := false
			for _, fallback := range GeminiFallbackModels(model) {
				attempt, attemptErr := c.GenerateContent(state, fallback, options.Messages,
					options.Tools, options.System, options.ResumeSessionID)
				if attemptErr == nil {
					if options.OnModelSwitch != nil {
						options.OnModelSwitch(fallback)
					}
					response, model = attempt, fallback
					recovered = true
					break
				}
				lastErr = attemptErr
			}
			if !recovered {
				return lastErr
			}
		} else if recovered := c.retryAfterSchemaRejection(err.Error(), state, model, options); recovered != nil {
			response = recovered
		} else {
			return err
		}
	}

	_ = options.Emit(types.ConnectionPhaseEvent{Phase: types.PhaseStreaming})

	if response.Response != nil && response.Response.UsageMetadata != nil {
		usage := response.Response.UsageMetadata
		_ = options.Emit(types.TokenUsageEvent{
			InputTokens:          usage.PromptTokenCount,
			OutputTokens:         usage.CandidatesTokenCount,
			CacheReadInputTokens: usage.CachedContentTokenCount,
		})
	}

	var candidate *GeminiCandidate
	if response.Response != nil && len(response.Response.Candidates) > 0 {
		candidate = &response.Response.Candidates[0]
	}
	if candidate == nil {
		if response.Response != nil && response.Response.PromptFeedback != nil {
			feedback := response.Response.PromptFeedback
			reason := "unspecified"
			if feedback.BlockReason != nil {
				reason = *feedback.BlockReason
			}
			detail := ""
			if feedback.BlockReasonMessage != nil && strings.TrimSpace(*feedback.BlockReasonMessage) != "" {
				detail = ": " + strings.TrimSpace(*feedback.BlockReasonMessage)
			}
			return fmt.Errorf("Gemini blocked the prompt (%s)%s", reason, detail)
		}
		return errString("Gemini returned no candidates for generateContent")
	}

	var stopReason *string
	if candidate.FinishReason != nil {
		lowered := asciiLower(*candidate.FinishReason)
		stopReason = &lowered
	}
	if candidate.Content == nil && isAbnormalBlockFinish(candidate.FinishReason) {
		reason := "unknown"
		if candidate.FinishReason != nil {
			reason = *candidate.FinishReason
		}
		detail := ""
		if candidate.FinishMessage != nil && strings.TrimSpace(*candidate.FinishMessage) != "" {
			detail = ": " + truncateRunes(strings.TrimSpace(*candidate.FinishMessage), 300)
		}
		return fmt.Errorf("Gemini stopped without content (%s)%s", reason, detail)
	}

	producedOutput := false
	if candidate.Content != nil {
		var pendingSignature *string
		for _, part := range candidate.Content.Parts {
			var partSignature *string
			if part.ThoughtSignature != nil && *part.ThoughtSignature != "" {
				signature := *part.ThoughtSignature
				partSignature = &signature
			}
			if part.Text != nil && *part.Text != "" {
				producedOutput = true
				_ = options.Emit(types.TextDelta{Text: *part.Text})
			}
			if part.FunctionCall != nil {
				producedOutput = true
				signature := partSignature
				if signature == nil {
					signature = pendingSignature
					pendingSignature = nil
				}
				rawCallID := newUUID()
				if part.FunctionCall.ID != nil && *part.FunctionCall.ID != "" {
					rawCallID = *part.FunctionCall.ID
				}
				callID := types.SanitizeToolID(rawCallID)
				_ = options.Emit(types.ToolUseStart{ID: callID, Name: part.FunctionCall.Name})
				_ = options.Emit(types.ToolInputDelta{Delta: string(part.FunctionCall.Args)})
				_ = options.Emit(types.ToolUseEnd{})
				if signature != nil {
					_ = options.Emit(types.ToolUseSignature{Signature: *signature})
				}
			} else if partSignature != nil {
				// Standalone signature part; applies to the next function call.
				pendingSignature = partSignature
			}
		}
		// A signature no function call consumed still carries reasoning signal.
		if pendingSignature != nil {
			_ = options.Emit(types.ThinkingSignatureDelta{Delta: *pendingSignature})
		}
	}

	if !producedOutput && isAbnormalFinish(candidate.FinishReason) {
		reason := "unknown"
		if candidate.FinishReason != nil {
			reason = *candidate.FinishReason
		}
		detail := ""
		if candidate.FinishMessage != nil && strings.TrimSpace(*candidate.FinishMessage) != "" {
			detail = ": " + truncateRunes(strings.TrimSpace(*candidate.FinishMessage), 300)
		}
		return fmt.Errorf("Gemini returned no usable output (finish_reason=%s)%s", reason, detail)
	}

	return options.Emit(types.MessageEnd{StopReason: stopReason})
}

func isAbnormalBlockFinish(reason *string) bool {
	if reason == nil {
		return false
	}
	switch strings.ToUpper(*reason) {
	case "SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "RECITATION":
		return true
	}
	return false
}

func isAbnormalFinish(reason *string) bool {
	if reason == nil {
		return false
	}
	switch strings.ToUpper(*reason) {
	case "STOP", "MAX_TOKENS", "FINISH_REASON_UNSPECIFIED", "":
		return false
	}
	return true
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// retryAfterSchemaRejection learns a rejected construct and retries once
// (ported from retry_after_schema_rejection).
func (c *Client) retryAfterSchemaRejection(errorText string, state *RuntimeState, model string, options CompleteOptions) *CodeAssistGenerateResponse {
	action := dialect.RecoverFromError(errorText, &dialect.Gemini)
	switch action.Kind {
	case dialect.RecoveryNotSchemaRelated, dialect.RecoveryUnrecoverable:
		return nil
	case dialect.RecoveryRetryWithoutConstruct:
		response, err := c.GenerateContent(state, model, options.Messages, options.Tools,
			options.System, options.ResumeSessionID)
		if err != nil {
			return nil
		}
		return response
	}
	return nil
}

// CompleteOptions carries one Complete call.
type CompleteOptions struct {
	Model           string
	Messages        []types.Message
	Tools           []types.ToolDefinition
	System          string
	ResumeSessionID string
	Emit            func(types.StreamEvent) error
	// OnModelSwitch observes the fallback model pin.
	OnModelSwitch func(model string)
}
