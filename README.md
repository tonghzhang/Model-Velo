# Model-Velo

Model-Velo 是一个用 Go 和 Gin 编写的多协议 LLM 网关。当前已实现 Chat Completions、Responses、Embeddings、Anthropic Messages 入站协议，多 Provider 原生转换与可靠性运行时，持久化 Usage/计费、在线控制平面、额度预算，以及阶段 6 的可观测性和工程门禁。

## 当前已经实现

- `GET /healthz` 健康检查；
- `GET /readyz` PostgreSQL/Redis 就绪检查，以及可选 Bearer 保护的 `GET /metrics`；
- `POST /v1/chat/completions` 非流式请求校验和转发；
- `POST /v1/responses`、`POST /v1/embeddings`、`POST /v1/messages` 和 `GET /v1/models`；
- 16 个内置厂商 Adapter：厂商身份与构造入口彼此独立；公开采用 OpenAI Chat 报文的厂商只复用协议编解码和 HTTP 边界；
- 一个厂商可配置多个 Provider 实例，每个实例可声明多个文本或视觉模型；
- request ID 生成、校验、响应回传和上游传播；
- 请求体与上游响应体大小限制；
- 上游超时、网络失败、HTTP 错误和非法响应的结构化错误；
- 操作系统退出信号和有界优雅关闭；
- 使用 `httptest.Server` 的本地测试，不调用真实付费 API；
- 固定版本的 PostgreSQL、Redis Compose 配置；
- 基于 GORM 的 PostgreSQL 连接、启动 Ping、连接池配置和退出关闭；
- 启动时通过 GORM `AutoMigrate` 同步租户/API Key、Usage/outbox、控制面/审计和额度账本；
- Model-Velo API Key 随机生成、摘要查找、HMAC 校验、过期判断、禁用和吊销；
- `model-velo-admin` 本地管理命令，可初始化租户、模型授权和首个 API Key；
- Gin Bearer 认证中间件、请求身份 Context 和租户模型授权检查；
- 官方 `go-redis/v9` Client、显式连接池、启动 Ping、可选启动降级和退出关闭；
- 基于 Redis Lua 的租户+模型固定窗口限流，以及可配置的 fail-open/fail-closed 运行时故障策略；
- 租户隔离的 Redis Exact Response Cache，支持规范化请求哈希、TTL、显式绕过和缓存故障降级；
- 精确模型/默认模型路由、有序候选去重、primary 选择和上游模型映射；
- 按模型声明 `text`、`image`、`audio`、`file`、`tools`、`structured` 能力，规划时先过滤协议或模型无法承载的候选；
- Provider Circuit Breaker 三态、指定故障计数、Open 快速拒绝和 HalfOpen 有界探测；
- 按 Provider 隔离的进程内有界 Queue，限制运行数和等待数，并传播请求取消；
- Provider 多 Key 安全身份与并发轮换：401 永久禁用错误 Key，403 只在当前请求内换 Key，429 按 `Retry-After` 临时冷却；
- 按 Provider ID 隔离的 Adapter、Circuit Breaker、Queue 和 Key Registry；
- 单候选 Attempt Executor：每个候选独立执行 Breaker、Queue、Key、有限 Retry 和上游调用；
- API Key Adapter 在装配时必须具备 Key Registry，错误装配会在启动边界失败而不是在请求期空指针崩溃；
- 有序 Fallback Orchestrator：成功立即停止，普通 400/取消停止，模型不可用等策略允许的失败进入下一候选；Fallback 成功响应不写 Exact Cache；
- Provider 执行总预算、单次调用超时和 Context-aware 退避取消；
- 每个 Provider 可独立覆盖 Breaker、Queue、Retry、Attempt Timeout 与 HTTP 连接池；
- Chat 文本、图片、音频、文件、Function Tool 与结构化输出：兼容协议保留原报文，原生协议只转换能够明确表达的字段并归一化响应；
- 流式预提交可靠性链：每次按 Breaker→Queue→Key→Adapter 建流并验证首事件，候选内支持有限 Retry，耗尽后按 Route Plan 有序 Fallback；最终 PreparedStream 持有成功流资源和完整安全 Trail，直到显式结束。
- OpenAI-compatible 客户端 SSE：有效首事件后才提交 Header，逐事件同步 Write/Flush，正常转发 `[DONE]`，客户端断开会取消上游并释放 Queue。
- Usage Event schema v2：记录 request、tenant、API Key ID、请求与实际模型、缓存、可靠性计数、详细 token、usage 来源、finish reason、TTFT、稳定终态和 UTC 延迟；原始 usage 子对象最多保留 64 KiB，不记录 Key Secret、提示词或完整上游响应；
- OpenAI-compatible 流请求默认合并 `stream_options.include_usage=true`；Provider 返回的缓存读写、音频、图像、推理和预测 token 会进入统一明细；
- 版本化价目表按 Provider、模型和事件时间生成不可变成本快照；Provider 明确上报的 USD 成本优先，缓存命中成本为已知零，无法定价时成本保持 `NULL` 而不是伪造零；
- API 在 Provider 前同步写 PostgreSQL pending 生命周期，终态先固化到 outbox，再以有界超时执行 Redis `XADD`；即时投递失败不会改写已经生成的模型响应；
- 独立 `model-velo-usage-worker` 使用 consumer group、`XREADGROUP`、`XAUTOCLAIM`、dead-letter 和 Context-aware 退避；
- PostgreSQL `usage_events.event_id` 主键与 `ON CONFLICT DO NOTHING` 提供幂等最终防线，数据库成功后才在 Redis 事务中执行 `XACK + XDEL`；
- 认证后的租户可查询 Usage 明细、汇总和时间序列；Worker 自动执行分批保留期清理，管理命令支持历史成本重算。
- 独立管理员身份、owner/operator/billing/auditor RBAC、脱敏审计日志，以及 Provider/路由/Provider Key/价格、租户、业务 API Key 和额度策略的在线管理；
- PostgreSQL 强一致额度账本：分钟/小时/日/月请求、Token、USD 预算，支持 deny/allow/alert 超额策略、请求前预留、真实 Usage 结算和中断恢复；
- JSON 结构化日志、Prometheus、OpenTelemetry、非 root 容器、GitHub Actions race/集成门禁与可复现 benchmark。

Usage 链路采用 **PostgreSQL outbox + Redis Stream at-least-once + 数据库幂等**，不宣称 exactly-once。API 在调用 Provider 前先写 pending 生命周期，结束时把完整 Event 固化为 ready；Redis 不可用不会丢失已固化事件，Worker 会从 outbox 重投。已标记 published 但尚未被 Usage 入库事务删除的记录也会周期性重发，覆盖 Redis 消息在消费前丢失或清理的窗口。数据库成功但 Redis ACK 响应丢失时，重投会命中 `usage_events.event_id` 唯一键。Worker 消失前未 ACK 的事件由 `XAUTOCLAIM` 恢复，坏版本/坏载荷达到阈值后进入有长度上限的 dead-letter Stream。进程在最终 Usage 形成前退出时，超时 pending 会转成明确的中断事件并保留“Usage 未知” caveat，而不是伪造 Token。

客户端始终收到 OpenAI SSE。Adapter 会按上游协议校验并转换 OpenAI-compatible SSE、Anthropic/Gemini/DashScope/Cohere SSE、Ollama NDJSON 或 Bedrock AWS EventStream；单行最大 1 MiB、单事件最大 2 MiB，Bedrock 二进制帧也在解码前限长。首事件前的 5xx、错误媒体类型、超时、EOF、坏 Chunk 和取消会沿用可靠性分类，可 Retry/Fallback，并在下一次尝试前释放资源；预提交总预算不会成为成功长流的上游 deadline。首事件提交后禁止切换 Provider，后续失败只安全记录并结束当前流。上游 heartbeat 当前不向客户端透传，但会重置事件空闲计时器；转换链使用同步背压，不创建无界 Chunk 队列。首事件验证成功后会清除当前 SSE 响应继承的 Server 总写截止时间，每个客户端帧仍有独立 15 秒 Write/Flush 截止时间，后续事件静默上限复用该 Provider 的 `attempt_timeout`。

