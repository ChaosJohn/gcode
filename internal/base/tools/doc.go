// Package tools implements the built-in toolset of crates/jcode-base:
// bash (PTY-backed), read, edit, write, apply_patch, glob, grep, ls,
// todo, web_fetch, web_search, and MCP proxies (stdio JSON-RPC).
//
// Constraint: CGO_ENABLED=0 end to end — bash runs via creack/pty (pure
// syscalls, ConPTY on Windows), search via stdlib regexp (linear-time,
// same engine class as ripgrep), glob via doublestar.
//
// Ported in milestone M3.
package tools
