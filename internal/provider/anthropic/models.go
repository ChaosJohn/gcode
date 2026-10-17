package anthropic

import (
	"sort"
	"strings"
)

// Model catalog and fallback ranking (ported from models.rs and
// error_classification.rs).

// DefaultClaudeModel is the quality-first default.
const DefaultClaudeModel = "claude-opus-5-5"

// AllClaudeModels is the curated flagship-first catalog; position = quality
// rank (lower is better).
var AllClaudeModels = []string{
	"claude-opus-5-5",
	DefaultClaudeModel,
	"claude-opus-5",
	"claude-fable-5-1",
	"claude-fable-5",
	"claude-opus-4-8",
	"claude-opus-4-6",
	"claude-opus-4-6[1m]",
	"claude-sonnet-5",
	"claude-sonnet-4-6",
	"claude-sonnet-4-6[1m]",
	"claude-haiku-4-5",
	"claude-opus-4-5",
	"claude-sonnet-4-5",
	"claude-sonnet-4-20250514",
}

// retiredAnthropicModelMarkers: models the server 404s; never fallback targets.
var retiredAnthropicModelMarkers = []string{"claude-mythos"}

// NormalizedModelKey is the comparable id: [1m]-stripped, trimmed, lowercase.
func NormalizedModelKey(model string) string {
	return strings.ToLower(strings.TrimSpace(StripLongContextSuffix(model)))
}

// AnthropicModelIsRetired reports retired families (matched as a substring of
// the normalized id so dated variants are covered too).
func AnthropicModelIsRetired(model string) bool {
	normalized := NormalizedModelKey(model)
	for _, marker := range retiredAnthropicModelMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// AnthropicModelQualityRank: lower is better; unknown ids sort after the
// curated list, retired models last (usize::MAX).
func AnthropicModelQualityRank(model string) int {
	if AnthropicModelIsRetired(model) {
		return int(^uint(0) >> 1) // max int
	}
	normalized := NormalizedModelKey(model)
	for pos, candidate := range AllClaudeModels {
		if NormalizedModelKey(candidate) == normalized {
			return pos
		}
	}
	return len(AllClaudeModels)
}

// knownAnthropicModelIDs returns the usable catalog (never-retired).
func knownAnthropicModelIDs() []string {
	out := make([]string, 0, len(AllClaudeModels))
	for _, model := range AllClaudeModels {
		if !AnthropicModelIsRetired(model) {
			out = append(out, model)
		}
	}
	return out
}

// AnthropicRecommendedModelFromError parses a server-recommended replacement
// from a 404 body, e.g. "Claude Fable 5 is not available. Please use Opus 4.8."
// errorStr must already be lowercased.
func AnthropicRecommendedModelFromError(errorStr string) string {
	hint := ""
	if idx := strings.Index(errorStr, "please use"); idx >= 0 {
		hint = errorStr[idx+len("please use"):]
	} else if idx := strings.Index(errorStr, "use "); idx >= 0 {
		hint = errorStr[idx+len("use "):]
	} else {
		return ""
	}
	// A decimal point inside a model version is not a sentence boundary.
	end := len(hint)
	for i := 0; i < len(hint); i++ {
		c := hint[i]
		if c != '.' && c != '!' && c != '\n' {
			continue
		}
		versionDot := c == '.' && i > 0 && isASCIIDigit(hint[i-1]) &&
			i+1 < len(hint) && isASCIIDigit(hint[i+1])
		if !versionDot {
			end = i
			break
		}
	}
	hint = strings.TrimSpace(hint[:end])
	if hint == "" {
		return ""
	}
	var hintTokens []string
	for _, token := range strings.FieldsFunc(hint, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		hintTokens = append(hintTokens, token)
	}
	if len(hintTokens) == 0 {
		return ""
	}
	best, bestScore := "", 0
	for _, candidate := range knownAnthropicModelIDs() {
		candidateTokens := strings.Split(NormalizedModelKey(candidate), "-")
		score := 0
		for _, token := range hintTokens {
			for i, part := range candidateTokens {
				if part == token {
					candidateTokens = append(candidateTokens[:i], candidateTokens[i+1:]...)
					score++
					break
				}
			}
		}
		// Require family word plus a version digit so a single shared token
		// cannot pick an arbitrary model.
		if score >= 2 && score > bestScore {
			best, bestScore = candidate, score
		}
	}
	return best
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// AnthropicFallbackModel picks the next model after a "model not found"
// failure: the server recommendation first, then the best untried curated
// model, skipping retired families.
func AnthropicFallbackModel(tried []string, errorStr string) string {
	alreadyTried := func(candidate string) bool {
		key := NormalizedModelKey(candidate)
		for _, model := range tried {
			if NormalizedModelKey(model) == key {
				return true
			}
		}
		return false
	}
	if recommended := AnthropicRecommendedModelFromError(errorStr); recommended != "" &&
		!alreadyTried(recommended) && !AnthropicModelIsRetired(recommended) {
		return recommended
	}
	best, bestRank := "", 0
	first := true
	for _, candidate := range knownAnthropicModelIDs() {
		if alreadyTried(candidate) {
			continue
		}
		rank := AnthropicModelQualityRank(candidate)
		if first || rank < bestRank {
			best, bestRank, first = candidate, rank, false
		}
	}
	return best
}

// BestAvailableOpusModel picks the best Opus model except the excluded one
// (Fable weekly-quota fallback).
func BestAvailableOpusModel(exclude string) string {
	var models []string
	for _, model := range knownAnthropicModelIDs() {
		base := StripLongContextSuffix(model)
		key := strings.ToLower(base)
		if !strings.Contains(key, "claude-opus") ||
			base == StripLongContextSuffix(exclude) {
			continue
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return ""
	}
	sort.Slice(models, func(i, j int) bool {
		return AnthropicModelQualityRank(models[i]) < AnthropicModelQualityRank(models[j])
	})
	return models[0]
}
