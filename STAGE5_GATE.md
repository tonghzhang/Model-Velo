# Model-Velo 阶段 5 门禁记录

> 检查日期：2026-07-24  
> 当前结论：阶段 5 Usage v2 生产链与非 race 门禁已完成，真实 Redis/PostgreSQL 集成通过。Windows race detector 仍在测试执行前被本机工具链阻止，因此不能宣称 race 或阶段总门禁已经通过。

## 1. 目标、非目标和验收条件

阶段 5 当前完成的 Usage 数据链路：

1. API 在请求生命周期结束时生成一个版本化 Usage Event；
2. API 用独立短超时将事件写入环境隔离的 Redis Stream；
3. 独立 Worker 使用 consumer group 消费，并在 PostgreSQL 成功写入后 ACK；
4. 重复投递、consumer 消失和 ACK 不确定窗口依靠 pending 恢复与唯一事件 ID 幂等；
5. 未知版本和坏载荷达到阈值后进入 dead-letter Stream；
6. 明确采用 at-least-once，不宣称 exactly-once；
7. schema v2 记录详细 token、usage 来源、finish reason、TTFT 与有界 raw usage，同时继续消费 v1；
8. OpenAI-compatible 流请求默认要求末尾 Usage Chunk；
9. 版本化价格按事件时间生成 nanoUSD 成本快照，未知价格保持 NULL；
10. 提供同时受 tenant/API Key 隔离的明细、汇总和时间序列查询；
11. Worker 执行保留期清理，Admin 支持显式历史成本重算；
12. 主 Stream 不用长度 trimming 删除 pending，落库后事务执行 XACK+XDEL。

本阶段不实现 Prometheus、tracing、GitHub Actions、benchmark 或管理 UI，也未进入阶段 6。

## 2. 实际调用链

```text
POST /v1/chat/completions
  -> 认证后创建 Usage Collector
  -> 授权 / 限流 / Route / Cache / Reliability / SSE
  -> 补充 cache、route、attempt、retry、fallback 和 token usage
  -> 流式请求补充 TTFT、finish reason 与末尾 usage-only Chunk
  -> success / cache_hit / failed / cancelled /
     stream_completed / stream_interrupted 只 finalize 一次
  -> Redis XADD model-velo:usage:v1:<environment>

model-velo-usage-worker
  -> XGROUP CREATE ... MKSTREAM
  -> XAUTOCLAIM 超过 idle 的 pending
  -> XREADGROUP COUNT ... BLOCK ...
  -> 解码并校验 schema v1/v2
  -> 计算 Provider 报价或版本化 catalog 成本快照
  -> INSERT usage_events ON CONFLICT (event_id) DO NOTHING
  -> PostgreSQL 成功后 TxPipeline(XACK, XDEL)
  -> 坏事件达到投递阈值后 TxPipeline(XADD dead-letter, XACK, XDEL)
  -> 周期性分批删除超过 retention 的 PostgreSQL 行
```

API 和 Worker 共享 `internal/usage` 的稳定事件类型，但作为两个进程运行。API 不同步写 PostgreSQL；Worker 不装配 Gin、Provider、Router、Retry 或 Fallback。

## 3. 数据与故障语义

- `event_id` 在 Collector 创建时由 `crypto/rand` 生成，同一事件重投保持不变；
- request ID、tenant ID 和 API Key ID 来自认证 Context；事件不包含 Model-Velo Key Secret、Provider Secret、提示词或完整上游响应；
- token usage 缺失时 JSON 字段省略、数据库列为 `NULL`；价格未知时成本列同样为 `NULL`，不会伪造全零计费数据；
- raw usage 仅保存上游 usage 子对象且最多 64 KiB，超限或非法明细转成稳定 caveat；
- 主 Redis Stream 只保留未处理积压，不能用长度阈值裁掉 pending；dead-letter 使用独立近似长度上限；
- Emit 使用独立短超时。Redis 写入失败只记录 request/event ID，不改变客户端结果；进程在成功重投前退出可能丢失该事件；
- 数据库错误不 ACK，消息保留在 pending；数据库成功但 Redis 事务未完成时，重投命中 `event_id` 主键后再 ACK/删除；
- Worker 读取、认领和故障退避都响应 Context；收到退出信号后停止新读取，当前批次最多使用 `worker_timeout` 收尾；
- dead-letter 只记录来源 entry ID、稳定原因和有界原事件载荷，日志不打印载荷；
- 缓存命中保留逻辑 token 但成本为本次请求的已知零；Retry/Fallback 后无法取得早期失败 attempt usage 时明确保存 caveat。

## 4. 自动化与真实依赖结果

