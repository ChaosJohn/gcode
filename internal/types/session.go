package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

// --- session persistence types (crates/jcode-session-types + the Session
// struct in crates/jcode-base/src/session.rs) -------------------------------

// ResponseStats is durable usage for one user turn. Missing telemetry is
// unknown (nil), not zero.
type ResponseStats struct {
	DurationSecs        *float64 `json:"duration_secs,omitempty"`
	InputTokens         *uint64  `json:"input_tokens,omitempty"`
	OutputTokens        *uint64  `json:"output_tokens,omitempty"`
	CacheReadTokens     *uint64  `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens *uint64  `json:"cache_creation_tokens,omitempty"`
}

// RenderedMessage is a UI-rendered transcript row.
type RenderedMessage struct {
	ResponseStats *ResponseStats `json:"response_stats,omitempty"`
	Role          string         `json:"role"`
	Content       string         `json:"content"`
	ToolCalls     []string       `json:"tool_calls"`
	ToolData      *ToolCall      `json:"tool_data,omitempty"`
	StoredIndex   *int           `json:"stored_index,omitempty"`
}

// RenderedCompactedHistoryInfo describes compacted-history rendering.
type RenderedCompactedHistoryInfo struct {
	TotalMessages     int `json:"total_messages"`
	VisibleMessages   int `json:"visible_messages"`
	RemainingMessages int `json:"remaining_messages"`
	HiddenUserPrompts int `json:"hidden_user_prompts"`
}

// RenderedImageSource mirrors the kind-tagged enum (snake_case).
type RenderedImageSource struct {
	Kind     string  `json:"kind"` // user_input | tool_result | other
	ToolName *string `json:"tool_name,omitempty"`
	Role     *string `json:"role,omitempty"`
}

// RenderedImageAnchor mirrors the kind-tagged enum (snake_case).
type RenderedImageAnchor struct {
	Kind    string  `json:"kind"` // tool_call | user_prompt
	ID      *string `json:"id,omitempty"`
	Ordinal *int    `json:"ordinal,omitempty"`
}

// RenderedImage is an image placed inline in the transcript flow.
type RenderedImage struct {
	MediaType           string               `json:"media_type"`
	Data                string               `json:"data"`
	Label               *string              `json:"label,omitempty"`
	Source              RenderedImageSource  `json:"source"`
	Anchor              *RenderedImageAnchor `json:"anchor,omitempty"`
	HistoryMessageIndex *int                 `json:"history_message_index,omitempty"`
}

// SessionStatus mirrors the externally tagged serde enum: unit variants
// serialize as bare PascalCase strings, struct variants as one-key objects.
// Default: Active (always serialized, never omitted).
type SessionStatus struct {
	Kind    string  // Active | Closed | Crashed | Reloaded | Compacted | RateLimited | Error
	Message *string // Crashed / Error payload
}

const (
	StatusActive      = "Active"
	StatusClosed      = "Closed"
	StatusCrashed     = "Crashed"
	StatusReloaded    = "Reloaded"
	StatusCompacted   = "Compacted"
	StatusRateLimited = "RateLimited"
	StatusError       = "Error"
)

var _ json.Marshaler = SessionStatus{}
var _ json.Unmarshaler = (*SessionStatus)(nil)

func (s SessionStatus) MarshalJSON() ([]byte, error) {
	switch s.Kind {
	case StatusCrashed, StatusError:
		payload := struct {
			Message *string `json:"message"`
		}{Message: s.Message}
		return json.Marshal(map[string]any{s.Kind: payload})
	case StatusActive, StatusClosed, StatusReloaded, StatusCompacted, StatusRateLimited:
		return json.Marshal(s.Kind)
	}
	return nil, fmt.Errorf("unknown session status kind %q", s.Kind)
}

func (s *SessionStatus) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, `"`) {
		var kind string
		if err := json.Unmarshal(data, &kind); err != nil {
			return err
		}
		switch kind {
		case StatusActive, StatusClosed, StatusReloaded, StatusCompacted, StatusRateLimited:
			s.Kind, s.Message = kind, nil
			return nil
		}
		return fmt.Errorf("unknown session status %q", kind)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	if len(obj) != 1 {
		return fmt.Errorf("invalid session status object with %d keys", len(obj))
	}
	for kind, payload := range obj {
		switch kind {
		case StatusCrashed, StatusError:
			var p struct {
				Message *string `json:"message"`
			}
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			s.Kind, s.Message = kind, p.Message
			return nil
		}
		return fmt.Errorf("unknown session status %q", kind)
	}
	return fmt.Errorf("empty session status object")
}

