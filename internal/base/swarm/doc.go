// Package swarm mirrors crates/jcode-swarm-core (upstream
// SWARM_ARCHITECTURE.md): coordinator / worktree-manager / agent roles,
// the agent lifecycle state machine (spawned → ready → running ⇄ blocked
// → completed → stopped | failed | crashed), the CommsRouter (DM, swarm
// broadcast, topic channels, shared context keys, channel discovery), and
// soft-interrupt injection at agent safe points.
//
// Constraint reproduced from upstream: agents cannot spawn other agents —
// the tree stays strictly two levels deep.
//
// Deferred: scheduled for M6 (outside the approved M0–M4 scope).
package swarm
