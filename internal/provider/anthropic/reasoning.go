package anthropic

import "strings"

// Reasoning request shaping (ported from reasoning_request.rs + the runtime's
// build_reasoning_request_parts_for_budget).

const bindingBeta = "thinking-binding-controls-2026-08-01"

// AdaptiveThinking builds the adaptive thinking param with progress summaries
// (Opus 5.5 intermediate progress is thinking, not text).
func AdaptiveThinking(model string) *ApiThinking {
	thinking := &ApiThinking{Adaptive: true, Display: "summarized"}
	if ThinkingAlwaysOn(model) {
		thinking.BlockBinding = true
	}
	return thinking
}

// FallbackTemperature is the OAuth temperature when thinking is off.
func FallbackTemperature(model string, isOAuth bool) *float32 {
	if isOAuth && !ThinkingAlwaysOn(model) {
		t := float32(1.0)
		return &t
	}
	return nil
}

// WithBindingBeta appends the binding beta header when block binding is set.
func WithBindingBeta(base string, thinking *ApiThinking) string {
	binding := thinking != nil && thinking.Adaptive && thinking.BlockBinding
	if !binding {
		return base
	}
	for _, header := range strings.Split(base, ",") {
		if strings.TrimSpace(header) == bindingBeta {
			return base
		}
	}
	if base == "" {
		return bindingBeta
	}
	return base + "," + bindingBeta
}

// ManualThinkingBudget maps effort to a concrete budget (manual-thinking
// models); nil when the effort needs no thinking.
func ManualThinkingBudget(effort string, maxTokens int) *int {
	var desired int
	switch effort {
	case "minimal", "low":
		desired = 1_024
	case "medium":
		desired = 4_096
	case "high":
		desired = 8_192
	case "xhigh", "max":
		desired = 16_384
	default:
		return nil
	}
	// Keep room for actual output.
	if desired > maxTokens/2 {
		desired = maxTokens / 2
	}
	return &desired
}

// ResolvedEffortForModel maps requested effort onto the model's ladder.
func ResolvedEffortForModel(model, effort string) string {
	caps := ReasoningCapsOf(model)
	if effort == "xhigh" && !caps.XhighEffort {
		return "high"
	}
	if effort == "max" && !caps.MaxEffort {
		if caps.XhighEffort {
			return "xhigh"
		}
		return "high"
	}
	return effort
}

// BuildReasoningRequestParts computes (thinking, output_config, temperature).
func BuildReasoningRequestParts(model string, isOAuth bool, showThinking bool, resolvedEffort string, maxTokens int) (*ApiThinking, *ApiOutputConfig, *float32) {
	return BuildReasoningRequestPartsForBudget(model, isOAuth, showThinking, resolvedEffort, maxTokens)
}

// BuildReasoningRequestPartsForBudget is the budget-aware variant.
func BuildReasoningRequestPartsForBudget(model string, isOAuth bool, showThinking bool, resolvedEffort string, maxTokens int) (*ApiThinking, *ApiOutputConfig, *float32) {
	alwaysOn := ThinkingAlwaysOn(model)
	// On always-on models `none` means the lowest supported effort, never off.
	if resolvedEffort == "none" && alwaysOn {
		resolvedEffort = "low"
	}
	if resolvedEffort == "none" {
		showThinking = false
	}
	effort := ""
	if resolvedEffort != "" && resolvedEffort != "none" && ReasoningCapsOf(model).SupportsReasoningEffort() {
		effort = resolvedEffort
	}

	caps := ReasoningCapsOf(model)
	var outputConfig *ApiOutputConfig
	if effort != "" && caps.OutputEffort {
		outputConfig = &ApiOutputConfig{Effort: ResolvedEffortForModel(model, effort)}
	}

	var thinking *ApiThinking
	switch {
	case caps.AdaptiveThinking:
		if alwaysOn || effort != "" || showThinking {
			thinking = AdaptiveThinking(model)
		}
	case caps.ManualThinking:
		resolved := effort
		if resolved == "" && showThinking {
			resolved = "low"
		}
		if resolved != "" {
			if budget := ManualThinkingBudget(resolved, maxTokens); budget != nil {
				thinking = &ApiThinking{IsEnabled: true, BudgetTokens: *budget}
			}
		}
	}

	// Thinking is incompatible with temperature; OAuth otherwise mirrors
	// Claude Code's temperature=1.0.
	var temperature *float32
	if isOAuth && thinking == nil {
		t := float32(1.0)
		temperature = &t
	}
	return thinking, outputConfig, temperature
}

// RecoverRejectedReasoning reshapes a request the API rejected for its
// reasoning fields; false when there is nothing to drop (surface the error).
func RecoverRejectedReasoning(request *ApiRequest, model string, isOAuth bool) bool {
	if ThinkingAlwaysOn(model) {
		if request.OutputConfig == nil {
			return false
		}
		request.OutputConfig = nil
		request.Thinking = AdaptiveThinking(model)
		request.Temperature = nil
		return true
	}
	if request.Thinking == nil && request.OutputConfig == nil {
		return false
	}
	request.Thinking = nil
	request.OutputConfig = nil
	request.Temperature = FallbackTemperature(model, isOAuth)
	return true
}
