# Model-Velo

Model-Velo 是一个用 Go 和 Gin 编写的 OpenAI-compatible LLM 网关。目前已经进入阶段 3：非流式 Chat Completions、PostgreSQL 鉴权、Redis 租户限流、Exact Response Cache、有序 Route Plan 和多 Provider 运行时已经接入请求链。

## 当前已经实现

- `GET /healthz` 健康检查；
- `POST /v1/chat/completions` 非流式请求校验和转发；
- 16 个内置厂商 Adapter：厂商身份与构造入口彼此独立；公开采用 OpenAI Chat 报文的厂商只复用协议编解码和 HTTP 边界；
- 一个厂商可配置多个 Provider 实例，每个实例可声明多个文本或视觉模型；
- request ID 生成、校验、响应回传和上游传播；
- 请求体与上游响应体大小限制；
- 上游超时、网络失败、HTTP 错误和非法响应的结构化错误；
- 操作系统退出信号和有界优雅关闭；
- 使用 `httptest.Server` 的本地测试，不调用真实付费 API；
- 固定版本的 PostgreSQL、Redis Compose 配置；
- 基于 GORM 的 PostgreSQL 连接、启动 Ping、连接池配置和退出关闭；
- 启动时通过 GORM `AutoMigrate` 同步租户、API Key 和模型授权三张表；
- Model-Velo API Key 随机生成、摘要查找、HMAC 校验、过期判断、禁用和吊销；
- `model-velo-admin` 本地管理命令，可初始化租户、模型授权和首个 API Key；
- Gin Bearer 认证中间件、请求身份 Context 和租户模型授权检查；
- 官方 `go-redis/v9` Client、显式连接池、启动 Ping、可选启动降级和退出关闭；
- 基于 Redis Lua 的租户+模型固定窗口限流，以及可配置的 fail-open/fail-closed 运行时故障策略；
- 租户隔离的 Redis Exact Response Cache，支持规范化请求哈希、TTL、显式绕过和缓存故障降级；
- 精确模型/默认模型路由、有序候选去重、primary 选择和上游模型映射；
- 按模型声明 `text`、`image`、`tools` 能力，规划时先过滤协议或模型无法承载的候选；
- Provider Circuit Breaker 三态、指定故障计数、Open 快速拒绝和 HalfOpen 有界探测；
- 按 Provider 隔离的进程内有界 Queue，限制运行数和等待数，并传播请求取消；
- Provider 多 Key 安全身份与并发轮换：401 永久禁用错误 Key，403 只在当前请求内换 Key，429 按 `Retry-After` 临时冷却；
- 按 Provider ID 隔离的 Adapter、Circuit Breaker、Queue 和 Key Registry；
- 单候选 Attempt Executor：每个候选独立执行 Breaker、Queue、Key、有限 Retry 和上游调用；
- 有序 Fallback Orchestrator：成功立即停止，普通 400/取消停止，模型不可用等策略允许的失败进入下一候选；Fallback 成功响应不写 Exact Cache；
- Provider 执行总预算、单次调用超时和 Context-aware 退避取消；
- 每个 Provider 可独立覆盖 Breaker、Queue、Retry、Attempt Timeout 与 HTTP 连接池；
- 非流式文本与 VLM `text`/`image_url` 内容：兼容协议原样透传，原生协议转换并归一化响应。

SSE 和 Usage 数据链路尚未实现。当前请求会按 Route Plan 顺序执行候选；每个候选内部在策略允许时进行有限 Retry，耗尽后只有具备 Fallback 信号的失败才进入下一候选。合法请求所需的能力不被当前 Provider 支持时会直接尝试下一候选，不消耗 Retry，也不计入 Breaker；所有候选都不支持时返回 `400 unsupported_provider_capability`。`POST /v1/chat/completions` 必须携带有效的 Model-Velo API Key，请求模型必须存在于该租户的模型授权表，并依次通过租户限流和 Route Plan；缓存未命中后，每次 Provider 调用都会重新取得目标 Provider 的 Breaker Permit、Queue 槽位和可用 Provider Key。`GET /healthz` 保持公开。

## 当前 Chat 契约

请求必须使用 `Content-Type: application/json`，当前对以下字段做出保证：

| 字段 | 当前行为 |
|---|---|
| `model` | 必填且不能是空白字符串；用于授权和 Route Plan，可通过 `upstream_model` 映射成厂商模型名。 |
| `messages` | 必填且至少包含一条消息。 |
| `messages[].role` | 接受 `system`、`developer`、`user`、`assistant`、`tool`；`developer`/`tool` 会要求目标模型声明 `tools` 能力。 |
| `messages[].content` | 接受非空字符串或非空内容块数组。当前跨协议保证 `text` 与 `image_url`；其他内容块只在目标 OpenAI-compatible 上游自行支持时原样透传。 |
| `stream` | 省略或为 `false` 时按非流式处理；`true` 返回 `stream_not_supported`，不会调用上游。 |

