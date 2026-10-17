// Package anthropic ports jcode's Anthropic Messages-API provider
// (jcode-provider-core/src/anthropic.rs, jcode-provider-anthropic,
// jcode-provider-anthropic-runtime): request shaping, history repair, prompt
// caching, reasoning caps, and SSE stream decoding.
package anthropic

import (
	"runtime"
	"strings"
)

// LongContextSuffix is the explicit 1M opt-in alias (claude-opus-4-6[1m]).
const LongContextSuffix = "[1m]"

// OAuth beta header lines (provider-core/anthropic.rs).
const (
	OAuthBetaHeaders     = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advisor-tool-2026-03-01,advanced-tool-use-2025-11-20,effort-2025-11-24"
	OAuthBetaHeaders1M   = OAuthBetaHeaders + ",context-1m-2025-08-07"
	OAuthBillingHeader   = "cc_version=2.1.280; cc_entrypoint=sdk-cli; cch=33f85;"
	ClaudeCodeIdentity   = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	ContinuationUserTurn = "Continue."

	// DisplacedToolResultText is the synthetic tool_result injected for a
	// tool_use whose real result exists but is not in the next message.
	DisplacedToolResultText = "[Tool output was recorded later in the conversation and is included there as text]"
	// InterruptedToolResultText is injected for dangling tool_uses.
	InterruptedToolResultText = "[Session interrupted before tool execution completed]"
)

// APIURL is the first-party Messages endpoint.
const APIURL = "https://api.anthropic.com/v1/messages"

// APIVersion is the anthropic-version header value.
const APIVersion = "2023-06-01"

// StripLongContextSuffix removes the [1m] alias.
func StripLongContextSuffix(model string) string {
	return strings.TrimSuffix(model, LongContextSuffix)
}

// SplitLongContext reports whether the model requested the 1M beta.
func SplitLongContext(model string) (string, bool) {
	if strings.HasSuffix(model, LongContextSuffix) {
		return strings.TrimSuffix(model, LongContextSuffix), true
	}
	return model, false
}

// StripDateSuffix removes a trailing -YYYYMMDD snapshot date.
func StripDateSuffix(model string) string {
	idx := strings.LastIndex(model, "-")
	if idx >= 0 {
		tail := model[idx+1:]
		if len(tail) == 8 && allASCIIDigits(tail) {
			return model[:idx]
		}
	}
	return model
}

func allASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ContextMode classifies how a Claude model exposes its 1M window.
type ContextMode int

const (
	ContextNative1M ContextMode = iota
	ContextOptIn1M
	ContextStandard
)

// DefaultContextWindow is the window without the 1M beta.
func (m ContextMode) DefaultContextWindow() int {
	if m == ContextNative1M {
		return 1_000_000
	}
	return 200_000
}

// LongContextWindow is the window with the 1M path engaged.
func (m ContextMode) LongContextWindow() int {
	switch m {
	case ContextNative1M:
		return 1_000_000
	case ContextOptIn1M:
		return 1_048_576 // the opt-in beta advertises this
	default:
		return 200_000
	}
}

// Has1MWindow reports whether any 1M path exists.
func (m ContextMode) Has1MWindow() bool { return m != ContextStandard }

// Exposes1MAlias reports whether the [1m] picker alias is meaningful.
func (m ContextMode) Exposes1MAlias() bool { return m == ContextOptIn1M }

// normalizedCapsKey lowercases, strips [1m] and -YYYYMMDD, dashes dots.
func normalizedCapsKey(model string) string {
	base := StripDateSuffix(strings.ReplaceAll(strings.ToLower(StripLongContextSuffix(strings.TrimSpace(model))), ".", "-"))
	return base
}

// parseClaudeFamilyVersion extracts (family, version) handling both
// version-last (claude-sonnet-4-6) and version-first (claude-3-7-sonnet).
func parseClaudeFamilyVersion(base string) (family string, major, minor int, ok bool) {
	var nums []int
	for _, segment := range strings.Split(base, "-") {
		if segment == "claude" {
			continue
		}
		if n, valid := parseUint(segment); valid {
			if len(nums) < 2 {
				nums = append(nums, n)
			}
		} else if family == "" && isAllAlpha(segment) {
			family = segment
		}
	}
	switch len(nums) {
	case 0:
		return "", 0, 0, false
	case 1:
		return family, nums[0], 0, true
	default:
		return family, nums[0], nums[1], true
	}
}

