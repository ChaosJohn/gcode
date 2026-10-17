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

## 批次三：schema-dialect + OpenAI provider（Responses API + Codex OAuth）

移植范围：

1. **schema-dialect 子系统**（jcode-schema-dialect，2.4k 行）→
   `internal/schema/dialect/`：keyword 分类（Subschema/SubschemaMap/
   SubschemaArray/Data；未知关键字按 Data 防腐）、apply 单一递归
   （allow-list 过滤、one_of→any_of 改写、const→enum、顶层 combiner 拍平、
   全深度 combiner 拍平（Antigravity）、allOf 分支合并、悬挂 required 剪除、
   object 补 properties）、registry 六方言（openai/gemini/anthropic/openrouter/
   antigravity-claude/antigravity-bridge，全部照抄关键字表）、quirks 持久化
   （~/.gcode/schema-quirks.json；GCODE_SCHEMA_QUIRKS_PATH/GCODE_HOME 覆盖；
   load-bearing 关键字永不可丢）。测试：上游 build_tools 三例语义 + anthropic/
   gemini 变换 + quirks 学习与持久化。
2. **OpenAI strict schema**（provider-core openai_schema.rs）→
   `internal/provider/openai/schema.go`：schema_supports_strict（object 无
   properties、additionalProperties:true/object、无类型 property、无类型
   combiner 分支、无 items 约束数组、$ref、无类型信息 property 七类 fail-closed）、
   strict_normalize_schema（全属性 required、可选属性 nullable 化、未知 format
   剥除、additionalProperties:false）、make_schema_nullable、build_tool/
   build_tools（deferred 不进顶层 tools）。测试移植上游 strict 五例。
3. **build_responses_input**（request.rs 全管线）→ `request.go`：user/assistant
   遍历 → raw items（图片 input_image、compaction 体积守门
   9,500,000/10,485,760 字符、openai reasoning 重放无 status、web_search_call
   原样重放/外部 provider_native 降级、function_call + 输出配对/延迟/缺失注入）、
   open_calls 排水、孤儿输出改写 user message（可丢弃占位符）、安全注入、
   按 call_id 重排（真实输出胜过占位符）、additional_tools 按 ToolReference
   插入 function_call_output 之后（前缀字节不变）。测试移植上游 8 例语义。
4. **SSE 流解码**（stream.rs 1161 行 + citations.rs）→ `stream.go`：
   Responses 全事件映射（created/output_text.delta/reasoning.delta/added/
   arguments.delta+done/output_item.done/incomplete/completed/failed/error/
   [DONE]/websocket fallback 通知/明文断流）、并行工具流状态机（identity 别名
   output:/call:/item 三键解析、未命名调用不阻塞、done 快照只发未见后缀、
   快照不一致报错、null/空参数归一化 "{}"）、文本包裹工具调用恢复
   （to=functions.<name>{...}，suffix 清洗）、reasoning 重放去重
   （流式过后不再全量重放）、compaction/web_search_call/image_generation_call
   （落盘 .gcode/generated-images + metadata json）、usage
   （input_tokens_details/prompt_tokens_details 回退、cache_write 可独立存在）、
   stop_reason（commentary phase、incomplete_details.reason、status）、
   citations 渲染（char offset、URL 白名单 http/https、marker U+E200..U+E201
   整体替换、已渲染 Markdown 去重、无效注解静默保留原文）、UTF-8 分块续解码。
   测试移植上游 stream_tests 11 例 + stream_tool_tests 14 例 + citations 5 例。
