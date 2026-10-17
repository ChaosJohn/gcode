# M1 Parity Report — 类型 / 协议层 + 配置层

- 基线: jcode v0.98.1 @ `11952e02ac76fa1c335633e9c6113691c1cf3395`
- gcode 分支: `m1-types`（合并入 main）
- 依赖: 新增 `github.com/pelletier/go-toml/v2 v2.4.3`（纯 Go，无 CGO）；`CGO_ENABLED=0` 全程成立
- 门禁: `gofmt`、`go vet ./...`、`go test ./...` 全绿

## 范围与对应上游

| gcode | 上游 crate | 内容 |
|---|---|---|
| internal/types | jcode-protocol, jcode-session-types, jcode-task-types, jcode-tools-types, jcode-usage-types, jcode-stream | 消息/块/会话/任务/流事件全部 serde 形状 |
| internal/base/config | jcode-config-types, jcode-base/config | 全部 26 个 section、枚举、默认值、加载/保存 |

## 逐字节/语义对拍证据

1. **Session fixture 往返（逐字节）** — `testdata/jcode/session_sample.json` 按 serde 字段序构造；`Session` 解码→`MarshalCompact` 再编码与 fixture **byte-identical**（`TestSessionFixtureByteRoundTrip`）。覆盖 ContentBlock 13 变体、SessionStatus 字符串/对象变体、ReplayEvent 三类、EnvSnapshot/MemoryInjection/TokenUsage。
2. **官方 config 模板** — `default_file.rs` 的 `default_config_file_contents()`（790 行注释模板）原样提取为 fixture；占位符 `@EFFORT_*@` 按上游同款平台替换后必须解析，且各 section 与 gcode 默认值全等（`TestDefaultTemplateParses`、`TestDefaultsCoverTemplateSections`）。
3. **真机文件验证（不提交数据）** —
   - `~/.jcode/sessions/session_chick_…json`（22 条消息）解析成功，再编码→再解析语义稳定；
   - `~/.jcode/config.toml` 解析成功（`TestParseRealJcodeConfig`）。
4. **上游用例移植** — WithTimestamps（user/tool_result/system-reminder 跳过、输入不可变）、MessagesWithDynamicSystemContext、EndsWithFreshUserTurn、cache_relevant_hashes_ignore_non_transmitted_metadata（timestamp/tool_duration_ms/reasoning_trace/text-cache_control 不影响投影哈希；非 text 的 cache_control 影响）、PromptTitle/StripTranscription、reasoning_display 默认与 legacy show_diffs 兼容、FNV-1a 向量（""/`0xcbf29ce484222325`，"a"/`0xaf63dc4c8601ec8c`）、resolve_tool_name 39 例、legacy 0–100 分数区间映射。

## serde 语义复刻要点

- **字段序**: 结构体按 Rust 声明序输出；map（Go 随机序）一律走 `jsonBuilder` 定序。
- **skip 规则**: `is_error`(仅 true)、`thought_signature`、`encrypted_content`、`status`、`title` 等按 `skip_serializing_if`；`summary`/`input`/`item`/`members` 为无 skip 的 Vec/Value，空值输出 `[]`/`null`。
- **外标签枚举**: SessionStatus 裸 PascalCase / `{"Crashed":{"message":…}}`；ResumeTarget PascalCase；ContentBlock `type` snake_case 且未知 type 报错；TurnStopReason 未知值回落 `unknown`（serde(other)）。
- **时间戳**: chrono AutoSi+Z（0/3/6/9 位小数、UTC 归一化）。
- **HookCommands**: 单命令裸字符串、多数组（serde 同形）。
- **config 保存**: 全字段写出（含默认值），与上游 `toml::to_string` 语义等价。

## 已知偏差（全部文档化，无静默差异）

| # | 偏差 | 理由 |
|---|---|---|
| 1 | 稳定哈希用 FNV-1a 替代 Rust `DefaultHasher` | 投影哈希仅进程内比较，值与 jcode 不同但性质相同 |
| 2 | 未知顶层 TOML 表保留在 `Extra` 并随保存写回 | 上游丢弃；gcode 保存到 `~/.gcode`，保留为超集 |
| 3 | 全部 config 枚举 lenient（未知→默认） | 上游仅部分枚举 lenient；对齐 issue #349 的"reload 不炸"哲学 |
| 4 | `display.reasoning_display` 非法值丢弃（None 语义）而非报错 | 同上；默认解析回退 legacy `show_thinking` |
| 5 | TOML 保存不追字节一致 | 上游自身保存非字节稳定；以 load→save→load 语义等价为验收 |
| 6 | `swarm_plan` replay 事件的 members/items 结构暂存 RawMessage | swarm 内部结构属 M6 |

## 延后项（已在 PORT_PLAN 挂账）

- 7 个其余 semantic_state 枚举 + TodoGoal/CatchupState → M6
- session search 评分 → M4；其余 telemetry 工具分类 → M3
- `~/.jcode` → `~/.gcode` 目录层（import-jcode）→ M4