func parseUint(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

func isAllAlpha(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func versionGE(family string, major, minor int, wantFamily string, wantMajor, wantMinor int) bool {
	if family != wantFamily {
		return false
	}
	if major != wantMajor {
		return major > wantMajor
	}
	return minor >= wantMinor
}

func versionEQ(family string, major, minor int, wantFamily string, wantMajor, wantMinor int) bool {
	return family == wantFamily && major == wantMajor && minor == wantMinor
}

// ContextModeOf classifies long-context behavior for a Claude id.
func ContextModeOf(model string) ContextMode {
	base := normalizedCapsKey(model)
	if !strings.HasPrefix(base, "claude") {
		return ContextStandard
	}
	family, major, minor, ok := parseClaudeFamilyVersion(base)
	if !ok {
		return ContextStandard
	}
	switch family {
	case "opus", "sonnet":
		if versionGE(family, major, minor, family, 4, 7) {
			return ContextNative1M
		}
		if versionEQ(family, major, minor, family, 4, 6) {
			return ContextOptIn1M
		}
		return ContextStandard
	case "haiku":
		if versionGE(family, major, minor, family, 5, 0) {
			return ContextNative1M
		}
		return ContextStandard
	default:
		if versionGE(family, major, minor, "", 5, 0) && major >= 5 {
			return ContextNative1M
		}
		return ContextStandard
	}
}

// ReasoningCaps mirrors AnthropicReasoningCaps.
type ReasoningCaps struct {
	OutputEffort     bool
	AdaptiveThinking bool
	ManualThinking   bool
	XhighEffort      bool
	MaxEffort        bool
}

// SupportsReasoningEffort reports whether any effort control exists.
func (c ReasoningCaps) SupportsReasoningEffort() bool { return c.OutputEffort || c.ManualThinking }

var (
	capsFull             = ReasoningCaps{true, true, false, true, true}
	capsEffortNoXhigh    = ReasoningCaps{true, true, false, false, true}
	capsManualWithEffort = ReasoningCaps{true, false, true, false, false}
	capsManualOnly       = ReasoningCaps{false, false, true, false, false}
	capsNone             = ReasoningCaps{}
)

// ReasoningCapsOf resolves effort/thinking capabilities for a Claude id.
func ReasoningCapsOf(model string) ReasoningCaps {
	base := normalizedCapsKey(model)
	if !strings.HasPrefix(base, "claude") {
		return capsNone
	}
	if strings.Contains(base, "mythos") {
		return capsEffortNoXhigh
	}
	family, major, minor, ok := parseClaudeFamilyVersion(base)
	if !ok {
		return capsNone
	}
	switch family {
	case "opus":
		if versionGE(family, major, minor, "opus", 4, 7) {
			return capsFull
		}
		if versionEQ(family, major, minor, "opus", 4, 6) {
			return capsEffortNoXhigh
		}
		if versionEQ(family, major, minor, "opus", 4, 5) {
			return capsManualWithEffort
		}
		return capsNone
	case "sonnet":
		if versionGE(family, major, minor, "sonnet", 5, 0) {
			return capsFull
		}
		if versionEQ(family, major, minor, "sonnet", 4, 6) {
			return capsEffortNoXhigh
		}
		if versionEQ(family, major, minor, "sonnet", 3, 7) {
			return capsManualOnly
		}
		return capsNone
	default:
		if major >= 5 {
			return capsFull
		}
		return capsNone
	}
}

// ThinkingAlwaysOn reports models whose thinking cannot be disabled
// (Opus 5.5, Fable 5.1): `none` effort means "lowest supported effort".
func ThinkingAlwaysOn(model string) bool {
	base := normalizedCapsKey(model)
	if !strings.HasPrefix(base, "claude-") {
		return false
	}
	family, major, minor, ok := parseClaudeFamilyVersion(base)
	if !ok {
		return false
	}
	return versionEQ(family, major, minor, "opus", 5, 5) ||
		versionEQ(family, major, minor, "fable", 5, 1)
}

// MaxOutputTokens is the synchronous Messages-API cap per model generation.
func MaxOutputTokens(model string) int {
	base := strings.ToLower(StripLongContextSuffix(strings.TrimSpace(model)))
	largePrefixes := []string{
		"claude-opus-5", "claude-opus-4-8", "claude-opus-4.8", "claude-opus-4-7",
		"claude-opus-4.7", "claude-opus-4-6", "claude-opus-4.6", "claude-sonnet-5",
		"claude-sonnet-4-6", "claude-sonnet-4.6", "claude-fable-5-1", "claude-fable-5",
		"claude-fable", "claude-mythos",
	}
	for _, prefix := range largePrefixes {
		if strings.HasPrefix(base, prefix) {
			return 128_000
		}
	}
	if strings.HasPrefix(base, "claude-haiku-4-5") || strings.HasPrefix(base, "claude-haiku-4.5") {
		return 64_000
	}
	return 32_768
}

// OAuthBetaHeadersFor returns the beta header value for the model.
func OAuthBetaHeadersFor(model string) string {
	if strings.HasSuffix(model, LongContextSuffix) {
		return OAuthBetaHeaders1M
	}
	return OAuthBetaHeaders
}

// MapToolNameForOAuth renames registry tools to Claude-Code builtin names.
func MapToolNameForOAuth(name string) string {
	switch name {
	case "bash":
		return "Bash"
	case "read":
		return "Read"
	case "write":
		return "Write"
	case "edit":
		return "Edit"
	case "glob":
		return "Glob"
	case "grep":
		return "Grep"
	case "subagent":
		return "Agent"
	case "schedule":
		return "ScheduleWakeup"
	case "skill_manage":
		return "Skill"
	default:
		return name
	}
}

// MapToolNameFromOAuth is the inverse of MapToolNameForOAuth.
func MapToolNameFromOAuth(name string) string {
	switch name {
	case "Bash":
		return "bash"
	case "Read":
		return "read"
	case "Write":
		return "write"
	case "Edit":
		return "edit"
	case "Glob":
		return "glob"
	case "Grep":
		return "grep"
	case "Agent":
		return "subagent"
	case "ScheduleWakeup":
		return "schedule"
	case "Skill":
		return "skill_manage"
	default:
		return name
	}
}

// StainlessOS mirrors the client metadata Anthropic sees, per runtime OS.
func StainlessOS() string {
	switch runtime.GOOS {
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return "MacOS"
	}
}

// StainlessArch mirrors the client metadata per runtime architecture.
func StainlessArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "x86"
	default:
		return "arm64"
	}
}
