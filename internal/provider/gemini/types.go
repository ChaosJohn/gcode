// Package gemini ports jcode-provider-gemini: request shaping for the Gemini
// Code Assist (cloudcode-pa) and Developer API (generativelanguage) backends.
package gemini

import (
	"encoding/json"
	"sort"

	"github.com/ChaosJohn/gcode/internal/schema/dialect"
	"github.com/ChaosJohn/gcode/internal/types"
)

// Non-dated server-side alias: always resolves to the newest stable pro model.
const DefaultModel = "gemini-pro-latest"

// AvailableModels is the static catalog, preferred order first.
var AvailableModels = []string{
	"gemini-pro-latest",
	"gemini-flash-latest",
	"gemini-3.1-pro-preview",
	"gemini-3-pro-preview",
	"gemini-3-flash-preview",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	"gemini-2.0-flash",
	"gemini-1.5-pro",
	"gemini-1.5-flash",
}

// FallbackModels is the model-not-found fallback ladder.
var FallbackModels = []string{
	"gemini-pro-latest",
	"gemini-3.1-pro-preview",
	"gemini-3-pro-preview",
	"gemini-2.5-pro",
	"gemini-3-flash-preview",
	"gemini-2.5-flash",
	"gemini-2.0-flash",
}

// Endpoints (Code Assist and the official Developer API).
const (
	CodeAssistEndpoint     = "https://cloudcode-pa.googleapis.com"
	CodeAssistAPIVersion   = "v1internal"
	GeminiAPIEndpoint      = "https://generativelanguage.googleapis.com"
	GeminiAPIVersion       = "v1beta"
	UserTierFree           = "free-tier"
	UserTierLegacy         = "legacy-tier"
	GeminiFunctionCallGuar = "\n\n## Function calling\n" +
		" - When you call a tool, emit a native function call, not code. Never write " +
		"Python (or any language) that calls the tool, and never wrap a call in " +
		"print(...) or a code block.\n" +
		" - Use the function name exactly as defined. Do not prepend `default_api.` " +
		"or any other namespace to the function name."
)

// RuntimeState carries the resolved Code Assist project and session.
type RuntimeState struct {
	ProjectID string
	SessionID string
}

// ClientMetadata is the Code Assist client identity block.
type ClientMetadata struct {
	IDEType     string  `json:"ideType"`
	Platform    string  `json:"platform"`
	PluginType  string  `json:"pluginType"`
	DuetProject *string `json:"duetProject,omitempty"`
}

// LoadCodeAssistRequest opens the Code Assist handshake.
type LoadCodeAssistRequest struct {
	CloudaicompanionProject *string        `json:"cloudaicompanionProject,omitempty"`
	Metadata                ClientMetadata `json:"metadata"`
	Mode                    *string        `json:"mode,omitempty"`
}

// LoadCodeAssistResponse reports the account's tier and project.
type LoadCodeAssistResponse struct {
	CurrentTier             *GeminiUserTier  `json:"currentTier,omitempty"`
	AllowedTiers            []GeminiUserTier `json:"allowedTiers,omitempty"`
	IneligibleTiers         []IneligibleTier `json:"ineligibleTiers,omitempty"`
	CloudaicompanionProject *string          `json:"cloudaicompanionProject,omitempty"`
	PaidTier                *GeminiUserTier  `json:"paidTier,omitempty"`
}

// GeminiUserTier is one Code Assist tier entry.
type GeminiUserTier struct {
	ID        *string `json:"id,omitempty"`
	Name      *string `json:"name,omitempty"`
	IsDefault *bool   `json:"isDefault,omitempty"`
}

// IneligibleTier explains why a tier is unavailable.
type IneligibleTier struct {
	ReasonCode    *string `json:"reasonCode,omitempty"`
	ReasonMessage *string `json:"reasonMessage,omitempty"`
	ValidationURL *string `json:"validationUrl,omitempty"`
}