// Display mirrors SessionStatus::display.
func (s SessionStatus) Display() string {
	switch s.Kind {
	case StatusRateLimited:
		return "rate limited"
	default:
		return strings.ToLower(s.Kind[:1]) + s.Kind[1:]
	}
}

// SessionImproveMode mirrors the serde enum with aliases.
type SessionImproveMode string

const (
	ImproveRun   SessionImproveMode = "improve_run"
	ImprovePlan  SessionImproveMode = "improve_plan"
	RefactorRun  SessionImproveMode = "refactor_run"
	RefactorPlan SessionImproveMode = "refactor_plan"
)

var _ json.Unmarshaler = (*SessionImproveMode)(nil)

func (m *SessionImproveMode) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch s {
	case "improve_run", "run":
		*m = ImproveRun
	case "improve_plan", "plan":
		*m = ImprovePlan
	case "refactor_run":
		*m = RefactorRun
	case "refactor_plan":
		*m = RefactorPlan
	default:
		return fmt.Errorf("unknown session improve mode %q", s)
	}
	return nil
}

// GitState is a repository snapshot.
type GitState struct {
	Root   string  `json:"root"`
	Head   *string `json:"head"`
	Branch *string `json:"branch"`
	Dirty  *bool   `json:"dirty"`
}

// EnvSnapshot is a post-mortem environment snapshot. Field names keep the
// upstream `jcode_*` spellings for on-disk compatibility.
type EnvSnapshot struct {
	CapturedAt    JTime     `json:"captured_at"`
	Reason        string    `json:"reason"`
	SessionID     string    `json:"session_id"`
	WorkingDir    *string   `json:"working_dir"`
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	JcodeVersion  string    `json:"jcode_version"`
	JcodeGitHash  *string   `json:"jcode_git_hash"`
	JcodeGitDirty *bool     `json:"jcode_git_dirty"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	PID           uint32    `json:"pid"`
	IsSelfdev     bool      `json:"is_selfdev"`
	IsDebug       bool      `json:"is_debug"`
	IsCanary      bool      `json:"is_canary"`
	TestingBuild  *string   `json:"testing_build"`
	WorkingGit    *GitState `json:"working_git"`
}

// StoredMemoryInjection records a memory injection event for replay.
type StoredMemoryInjection struct {
	Summary       string   `json:"summary"`
	Content       string   `json:"content"`
	Count         uint32   `json:"count"`
	MemoryIDs     []string `json:"memory_ids,omitempty"`
	AgeMs         *uint64  `json:"age_ms,omitempty"`
	BeforeMessage *int     `json:"before_message,omitempty"`
	Timestamp     JTime    `json:"timestamp"`
}

// StoredDisplayRole mirrors the snake_case enum (system | background_task).
type StoredDisplayRole string

const (
	DisplayRoleSystem         StoredDisplayRole = "system"
	DisplayRoleBackgroundTask StoredDisplayRole = "background_task"
)

// StoredTokenUsage is per-message usage. prompt_tokens is absent on older
// records and cannot be reconstructed for mixed-provider totals.
type StoredTokenUsage struct {
	PromptTokens             *uint64 `json:"prompt_tokens,omitempty"`
	InputTokens              uint64  `json:"input_tokens"`
	OutputTokens             uint64  `json:"output_tokens"`
	CacheReadInputTokens     *uint64 `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *uint64 `json:"cache_creation_input_tokens,omitempty"`
}