阶段 3 的生产功能、合并故障矩阵、全量测试、vet 和独立性复查已经完成；Breaker、Queue、Key 并发用例也已通过普通执行，但 race detector 仍被本机 Go/race 工具链阻止，因此不能宣称 race 已通过。详细证据见 `STAGE3_GATE.md`。

阶段 5 的 Usage v2 生产链和真实 Redis/PostgreSQL 集成门禁已经通过；当前 PATH 没有 GCC，`go test -race` 在 `runtime/cgo` 编译前失败，因此阶段 5 race 与最终门禁仍保留，不能宣称 race 已通过。详细证据见 `STAGE5_GATE.md`。

当前请求会按 Route Plan 顺序执行候选；每个候选内部在策略允许时进行有限 Retry，耗尽后只有具备 Fallback 信号的失败才进入下一候选。合法请求所需的能力不被当前 Provider 支持时会直接尝试下一候选，不消耗 Retry，也不计入 Breaker；所有候选都不支持时返回 `400 unsupported_provider_capability`。`POST /v1/chat/completions` 必须携带有效的 Model-Velo API Key，请求模型必须存在于该租户的模型授权表，并依次通过租户限流和 Route Plan；缓存未命中后，每次 Provider 调用都会重新取得目标 Provider 的 Breaker Permit、Queue 槽位和可用 Provider Key。`GET /healthz` 保持公开。

## 当前 Chat 契约

请求必须使用 `Content-Type: application/json`，当前对以下字段做出保证：

| 字段 | 当前行为 |
|---|---|
| `model` | 必填且不能是空白字符串；用于授权和 Route Plan，可通过 `upstream_model` 映射成厂商模型名。 |
| `messages` | 必填且至少包含一条消息。 |
| `messages[].role` | 接受 `system`、`developer`、`user`、`assistant`、`tool`；`tool`、assistant `tool_calls` 和 Tool 定义会要求目标模型声明 `tools`。 |
| `messages[].content` | 接受非空字符串或非空内容块数组；已建模 `text`/`input_text`、`image_url`、`input_audio` 和 `file`。未知块不会穿过原生转换器。 |
| `messages[].tool_calls` / `tool_call_id` | 校验 Function 名称、唯一调用 ID、JSON object 参数和历史引用；原生 Adapter 映射 Tool Use/Result，响应统一返回 OpenAI `tool_calls`。 |
| `tools` / `tool_choice` / `parallel_tool_calls` | 支持 Function Tool；厂商没有等价控制项时明确返回能力错误，不会删除字段继续请求。 |
| `response_format` | 支持 `text`、`json_object`、`json_schema`；需要模型声明 `structured`。DashScope 原生接口只接受 `json_object`，Cohere 不允许与 `tools` 组合。 |
| 生成参数 | 建模并校验 token 上限、`temperature`、`top_p`、`stop`、`seed`、penalty、`n`、logprobs 与 `reasoning_effort`；原生协议仅映射等价字段，其余明确拒绝。 |
| `stream` | 省略或为 `false` 时返回完整 JSON；`true` 时绕过 Exact Cache，并以 `text/event-stream` 逐事件返回兼容 Chunk 与 `[DONE]`。 |

OpenAI、Mistral、DeepSeek、xAI、Zhipu、Groq、NVIDIA、Together 和 Cloudflare 均由各自的厂商装配入口设置端点与能力边界；它们公开采用 OpenAI Chat 报文，因此复用同一套 wire codec 和 HTTP 安全边界，只在路由配置要求时改写 `model`。Anthropic、Gemini、DashScope、Cohere、Ollama 和 Bedrock 使用各自原生消息、Tool、结构化输出、Usage 和流式事件格式。

请求 JSON 只解析一次。兼容协议会保留未知顶层字段和消息字段；原生协议在转换前检查这些字段，无法无损表达时返回能力不匹配并尝试下一候选，不会静默丢字段。

各协议在当前 Adapter 中可表达的上限如下；模型还必须在 `model_capabilities` 中显式声明对应能力：

| 协议 | image | audio | file | tools | structured | stream |
|---|---:|---:|---:|---:|---:|---:|
| OpenAI / Azure / custom OpenAI-compatible | 是 | 是 | 是 | 是 | 是 | 是 |
| Anthropic Messages | 是 | 否 | 是 | 是 | 是 | 是 |
| Gemini generateContent | 是 | 是 | 是（内嵌数据） | 是 | 是 | 是 |
| DashScope Generation | 否 | 否 | 否 | 是 | 仅 `json_object` | 是 |
| Cohere v2 Chat | 是 | 否 | 否 | 是 | 是 | 是 |
| Ollama Chat | 是 | 否 | 否 | 是 | 是 | 是 |
| Bedrock Converse | 是 | 否 | 是（内嵌数据） | 是 | 是 | 是 |
| Cloudflare Workers AI Chat | 是 | 是 | 是 | 是 | 是 | 是 |

这张表描述线协议转换能力，不承诺每个厂商模型都具备该能力。Anthropic 与 Cohere 可把远程图片 URL 交给上游；Gemini、Ollama 和 Bedrock 的当前转换器要求图片为 Base64 data URL。显式 `detail=low/high` 只在能保留该语义的 Cohere/OpenAI wire 上发送，其他原生协议会拒绝而不是忽略。Gemini、Bedrock 的 OpenAI `file_id` 没有安全等价物，当前只接受内嵌 `file_data`；Anthropic 同时接受 Files API `file_id` 和内嵌文件，使用 `file_id` 时 Adapter 会自动发送 Files API Beta Header。视频和厂商私有内容块不在 Chat 契约内。

成功时，兼容协议响应在通过 2xx、Content-Type、大小、错误信封和非空 Chat `choices[].message` 检查后原样返回；原生协议响应会转换为非流式 OpenAI Chat Completion。原生响应缺少 Usage 时省略 `usage`，不会伪造全零计费数据；出现当前无法表示的非文本输出时明确失败并按策略 Fallback。

## Usage 查询与成本

以下接口位于认证后的 `/v1` 路由组，并同时强制 tenant ID 与当前 API Key ID，普通模型 Key 不能读取同租户其他 Key 的账单或审计数据：

- `GET /v1/usage/events`：游标分页明细，支持 `start`、`end`、`model`、`provider`、`api_key_id`、`request_id`、`status`、`cache_status`、`stream`、`limit` 和 `include_raw`；
- `GET /v1/usage/summary`：请求、成功/失败、缓存、token、已知/未知成本、延迟、TTFT 和重试/Fallback 汇总，`group_by` 支持 `model`、`provider`、`status`、`cache`、`api_key`；
- `GET /v1/usage/series`：按 `hour`、`day`、`week`、`month` 或 `year` 返回时间序列，并接受 IANA timezone。

`api_key_id` 省略时自动使用当前 Key，显式值也只能等于当前 Key。默认查询最近 30 天，单次范围最多 366 天，明细每页最多 200 条，分组最多返回 1000 组并显式标记截断。所有响应带 `Cache-Control: no-store`。成本以整数 nanoUSD 存储与聚合，接口同时返回精确十进制 USD 字符串；未知价格、缺失 usage 或无法覆盖早期失败 attempt 时会保留 caveat。

