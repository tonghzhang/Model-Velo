# Model-Velo 架构设计

> 状态：协议面、控制面、额度账本、Usage outbox 和阶段 6 工程化代码已接入。2026-07-24 的全量测试、vet、构建、真实 PostgreSQL/Redis 集成和独立性检查已通过；Windows race 工具链与远端 CI 仍以 Gate 文件中的实际证据为准。

## 0. 当前实现边界

截至 2026-07-24，Chat Completions、Responses、Anthropic Messages 与 Embeddings 共用认证、授权、额度、路由和可靠性底座：

```text
HTTP Client
  -> Gin Router / request ID middleware
  -> Chat 请求大小、Content-Type 和最小字段校验
  -> Bearer 认证和租户模型授权
  -> Redis Lua 租户+模型限流
  -> Router 按精确规则或默认规则生成有序 Route Plan
  -> PostgreSQL 额度窗口原子预留 Token 与成本
  -> Redis Exact Cache 查询
  -> 命中直接返回；未命中由 Fallback Orchestrator 建立统一执行预算
  -> 按 Route Plan 顺序把当前候选交给 Attempt Executor
  -> Attempt 每次重新执行 Breaker 准入和 Provider Queue 有界等待
  -> Key Selector 选择可用 Key；5xx/网络重试优先保持原 Key
  -> 执行候选并映射上游模型，单次 HTTP 调用有独立期限
  -> 按结果反馈 Key/Breaker 并释放 Queue；允许时 Context-aware 退避后重试
  -> 候选最终失败且策略允许时进入下一候选；首个成功立即停止
  -> 完整成功 JSON 回填 Redis Cache
  -> Provider Adapter 按厂商协议转换请求并调用官方端点
  -> 兼容响应原样返回；原生响应归一化为 OpenAI Chat Completion
  -> Usage 与额度按真实 Provider 结果结算；outbox 保证 Redis 故障后可重投
```

`cmd/model-velo` 负责 API、控制平面、动态 runtime、额度预留和优雅关闭；`cmd/model-velo-usage-worker` 装配 Redis、PostgreSQL、outbox relay 与 Usage Worker。`internal/adminauth` 隔离管理员身份与 RBAC，`internal/controlplane` 管理加密配置版本和审计，`internal/gateway` 原子发布不可变运行时，`internal/quota` 负责强一致预留/结算，`internal/observability` 负责日志、指标和 tracing。协议、可靠性、Provider 和 Usage 包继续保持各自边界。在线 API 会同步写最小 outbox 生命周期和额度账本，但不会同步执行 Usage 聚合或历史 SQL。

启动配置没有隐式 Provider：`MODEL_VELO_ROUTING_JSON` 必须显式声明 Provider 与 Route，一个 Provider 也按相同格式配置；缺失或空白会在连接外部基础设施和监听 HTTP 前失败。生产 HTTP 装配只接受完整的 Adapter、Breaker、Queue 与 Route Registry；只要存在需要 API Key 的 Adapter，就必须同时提供 Key Registry，错误装配在 Attempt Executor 构造时失败。全部 Adapter 均为无鉴权类型时允许 Key Registry 为空。当前不保留单 Provider 快捷构造路径。

阶段 3 会先按请求所需的 `text`、`image`、`audio`、`file`、`tools`、`structured` 能力过滤 Route Plan，再在统一预算内按顺序执行候选：每个候选内部按 Provider ID 有限 Retry，401 永久停用无效 Key，403 只在当前请求内换 Key，429 优先换未冷却 Key；全部 Key 冷却时，只有最早恢复时间能放入剩余预算才在释放准入资源后等待；指定 5xx、网络和上游超时有界退避，每次调用重新进入 Breaker、Queue 和 Key 选择。候选耗尽后，普通 400 与取消停止，明确的模型不可用 4xx 等策略失败进入下一候选。Provider 层对 Function Tool、结构化输出和已建模内容块执行严格转换；无法等价映射的字段只触发 Fallback，不 Retry、不计 Breaker，全部候选都不支持时返回明确 400。上游模型自身的实际模态能力仍由厂商响应决定。总预算与跨 Fallback 取消已有端到端证据；仅 race 环境门禁仍未完成。阶段 1 历史证据见 `STAGE1_GATE.md`，阶段 3 收口证据见 `STAGE3_GATE.md`。