// OnboardUserRequest enrolls the account into a tier.
type OnboardUserRequest struct {
	TierID                  *string         `json:"tierId,omitempty"`
	CloudaicompanionProject *string         `json:"cloudaicompanionProject,omitempty"`
	Metadata                *ClientMetadata `json:"metadata,omitempty"`
}

// LongRunningOperationResponse is the onboardUser LRO envelope.
type LongRunningOperationResponse struct {
	Name     *string              `json:"name,omitempty"`
	Done     *bool                `json:"done,omitempty"`
	Response *OnboardUserResponse `json:"response,omitempty"`
}

// OnboardUserResponse carries the provisioned project.
type OnboardUserResponse struct {
	CloudaicompanionProject *ProjectRef `json:"cloudaicompanionProject,omitempty"`
}

// ProjectRef wraps a project id.
type ProjectRef struct {
	ID *string `json:"id,omitempty"`
}

// CodeAssistGenerateRequest is the Code Assist envelope.
type CodeAssistGenerateRequest struct {
	Model        string                       `json:"model"`
	Project      string                       `json:"project"`
	UserPromptID string                       `json:"user_prompt_id"`
	Request      VertexGenerateContentRequest `json:"request"`
}

// VertexGenerateContentRequest is the inner generateContent body.
type VertexGenerateContentRequest struct {
	Contents          []GeminiContent   `json:"contents"`
	SystemInstruction *GeminiContent    `json:"systemInstruction,omitempty"`
	Tools             []GeminiTool      `json:"tools,omitempty"`
	ToolConfig        *GeminiToolConfig `json:"toolConfig,omitempty"`
	SessionID         *string           `json:"session_id,omitempty"`
}

// GeminiContent is one role/parts pair; responses may omit either field.
type GeminiContent struct {
	Role  string       `json:"role"`
	Parts []GeminiPart `json:"parts"`
}

// GeminiPart is one content part.
type GeminiPart struct {
	Text             *string                 `json:"text,omitempty"`
	InlineData       *InlineData             `json:"inlineData,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
	// ThoughtSignature is the Gemini-3 opaque reasoning signature; it must be
	// replayed verbatim on the functionCall part in later turns.
	ThoughtSignature *string `json:"thoughtSignature,omitempty"`
}

// InlineData is a base64 image part.
type InlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// GeminiFunctionCall is a model tool call.
type GeminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	ID   *string         `json:"id,omitempty"`
}

// GeminiFunctionResponse is a tool result part.
type GeminiFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
	ID       *string         `json:"id,omitempty"`
}

// GeminiTool wraps the function declarations.
type GeminiTool struct {
	FunctionDeclarations []GeminiFunctionDeclaration `json:"functionDeclarations"`
}

// GeminiFunctionDeclaration is one tool definition.
type GeminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// GeminiToolConfig pins the calling mode.
type GeminiToolConfig struct {
	FunctionCallingConfig GeminiFunctionCallingConfig `json:"functionCallingConfig"`
}

// GeminiFunctionCallingConfig carries the mode string.
type GeminiFunctionCallingConfig struct {
	Mode string `json:"mode"`
}

// CodeAssistGenerateResponse wraps the vertex response.
type CodeAssistGenerateResponse struct {
	TraceID  *string                        `json:"traceId,omitempty"`
	Response *VertexGenerateContentResponse `json:"response,omitempty"`
}

// VertexGenerateContentResponse is the generateContent result.
type VertexGenerateContentResponse struct {
	Candidates     []GeminiCandidate     `json:"candidates,omitempty"`
	PromptFeedback *GeminiPromptFeedback `json:"promptFeedback,omitempty"`
	UsageMetadata  *GeminiUsageMetadata  `json:"usageMetadata,omitempty"`
}

// GeminiCandidate is one completion candidate.
type GeminiCandidate struct {
	Content       *GeminiContent `json:"content,omitempty"`
	FinishReason  *string        `json:"finishReason,omitempty"`
	FinishMessage *string        `json:"finishMessage,omitempty"`
}

// GeminiPromptFeedback explains a blocked prompt.
type GeminiPromptFeedback struct {
	BlockReason        *string `json:"blockReason,omitempty"`
	BlockReasonMessage *string `json:"blockReasonMessage,omitempty"`
}

// GeminiUsageMetadata reports token counts.
type GeminiUsageMetadata struct {
	PromptTokenCount        *uint64 `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    *uint64 `json:"candidatesTokenCount,omitempty"`
	CachedContentTokenCount *uint64 `json:"cachedContentTokenCount,omitempty"`
}

