# gcode 移植执行计划（已批准版）

> 批准时间：2026-10-17。批准口径见 [DECISIONS.md](DECISIONS.md)。
> 本文档是从对话中确认的执行计划表，执行过程中如有修订，修订记录见文末。

## 1. 总体策略

| 原则 | 说明 |
|---|---|
| 里程碑驱动 | M0–M8 共 9 个里程碑；本仓库按批准范围先执行 **M0–M4**，每个独立可验收、可运行；每里程碑一个分支 + 一次 push，用户在里程碑边界审阅 |
| 对拍优先 | 每个 jcode 行为以锁定基线（[BASELINE.md](BASELINE.md)）源码 + 真实 transcript 为验收基准，不做凭空设计 |
| 数据互操作 | 能直接读写上游 `~/.jcode/`；gcode 自身默认写 `~/.gcode/`，提供 `--import-jcode` 迁移 |
| 零 CGO | 全程 `CGO_ENABLED=0` 可交叉编译；SQLite 用 `modernc.org/sqlite`，PTY 用 `creack/pty` |
| 明确不做 | iOS App、TypeScript/npm SDK、voice/telegram/gmail 等外围集成、Mermaid 终端光栅化（ASCII 降级） |

## 2. 仓库布局（jcode 88 crates → Go 包收敛映射）

```
gcode/
├── cmd/gcode/              # 主 CLI（对应 jcode 主 bin）
├── cmd/gcode-harness/      # harness 服务 bin
├── internal/
│   ├── types/              # message/tool/session/task 等 jcode-*-types 全家 → 一个类型包
│   ├── toolcore/           # 工具接口、注册表、intent/accept_large_output 协议
│   ├── base/               # jcode-base：config/transport/safety/skill/registry
│   │   ├── tools/          # 30+ 内置工具（bash/read/edit/grep/... 每工具一文件）
│   │   ├── provider/       # provider-core + anthropic/openai/gemini/... 每家一子包
│   │   ├── memory/         # 记忆图谱 + 级联检索 + HDBSCAN
│   │   ├── swarm/          # 协调者/WTM/CommsRouter/软中断
│   │   └── storage/        # modernc SQLite + 会话存取
│   ├── agent/              # agent loop：turn 管理/流式/工具分派/compaction
│   ├── embedding/          # 手写 MiniLM-L6 前向 + WordPiece + safetensors 加载
│   ├── appcore/            # server/client 生命周期、communicate、后台任务
│   ├── harness/            # harness-api-server
│   └── tui/                # bubbletea 全量 TUI（render/markdown/style/permissions/...）
└── docs/port/              # 移植文档 + 每里程碑对拍报告
```

## 3. 里程碑计划表

| # | 里程碑 | 主要内容（映射 jcode 源） | Go 技术选型 | 交付物 / 验收标准 | 预估规模 |
|---|---|---|---|---|---|
| **M0** | 工程骨架 | 目录结构、`go.mod`、CI（build+vet+test+`CGO_ENABLED=0` 交叉编译矩阵）、移植文档、对拍基线锁定 | stdlib（零第三方依赖起步） | `go build ./...` 绿灯；CI 全平台编译通过 | ~1k 行 |
| **M1** | 类型与协议层 | `message-types`/`tool-types`/`session-types`/`schema-dialect`/`config-types`；serde JSON 字段兼容（含 `raw_value` 透传语义） | stdlib json + 自写 raw-value 包装、`pelletier/go-toml/v2` | 用 jcode 真实会话/配置文件做 fixture，gcode 解析→再序列化语义等价 | ~8k 行 |
| **M2** | Provider 层（分两批） | `provider-core` 抽象 + 批次①：Anthropic（Messages API/SSE 流式/OAuth PKCE）、OpenAI、Gemini；批次②：OpenRouter、Copilot、Bedrock、Azure 等 | stdlib `net/http`（HTTP/2 内建）+ `x/oauth2` + SSE 手写解析 | 同一订阅账号在 gcode 与 jcode 中完成流式对话 + 工具调用 round-trip；doctor 诊断命令 | 批①~10k，批②~15k |
| **M3** | Agent 主循环 + 核心工具 | agent loop（turn/流式分派/软中断注入/上下文预算）；工具：bash(PTY)、read/edit/write/apply_patch、glob/grep、ls、todo、web_fetch/search、mcp（stdio JSON-RPC） | creack/pty、stdlib regexp、doublestar；diff 自实现（对齐 `similar` 行为） | `gcode run "任务"` 无头完成真实编码任务；工具输出与 jcode 逐字段对拍 | ~25k 行 |
| **M4** | 会话与存储 | SQLite 会话存储、resume/session list、compaction、harness-api-server 首版 | `modernc.org/sqlite`（纯 Go） | `gcode --resume` 恢复历史继续工作；可直接读取 jcode 创建的会话库 | ~15k 行 |
| M5 | 记忆系统（后续批次） | WordPiece、手写 MiniLM-L6、memory graph（`graph.json` 兼容）、级联检索、HDBSCAN | 纯 Go 手写推理 | 嵌入余弦相似度 ≥0.999；可挂载 jcode 记忆目录检索 | ~8k 行 |
| M6 | Swarm 与安全（后续批次） | 协调者/Agent 状态机、CommsRouter、软中断、Plan Mode、command-risk | goroutine + channel | 双 Agent 协作完成跨文件重构；链路对拍 `SWARM_ARCHITECTURE.md` | ~20k 行 |
| M7 | TUI 全量（后续批次） | bubbletea 移植：渲染核心、markdown、diff 内联、权限 UI、session picker | bubbletea + lipgloss | 行为等价 + 关键屏对拍 | ~60k 行 |
| M8 | 发布打磨（后续批次） | 安装脚本、多平台 release、Go SDK、文档 | GoReleaser | 一键安装可用 | ~5k 行 + CI |

M0–M4 合计约 6 万行 Go（不含测试；每里程碑同步编写单元测试 + fixture 对拍测试）。

## 4. 对拍验证策略（每个里程碑通用）

1. **Fixture 对拍**：从 jcode 提取真实产物（会话 JSON、配置、记忆图、transcript）作为测试夹具，放 `testdata/jcode/`。
2. **双跑对比**：关键路径（工具输出、流式事件序列、嵌入向量）由脚本同时跑 jcode 与 gcode，diff 报告进 `docs/port/`。
3. **里程碑验收单**：每里程碑收尾输出对拍报告（模板见 [PARITY_TEMPLATE.md](PARITY_TEMPLATE.md)），列出"一致项 / 已知差异项（及理由）"，随分支 push 供审阅。

## 5. 风险与应对

| 风险 | 等级 | 应对 |
|---|---|---|
| 嵌入数值非 bit-exact | 低 | 差异 ~1e-6 级；验收口径为余弦相似度 ≥0.999 + 检索 top-10 排序一致 |
| jcode 活跃开发，目标在漂移 | 中 | 锁定 v0.98.1（commit `11952e02`）为基准；M0–M4 完成后再议追版本 |
| TUI 工时失控 | 高 | M7 拆为 4 个子批次独立交付；已定行为等价口径 |
| 部分 provider 无测试账号（Bedrock/Copilot 等） | 中 | 按公开 API 规范实现 + mock 对拍，标注"待实机验证" |
| 28 万行参照代码的上下文消耗 | 中 | 按里程碑按需精读对应 crate，不整库通读；grep 定位 + 子代理并行移植 |

## 修订记录

- 2026-10-17 初版（M0–M4 范围经用户批准）。