阶段 4 的 Provider 边界实现了 `StreamingAdapter.OpenStream`。兼容请求会强制 `stream=true` 并保留未知字段；原生 Adapter 分别读取 Anthropic/Gemini/DashScope/Cohere SSE、Ollama NDJSON 和 Bedrock AWS EventStream，再转换为统一 OpenAI Chunk。文本、Tool 参数增量、finish reason 和可获得的 Usage 都在提交前通过统一 Chunk 校验；SSE 单行上限 1 MiB、单事件和 Bedrock 帧上限 2 MiB，转换通过同步 Pipe 形成有界背压。`ChatEventStream` 拒绝坏 UTF-8/JSON，跳过且不向客户端透传心跳，并把完整 `[DONE]` 标记为终止。reliability 层的流式 Attempt 会让每次尝试重新进入 Breaker、Queue、Key 和 Adapter，在 Attempt Timeout 内读取首个有效内容事件；策略允许时进行候选内 Retry，耗尽后由 `Orchestrator.OpenStream` 有序 Fallback。失败流会在下一次尝试前关闭 Body、取消上游并释放准入资源，成功 PreparedStream 只持有最终流的资源。预提交总预算使用独立 Context，成功上游流继承原客户端 Context，后续有效事件的静默上限复用 Provider Attempt Timeout，心跳会重置计时器。HTTP Handler 在检查底层 Flusher 后才调用这条链，有效首事件返回后清除当前响应继承的 Server 总写截止时间，每个 SSE 帧使用独立 15 秒 Write/Flush 截止时间；`[DONE]` 标记成功，提交后失败只记录稳定类别并结束当前流，不再 Retry/Fallback 或追加 JSON 错误体。

## 1. 项目定位

Model-Velo 是一个面向学习、实习求职展示和实际运行的轻量 LLM Gateway，技术栈固定为：

- Go + Gin：HTTP API；
- Redis：分布式限流、响应缓存、Usage Stream；
- PostgreSQL：API Key、Usage 和必要的持久化数据；
- OpenAI-compatible API：对客户端提供统一协议；
- 外部 Provider：通过统一 Adapter 契约接入厂商原生协议、厂商 OpenAI Chat 协议和自定义兼容端点。

项目采用“先完成最小纵向请求，再逐步增强”的方式，不在第一阶段搭建完整平台。

## 2. 参考来源与独立设计

只读参考：

- GoModel：本地 `D:\agent开源项目\GoModel-main\GoModel-main`，GitHub <https://github.com/ENTERPILOT/GoModel>
- Bifrost：本地 `D:\agent开源项目\bifrost-dev\bifrost-dev`，GitHub <https://github.com/maximhq/bifrost>

概念来源：

- 从 GoModel 借鉴显式分层、Provider Adapter、响应缓存、Circuit Breaker、错误分类 Fallback 和流式 observer 的思路。
- 从 Bifrost 借鉴 Provider Queue、Attempt Executor、Key 选择/轮换、有序 Fallback 和 SSE 首 Chunk 检查的思路。
- Model-Velo 自己确定 Gin 接入、Redis 分布式限流、Redis Stream Usage 管道、PostgreSQL 幂等落库以及具体包边界。

2026-07-22 只读核对 GoModel 的 `cmd/gomodel/main.go`、`internal/providers/init.go` 和 ADR-0001：它通过显式工厂注册选择可用 Provider，初始化后没有任何 Provider 会返回错误，而不是虚构一个兼容上游。Model-Velo 没有复制其注册结构，独立采用“显式路由 JSON + 启动失败”的配置边界。

这些是架构概念，不复制两个项目的代码、命名和目录。具体重合度约束见 `AGENTS.md`。

2026-07-18 使用 `modelmux-clone-check 1.0`，按连续至少 12 条非空逻辑行或 80 个 token 的阈值扫描 19 个生产 Go 文件、1763 条逻辑行，并排除测试、vendor、generated、UI 和示例。对 GoModel、Bifrost 及二者合并集的字面重复度和标识符归一化近似重复度均为 0.00%；人工复核也未发现相同独特命名、注释、错误文案或控制流。该结果只证明当前快照，不替代后续里程碑复查。

2026-07-21 在原生 Provider Adapter 切片后以同一工具和阈值复查 47 个生产 Go 文件、4892 条逻辑行：对 GoModel 的字面/近似重复度均为 0.00%；对 Bifrost 的字面重复度为 0.00%、近似重复度为 0.31%（15 行）；合并参考集为 0.00%/0.31%。人工复核本轮协议结构、错误文案和转换控制流未发现参考项目特有实现被复制。该结果仍只证明当前快照，阶段 3 总门禁保持未完成。