5. **provider 装配 + Codex OAuth** → `provider.go`/`oauth.go`：
   build_response_request（instructions/input/tools+hosted/tool_choice/
   parallel_tool_calls:false/stream/store:false/include reasoning.encrypted_content、
   API 模式 max_output_tokens、reasoning{effort,summary:auto}、service_tier
   ultrafast 门控、context_management 压缩阈值、prompt_cache_key/retention
   （gpt-5.6+ 省略、扩展保留模型默认 24h、in_memory/24h 归一化））、
   hosted web_search 门控（native_enabled && first_party && 非 codex &&
   session 有本地 websearch；allowed_domains filters；附加时剔除本地工具）、
   image_generation（ChatGPT 模式且非 codex）、effort 归一化/default
   （gpt-5.6-sol→low）/additional_tools 版本门（gpt-5.4+）、端点解析
   （GCODE_OPENAI_API_BASE→OPENAI_BASE_URL→OPENAI_API_BASE，/responses 去重）、
   ChatGPT 后端固定 chatgpt.com/backend-api/codex + originator:
   codex_cli_rs + chatgpt-account-id、Codex OAuth（client
   app_EMoamEEZ73f0CkXaXp7hrann、auth.openai.com、scopes、1455 端口回调、
   refresh grant x-www-form-urlencoded）、瞬态重试（MAX_RETRIES=3、base 1s
   指数退避、RetryRollback 回滚事件、provider-core 瞬态分类器移植到
   internal/provider/transporterr 共享包）。
6. **Anthropic 侧接回**：FormatTools 现在经 dialect.Anthropic 归一化
   input_schema（上游 anthropic_input_schema 等价，顶层 combiner 拍平 +
   强制 properties map）。

测试统计（批次三新增）：dialect 11 例、openai schema 7 例、request 12 例、
stream 25 例、citations 5 例、provider 12 例，全绿。

文档化偏差（批次三）：

1. WebSocket 传输（persistent ws、prewarm、healthcheck、compact ws）未移植——
   仅 HTTPS/SSE 路径；ws 是传输优化，语义等价。
2. swarm effort 映射（swarm/swarm-deep → API effort 按模型 ladder 解析）未移植：
   swarm 功能本身不在 M0–M4；NormalizeReasoningEffort 接受哨兵值存储，
   APIReasoningEffort 拒绝其上网络径。
3. model catalog 快照依赖（model_selection.rs、openai_model_supports_ultrafast
   的目录检查、账户可用性 fallback）未移植——ultrafast 门控近似为非 codex
   放行；目录与可用性随 M4 之后的 catalog 工作补。
4. native compaction 阈值计算（context_window 感知）接线到 agent 循环时补，
   请求字段 context_management 已就位。
5. image_generation 落盘目录用 `.gcode/generated-images`（上游
   `.jcode/generated-images`，D4 写侧改名）。
6. 环境变量 JCODE_OPENAI_API_BASE → GCODE_OPENAI_API_BASE（其余
   OPENAI_* 原样）；quirks 存储 ~/.jcode/schema-quirks.json →
   ~/.gcode/schema-quirks.json（D4）。
7. Go net/http 无 reqwest 的 per-request 中间件栈：Retry-After 解析、
   初始响应超时（initial response timeout）并入 http.Client 超时与错误包装，
   语义等价（断连包装 "connection closed before response headers" 进瞬态分类）。

## 批次四：Gemini provider（Code Assist + Developer API）

移植范围：

1. **请求成形**（jcode-provider-gemini lib.rs）→
   `internal/provider/gemini/types.go`：build_contents（role user/model 映射、
   tool_use→functionCall 带 id、tool_result→functionResponse 反查工具名、
   错误结果包 {"error": ...}、图片 inlineData、非 object 参数归一化 {}）、
   **Gemini-3 thoughtSignature 重放策略**（own→carried-forward 全历史语义，
   本地合成/导入历史不再 400 #339）、DowngradeToolCallsToText 恢复策略
   （调用与结果降级为 `[previous tool call/result]` 文本，Antigravity runtime
   语义）、build_system_instruction_with_tool_guard（有工具时附加
   MALFORMED_FUNCTION_CALL 防护块）、build_tools + gemini_compatible_schema
   （经共享 dialect.Gemini：additionalProperties/propertyNames/uniqueItems 剥除、
   const→enum、oneOf→anyOf、悬挂 required 剪除）。
