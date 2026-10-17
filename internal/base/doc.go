// Package base mirrors crates/jcode-base, the application substrate:
// configuration loading, command transport, safety checks, skills, the
// tool registry, and session plumbing.
//
// Subpackages:
//
//	tools    — the built-in toolset (bash/read/edit/grep/glob/...)
//	provider — model providers (anthropic/openai/gemini/...)
//	memory   — persistent semantic memory graph
//	swarm    — multi-agent coordination
//	storage  — SQLite-backed session storage
//
// Ported across milestones M1–M4.
package base