历史 schema v1 没有 API Key ID，Worker 仍能可靠消费和存储，但它不能被安全归属到某一把 Key，因此不会出现在普通 Key 的 HTTP 查询中；可通过 PostgreSQL 管理通道或带 tenant 条件的 `reprice-usage` 处理。系统不会为了补齐归属而猜测或把 v1 数据暴露给整个租户。

## 管理 API

`/admin/v1` 只接受独立的 `mv_admin_...` Bearer Key，所有响应都带 `Cache-Control: no-store`。运行时、价格、管理员、租户/API Key 与额度变更和审计写入处于同一数据库事务；创建返回的管理员 Key 或业务 API Key 明文只出现一次。

- `GET/PUT /admin/v1/runtime`：版本化 Provider、路由、上游 Key 与可靠性参数；写入必须携带 `If-Match`；
- `GET/PUT /admin/v1/pricing`：版本化价目；
- `GET /admin/v1/principals`、`POST /admin/v1/principals`、`PATCH /admin/v1/principals/:id`：管理员与角色；
- `GET/POST/PUT /admin/v1/quotas`、`GET /admin/v1/quota-windows`：额度策略和当前已结算/预留窗口；
- `GET/POST /admin/v1/tenants`、`PUT /admin/v1/tenants/:id`、`GET/POST /admin/v1/tenants/:id/keys`、`PATCH /admin/v1/api-keys/:id`：租户、模型授权及业务 Key 生命周期；
- `GET /admin/v1/usage/events`、`GET /admin/v1/usage/summary`、`GET /admin/v1/usage/series`：需要 `usage:read` 的跨租户只读 Usage 查询，可按 `tenant_id` 与 `api_key_id` 下钻，汇总额外支持 `group_by=tenant`；明细不开放原始 Usage JSON；
- `GET /admin/v1/audit`：只读游标分页审计。

## 配置