2026-07-24 最终复查使用 `modelmux-clone-check 1.0`，参数为连续至少 12 个非空逻辑行或 80 个有效 token，排除 `_test.go`、vendor、generated、UI、frontend、web、examples 和 testdata；共扫描 92 个生产 Go 文件、17242 条逻辑行。GoModel 为字面 `0.00%`、近似 `0.57%`，Bifrost 为字面 `0.00%`、近似 `1.13%`，合并参考集为字面 `0.00%`、近似 `1.20%`，均严格小于 10%。人工复核 Provider 转换、控制面、额度和 Usage outbox 的控制流、独特命名、注释与错误字符串，未发现参考项目特有实现被复制。

## 3. 运行时部署架构

下面是当前部署图：

```mermaid
flowchart LR
    Client["客户端<br/>OpenAI SDK / HTTP"] -->|"JSON / SSE"| API["model-velo-api<br/>Go + Gin"]

    Admin["管理员"] -->|"独立 Key / RBAC"| API
    API -->|"鉴权 / 加密配置 / 配额预留 / Usage outbox"| PG[("PostgreSQL")]
    API -->|"限流 / Cache / Usage Stream"| Redis[("Redis")]
    API -->|"HTTPS"| Providers["外部模型 Provider<br/>16 个厂商配置 + custom"]

    Worker["model-velo-usage-worker"] -->|"XREADGROUP / XAUTOCLAIM / XACK"| Redis
    Worker -->|"幂等 INSERT/UPSERT"| PG

    Prom["Prometheus"] -->|"scrape /metrics"| API
    Prom -->|"scrape :9091/metrics"| Worker
    API -->|"OTLP traces"| OTel["OpenTelemetry Collector"]
```

运行时约束：

- 当前 `model-velo-api` 同时装配 PostgreSQL、Redis 和所有路由中声明的 Provider；每个 Provider 的 Adapter 与可靠性状态相互隔离。
- API 与 Worker 使用同一仓库、共享稳定的数据契约，但作为不同进程运行，避免 Usage 落库拖慢在线请求。
- Redis、PostgreSQL 都是外部组件，通过 Docker Compose 提供本地开发环境；生产部署细节不在早期阶段展开。

## 4. 核心请求架构

```mermaid
flowchart TD
    C["客户端"] --> Edge["边缘检查<br/>request ID / Body 上限 / Content-Type"]
    Edge --> Auth["API Key 身份认证"]
    Auth --> Parse["OpenAI 协议解析与业务校验"]
    Parse --> Authorize["模型级授权"]
    Authorize --> Limit["Redis 分布式限流"]
    Limit --> Route["Router 生成有序候选路由计划"]
    Route --> Cache{"响应缓存命中？"}

    Cache -->|"非流式命中"| Response["非流式 JSON"]
    Cache -->|"未命中 / SSE bypass"| Orch["Request Orchestrator"]

    Orch --> Attempt["对当前候选执行 Attempt"]
    Attempt --> Breaker{"Circuit Breaker 准入"}
    Breaker -->|"Open"| Decide{"错误允许 Fallback？"}
    Breaker -->|"Allow / Half-open probe"| Queue["Provider 有界队列"]
    Queue --> Key["Provider Key 选择"]
    Key --> Retry["按错误类型进行有限 Retry"]
    Retry --> Upstream["Provider Adapter → 上游模型"]
    Upstream --> Feedback["更新 Key 状态和 Breaker"]

    Feedback -->|"成功"| Mode{"非流式还是 SSE？"}
    Feedback -->|"失败"| Decide
    Decide -->|"存在下一个候选"| Attempt
    Decide -->|"无候选"| Error["返回最终标准化错误"]

    Mode -->|"非流式"| StoreCache["写入响应缓存"]
    StoreCache --> Response
    Mode -->|"SSE"| First["读取并检查第一个 Chunk"]
    First -->|"首 Chunk 前失败"| Decide
    First -->|"有效首 Chunk"| Commit["提交 SSE Header 并输出"]
    Commit -->|"后续失败"| StreamEnd["结束流；禁止 Retry/Fallback"]

    Response --> Usage["固化 Usage Event"]
    Error --> Usage
    StreamEnd --> Usage
    Usage --> Outbox[("PostgreSQL Usage Outbox")]
    Outbox --> RedisStream["Redis Stream"]
    RedisStream --> UsageWorker["Usage Worker"]
    UsageWorker --> UsageDB[("PostgreSQL")]

    Obs["日志 / Metrics / Trace"] -. "贯穿整个 request context" .-> Edge
    Obs -.-> Orch
    Obs -.-> Usage
```

