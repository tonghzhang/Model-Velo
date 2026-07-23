# Model-Velo 阶段 3 门禁记录

> 检查日期：2026-07-23
> 当前结论：阶段 3 的生产链、非 race 自动化检查、故障矩阵和独立性复查已完成。`go test -race` 在业务测试执行前被本机 Go/race 工具链阻止；用户已于 2026-07-23 明确允许带着该环境缺口进入阶段 4，但 race 总门禁仍不能改写为通过。

## 1. 目标、非目标和验收条件

阶段 3 的目标是完成非流式请求的多 Provider 可靠性链：先生成有序 Route Plan，再让每个候选独立经过 Circuit Breaker、Provider Queue、Key 选择、有限 Retry 和单次上游调用；只有策略允许的最终失败才能进入下一候选。客户端取消、请求总预算和单次调用超时必须贯穿队列、退避、上游请求与 Fallback。

本阶段不实现 SSE、首 Chunk 提交边界、Usage Event、Redis Stream、Usage Worker、Prometheus、Tracing、CI 或 benchmark，也不把 Queue 扩展成跨实例分布式调度器。

门禁要求：

1. 路由、Breaker、Queue、Key、Retry、Attempt 和 Fallback 的生产调用链完整；
2. 400、401/403、429、指定 5xx、网络、超时、取消、Breaker Open、Queue 满、Key 耗尽和能力不匹配有稳定策略；
3. 高风险并发状态、资源归还和取消传播有合并证据；
4. `gofmt`、`go test ./...`、`go vet ./...` 通过；race 无法执行时必须记录真实工具链原因；
5. 生产 Go 代码对 GoModel、Bifrost 及合并参考集合的字面与归一化近似重复度严格小于 10%；
6. 文档只描述已实现能力，未经用户授权不进入阶段 4。

## 2. 实际调用链与生产收口

```text
chatHandler.complete
  -> 认证 -> 模型授权 -> Redis 限流
  -> Route Plan -> Exact Cache
  -> Orchestrator（统一请求预算、有序候选循环）
     -> Attempt Executor（单候选有限 Retry）
        -> Breaker Allow
        -> Queue Acquire
        -> Provider Key Select
        -> Adapter Complete（单次 attempt timeout）
        -> Key / Breaker 反馈
        -> Queue Lease / Breaker Permit / Timer 释放
     -> 按 Failure Signals 决定停止或 Fallback
  -> primary 成功时回填 Cache；Fallback 成功不回填
  -> 完整 JSON 或结构化错误
```

本次门禁审计发现并修复一处装配安全缺口：当 Adapter Registry 中存在需要 API Key 的 Adapter 时，`NewAttemptExecutor` 现在要求非空 Provider Key Registry。错误装配会在构造阶段失败，不再等到请求执行时因空指针崩溃；全部 Adapter 均为无鉴权类型时仍允许 Key Registry 为空。

没有新增 Retry/Fallback 抽象，也没有改变既有恢复策略。工作区在本次开始前已有一组未提交的 `internal/provider` 边界重构；本次保留这些改动，并在当前完整工作树上执行所有检查。

## 3. 合并故障与并发证据

阶段 3 只新增两份合并测试文件：

- `internal/reliability/reliability_test.go`：配置边界、确定性指数退避、Context-aware 等待、HTTP-date `Retry-After`、Breaker HalfOpen 并发上限、Queue 压力、Key 并发轮换、Secret 安全格式化，以及 API Key Adapter 的装配拒绝；
- `internal/httpapi/reliability_e2e_test.go`：401/403 换 Key 差异、429 冷却并换 Key、primary Key 耗尽后的 Fallback、网络失败重试/耗尽、取消期间停止后续候选，以及 Queue/Breaker 资源归还。

既有 `internal/httpapi/chat_test.go` 和 `internal/provider` 契约用例继续提供 Route Plan 顺序、普通 400 不 Retry、指定 5xx、Breaker Open、Queue 满、能力不匹配、上游超时/断流/超大响应，以及 16 个内置 Adapter 的协议边界证据。没有为 Handler、Service 和组件层重复建立三套相同矩阵。

