# 对拍基线（Baseline Lock）

本仓库（gcode）的行为复刻基准锁定为以下 jcode 版本。jcode 上游仍在活跃开发，
在 M0–M4 完成前，所有对拍测试只针对该基线，不追新。

| 项 | 值 |
|---|---|
| 上游仓库 | https://github.com/1jehuang/jcode |
| 锁定版本 | v0.98.1 |
| 锁定 commit | `11952e02ac76fa1c335633e9c6113691c1cf3395` |
| commit 时间 | 2026-10-17T00:39:30-07:00 |
| 参照克隆 | `/Users/chaos/Work/github/1jehuang/jcode`（本地）、`/tmp/jcode-analysis`（分析用） |
| 上游许可 | MIT |
| 复刻范围 | 核心可用版 M0–M4（见 [PORT_PLAN.md](PORT_PLAN.md)） |

## 上游规模参考（复刻工作量依据）

| 指标 | 值 |
|---|---|
| Rust 总行数 | 830,784（含测试；生产代码 ≈ 28 万行） |
| workspace crates | 88 |
| 本计划映射 crates | message/tool/session/task/schema-dialect/config-types、provider-core + 全家、
  tool-core、base（工具/记忆/存储/swarm）、agent-runtime、app-core（部分）、harness-api-server（部分） |

## 固定不变的复刻口径

1. **TUI**：行为等价 + 关键屏对拍（非像素级）。
2. **Mermaid**：ASCII 降级，不做 SVG 光栅化。
3. **不做**：iOS App、TypeScript/npm SDK、voice/telegram/gmail/discord 等外围集成。
4. **嵌入数值**：手写 MiniLM-L6 前向，与上游非 bit-exact，验收口径为余弦相似度 ≥ 0.999。
5. **数据互操作**：可读上游 `~/.jcode/`（会话、记忆 `graph.json`、配置 TOML）；gcode 默认写 `~/.gcode/`。
6. **零 CGO**：全部构建 `CGO_ENABLED=0`；SQLite 用 `modernc.org/sqlite`，PTY 用 `creack/pty`。