| 环境变量 | 必填 | 默认值 | 用途 |
|---|---:|---|---|
| `MODEL_VELO_HTTP_ADDR` | 否 | `:8080` | Model-Velo HTTP 监听地址。 |
| `MODEL_VELO_ENVIRONMENT` | API 必填 | 无 | 1–32 位小写环境标识，用于隔离 Redis Key，例如 `development`、`staging`。 |
| `MODEL_VELO_PROVIDER_KEYS_JSON` | 条件必填 | 无 | 按需要鉴权的 Provider ID 配置一个或多个 Key；无鉴权的 Ollama 不配置 Key 集合。Secret 只用于上游鉴权，不进入快照、错误或日志。 |
| `MODEL_VELO_SHUTDOWN_TIMEOUT` | 否 | `10s` | 收到退出信号后等待活动请求结束的期限。 |
| `MODEL_VELO_POSTGRES_DB` | 否 | `model_velo` | Compose 创建的本地数据库名。 |
| `MODEL_VELO_POSTGRES_USER` | 否 | `model_velo` | Compose 创建的本地数据库用户。 |
| `MODEL_VELO_POSTGRES_PASSWORD` | Compose 必填 | 无 | 本地 PostgreSQL 密码，必须在 `.env` 中替换示例值。 |
| `MODEL_VELO_POSTGRES_PORT` | 否 | `5432` | PostgreSQL 映射到本机回环地址的端口。 |
| `MODEL_VELO_POSTGRES_DSN` | API 必填 | 无 | PostgreSQL URL；必须包含 `postgres/postgresql` scheme、用户、Host 和数据库名。 |
| `MODEL_VELO_POSTGRES_MAX_OPEN_CONNS` | 否 | `10` | `database/sql` 连接池最大打开连接数。 |
| `MODEL_VELO_POSTGRES_MAX_IDLE_CONNS` | 否 | `2` | `database/sql` 连接池最大空闲连接数，不能超过最大打开连接数。 |
| `MODEL_VELO_POSTGRES_CONNECT_TIMEOUT` | 否 | `5s` | PostgreSQL 启动连接检查期限。 |
| `MODEL_VELO_POSTGRES_MAX_CONN_LIFETIME` | 否 | `30m` | 单个 PostgreSQL 连接的最长寿命。 |
| `MODEL_VELO_POSTGRES_MAX_CONN_IDLE_TIME` | 否 | `5m` | PostgreSQL 连接的最大空闲时间。 |
| `MODEL_VELO_API_KEY_PEPPER` | API 与管理命令必填 | 无 | 至少 32 字节的服务端秘密，用于 HMAC 校验 Model-Velo API Key；更换后已有 Key 将全部失效。 |
| `MODEL_VELO_ADMIN_KEY_PEPPER` | API 与管理员初始化必填 | 无 | 与业务 Key 分离的至少 32 字节秘密。 |
| `MODEL_VELO_CONTROL_MASTER_KEY` | API 必填 | 无 | Base64 编码的 32 字节 AES-256-GCM 密钥，用于加密托管 Provider Key。 |
| `MODEL_VELO_CONTROL_REFRESH_INTERVAL` | 否 | `5s` | 多 API 实例同步活动运行时与价格版本的间隔。 |
| `MODEL_VELO_REDIS_ADDR` | API 必填 | 无 | Redis `host:port` 地址。 |
| `MODEL_VELO_REDIS_PASSWORD` | API 与 Compose 必填 | 无 | Redis 密码，错误信息和日志不得包含该值。 |
| `MODEL_VELO_REDIS_DB` | 否 | `0` | Go 应用使用的非负 Redis logical DB。 |
| `MODEL_VELO_REDIS_PORT` | 否 | `6379` | Redis 映射到本机回环地址的端口。 |
| `MODEL_VELO_REDIS_DIAL_TIMEOUT` | 否 | `5s` | Redis 建连期限。 |
| `MODEL_VELO_REDIS_READ_TIMEOUT` | 否 | `2s` | Redis 读取期限。 |
| `MODEL_VELO_REDIS_WRITE_TIMEOUT` | 否 | `2s` | Redis 写入期限。 |
| `MODEL_VELO_REDIS_POOL_SIZE` | 否 | `20` | Redis 连接池最大连接数。 |
| `MODEL_VELO_REDIS_MIN_IDLE_CONNS` | 否 | `2` | Redis 连接池预留的最小空闲连接数，不能超过池容量。 |
| `MODEL_VELO_REDIS_POOL_TIMEOUT` | 否 | `2s` | 连接池耗尽时等待可用连接的期限。 |
| `MODEL_VELO_REDIS_STARTUP_POLICY` | 否 | `required` | `required` 表示启动 Ping 失败终止；`optional` 表示记录警告后继续。它与限流运行时故障策略相互独立。 |
| `MODEL_VELO_RATE_LIMIT_REQUESTS` | 否 | `60` | 每个租户+模型在一个窗口内可接受的请求数，范围 1–1,000,000。 |
| `MODEL_VELO_RATE_LIMIT_WINDOW` | 否 | `1m` | 固定窗口时长，范围 `1s`–`24h`；窗口从该 Key 的首个请求开始。 |
| `MODEL_VELO_RATE_LIMIT_FAILURE_POLICY` | 否 | `fail-closed` | Redis 运行时失败时，`fail-closed` 返回 503；`fail-open` 标记绕过并继续 Provider。 |
| `MODEL_VELO_CACHE_TTL` | 否 | `5m` | Exact Cache 保存时间，范围 `1s`–`24h`；`0` 或 `off` 禁用缓存。 |
| `MODEL_VELO_CACHE_ROUTE_VERSION` | 否 | `routes-v1` | 环境变量启动路由的缓存命名空间；托管运行时切换会自动使用版本化命名空间。 |
| `MODEL_VELO_USAGE_EMIT_TIMEOUT` | 否 | `200ms` | API 在请求结束后投递 Usage Event 的独立短超时。 |
| `MODEL_VELO_USAGE_GROUP` | 否 | `model-velo-usage-workers` | Usage Worker consumer group。 |
| `MODEL_VELO_USAGE_CONSUMER` | 否 | `<hostname>-<pid>` | 当前 Worker consumer 名；同组并发进程应不同。 |
| `MODEL_VELO_USAGE_BATCH_SIZE` | 否 | `50` | 每次读取或认领的最大消息数。 |
| `MODEL_VELO_USAGE_READ_BLOCK` | 否 | `2s` | 空 Stream 上 `XREADGROUP` 的阻塞时间。 |
| `MODEL_VELO_USAGE_CLAIM_IDLE` | 否 | `30s` | pending 消息允许被其他 Worker 认领前的空闲时间。 |
| `MODEL_VELO_USAGE_MAX_DELIVERIES` | 否 | `5` | 坏版本/坏载荷进入 dead-letter 前的最大投递次数。 |
| `MODEL_VELO_USAGE_RETRY_BACKOFF` | 否 | `500ms` | Redis 读取/认领失败后的 Context-aware 退避。 |
| `MODEL_VELO_USAGE_WORKER_TIMEOUT` | 否 | `10s` | 单批写库与关闭收尾共享的最大处理时间。 |
| `MODEL_VELO_USAGE_DEAD_LETTER_MAX_LEN` | 否 | `100000` | dead-letter Stream 的近似长度上限；旧 `MODEL_VELO_USAGE_STREAM_MAX_LEN` 仍兼容，但不能与新变量同时设置。 |
| `MODEL_VELO_USAGE_ENFORCE_STREAM` | 否 | `true` | 对 OpenAI-compatible 流请求强制合并 `stream_options.include_usage=true`；仅在确认自定义上游不兼容时关闭。 |
| `MODEL_VELO_USAGE_RETENTION_DAYS` | 否 | `90` | PostgreSQL Usage 保留天数，范围 0–3650；`0` 禁用自动清理。 |
| `MODEL_VELO_USAGE_MAINTENANCE_INTERVAL` | 否 | `1h` | Worker 执行保留期清理的间隔。 |
| `MODEL_VELO_USAGE_MAINTENANCE_BATCH_SIZE` | 否 | `1000` | 单次删除批量，Worker 会在维护超时内持续分批清理。 |
| `MODEL_VELO_USAGE_PRICING_JSON` | 否 | `[]` | 版本化 USD/百万 token 价目表；支持生效时间、缓存、音频、图像和推理 token 专属费率，最大 256 KiB。 |
| `MODEL_VELO_USAGE_PRICING_REFRESH_INTERVAL` | 否 | `30s` | 独立 Worker 刷新托管价格目录的间隔。 |
| `MODEL_VELO_USAGE_PENDING_TIMEOUT` | 否 | `15m` | 把未完成 outbox 生命周期恢复成中断事件前的保守等待时间，范围 5m–24h。 |
| `MODEL_VELO_QUOTA_RESERVATION_TTL` | 否 | `15m` | 崩溃后活动额度预留转为保守估算结算的时间。 |
| `MODEL_VELO_QUOTA_REAP_INTERVAL` | 否 | `1m` | 扫描过期额度预留的间隔。 |
| `MODEL_VELO_QUOTA_DEFAULT_MAX_OUTPUT_TOKENS` | 否 | `4096` | 请求未给输出上限时用于 Token/成本预留的默认值。 |
| `MODEL_VELO_LOG_FORMAT` | 否 | `json` | `json` 或本地开发用 `text`。 |
| `MODEL_VELO_LOG_LEVEL` | 否 | `info` | `debug`、`info`、`warn` 或 `error`。 |
| `MODEL_VELO_SERVICE_NAME` | 否 | `model-velo` | 日志与 OpenTelemetry service name。 |
| `MODEL_VELO_METRICS_TOKEN` | 否 | 无 | 设置后 API 与 Worker `/metrics` 要求至少 32 字节的 Bearer Token。 |
| `MODEL_VELO_WORKER_METRICS_ADDR` | 否 | `:9091` | Usage Worker 的 `/healthz`、`/readyz`、`/metrics` 监听地址。 |
| `MODEL_VELO_OTEL_EXPORTER_OTLP_ENDPOINT` | 否 | 无 | OTLP/HTTP Trace Collector 绝对 URL；空值禁用导出。 |
| `MODEL_VELO_OTEL_EXPORTER_OTLP_INSECURE` | 否 | `false` | 是否允许明文 OTLP/HTTP；`http` URL 也会启用。 |
| `MODEL_VELO_OTEL_SAMPLE_RATIO` | 否 | `0.1` | Parent-based Trace 采样比例，范围 0–1。 |
| `MODEL_VELO_READINESS_TIMEOUT` | 否 | `1s` | 单次依赖就绪检查的总期限。 |
| `MODEL_VELO_ROUTING_JSON` | API 必填 | 无 | 显式定义任意数量的 Provider、厂商预设、模型能力、Provider 级运行参数、精确/默认路由和有序候选；只配置一个 Provider 时也不能省略。 |
| `MODEL_VELO_BREAKER_FAILURE_THRESHOLD` | 否 | `5` | 连续可计数失败达到此值后 Open，范围 1–1000。 |
| `MODEL_VELO_BREAKER_OPEN_DURATION` | 否 | `30s` | Open 冷却时间，范围 `1s`–`10m`。 |
| `MODEL_VELO_BREAKER_HALF_OPEN_PROBES` | 否 | `1` | HalfOpen 同时允许的探测数，以及重新关闭所需成功数，范围 1–100。 |
| `MODEL_VELO_QUEUE_MAX_IN_FLIGHT` | 否 | `20` | 每个 Provider、每个网关进程允许的同时执行数，范围 1–10,000。 |
| `MODEL_VELO_QUEUE_MAX_WAITING` | 否 | `100` | 每个 Provider、每个网关进程允许的等待数；`0` 表示满载时立即拒绝，范围 0–100,000。 |
| `MODEL_VELO_QUEUE_WAIT_TIMEOUT` | 否 | `2s` | 等待 Provider 槽位的最长期限，范围 `10ms`–`1m`，同时受请求 Context 更早截止时间约束。 |
| `MODEL_VELO_RETRY_MAX_ATTEMPTS` | 否 | `3` | 每个候选最多调用次数，范围 1–10；包含第一次调用。 |
| `MODEL_VELO_RETRY_INITIAL_BACKOFF` | 否 | `100ms` | 首次可退避重试的基础等待，范围 `10ms`–`30s`。 |
| `MODEL_VELO_RETRY_MAX_BACKOFF` | 否 | `2s` | 指数退避上限，不得小于初始等待且不超过 `30s`。 |
| `MODEL_VELO_RETRY_BACKOFF_MULTIPLIER` | 否 | `2` | 退避倍数，范围 1–10。 |
| `MODEL_VELO_RETRY_JITTER_RATIO` | 否 | `0.2` | 退避随机抖动比例，范围 0–1。 |
| `MODEL_VELO_REQUEST_TIMEOUT` | 否 | `45s` | Cache miss 后 Retry 与全部 Fallback 候选共享的执行预算，范围 `1s`–`5m`。 |
| `MODEL_VELO_ATTEMPT_TIMEOUT` | 否 | `20s` | 非流式单次调用或流式建连+首事件等待期限，至少 `100ms` 且不得超过 Provider 执行总预算。 |

价目使用十进制字符串，避免浮点配置误差；同一个 Provider/模型的生效时间窗口不能重叠。`provider` 或 `model` 可用 `*` 作为启动时已知的回退价格：