## 4. 自动化检查结果

| 检查 | 命令或口径 | 结果 |
|---|---|---|
| 修改文件格式 | `gofmt -w` 后对全部 Go 文件执行 `gofmt -l` | 通过，无文件输出。 |
| 新增可靠性证据 | `go test -count=1 -timeout=60s ./internal/reliability ./internal/httpapi` | 通过。 |
| 并发/取消稳定性 | 新增并发状态和跨 Fallback 取消用例执行 `-count=20` | 两个包均通过；没有超时或偶发失败。 |
| 全量测试 | `go test -count=1 -timeout=120s ./...` | 通过；所有有测试的包成功，真实基础设施用例按既有显式环境变量规则决定是否跳过。 |
| 静态检查 | `go vet ./...` | 通过，无输出。 |
| race detector | `CGO_ENABLED=1 go test -race -count=1 -timeout=120s ./...` | 未进入业务测试：当前 Go 1.26.0 在 setup/build 阶段报告无法解析 `runtime/race`，本机 `PATH` 同时没有 gcc、clang 或 zig。不能记为通过。 |
| 密钥扫描 | 高置信度 API Key、Bearer、私钥和带密码 PostgreSQL DSN 模式 | 命中均为 `.env.example`、README 的 `replace-with-*` 示例或测试占位值；未发现真实密钥。 |

本阶段的 Provider 故障用例全部使用内存假 Adapter 或 `httptest.Server`，不调用公网和真实付费 API。阶段 3 没有新增 PostgreSQL/Redis 语义，因此不重复运行阶段 2 已完成的真实基础设施矩阵。

## 5. 代码独立性检查

自动检查使用现有临时只读扫描器 `modelmux-clone-check 1.0`，参数为连续至少 12 个非空逻辑行或至少 80 个有效 token；排除 `_test.go`、vendor、generated、UI/frontend/web、examples 和 testdata。

扫描对象是当前工作树的 53 个生产 Go 文件，共 5282 条扫描器逻辑行。参考集合分别为只读的 GoModel、Bifrost 和二者合并集。

| 参考集合 | 字面重复 | 归一化近似重复 |
|---|---:|---:|
| GoModel | 0/5282 行；0.00% | 0/5282 行；0.00% |
| Bifrost | 0/5282 行；0.00% | 29/5282 行；0.55% |
| 合并集合 | 0/5282 行；0.00% | 29/5282 行；0.55% |

归一化结果是启发式工具观测值，不把它宣称为通用相似度的绝对精确值。人工复核覆盖阶段 3 的 Router、Failure、Breaker、Queue、Key Selector、Retry、Attempt、Orchestrator 和本次装配校验；在两个参考仓库中搜索 `ProviderKeySelection`、`CandidatesTried`、`TimeoutRequestBudget`、`provider_keys_exhausted`、`stateVersion`、`RetryPolicies` 及新增错误文案均无命中，也未发现相同独特注释、错误字符串或特色控制流。保守结论是两项指标均严格小于 10%。

## 6. 未完成门禁与阶段决定

- `S3-042`、`S3-053`、`S3-064` 已有并发/压力行为证据，但因 race detector 未运行成功而保持未勾选；
- `S3-090` 的格式、测试、vet、故障矩阵、密钥、独立性和用户进入 S4 的确认已完成，race 环境项仍未完成；
- 当前不能宣称已经通过 race、生产可用或高并发无数据竞争；
- 用户已明确授权进入阶段 4；该授权不会把尚未执行的 race 检查改写为通过。

因此阶段 3 的功能开发已经收口，当前账本为 86/90。项目可以继续阶段 4 中与 race 环境缺口独立的生产功能；后续仍需修复或更换可用的 race 工具链，不用新增普通测试填充该缺口。
