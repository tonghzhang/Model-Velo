# Model-Velo 阶段 4 门禁记录

> 检查日期：2026-07-23  
> 当前结论：门禁未完成。阶段 4 生产边界、普通并发/故障测试、全量测试、静态检查、密钥扫描和独立性复查已经完成；用户已经允许进入阶段 5，Windows race detector 仍在测试执行前被本机工具链阻止。

## 1. 目标、非目标和验收条件

本阶段目标是为公开 `POST /v1/chat/completions` 增加 OpenAI-compatible SSE，并把可靠性边界固定在首个有效事件是否已经提交：

1. 首事件前的建连、5xx、协议错误和超时可以按既有策略 Retry/Fallback；
2. 首事件写给客户端后禁止切换 Provider，也不追加 JSON 错误；
3. 客户端取消、事件空闲超时和进程关闭能够中止上游并释放 Queue、Key 与 Breaker 资源；
4. 长流不被面向普通响应的 Server 总 `WriteTimeout` 截断，单帧慢写仍然有界；
5. 上游 SSE 单行、单事件、编码和终止标记攻击不会造成无界内存或 panic。

本阶段不实现 Usage Event、Redis Stream、Usage Worker、PostgreSQL Usage 表、Metrics 或 tracing。

## 2. 实际调用链和提交边界

```text
chatHandler.complete
  -> 认证 / 授权 / 限流 / Route Plan
  -> stream=true 绕过 Exact Cache
  -> Orchestrator.OpenStream
     -> 对候选执行 Breaker -> Queue -> Key -> StreamingAdapter
     -> 在 Attempt Timeout 内读取并校验首个有效事件
     -> 失败时释放资源并按策略 Retry / Fallback
  -> PreparedStream
  -> 清除当前 SSE 响应继承的 Server 总写截止时间
  -> 每帧设置 15 秒写截止时间 -> Write -> Flush -> 清除截止时间
  -> 后续事件或 heartbeat 重置 Provider attempt_timeout 静默计时器
  -> [DONE] 成功结束；读取/写入/取消/空闲超时则结束当前流
  -> 关闭上游 Body、反馈 Key/Breaker、释放 Queue
```

非流式请求仍使用 `request_timeout + 15s` 的 Server `WriteTimeout`。流式响应只有在首事件已经通过协议校验后才清除这一总时长限制；首事件前仍由请求总预算和 Provider Attempt Timeout 保护。后续每个客户端帧独立限制写入时间，避免慢客户端无限占用 Provider 资源。上游注释不会转发给客户端，但会作为连接活动重置事件空闲计时器。

## 3. 协议与攻击边界

- 单行最多 1 MiB，持续无换行输入在该边界停止；
- 单事件最多 2 MiB，多行 `data:` 不能绕过总事件上限；
- `data:` 必须是合法 UTF-8 和 JSON；
- 只有完整 data 值 `[DONE]` 才终止流，JSON 字符串中的 `[DONE]` 仍是普通内容；
- 首事件前的坏 Chunk 保持未提交状态，可以安全返回 JSON 错误或 Fallback；
- 首事件后的坏 Chunk 只终止当前流，不调用下一 Provider；
- 每次只同步持有和写出一个事件，不创建无界 Chunk channel。

## 4. 自动化检查结果

