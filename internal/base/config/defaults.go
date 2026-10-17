package config

import (
	"encoding/json"
	"fmt"
	"sort"
)

func tomlErr(field string, value any) error {
	return fmt.Errorf("config field %s: expected string, got %T", field, value)
}

// ---- small sections ----

// SessionConfig (crates/jcode-config-types/src/session.rs).
type SessionConfig struct {
	HibernationIdleSecs uint64 `toml:"hibernation_idle_secs"`
}

func defaultSessionConfig() SessionConfig { return SessionConfig{HibernationIdleSecs: 30 * 60} }

// ServerConfig (crates/jcode-base/src/config.rs).
type ServerConfig struct {
	WakeMode WakeMode `toml:"wake_mode"`
}

func defaultServerConfig() ServerConfig { return ServerConfig{WakeMode: WakeInternal} }

// AcpConfig.
type AcpConfig struct {
	Profile     string `toml:"profile"`
	ToolProfile string `toml:"tool_profile"`
}

func defaultAcpConfig() AcpConfig { return AcpConfig{Profile: "standard", ToolProfile: "acp"} }

// AuthConfig.
type AuthConfig struct {
	TrustedExternalSources     []string `toml:"trusted_external_sources"`
	TrustedExternalSourcePaths []string `toml:"trusted_external_source_paths"`
}

// ToolConfig.
type ToolConfig struct {
	Profile                string       `toml:"profile"`
	Enabled                []string     `toml:"enabled"`
	Disabled               []string     `toml:"disabled"`
	DisableBaseTools       bool         `toml:"disable_base_tools"`
	McpTools               McpToolsMode `toml:"mcp_tools"`
	McpToolsTokenThreshold uint64       `toml:"mcp_tools_token_threshold"`
	Codemode               bool         `toml:"codemode"`
}

func defaultToolConfig() ToolConfig {
	return ToolConfig{McpTools: McpToolsAuto, McpToolsTokenThreshold: 8_000}
}

// DictationConfig.
type DictationConfig struct {
	Command       string         `toml:"command"`
	Mode          TranscriptMode `toml:"mode"`
	Key           string         `toml:"key"`
	TimeoutSecs   uint64         `toml:"timeout_secs"`
	Vocabulary    []string       `toml:"vocabulary"`
	Recorder      string         `toml:"recorder"`
	GlobalDevices []string       `toml:"global_devices"`
}

func defaultDictationConfig() DictationConfig {
	return DictationConfig{Mode: TranscriptSend, Key: "off", TimeoutSecs: 90}
}

// FeatureConfig.
type FeatureConfig struct {
	CheckUpdates            bool          `toml:"check_updates"`
	Memory                  bool          `toml:"memory"`
	Swarm                   bool          `toml:"swarm"`
	Mermaid                 bool          `toml:"mermaid"`
	AutoPoke                bool          `toml:"auto_poke"`
	MessageTimestamps       bool          `toml:"message_timestamps"`
	PersistMemoryInjections bool          `toml:"persist_memory_injections"`
	KvCacheMissNotices      bool          `toml:"kv_cache_miss_notices"`
	UpdateChannel           UpdateChannel `toml:"update_channel"`
}

func defaultFeatureConfig() FeatureConfig {
	return FeatureConfig{
		CheckUpdates: true, Memory: true, Swarm: true, Mermaid: true,
		AutoPoke: true, MessageTimestamps: true, PersistMemoryInjections: false,
		KvCacheMissNotices: true, UpdateChannel: ChannelStable,
	}
}

// PowerConfig.
type PowerConfig struct {
	PreventSleepWhileStreaming bool `toml:"prevent_sleep_while_streaming"`
	BlockLidClose              bool `toml:"block_lid_close"`
}

func defaultPowerConfig() PowerConfig {
	return PowerConfig{PreventSleepWhileStreaming: true, BlockLidClose: true}
}

// MenubarConfig.
type MenubarConfig struct {
	Enabled bool `toml:"enabled"`
}

func defaultMenubarConfig() MenubarConfig { return MenubarConfig{Enabled: true} }

// AutoReviewConfig.
type AutoReviewConfig struct {
	Enabled bool    `toml:"enabled"`
	Model   *string `toml:"model"`
}

// AutoJudgeConfig.
type AutoJudgeConfig struct {
	Enabled bool    `toml:"enabled"`
	Model   *string `toml:"model"`
}

// SponsorsConfig (integration discovery; legacy section name kept).
type SponsorsConfig struct {
	Enabled  bool   `toml:"enabled"`
	Endpoint string `toml:"endpoint"`
}

func defaultSponsorsConfig() SponsorsConfig {
	return SponsorsConfig{Enabled: true, Endpoint: "https://api.jcode.sh/v1/discovery"}
}

