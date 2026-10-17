// Package storage mirrors the persistence layer of crates/jcode-base and
// crates/jcode-harness-api-server (both use bundled SQLite upstream).
//
// gcode uses modernc.org/sqlite (a machine-translated, CGO-free SQLite) so
// the same database format is read and written without any C toolchain.
// Session storage, resume, compaction records, and the harness API store
// live here. Reading databases created by jcode itself is an explicit M4
// acceptance item.
//
// Ported in milestone M4.
package storage