| 检查 | 命令或口径 | 结果 |
|---|---|---|
| Go 格式 | `gofmt -l` 检查 `cmd`、`internal` 下全部 `.go` | 通过，无文件输出。 |
| 全量测试 | `go test -count=1 -timeout=120s ./...` | 通过；真实 PostgreSQL/Redis 用例仍按显式环境变量决定运行或跳过。 |
| 静态检查 | `go vet ./...` | 通过，无问题输出。 |
| SSE 稳定性 | SSE Handler、Parser、PreparedStream、Fallback 和 Server Shutdown 相关用例执行 `-count=10` | 四个相关包全部通过。 |
| 长流写超时 | 真实 HTTP Server 使用 `WriteTimeout=40ms`，第二事件延迟 120ms | 客户端仍依次收到两个事件和 `[DONE]`。 |
| 并发与资源 | 8 路并发真实 SSE 请求 | 全部成功，最终上游 active 与 Queue active/waiting 均回到零。 |
| 空闲与心跳 | 无后续事件、持续 heartbeat 两种场景 | 前者按 upstream timeout 取消并释放资源；后者在总时长超过单次静默上限时仍继续到 `[DONE]`。 |
| 攻击矩阵 | 超长行、持续无换行、跨行超大事件、坏 UTF-8/JSON、伪 `[DONE]` | 均返回稳定边界错误，无 panic。 |
| 密钥扫描 | 高置信度 Key、长 Bearer、私钥和带密码 PostgreSQL DSN 模式 | 命中项均为 `.env.example`、README 或测试中的明确无效占位值，未发现真实密钥。 |
| diff 检查 | `git diff --check` | 通过；Git 仅报告现有 Windows 行尾转换提示。 |

## 5. Race detector 状态

执行：

```powershell
$env:CGO_ENABLED = "1"
go test -race -count=1 -timeout=90s ./internal/provider ./internal/reliability ./internal/httpapi ./cmd/model-velo
```

系统 Go `go1.26.0 windows/amd64` 在测试执行前失败：

```text
cannot find package runtime/race
```

这不是用例失败，也不能记为 race 通过。便携 Go 1.26.5 的完整运行库已经位于临时目录。根据 Go 官方要求，Windows race 还需要包含新版 MinGW-w64 runtime 的 C 编译器。已从 w64devkit v2.8.0 官方 GitHub Release 下载 x64 归档：

- SHA-256：`6252BF34FE2231A55AC7F03D482B36D2C7C58697990551BBA508102CFB3F342E`；
- Authenticode：`Valid`；
- 签名者：`Christopher Wellons`；
- 仅使用 Windows 自带 `tar.exe` 解压到 `D:\tmp`，没有修改系统 PATH。

安全策略要求用户单独明确授权后才能执行新下载的第三方 GCC。因此 `S4-034` 和阶段 4 总门禁继续保持未完成。

## 6. 代码独立性检查

自动检查工具为临时只读扫描器 `modelmux-clone-check 1.0`，参数为连续至少 12 个非空逻辑行或至少 80 个有效 token，排除 `_test.go`、vendor、generated、UI、frontend、web、examples 和 testdata。

扫描 55 个 Model-Velo 生产 Go 文件、6078 条逻辑行：

| 参考集合 | 字面重复度 | 归一化近似重复度 |
|---|---:|---:|
| GoModel | 0/6078，工具观测值 0.00% | 0/6078，工具观测值 0.00% |
| Bifrost | 0/6078，工具观测值 0.00% | 29/6078，工具观测值 0.48% |
| 合并集合 | 0/6078，工具观测值 0.00% | 29/6078，工具观测值 0.48% |

人工复核搜索了本阶段新增的超时函数、错误文案、攻击测试命名和 SSE 结束文案。GoModel 的 `internal/server/http.go` 和 `internal/admin/handler_live.go` 也使用标准库 `ResponseController` 清除长连接写截止时间；Model-Velo 的独立取舍是在首事件验证后才清除当前响应的总截止时间，并为每帧重新设置有界写截止时间，同时由 reliability 层按 Provider Attempt Timeout 管理事件空闲。Bifrost 未发现相同命名或控制流。没有复制参考项目源码、注释或测试。

自动归一化检测是启发式结果，不宣称为绝对精确相似度；结合人工复核，保守结论是两项指标均严格小于 10%。

## 7. 当前决定

阶段 4 的生产功能和非 race 门禁已经收口，账本为 33/35；用户已明确允许继续阶段 5。当前仍不能宣称 race 已通过，也不能把阶段 4 标记为完成。获得用户对便携 GCC 执行的明确授权后，应补跑阶段 4 race 命令并同步最终门禁。