// BuildSystemInstruction renders the system prompt as a user-role content.
func BuildSystemInstruction(system string) *GeminiContent {
	trimmed := trimSpace(system)
	if trimmed == "" {
		return nil
	}
	text := trimmed
	return &GeminiContent{Role: "user", Parts: []GeminiPart{{Text: &text}}}
}

// BuildSystemInstructionWithToolGuard appends the MALFORMED_FUNCTION_CALL
// prevention guidance when tools are advertised.
func BuildSystemInstructionWithToolGuard(system string, hasTools bool) *GeminiContent {
	if !hasTools {
		return BuildSystemInstruction(system)
	}
	combined := trimSpace(system) + GeminiFunctionCallGuar
	return BuildSystemInstruction(combined)
}

// SignaturePolicy selects Gemini-3 thought-signature replay behavior.
type SignaturePolicy int

const (
	// ReplayCarriedForward replays each call's own signature, carrying the
	// most recent real signature forward onto calls that lack one.
	ReplayCarriedForward SignaturePolicy = iota
	// DowngradeToolCallsToText is the recovery path for a missing
	// thought_signature 400: degrade calls and results to plain text.
	DowngradeToolCallsToText
)

// IsMissingThoughtSignatureError recognizes the backend rejection.
func IsMissingThoughtSignatureError(message string) bool {
	lowered := asciiLower(message)
	return contains(lowered, "thought_signature") || contains(lowered, "thoughtsignature")
}

func toolNameFromToolResult(toolUseID string, messages []types.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		for _, block := range messages[i].Content {
			if id, name, _, ok := block.AsToolUse(); ok && id == toolUseID {
				return name
			}
		}
	}
	return "tool"
}

// BuildContents converts a transcript into Gemini contents.
func BuildContents(messages []types.Message) []GeminiContent {
	return BuildContentsWithSignaturePolicy(messages, ReplayCarriedForward)
}

