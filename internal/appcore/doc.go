// Package appcore mirrors the server-side glue of crates/jcode-app-core:
// client/session lifecycle, the communicate tool plumbing, background
// task management, and the surfaces shared by the TUI and harness server.
//
// Only the pieces required by M3/M4 headless operation are ported in the
// approved scope; the remaining app-core surface (side panels, applets,
// ambient mode) is deferred with M6/M7.
package appcore