```powershell
$env:MODEL_VELO_USAGE_PRICING_JSON = '[{"provider":"openai-main","model":"gpt-4o-mini","version":"openai-2026-07","effective_from":"2026-07-01T00:00:00Z","input_usd_per_million":"0.15","output_usd_per_million":"0.60","cached_read_usd_per_million":"0.075"}]'
```

上游 `http.Client` 不再维护第三套总超时。非流式调用服从 Attempt Context 和父级请求预算；流式调用在首事件前同时受 Attempt Timeout 与父 Context 约束，首事件验证通过后不再沿用短 Attempt deadline，但后续两个有效事件之间仍复用当前 Provider 的 Attempt Timeout 作为静默上限，上游 heartbeat 会重置计时器。HTTP Server 写超时固定覆盖托管运行时允许的最长 5 分钟请求预算并额外保留 15 秒收尾时间；每个非流式请求仍由自身快照中的较短总预算取消。流式 Handler 在首事件验证后清除总写截止时间，并为每个客户端帧设置独立 15 秒写截止时间。

### Provider 厂商预设

`vendor` 负责选择厂商目录和默认 API Base，`type` 必须显式声明协议；二者不匹配时启动失败。一个厂商可以配置多个 Provider ID，一个 Provider 的 `models` 可以列出多个文本、推理或 VLM 模型；`base_url` 可覆盖区域、私有部署或账号级端点。模型清单由配置显式声明，代码不硬编码容易过期的型号。

| `vendor` | 必须声明的 `type` | 默认 API Base |
|---|---|---|
| `openai` | `openai` | `https://api.openai.com/v1` |
| `anthropic` | `anthropic` | `https://api.anthropic.com` |
| `google` | `gemini` | `https://generativelanguage.googleapis.com/v1beta` |
| `azure` | `azure-openai` | 必须配置资源 Endpoint |
| `alibaba` | `dashscope` | `https://dashscope.aliyuncs.com/api/v1` |
| `cohere` | `cohere` | `https://api.cohere.com/v2` |
| `ollama` | `ollama` | `http://localhost:11434` |
| `bedrock` | `bedrock` | 必须配置区域 Bedrock Runtime Endpoint |
| `cloudflare` | `cloudflare` | 必须配置含 account ID 的 API Base |
| `mistral` | `mistral` | `https://api.mistral.ai/v1` |
| `xai` | `xai` | `https://api.x.ai/v1` |
| `deepseek` | `deepseek` | `https://api.deepseek.com` |
| `zhipu` | `zhipu` | `https://open.bigmodel.cn/api/paas/v4` |
| `groq` | `groq` | `https://api.groq.com/openai/v1` |
| `nvidia` | `nvidia` | `https://integrate.api.nvidia.com/v1` |
| `together` | `together` | `https://api.together.ai/v1` |

当前 Azure Adapter 使用 v1 Endpoint 的 `api-key` 鉴权；Bedrock Adapter 使用 Bedrock Runtime Converse 与 Bearer API Key，尚未接 IAM Role/SigV4；Cloudflare Adapter 使用 Workers AI 官方 `/ai/v1/chat/completions`。配置其他鉴权方式或模型专属输入 schema 时会明确报不支持，不会静默伪装成兼容成功。

Mistral、DeepSeek、xAI、Zhipu、Groq、NVIDIA 和 Together 都有独立的 Adapter 类型与文件，后续厂商专属字段、错误解析或能力规则只进入对应 Adapter。它们没有复制七份相同的请求发送代码：官方相同的 Bearer 鉴权、OpenAI Chat JSON 和响应校验由 `compatible.go` 复用。DeepSeek Adapter 使用其 API Base 下的 `/chat/completions`，不会套用自定义兼容端点默认补 `/v1` 的规则。

另有 `custom`，必须显式配置 `type: "openai-compatible"` 和 `base_url`。已提供兼容接口的 Google、Alibaba 等厂商也可以显式把 `type` 设成 `openai-compatible`；这是主动选择兼容协议。

`MODEL_VELO_ROUTING_JSON` 没有隐式默认值。缺失或只有空白时进程会在连接 PostgreSQL、Redis 和监听端口前启动失败；即使只接一个自定义兼容上游，也必须显式写出 Provider 和 Route。配置中的 `model: "*"` 是默认路由；`upstream_model` 留空表示透传客户端模型，设置值则执行模型别名映射。`candidates` 的数组顺序就是稳定优先级；Router 先按请求所需能力过滤候选，首个实际执行的候选失败且错误策略允许 Fallback 时，Orchestrator 才会执行下一候选。

`model_capabilities` 按上游模型声明 `text`、`image`、`audio`、`file`、`tools`、`structured`；未声明的模型安全地默认为 `text`。能力不能超过当前 Adapter 协议真正能承载的范围，否则启动失败。Provider 的可选 `runtime` 可分别覆盖 `breaker`、`queue`、`retry` 和 `http`：请求总预算仍是全链路唯一值，Provider 只覆盖单次 `attempt_timeout`；HTTP 每 Host 连接数默认跟随该 Provider 的 `queue.max_in_flight`，避免 Queue 放行后又在默认连接池里形成第二条隐形队列。

`runtime` 支持的字段是：`breaker.failure_threshold/open_duration/half_open_max_probes`、`queue.max_in_flight/max_waiting/wait_timeout`、`retry.max_attempts/initial_backoff/max_backoff/backoff_multiplier/jitter_ratio/attempt_timeout`，以及 `http.max_idle_connections/max_idle_connections_per_host/max_connections_per_host`。未写的字段继承上表环境变量默认值，所有组合都在启动时校验。

以下示例让同一个 Gemini Provider 承载文本和 VLM，并准备 DeepSeek、OpenAI 作为其他路由或后续 fallback 候选：

```json
{
  "providers": [
    {"id":"gemini-main","vendor":"google","type":"gemini","models":["gemini-2.5-flash","gemini-2.5-pro"],"model_capabilities":{"gemini-2.5-flash":["text"],"gemini-2.5-pro":["text","image"]},"runtime":{"queue":{"max_in_flight":40,"max_waiting":200,"wait_timeout":"2s"},"retry":{"max_attempts":2,"attempt_timeout":"15s"},"http":{"max_idle_connections":80}}},
    {"id":"deepseek-main","vendor":"deepseek","type":"deepseek","models":["deepseek-chat","deepseek-reasoner"],"model_capabilities":{"deepseek-chat":["text","tools"],"deepseek-reasoner":["text"]}},
    {"id":"openai-main","vendor":"openai","type":"openai","models":["gpt-4o-mini","gpt-4o"],"model_capabilities":{"gpt-4o-mini":["text","tools"],"gpt-4o":["text","image","tools"]}}
  ],
  "routes": [
    {"model":"fast-chat","candidates":[{"provider":"gemini-main","upstream_model":"gemini-2.5-flash"},{"provider":"deepseek-main","upstream_model":"deepseek-chat"}]},
    {"model":"vision","candidates":[{"provider":"gemini-main","upstream_model":"gemini-2.5-pro"},{"provider":"openai-main","upstream_model":"gpt-4o"}]}
  ]
}
```

多 Key 配置与路由配置分离，避免 Route Plan 携带凭证。对应的多 Provider Key 配置：

```json
{"providers":[{"provider_id":"gemini-main","keys":[{"id":"primary","secret":"replace-with-gemini-key"}]},{"provider_id":"deepseek-main","keys":[{"id":"primary","secret":"replace-with-deepseek-key"},{"id":"secondary","secret":"replace-with-second-deepseek-key"}]},{"provider_id":"openai-main","keys":[{"id":"primary","secret":"replace-with-openai-key"}]}]}
```