// BuildContentsWithSignaturePolicy applies the signature replay policy.
func BuildContentsWithSignaturePolicy(messages []types.Message, policy SignaturePolicy) []GeminiContent {
	var lastSignature *string
	var out []GeminiContent
	for mi := range messages {
		message := messages[mi]
		role := "user"
		if message.Role == types.RoleAssistant {
			role = "model"
		}
		var parts []GeminiPart
		for bi := range message.Content {
			block := message.Content[bi]
			switch block.Type {
			case types.BlockText:
				text := ""
				if block.Text != nil {
					text = *block.Text
				}
				parts = append(parts, GeminiPart{Text: &text})
			case types.BlockReasoning, types.BlockReasoningTrace,
				types.BlockAnthropicThink, types.BlockOpenAIReasoning:
				// Reasoning blocks have no Gemini wire form.
			case types.BlockToolUse:
				id, name, input, _ := block.AsToolUse()
				if policy == DowngradeToolCallsToText {
					// Preserve the call as readable text so the model keeps
					// its context instead of 400ing forever.
					text := "[previous tool call] " + name + "(" + string(types.NormalizeInputToObject(input)) + ")"
					parts = append(parts, GeminiPart{Text: &text})
					continue
				}
				var ownSignature *string
				if block.ThoughtSignature != nil && *block.ThoughtSignature != "" {
					sig := *block.ThoughtSignature
					ownSignature = &sig
				}
				if ownSignature != nil {
					lastSignature = ownSignature
				}
				signature := ownSignature
				if signature == nil {
					signature = lastSignature
				}
				idCopy := id
				parts = append(parts, GeminiPart{
					FunctionCall: &GeminiFunctionCall{
						Name: name,
						Args: types.NormalizeInputToObject(input),
						ID:   &idCopy,
					},
					ThoughtSignature: signature,
				})
			case types.BlockToolResult:
				toolUseID, content, isErr, _ := block.AsToolResult()
				if policy == DowngradeToolCallsToText {
					// A functionResponse without a matching signed call is
					// also rejected, so downgrade it too.
					label := "previous tool result"
					if isErr {
						label = "previous tool error"
					}
					text := "[" + label + "] " + toolNameFromToolResult(toolUseID, messages) + ": " + content
					parts = append(parts, GeminiPart{Text: &text})
					continue
				}
				idCopy := toolUseID
				var response json.RawMessage
				if isErr {
					response, _ = json.Marshal(map[string]any{"error": content})
				} else {
					response, _ = json.Marshal(map[string]any{"content": content})
				}
				parts = append(parts, GeminiPart{
					FunctionResponse: &GeminiFunctionResponse{
						Name:     toolNameFromToolResult(toolUseID, messages),
						Response: response,
						ID:       &idCopy,
					},
				})
			case types.BlockImage:
				mediaType, data := "", ""
				if block.MediaType != nil {
					mediaType = *block.MediaType
				}
				if block.Data != nil {
					data = *block.Data
				}
				parts = append(parts, GeminiPart{
					InlineData: &InlineData{MimeType: mediaType, Data: data},
				})
			default:
				// OpenAICompaction, ToolReference, ProviderNative: no wire form.
			}
		}
		if len(parts) > 0 {
			out = append(out, GeminiContent{Role: role, Parts: parts})
		}
	}
	return out
}

// BuildTools renders the function declarations; nil when no tools.
func BuildTools(tools []types.ToolDefinition) []GeminiTool {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]GeminiFunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		declarations = append(declarations, GeminiFunctionDeclaration{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  GeminiCompatibleSchema(tool.InputSchema),
		})
	}
	return []GeminiTool{{FunctionDeclarations: declarations}}
}

// GeminiCompatibleSchema normalizes a tool schema for the Gemini OpenAPI
// subset via the shared dialect.
func GeminiCompatibleSchema(raw json.RawMessage) json.RawMessage {
	return dialect.NormalizeSchemaJSON(raw, &dialect.Gemini)
}

// GeminiFallbackModels lists fallbacks excluding the current model.
func GeminiFallbackModels(currentModel string) []string {
	var out []string
	for _, candidate := range FallbackModels {
		if !asciiEqualFold(candidate, currentModel) {
			out = append(out, candidate)
		}
	}
	return out
}

// GoogleCloudProjectFromEnv reads the Code Assist project override.
func GoogleCloudProjectFromEnv() string {
	for _, variable := range []string{"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_PROJECT_ID"} {
		if value := trimSpace(osGetenv(variable)); value != "" {
			return value
		}
	}
	return ""
}

// ClientMetadataFor builds the metadata block.
func ClientMetadataFor(projectID string) ClientMetadata {
	var duet *string
	if projectID != "" {
		copy := projectID
		duet = &copy
	}
	return ClientMetadata{
		IDEType:     "IDE_UNSPECIFIED",
		Platform:    "PLATFORM_UNSPECIFIED",
		PluginType:  "GEMINI",
		DuetProject: duet,
	}
}

