# gcode

**Pure-Go (CGO-free) replica of [jcode](https://github.com/1jehuang/jcode)** — a high-performance coding agent harness, reimplemented 1:1 in Go.

```
gcode version   # build info
```

## Why

jcode is written in Rust (~280k lines of production code). gcode replicates its
behavior in Go with **zero CGO**: single static binaries, first-class cross
compilation, and the same "small, instant, terminal-native" ethos. The upstream
baseline is locked — see [docs/port/BASELINE.md](docs/port/BASELINE.md).

## Status

| Milestone | Scope | Status |
|---|---|---|
| M0 | Project scaffold, CI, porting docs | 🚧 in progress |
| M1 | Type/protocol layer (serde-JSON-compatible shapes) | ⬜ |
| M2 | Providers: Anthropic / OpenAI / Gemini (batch 1), more (batch 2) | ⬜ |
| M3 | Agent loop + core tools (bash PTY, read/edit, glob/grep, web, MCP) | ⬜ |
| M4 | Sessions: SQLite storage, resume, `~/.jcode` interop | ⬜ |
| M5–M8 | Memory graph, swarm, full TUI, release engineering | ⬜ deferred |

## Alignment decisions (docs/port/DECISIONS.md)

- **Scope**: core-usable build = M0–M4 first; memory/swarm/TUI milestones follow later.
- **TUI**: behavioral equivalence + key-screen diffs, not pixel-perfect.
- **Mermaid**: ASCII rendering in terminal; no SVG rasterization port.
- **Not ported**: iOS app, TypeScript/npm SDK, voice/telegram/gmail integrations.
- **Hard constraint**: `CGO_ENABLED=0` everywhere — SQLite via `modernc.org/sqlite`, PTY via `creack/pty`, embeddings via a hand-written MiniLM-L6 forward pass.

## Build

Requires Go ≥ 1.26. No C toolchain, ever.

```sh
make check        # vet + build + test + gofmt gate
make cross        # release matrix: linux/darwin/windows/freebsd × amd64/arm64
```

CI enforces the same gates on every push (3 OSes × CGO disabled + 6-way cross-compile).

## Attribution

Behavioral reference only: gcode is an independent Go implementation modeled on
the behavior of [1jehuang/jcode](https://github.com/1jehuang/jcode) (MIT). No
upstream source code is copied. jcode is © its authors; gcode is © 2026 Chaos John, MIT licensed.

## Docs

- [Porting plan](docs/port/PORT_PLAN.md) · [Decisions](docs/port/DECISIONS.md) · [Parity report template](docs/port/PARITY_TEMPLATE.md)