// TerminalConfig.
type TerminalConfig struct {
	SpawnHook *string `toml:"spawn_hook"`
	FocusHook *string `toml:"focus_hook"`
	Preferred *string `toml:"preferred"`
}

// GatewayConfig.
type GatewayConfig struct {
	Enabled  bool   `toml:"enabled"`
	Port     uint16 `toml:"port"`
	BindAddr string `toml:"bind_addr"`
}

func defaultGatewayConfig() GatewayConfig {
	return GatewayConfig{Port: 7643, BindAddr: "0.0.0.0"}
}

// CompactionConfig.
type CompactionConfig struct {
	Mode                       CompactionMode `toml:"mode"`
	LookaheadTurns             uint64         `toml:"lookahead_turns"`
	EwmaAlpha                  float64        `toml:"ewma_alpha"`
	ProactiveFloor             float64        `toml:"proactive_floor"`
	MinSamples                 uint64         `toml:"min_samples"`
	StallWindow                uint64         `toml:"stall_window"`
	MinTurnsBetweenCompactions uint64         `toml:"min_turns_between_compactions"`
	TopicShiftThreshold        float64        `toml:"topic_shift_threshold"`
	RelevanceKeepThreshold     float64        `toml:"relevance_keep_threshold"`
	GoalWindowTurns            uint64         `toml:"goal_window_turns"`
	MaxContextTokens           uint64         `toml:"max_context_tokens"`
}

func defaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		Mode: CompactionReactive, LookaheadTurns: 15, EwmaAlpha: 0.3,
		ProactiveFloor: 0.40, MinSamples: 3, StallWindow: 5,
		MinTurnsBetweenCompactions: 10, TopicShiftThreshold: 0.45,
		RelevanceKeepThreshold: 0.65, GoalWindowTurns: 5, MaxContextTokens: 0,
	}
}

// NativeScrollbarConfig.
type NativeScrollbarConfig struct {
	Chat      bool `toml:"chat"`
	SidePanel bool `toml:"side_panel"`
}

// DisplayConfig (crates/jcode-config-types/src/display.rs), fields in
// declaration order.
type DisplayConfig struct {
	DiffMode              DiffDisplayMode       `toml:"diff_mode"`
	ShowDiffs             *bool                 `toml:"show_diffs"`
	PinImages             bool                  `toml:"pin_images"`
	PinTodos              bool                  `toml:"pin_todos"`
	QueueMode             bool                  `toml:"queue_mode"`
	AutoServerReload      bool                  `toml:"auto_server_reload"`
	MouseCapture          bool                  `toml:"mouse_capture"`
	DebugSocket           bool                  `toml:"debug_socket"`
	Emoji                 bool                  `toml:"emoji"`
	Centered              bool                  `toml:"centered"`
	ShowThinking          bool                  `toml:"show_thinking"`
	ReasoningDisplaySet   *ReasoningDisplayMode `toml:"reasoning_display"`
	DiagramMode           DiagramDisplayMode    `toml:"diagram_mode"`
	MarkdownSpacing       MarkdownSpacingMode   `toml:"markdown_spacing"`
	LatexRendering        LatexRenderingMode    `toml:"latex_rendering"`
	IdleAnimation         bool                  `toml:"idle_animation"`
	PromptEntryAnimation  bool                  `toml:"prompt_entry_animation"`
	DisabledAnimations    []string              `toml:"disabled_animations"`
	Performance           string                `toml:"performance"`
	AnimationFps          uint64                `toml:"animation_fps"`
	RedrawFps             uint64                `toml:"redraw_fps"`
	PromptPreview         bool                  `toml:"prompt_preview"`
	CompactNotifications  bool                  `toml:"compact_notifications"`
	CopyBadgeAltLabel     string                `toml:"copy_badge_alt_label"`
	ShowAgentgrepOutput   bool                  `toml:"show_agentgrep_output"`
	ShowBashOutput        bool                  `toml:"show_bash_output"`
	ToolCallDetails       bool                  `toml:"tool_call_details"`
	NativeScrollbars      NativeScrollbarConfig `toml:"native_scrollbars"`
	KeybindingHints       bool                  `toml:"keybinding_hints"`
	Theme                 string                `toml:"theme"`
	Colors                map[string]string     `toml:"colors"`
	ActiveSessionsManager bool                  `toml:"active_sessions_manager"`
	ExternalSessions      bool                  `toml:"external_sessions"`
	ResumeProjectScope    bool                  `toml:"resume_project_scope"`
	UsageDisplay          string                `toml:"usage_display"`
}

