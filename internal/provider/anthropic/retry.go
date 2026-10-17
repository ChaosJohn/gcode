package anthropic

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/ChaosJohn/gcode/internal/types"
)

// Retry loop (ported from run_stream_with_retries + attempt_tracker).
const (
	MaxRetries       = 3
	RetryBaseDelayMS = 1000
)

// RetryBackoffDelay: exponential backoff with ±20% jitter (~1s, ~2s, ~4s).
func RetryBackoffDelay(attempt int, baseMS uint64) time.Duration {
	shift := attempt - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 16 {
		shift = 16
	}
	exp := baseMS << uint(shift)
	jittered := uint64(float64(exp) * (0.8 + 0.4*rand.Float64()))
	if jittered < 1 {
		jittered = 1
	}
	return time.Duration(jittered) * time.Millisecond
}

// streamEventIsReplayVisible: events that reach the consumer's transcript and
// must be rolled back when a retry replays from the top.
func streamEventIsReplayVisible(event types.StreamEvent) bool {
	switch event.(type) {
	case types.TextDelta, types.TextSuffixReplace, types.TextDone,
		types.ToolUseStart, types.ToolInputDelta, types.ToolUseEnd,
		types.ToolResultEvent, types.ThinkingDelta, types.ThinkingSignatureDelta,
		types.MessageEnd, types.CompactionEvent, types.ProviderNativeEvent:
		return true
	default:
		return false
	}
}

// RetrySettings snapshots the caller's reasoning preferences so retries rebuild
// the wire request for a different model.
type RetrySettings struct {
	Effort            string
	ShowThinking      bool
	MaxTokensOverride *int
	ServiceTier       string
}

// Reshape rebuilds the request for a (fallback) model, preserving caller
// preferences rather than the original model's resolved defaults.
func (s RetrySettings) Reshape(request *ApiRequest, model string, isOAuth bool) {
	request.Model = StripLongContextSuffix(model)
	maxTokens := MaxOutputTokens(model)
	if s.MaxTokensOverride != nil {
		maxTokens = *s.MaxTokensOverride
	}
	request.MaxTokens = maxTokens
	effort := s.Effort
	if effort == "" {
		effort = defaultReasoningEffortForModel(model)
	}
	thinking, outputConfig, temperature := BuildReasoningRequestPartsForBudget(
		model, isOAuth, s.ShowThinking, effort, maxTokens)
	request.Thinking = thinking
	request.OutputConfig = outputConfig
	request.Temperature = temperature
	request.ServiceTier = s.ServiceTier
	if request.ServiceTier != "" && !ModelSupportsPriorityServiceTier(model) {
		request.ServiceTier = ""
	}
}

// defaultReasoningEffortForModel: jcode's per-model default ladder entry.
func defaultReasoningEffortForModel(model string) string {
	caps := ReasoningCapsOf(model)
	switch {
	case caps.XhighEffort:
		return "xhigh"
	case caps.OutputEffort:
		return "high"
	case caps.ManualThinking:
		return "medium"
	default:
		return ""
	}
}

// ModelSupportsPriorityServiceTier: priority service_tier exists only on
// first-party Anthropic models (all Claude ids).
func ModelSupportsPriorityServiceTier(model string) bool {
	return strings.HasPrefix(strings.ToLower(StripLongContextSuffix(model)), "claude")
}

// Options carries the retry loop's collaborators.
type Options struct {
	Transport TransportConfig
	IsOAuth   bool
	SessionID string
	ModelName string
	Ctx       context.Context
	// ResolveToken returns the current access token; forceRefresh asks the
	// credential store to refresh first (OAuth auth-failure recovery).
	ResolveToken func(forceRefresh bool) (string, error)
	// OnModelSwitch persists a fallback-model switch for later turns.
	OnModelSwitch func(newModel string)
}