// StoredCompactionState persists the compacted-view state.
type StoredCompactionState struct {
	SummaryText            string  `json:"summary_text"`
	OpenAIEncryptedContent *string `json:"openai_encrypted_content,omitempty"`
	CoversUpToTurn         int     `json:"covers_up_to_turn"`
	OriginalTurnCount      int     `json:"original_turn_count"`
	CompactedCount         int     `json:"compacted_count"`
}

// StoredMessage is one persisted transcript message.
type StoredMessage struct {
	ID             string             `json:"id"`
	Role           Role               `json:"role"`
	Content        []ContentBlock     `json:"content"`
	DisplayRole    *StoredDisplayRole `json:"display_role,omitempty"`
	Timestamp      *JTime             `json:"timestamp,omitempty"`
	ToolDurationMs *uint64            `json:"tool_duration_ms,omitempty"`
	TokenUsage     *StoredTokenUsage  `json:"token_usage,omitempty"`
}

// ToMessage mirrors StoredMessage::to_message.
func (m StoredMessage) ToMessage() Message {
	msg := Message{Role: m.Role, Content: m.Content, ToolDurationMs: m.ToolDurationMs}
	if m.Timestamp != nil {
		ts := *m.Timestamp
		msg.Timestamp = &ts
	}
	return msg
}

// ContentPreview mirrors StoredMessage::content_preview.
func (m StoredMessage) ContentPreview() string {
	for _, block := range m.Content {
		switch {
		case block.Type == BlockText && block.Text != nil:
			text := strings.TrimSpace(*block.Text)
			if text != "" {
				return strings.ReplaceAll(text, "\n", " ")
			}
		case block.Type == BlockToolUse && block.Name != nil:
			return fmt.Sprintf("[tool: %s]", *block.Name)
		case block.Type == BlockToolResult && block.Content != nil:
			preview := strings.ReplaceAll(strings.TrimSpace(*block.Content), "\n", " ")
			if preview != "" {
				return fmt.Sprintf("[result: %s]", preview)
			}
		}
	}
	return "(empty)"
}

// TurnStopReason mirrors the snake_case enum with #[serde(other)] Unknown.
type TurnStopReason string

const (
	StopInterrupted       TurnStopReason = "interrupted"
	StopFailure           TurnStopReason = "failure"
	StopCrash             TurnStopReason = "crash"
	StopProviderGuardrail TurnStopReason = "provider_guardrail"
	StopLimitReached      TurnStopReason = "limit_reached"
	StopUnknown           TurnStopReason = "unknown"
)

var _ json.Unmarshaler = (*TurnStopReason)(nil)

func (r *TurnStopReason) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch TurnStopReason(s) {
	case StopInterrupted, StopFailure, StopCrash, StopProviderGuardrail, StopLimitReached:
		*r = TurnStopReason(s)
	default:
		*r = StopUnknown // serde(other)
	}
	return nil
}

// ResumeTarget identifies a session to resume across agent backends.
// Externally tagged with PascalCase variant names (no rename upstream).
type ResumeTarget struct {
	Kind        string `json:"-"` // JcodeSession | ClaudeCodeSession | ...
	SessionID   string `json:"session_id,omitempty"`
	SessionPath string `json:"session_path,omitempty"`
}

var _ json.Marshaler = ResumeTarget{}
var _ json.Unmarshaler = (*ResumeTarget)(nil)