最重要的结构不是模块数量，而是两个闭环：

1. **Fallback 外层循环**：Orchestrator 选择候选；每个 primary 或 fallback 候选都重新进入完整的 Breaker、Queue、Key、Retry 和上游调用流程。
2. **Breaker 状态闭环**：调用前检查是否允许；调用后根据网络错误、5xx 或成功更新状态。Key 自身的 401/403/429 不应轻易熔断整个 Provider。

## 5. 模块职责边界

| 模块 | 负责 | 不负责 |
|---|---|---|
| HTTP Handler | Gin 路由、Body 上限、解码、HTTP/SSE 输出 | SQL、全局 Retry、Provider 选择 |
| Auth | Key 哈希校验、租户身份、模型权限 | 限流、Provider 凭证选择 |
| Rate Limiter | Redis 原子限流、返回剩余额度和 Retry-After | 业务路由、计费落库 |
| Protocol | OpenAI 请求/响应类型、校验、标准错误 | 直接调用外部 Provider |
| Router | 根据请求和配置生成有序候选列表 | 自己执行 HTTP Retry |
| Response Cache | 稳定 Cache Key、TTL、租户隔离、命中/回填 | 决定 Provider 是否健康 |
| Orchestrator | 依次运行候选 Attempt、控制总时间预算和 Fallback | 处理具体 Provider JSON 字段差异 |
| Attempt Executor | Breaker、Queue、Key、Retry 和一次候选调用 | 跨候选递归 Fallback |
| Provider Adapter | 上游请求转换、HTTP 调用、响应/错误转换 | 全局限流、数据库写入 |
| Usage Emitter | 先写最小 PostgreSQL 生命周期，再固化完整 outbox Event 并尝试 XADD | 执行统计 SQL |
| Usage Worker | outbox relay、Redis consumer group、幂等落库、成本快照、重试/认领 pending、保留期 | 参与在线模型调用 |
| Usage Reader | 当前租户的明细、聚合、时间序列与严格查询边界 | 读取其他租户或修改历史记录 |
| Admin Auth | 独立管理员 Key、固定角色权限、最后 owner 保护 | 接受业务模型 Key |
| Control Plane | 加密版本、乐观并发、脱敏读取、审计与原子 runtime 发布 | 把 Secret 写日志或返回 GET |
| Quota Ledger | 周期策略、请求前预留、结束结算和过期保守恢复 | 把 tokenizer 估算冒充精确 Token |
| Observability | JSON 日志、Prometheus、W3C Trace/OTLP | 在标签或 span 中放 Key/Prompt |

早期实现可以让几个职责暂时位于同一包中，但不能把它们混进同一个巨大 Handler。只有出现第二个真实实现时才提取通用接口。

## 6. 非流式请求时序

```mermaid
sequenceDiagram
    autonumber
    participant C as 客户端
    participant H as Gin Handler
    participant A as Auth + Rate Limit
    participant R as Router + Cache
    participant O as Orchestrator
    participant X as Attempt Executor
    participant P as Provider
    participant U as 外部模型
    participant E as Usage Emitter

    C->>H: POST /v1/chat/completions, stream=false
    H->>A: 认证租户并限流
    A->>R: 标准化请求并生成路由计划
    R->>R: 查询响应缓存
    alt cache hit
        R-->>H: cached response
    else cache miss
        R->>O: Execute(route plan)
        loop primary 与有序 fallbacks
            O->>X: Attempt(candidate, total deadline)
            X->>X: Breaker → Queue → Key → Retry
            X->>P: Adapter.Call
            P->>U: HTTPS
            U-->>P: JSON 或错误
            P-->>X: 标准响应/错误
            X-->>O: attempt result
        end
        O-->>R: 最终响应/错误
        R->>R: 成功结果写缓存
    end
    R-->>H: OpenAI-compatible JSON
    H-->>C: HTTP response
    H->>E: 发出成功/缓存命中/失败 Usage Event
```

## 7. SSE 请求时序

