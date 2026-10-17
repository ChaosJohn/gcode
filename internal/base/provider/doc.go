// Package provider mirrors crates/jcode-provider-core plus the per-vendor
// crates (jcode-provider-anthropic/openai/gemini/openrouter/copilot/
// bedrock/...).
//
// Batch 1 (M2): anthropic, openai, gemini — streaming (SSE), tool-call
// round-trip, OAuth PKCE login flows.
// Batch 2 (M2, after batch 1 lands): openrouter, copilot, bedrock, azure,
// and the remaining vendors.
//
// Transport is stdlib net/http (HTTP/2 built in); TLS is crypto/tls, no
// CGO anywhere.
package provider
