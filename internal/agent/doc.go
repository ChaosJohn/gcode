// Package agent mirrors the agent runtime loop: crates/jcode-agent-runtime
// plus the orchestration glue in crates/jcode-base (turn management,
// streaming dispatch into tool calls, soft-interrupt injection points,
// context budgeting, and compaction triggers).
//
// In Go the tokio task/channel model maps to goroutines and channels; the
// interrupt signal becomes a context-scoped channel inspected at the same
// safe points upstream defines.
//
// Ported in milestone M3.
package agent