```mermaid
sequenceDiagram
    autonumber
    participant C as 客户端
    participant H as Gin Handler
    participant O as Orchestrator
    participant X as Attempt Executor
    participant P as Provider
    participant U as 外部模型
    participant E as Usage Emitter

    C->>H: POST /v1/chat/completions, stream=true
    H->>O: OpenStream(route plan)
    loop 尚未提交有效首 Chunk
        O->>X: AttemptStream(candidate)
        X->>P: Breaker → Queue → Key → 上游流
        P->>U: 建立 streaming request
        U-->>X: stream
        X->>X: 检查首 Chunk
        alt 建流/首 Chunk 失败
            X-->>O: 分类错误，可 Retry/Fallback
        else 有效首 Chunk
            X-->>H: 已验证 stream + first chunk
        end
    end
    H-->>C: 200 text/event-stream + first chunk
    loop 后续 Chunk
        U-->>H: chunk
        H-->>C: SSE flush
    end
    opt 客户端断开或后续错误
        C-xH: cancel
        H->>X: 传播 context cancellation
        Note over H,O: 已输出 Chunk 后不再 Retry/Fallback
    end
    H->>E: 流结束 Usage Event
```

## 8. 错误分类和恢复策略

| 情况 | Retry | 换 Key | Fallback | Breaker |
|---|---|---|---|---|
| 本地解析/校验 400 | 否 | 否 | 否 | 不记录 Provider 失败 |
| Provider 能力不匹配 | 否 | 否 | 允许 | 不触发 |
| Provider 返回网关暂不能表达的输出 | 否 | 否 | 允许 | 不触发；最终映射为 502 |
| 上游普通 4xx | 否 | 否 | 否 | 不触发 |
| 模型不可用 400/404/422 | 否 | 否 | 允许 | 不触发 |
| 401 | 不使用原 Key 重试 | 有其他 Key 时可以 | Key 耗尽后按策略决定 | 永久停用 Key，不熔断 Provider |
| 403 | 当前请求不再使用原 Key | 有其他 Key 时可以 | Key 耗尽后按策略决定 | 不修改全局 Key 状态，不熔断 Provider |
| 429 | 尊重 `Retry-After`，受总预算限制 | 优先尝试未冷却 Key | Key 耗尽后允许 | 默认做 Key 冷却，不直接算 Provider 故障 |
| 500/502/503/504 | 有限次数 | 通常保持当前 Key | Retry 耗尽后允许 | 计入 Provider 失败 |
| 网络连接/超时 | 有限次数 | 通常保持当前 Key | Retry 耗尽后允许 | 计入 Provider 失败 |
| 畸形 2xx 协议响应 | 否 | 否 | 允许 | 计入 Provider 失败；网关主动大小限制除外 |
| 客户端取消 | 否 | 否 | 否 | 不把取消算作 Provider 故障 |
| SSE 首 Chunk 前错误 | 按上述类别 | 按上述类别 | 允许 | 按上述类别更新 |
| SSE 已输出 Chunk 后错误 | 否 | 否 | 否 | 根据真实上游错误更新 |

Retry 次数不是唯一约束：所有 Retry 和 Fallback 必须共享请求总 deadline，避免候选越多、响应时间越失控。

## 9. Redis 和 PostgreSQL 数据链路

### PostgreSQL Schema

- Model-Velo 当前统一使用 GORM，不在业务代码中直接调用 pgx，也不维护 `golang-migrate` runner 和手写 SQL migration；模块图中的 pgx 仅是 GORM 官方 PostgreSQL Dialector 的间接依赖。
- API 与 Usage Worker 启动并 Ping 成功后执行 `AutoMigrate`，同步租户/API Key/模型授权、Usage Event/outbox、管理员/RBAC、运行时配置、价目、审计以及额度策略/窗口/预留表。
- 模型标签保留主键、唯一索引、查询索引、外键删除策略和必要的检查约束。
- `AutoMigrate` 不作为任意破坏性变更工具：删除列、改写存量数据或不兼容约束变更必须单独评审、备份并设计显式升级步骤。
- 当前没有 schema 版本回退命令；这是阶段 2 选择轻量 GORM 工作流后的明确边界。
- PostgreSQL 综合门禁使用显式测试 DSN 和随机 `model_velo_it_*` schema，重复执行 AutoMigrate 后检查表、索引、外键、检查约束及完整 API Key 生命周期，最后只删除该随机 schema；2026-07-24 已与 Redis 8.8 一起在 PostgreSQL 17.10 临时容器重新通过并完成清理。

