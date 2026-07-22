# Model-Velo 阶段 1 门禁记录

> 名称说明：本记录产生于预发布改名前；为保持当前仓库可读性，显示名称和源码路径已更新，测试结论与扫描数字仍是当时的历史证据。

> 检查日期：2026-07-16  
> 当前结论：门禁未完成。功能、测试、静态检查和独立性检查已满足；race detector 被本机缺少 GCC 阻止。用户已于 2026-07-16 明确授权开始阶段 2，但该授权不会把尚未运行成功的 race 检查改写为通过。

## 1. 目标、非目标和验收条件

本阶段目标是证明一条最小非流式网关链路能够正确往返：Gin 接收入站请求，校验最小 Chat 协议，把请求发送给一个 OpenAI-compatible 上游，再返回完整成功 JSON 或稳定结构化错误。

本阶段不实现 Redis、PostgreSQL、鉴权、限流、缓存、Retry、Fallback、多 Provider、Circuit Breaker、Provider Queue、SSE、Usage Worker、管理 UI 或插件系统。

门禁要求：

1. `/healthz` 和非流式 `/v1/chat/completions` 正常链路有黑盒测试；
2. 本地校验、上游 HTTP 异常、非法响应、超大响应、断流、超时、网络失败和取消有测试；
3. `gofmt`、`go test ./...`、`go vet ./...` 通过；
4. 并发和取消相关代码通过 race detector，或如实记录阻塞原因且不得假称通过；
5. 生产 Go 代码对 GoModel、Bifrost 及合并参考集合的字面和归一化近似重复度严格小于 10%；
6. 文档只描述真实能力，用户明确确认后才能进入阶段 2。

## 2. 实际调用链

```text
main
  -> 加载监听地址、上游地址、上游 Key、调用超时、关闭超时
  -> 创建 provider.Adapter（OpenAI 实现）
  -> 创建 Gin Router 和 http.Server
  -> request ID middleware
  -> chatHandler.complete
  -> provider.Adapter.Complete
  -> 假上游或配置的 OpenAI-compatible 上游
  -> 原样成功 JSON / writeAPIError
```

HTTP Handler 只处理传输协议和错误写出；当时的单 OpenAI Adapter 只构造和执行单次上游请求。阶段 1 没有提前创建 Retry、Fallback 或通用 Provider 接口。

## 3. 自动化检查结果

| 检查 | 命令或口径 | 结果 |
|---|---|---|
| Go 格式 | 对 `cmd`、`internal` 下全部 `.go` 执行 `gofmt -l` | 通过，无文件输出。 |
| 全量测试 | `go test -count=1 -timeout=45s ./...` | 通过：`cmd/model-velo`、`internal/httpapi`、`internal/provider` 全部成功。 |
| 静态检查 | `go vet ./...` | 通过，无问题输出。 |
| 重复稳定性 | 取消传播、优雅关闭、并发 request ID 测试执行 `-count=20` | 三个包全部通过，没有超时或偶发失败。 |
| 正常链路演示 | `TestChatCompletionSuccess` | 通过，覆盖客户端到假上游再返回客户端。 |
| 异常链路演示 | HTTP 错误矩阵、上游断流、客户端取消测试 | 全部通过。 |
| race detector | `go test -race -count=1 -timeout=60s ./...` | 未通过环境门禁：默认 `CGO_ENABLED=0`；临时启用后确认 `gcc` 不在 `PATH`。这不是代码测试失败，也不能记为通过。 |
| 密钥扫描 | 高置信度 Key、长 Bearer Token、私钥、带密码 PostgreSQL DSN；再复核 credential 字段命中 | 未发现真实密钥。泛化命中是环境变量名、规则文字和 `provider-test-key` 等测试占位值。 |

所有上游测试均使用 `httptest.Server`，没有访问公网或真实付费 API。

密钥基线使用的高置信度扫描命令为：

```powershell
rg -l --hidden -g '!.git/**' -g '!go.sum' 'sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|-----BEGIN ([A-Z ]+ )?PRIVATE KEY-----|postgres(?:ql)?://[^\s:]+:[^\s@]+@|Bearer [A-Za-z0-9._-]{24,}' .
```

随后使用 `(api[_-]?key|authorization|token|password|secret)` 做宽泛文件级复核；命中文件均已人工确认只包含变量名、说明文字或无效测试占位值。

## 4. 代码独立性检查

扫描对象是 Model-Velo 的 6 个生产 Go 文件，共 351 个扫描器逻辑行；排除 `_test.go`、`vendor`、generated、UI/frontend/web、examples 和 testdata。参考集合分别为：

- `D:\agent开源项目\GoModel-main\GoModel-main`；
- `D:\agent开源项目\bifrost-dev\bifrost-dev`；
- 两者合并后的引用集合，Model-Velo 命中行只计一次。

自动检查工具为临时本地只读扫描器 `model-velo-clone-check 1.0`。参数为连续至少 12 个非空逻辑行或至少 80 个有效 token；字面模式保留标识符，近似模式在每个候选窗口内按首次出现顺序归一化 Go 标识符，同时保留关键字、运算符和字面量。

执行命令为：

```powershell
go run <clone-check-script> -model <repository-root> -gomodel D:\agent开源项目\GoModel-main\GoModel-main -bifrost D:\agent开源项目\bifrost-dev\bifrost-dev
```

| 参考集合 | 字面命中 | 归一化近似命中 |
|---|---:|---:|
| GoModel | 0/351 行；工具观测值 0.00% | 0/351 行；工具观测值 0.00% |
| Bifrost | 0/351 行；工具观测值 0.00% | 0/351 行；工具观测值 0.00% |
| 合并集合 | 0/351 行；工具观测值 0.00% | 0/351 行；工具观测值 0.00% |

归一化检测是启发式工具结果，不把 `0.00%` 宣称为通用代码相似度的绝对精确值；结合逐文件人工复核，保守结论区间为 `0% <= 重复度 < 10%`，满足本项目门禁阈值。

人工复核覆盖 `cmd/model-velo/main.go`、`internal/httpapi` 的四个生产文件和当前的 `internal/provider/openai.go`（阶段 1 时文件名为 `client.go`）。没有发现相同的特色控制流、独特命名、注释或错误字符串；对 `requestIDMiddleware`、`normalizeServeError`、`chatCompletionsEndpoint`、`upstream_response_too_large`、`request_id_generation_failed`、`MODEL_VELO_UPSTREAM_BASE_URL`、`model-velo.request_id`、`stream_not_supported` 的参考仓库搜索也无命中。本次没有打开参考项目的具体实现函数进行改写。

## 5. 未覆盖边界和门禁决定

- 未用真实 Provider 验证供应商私有差异；阶段 1 只保证 OpenAI-compatible 假上游链路。
- 未支持 `stream=true`、工具消息、多模态内容或模型映射。
- 缺少 GCC，race detector 尚未成功运行；安装可用 C 编译器并设置 `CGO_ENABLED=1` 后需要重新执行。
- 当前尚未初始化 Git，不能形成 `S1-050` 要求的可解释提交。
- 用户已经明确授权进入阶段 2；阶段 1 的 race 环境缺口作为已知技术债继续保留。

因此 `S1-049` 保持未完成；阶段 2 已经开始，但后续里程碑仍需补做 race 检查。