func (t ResumeTarget) MarshalJSON() ([]byte, error) {
	payload := map[string]any{}
	switch t.Kind {
	case "JcodeSession":
		payload["session_id"] = t.SessionID
	case "ClaudeCodeSession", "CodexSession", "OpenCodeSession", "CursorSession":
		payload["session_id"] = t.SessionID
		payload["session_path"] = t.SessionPath
	case "PiSession":
		payload["session_path"] = t.SessionPath
	default:
		return nil, fmt.Errorf("unknown resume target kind %q", t.Kind)
	}
	return json.Marshal(map[string]any{t.Kind: payload})
}

func (t *ResumeTarget) UnmarshalJSON(data []byte) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	if len(obj) != 1 {
		return fmt.Errorf("invalid resume target with %d keys", len(obj))
	}
	for kind, payload := range obj {
		t.Kind = kind
		switch kind {
		case "JcodeSession":
			var p struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			t.SessionID, t.SessionPath = p.SessionID, ""
		case "ClaudeCodeSession", "CodexSession", "OpenCodeSession", "CursorSession":
			var p struct {
				SessionID   string `json:"session_id"`
				SessionPath string `json:"session_path"`
			}
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			t.SessionID, t.SessionPath = p.SessionID, p.SessionPath
		case "PiSession":
			var p struct {
				SessionPath string `json:"session_path"`
			}
			if err := json.Unmarshal(payload, &p); err != nil {
				return err
			}
			t.SessionID, t.SessionPath = "", p.SessionPath
		default:
			return fmt.Errorf("unknown resume target kind %q", kind)
		}
	}
	return nil
}

// StableID mirrors ResumeTarget::stable_id.
func (t ResumeTarget) StableID() string {
	if t.Kind == "PiSession" {
		return t.SessionPath
	}
	return t.SessionID
}

// --- Session ---------------------------------------------------------------

// Session is the persisted transcript snapshot (crates/jcode-base Session).
// Field order and skip/serialize semantics mirror serde exactly: Option
// fields without skip serialize as null; Vec/map fields with
// skip_serializing_if empty are omitted when empty; plain bools with
// #[serde(default)] always serialize.
type Session struct {
	ID                  string                  `json:"id"`
	ParentID            *string                 `json:"parent_id"`
	Title               *string                 `json:"title"`
	CustomTitle         *string                 `json:"custom_title,omitempty"`
	CreatedAt           JTime                   `json:"created_at"`
	UpdatedAt           JTime                   `json:"updated_at"`
	Messages            []StoredMessage         `json:"messages"`
	SystemPrompt        *string                 `json:"system_prompt,omitempty"`
	ModelUsageTurnID    *string                 `json:"model_usage_turn_id,omitempty"`
	Compaction          *StoredCompactionState  `json:"compaction,omitempty"`
	ProviderSessionID   *string                 `json:"provider_session_id,omitempty"`
	ProviderKey         *string                 `json:"provider_key,omitempty"`
	Model               *string                 `json:"model,omitempty"`
	RouteAPIMethod      *string                 `json:"route_api_method,omitempty"`
	ReasoningEffort     *string                 `json:"reasoning_effort,omitempty"`
	SubagentModel       *string                 `json:"subagent_model,omitempty"`
	AgentModelOverrides map[string]string       `json:"agent_model_overrides,omitempty"`
	ImproveMode         *SessionImproveMode     `json:"improve_mode,omitempty"`
	AutoreviewEnabled   *bool                   `json:"autoreview_enabled,omitempty"`
	AutojudgeEnabled    *bool                   `json:"autojudge_enabled,omitempty"`
	IsCanary            bool                    `json:"is_canary"`
	TestingBuild        *string                 `json:"testing_build,omitempty"`
	WorkingDir          *string                 `json:"working_dir,omitempty"`
	ShortName           *string                 `json:"short_name,omitempty"`
	Status              SessionStatus           `json:"status"`
	LastPID             *uint32                 `json:"last_pid,omitempty"`
	LastActiveAt        *JTime                  `json:"last_active_at,omitempty"`
	IsDebug             bool                    `json:"is_debug"`
	Saved               bool                    `json:"saved"`
	SaveLabel           *string                 `json:"save_label,omitempty"`
	EnvSnapshots        []EnvSnapshot           `json:"env_snapshots,omitempty"`
	MemoryInjections    []StoredMemoryInjection `json:"memory_injections,omitempty"`
	ReplayEvents        []StoredReplayEvent     `json:"replay_events,omitempty"`
	MigrationEpoch      uint64                  `json:"migration_epoch,omitempty"`
}