需要鉴权的 Provider ID 必须与 Key 配置一一对应；每个此类 Provider 至少一个 Key，Key ID 必须唯一，Secret 不能为空。Ollama 不出现在 Key 配置中。配置错误会在连接 PostgreSQL/Redis 前阻止启动。模型名只是路由声明，不表示网关替厂商保证该账号、区域或模型已开通。

可以复制 `.env.example` 的变量名用于本地配置，但程序当前不会自动读取 `.env` 文件，需要在启动进程前把变量注入环境。

> 预发布改名说明：项目已统一改为 `Model-Velo`。旧环境变量不再读取，旧 API Key 前缀不再接受，旧 Redis namespace 不再命中。新 Compose 默认数据库和用户是 `model_velo`；已有 PostgreSQL 数据不会被删除，需要时可继续通过新的 `MODEL_VELO_POSTGRES_DSN` 显式指向原数据库。

## 启动 PostgreSQL 和 Redis

需要先启动 Docker Desktop。首次使用时复制配置并替换两个本地密码：

```powershell
Copy-Item .env.example .env
```

`MODEL_VELO_POSTGRES_PASSWORD` 必须与 `MODEL_VELO_POSTGRES_DSN` 中的密码保持一致。然后解析配置并启动基础设施：

```powershell
docker compose config --quiet
docker compose up -d postgres redis
docker compose ps
```

`docker compose ps` 中两个服务都应显示为 `healthy`。排查启动问题：

```powershell
docker compose logs postgres
docker compose logs redis
```

停止容器但保留 PostgreSQL 和 Redis 数据：

```powershell
docker compose down
```

显式删除容器、网络和两个命名数据卷，恢复全新环境：

```powershell
docker compose down --volumes
```

不要在仍需保留本地开发数据时执行带 `--volumes` 的命令。

真实 PostgreSQL 综合用例只读取显式的 `MODEL_VELO_POSTGRES_TEST_DSN`，不会回退到 API 使用的 DSN。测试用户需要拥有 `CREATE SCHEMA` 权限；每次运行创建随机的 `model_velo_it_*` schema，在其中重复执行 GORM `AutoMigrate`、API Key 生命周期和认证授权验证，结束后只定向删除该随机 schema：

```powershell
$env:MODEL_VELO_POSTGRES_TEST_DSN = $env:MODEL_VELO_POSTGRES_DSN
go test -count=1 -run '^TestPostgresAPIKeyLifecycle$' -v ./internal/apikey
```

未设置测试 DSN 时用例会跳过，跳过不等于成功。2026-07-18 已使用无持久卷的 PostgreSQL 17.10 一次性容器真实通过：随机 schema 中两次 AutoMigrate、约束/索引检查和完整认证生命周期均成功，随后 schema 与容器已清理。

## Redis Client

Model-Velo 固定使用 `github.com/redis/go-redis/v9 v9.21.0`。API 启动时按照配置创建自带连接池的 Redis Client，并在 `MODEL_VELO_REDIS_DIAL_TIMEOUT` 期限内执行 `PING`：

- `required`：网络、认证或 Redis 服务异常会关闭 Client 并阻止 HTTP Server 启动；
- `optional`：启动失败会记录不含密码的警告并继续，Client 保留，Redis 恢复后可供后续命令使用；
- 根 Context 已取消时，无论策略是什么都停止启动；
- 进程退出时通过幂等 `Close` 释放连接池。

Chat 请求会在认证和模型授权成功后访问 Redis 执行限流，再执行 Exact Cache 读写。`optional` 只决定启动 Ping 失败时能否继续监听；运行中的限流故障由 `MODEL_VELO_RATE_LIMIT_FAILURE_POLICY` 决定，缓存故障固定为 fail-open。

## Redis 租户限流

当前配额模型是“每个租户、每个网关模型、每个固定窗口最多 N 个已接受请求”。Redis Key 的结构为：

```text
model-velo:rate-limit:v1:<environment>:tenant:<tenant_sha256>:model:<model_sha256>
```

Key 包含环境命名空间，并分别对 tenant ID 和规范化模型名做 SHA-256；它不包含 Model-Velo API Key、Provider Key 或原始模型名。多个 API Key 只要属于同一租户，就共享该租户对应模型的配额。

限流器通过一个 Lua 脚本完成读取计数、首次写入、递增、TTL、Redis 服务端时间和决策返回。Redis 把脚本作为单个命令执行，执行期间不会插入其他客户端命令，因此多个 Model-Velo 实例不会在应用侧 `GET` 与 `SET` 之间同时放过超额请求。拒绝请求不会递增计数或延长窗口。

请求通过限流时响应包含 `X-RateLimit-Limit`、`X-RateLimit-Remaining` 和 Unix 秒格式的 `X-RateLimit-Reset`。额度耗尽时返回结构化 `429 rate_limit_exceeded`，并增加整数秒 `Retry-After`。Redis 运行时故障时：

- `fail-closed`（默认）：返回结构化 `503 rate_limit_unavailable`，不调用 Provider；
- `fail-open`：继续调用 Provider，并返回 `X-RateLimit-Status: bypassed`，但不伪造额度与重置值；
- 请求 Context 已取消时：无论故障策略如何都立即停止，不把取消转换为放行。

该算法的边界是固定窗口交界处可能出现突发流量；当前阶段选择它是因为配额语义直接、单脚本可解释。Redis 8.8.0 容器测试已经验证窗口恢复、租户/模型隔离和双 Client 竞争不超发；race 门禁因本机缺少 CGO/C 编译器暂未完成，仍是阶段 2 的最后环境缺口。

真实 Redis 用例通过 `MODEL_VELO_REDIS_TEST_ADDR`、`MODEL_VELO_REDIS_TEST_PASSWORD` 和 `MODEL_VELO_REDIS_TEST_DB` 显式启用，使用随机 namespace 定向清理，不执行 `FLUSHDB`。未配置时测试会跳过，跳过不等于验证通过。

## Redis Exact Response Cache

缓存只处理已经通过请求校验、认证、模型授权和限流的 `stream=false` 请求。请求携带 `Cache-Control: no-store` 时显式绕过。缓存命中仍会消耗一次租户配额，这是当前明确选择的调用顺序，而不是“命中免费”。

Redis Key 为：

```text
model-velo:response-cache:v1:<environment>:tenant:<tenant_sha256>:model:<model_sha256>:route:<route_version_sha256>:request:<canonical_request_sha256>
```

Key 不包含原始 API Key、tenant ID、模型名或提示词。规范化会递归排序 JSON 对象字段，并保留数组顺序、数字文本以及“字段缺失/显式给值”的区别；因此消息、工具和生成参数顺序不会被错误改写。包含重复对象字段的 JSON 直接绕过缓存，避免不同解析规则造成碰撞。

命中返回 Redis 中的完整合法 JSON；未命中只在首候选返回完整成功 JSON 后回填。Fallback 成功、上游错误、响应读取失败、客户端取消和显式绕过均不写缓存，避免临时降级结果覆盖正常路由语义。Redis 读写错误只记录 request ID 和安全错误，不记录 tenant、模型、Key 或提示词，并继续 Provider；响应通过 `X-Model-Velo-Cache: HIT|MISS|BYPASS` 表达本次状态，最终 Usage Event 也记录同一状态。

`MODEL_VELO_CACHE_ROUTE_VERSION` 只属于缓存命名空间，并以摘要进入缓存 Key。更换上游、模型映射或候选顺序时必须递增它，避免新路由命中旧响应。2026-07-18 真实 Redis 8.8.0 综合用例已验证跨请求命中、TTL、租户/参数隔离、错误不缓存和关闭 Client 后的故障降级；测试使用无持久卷的一次性容器并已自动清理。

