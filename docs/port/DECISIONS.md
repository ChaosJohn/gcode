# 复刻口径决策记录（Decisions）

记录用户已拍板的口径。后续里程碑的验收一律以此为准；变更须在本文件追加记录。

## D1 复刻范围 —— 核心可用版 M0–M4（2026-10-17 批准）

- **批准**：M0 工程骨架、M1 类型/协议层、M2 Provider 层、M3 Agent 主循环 + 核心工具、M4 会话与存储。
- **暂缓**：M5 记忆系统、M6 Swarm、M7 TUI 全量、M8 发布打磨（计划表已保留，待 M0–M4 验收后另行批准）。
- **不做**：iOS App、TypeScript/npm SDK、voice/telegram/gmail/discord/浏览器控制等外围集成。

## D2 TUI 对齐口径 —— 行为等价 + 关键屏对拍（2026-10-17 批准）

- 功能、快捷键、交互流程与 jcode 一致；版式细节（间距、动画帧、配色微差）允许差异。
- "关键屏"清单在 M7 启动时另行确认（预计：主会话屏、内联 diff、权限确认、session picker、usage 面板）。

## D3 Mermaid 渲染 —— ASCII 降级（2026-10-17 批准）

- Mermaid 图渲染为终端 ASCII 结构图；不移植 mermaid-rs-renderer/resvg SVG 光栅化管线。
- 保持单二进制、零外部依赖的发布形态。

## D4 执行方式 —— 每里程碑分支 + push 审阅（2026-10-17 批准）

- 命名：`m<N>-<slug>`（如 `m0-scaffold`）。
- 里程碑验收（build/vet/test 全绿 + 对拍报告）后合入 `main` 并 push `origin`。

## D5 技术底线（贯穿性约束）

- 全程 `CGO_ENABLED=0`；任何引入 CGO 的方案直接否决。
- SQLite 一律 `modernc.org/sqlite`；PTY 一律 `creack/pty`。
- 嵌入推理手写 MiniLM-L6 前向，禁止引入 cgo ONNX 运行时。
- 数据互操作：能读 jcode `~/.jcode/`；gcode 默认写 `~/.gcode/`。