func defaultDisplayConfig() DisplayConfig {
	full := ReasoningFull
	return DisplayConfig{
		DiffMode: DiffInline, PinImages: true, PinTodos: true,
		AutoServerReload: true, MouseCapture: true, DebugSocket: false,
		Emoji: true, Centered: false, ShowThinking: true,
		ReasoningDisplaySet: &full, DiagramMode: DiagramNone,
		MarkdownSpacing: SpacingCompact, LatexRendering: LatexImage,
		IdleAnimation: false, PromptEntryAnimation: true,
		AnimationFps: 60, RedrawFps: 60, PromptPreview: true,
		CompactNotifications: false, ShowAgentgrepOutput: false,
		ShowBashOutput: false, ToolCallDetails: false,
		NativeScrollbars: NativeScrollbarConfig{Chat: true, SidePanel: true},
		KeybindingHints:  true, Colors: map[string]string{},
		ActiveSessionsManager: false, ExternalSessions: true,
		ResumeProjectScope: false, UsageDisplay: "left",
	}
}

// HasExplicitReasoningDisplay reports whether the user chose a reasoning
// display mode (ported from DisplayConfig::has_explicit_reasoning_display;
// front-ends apply their own default only when absent).
func (d *DisplayConfig) HasExplicitReasoningDisplay() bool { return d.ReasoningDisplaySet != nil }

// ReasoningDisplay resolves the effective mode: an explicit setting wins;
// otherwise the legacy show_thinking flag maps to Full (ported from
// DisplayConfig::reasoning_display).
func (d *DisplayConfig) ReasoningDisplay() ReasoningDisplayMode {
	if d.ReasoningDisplaySet != nil {
		return *d.ReasoningDisplaySet
	}
	if d.ShowThinking {
		return ReasoningFull
	}
	return ReasoningOff
}

// SetReasoningDisplay stores the explicit mode and keeps show_thinking in
// sync (any active mode requests reasoning from the provider).
func (d *DisplayConfig) SetReasoningDisplay(mode ReasoningDisplayMode) {
	d.ReasoningDisplaySet = &mode
	d.ShowThinking = mode != ReasoningOff
}

// ApplyLegacyCompat maps the removed `show_diffs = false` onto diff_mode
// (ported from apply_legacy_compat).
func (d *DisplayConfig) ApplyLegacyCompat() {
	if d.ShowDiffs != nil && !*d.ShowDiffs && d.DiffMode == DiffInline {
		d.DiffMode = DiffOff
	}
}

// ---- provider ----

// ProviderConfig (crates/jcode-config-types/src/lib.rs).
type ProviderConfig struct {
	DefaultModel                          *string                   `toml:"default_model"`
	DefaultProvider                       *string                   `toml:"default_provider"`
	OpenAIReasoningEffort                 *string                   `toml:"openai_reasoning_effort"`
	AnthropicReasoningEffort              *string                   `toml:"anthropic_reasoning_effort"`
	AnthropicCacheTTL1h                   bool                      `toml:"anthropic_cache_ttl_1h"`
	OpenAITransport                       *string                   `toml:"openai_transport"`
	OpenAIServiceTier                     *string                   `toml:"openai_service_tier"`
	OpenAINativeCompactionMode            string                    `toml:"openai_native_compaction_mode"`
	OpenAINativeCompactionThresholdTokens uint64                    `toml:"openai_native_compaction_threshold_tokens"`
	PreserveReasoningContext              bool                      `toml:"preserve_reasoning_context"`
	CrossProviderFailover                 CrossProviderFailoverMode `toml:"cross_provider_failover"`
	SameProviderAccountFailover           bool                      `toml:"same_provider_account_failover"`
	CopilotPremium                        *string                   `toml:"copilot_premium"`
	GeminiForceOAuth                      bool                      `toml:"gemini_force_oauth"`
	GeminiProject                         *string                   `toml:"gemini_project"`
	ModelPickerProviders                  []string                  `toml:"model_picker_providers"`
	StreamIdleTimeoutSecs                 uint64                    `toml:"stream_idle_timeout_secs"`
	MaxRetries                            uint32                    `toml:"max_retries"`
	RetryBackoffCapSecs                   uint64                    `toml:"retry_backoff_cap_secs"`
}

func defaultProviderConfig() ProviderConfig {
	low := "low"
	priority := "priority"
	return ProviderConfig{
		OpenAIReasoningEffort:                 &low,
		AnthropicCacheTTL1h:                   true,
		OpenAIServiceTier:                     &priority,
		OpenAINativeCompactionMode:            "auto",
		OpenAINativeCompactionThresholdTokens: 200_000,
		PreserveReasoningContext:              true,
		CrossProviderFailover:                 FailoverCountdown,
		SameProviderAccountFailover:           true,
		StreamIdleTimeoutSecs:                 180,
		MaxRetries:                            8,
		RetryBackoffCapSecs:                   30,
	}
}

