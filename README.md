# Model-Velo

Model-Velo 是一个用 Go 和 Gin 编写的 OpenAI-compatible LLM 网关。目前已经进入阶段 3：非流式 Chat Completions、PostgreSQL 鉴权、Redis 租户限流、Exact Response Cache 和有序 Route Plan 已经接入请求链。

## 当前已经实现

- `GET /healthz` 健康检查；
- `POST /v1/chat/completions` 非流式请求校验和转发；
- 单个 OpenAI-compatible 上游；
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
- Provider Circuit Breaker 三态、指定故障计数、Open 快速拒绝和 HalfOpen 有界探测；
- 按 Provider 隔离的进程内有界 Queue，限制运行数和等待数，并传播请求取消。

Key 轮换、Retry、Fallback、多 Provider 实际调用、SSE 和 Usage 数据链路尚未实现。`POST /v1/chat/completions` 必须携带有效的 Model-Velo API Key，请求模型必须存在于该租户的模型授权表，并依次通过租户限流和 Route Plan；缓存未命中后还必须取得 Provider Breaker Permit 和 Queue 槽位才会调用上游。`GET /healthz` 保持公开。

## 阶段 1 Chat 契约

请求必须使用 `Content-Type: application/json`，阶段 1 对以下字段做出保证：

| 字段 | 阶段 1 行为 |
|---|---|
| `model` | 必填且不能是空白字符串；原样发送给上游，不做模型别名或 Provider 模型映射。 |
| `messages` | 必填且至少包含一条消息。 |
| `messages[].role` | 只接受 `system`、`user`、`assistant`。 |
| `messages[].content` | 只接受非空字符串；多模态数组、工具结果等内容形态暂不支持。 |
| `stream` | 省略或为 `false` 时按非流式处理；`true` 返回 `stream_not_supported`，不会调用上游。 |

网关校验完上述最小字段后，会把客户端的原始 JSON Body 原样发送给上游。因此 `temperature`、`top_p`、`max_tokens`、`stop`、`presence_penalty`、`frequency_penalty`、`seed` 和 `response_format` 等生成参数可以透传，但阶段 1 不验证其类型、取值和上游支持情况。其他未知顶层字段也不会被网关删除，不过不属于阶段 1 保证的兼容范围。

阶段 1 不保证工具调用、多模态消息、结构化消息内容、`developer`/`tool` 角色以及供应商私有扩展；即使某个顶层字段能被透传，与这些能力相关的消息结构仍可能在本地校验时被拒绝。

成功时，网关仅确认上游返回 2xx、JSON Content-Type、合法 JSON 且未超过大小限制，然后原样返回完整响应，避免因网关响应结构过窄而丢失上游字段。

## 配置

| 环境变量 | 必填 | 默认值 | 用途 |
|---|---:|---|---|
| `MODEL_VELO_HTTP_ADDR` | 否 | `:8080` | Model-Velo HTTP 监听地址。 |
| `MODEL_VELO_ENVIRONMENT` | API 必填 | 无 | 1–32 位小写环境标识，用于隔离 Redis Key，例如 `development`、`staging`。 |
| `MODEL_VELO_UPSTREAM_BASE_URL` | 是 | 无 | OpenAI-compatible 上游根地址；网关在其后拼接 `/v1/chat/completions`。 |
| `MODEL_VELO_UPSTREAM_API_KEY` | 是 | 无 | 只用于上游 `Authorization: Bearer ...`，不得写入日志或提交仓库。 |
| `MODEL_VELO_UPSTREAM_TIMEOUT` | 否 | `30s` | 单次上游 HTTP 调用总超时，使用 Go duration 格式。 |
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
| `MODEL_VELO_CACHE_ROUTE_VERSION` | 否 | `single-provider-v1` | 1–64 位路由语义版本；切换上游或模型映射时必须修改，使旧缓存自然失效。 |
| `MODEL_VELO_ROUTING_JSON` | 否 | 单 Provider 全模型透传 | 定义 Provider、可用模型、精确/默认路由和有序候选；当前执行层只允许一个 `openai-compatible` Provider。 |
| `MODEL_VELO_BREAKER_FAILURE_THRESHOLD` | 否 | `5` | 连续可计数失败达到此值后 Open，范围 1–1000。 |
| `MODEL_VELO_BREAKER_OPEN_DURATION` | 否 | `30s` | Open 冷却时间，范围 `1s`–`10m`。 |
| `MODEL_VELO_BREAKER_HALF_OPEN_PROBES` | 否 | `1` | HalfOpen 同时允许的探测数，以及重新关闭所需成功数，范围 1–100。 |
| `MODEL_VELO_QUEUE_MAX_IN_FLIGHT` | 否 | `20` | 每个 Provider、每个网关进程允许的同时执行数，范围 1–10,000。 |
| `MODEL_VELO_QUEUE_MAX_WAITING` | 否 | `100` | 每个 Provider、每个网关进程允许的等待数；`0` 表示满载时立即拒绝，范围 0–100,000。 |
| `MODEL_VELO_QUEUE_WAIT_TIMEOUT` | 否 | `2s` | 等待 Provider 槽位的最长期限，范围 `10ms`–`1m`，同时受请求 Context 更早截止时间约束。 |

