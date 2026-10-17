# M2 对拍报告（批次一：Anthropic 请求成形 / 历史修复 / SSE）

基线：jcode v0.98.1 @ `11952e02ac76fa1c335633e9c6113691c1cf3395`
范围：`jcode-provider-core/src/anthropic.rs`、`jcode-message-types/src/provider_native.rs`、
`jcode-provider-anthropic/src/{lib.rs, history_repair.rs}`、
`jcode-provider-anthropic-runtime/src/{reasoning_request.rs, sse_types.rs}` 中与请求成形、
历史修复、SSE 解码直接相关的部分。HTTP 传输 / OAuth 令牌 / 重试 / OpenAI / Gemini 属 M2 批次二。

## 新增

- `internal/types/providernative.go`：provider-native 项的判定 / 展示 / 文本降级
  （`is_anthropic_server_tool_block`、`provider_native_display`、`provider_native_text_fallback`、
  `ProviderNativeTracker` 配对器，`web_search_call` 的 OpenAI 分支一并移植）。
- `internal/provider/anthropic/`：
  - `caps.go` — 1M 上下文分级（Native1M / OptIn1M / Standard）、reasoning caps 表、
    thinking-always-on（Opus 5.5 / Fable 5.1）、`anthropic_max_output_tokens`、
    `[1m]` / `-YYYYMMDD` 后缀剥离、OAuth 工具名双向映射、beta 头常量。
  - `apitypes.go` — ApiRequest/ApiMessage/ApiTool/ApiSystem/ApiThinking/ApiOutputConfig 等
    wire 类型；所有 MarshalJSON 经 jsonBuilder 按 serde 字段序输出；HTML 不转义
    （types.MarshalCompact）。Marshaler 内一律逐元素编码，避免 encoding/json 自递归。
  - `format.go` — FormatTools（deferred 置尾、全 deferred 提升为 eager、末位 cache 断点）、
    RequestTools（server tools 夹在 eager 与 deferred 之间）、BuildSystemParamSplit
    （OAuth 四块：billing / identity / static 缓存 / dynamic 不缓存）、
    AddMessageCacheBreakpoint（最近两条 assistant 滑动双标记）、
    FormatMessagesWithNative 完整管线。
  - `historyrepair.go` — hoist_late_tool_results（turn 折叠 + anchor 插入 + attached blocks）、
    attached_to_result（图片及其标签归位）、repair_dangling_anthropic_server_tools
    （末尾 server_tool_use 保留 = pause_turn 形状）、dedupe_tool_uses / dedupe_tool_results
    （占位符让位真结果）、rewrite_orphaned_tool_results、answer_unpaired_tool_uses、
    downgrade_native_replay + IsInvalidNativeReplayError。
  - `reasoning.go` — adaptive thinking（display=summarized + binding beta）、手动预算表
    （minimal/low 1024、medium 4096、high 8192、xhigh/max 16384、不超过 max_tokens/2）、
    BuildReasoningRequestParts（thinking/output_config/temperature 三元组）、
    RecoverRejectedReasoning（always-on 保 binding、其余全撤 + temperature 1.0）。
  - `sse.go` — SSE 解析（\n\n 与 \r\n\r\n、部分缓冲）、process_sse_event 全事件映射
    （message_start usage、thinking/tool_use/server_tool_use 累积器、message_delta stop_reason、
    ping→ConnectionPhase、error、未知忽略）、FinalTokenUsage。

## 对拍证据（测试）

`internal/provider/anthropic/`（`go test ./internal/provider/anthropic/` 全绿）：

- 请求体逐字节 golden（OAuth 四块 system + tool cache_control + thinking/output_config + temperature）。
- 上游移植用例：`test_orphaned_tool_result_is_rewritten_as_text`、
  `test_displaced_tool_result_still_answers_tool_use_in_place`（含角色交替 + tool_use 就地应答断言）、
  `test_no_repair_when_tool_results_present`、`test_parallel_image_tool_results_stay_contiguous`、
  `test_dangling_tool_use_repair`（成形半边）、`test_anthropic_thinking_sse_events`、
  `test_parse_sse_event`、cache-breakpoint 三例。
- SSE 全流 golden：message_start→text→tool_use→message_delta→ping→message_stop 的事件序列
  逐一比对；server_tool_use 增量累积后逐字回放、web_search_tool_result 原文直通、
  文本降级摘要（编号 + URL + "No results"/"... and N more"）。

## 文档化偏差

1. `MAX_DISPLAY_RESULTS` 摘要截断语义一致（前 10 条 + "… and N more"），但 Rust 端
   `trim_end()` 与 Go `strings.TrimRight(out, " \t\n")` 等价实现。
2. usage 中 `service_tier` 在 Rust 侧经 UsageInfo 透传后仅用于日志；Go 侧保留在
   SseStreamState.ServiceTier，未接入 TokenUsageEvent（stream.rs 的 TokenUsage 变体无该字段）。
3. 传输层日志（`jcode_logging::warn` 的 moved/dropped 计数、orphan 改写计数）未接入——
   gcode 日志设施随 M2 批次二（HTTP 传输）落地，届时补齐计数上报。
4. `anthropic_max_output_tokens` 的 haiku 分支按前缀 `claude-haiku-4-5`/`claude-haiku-4.5`
   匹配（64k），其余未知代际 32k；与上游 if-chain 语义一致，未覆盖的上游新增分支
   （若有）将在批次二对照 model 目录时复核。
5. `format_messages_with_tools` 的 OAuth 分支在批次二接入 Provider 结构体后按上游签名
   （messages, is_oauth, tools）收敛；当前 `FormatMessagesWithTools(messages, isOAuth, tools)` 已对齐。
6. schema-dialect（input_schema oneOf 归一化）仍挂账，随批次二实现。

## 状态

M2 批次一完成；批次二（HTTP transport + retry + OAuth 令牌 + metadata + Provider 装配 + native_web_search）进行中。