### API Key 存储与校验

- Key 使用 `mvl_<locator>_<secret>`，locator 和 secret 都来自 `crypto/rand`，完整明文只在创建时返回一次。由于 Base64URL 内容可以包含 `_`，解析按两个字段的固定编码长度定位分隔符，不能按所有下划线拆分。
- PostgreSQL 保存公开前缀、前缀 SHA-256、secret 的 HMAC-SHA-256 和哈希版本，不保存 secret 或完整 Key。
- HMAC Pepper 由 `MODEL_VELO_API_KEY_PEPPER` 提供，不写入数据库、日志或仓库；Pepper 变化会使已有 Key 无法通过校验。
- 查询先使用 locator 摘要命中唯一索引，再用常量时间比较验证 HMAC；未知 locator 与错误 secret 对外统一归类为无效凭证。
- 每次认证都从 PostgreSQL 检查 Key 状态、租户状态和 UTC 过期时间，因此禁用和吊销不依赖本地缓存过期；创建与认证共享同一到期边界，到期时刻本身即失效。
- `model-velo-admin` 负责租户初始化、Key 创建、禁用和吊销；永久吊销不可退回普通禁用。
- Gin 使用业务 Key 保护 `/v1`，使用独立管理员 Key/RBAC 保护 `/admin/v1`；`/healthz`、`/readyz` 公开但只返回安全状态，`/metrics` 可配置独立 Bearer Token。认证成功后只把最小身份写入 Go Context。
- Chat Handler 在请求结构校验后查询 `(tenant_id, gateway_model)`，未授权模型不会进入 Provider。
- 客户端 Model-Velo Key 不向 Provider 传播，Provider Adapter 继续只使用独立的上游凭证。

### 限流

- Redis 基础 Client 使用官方 `go-redis/v9`，显式配置地址、认证、DB、拨号/读写/池等待超时、池容量和最小空闲连接。
- 启动 `required/optional` 只控制首次 Ping 失败是否阻止 API 监听；运行时限流失败默认 fail-closed 返回 503，也可配置 fail-open 绕过并标记响应。
- 当前核心配额是每个环境、租户和规范化模型的固定窗口请求数；同租户下的多个 API Key 共享额度，未认证 IP 级保护不在本切片范围。
- Key 使用 `model-velo:rate-limit:v1:<environment>:tenant:<tenant_sha256>:model:<model_sha256>`，不包含原始 API Key、Provider Key 或原始模型名。
- Redis Lua 在一次原子执行中完成读取、首次计数、递增、TTL、服务端时间和决策返回；拒绝请求不递增、不续期，避免应用侧“先 GET 再 SET”的竞态。
- 允许/拒绝结果包含上限、剩余量和 Unix 秒重置时间；429 额外返回向上取整且至少为 1 秒的 `Retry-After`。
- 请求 Context 直接传入 go-redis 脚本调用；客户端取消不会被 fail-open 转换成放行。
- 真实 Redis 测试使用随机环境 namespace 定向清理；已验证窗口耗尽/恢复、租户/模型隔离，以及两个独立 Client 下 200 个并发请求竞争 25 个额度时恰好放行 25 个。race detector 仍因本机缺少 CGO/GCC 未完成。
- Provider 容量限流与客户端配额限流是两个概念：前者由每个进程内、每个 Provider 独立的有界 Queue 控制，后者由 Redis Rate Limiter 跨实例控制。

### 响应缓存

当前 Cache Key schema 为 `model-velo:response-cache:v1:<environment>:tenant:<tenant_sha256>:model:<model_sha256>:route:<route_version_sha256>:request:<canonical_request_sha256>`。整个请求 JSON 被递归规范化：对象字段排序，数组顺序和数字文本保留，缺失字段不与显式默认值合并，重复字段直接 bypass。原始 API Key、tenant、模型和提示词不进入 Key。

只缓存通过认证、授权和限流的非流式请求及其完整成功响应；`Cache-Control: no-store` 显式 bypass。缓存位于限流之后，因此命中也消耗配额。流中断、客户端取消、上游错误和非法响应不写缓存。缓存不可用时固定 fail-open，记录安全错误并继续上游；Context 取消仍直接传播。`HIT/MISS/BYPASS` 同时存在于内部结果、HTTP Header 和最终 Usage Event。

