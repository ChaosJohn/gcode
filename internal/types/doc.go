// Package types is the converged Go type layer for the jcode-*-types crates:
//
//	jcode-message-types   → message payloads, tool calls/results, stream events
//	jcode-tool-types      → tool input/output envelopes
//	jcode-session-types   → session records, turns, transcripts
//	jcode-task-types      → task/todo state
//	jcode-schema-dialect  → JSON-schema dialect used by providers
//	jcode-config-types    → user/project configuration
//	jcode-usage-types     → token usage and cost accounting
//
// Upstream serializes these with serde/serde_json (raw_value passthrough in
// places); M1 reproduces the field-level JSON shapes so gcode can read and
// write jcode artifacts losslessly. Ported in milestone M1.
package types