// NamedProviderModelConfig.
type NamedProviderModelConfig struct {
	ID              string  `toml:"id"`
	Reasoning       *bool   `toml:"reasoning"`
	ReasoningEffort *string `toml:"reasoning_effort"`
	ContextWindow   *uint64 `toml:"context_window"`
	Input           *string `toml:"input"`
}

// NamedProviderConfig.
type NamedProviderConfig struct {
	ProviderType               NamedProviderType          `toml:"provider_type"`
	BaseURL                    string                     `toml:"base_url"`
	API                        *string                    `toml:"api"`
	Auth                       NamedProviderAuth          `toml:"auth"`
	AuthHeader                 *string                    `toml:"auth_header"`
	Headers                    map[string]string          `toml:"headers"`
	APIKeyEnv                  *string                    `toml:"api_key_env"`
	APIKey                     *string                    `toml:"api_key"`
	EnvFile                    *string                    `toml:"env_file"`
	DefaultModel               *string                    `toml:"default_model"`
	RequiresAPIKey             *bool                      `toml:"requires_api_key"`
	ProviderRouting            bool                       `toml:"provider_routing"`
	ModelCatalog               bool                       `toml:"model_catalog"`
	AllowProviderPinning       bool                       `toml:"allow_provider_pinning"`
	Models                     []NamedProviderModelConfig `toml:"models"`
	ExtraBody                  map[string]any             `toml:"extra_body"`
	SupportsReasoningEffort    *bool                      `toml:"supports_reasoning_effort"`
	DisableReasoningHeuristics bool                       `toml:"disable_reasoning_heuristics"`
}

// ---- agents ----

// AgentsConfig.
type AgentsConfig struct {
	SwarmModel               *string          `toml:"swarm_model"`
	SwarmEffort              *string          `toml:"swarm_effort"`
	SwarmRootEffort          *string          `toml:"swarm_root_effort"`
	SwarmDeepRootEffort      *string          `toml:"swarm_deep_root_effort"`
	SwarmSpawnMode           SwarmSpawnMode   `toml:"swarm_spawn_mode"`
	SwarmGalleryMaxPct       *uint64          `toml:"swarm_gallery_max_pct"`
	SwarmStripLayout         SwarmStripLayout `toml:"swarm_strip_layout"`
	MemoryJevProvider        string           `toml:"memory_jev_provider"`
	MemoryJevThreshold       float32          `toml:"memory_jev_threshold"`
	MemoryModel              *string          `toml:"memory_model"`
	MemorySidecarEnabled     bool             `toml:"memory_sidecar_enabled"`
	MemoryRerankCadence      int              `toml:"memory_rerank_cadence"`
	MemoryRerankVotes        int              `toml:"memory_rerank_votes"`
	MemoryRerankMinAgree     int              `toml:"memory_rerank_min_agree"`
	MemoryEmbeddingBackend   string           `toml:"memory_embedding_backend"`
	MemoryEmbeddingModel     *string          `toml:"memory_embedding_model"`
	MemoryEmbeddingBaseURL   *string          `toml:"memory_embedding_base_url"`
	MemoryEmbeddingDim       *int             `toml:"memory_embedding_dim"`
	SwarmMaxConcurrentAgents int              `toml:"swarm_max_concurrent_agents"`
	SwarmStallAfterSecs      uint64           `toml:"swarm_stall_after_secs"`
	SwarmStallNudge          bool             `toml:"swarm_stall_nudge"`
}

func defaultAgentsConfig() AgentsConfig {
	return AgentsConfig{
		SwarmSpawnMode:           SwarmSpawnInline,
		SwarmStripLayout:         SwarmStripVertical,
		MemoryJevProvider:        "auto",
		MemoryJevThreshold:       0.8,
		MemorySidecarEnabled:     true,
		MemoryRerankCadence:      3,
		MemoryRerankVotes:        2,
		MemoryRerankMinAgree:     2,
		MemoryEmbeddingBackend:   "local",
		SwarmMaxConcurrentAgents: 32,
		SwarmStallAfterSecs:      300,
		SwarmStallNudge:          false,
	}
}

// ---- hooks ----

// HookCommands holds one or many commands; a single command serializes as a
// bare string (ported custom serde impl). Decoding accepts either form: the
// doc walker wraps bare strings, so this is a plain slice by decode time.
type HookCommands []string

func (h HookCommands) First() (string, bool) {
	if len(h) == 0 {
		return "", false
	}
	return h[0], true
}

// MarshalTOML implements unstable.Marshaler: one command is a bare string,
// several are an array (serde does the same).
func (h HookCommands) MarshalTOML() ([]byte, error) {
	if len(h) == 1 {
		encoded, _ := json.Marshal(h[0])
		return encoded, nil
	}
	return json.Marshal([]string(h))
}