// LoadCodeAssistRequestFor builds the handshake request.
func LoadCodeAssistRequestFor(projectID *string, metadata ClientMetadata) LoadCodeAssistRequest {
	return LoadCodeAssistRequest{CloudaicompanionProject: projectID, Metadata: metadata}
}

// ValidateLoadCodeAssistResponse surfaces VALIDATION_REQUIRED blocks.
func ValidateLoadCodeAssistResponse(res *LoadCodeAssistResponse) error {
	if res.CurrentTier != nil {
		return nil
	}
	for _, tier := range res.IneligibleTiers {
		if tier.ReasonCode != nil && *tier.ReasonCode == "VALIDATION_REQUIRED" && tier.ValidationURL != nil {
			description := "Account validation required"
			if tier.ReasonMessage != nil {
				description = *tier.ReasonMessage
			}
			return errString(description + ". Complete account validation: " + *tier.ValidationURL)
		}
	}
	return nil
}

// IneligibleOrProjectError renders the no-project failure.
func IneligibleOrProjectError(res *LoadCodeAssistResponse) error {
	if res.PaidTier != nil || res.AllowedTiers != nil {
		return errString("Gemini Code Assist returned no cloudaicompanionProject for the account; " +
			"set provider.gemini_project (or GOOGLE_CLOUD_PROJECT) to continue")
	}
	return errString("Gemini Code Assist is not available for this account; no eligible tier was returned")
}

// ChooseOnboardTier picks the tier to onboard into.
func ChooseOnboardTier(res *LoadCodeAssistResponse) GeminiUserTier {
	if res.AllowedTiers != nil {
		for i := range res.AllowedTiers {
			tier := &res.AllowedTiers[i]
			if tier.IsDefault != nil && *tier.IsDefault {
				return *tier
			}
		}
		for i := range res.AllowedTiers {
			tier := &res.AllowedTiers[i]
			if tier.ID != nil && *tier.ID == UserTierFree {
				return *tier
			}
		}
	}
	if res.PaidTier != nil {
		return *res.PaidTier
	}
	return GeminiUserTier{ID: strPtr(UserTierFree), Name: strPtr("Free tier")}
}

// MergeGeminiModelLists orders discovered models: known catalog first, then
// sorted extras.
func MergeGeminiModelLists(models []string) []string {
	seen := map[string]bool{}
	var preferred []string
	for _, known := range AvailableModels {
		present := false
		for _, model := range models {
			if model == known {
				present = true
				break
			}
		}
		if present && !seen[known] {
			seen[known] = true
			preferred = append(preferred, known)
		}
	}
	var extras []string
	for _, model := range models {
		trimmed := trimSpace(model)
		if IsGeminiModelID(trimmed) && !seen[trimmed] {
			seen[trimmed] = true
			extras = append(extras, trimmed)
		}
	}
	sort.Strings(extras)
	return append(preferred, extras...)
}

// ExtractGeminiModelIDs walks an arbitrary JSON value collecting model ids.
func ExtractGeminiModelIDs(value any) []string {
	found := map[string]bool{}
	collectGeminiModelIDs(value, found)
	models := make([]string, 0, len(found))
	for model := range found {
		models = append(models, model)
	}
	return MergeGeminiModelLists(models)
}

func collectGeminiModelIDs(value any, found map[string]bool) {
	switch v := value.(type) {
	case string:
		trimmed := trimSpace(v)
		if IsGeminiModelID(trimmed) {
			found[trimmed] = true
		}
	case []any:
		for _, item := range v {
			collectGeminiModelIDs(item, found)
		}
	case map[string]any:
		for _, item := range v {
			collectGeminiModelIDs(item, found)
		}
	}
}

// IsGeminiModelID validates a gemini-* model identifier.
func IsGeminiModelID(value string) bool {
	trimmed := trimSpace(value)
	if trimmed == "" || !hasPrefix(trimmed, "gemini-") {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		b := trimmed[i]
		if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '.' || b == '_') {
			return false
		}
	}
	return true
}