当前调用顺序是 `认证 → 授权 → 限流 → Route Plan → Cache → Fallback Orchestrator → 当前候选 Attempt（Breaker → Queue → Key → Retry → Provider → 反馈/释放）→ 成功回填`。无路由返回 `503 route_unavailable`；Breaker Open 返回 `503 provider_circuit_open`；Queue 满或等待超时分别返回 `503 provider_queue_full`、`503 provider_queue_timeout`；没有可用 Key 返回 `503 provider_keys_exhausted`。本地拒绝不会调用当前 Provider；是否进入下一候选由统一 Fallback 策略决定。

## Provider Circuit Breaker

Breaker 以稳定 Provider ID 隔离状态。Closed 累计网络错误、上游超时、500/502/503/504，以及上游返回 2xx 却不满足已声明响应协议的错误；成功会清零连续失败。401/403、429、普通 4xx、501 等非策略 5xx、网关主动限制的超大响应和客户端取消不会把整个 Provider 熔断。达到阈值后进入 Open，缓存未命中的新请求快速失败；冷却到期后进入 HalfOpen，只放行配置数量的探测，探测成功关闭，符合计数策略的失败重新 Open。

每次准入返回一次性 Permit，Provider 调用后必须反馈成功或分类后的 Failure；调用链额外使用 `defer Abandon()` 防止提前退出泄漏 HalfOpen 探测名额。401/403 与 429 不计 Provider 故障，而是反馈给当前 Key 的健康状态。

## Provider 有界 Queue

Queue 是网关进程内的 Provider 容量保护，不是 Redis 租户配额。每个 Provider 拥有独立的带缓冲 channel：channel 容量是正在调用上游的硬上限；容量耗尽后，只有 `MAX_WAITING` 个请求可以等待，更多请求立即返回结构化 503。等待同时监听请求 Context 和 Queue Timer，因此客户端断开、请求总 deadline 或 Queue 等待超时都会停止占位。

成功取得槽位后会得到一次性 Lease，调用链用 `defer Release()` 配对归还；原子标记阻止重复释放。Queue Snapshot 只暴露 Provider ID、active、waiting、容量和拒绝/超时/取消计数，不包含 Provider Key、请求内容或错误 cause。Queue 拒绝不计入 Breaker 故障，因为它说明本实例容量饱和，不代表上游已经失败。

Queue Registry 按 Provider ID 隔离；primary 与每个 fallback 候选都会进入自己 Provider 的 Queue，不会复用上一个候选的 Lease。Queue 不是跨实例分布式信号：部署多个副本时，理论总并发上限约为“副本数 × `MAX_IN_FLIGHT`”，应结合上游账号配额设置每实例容量。

## Provider Key Selector

Key Registry 按 Provider ID 隔离。选择器只把稳定 Key ID 暴露给快照和失败元数据，Secret 只在构造上游 Authorization 时读取。每次选择通过原子游标轮换起点，再在读锁下跳过禁用或冷却中的 Key。401 表示凭证无效，会永久禁用当前 Key，直到进程使用修正配置重启；403 可能来自模型或账号权限，只把该 Key 排除在当前请求之外；429 优先按上游 `Retry-After` 冷却，缺失或非法时使用 30 秒，最长限制为 24 小时。成功反馈可以清除临时冷却，但不会恢复 401 禁用状态。

401、403 或 429 反馈后，当前请求会释放 Queue/Breaker 资源并重新进入完整准入链，从其他可用 Key 继续。同一请求不会再次选择已返回 401/403 的 Key。所有可用 Key 都在冷却时，只有仍有 Retry 次数且最早恢复时间小于剩余请求预算才等待；等待期间可被客户端取消，且不占用 Queue 槽位或 Breaker Permit。等待不合算时立即 Fallback；没有候选可切换时返回 `503 provider_keys_exhausted`，并携带最早恢复时间对应的 `Retry-After`。指定 5xx、网络和上游超时则优先保持原 Key 并按指数退避重试。Key Selector 与 Retry 的完整并发/race 证据仍保留在阶段 3 门禁，不能据此宣称已经完成高并发验证。

## PostgreSQL 与 GORM

Model-Velo 直接使用 GORM 连接 PostgreSQL。API 启动时先取得 GORM 底层的 `database/sql` 连接池，设置最大打开连接数、最大空闲连接数、连接寿命和空闲时间，再执行有界 `PingContext`。数据库不可达时 API 不会开始监听端口。官方 `gorm.io/driver/postgres` 内部依赖 pgx，因此模块图中仍会出现 `pgx // indirect`，但 Model-Velo 源码不直接导入或调用 pgx。

连接成功后，启动流程调用 GORM `AutoMigrate` 同步以下模型：

1. `Tenant` → `tenants`；
2. `APIKey` → `api_keys`；
3. `TenantModelGrant` → `tenant_model_grants`。

当前阶段不再维护 `golang-migrate`、独立 Migration CLI 或手写 SQL 文件。`AutoMigrate` 用于创建缺失的表、列、索引、外键和检查约束，但不会负责删除废弃列，也没有 `down`/版本回滚命令。发生破坏性 schema 变更时，必须先备份数据并为该次变更单独设计显式升级方案，不能依赖启动时自动删改数据。

Schema 安全边界：

- `tenants.slug` 唯一，租户状态只允许 `active/disabled`；
- `api_keys` 只保存 Key 前缀、查找摘要、不可逆哈希和哈希版本，不存在明文 Key 列；
- 租户存在 API Key 时禁止删除，避免凭证被级联误删；
- 删除没有 API Key 的租户时，其模型授权记录级联删除；
- 禁用租户不会删除 Key 或模型授权，后续认证查询必须同时检查租户状态；
- `tenant_model_grants` 使用 `(tenant_id, gateway_model)` 主键避免重复授权。

## Model-Velo API Key

Key 格式固定为：

```text
mvl_<16 字符公开定位符>_<43 字符随机密文>
```

定位符由 12 个随机字节编码得到，密文由 32 个随机字节编码得到，均使用无填充 Base64 URL 编码。数据库不会保存完整 Key：

Base64URL 的合法字符本身包含 `_`，所以解析器按 16 字符 locator 和 43 字符 secret 的固定位置切分，不使用普通的下划线 `Split`；否则网关会偶发拒绝自己生成的合法 Key。

- `key_prefix` 保存 `mvl_<定位符>`，用于后台展示；
- `lookup_digest` 保存公开定位符前缀的 SHA-256，用于索引查询；
- `key_hash` 保存随机密文在服务端 Pepper 下的 HMAC-SHA-256；
- `hash_version` 当前为 `1`，为以后更换校验方案保留升级边界；
- 明文 Key 只在创建成功时输出一次，之后无法从数据库恢复。

先配置 PostgreSQL 和稳定的 Pepper。本地首次使用可以在 PowerShell 中生成随机 Pepper：

```powershell
$pepperBytes = New-Object byte[] 48
[System.Security.Cryptography.RandomNumberGenerator]::Fill($pepperBytes)
$env:MODEL_VELO_API_KEY_PEPPER = [Convert]::ToBase64String($pepperBytes)
```

同一个数据库必须长期使用同一个 Pepper。初始化租户、模型授权和首个 Key：

```powershell
go run ./cmd/model-velo-admin bootstrap-tenant `
  --slug demo `
  --name "Demo Tenant" `
  --label "local development" `
  --models "gpt-4o-mini,gpt-4.1-mini"
```

命令成功后输出 `tenant_id`、`api_key_id`、公开前缀以及仅出现一次的 `api_key`。为已有租户创建第二个 Key：

