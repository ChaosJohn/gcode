// Package tui mirrors crates/jcode-tui (231k lines upstream — the largest
// single component): terminal UI over bubbletea/lipgloss instead of
// ratatui/crossterm.
//
// Alignment bar (DECISIONS.md D2): behavioral equivalence — features,
// keybindings, and interaction flows match jcode; pixel-level layout
// details may differ. Key screens are diffed against upstream renders.
//
// Mermaid diagrams render as ASCII structure (DECISIONS.md D3); no SVG
// rasterization is ported.
//
// Deferred: scheduled for M7 (outside the approved M0–M4 scope).
package tui
