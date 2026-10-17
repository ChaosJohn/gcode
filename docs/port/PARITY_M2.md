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

## 批次二（HTTP transport + retry + OAuth + metadata + Provider 装配 + native web search）

新增：

- `models.go` — ALL_CLAUDE_MODELS 目录（opus-5-5 旗舰优先）、退役标记（claude-mythos）、
  质量排名、404 "Please use X" 建议解析、`anthropic_fallback_model`、
  `best_available_opus_model`。
- `errclass.go` — is_transient_transport_error 全标记表、is_retryable_error、
  account-limit / Fable 周额度 / model-not-found / reasoning-unsupported /
  OAuth auth 分类。
- `transport.go` — StreamResponse（initial-response 超时=空闲超时 180s、状态码 +
  Retry-After 解析（秒数与 HTTP-date，上限 120s）、OAuth vs API-key 双路径头、
  attribution 头、?beta=true）、逐行 SSE 泵（等价 \n\n 分帧、注释行跳过、
  可重试 SSE error 短路）、UUIDv5(simple) device_id、OAuthRequestMetadata、
  newOAuthRequestID（v4）。
- `oauth.go` — 凭据解析（~/.claude/.credentials.json → CLAUDE_CODE_OAUTH_TOKEN →
  ~/.jcode/auth.json 多账户/legacy）、token 刷新（scoped 优先，invalid_scope 回退
  无 scope，15s 超时）。
- `nativewebsearch.go` — server tool JSON（版本、max_uses、allowed/blocked 域名、
  新版本 allowed_callers=["direct"]）、session_offers_websearch、
  without_local_websearch。
- `retry.go` — RunStreamWithRetries 完整恢复阶梯：退避抖动（±20%）、
  Retrying 阶段事件、OAuth 强制刷新一次、404 模型回退（建议优先 → 目录质量序，
  跳过退役）、Fable 周额度切 Opus、reasoning 自愈一次、native replay 降级、
  瞬态重试 + RetryRollback 回滚。
- `provider.go` — Complete() 装配：server tools → FormatTools →
  FormatMessagesWithNative → reasoning 三元组 → metadata → RequestTools，
  驱动重试流。

对拍证据（`transport_test.go` / `provider_test.go`，全绿）：

- UUIDv5 device_id 算法对拍（sha1 验证，隔离真实 ~/.claude.json）。
- native web search 上游用例移植：默认版本+max_uses、新版本 pin direct、
  allowed/blocked 域名胜负、first-party 门槛、session 需含本地 websearch、
  SDK 自定义 websearch 不替换。
- httptest 端到端：请求头（x-api-key/beta/bearer/UA/attribution/1m beta）、
  SSE→事件序列 golden、Retry-After 7s 保留、可重试 SSE error 短路、
  瞬态断流回滚重试（RetryRollback 事件）、不可重试错误直通、
  404 → "Please use Opus 4.8" → 切换 claude-opus-4-8 且 OnModelSwitch 回调、
  Complete() 全装配（billing/identity/static 三块、adaptive thinking、
  output_config effort、无 temperature、metadata、web_search server tool、
  工具名 Bash→bash 解映射、TokenUsage）。
- 修正 M1 遗留：defaultWebSearchConfig 补 `native_max_uses=5`（上游
  WebSearchConfig::default 的 Some(5)，漏移植会影响 server tool JSON 字节）。

文档化偏差（批次二）：

1. 账户池轮换（has_alternate_account_in_pool / account failover）未移植——gcode
   暂只有单账户，判定点保留（quota/auth 错误且未接受响应时走重试阶梯），
   多账户池随 M4 之后补。
2. jcode 的 usage 探针 / 状态栏里程碑（oauth preflight、eval endpoint）不在
   complete 路径，未移植；相关特征（billing 头、attribution 头）已对齐。
3. Go net/http 的连接池行为与 reqwest 不同：重试时统一换新 Client
   （fresh_transport_client 等价），首次请求共享 Client。
4. fmtError 链拼接（anyhow {:#}）以 errorChain 近似：顶层消息 + 解包链，
   分隔符同为 ": "。
5. 模型目录缓存刷新（catalog_refresh）未移植；目录静态取 ALL_CLAUDE_MODELS。

## 状态

M2 Anthropic 部分完成（请求成形/修复/SSE/传输/重试/OAuth/装配 + 测试全绿）。
剩余：OpenAI provider（Responses API / Chat Completions / ChatGPT OAuth）、
Gemini provider、schema-dialect 归一化。
