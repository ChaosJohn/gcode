# 里程碑对拍报告：M0 · 工程骨架

| 项 | 值 |
|---|---|
| 里程碑 | M0 工程骨架 |
| 分支 | `m0-scaffold` |
| 基线 | jcode v0.98.1 @ `11952e02` |
| 报告日期 | 2026-10-17 |
| 结论 | ✅ 验收通过 |

## 1. 交付物清单

- [x] `go.mod`（`github.com/ChaosJohn/gcode`，go 1.26，**零第三方依赖**）
- [x] 包骨架 12 个（`internal/{types,toolcore,agent,embedding,appcore,harness,tui}`
      + `internal/base/{,tools,provider,memory,swarm,storage}`），每个 doc.go 注明
      对应 jcode crate 与目标里程碑
- [x] `cmd/gcode`（最小 CLI：version/help + 计划命令占位，退出码契约 0/1/2/3）
- [x] `cmd/gcode-harness`（M4 占位 + version）
- [x] `internal/version`（ldflags 注入的构建元数据，banner 携带 pure-Go/no-cgo 标记）
- [x] CI：`.github/workflows/ci.yml`（3 OS 验证 + 6 目标交叉编译，全程 `CGO_ENABLED=0`）
- [x] `Makefile` + `scripts/cross-build.sh`（`make check` 本地门禁）
- [x] 移植文档：`docs/port/{BASELINE,PORT_PLAN,DECISIONS,PARITY_TEMPLATE}.md`
- [x] README（状态表、口径、归属声明）

## 2. 对拍项

| # | 对拍对象（jcode 源位置） | gcode 对应实现 | 方法 | 结果 |
|---|---|---|---|---|
| 1 | 仓库根 `Cargo.toml` 88-crate workspace → 单 Go module 收敛 | `internal/` 包结构（doc.go 逐一映射） | 人工比对 CRATE_OWNERSHIP_BOUNDARIES.md | ✅ |
| 2 | jcode CLI 顶层命令面（run/serve/doctor/resume 等） | `cmd/gcode` 命令表 + 里程碑占位 | 命令清单比对 | ✅ |
| 3 | 构建纪律（上游 jemalloc/openssl 等 C 依赖） | 零 CGO 构建 + banner 声明 | `CGO_ENABLED=0` 六目标交叉编译 | ✅ |

## 3. 一致项

- CLI 顶层命令命名与 jcode 对齐（run/serve/doctor/resume）。
- 六目标交叉编译矩阵（linux/darwin/windows/freebsd × amd64/arm64）一次通过。

## 4. 已知差异项

| 差异 | 理由 | 影响评估 | 依据口径 |
|---|---|---|---|
| CLI 用 stdlib flag 而非 cobra | M0 零依赖原则；M3 引入子命令复杂度后再评估 | 无 | PORT_PLAN §1 |
| `gcode run` 等返回退出码 3 + 里程碑指引 | 尚未移植，明示而非静默失败 | 无（占位即契约） | D4 |

## 5. 测试与验证数据

```
gofmt -l .            → clean
go vet ./...          → pass
go test ./...         → ok (cmd/gcode, cmd/gcode-harness, internal/version)
CGO_ENABLED=0 cross   → linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
                        windows/amd64 freebsd/amd64 → 全部构建成功
./bin/gcode version   → gcode 0.0.0-dev (…, go1.26.5, pure-Go/no-cgo)
./bin/gcode run …     → exit 3, 提示 planned for M3
```

测试过程中发现并修复 1 处真实缺陷：命令表匹配键混入参数占位符导致
`lookup` 失配（TestPlannedCommandsReportMilestone 捕获）。

## 6. 遗留与后续

- golangci-lint 暂缓至 M8（M0 以 gofmt+go vet 为门禁，避免工具链版本摩擦）。
- M1 起引入 `testdata/jcode/` fixture 对拍（会话/配置 JSON 语义等价）。
