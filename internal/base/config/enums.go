package config

import (
	"runtime"
	"strings"
)

// runtimePlatform reports the keybinding platform ("macos" or "other"),
// mirroring KeybindingPlatform::current().
func runtimePlatform() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return "other"
}

func parseLower(value string, options map[string]string) (string, bool) {
	if canon, ok := options[strings.ToLower(strings.TrimSpace(value))]; ok {
		return canon, true
	}
	return "", false
}

// ---- enums ----

type CompactionMode string

const (
	CompactionReactive  CompactionMode = "reactive"
	CompactionProactive CompactionMode = "proactive"
	CompactionSemantic  CompactionMode = "semantic"
)

func ParseCompactionMode(s string) (CompactionMode, bool) {
	canon, ok := parseLower(s, map[string]string{
		"reactive": string(CompactionReactive), "proactive": string(CompactionProactive), "semantic": string(CompactionSemantic),
	})
	return CompactionMode(canon), ok
}

func (m CompactionMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *CompactionMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseCompactionMode(string(data)); ok {
		*m = parsed
	} else {
		*m = CompactionReactive // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type SessionPickerResumeAction string

const (
	ResumeNewTerminal     SessionPickerResumeAction = "new-terminal"
	ResumeCurrentTerminal SessionPickerResumeAction = "current-terminal"
)

func ParseSessionPickerResumeAction(s string) (SessionPickerResumeAction, bool) {
	canon, ok := parseLower(s, map[string]string{
		"new-terminal": string(ResumeNewTerminal), "current-terminal": string(ResumeCurrentTerminal),
	})
	return SessionPickerResumeAction(canon), ok
}

func (a SessionPickerResumeAction) MarshalText() ([]byte, error) { return []byte(a), nil }
func (a *SessionPickerResumeAction) UnmarshalText(data []byte) error {
	if parsed, ok := ParseSessionPickerResumeAction(string(data)); ok {
		*a = parsed
	} else {
		*a = ResumeCurrentTerminal // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type DiffDisplayMode string

const (
	DiffOff       DiffDisplayMode = "off"
	DiffInline    DiffDisplayMode = "inline"
	DiffFullInine DiffDisplayMode = "full-inline"
	DiffFile      DiffDisplayMode = "file"
)

func ParseDiffDisplayMode(s string) (DiffDisplayMode, bool) {
	canon, ok := parseLower(s, map[string]string{
		"off": string(DiffOff), "inline": string(DiffInline),
		"full-inline": string(DiffFullInine), "file": string(DiffFile),
	})
	return DiffDisplayMode(canon), ok
}

func (m DiffDisplayMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *DiffDisplayMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseDiffDisplayMode(string(data)); ok {
		*m = parsed
	} else {
		*m = DiffInline // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type DiagramDisplayMode string

const (
	DiagramNone   DiagramDisplayMode = "none"
	DiagramMargin DiagramDisplayMode = "margin"
	DiagramPinned DiagramDisplayMode = "pinned"
)

func ParseDiagramDisplayMode(s string) (DiagramDisplayMode, bool) {
	// `inline`/`off` are accepted spellings of None (issue #689).
	canon, ok := parseLower(s, map[string]string{
		"none": string(DiagramNone), "inline": string(DiagramNone), "off": string(DiagramNone),
		"margin": string(DiagramMargin), "pinned": string(DiagramPinned),
	})
	return DiagramDisplayMode(canon), ok
}

func (m DiagramDisplayMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *DiagramDisplayMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseDiagramDisplayMode(string(data)); ok {
		*m = parsed
	} else {
		*m = DiagramNone // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type DiagramPanePosition string

const (
	DiagramPaneSide DiagramPanePosition = "side"
	DiagramPaneTop  DiagramPanePosition = "top"
)

func ParseDiagramPanePosition(s string) (DiagramPanePosition, bool) {
	canon, ok := parseLower(s, map[string]string{
		"side": string(DiagramPaneSide), "top": string(DiagramPaneTop),
	})
	return DiagramPanePosition(canon), ok
}

func (p DiagramPanePosition) MarshalText() ([]byte, error) { return []byte(p), nil }
func (p *DiagramPanePosition) UnmarshalText(data []byte) error {
	if parsed, ok := ParseDiagramPanePosition(string(data)); ok {
		*p = parsed
	} else {
		*p = DiagramPaneSide // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type MarkdownSpacingMode string

const (
	SpacingCompact  MarkdownSpacingMode = "compact"
	SpacingDocument MarkdownSpacingMode = "document"
)

func ParseMarkdownSpacingMode(s string) (MarkdownSpacingMode, bool) {
	canon, ok := parseLower(s, map[string]string{
		"compact": string(SpacingCompact), "document": string(SpacingDocument),
	})
	return MarkdownSpacingMode(canon), ok
}

func (m MarkdownSpacingMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *MarkdownSpacingMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseMarkdownSpacingMode(string(data)); ok {
		*m = parsed
	} else {
		*m = SpacingCompact // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type LatexRenderingMode string

const (
	LatexNone    LatexRenderingMode = "none"
	LatexUnicode LatexRenderingMode = "unicode"
	LatexImage   LatexRenderingMode = "image"
)

func ParseLatexRenderingMode(s string) (LatexRenderingMode, bool) {
	canon, ok := parseLower(s, map[string]string{
		"none": string(LatexNone), "raw": string(LatexNone), "off": string(LatexNone),
		"unicode": string(LatexUnicode), "terminal": string(LatexUnicode), "text": string(LatexUnicode),
		"image": string(LatexImage), "images": string(LatexImage), "png": string(LatexImage),
	})
	return LatexRenderingMode(canon), ok
}

func (m LatexRenderingMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *LatexRenderingMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseLatexRenderingMode(string(data)); ok {
		*m = parsed
	} else {
		*m = LatexImage // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type ReasoningDisplayMode string

const (
	ReasoningOff     ReasoningDisplayMode = "off"
	ReasoningFull    ReasoningDisplayMode = "full"
	ReasoningCurrent ReasoningDisplayMode = "current"
)

func ParseReasoningDisplayMode(s string) (ReasoningDisplayMode, bool) {
	canon, ok := parseLower(s, map[string]string{
		"off": string(ReasoningOff), "none": string(ReasoningOff), "false": string(ReasoningOff), "0": string(ReasoningOff), "no": string(ReasoningOff),
		"full": string(ReasoningFull), "all": string(ReasoningFull), "true": string(ReasoningFull), "1": string(ReasoningFull), "yes": string(ReasoningFull), "on": string(ReasoningFull),
		"current": string(ReasoningCurrent), "live": string(ReasoningCurrent), "ephemeral": string(ReasoningCurrent), "collapse": string(ReasoningCurrent),
	})
	return ReasoningDisplayMode(canon), ok
}

func (m ReasoningDisplayMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *ReasoningDisplayMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseReasoningDisplayMode(string(data)); ok {
		*m = parsed
	} else {
		*m = ReasoningOff // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type UpdateChannel string

const (
	ChannelStable UpdateChannel = "stable"
	ChannelMain   UpdateChannel = "main"
)

func ParseUpdateChannel(s string) (UpdateChannel, bool) {
	canon, ok := parseLower(s, map[string]string{
		"stable": string(ChannelStable), "release": string(ChannelStable),
		"main": string(ChannelMain), "nightly": string(ChannelMain), "edge": string(ChannelMain),
	})
	return UpdateChannel(canon), ok
}

func (c UpdateChannel) MarshalText() ([]byte, error) { return []byte(c), nil }
func (c *UpdateChannel) UnmarshalText(data []byte) error {
	if parsed, ok := ParseUpdateChannel(string(data)); ok {
		*c = parsed
	} else {
		*c = ChannelStable // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type CrossProviderFailoverMode string

const (
	FailoverCountdown CrossProviderFailoverMode = "countdown"
	FailoverManual    CrossProviderFailoverMode = "manual"
)

func ParseCrossProviderFailoverMode(s string) (CrossProviderFailoverMode, bool) {
	canon, ok := parseLower(s, map[string]string{
		"countdown": string(FailoverCountdown), "auto": string(FailoverCountdown), "automatic": string(FailoverCountdown),
		"manual": string(FailoverManual), "off": string(FailoverManual), "false": string(FailoverManual),
		"disabled": string(FailoverManual), "none": string(FailoverManual),
	})
	return CrossProviderFailoverMode(canon), ok
}

func (m CrossProviderFailoverMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *CrossProviderFailoverMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseCrossProviderFailoverMode(string(data)); ok {
		*m = parsed
	} else {
		*m = FailoverCountdown // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type NamedProviderType string

const (
	NamedProviderOpenAICompatible    NamedProviderType = "openai-compatible"
	NamedProviderAnthropicCompatible NamedProviderType = "anthropic-compatible"
	NamedProviderOpenRouter          NamedProviderType = "openrouter"
)

func ParseNamedProviderType(s string) (NamedProviderType, bool) {
	canon, ok := parseLower(s, map[string]string{
		"openai-compatible":    string(NamedProviderOpenAICompatible),
		"anthropic-compatible": string(NamedProviderAnthropicCompatible),
		"openrouter":           string(NamedProviderOpenRouter),
	})
	return NamedProviderType(canon), ok
}

func (t NamedProviderType) MarshalText() ([]byte, error) { return []byte(t), nil }
func (t *NamedProviderType) UnmarshalText(data []byte) error {
	if parsed, ok := ParseNamedProviderType(string(data)); ok {
		*t = parsed
	} else {
		*t = NamedProviderOpenAICompatible // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type NamedProviderAuth string

const (
	NamedAuthBearer NamedProviderAuth = "bearer"
	NamedAuthHeader NamedProviderAuth = "header"
	NamedAuthNone   NamedProviderAuth = "none"
)

func ParseNamedProviderAuth(s string) (NamedProviderAuth, bool) {
	canon, ok := parseLower(s, map[string]string{
		"bearer": string(NamedAuthBearer), "header": string(NamedAuthHeader), "none": string(NamedAuthNone),
	})
	return NamedProviderAuth(canon), ok
}

func (a NamedProviderAuth) MarshalText() ([]byte, error) { return []byte(a), nil }
func (a *NamedProviderAuth) UnmarshalText(data []byte) error {
	if parsed, ok := ParseNamedProviderAuth(string(data)); ok {
		*a = parsed
	} else {
		*a = NamedAuthBearer // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type SwarmSpawnMode string

const (
	SwarmSpawnVisible  SwarmSpawnMode = "visible"
	SwarmSpawnHeadless SwarmSpawnMode = "headless"
	SwarmSpawnInline   SwarmSpawnMode = "inline"
	SwarmSpawnAuto     SwarmSpawnMode = "auto"
)

func ParseSwarmSpawnMode(s string) (SwarmSpawnMode, bool) {
	canon, ok := parseLower(s, map[string]string{
		"visible": string(SwarmSpawnVisible), "headless": string(SwarmSpawnHeadless),
		"inline": string(SwarmSpawnInline), "auto": string(SwarmSpawnAuto),
	})
	return SwarmSpawnMode(canon), ok
}

func (m SwarmSpawnMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *SwarmSpawnMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseSwarmSpawnMode(string(data)); ok {
		*m = parsed
	} else {
		*m = SwarmSpawnInline // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type SwarmStripLayout string

const (
	SwarmStripVertical   SwarmStripLayout = "vertical"
	SwarmStripHorizontal SwarmStripLayout = "horizontal"
)

func ParseSwarmStripLayout(s string) (SwarmStripLayout, bool) {
	canon, ok := parseLower(s, map[string]string{
		"vertical": string(SwarmStripVertical), "horizontal": string(SwarmStripHorizontal),
	})
	return SwarmStripLayout(canon), ok
}

func (l SwarmStripLayout) MarshalText() ([]byte, error) { return []byte(l), nil }
func (l *SwarmStripLayout) UnmarshalText(data []byte) error {
	if parsed, ok := ParseSwarmStripLayout(string(data)); ok {
		*l = parsed
	} else {
		*l = SwarmStripVertical // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type WakeMode string

const (
	WakeInternal WakeMode = "internal"
	WakeExternal WakeMode = "external"
)

func ParseWakeMode(s string) (WakeMode, bool) {
	canon, ok := parseLower(s, map[string]string{
		"internal": string(WakeInternal), "external": string(WakeExternal),
	})
	return WakeMode(canon), ok
}

func (m WakeMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *WakeMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseWakeMode(string(data)); ok {
		*m = parsed
	} else {
		*m = WakeInternal // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type McpToolsMode string

const (
	McpToolsAuto     McpToolsMode = "auto"
	McpToolsEager    McpToolsMode = "eager"
	McpToolsDeferred McpToolsMode = "deferred"
)

func ParseMcpToolsMode(s string) (McpToolsMode, bool) {
	canon, ok := parseLower(s, map[string]string{
		"auto": string(McpToolsAuto), "eager": string(McpToolsEager), "deferred": string(McpToolsDeferred),
	})
	return McpToolsMode(canon), ok
}

func (m McpToolsMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *McpToolsMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseMcpToolsMode(string(data)); ok {
		*m = parsed
	} else {
		*m = McpToolsAuto // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type WebSearchEngine string

const (
	WebSearchDuckDuckGo WebSearchEngine = "duckduckgo"
	WebSearchBing       WebSearchEngine = "bing"
	WebSearchSearXNG    WebSearchEngine = "searxng"
	WebSearchCustom     WebSearchEngine = "custom"
	WebSearchNative     WebSearchEngine = "native"
)

func ParseWebSearchEngine(s string) (WebSearchEngine, bool) {
	canon, ok := parseLower(s, map[string]string{
		"duckduckgo": string(WebSearchDuckDuckGo), "bing": string(WebSearchBing),
		"searxng": string(WebSearchSearXNG), "custom": string(WebSearchCustom), "native": string(WebSearchNative),
	})
	return WebSearchEngine(canon), ok
}

func (e WebSearchEngine) MarshalText() ([]byte, error) { return []byte(e), nil }
func (e *WebSearchEngine) UnmarshalText(data []byte) error {
	if parsed, ok := ParseWebSearchEngine(string(data)); ok {
		*e = parsed
	} else {
		*e = WebSearchDuckDuckGo // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

type CustomWebSearchMethod string

const (
	CustomWebSearchGet  CustomWebSearchMethod = "get"
	CustomWebSearchPost CustomWebSearchMethod = "post"
)

func ParseCustomWebSearchMethod(s string) (CustomWebSearchMethod, bool) {
	canon, ok := parseLower(s, map[string]string{
		"get": string(CustomWebSearchGet), "post": string(CustomWebSearchPost),
	})
	return CustomWebSearchMethod(canon), ok
}

func (m CustomWebSearchMethod) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *CustomWebSearchMethod) UnmarshalText(data []byte) error {
	if parsed, ok := ParseCustomWebSearchMethod(string(data)); ok {
		*m = parsed
	} else {
		*m = CustomWebSearchGet // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}

// TranscriptMode is shared with the protocol layer (jcode's
// jcode-protocol::TranscriptMode): "send" (default) or "improve".
type TranscriptMode string

const (
	TranscriptSend    TranscriptMode = "send"
	TranscriptImprove TranscriptMode = "improve"
)

func ParseTranscriptMode(s string) (TranscriptMode, bool) {
	canon, ok := parseLower(s, map[string]string{
		"send": string(TranscriptSend), "improve": string(TranscriptImprove),
	})
	return TranscriptMode(canon), ok
}

func (m TranscriptMode) MarshalText() ([]byte, error) { return []byte(m), nil }
func (m *TranscriptMode) UnmarshalText(data []byte) error {
	if parsed, ok := ParseTranscriptMode(string(data)); ok {
		*m = parsed
	} else {
		*m = TranscriptSend // lenient: unknown spelling falls back (jcode #349)
	}
	return nil
}
