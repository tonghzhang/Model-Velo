# 可复现 benchmark 与故障门禁

## HTTP 基准

基准使用进程内 Gin 网关与本地 `httptest` 上游，不访问公网，不包含 PostgreSQL/Redis 延迟。

```bash
go test ./internal/httpapi \
  -run '^$' \
  -bench '^BenchmarkChatCompletions$' \
  -benchmem \
  -benchtime=10s \
  -count=5
```

报告结果时必须同时记录：

- commit SHA；
- `go version` 与 `go env GOOS GOARCH`；
- CPU、逻辑核心数与内存；
- 上述完整命令、请求体、单 Provider/单 Attempt 条件；
- `ns/op`、`B/op`、`allocs/op`，以及五次结果的离散程度。

这个 benchmark 只用于检测网关本身回归，不能换算成真实 Provider QPS。端到端容量测试必须固定 PostgreSQL、Redis、上游延迟分布、并发、持续时间、连接复用和错误比例。

### 2026-07-24 本机基线

- 源码：base commit `e3c2a64` 加当前未提交工作树；
- 环境：Windows/amd64，Go 1.26.0，Intel Core i7-13700HX，24 个逻辑执行线程；系统权限不允许读取总内存，因此未填猜测值；
- 载荷：单 Provider、单 Attempt、进程内 Gin、复用本地 `httptest` HTTP 上游；请求为一条 `hello` 用户消息，固定返回 9 token；
- 命令：与上文完全一致，`10s × 5`；
- 五次 `ns/op`：412685、432075、480725、459724、462468；中位数 459724，范围 412685–480725；
- 分配：33755–34157 B/op，均为 324 allocs/op。

该结果包含本机回环 HTTP、认证/限流/Usage 的测试替身和完整网关 Handler，但不包含真实 PostgreSQL、Redis、TLS、网络或模型推理延迟，也不是并发容量或 QPS 声明。

## 故障与并发

CI 使用 PostgreSQL 17、Redis 8 并运行：

```bash
go test ./...
go test -race ./...
```

已有合并故障用例覆盖网络错误、指定 5xx、429/`Retry-After`、错误 Key、Breaker、Queue 满/超时、客户端取消、首 SSE 事件前 Retry/Fallback、提交后断流以及 Usage consumer reclaim/dead-letter。故障报告不得把普通测试通过等同于 race 通过。