// RunStreamWithRetries drives StreamResponse with the upstream recovery
// ladder: OAuth forced refresh, model fallback, reasoning self-heal, native
// replay downgrade, transient retry with rollback.
func RunStreamWithRetries(opts Options, request ApiRequest, settings RetrySettings, emit EmitFunc) error {
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	token, err := opts.ResolveToken(false)
	if err != nil {
		return err
	}
	modelName := opts.ModelName
	triedModels := []string{opts.ModelName}
	attemptedForcedRefresh := false
	sawOutput := false
	sawOutputForAttempt := false
	trackedEmit := func(event types.StreamEvent) error {
		if streamEventIsReplayVisible(event) {
			sawOutputForAttempt = true
		}
		return emit(event)
	}

	var lastError error
	nextRetryDelay := time.Duration(0)
	hasServerHint := false

	for attempt := 0; attempt < MaxRetries; attempt++ {
		if attempt > 0 {
			delay := RetryBackoffDelay(attempt, RetryBaseDelayMS)
			if hasServerHint {
				delay = nextRetryDelay
			}
			emitPhase(emit, retryingPhase(attempt+1, MaxRetries))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		sawOutputForAttempt = false
		responseAccepted := false
		cfg := opts.Transport
		if attempt > 0 {
			// Fresh client per retry: the fault that broke attempt N may have
			// poisoned other pooled connections on the same path.
			cfg.Client = DefaultHTTPClient()
		}
		streamErr := StreamResponse(ctx, cfg, token, opts.IsOAuth, request,
			opts.SessionID, modelName, trackedEmit, &responseAccepted)
		sawOutput = sawOutput || sawOutputForAttempt
		if streamErr == nil {
			return nil // Success
		}
		lastError = streamErr
		errorStr := strings.ToLower(errorChain(streamErr))

		// OAuth auth failures: force refresh and retry once immediately.
		if opts.IsOAuth && IsOAuthAuthError(errorStr) && !attemptedForcedRefresh {
			attemptedForcedRefresh = true
			_ = emit(types.ConnectionPhaseEvent{Phase: types.PhaseAuthenticating})
			if refreshed, refreshErr := opts.ResolveToken(true); refreshErr == nil {
				token = refreshed
				continue
			} else {
				return fmt.Errorf("%w\n\nAutomatic Claude OAuth refresh failed: %v\nRun `gcode login --provider claude` (preferred) or `claude`, then retry.", streamErr, refreshErr)
			}
		}

		// Model not found: transparently fall back to the best available model.
		if IsModelNotFoundError(errorStr) && !sawOutput {
			if fallback := AnthropicFallbackModel(triedModels, errorStr); fallback != "" {
				_ = emit(types.StatusDetailEvent{Detail: fmt.Sprintf("⚠ '%s' is unavailable; falling back to '%s'",
					StripLongContextSuffix(modelName), StripLongContextSuffix(fallback))})
				settings.Reshape(&request, fallback, opts.IsOAuth)
				if opts.OnModelSwitch != nil {
					opts.OnModelSwitch(fallback)
				}
				triedModels = append(triedModels, fallback)
				modelName = fallback
				continue
			}
		}

		// Fable weekly quota is terminal for Fable, not a transient 429.
		if opts.IsOAuth && !sawOutput && IsFableScopedLimitError(modelName, errorStr) {
			if fallback := BestAvailableOpusModel(modelName); fallback != "" {
				_ = emit(types.StatusDetailEvent{Detail: fmt.Sprintf("⚠ '%s' weekly limit reached; switching to '%s'",
					StripLongContextSuffix(modelName), StripLongContextSuffix(fallback))})
				settings.Reshape(&request, fallback, opts.IsOAuth)
				if opts.OnModelSwitch != nil {
					opts.OnModelSwitch(fallback)
				}
				triedModels = append(triedModels, fallback)
				modelName = fallback
				continue
			}
		}

		// Reasoning request rejected: self-heal once by dropping the fields.
		if request.Thinking != nil || request.OutputConfig != nil {
			if !sawOutput && IsReasoningUnsupportedError(errorStr) &&
				RecoverRejectedReasoning(&request, modelName, opts.IsOAuth) {
				continue
			}
		}

		// Account-level quota/auth failures belong to account routing.
		if opts.IsOAuth && !sawOutput && !responseAccepted &&
			(IsAccountLimitError(errorStr) || IsOAuthAuthError(errorStr)) {
			// gcode has no alternate-account pool yet (parity: upstream defers
			// to the router only when one exists); fall through to retry.
		}

		// Replayed server-tool payloads are account-bound; downgrade once.
		if !sawOutput && IsInvalidNativeReplayError(errorStr) &&
			downgradeNativeReplay(request.Messages) {
			continue
		}

		// Transient error: retry with backoff; roll back partial output.
		if IsRetryableError(errorStr) && attempt+1 < MaxRetries {
			if sawOutput {
				_ = emit(types.RetryRollback{Attempt: uint32(attempt + 2), Max: MaxRetries})
			}
			if hint, ok := RetryAfterFromError(streamErr); ok {
				nextRetryDelay, hasServerHint = hint, true
			} else {
				hasServerHint = false
			}
			continue
		}

		// Non-retryable or final attempt.
		if opts.IsOAuth && IsOAuthAuthError(errorStr) {
			return fmt.Errorf("%w\n\nClaude OAuth authentication failed. Run `gcode login --provider claude` (preferred) or `claude`, then retry.", streamErr)
		}
		return streamErr
	}

	return fmt.Errorf("failed after %d retries: %w", MaxRetries, lastError)
}

func retryingPhase(attempt, max int) types.ConnectionPhaseEvent {
	a := uint32(attempt)
	m := uint32(max)
	return types.ConnectionPhaseEvent{
		Phase: types.PhaseRetrying, RetryAttempt: &a, RetryMax: &m,
	}
}

func emitPhase(emit EmitFunc, event types.ConnectionPhaseEvent) {
	_ = emit(event)
}

// errorChain flattens wrapped errors the way anyhow's {:#} does.
func errorChain(err error) string {
	parts := []string{err.Error()}
	for e := err; e != nil; {
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
		if e != nil && e.Error() != parts[len(parts)-1] {
			parts = append(parts, e.Error())
		}
	}
	return strings.Join(parts, ": ")
}