// hookKeys are the [hooks] fields accepting a string or a list of strings.
var hookKeys = map[string]struct{}{
	"pre_tool_transform": {}, "turn_start": {}, "turn_end": {},
	"session_start": {}, "session_end": {}, "pre_tool": {},
	"post_tool": {}, "post_tool_feedback": {},
}

// normalizeHooks wraps bare-string hook commands into one-element arrays so
// the plain-slice decode accepts both spellings.
func normalizeHooks(table map[string]any) {
	for key, value := range table {
		if _, isHook := hookKeys[key]; !isHook {
			continue
		}
		if _, isString := value.(string); isString {
			table[key] = []any{value}
		}
	}
}

// HooksConfig.
type HooksConfig struct {
	PreToolTransform          HookCommands `toml:"pre_tool_transform,omitempty"`
	PreToolTransformTimeoutMs uint64       `toml:"pre_tool_transform_timeout_ms"`
	TurnStart                 HookCommands `toml:"turn_start,omitempty"`
	TurnEnd                   HookCommands `toml:"turn_end,omitempty"`
	SessionStart              HookCommands `toml:"session_start,omitempty"`
	SessionEnd                HookCommands `toml:"session_end,omitempty"`
	PreTool                   HookCommands `toml:"pre_tool,omitempty"`
	PostTool                  HookCommands `toml:"post_tool,omitempty"`
	PostToolFeedback          HookCommands `toml:"post_tool_feedback,omitempty"`
	PostToolFeedbackTimeoutMs uint64       `toml:"post_tool_feedback_timeout_ms"`
	PreToolTimeoutMs          uint64       `toml:"pre_tool_timeout_ms"`
}

func defaultHooksConfig() HooksConfig {
	return HooksConfig{
		PreToolTransformTimeoutMs: 500,
		PostToolFeedbackTimeoutMs: 10_000,
		PreToolTimeoutMs:          5000,
	}
}

// ---- ambient / notifications / safety ----

// AmbientConfig.
type AmbientConfig struct {
	Enabled              bool    `toml:"enabled"`
	Provider             *string `toml:"provider"`
	Model                *string `toml:"model"`
	AllowAPIKeys         bool    `toml:"allow_api_keys"`
	APIDailyBudget       *uint64 `toml:"api_daily_budget"`
	MinIntervalMinutes   uint32  `toml:"min_interval_minutes"`
	MaxIntervalMinutes   uint32  `toml:"max_interval_minutes"`
	PauseOnActiveSession bool    `toml:"pause_on_active_session"`
	ProactiveWork        bool    `toml:"proactive_work"`
	WorkBranchPrefix     string  `toml:"work_branch_prefix"`
	Visible              bool    `toml:"visible"`
}

func defaultAmbientConfig() AmbientConfig {
	return AmbientConfig{
		MinIntervalMinutes: 5, MaxIntervalMinutes: 120,
		PauseOnActiveSession: true, ProactiveWork: true,
		WorkBranchPrefix: "ambient/", Visible: true,
	}
}

// NotificationsConfig.
type NotificationsConfig struct {
	TurnComplete                  bool   `toml:"turn_complete"`
	TurnCompleteMinSecs           uint64 `toml:"turn_complete_min_secs"`
	TurnCompleteTodoMinSecs       uint64 `toml:"turn_complete_todo_min_secs"`
	TurnCompleteOnlyWhenUnfocused bool   `toml:"turn_complete_only_when_unfocused"`
	TurnCompleteSound             string `toml:"turn_complete_sound"`
}

func defaultNotificationsConfig() NotificationsConfig {
	return NotificationsConfig{
		TurnComplete: true, TurnCompleteMinSecs: 120,
		TurnCompleteTodoMinSecs: 30, TurnCompleteOnlyWhenUnfocused: true,
		TurnCompleteSound: "Glass",
	}
}