当前调用链为认证→授权→限流→按请求能力过滤 Route Plan→缓存→Fallback Orchestrator 建立 Provider 执行总预算→按序调用单候选 Attempt Executor。每次真正的上游调用都按 Provider ID 重新进入 Adapter/Breaker→Queue→Key Selector→厂商协议端点→Key/Breaker 反馈→Queue 释放；Queue、Breaker 或 Key 冷却等调用前准入等待不占用 `MaxAttempts`，只有策略允许时才在资源释放后用 Timer/select 退避。Chat 请求在 HTTP 边界只解析一次；兼容协议保留原始 JSON 和未知字段，原生协议在转换前明确拒绝无法表达的字段。Adapter Registry 根据路由显式声明的 `vendor`/`type` 构造实现：OpenAI、Mistral、DeepSeek、xAI、Zhipu、Groq、NVIDIA、Together 各自拥有厂商 Adapter，并复用它们共同采用的 OpenAI Chat wire codec 与 HTTP 安全边界；custom-compatible 使用独立的通用兼容 Adapter。Anthropic、Gemini、DashScope、Cohere、Ollama、Bedrock 和 Cloudflare 执行显式请求/响应转换，Azure 使用自己的 Endpoint 与 `api-key` 鉴权。Adapter 不保存默认 API Key，每次调用只接收 Key Selector 为本 attempt 选出的 Secret。Adapter 能力不匹配会跳过当前候选，不消耗 Retry、不污染 Breaker；401 永久停用无效 Key，403 仅在当前请求排除该 Key，429 按 `Retry-After` 冷却并优先选择其他 Key。网络、上游超时与 500/502/503/504 退避重试并优先保持原 Key；普通 4xx 停止，带安全错误码的模型不可用 400/404/422 允许 Fallback。兼容 2xx 必须满足 Chat 响应结构，原生输出无法表示时明确 Fallback；真正的畸形协议响应会计入 Provider Breaker。Fallback 成功结果不写 Exact Cache。全部 Retry 与 Fallback 共享一个请求总预算，Provider 可独立覆盖单次超时、重试、Breaker、Queue 和 HTTP 连接池；连接池默认跟随 Queue 并发上限。Attempt Trail 记录实际 Provider 调用的 Provider/模型/Key ID、序号、类别、状态码和耗时，不记录 Secret 或提示词，并在成功结果与最终 Failure 中保留，供 Usage Collector 记录可靠性元数据。Queue、Breaker 和 Key 状态都按 Provider ID 隔离；Queue 是进程内容量保护，多实例总容量由每实例配置相加，不提供分布式全局并发上限。总预算取消和 race 证据留在阶段门禁。

### Usage Stream

Usage Event schema v2 在 v1 生命周期字段之外增加 API Key ID、usage 来源、input/output token 明细、最大 64 KiB 的原始 usage 子对象、finish reason 和流式 TTFT。Worker 仍能消费遗留 schema v1 事件，并推断其 usage 来源；未知未来版本进入 poison/dead-letter 流程。缺少上游 usage 时数据库 token 列保持 `NULL`，不会用零冒充已知值。终态包括 `success`、`cache_hit`、`failed`、`cancelled`、`stream_completed` 和 `stream_interrupted`；Collector 保证一个请求至多 finalize 一次。

OpenAI-compatible 流请求默认合并 `stream_options.include_usage=true`，Collector 检查所有已验证 SSE 事件：首个 choices Chunk 记录 TTFT，末尾 usage-only Chunk 更新 token，`[DONE]` 只决定完成终态。兼容与原生 Adapter 会把可获得的 cached read/write、audio、image、reasoning 和 prediction token 映射到统一明细。缓存命中保留响应中的逻辑 token，但 usage 来源是 `cache_replay`，本次实际上游成本固定为已知零。

成本不使用二进制浮点。配置中的 USD/百万 token 十进制字符串在启动时转换为整数 nanoUSD，按 Provider、模型、版本和半开生效时间窗口查价；上游明确返回的 USD 成本优先，否则使用本地价目。每条数据库记录保存 input/output/total 成本、来源和价格版本。缺少 usage、找不到价格或溢出时成本列保持 `NULL` 并保存稳定 caveat；Retry/Fallback 后只有最终响应 usage 可见时明确标记成本未覆盖更早失败 attempt。管理命令可在有界时间、过滤条件和显式确认下重算历史成本。