// DisplayRoleString returns the display-role string or "".
func (m StoredMessage) DisplayRoleString() string {
	if m.DisplayRole == nil {
		return ""
	}
	return string(*m.DisplayRole)
}

// StoredReplayEvent is {timestamp, ...kind} with the kind enum flattened and
// tagged "event". Swarm payloads are preserved verbatim (typed in M6).
type StoredReplayEvent struct {
	Timestamp JTime           `json:"timestamp"`
	Event     string          `json:"event"` // display_message | swarm_status | swarm_plan
	Raw       json.RawMessage `json:"-"`
	// display_message payload
	Role    *string `json:"role,omitempty"`
	Title   *string `json:"title,omitempty"`
	Content *string `json:"content,omitempty"`
	// swarm payloads kept raw for verbatim replay
	Members      json.RawMessage `json:"members,omitempty"`
	SwarmID      *string         `json:"swarm_id,omitempty"`
	Version      *uint64         `json:"version,omitempty"`
	Items        json.RawMessage `json:"items,omitempty"`
	Participants []string        `json:"participants,omitempty"`
	Reason       *string         `json:"reason,omitempty"`
}

var _ json.Marshaler = StoredReplayEvent{}
var _ json.Unmarshaler = (*StoredReplayEvent)(nil)

func (e StoredReplayEvent) MarshalJSON() ([]byte, error) {
	// Field order matters for byte parity with serde: timestamp, then the
	// flattened kind enum (event first, then payload fields in declaration
	// order). Go maps randomize order, so build the object explicitly.
	ts, err := e.Timestamp.MarshalJSON()
	if err != nil {
		return nil, err
	}
	b := &jsonBuilder{}
	b.rawKey("timestamp", string(ts))
	b.strKey("event", e.Event)
	switch e.Event {
	case "display_message":
		if e.Role != nil {
			b.strKey("role", *e.Role)
		}
		if e.Title != nil {
			b.strKey("title", *e.Title)
		}
		if e.Content != nil {
			b.strKey("content", *e.Content)
		}
	case "swarm_status":
		b.rawKey("members", rawOrNull(e.Members))
	case "swarm_plan":
		b.rawKey("swarm_id", jsonStringPtr(e.SwarmID))
		b.rawKey("version", jsonUintPtr(e.Version))
		b.rawKey("items", rawOrNull(e.Items))
		if len(e.Participants) > 0 {
			p, err := json.Marshal(e.Participants)
			if err != nil {
				return nil, err
			}
			b.rawKey("participants", string(p))
		}
		if e.Reason != nil {
			b.strKey("reason", *e.Reason)
		}
	default:
		return nil, fmt.Errorf("unknown replay event kind %q", e.Event)
	}
	return b.bytes(), nil
}