// SafetyConfig.
type SafetyConfig struct {
	NtfyTopic                 *string `toml:"ntfy_topic"`
	NtfyServer                string  `toml:"ntfy_server"`
	DesktopNotifications      bool    `toml:"desktop_notifications"`
	EmailEnabled              bool    `toml:"email_enabled"`
	EmailTo                   *string `toml:"email_to"`
	EmailSMTPHost             *string `toml:"email_smtp_host"`
	EmailSMTPPort             uint16  `toml:"email_smtp_port"`
	EmailFrom                 *string `toml:"email_from"`
	EmailPassword             *string `toml:"email_password"`
	EmailIMAPHost             *string `toml:"email_imap_host"`
	EmailIMAPPort             uint16  `toml:"email_imap_port"`
	EmailReplyEnabled         bool    `toml:"email_reply_enabled"`
	TelegramEnabled           bool    `toml:"telegram_enabled"`
	TelegramBotToken          *string `toml:"telegram_bot_token"`
	TelegramChatID            *string `toml:"telegram_chat_id"`
	TelegramReplyEnabled      bool    `toml:"telegram_reply_enabled"`
	DiscordEnabled            bool    `toml:"discord_enabled"`
	DiscordBotToken           *string `toml:"discord_bot_token"`
	DiscordChannelID          *string `toml:"discord_channel_id"`
	DiscordBotUserID          *string `toml:"discord_bot_user_id"`
	DiscordReplyEnabled       bool    `toml:"discord_reply_enabled"`
	JadeRelayEnabled          bool    `toml:"jade_relay_enabled"`
	JadeRelayAPIBase          *string `toml:"jade_relay_api_base"`
	JadeRelayToken            *string `toml:"jade_relay_token"`
	JadeRelayTokenID          *string `toml:"jade_relay_token_id"`
	JadeRelayUserID           *string `toml:"jade_relay_user_id"`
	JadeRelaySessionID        *string `toml:"jade_relay_session_id"`
	JadeRelayReplyEnabled     bool    `toml:"jade_relay_reply_enabled"`
	JadeRelayLaunchEnabled    bool    `toml:"jade_relay_launch_enabled"`
	JadeRelayLaunchWorkingDir *string `toml:"jade_relay_launch_working_dir"`
}

func defaultSafetyConfig() SafetyConfig {
	return SafetyConfig{
		NtfyServer: "https://ntfy.sh", DesktopNotifications: true,
		EmailSMTPPort: 587, EmailIMAPPort: 993,
	}
}

// ---- web search ----

// CustomWebSearchConfig.
type CustomWebSearchConfig struct {
	Endpoint       string                `toml:"endpoint"`
	Method         CustomWebSearchMethod `toml:"method"`
	QueryParam     string                `toml:"query_param"`
	CountParam     *string               `toml:"count_param"`
	Params         map[string]any        `toml:"params"`
	Body           map[string]any        `toml:"body"`
	Headers        map[string]string     `toml:"headers"`
	HeaderEnv      map[string]string     `toml:"header_env"`
	ResultsPointer string                `toml:"results_pointer"`
	TitlePointer   string                `toml:"title_pointer"`
	URLPointer     string                `toml:"url_pointer"`
	SnippetPointer string                `toml:"snippet_pointer"`
}

// WebSearchConfig. DEFAULT_NATIVE_WEB_SEARCH_MAX_USES = 5 and
// DEFAULT_ANTHROPIC_WEB_SEARCH_TOOL = "web_search_20250305" (upstream consts).
type WebSearchConfig struct {
	Custom                     *CustomWebSearchConfig `toml:"custom"`
	Engine                     WebSearchEngine        `toml:"engine"`
	FallbackEngines            []WebSearchEngine      `toml:"fallback_engines"`
	BingAPIKey                 *string                `toml:"bing_api_key"`
	BingAPIKeyEnv              string                 `toml:"bing_api_key_env"`
	BingMarket                 string                 `toml:"bing_market"`
	SearxngURL                 *string                `toml:"searxng_url"`
	SearxngURLEnv              string                 `toml:"searxng_url_env"`
	PreferNative               bool                   `toml:"prefer_native"`
	NativeMaxUses              *uint64                `toml:"native_max_uses"`
	NativeAllowedDomains       []string               `toml:"native_allowed_domains"`
	NativeBlockedDomains       []string               `toml:"native_blocked_domains"`
	NativeAnthropicToolVersion string                 `toml:"native_anthropic_tool_version"`
}

const (
	DefaultNativeWebSearchMaxUses = 5
	DefaultAnthropicWebSearchTool = "web_search_20250305"
)

func defaultWebSearchConfig() WebSearchConfig {
	fallback := WebSearchBing
	maxUses := uint64(DefaultNativeWebSearchMaxUses)
	return WebSearchConfig{
		Engine: WebSearchDuckDuckGo, FallbackEngines: []WebSearchEngine{fallback},
		BingAPIKeyEnv: "JCODE_BING_API_KEY", BingMarket: "en-US",
		SearxngURLEnv: "JCODE_SEARXNG_URL", PreferNative: true,
		NativeMaxUses:              &maxUses,
		NativeAnthropicToolVersion: DefaultAnthropicWebSearchTool,
	}
}

// ---- launch hotkeys ----

// LaunchHotkeyEntry.
type LaunchHotkeyEntry struct {
	Chord   string `toml:"chord"`
	Dir     string `toml:"dir"`
	Label   string `toml:"label"`
	SelfDev *bool  `toml:"self_dev"`
}