`MODEL_VELO_ROUTING_JSON` 中 `model: "*"` 是默认路由；`upstream_model` 留空表示透传客户端模型，设置值则执行模型别名映射。`candidates` 的数组顺序就是稳定优先级；当前只调用 primary，后续候选留给 Attempt/Fallback 切片。

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

命中返回 Redis 中的完整合法 JSON；未命中只在 Provider 返回完整成功 JSON 后回填。上游错误、响应读取失败、客户端取消和显式绕过均不写缓存。Redis 读写错误只记录 request ID 和安全错误，不记录 tenant、模型、Key 或提示词，并继续 Provider；响应通过 `X-Model-Velo-Cache: HIT|MISS|BYPASS` 表达本次状态，内部也保留同一状态供后续 Usage 使用。

`MODEL_VELO_CACHE_ROUTE_VERSION` 同时写入 Route Plan，并以摘要进入缓存 Key。更换上游、模型映射或候选顺序时必须递增它，避免新路由命中旧响应。2026-07-18 真实 Redis 8.8.0 综合用例已验证跨请求命中、TTL、租户/参数隔离、错误不缓存和关闭 Client 后的故障降级；测试使用无持久卷的一次性容器并已自动清理。

当前调用顺序是 `认证 → 授权 → 限流 → Route Plan → Cache → Breaker 准入 → Provider Queue → primary Provider → Queue 释放 → Breaker 反馈 → 成功回填`。无路由返回 `503 route_unavailable`；Breaker Open 返回 `503 provider_circuit_open` 和剩余秒数 `Retry-After`；Queue 满或等待超时分别返回 `503 provider_queue_full`、`503 provider_queue_timeout`，这些拒绝都不会调用 Provider。

## Provider Circuit Breaker

Breaker 以稳定 Provider ID 隔离状态。Closed 只累计网络错误、上游超时以及 500/502/503/504；成功会清零连续失败。401/403、429、普通 4xx、501 等非策略 5xx、协议错误和客户端取消都不会把整个 Provider 熔断。达到阈值后进入 Open，缓存未命中的新请求快速失败；冷却到期后进入 HalfOpen，只放行配置数量的探测，探测成功关闭，符合计数策略的失败重新 Open。

每次准入返回一次性 Permit，Provider 调用后必须反馈成功或分类后的 Failure；调用链额外使用 `defer Abandon()` 防止提前退出泄漏 HalfOpen 探测名额。当前 401/403 与 429 只完成“不计 Provider 故障”的边界，Key 禁用、轮换和 cooldown 尚未实现。

## Provider 有界 Queue

Queue 是网关进程内的 Provider 容量保护，不是 Redis 租户配额。每个 Provider 拥有独立的带缓冲 channel：channel 容量是正在调用上游的硬上限；容量耗尽后，只有 `MAX_WAITING` 个请求可以等待，更多请求立即返回结构化 503。等待同时监听请求 Context 和 Queue Timer，因此客户端断开、请求总 deadline 或 Queue 等待超时都会停止占位。

成功取得槽位后会得到一次性 Lease，调用链用 `defer Release()` 配对归还；原子标记阻止重复释放。Queue Snapshot 只暴露 Provider ID、active、waiting、容量和拒绝/超时/取消计数，不包含 Provider Key、请求内容或错误 cause。Queue 拒绝不计入 Breaker 故障，因为它说明本实例容量饱和，不代表上游已经失败。

Queue Registry 按 Provider ID 隔离，但当前配置执行层仍只调用一个 Provider。Queue 不是跨实例分布式信号：部署多个副本时，理论总并发上限约为“副本数 × `MAX_IN_FLIGHT`”，应结合上游账号配额设置每实例容量。

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
7. 通过限流后，以环境、租户、模型、路由版本和规范化请求查询 Exact Cache；
8. 命中直接返回；未命中或缓存故障才调用外部 Provider；
9. 只把 Provider 的完整成功 JSON 回填缓存，然后返回客户端。

未知 Key、错误 secret、禁用、吊销、过期和禁用租户对客户端统一返回 `401 invalid_api_key`，避免暴露具体凭证状态。到期时刻本身已经算过期，创建 Key 时也只接受严格晚于当前时刻的 UTC 时间。缺少模型授权返回 `403 model_not_allowed`。认证数据库故障返回 `503 authentication_unavailable`，不会退化成匿名请求。

客户端的 Model-Velo Key 不会转发给外部 Provider。Provider 请求使用独立的 `MODEL_VELO_UPSTREAM_API_KEY`。

## 运行 Model-Velo API

需要 Go 工具链、一个已经健康的 PostgreSQL，以及一个 OpenAI-compatible 上游。启动时会自动同步当前 GORM 模型。PowerShell 示例：

```powershell
$env:MODEL_VELO_UPSTREAM_BASE_URL = "https://api.example.com"
$env:MODEL_VELO_UPSTREAM_API_KEY = "replace-with-provider-key"
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