2. **认证** → `auth.go`：Developer API key 优先（GEMINI_API_KEY→GOOGLE_API_KEY
   →gemini.env，force_oauth 可钉死 OAuth 路线，JCODE→GCODE 改名）、
   Google OAuth（Gemini CLI desktop client 681255809395-…，refresh grant
   x-www-form-urlencoded，60s 提前刷新窗口，单飞锁）、凭据存
   ~/.gcode/gemini_oauth.json，可回退读取官方 CLI 的
   ~/.gemini/oauth_creds.json。
3. **Code Assist 运行时**（runtime lib.rs）→ `provider.go`/`models.go`：
   loadCodeAssist→onboardUser LRO（5s 轮询、free-tier 不带 project 元数据、
   standard-tier VPC-SC 降级）、generateContent 信封（model/project/
   user_prompt_id UUID/request.contents+systemInstruction+tools+toolConfig
   AUTO+session_id）、Developer API 直发内层体（无信封、x-goog-api-key 头、
   models/{model}:generateContent）、模型发现（ListModels 剥 models/ 前缀 +
   loadCodeAssist 全树收集 + 目录缓存 gemini_models_cache.json）、
   HTTP 层（GeminiCLI-tui/0.57.0 UA + x-goog-api-client gl-node/26.4.0 配额
   身份、send_with_retry 两次瞬态传输重试 250ms、generateContent/loadCodeAssist
   的 5xx/429 重试阶梯 MAX=5 base 1500ms 带 Retry-After 提示、daily/billing
   429 不重试、onboardUser 永不自动重放）、模型 404 回退阶梯
   （FALLBACK_MODELS 逐个试、成功即钉住并 OnModelSwitch）、
   schema 拒答恢复（dialect.RecoverFromError 学习 + 单次重试）。
4. **complete() 事件映射**：非流式响应一次性映射 →
   ConnectionType(https)/Authenticating/SendingRequest/WaitingResponse →
   SessionId → Streaming → TokenUsage → 逐 part（text→TextDelta、
   functionCall→ToolUseStart/InputDelta/ToolUseEnd/ToolUseSignature）→
   MessageEnd（finish_reason 小写）。promptFeedback 拦截（"Gemini blocked the
   prompt ({reason})"）、SAFETY/BLOCKLIST/PROHIBITED_CONTENT/SPII/RECITATION
   无内容即报错、finish_message 截断 300。
5. **dialect 补齐**（jcode-schema-dialect rejection.rs + recover_from_error）→
   `internal/schema/dialect/rejection.go`：三类真实拒答 shape（#754 Unknown
   name…Cannot find field、#543/#687 OpenAI 格式与关键字、#655 悬挂 required、
   #713 类型缺失、#495 combiner），escape 去引号、多 fieldViolations 去重收集、
   load-bearing 拒绝不可吸收、学习一次重试一次不循环。顺带修复 quirks 存储
   的测试路径分支未拼文件名的 bug（此前每次读空存储）。

测试统计（批次四新增）：gemini 19 例、dialect rejection 10 例，全绿；
含 httptest 端到端（Developer API 事件序列 golden、Code Assist 信封 +
bearer、模型 404 回退钉住、thought_signature 400 直通、schema 拒答学习重试、
prompt 拦截、ListModels 发现 + 目录缓存）。

文档化偏差（批次四）：

1. Antigravity runtime（独立 provider、antigravity 桥方言 quirks 预置、
   DowngradeToolCallsToText 的完整 wiring）延后——Antigravity 是 M2 范围外的
   第二个 Gemini 形态 provider（计划批准的范围为 Anthropic/OpenAI/Gemini）；
   降级策略本身已随 types.go 就位。
2. login 浏览器流（PKCE + 本地回调端口 + codeassist.google.com/authcode 手动
   粘贴）未移植——gcode 登录编排属 CLI 侧（M3 接线时补），refresh 链路已完整。
3. gemini_cli_command / has_gemini_cli（检测本机 Gemini CLI 作登录辅助）未
   移植；CLI 凭据导入已支持。
4. usage 探针 / catalog_refresh 定时刷新 / available_models_display 与 TUI
   相关部分延后（同 Anthropic 批次二偏差 2/5）。