| 检查 | 命令或场景 | 结果 |
|---|---|---|
| API 生命周期 | `internal/httpapi` 合并用例 | 非流式成功、缓存命中、最终 5xx、客户端取消、流式 usage chunk 均只生成一个终态事件；Emitter 失败不改 HTTP 结果。 |
| Event 契约 | `internal/usage` 单元用例 | schema v1/v2、详细 token、usage 来源、raw 上限、TTFT、UTC 延迟和重复 finalize 通过；未知版本与坏事件被拒绝。 |
| 成本 | catalog 与真实 PostgreSQL | 版本窗口、缓存价、详细 token 费率、Provider 报价、未知价格 NULL、历史重算通过。 |
| 查询 | HTTP + 真实 PostgreSQL | 当前 tenant/API Key 强制注入、跨 Key 拒绝、严格参数、no-store、明细/汇总和跨租户隔离通过。 |
| Emit 故障 | 阻塞 Dialer + 25ms Emit timeout | 在边界内返回错误；无 Redis 命令前拒绝非法事件。 |
| 真实 Redis/PostgreSQL | `TestUsageRedisPostgresPipeline` | PostgreSQL 17.10、Redis 8.8.0 上通过。 |
| Schema | 随机 schema 中连续两次 `AutoMigrate` | v2 列、`usage_events` 表、主键、状态约束和 tenant/model/provider/key/cost 查询索引可重复创建。 |
| Redis 载荷 | 真实 `XADD` 后 `XRANGE` 解码 | entry ID 返回、schema v2 载荷可解码；落库后主 Stream 长度回到零。 |
| 幂等 | 同一事件两次 XADD | Worker 统计一次 stored、一次 duplicate，数据库仅一行。 |
| pending 恢复 | stopped consumer 读取后不 ACK | 超过 idle 后被另一 consumer 的 `XAUTOCLAIM` 认领并写库。 |
| 毒消息 | 非法 JSON，`max_deliveries=1` | 未进入正常 INSERT，转入 dead-letter 后 ACK。 |
| 保留期 | 真实 PostgreSQL，batch=1 | 过期记录按批删除，未过期记录保留。 |
| 资源清理 | 测试随机 schema/stream | 测试结束时定向删除，不清空共享数据库或 Redis DB。 |
| 全量测试 | 带真实 Redis/PostgreSQL 环境执行 `go test -count=1 -timeout=120s ./...` | 全部通过；`internal/usage`、鉴权、限流、Redis Client 等真实依赖用例实际运行。 |
| 静态检查 | `go vet ./...` | 通过，无问题输出。 |
| 格式 | `gofmt -l` 检查 `cmd`、`internal` 下全部 `.go` | 通过，无文件输出。 |

真实依赖命令：

```powershell
$env:MODEL_VELO_POSTGRES_TEST_DSN = "postgres://model_velo:<local-test-password>@localhost:5432/model_velo?sslmode=disable"
$env:MODEL_VELO_REDIS_TEST_ADDR = "localhost:6379"
$env:MODEL_VELO_REDIS_TEST_PASSWORD = "<local-test-password>"
go test -count=1 -run '^TestUsageRedisPostgresPipeline$' -v ./internal/usage
```

2026-07-24 的最终 v2 单项结果为 `PASS`，耗时约 0.64 秒；随后带相同真实依赖执行全仓测试也全部通过。测试使用无持久卷的隔离容器和测试专用密码，连接信息只在当前进程环境中使用，未写入仓库文件。

## 5. Race detector 状态

执行：

```powershell
$env:CGO_ENABLED = "1"
go test -race -count=1 ./internal/usage ./internal/httpapi
```

系统 Go `go1.26.0 windows/amd64` 在测试执行前失败：

```text
cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%
```

这不是测试用例失败，也不能记为 race 通过。阶段 4 已准备的便携 Go 和签名有效的 MinGW-w64 GCC 仍未执行；根据既有安全决定，执行新下载的第三方 GCC 需要用户单独明确授权。因此 `S5-018` 与 `S5-045` 保持未完成。

## 6. 代码独立性

本次按用户要求只读核对了 GoModel `internal/usage/usage.go`、`reader.go`、`request_summary.go`、`internal/gateway/inference_execute.go`，以及 Bifrost `core/schemas/bifrost.go`、`plugins/logging/operations.go`、`plugins/logging/utils.go`、`plugins/logging/writer.go` 中直接相关的 Usage/成本/查询边界。共同生产能力是强制流式 usage、详细 token、成本、查询、保留/重算；Model-Velo 按自己的 Gin/GORM/Redis Stream 边界独立实现，没有复制其源码、注释、测试、SQL、配置结构或错误文案。

自动检查工具为临时只读扫描器 `modelmux-clone-check 1.0`，参数为连续至少 12 个非空逻辑行或至少 80 个有效 token，排除 `_test.go`、vendor、generated、UI、frontend、web、examples 和 testdata。扫描 67 个 Model-Velo 生产 Go 文件、8848 条逻辑行：

| 参考集合 | 字面重复度 | 归一化近似重复度 |
|---|---:|---:|
| GoModel | 0/8848，工具观测值 0.00% | 74/8848，工具观测值 0.84% |
| Bifrost | 0/8848，工具观测值 0.00% | 134/8848，工具观测值 1.51% |
| 合并集合 | 0/8848，工具观测值 0.00% | 134/8848，工具观测值 1.51% |

人工复核了 stream usage、token details、unknown cost、价格重算、查询聚合、保留期、`XADD`、`XREADGROUP`、`XAUTOCLAIM` 和 dead-letter 控制流。GoModel/Bifrost 的共同概念被保留，但 Model-Velo 使用整数 nanoUSD、半开价格窗口、tenant 强制查询、版本化事件兼容、Redis pending 恢复和落库后 XACK+XDEL，包边界、字段、命名与失败语义均为独立设计。

自动归一化检测是启发式结果，不宣称为绝对精确相似度；结合人工复核，两项指标均严格小于 10%。

## 7. 当前决定

阶段 5 的生产功能和非 race 门禁已收口。下一步只能在用户明确允许执行便携 GCC 后补跑 S4/S5 race，或等待用户确认阶段 6；不会自动实现 Metrics、CI 或 benchmark。