可靠性语义是 **at-least-once**：

1. API 在调用 Provider 前写 `usage_outbox` pending 生命周期；
2. 请求结束后在 PostgreSQL 固化完整 ready Event，再尝试 `XADD`；
3. Worker 既 relay ready outbox，也使用 consumer group 读取 Stream；
4. PostgreSQL 以事件 ID 唯一约束完成幂等 INSERT，且与 outbox 删除处于同一事务；
5. 数据库事务成功后再 `XACK + XDEL`；
6. Worker 能处理 pending、重试、毒消息和 stale 生命周期，但不宣称 exactly-once。

API 的即时 `XADD` 失败不改变已完成响应，因为 ready outbox 已可恢复。超过安全时间仍为 pending 的生命周期会转成 `request_lifecycle_interrupted`，Token/成本保持未知并附 caveat。主 Stream 不使用会裁掉 pending 的长度 trimming：数据库故障期间不会为了固定长度静默删除未处理事件。Worker 以 `XGROUP CREATE ... MKSTREAM` 幂等创建消费组，先用 `XAUTOCLAIM` 恢复超过 idle 阈值的 pending，再用有界 `XREADGROUP BLOCK` 读取新事件。未知版本或坏事件不会写库，达到投递阈值后在事务中写入有独立长度上限的 dead-letter Stream，并从主流 ACK/删除。

`usage_events.event_id` 是主键。Worker 使用 `ON CONFLICT (event_id) DO NOTHING`，因此数据库提交成功但 Redis 事务未执行时，下一次消费只会命中幂等行，再执行 ACK/删除；若事务已执行但响应丢失，数据库行已经是最终防线。查询索引覆盖 tenant+时间、tenant+模型+时间、tenant+Provider+时间、API Key+时间、request、状态和成本。认证后的 `/v1/usage/events|summary|series` 同时强制注入当前 tenant 与 API Key 条件，并限制时间窗口、分页、分组数量、字段和值；在项目拥有明确管理员 Scope 前，不允许普通模型 Key 跨 Key 读取租户账单。Worker 收到退出信号后停止新读取，当前批次使用独立有界时间完成；它还按配置保留期持续分批删除过期行，`0` 明确表示禁用自动清理。

## 10. 分阶段演进

### 快速交付原则

后续阶段优先形成可运行的生产纵向链，不以测试文件数量衡量工程质量。每个纵向功能只在安全、并发、持久化或协议边界选择最贴近风险的一层验证，通常最多维护一个合并测试文件；完整 race、真实依赖和故障矩阵集中到阶段收口执行一次。测试类 TODO 是证据槽位，不是独立开发里程碑，用户也不需要把测试代码作为学习主线。

```mermaid
flowchart LR
    M1["阶段 1<br/>Gin + 单 Provider<br/>非流式最小链路"] --> M2["阶段 2<br/>PostgreSQL 鉴权<br/>Redis 限流与缓存"]
    M2 --> M3["阶段 3<br/>Router + Breaker + Queue<br/>Attempt + Retry + Fallback"]
    M3 --> M4["阶段 4<br/>SSE + 首 Chunk 边界<br/>客户端取消"]
    M4 --> M5["阶段 5<br/>Redis Stream<br/>Usage Worker → PostgreSQL"]
    M5 --> M6["阶段 6<br/>Metrics / CI<br/>Benchmark / 文档"]
```

每个阶段必须满足：

- 有可以运行的入口；
- 有自动化测试；
- 能演示正常链路和至少一个异常链路；
- 文档只描述已经实现的能力；
- 通过 `AGENTS.md` 规定的代码独立性检查；
- 用户确认后才进入下一阶段。

## 11. 第一阶段验收标准

开始写代码后的第一个里程碑只要求：

1. `GET /healthz` 返回稳定健康响应；
2. `POST /v1/chat/completions` 能校验最小 OpenAI Chat 请求；
3. 请求转发到一个可配置的 OpenAI-compatible 假上游或真实上游；
4. 正确返回非流式 JSON；
5. 上游超时、4xx、5xx 能转换成稳定错误；
6. request context 能取消上游请求；
7. 测试使用 `httptest.Server`，不需要 Redis、PostgreSQL 或付费 API；
8. `go test ./...` 和 `go vet ./...` 通过。

第一阶段完成后再引入 Redis 和 PostgreSQL，避免同时调试 HTTP 转发、数据库、缓存和并发控制。