5. oauth_creds.json 的 expiry_date 解析按 epoch 毫秒数处理；CLI 写 ISO8601
   的情形按无法解析→立即视为过期→走 refresh 降级（行为安全）。
6. 环境变量 JCODE_GEMINI_MODEL→GCODE_GEMINI_MODEL、
   JCODE_GEMINI_FORCE_OAUTH→GCODE_GEMINI_FORCE_OAUTH、
   JCODE_GEMINI_USER_AGENT→GCODE_GEMINI_USER_AGENT、
   JCODE_GEMINI_PROJECT→GCODE_GEMINI_PROJECT（D4 写侧改名）。

## 批次五：provider 门面（provider-core trait 核心）

移植范围（provider-core Provider trait 的 M3 消费子集）→
`internal/provider/provider.go` + `adapters.go`：

1. Provider 接口：Name/Model/SetModel/AvailableModels/
   SupportsImageInput/SupportsCompaction/Fork/InvalidateCredentials/
   Complete(TurnRequest, emit)。OAuth 账户变更器、route selection、
   catalog sharing identity 随 M4 后账户工作补。
2. TurnRequest（messages/tools/system/dynamic_system/resume_session_id/
   max_tokens_override/show_thinking/effort）+ messages_with_dynamic_system_context
   （`<system_context>` 包裹的 in-band user 消息）。
3. 三个适配器：claude（catalog = ALL_CLAUDE_MODELS、Complete 装配
   CompleteConfig/ProviderOptions/RetrySettings）、codex
   （BuildResponsesInput 组装 Input、resume_session_id→prompt_cache_key、
   max_tokens_override→MaxOutputTokens）、gemini（静态目录 + IsGeminiModelID
   校验、Code Assist 状态失效、OnModelSwitch 回写 SetModel、fork 共享
   HTTP client）。

测试：适配器模型生命周期/fork 隔离/目录校验/dynamic system 包装 5 例。

## 状态

M2 完成：三大 provider（Anthropic/OpenAI/Gemini）+ schema-dialect +
provider 门面全部落地，测试全绿。待推送（批次四的 Gemini CLI 公开
OAuth client id 触发 GitHub Push Protection，等待仓库所有者在
secret-scanning unblock 页面放行）。下一步：M3 agent loop + 核心工具。

---

# M3 对拍报告（agent loop + 核心工具）

## M3 批次一：toolcore 契约 + 基础文件工具

移植范围：

1. **toolcore 契约**（jcode-tool-types + jcode-tool-core）→
   `internal/toolcore/toolcore.go`：ToolOutput（title/metadata/images）、
   resolve_tool_name 全别名表（functions. 前缀剥离、shell_exec/Read/multiedit/
   patch/grep→agentgrep/PascalCase OAuth 别名等）、ensure_intent_in_schema
   （每个 object schema 中央注入 required `intent` + 可选
   `accept_large_output` 逃生舱，MCP 代理 schema 同样覆盖）、ToolContext
   （session/message/tool_call 身份、working_dir 解析、stdin 请求通道、
   for_subcall 父作用域 retry-stable 子 id：`len:parent:child` 长度前缀组合）。
2. **image_normalize**（jcode-base）→ `internal/imaging`：四家 provider 接受
   的 png/jpeg/gif/webp 魔数嗅探直通；BMP/ICO/TIFF 解码重编 PNG（x/image，
   纯 Go 无 CGO；ICO 支持内嵌 PNG 与 legacy BMP 双形态）；HEIC/AVIF/SVG/
   损坏数据给模型可读的拒绝原因；PNG/GIF/JPEG/BMP/WEBP 尺寸嗅探。
3. **read 工具**（tool/read.rs）→ `internal/tools/read.go`：双 range 语义
   （start_line/end_line 1-based vs offset/limit 0-based，混用与零值完整
   错误文案）、缺失文件同目录建议（containment 相似度，最多 3 个）、扩展名
   + 前 8KB NUL 密度二进制检测、图片按 20MB 上限走 normalize 附
   labeled_image + 📷 标题（BMP 转 PNG 附 Converted from 说明，#1712）、
   PDF 文本抽取（ledongthuc/pdf 纯 Go，页内 10000 截断、失败降级元数据）、
   `%5d\t` 行号 + 2000 列截断 + continuation hint（两种 style 各自的
   续读参数）。
