# 阶段 6 门禁

## 已实现

- JSON `slog` 请求摘要与可配置日志等级；
- W3C Trace Context、HTTP server/client span 与可选 OTLP HTTP exporter；
- 受保护的 Prometheus `/metrics`，覆盖 HTTP、Provider attempt、Retry、Fallback、缓存、认证、限流、额度，以及动态 Breaker/Queue/Provider Key 状态；
- `/healthz` liveness 与检查 PostgreSQL、Redis 的 `/readyz`；
- GitHub Actions：格式、模块校验、vet、真实 PostgreSQL/Redis 测试、race、构建和 benchmark smoke；
- 多阶段非 root 容器、API/Usage Worker/管理工具 Compose 编排；
- 可复现 HTTP benchmark 命令与故障门禁说明。

## 当前本机证据

- 2026-07-24：`gofmt -l .` 无输出；
- 2026-07-24：`go test ./...`、`go vet ./...`、`go build ./cmd/...`、`go mod verify` 和 `git diff --check` 通过；`git diff --check` 只有 Windows CRLF 提示，没有空白错误；
- 使用临时 PostgreSQL `17.10-alpine3.23` 与 Redis `8.8.0-alpine3.23`、Docker 自动分配本机端口运行 `go test ./...`，包含随机 schema、Redis 隔离 namespace、Usage outbox/consumer group 和 API Key 生命周期，全部通过；临时容器随后自动删除；
- Compose 配置解析通过；完整 `docker build --check .` 在拉取 `golang:1.26-bookworm` 元数据时因 Docker Hub 认证端点网络超时中止，因此不能写成镜像构建成功；
- benchmark 命令、环境和五次结果记录在 `docs/benchmark.md`：中位数 `459724 ns/op`，范围 `412685–480725 ns/op`，`33755–34157 B/op`，均为 `324 allocs/op`；它不是 QPS 或真实容量结论；
- `modelmux-clone-check 1.0` 按 `min_lines=12`、`min_tokens=80` 扫描 92 个生产 Go 文件、17242 条逻辑行：GoModel `0.00%/0.57%`、Bifrost `0.00%/1.13%`、合并集 `0.00%/1.20%`（字面/归一化近似），均小于 10%；人工复核未发现相同的独特命名、注释、错误文案或实现控制流。

GitHub Actions 文件存在不等于远端 CI 已运行；当前没有对应 workflow run 证据。

## 尚不能宣称

- Windows Go 1.26.0 环境没有 gcc/clang，且本机 Go 安装不含可用 Windows race runtime；`CGO_ENABLED=1 go test -race ./...` 在构建 `runtime/race` 时失败。当前也没有可用的本地 Linux Go 镜像，因此 race 仍未通过；
- benchmark 只测进程内单 Provider/单 Attempt Handler，不包含真实数据库、Redis、TLS、上游推理、并发矩阵或资源监控，不能宣称具体 QPS、p95/p99 或生产容量；
- OTLP endpoint 未连接 Collector 时，只能证明本地 span 装配，不能证明后端已接收。
- 管理控制面、额度、Usage outbox、协议转换和可观测性已经实现并通过本机/真实依赖测试，但尚未完成远端 CI、完整镜像构建、漏洞扫描、全故障演示脚本和干净环境最终演示，因此项目不能笼统宣称“所有生产门禁已完成”。