// LaunchHotkeysConfig.
type LaunchHotkeysConfig struct {
	Enabled  *bool               `toml:"enabled"`
	Entries  []LaunchHotkeyEntry `toml:"entries"`
	Imported bool                `toml:"imported"`
}

// ---- keybindings ----

// KeybindingsConfig carries the resolved chord per action. Defaults resolve
// per-platform from keybindingDefaults (the single source of truth ported
// from keybindings.rs), falling back to the field's static fallback.
type KeybindingsConfig struct {
	ScrollUp                    string                    `toml:"scroll_up"`
	ScrollDown                  string                    `toml:"scroll_down"`
	ScrollPageUp                string                    `toml:"scroll_page_up"`
	ScrollPageDown              string                    `toml:"scroll_page_down"`
	ModelSwitchNext             string                    `toml:"model_switch_next"`
	ModelSwitchPrev             string                    `toml:"model_switch_prev"`
	FallbackSwitch              string                    `toml:"fallback_switch"`
	EffortIncrease              string                    `toml:"effort_increase"`
	EffortDecrease              string                    `toml:"effort_decrease"`
	SpeedIncrease               string                    `toml:"speed_increase"`
	SpeedDecrease               string                    `toml:"speed_decrease"`
	CenteredToggle              string                    `toml:"centered_toggle"`
	ScrollPromptUp              string                    `toml:"scroll_prompt_up"`
	ScrollPromptDown            string                    `toml:"scroll_prompt_down"`
	ScrollBookmark              string                    `toml:"scroll_bookmark"`
	AutoPokeToggle              string                    `toml:"auto_poke_toggle"`
	ScrollUpFallback            string                    `toml:"scroll_up_fallback"`
	ScrollDownFallback          string                    `toml:"scroll_down_fallback"`
	WorkspaceLeft               string                    `toml:"workspace_left"`
	WorkspaceDown               string                    `toml:"workspace_down"`
	WorkspaceUp                 string                    `toml:"workspace_up"`
	WorkspaceRight              string                    `toml:"workspace_right"`
	SidePanelToggle             string                    `toml:"side_panel_toggle"`
	CopySelectionToggle         string                    `toml:"copy_selection_toggle"`
	DiagramPaneToggle           string                    `toml:"diagram_pane_toggle"`
	DiagramPaneVisibilityToggle string                    `toml:"diagram_pane_visibility_toggle"`
	TypingScrollLockToggle      string                    `toml:"typing_scroll_lock_toggle"`
	DiffModeCycle               string                    `toml:"diff_mode_cycle"`
	CommitLog                   string                    `toml:"commit_log"`
	InfoWidgetToggle            string                    `toml:"info_widget_toggle"`
	TodoCardToggle              string                    `toml:"todo_card_toggle"`
	SwarmPanelFocus             string                    `toml:"swarm_panel_focus"`
	NewTerminal                 string                    `toml:"new_terminal"`
	OpenResume                  string                    `toml:"open_resume"`
	VoiceInput                  string                    `toml:"voice_input"`
	SessionPickerEnter          SessionPickerResumeAction `toml:"session_picker_enter"`
}

// keybindingDefault is one entry of the upstream table: macos and other
// platform bindings ("dev" provenance; unbound = "").
type keybindingDefault struct {
	macos string
	other string
}

