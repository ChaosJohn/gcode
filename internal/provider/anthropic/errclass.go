package anthropic

import "strings"

// Error classification (ported from error_classification.rs + transport.rs).

// transientTransportMarkers: transport-level faults worth a fresh-connection
// retry (jcode-provider-core/src/transport.rs).
var transientTransportMarkers = []string{
	"connection reset", "connection closed", "connection refused", "connection aborted",
	"broken pipe", "timed out", "timeout", "operation timed out",
	"error decoding", "error reading", "unexpected eof",
	"close_notify", "peer closed connection", "tls handshake eof",
	"client error (connect)", "connection error",
	"incomplete message", "request or response body error",
	"badrecordmac", "bad_record_mac", "fatal alert: badrecordmac",
	"fatal alert: bad_record_mac", "decryption failed or bad record mac",
	"temporary failure in name resolution", "failed to lookup address information",
	"dns error", "name or service not known", "no route to host",
	"network is unreachable", "host is unreachable",
	"http2 error", "stream error", "stream_read_error",
	"protocol error", "refused_stream", "refused stream",
	"enhance_your_calm", "goaway", "go away", "sendrequest",
}

// IsTransientTransportError reports a transport fault likely to succeed on a
// fresh connection.
func IsTransientTransportError(errorStr string) bool {
	lower := strings.ToLower(errorStr)
	for _, marker := range transientTransportMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// IsRetryableError checks whether an error should be retried (transport faults,
// 5xx, rate limiting, API-level server errors).
func IsRetryableError(errorStr string) bool {
	lower := strings.ToLower(errorStr)
	return IsTransientTransportError(lower) ||
		containsAll(lower,
			"500 internal server error", "502 bad gateway", "503 service unavailable",
			"504 gateway timeout", "overloaded",
			"429 too many requests", "rate limit", "rate_limit",
			"api_error", "internal server error")
}

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// IsAccountLimitError: account-local limits escapable by another OAuth pool
// member. Deliberately excludes model/transport overload.
func IsAccountLimitError(errorStr string) bool {
	lower := strings.ToLower(errorStr)
	return containsAll(lower,
		"429 too many requests", "rate limit", "rate_limit", "quota",
		"usage exhausted", "usage limit", "limit reached",
		"billing", "credit balance", "payment required")
}

// IsFableScopedLimitError detects the model-scoped weekly quota error
// (terminal for Fable, not a transient 429).
func IsFableScopedLimitError(model, errorStr string) bool {
	base := strings.ToLower(StripLongContextSuffix(model))
	if !strings.Contains(base, "fable") {
		return false
	}
	lower := strings.ToLower(errorStr)
	isLimit := strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "usage_limit") ||
		strings.Contains(lower, "usage limit")
	isScoped := strings.Contains(lower, "fable") ||
		strings.Contains(lower, "weekly") ||
		strings.Contains(lower, "week limit") ||
		strings.Contains(lower, "7-day") ||
		strings.Contains(lower, "7 day")
	return isLimit && isScoped
}

// IsModelNotFoundError detects an Anthropic 404 model rejection
// ("type":"not_found_error"). errorStr must be lowercased.
func IsModelNotFoundError(errorStr string) bool {
	mentionsModel := strings.Contains(errorStr, "model")
	isNotFound := strings.Contains(errorStr, "not_found_error") ||
		strings.Contains(errorStr, "404 not found") ||
		strings.Contains(errorStr, "(404 ")
	return isNotFound && (mentionsModel ||
		strings.Contains(errorStr, "is not available") ||
		strings.Contains(errorStr, "please use"))
}

// IsReasoningUnsupportedError detects a 400 invalid_request_error rejecting the
// thinking/effort fields. errorStr must be lowercased.
func IsReasoningUnsupportedError(errorStr string) bool {
	isBadRequest := strings.Contains(errorStr, "invalid_request_error") ||
		strings.Contains(errorStr, "400 bad request")
	mentionsReasoningField := strings.Contains(errorStr, "thinking") ||
		strings.Contains(errorStr, "effort") ||
		strings.Contains(errorStr, "output_config")
	mentionsUnsupported := strings.Contains(errorStr, "not supported") ||
		strings.Contains(errorStr, "does not support")
	return isBadRequest && mentionsReasoningField && mentionsUnsupported
}

// IsOAuthAuthError detects OAuth authentication failures.
func IsOAuthAuthError(errorStr string) bool {
	lower := strings.ToLower(errorStr)
	return strings.Contains(lower, "oauth token has expired") ||
		strings.Contains(lower, "token has expired") ||
		strings.Contains(lower, "authentication_error") ||
		strings.Contains(lower, "invalid token") ||
		strings.Contains(lower, "invalid_grant") ||
		strings.Contains(lower, "does not meet scope requirement") ||
		((strings.Contains(lower, "401 unauthorized") || strings.Contains(lower, "403 forbidden")) &&
			(strings.Contains(lower, "oauth") || strings.Contains(lower, "token")))
}

// IsOAuthCatalogAuthError is the broader catalog probe variant.
func IsOAuthCatalogAuthError(errorStr string) bool {
	lower := strings.ToLower(errorStr)
	return strings.Contains(lower, "401 unauthorized") ||
		strings.Contains(lower, "403 forbidden") ||
		IsOAuthAuthError(lower)
}
