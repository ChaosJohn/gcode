// Package toolcore mirrors crates/jcode-tool-core: the Tool interface,
// registry, per-call intent metadata, and the accept_large_output escape
// hatch that rides on every tool schema.
//
// Upstream contract highlights reproduced here:
//   - every tool schema gets a required "intent" string property, added
//     centrally when converting tools to provider definitions;
//   - tools may withhold oversized results and state the token cost; the
//     caller repeats the call with accept_large_output set to accept it.
//
// Ported in milestone M3.
package toolcore