// keybindingDefaults ports the KEYBINDING_DEFAULTS table from keybindings.rs
// (dev-provenance entries only; the four upstream "example" entries are
// documentation samples and are not carried over).
var keybindingDefaults = map[string]keybindingDefault{
	"scroll_up":                      {"ctrl+shift+k", "ctrl+shift+k"},
	"scroll_down":                    {"ctrl+shift+j", "ctrl+shift+j"},
	"scroll_page_up":                 {"alt+u", "alt+u"},
	"scroll_page_down":               {"alt+d", "alt+d"},
	"model_switch_next":              {"ctrl+tab", "ctrl+tab"},
	"model_switch_prev":              {"ctrl+shift+tab", "ctrl+shift+tab"},
	"fallback_switch":                {"ctrl+y", "ctrl+y"},
	"effort_increase":                {"cmd+right", "alt+right"},
	"effort_decrease":                {"cmd+left", "alt+left"},
	"speed_increase":                 {"alt+up", "alt+up"},
	"speed_decrease":                 {"alt+down", "alt+down"},
	"centered_toggle":                {"alt+c", "alt+c"},
	"scroll_prompt_up":               {"ctrl+k", "ctrl+k"},
	"scroll_prompt_down":             {"ctrl+j", "ctrl+j"},
	"scroll_bookmark":                {"ctrl+g", "ctrl+g"},
	"auto_poke_toggle":               {"ctrl+p", "ctrl+p"},
	"scroll_up_fallback":             {"", ""},
	"scroll_down_fallback":           {"", ""},
	"workspace_left":                 {"alt+h", "alt+h"},
	"workspace_down":                 {"alt+j", "alt+j"},
	"workspace_up":                   {"alt+k", "alt+k"},
	"workspace_right":                {"alt+l", "alt+l"},
	"side_panel_toggle":              {"alt+m", "alt+m"},
	"copy_selection_toggle":          {"alt+y", "alt+y"},
	"diagram_pane_toggle":            {"alt+t", "alt+t"},
	"diagram_pane_visibility_toggle": {"alt+shift+m", "alt+shift+m"},
	"typing_scroll_lock_toggle":      {"alt+s", "alt+s"},
	"diff_mode_cycle":                {"", ""},
	"commit_log":                     {"alt+g", "alt+g"},
	"info_widget_toggle":             {"alt+i", "alt+i"},
	"todo_card_toggle":               {"alt+x", "alt+x"},
	"swarm_panel_focus":              {"alt+n", "alt+n"},
	"new_terminal":                   {"cmd+shift+;", "alt+shift+;"},
	"open_resume":                    {"cmd+b", "alt+r"},
	"voice_input":                    {"ctrl+space", "ctrl+space"},
}

// DefaultKeybindings resolves the table for a platform ("macos"/"other").
func DefaultKeybindings(platform string) KeybindingsConfig {
	get := func(id, fallback string) string {
		if entry, ok := keybindingDefaults[id]; ok {
			if platform == "macos" {
				return entry.macos
			}
			return entry.other
		}
		return fallback
	}
	_ = get // table entries above already embed the resolved fallbacks
	return KeybindingsConfig{
		ScrollUp:                    get("scroll_up", "ctrl+k"),
		ScrollDown:                  get("scroll_down", "ctrl+j"),
		ScrollPageUp:                get("scroll_page_up", "alt+u"),
		ScrollPageDown:              get("scroll_page_down", "alt+d"),
		ModelSwitchNext:             get("model_switch_next", "ctrl+tab"),
		ModelSwitchPrev:             get("model_switch_prev", "ctrl+shift+tab"),
		FallbackSwitch:              get("fallback_switch", "ctrl+y"),
		EffortIncrease:              get("effort_increase", "alt+right"),
		EffortDecrease:              get("effort_decrease", "alt+left"),
		SpeedIncrease:               get("speed_increase", "alt+up"),
		SpeedDecrease:               get("speed_decrease", "alt+down"),
		CenteredToggle:              get("centered_toggle", "alt+c"),
		ScrollPromptUp:              get("scroll_prompt_up", "ctrl+["),
		ScrollPromptDown:            get("scroll_prompt_down", "ctrl+]"),
		ScrollBookmark:              get("scroll_bookmark", "ctrl+g"),
		AutoPokeToggle:              get("auto_poke_toggle", "ctrl+p"),
		ScrollUpFallback:            get("scroll_up_fallback", ""),
		ScrollDownFallback:          get("scroll_down_fallback", ""),
		WorkspaceLeft:               get("workspace_left", "alt+h"),
		WorkspaceDown:               get("workspace_down", "alt+j"),
		WorkspaceUp:                 get("workspace_up", "alt+k"),
		WorkspaceRight:              get("workspace_right", "alt+l"),
		SidePanelToggle:             get("side_panel_toggle", "alt+m"),
		CopySelectionToggle:         get("copy_selection_toggle", "alt+y"),
		DiagramPaneToggle:           get("diagram_pane_toggle", "alt+t"),
		DiagramPaneVisibilityToggle: get("diagram_pane_visibility_toggle", "alt+shift+m"),
		TypingScrollLockToggle:      get("typing_scroll_lock_toggle", "alt+s"),
		DiffModeCycle:               get("diff_mode_cycle", ""),
		CommitLog:                   get("commit_log", "alt+g"),
		InfoWidgetToggle:            get("info_widget_toggle", "alt+i"),
		TodoCardToggle:              get("todo_card_toggle", "alt+x"),
		SwarmPanelFocus:             get("swarm_panel_focus", "alt+n"),
		NewTerminal:                 get("new_terminal", ""),
		OpenResume:                  get("open_resume", "alt+r"),
		VoiceInput:                  get("voice_input", "ctrl+space"),
		SessionPickerEnter:          ResumeCurrentTerminal,
	}
}

// SortedExtraKeys returns Extra table names in stable order (tests).
func (c *Config) SortedExtraKeys() []string {
	keys := make([]string, 0, len(c.Extra))
	for k := range c.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