func (e *StoredReplayEvent) UnmarshalJSON(data []byte) error {
	e.Raw = append(e.Raw[:0], data...)
	var probe struct {
		Timestamp JTime  `json:"timestamp"`
		Event     string `json:"event"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	e.Timestamp, e.Event = probe.Timestamp, probe.Event
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	decode := func(key string, dst any) error {
		raw, ok := fields[key]
		if !ok {
			return nil
		}
		return json.Unmarshal(raw, dst)
	}
	switch e.Event {
	case "display_message":
		var role, title, content *string
		if err := decode("role", &role); err != nil {
			return err
		}
		if err := decode("title", &title); err != nil {
			return err
		}
		if err := decode("content", &content); err != nil {
			return err
		}
		e.Role, e.Title, e.Content = role, title, content
	case "swarm_status":
		var members json.RawMessage
		if err := decode("members", &members); err != nil {
			return err
		}
		e.Members = members
	case "swarm_plan":
		var swarmID *string
		var version *uint64
		var items json.RawMessage
		var participants []string
		var reason *string
		if err := decode("swarm_id", &swarmID); err != nil {
			return err
		}
		if err := decode("version", &version); err != nil {
			return err
		}
		if err := decode("items", &items); err != nil {
			return err
		}
		if err := decode("participants", &participants); err != nil {
			return err
		}
		if err := decode("reason", &reason); err != nil {
			return err
		}
		e.SwarmID, e.Version, e.Items, e.Participants, e.Reason = swarmID, version, items, participants, reason
	default:
		return fmt.Errorf("unknown replay event kind %q", e.Event)
	}
	return nil
}

// --- title / transcription helpers (jcode-session-types) -------------------

const maxTitleChars = 64

// PromptTitle turns a user prompt into a compact one-line title; slash
// commands and wrapped-internal prompts produce no title.
func PromptTitle(prompt string) (string, bool) {
	withoutReminders := stripBlocks(prompt, "<system-reminder>", "</system-reminder>", false)
	visible := stripBlocks(withoutReminders, "<transcription>", "</transcription>", true)
	normalized := strings.Join(strings.Fields(visible), " ")
	if normalized == "" ||
		strings.HasPrefix(normalized, "/") ||
		strings.HasPrefix(normalized, "[Scheduled task]") {
		return "", false
	}
	runes := []rune(normalized)
	if len(runes) <= maxTitleChars {
		return normalized, true
	}
	title := strings.TrimRight(string(runes[:maxTitleChars]), " \t\n")
	return title + "…", true
}

// stripBlocks removes complete open..close segments; with keepInner only the
// tags are dropped. Unbalanced text is kept verbatim.
func stripBlocks(text, open, close string, keepInner bool) string {
	var out strings.Builder
	rest := text
	for {
		start := strings.Index(rest, open)
		if start < 0 {
			break
		}
		afterOpen := rest[start+len(open):]
		end := strings.Index(afterOpen, close)
		if end < 0 {
			break
		}
		out.WriteString(rest[:start])
		if keepInner {
			out.WriteString(" ")
			out.WriteString(afterOpen[:end])
			out.WriteString(" ")
		}
		rest = afterOpen[end+len(close):]
	}
	out.WriteString(rest)
	return out.String()
}

// Transcription markers shared with Jcode Desktop.
const (
	TranscriptionOpen  = "<transcription>"
	TranscriptionClose = "</transcription>"
)

// WrapTranscription wraps a transcript for the model.
func WrapTranscription(text string) string {
	return fmt.Sprintf("%s\n%s\n%s", TranscriptionOpen, strings.TrimSpace(text), TranscriptionClose)
}

// StripTranscription removes transcription tags for display. Returns the
// visible text and whether any complete tagged segment was found.
func StripTranscription(text string) (string, bool) {
	if !strings.Contains(text, TranscriptionOpen) || !strings.Contains(text, TranscriptionClose) {
		return text, false
	}
	var out strings.Builder
	rest := text
	found := false
	for {
		start := strings.Index(rest, TranscriptionOpen)
		if start < 0 {
			break
		}
		afterOpen := rest[start+len(TranscriptionOpen):]
		end := strings.Index(afterOpen, TranscriptionClose)
		if end < 0 {
			break
		}
		out.WriteString(rest[:start])
		out.WriteString(strings.Trim(afterOpen[:end], "\n"))
		rest = afterOpen[end+len(TranscriptionClose):]
		found = true
	}
	if !found {
		return text, false
	}
	out.WriteString(rest)
	return strings.TrimSpace(out.String()), true
}
