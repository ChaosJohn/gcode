// Package harness mirrors crates/jcode-harness-api-server and the jcode-harness
// binary: a local API server (plus its SQLite store) that external clients
// talk to, with protocol translation in crates/jcode-protocol.
//
// M4 ships the first version: session list/resume over a local endpoint,
// backed by internal/base/storage, sharing the database format so jcode
// sessions are readable.
package harness