```powershell
go run ./cmd/model-velo-admin create-key `
  --tenant-id "replace-with-tenant-uuid" `
  --label "second key" `
  --expires-in 720h
```

禁用或永久吊销 Key：

```powershell
go run ./cmd/model-velo-admin disable-key --id "replace-with-api-key-uuid"
go run ./cmd/model-velo-admin revoke-key --id "replace-with-api-key-uuid"
```

禁用和吊销会立即影响后续认证。永久吊销的 Key 不能通过 `disable-key` 降级回普通禁用状态。

## HTTP 认证与模型授权

`GET /healthz` 不需要 API Key。`POST /v1/chat/completions` 必须使用一个且只能使用一个 Authorization Header：

```http
Authorization: Bearer mvl_<locator>_<secret>
```

网关按以下顺序处理请求：

1. 严格解析 Bearer Header，拒绝缺失、重复、错误 scheme、空 Token 和额外空白；
2. 根据 locator 摘要查询 Key，并以 HMAC 常量时间比较验证 secret；
3. 检查 Key 状态、UTC 过期时间和租户状态；
4. 把 `tenant_id`、`api_key_id` 和公开 Key 前缀写入请求 Context；
5. 解析 Chat JSON 后，检查 `(tenant_id, model)` 是否存在于 `tenant_model_grants`；
6. 用 tenant ID 和规范化模型名执行 Redis 原子限流；
7. 生成有序 Route Plan，再以环境、租户、模型、路由版本和规范化请求查询 Exact Cache；
8. 命中直接返回；未命中或缓存故障时，在统一总预算内按候选顺序执行 Attempt；
9. 每个候选重新选择对应 Adapter、Breaker、Queue 和 Key，在候选内有限 Retry；允许 Fallback 的最终失败才进入下一候选；
10. 首个完整成功 JSON 回填缓存并返回客户端；普通 400、取消或候选耗尽返回结构化错误。
11. 请求终态由 Collector finalize 一次，并在独立短超时内投递 Usage Event；流式请求在 `[DONE]`、取消或提交后中断时分别记录终态。

未知 Key、错误 secret、禁用、吊销、过期和禁用租户对客户端统一返回 `401 invalid_api_key`，避免暴露具体凭证状态。到期时刻本身已经算过期，创建 Key 时也只接受严格晚于当前时刻的 UTC 时间。缺少模型授权返回 `403 model_not_allowed`。认证数据库故障返回 `503 authentication_unavailable`，不会退化成匿名请求。

客户端的 Model-Velo Key 不会转发给外部 Provider。Provider 请求只使用 `MODEL_VELO_PROVIDER_KEYS_JSON` 中按目标 Provider ID 选出的 Secret。

## 运行 Model-Velo API

需要 Go 工具链、一个已经健康的 PostgreSQL、Redis，以及至少一个显式配置的 Provider。启动时会自动同步当前 GORM 模型。下面用单个自定义 OpenAI-compatible Provider 演示；这仍然是一份显式路由，不是默认回退：

```powershell
$env:MODEL_VELO_ROUTING_JSON = '{"providers":[{"id":"upstream","vendor":"custom","type":"openai-compatible","base_url":"https://api.example.com","models":["*"],"model_capabilities":{"*":["text"]}}],"routes":[{"model":"*","candidates":[{"provider":"upstream"}]}]}'
$env:MODEL_VELO_PROVIDER_KEYS_JSON = '{"providers":[{"provider_id":"upstream","keys":[{"id":"primary","secret":"replace-with-provider-key"}]}]}'
$env:MODEL_VELO_ENVIRONMENT = "development"
$env:MODEL_VELO_POSTGRES_DSN = "postgres://model_velo:replace-with-local-postgres-password@localhost:5432/model_velo?sslmode=disable"
$env:MODEL_VELO_API_KEY_PEPPER = "use-the-same-stable-pepper-as-model-velo-admin"
$env:MODEL_VELO_REDIS_ADDR = "localhost:6379"
$env:MODEL_VELO_REDIS_PASSWORD = "replace-with-local-redis-password"
$env:MODEL_VELO_RATE_LIMIT_FAILURE_POLICY = "fail-closed"
go run ./cmd/model-velo
```

Usage Worker 与 API 使用相同的 PostgreSQL、Redis 和 `MODEL_VELO_ENVIRONMENT`，但作为独立进程运行：

```powershell
go run ./cmd/model-velo-usage-worker
```

Worker 启动会幂等创建 consumer group。数据库写入失败不 ACK；收到退出信号后停止新读取，并给当前批次最多 `MODEL_VELO_USAGE_WORKER_TIMEOUT` 完成。生产环境应为每个并发 Worker 设置不同的 `MODEL_VELO_USAGE_CONSUMER`，或使用默认的主机名与 PID。

价目新增或修正后，可显式重算缺少成本的历史记录。命令要求确认、限制时间范围和单批数量，不会把无法定价的记录写成零；输出非空 `next_cursor` 时，把它传给下一批即可越过当前批次中仍无法定价的行：

```powershell
go run ./cmd/model-velo-admin reprice-usage `
  --start 2026-07-01T00:00:00Z `
  --end 2026-08-01T00:00:00Z `
  --missing-only=true `
  --limit 1000 `
  --confirm
```

请求示例：

```powershell
$modelVeloKey = "replace-with-created-mvl-key"
$headers = @{
  Authorization = "Bearer $modelVeloKey"
  "Content-Type" = "application/json"
}
$body = @{
  model = "gpt-4o-mini"
  messages = @(
    @{ role = "user"; content = "hello" }
  )
} | ConvertTo-Json -Depth 5

Invoke-RestMethod `
  -Method Post `
  -Uri "http://localhost:8080/v1/chat/completions" `
  -Headers $headers `
  -Body $body
```

同一接口也可直接使用 OpenAI Python SDK；`base_url` 必须指向网关的 `/v1`，Key 使用创建时仅返回一次的 Model-Velo 业务 Key：

```python
import os
from openai import OpenAI

client = OpenAI(
    api_key=os.environ["MODEL_VELO_CLIENT_KEY"],
    base_url="http://localhost:8080/v1",
)

completion = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "hello"}],
)
print(completion.choices[0].message.content)

stream = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "stream hello"}],
    stream=True,
)
for chunk in stream:
    if chunk.choices and chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

Bash/curl 的最小重放请求：

```bash
curl --fail-with-body http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer ${MODEL_VELO_CLIENT_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}'
```

聊天、Responses 与 Anthropic Messages 请求体上限为 16 MiB，Embeddings 为 8 MiB；这允许常见内嵌多模态输入，同时仍在 JSON 解码前限制内存占用。更大的媒体应使用厂商支持的 URL/File ID，而不是继续扩大 Base64 请求。

不要使用示例地址调用真实服务，也不要把真实 Key 写入 `.env.example`、测试、日志或 Git。

日常开发检查命令：

```powershell
$goFiles = rg --files -g '*.go'
gofmt -w $goFiles
go test ./...
go vet ./...
```

项目当前采用快速交付模式：学习和讲解以生产代码、请求链和故障策略为主，不要求逐个学习测试文件。一个纵向功能通常最多维护一个合并测试文件；已有端到端证据能覆盖时，不再给 Handler、Service 和存储层重复补同类测试，也不追求覆盖率数字。

`go test ./...` 和 `go vet ./...` 在每次交付前统一运行一次。`go test -race ./...`、真实 Redis/PostgreSQL 故障矩阵和完整异常测试只在阶段门禁集中执行一次；环境不满足时记录缺口，不把纯测试任务继续当作开发进度。

阶段拆解和实时完成度见 `TODO.md`，完整规划边界见 `ARCHITECTURE.md`，开发约束见 `AGENTS.md`。