OpenAI、Mistral、DeepSeek、xAI、Zhipu、Groq、NVIDIA 和 Together 均由各自的厂商 Adapter 装配端点与能力边界；由于这些厂商当前公开的 Chat API 使用 OpenAI Chat 报文，它们复用同一套 wire codec 和 HTTP 安全边界，并保留客户端原始 JSON，只在路由配置要求时改写 `model`。这与把所有厂商注册成同一个 OpenAI Adapter 不同。Anthropic、Gemini、DashScope、Cohere、Ollama、Bedrock 和 Cloudflare 等原生协议只转换当前明确支持的公共字段：消息、`max_tokens`/`max_completion_tokens`、`temperature`、`top_p` 和 `stop`；未知厂商扩展不会被伪装成已支持。

请求 JSON 只解析一次。兼容协议会保留未知顶层字段和消息字段；原生协议在转换前检查这些字段，无法无损表达时返回能力不匹配并尝试下一候选，不会静默丢字段。

工具调用和 `developer`/`tool` 角色仅在采用 OpenAI Chat 报文、且模型显式声明 `tools` 的候选上原样透传；当前原生转换器不会伪造工具调用支持。音频、视频和不同厂商的私有扩展仍不保证。Anthropic Adapter 支持 URL/base64 图片；Gemini、Ollama 和 Bedrock 当前要求 `data:*;base64` 图片；OpenAI/Azure/Mistral/xAI 等 Chat 协议按模型声明处理 `image_url`。Adapter 已知无法表达某种输入时会报告能力不匹配并触发 Fallback；上游模型自身的实际模态能力仍由厂商响应决定。

成功时，兼容协议响应在通过 2xx、Content-Type、大小、错误信封和非空 Chat `choices[].message` 检查后原样返回；原生协议响应会转换为非流式 OpenAI Chat Completion。原生响应缺少 Usage 时省略 `usage`，不会伪造全零计费数据；出现当前无法表示的非文本输出时明确失败并按策略 Fallback。

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
| `MODEL_VELO_CACHE_ROUTE_VERSION` | 否 | `routes-v1` | 1–64 位路由语义版本；切换上游或模型映射时必须修改，使旧缓存自然失效。 |
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
| `MODEL_VELO_ATTEMPT_TIMEOUT` | 否 | `20s` | 单次上游 HTTP 调用期限，至少 `100ms` 且不得超过 Provider 执行总预算。 |

上游 `http.Client` 不再维护第三套总超时；网络调用完全服从 Attempt Context 和父级请求预算。非流式 HTTP Server 的写超时由请求预算加 15 秒前置处理/收尾余量派生，避免静态 Server 超时先于可靠性层截断响应。

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

当前 Azure Adapter 使用 v1 Endpoint 的 `api-key` 鉴权；Bedrock Adapter 使用 Bedrock Runtime Converse 与 Bearer API Key，尚未接 IAM Role/SigV4；Cloudflare Adapter 面向 Workers AI 中接受 `messages` 的 chat 模型。配置其他鉴权方式或模型专属输入 schema 时会明确报不支持，不会静默伪装成兼容成功。

Mistral、DeepSeek、xAI、Zhipu、Groq、NVIDIA 和 Together 都有独立的 Adapter 类型与文件，后续厂商专属字段、错误解析或能力规则只进入对应 Adapter。它们没有复制七份相同的请求发送代码：官方相同的 Bearer 鉴权、OpenAI Chat JSON 和响应校验由 `compatible.go` 复用。DeepSeek Adapter 使用其 API Base 下的 `/chat/completions`，不会套用自定义兼容端点默认补 `/v1` 的规则。

另有 `custom`，必须显式配置 `type: "openai-compatible"` 和 `base_url`。已提供兼容接口的 Google、Alibaba 等厂商也可以显式把 `type` 设成 `openai-compatible`；这是主动选择兼容协议。

`MODEL_VELO_ROUTING_JSON` 没有隐式默认值。缺失或只有空白时进程会在连接 PostgreSQL、Redis 和监听端口前启动失败；即使只接一个自定义兼容上游，也必须显式写出 Provider 和 Route。配置中的 `model: "*"` 是默认路由；`upstream_model` 留空表示透传客户端模型，设置值则执行模型别名映射。`candidates` 的数组顺序就是稳定优先级；Router 先按请求所需能力过滤候选，首个实际执行的候选失败且错误策略允许 Fallback 时，Orchestrator 才会执行下一候选。

`model_capabilities` 按上游模型声明 `text`、`image`、`tools`；未声明的模型安全地默认为 `text`。能力不能超过当前 Adapter 协议真正能承载的范围，否则启动失败。Provider 的可选 `runtime` 可分别覆盖 `breaker`、`queue`、`retry` 和 `http`：请求总预算仍是全链路唯一值，Provider 只覆盖单次 `attempt_timeout`；HTTP 每 Host 连接数默认跟随该 Provider 的 `queue.max_in_flight`，避免 Queue 放行后又在默认连接池里形成第二条隐形队列。

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

命中返回 Redis 中的完整合法 JSON；未命中只在首候选返回完整成功 JSON 后回填。Fallback 成功、上游错误、响应读取失败、客户端取消和显式绕过均不写缓存，避免临时降级结果覆盖正常路由语义。Redis 读写错误只记录 request ID 和安全错误，不记录 tenant、模型、Key 或提示词，并继续 Provider；响应通过 `X-Model-Velo-Cache: HIT|MISS|BYPASS` 表达本次状态，内部也保留同一状态供后续 Usage 使用。

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