4. **write 工具** → `write.go`：父目录创建、进程内文件锁、旧内容 diff
   摘要（`42- old`/`42+ new` 20 行上限）、file touch 事件（经
   SetFileTouchObserver 钩子，swarm bus 随 M4+ 接线）、`/dev/null` 创建
   unified diff、旧文件不可读时不宣称权威 diff。
5. **edit 工具** → `edit.go`：edits 数组/单编辑速记互斥、全有或全无
   （失败聚合 `✗ Edit N: ...` 报告 + "Edits apply in order" 提示）、
   flexible_match_hint 三级诊断（trim/缩进/absent）、单编辑输出（紧凑 diff
   + `%4d│ ` 上下文 ±3 行）、多编辑输出（✓ 逐条 + Diff: 摘要 30 行）、
   file_diff attach（```diff 围栏 + metadata.diff 权威 unified diff，
   context radius 3、`\ No newline at end of file`、控制字符文件名引用）。
   自研 LCS 行 diff 对齐 similar::TextDiff 的 unified 渲染
   （`@@ -37,7 +37,8 @@` hunk 合并、`,1` 省略、空 range 前移一位）。
6. **ls 工具** → `ls.go`：默认 ignore 表 + 用户 pattern、目录优先排序、
   隐藏文件跳过、深度 5、100 条目截断 + files/directories 计数；
   自研 globMatch（`**`/`*`/`?`）。
7. **config_edit_notice + change_report** → `confignotice.go`：活跃
   config.toml 写入后追加 per-key 变更报告（dotted key 扁平化、值 JSON
   渲染、live now/needs restart 分区、TOML 解析失败显式 WARNING）；
   ConfigPathProvider 由 app 注入。

测试统计（批次一新增）：toolcore 5 例、imaging 4 例、tools 10 例
（上游 file_diff/edit/read/ls 用例移植 + unified diff golden 全量断言），
全绿。

文档化偏差（M3 批次一）：

1. swarm bus（Bus::global FileTouch 事件）改为进程内 observer 钩子
   （SetFileTouchObserver），事件 shape 保持一致；多进程 swarm 协调随 M4+。
2. edit_stats 遥测记录（usage 统计）未移植——M2 已声明的 telemetry 整体
   延后项的一部分。
3. PDF 文本抽取用 ledongthuc/pdf 替代 pdf_extract crate：纯 Go 无 CGO 约束
   下的最近等价物；页面分隔符（\x0c）与降级行为保持一致，抽取格式细节
   （空格重建）可能有差异。
4. ICO 解码自实现（x/image 无 ICO）：现代 PNG-内嵌与 legacy BMP-内嵌两形态
   已覆盖，罕见特例（调色板边界）可能拒绝并给出可读原因。
5. config change_report 的 liveness 分区（gateway/acp/launch_hotkeys 需
   重启）照抄；gcode 侧配置节名如后续分叉需同步此表。

## M3 进度

批次一完成。剩余：批次二 agentgrep（上游独立 crate 6.3k 行，含 smart DSL/
outline/find 模式与置信度评分）、批次三 bash PTY、批次四 webfetch/websearch
+ mcp、批次五 agent 主循环（agent.rs 1226 行 + agent/ 10.5k 行）。

## M3 批次二：agentgrep 引擎与工具

移植范围（agentgrep crate v0.1.7，6.3k 行 Rust）→ `internal/agentgrep` +
`internal/tools/agentgrep.go`：

1. **structure.rs** → structure.go：per-language 行扫描符号检测（rust
   fn/struct/enum/trait/impl 含 pub(crate)/async/generics 剥离、ts-js
   function/class/interface/箭头函数、python def/class、markdown 标题、
   generic SECTION 大写段）、行区间 finalize（下一 item 起点或 EOF）、
   detect_language 扩展名表、infer_role 路径角色（test/docs/ui/auth/
   provider/config/handler/implementation/generic）。
2. **search.rs** → grep.go + rg.go：rg 快路径（`--no-config`、fixed-strings/
   regex `-e` 传参防 `-` 前缀解析、`--follow`、`--no-messages`、hidden/
   no-ignore、`-g` glob、自定义 `--type-add/-t` 实现 glob∩type 交集语义、
   exit 0/1/2 语义、NUL 分隔 paths-only、`path:line:col:text` 冒号路径
   前向重切）+ 原生 walker 兜底（默认 ignore、gitignore/rgignore 基础规则、
   隐藏跳过、符号链接 follow、字节序确定性排序）；结构分组（file-scope
   组前置、符号组按文件序、≥12 匹配时 max 8 组/2 other、≥24 匹配跳过结构
   抽取走 dense file-scope）；matcher（literal contains / regex）。
3. **find.rs** → find.go：tokenize（字母数字+下划线）、path/symbol/text
   证据评分（full query 120 / token 25 / symbol 8×4 cap / text 4 / role 20、
   role boost +20、docs -25、test -15、无证据即弃）、top 8 symbols、
   score 降序 + path tie-break、max_files 截断。
4. **outline.rs** → RunOutline + RenderOutlineOutput。
5. **render.rs** → render.go：grep 渲染（query/matches 头、symbols 行、
   `- kind label @ a-b`、`- @ line text`、paths_only、max_regions 截断 +
   "more matches omitted (max_regions=N)"、非代码文件 3 行 cap）、240 字符
   match 行压缩（match 前 80 字符上下文 + `[truncated: N chars before/after]`）、
   find/outline 渲染 shape。
6. **workspace.rs** → 收敛进 grep.go（walker/read_text_file NUL 检测/
   normalize_file_type 别名）。
7. **工具包装**（tool/agentgrep.rs + args.rs）：schema（mode enum
   grep/find/outline/trace、query/file/terms/regex/path/glob/type/max_files/
   max_regions/paths_only，无内部字段泄漏）、mode 默认 grep、
   resolved_search_scope（file→root+basename glob、目录→root）、match-all
   glob 剥离、find 未定向空 query 拒绝、outline file/query/first-term 与
   file-valued path 容错、exact-file 过滤（grep/find 各自的 result 过滤 +
   计数重算）、错误统一 `agentgrep {mode} failed after {ms}ms: {detail}`、
   无 session cwd 且无绝对路径拒绝。

测试：agentgrep 包 11 例（上游 structure/find/render/glob/walker 断言 +
rg/native 双路径对拍、`-` 前缀 query、非法 regex hint）+ tools 2 例
（mode 矩阵、cwd 守卫）。全绿，全仓 gate 通过。

文档化偏差（M3 批次二）：

1. trace/smart 模式（smart_dsl.rs + smart_engine.rs，1.3k 行）未移植：
   terms 解析报错后指引用 grep/find/outline，schema 枚举保留 trace 以便
   后续批次接入；跟批（M3 收尾或 M4 前）补齐。
2. rg 缺失时原生 walker 是兜底而非逐字节等价：ignore 规则子集
   （无 `**` 目录重锚定、无字符类、无嵌套 .gitignore 优先级 beyond 目录
   前缀），rg 路径语义为准。
3. 非 UTF-8 路径的 disambiguate（`#b=` + hex token 流）简化为
   lossy + 整体 hex 后缀，行为差异仅影响非法字节文件名（罕见）。
4. 前台 5s 预算转后台任务（AGENTGREP_FOREGROUND_BUDGET → background
   registry）未移植——后台任务基础设施随 agent 主循环（批次五）落地；
   当前同步执行，5s 预算常量已导出备接。
5. 多线程并行文件处理（thread::scope chunk）以单 goroutine 顺序处理
   替代（rg 快路径下吞吐已足够；原生路径处理大仓时慢但语义一致）。
