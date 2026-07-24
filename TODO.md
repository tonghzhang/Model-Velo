# Model-Velo 全量实施 TODO

> 状态基线：2026-07-23。项目已在预发布阶段统一更名为 `Model-Velo`；本文根据 `AGENTS.md` 与 `ARCHITECTURE.md` 拆解，是当前规划范围的可执行账本，不是对未来所有需求的永久封闭承诺。实现中发现新的真实工作时，应新增任务；不得为了维持完成率隐藏工作。

## 1. 使用规则

- `[x]`：已有代码、测试或文档证据，并完成与范围匹配的验证。
- `[ ]`：尚未完成，或虽然有部分代码但还没有达到本项的完成证据。
- ID 用于稳定引用，不表示必须机械地按编号执行；同一阶段内应按依赖关系和用户确认选择下一个纵向切片。
- 每次交付后同步勾选项和顶部统计，让摘要始终反映仓库证据，而不是主观估计。
- 每次只选择一个纵向切片实施；不要因为本文列出了未来目录或模块，就提前创建空接口和空包。
- 一个任务只有在“实施内容”和“完成证据”都满足后才能勾选；README 宣称、编译通过或手工试过一次不能替代测试证据。
- TODO 是范围账本，不是要求按 ID 逐项停工验收的流水线；同一纵向切片可以一次完成多个相邻 ID。
- 测试类 ID 默认属于阶段门禁：功能开发时只补保护核心行为的最小测试，完整并发、race、故障矩阵和资源观测集中到阶段末执行。
- 一个代表性测试可以同时作为多个相关 ID 的证据；禁止为了增加完成数在 Handler、Service、集成层重复验证同一控制流。
- **当前启用快速交付模式**：测试/验证/race 类 ID 只保留为证据槽位，不作为独立的“下一步”；下一步必须优先选择用户已授权阶段中的生产功能。
- 一个纵向切片通常最多修改一个合并测试文件；已有用例可扩展时不新建文件，已有端到端证据可覆盖时不补低层重复测试。
- 不追求覆盖率百分比，不为普通 getter、配置搬运、简单错误映射或文档变更新增测试。
- 如果已授权阶段没有剩余生产功能，只剩测试环境门禁，则记录未完成原因并等待用户授权下一阶段，不继续堆测试代码。
- 某个测试门禁被 GCC、Docker 或本机权限阻止时，保持该 ID 未勾选并记录原因；经用户允许后，不阻塞同阶段内与它独立的下一个纵向功能。
- 每个阶段结束前必须执行阶段门禁：正常链路、异常链路、格式化、测试、静态检查、必要的 race/integration test、代码独立性检查、文档同步和用户确认。
- 任务数量不等于代码行数，也不应直接当作工期百分比。Circuit Breaker、SSE 提交边界、Redis/PostgreSQL 故障语义等任务的风险远高于普通配置项。
- 如果某项因设计变化不再需要，应先在架构或决策记录中说明原因，再修改本文；不要直接删除未完成项来制造进度。

## 2. 当前进度摘要

| 阶段 | 完成/总数 | 状态 | 说明 |
|---|---:|---:|---|
| S0 项目治理与基线 | 10/15 | 进行中 | README、架构现状和密钥基线已同步，Git 与治理模板仍待补齐 |
| S1 最小非流式网关 | 48/50 | 进行中 | 功能检查通过且用户已授权进入 S2；race 仍被本机缺少 GCC 阻止，可解释提交尚未形成 |
| S2 PostgreSQL、鉴权、Redis 限流与缓存 | 58/60 | 进行中 | 生产功能与真实依赖门禁已完成；用户已允许带着 race 环境缺口进入 S3，缺口仍保留 |
| S3 路由与可靠性 | 86/90 | 进行中 | 生产链与非 race 门禁已完成；用户已允许带着 3 个 race 环境证据缺口进入 S4，缺口继续保留 |
| S4 SSE | 33/35 | 进行中 | 生产边界、长流写超时、事件空闲保护与攻击矩阵已完成；race 和阶段门禁待收口 |
| S5 Usage 数据链路 | 50/52 | 进行中 | Usage v2 生产链和真实依赖集成已完成；race 与最终门禁受本机缺少 GCC 阻塞 |
| S6 工程化与求职展示 | 0/75 | 未开始 | 可观测性、CI、benchmark、故障报告和展示材料尚未实现 |
| **合计** | **278/370** | **75.1%** | 百分比只表示任务数量，不表示风险或工期完成度 |

## 3. S0：项目治理、仓库基线与独立性约束

- [x] `S0-001` **建立开发规则** — 实施：在 `AGENTS.md` 固化阶段范围、KISS、测试、Git、安全与参考项目只读约束；完成证据：规则文件可被开发者和 AI 直接读取。
- [x] `S0-002` **建立架构规划稿** — 实施：在 `ARCHITECTURE.md` 描述部署、核心请求链路、模块边界、错误策略与阶段演进；完成证据：关键链路有图和文字说明。
- [x] `S0-003` **定义代码独立性口径** — 实施：明确字面重复度、归一化近似重复度、排除范围、阈值与人工复核要求；完成证据：两项指标均要求严格小于 10%。
- [x] `S0-004` **获得阶段 1 启动授权** — 实施：由用户明确宣布开始阶段 1；完成证据：允许创建 Go module 和源码，且未自动进入阶段 2。
- [x] `S0-005` **建立全量 TODO 账本** — 实施：按阶段、稳定编号、完成证据拆解项目；完成证据：`TODO.md` 能反映当前真实完成状态。
- [ ] `S0-006` **初始化 Git 仓库** — 实施：确认仓库根目录后执行 Git 初始化并设置主分支；完成证据：`git status` 可用且只显示预期文件，不自动提交。
- [ ] `S0-007` **建立 `.gitignore`** — 实施：排除 `.env`、二进制、覆盖率、benchmark 临时文件、IDE、本地数据库与运行日志；完成证据：密钥和构建产物不会进入 `git status`。
- [x] `S0-008` **建立安全的环境变量示例** — 实施：创建 `.env.example`，只放变量名和无效示例值；完成证据：不包含 API Key、Provider Key 或真实连接串。
- [x] `S0-009` **建立 README 状态骨架** — 实施：只记录当前已实现能力、运行前提和阶段状态；完成证据：不存在 Redis、Fallback、SSE 等未实现能力的虚假宣称。
- [x] `S0-010` **记录本地开发命令** — 实施：说明格式化、测试、vet、race、运行和环境检查命令；完成证据：新终端可以按文档复现当前验证。
- [ ] `S0-011` **建立里程碑变更记录模板** — 实施：记录目标、非目标、调用链、修改文件、测试结果和未覆盖边界；完成证据：每个阶段可按同一格式复盘。
- [ ] `S0-012` **建立参考项目研究日志** — 实施：只记录查看过的文件/函数、借鉴概念和 Model-Velo 独立取舍，不粘贴源码；完成证据：每次参考行为可追溯。
- [ ] `S0-013` **建立阶段门禁模板** — 实施：把运行入口、正常/异常演示、测试、独立性、文档和用户确认列为必检项；完成证据：任何阶段不能只凭功能代码进入下一阶段。
- [x] `S0-014` **执行仓库密钥基线扫描** — 实施：扫描现有文件中的 Token、Authorization、数据库密码和私钥模式；完成证据：报告命令与结果，发现项被清理或解释。
- [x] `S0-015` **同步架构文档当前状态** — 实施：把“当前还没有实现代码”更新为真实进度，并标明规划模块不等于已实现模块；完成证据：`ARCHITECTURE.md` 与仓库现状一致。

## 4. S1：Gin + 单一 OpenAI-compatible 上游的最小非流式网关

### 4.1 已完成的 HTTP 基线

- [x] `S1-001` **初始化 Go module** — 实施：创建 `go.mod` 并使用独立模块名；完成证据：`go list ./...` 能发现 Model-Velo 包。
- [x] `S1-002` **固定 Gin 直接依赖** — 实施：在 `go.mod` 固定 Gin 版本并生成 `go.sum`；完成证据：依赖可重复解析且没有受污染标准库引入的 GORM。
- [x] `S1-003` **建立 API 进程入口** — 实施：创建 `cmd/model-velo/main.go`；完成证据：包为 `main` 且能构建可执行程序。
- [x] `S1-004` **建立 HTTP Router 组合点** — 实施：创建 `internal/httpapi.NewRouter` 并让入口只依赖 Router；完成证据：路由细节不写入 `main`。
- [x] `S1-005` **启用 panic 恢复中间件** — 实施：在 Gin Engine 注册 `gin.Recovery()`；完成证据：Handler panic 不直接终止进程。
- [x] `S1-006` **实现 `GET /healthz`** — 实施：返回固定 `200` 和 `{"status":"ok"}`；完成证据：无数据库或上游依赖即可响应。
- [x] `S1-007` **为 `healthz` 编写黑盒 HTTP 测试** — 实施：用 `httptest` 验证状态码、Content-Type 和响应体；完成证据：测试不监听真实端口。
- [x] `S1-008` **支持监听地址环境变量** — 实施：读取 `MODEL_VELO_HTTP_ADDR`，空值回退到 `:8080`；完成证据：默认、空白和自定义地址均有测试。
- [x] `S1-009` **改用显式 `http.Server`** — 实施：配置 ReadHeader、Read、Write 和 Idle Timeout；完成证据：不再依赖 `gin.Engine.Run` 的隐式 Server。
- [x] `S1-010` **测试 HTTP Server 默认配置** — 实施：验证 Handler 非空及四项超时；完成证据：配置回归会导致测试失败。
- [x] `S1-011` **验证当前 HTTP 基线** — 实施：执行 `gofmt`、`go test ./...`、`go vet ./...`；完成证据：2026-07-15 三项均通过。

### 4.2 进程生命周期与边缘请求约束

- [x] `S1-012` **设计可测试的 Server 运行函数** — 实施：把阻塞启动和退出路径从 `main` 中分离，但不引入通用应用框架；完成证据：启动失败和正常关闭可测试。
- [x] `S1-013` **接入操作系统退出信号** — 实施：使用 `signal.NotifyContext` 监听 Ctrl+C/终止信号；完成证据：信号能取消应用根 Context。
- [x] `S1-014` **实现有界优雅关闭** — 实施：调用 `http.Server.Shutdown` 并设置独立关闭超时；完成证据：已有连接获得退出窗口，超时后进程不会无限等待。
- [x] `S1-015` **测试 Server 启动和关闭路径** — 实施：覆盖监听失败、Context 取消、`http.ErrServerClosed` 和 Shutdown 错误；完成证据：测试不依赖固定端口且不泄漏 goroutine。
- [x] `S1-016` **限制请求体大小** — 实施：在 JSON 解码前设置明确 Body 上限；完成证据：超限请求得到稳定 4xx，Handler 不继续读取无限数据。
- [x] `S1-017` **校验请求 Content-Type** — 实施：聊天接口只接受兼容的 JSON Content-Type；完成证据：缺失、合法带 charset、非法类型均有测试。
- [x] `S1-018` **生成 request ID** — 实施：为未携带 ID 的请求生成不可预测且可记录的 ID；完成证据：每个响应都带稳定的 request ID Header。
- [x] `S1-019` **接收并规范化外部 request ID** — 实施：为客户端 ID 设置长度和字符约束，非法值改为新 ID；完成证据：不能通过 Header 注入日志控制字符。
- [x] `S1-020` **把 request ID 放入 Context** — 实施：由中间件写入 Gin/Go Context，后续 Handler 和 Provider 共用；完成证据：上游请求与错误响应能读取同一个 ID。
- [x] `S1-021` **测试 request ID 中间件** — 实施：覆盖生成、合法透传、非法替换、响应 Header 和并发唯一性；完成证据：测试不依赖全局可变随机状态。

### 4.3 OpenAI Chat 请求与结构化错误

- [x] `S1-022` **定义阶段 1 支持的 Chat 字段范围** — 实施：明确 `model`、`messages`、`stream` 以及允许透传的生成参数；完成证据：文档区分已支持字段和暂未保证字段。
- [x] `S1-023` **定义最小 Chat 请求类型** — 实施：用 Model-Velo 自己的命名表达模型、消息、角色和内容；完成证据：类型不复制参考项目结构且能承载验收请求。
- [x] `S1-024` **定义非流式 Chat 响应类型或透传边界** — 实施：决定哪些字段由网关校验、哪些字段保持上游兼容；完成证据：不会因过窄结构静默丢失上游字段。
- [x] `S1-025` **实现 JSON 解码错误识别** — 实施：区分空 Body、截断 JSON、类型不匹配和多个 JSON 值；完成证据：各类错误返回稳定 400 而非 Gin 默认文本。
- [x] `S1-026` **校验必填 `model`** — 实施：拒绝空值和纯空白模型名；完成证据：错误指向 `model` 且不调用上游。
- [x] `S1-027` **校验非空 `messages`** — 实施：拒绝缺失或空消息数组；完成证据：错误指向 `messages` 且不调用上游。
- [x] `S1-028` **校验消息角色和最小内容** — 实施：接受阶段 1 明确支持的 role/content 组合，拒绝不可解释值；完成证据：表驱动测试覆盖 system/user/assistant 和非法角色。
- [x] `S1-029` **明确拒绝 `stream=true`** — 实施：在尚未支持 SSE 时返回 OpenAI-compatible “不支持流式”错误；完成证据：请求不会到达上游，状态码和错误 code 固定。
- [x] `S1-030` **定义统一错误响应结构** — 实施：至少包含 message、type、param、code，并关联 request ID；完成证据：本地和上游错误使用同一 JSON 外壳。
- [x] `S1-031` **建立错误写出函数** — 实施：集中设置 Content-Type、状态码和错误 JSON，防止 Handler 重复拼装；完成证据：写出后不会二次写 Header。
- [x] `S1-032` **测试 Chat 协议与错误响应** — 实施：覆盖合法最小请求、所有本地校验错误和 `stream=true`；完成证据：非法请求的假上游调用次数为零。

### 4.4 单一上游配置与 Provider 调用

- [x] `S1-033` **定义单上游环境变量** — 实施：配置 Base URL、Provider API Key、上游模型策略和调用超时；完成证据：变量名写入 `.env.example` 且没有真实密钥。
- [x] `S1-034` **实现启动时配置加载** — 实施：集中读取和裁剪环境变量，区分必填项与默认值；完成证据：配置错误在监听端口前失败。
- [x] `S1-035` **校验上游 URL** — 实施：只接受明确支持的 http/https URL，规范尾部路径但不错误拼接；完成证据：非法 scheme、缺 host 和路径边界有测试。
- [x] `S1-036` **保护 Provider Key** — 实施：Key 只进入 Authorization Header，不进入错误、日志和测试快照；完成证据：失败路径扫描不到真实 Key。
- [x] `S1-037` **构造专用上游 HTTP Client** — 实施：配置总调用超时和合理 Transport，不使用无超时默认 Client；完成证据：超时值可由测试控制。
- [x] `S1-038` **实现单 Provider Adapter** — 实施：只负责 OpenAI-compatible 请求构造、HTTP 调用和响应读取；完成证据：Adapter 不包含 Retry、Fallback、SQL 或 Redis。
- [x] `S1-039` **正确拼接 Chat Completions 路径** — 实施：Base URL 与 `/v1/chat/completions` 组合不产生双斜杠或路径丢失；完成证据：带/不带尾斜杠均有测试。
- [x] `S1-040` **构造上游请求 Header** — 实施：设置 JSON Content-Type、Bearer Key 和 request ID，不盲目复制客户端敏感 Header；完成证据：假上游断言 Header 精确符合预期。
- [x] `S1-041` **传播客户端 Context** — 实施：使用 `NewRequestWithContext` 让取消和 deadline 到达上游；完成证据：客户端取消会结束假上游阻塞请求。
- [x] `S1-042` **安全读取上游响应** — 实施：始终关闭 Body，设置响应体上限并处理读取错误；完成证据：超大或中断响应不会无限占用内存/连接。
- [x] `S1-043` **返回成功的非流式 JSON** — 实施：仅在上游 2xx 且响应满足阶段 1 边界时回写；完成证据：客户端收到兼容 JSON，关键字段不丢失。
- [x] `S1-044` **转换上游 4xx** — 实施：保留可安全表达的语义并转换为稳定网关错误；完成证据：不泄露上游 Key、内部 URL 或任意 HTML。
- [x] `S1-045` **转换上游 5xx** — 实施：映射为稳定 502/503 类错误并保留 request ID；完成证据：不同上游错误体不会改变公共错误外壳。
- [x] `S1-046` **区分网络、超时与客户端取消** — 实施：使用 `errors.Is`/Context 状态分类；完成证据：超时、DNS/连接失败和客户端取消得到不同内部类别。

### 4.5 假上游、验证与阶段门禁

- [x] `S1-047` **建立成功假上游测试** — 实施：用 `httptest.Server` 断言路径、Header、请求 JSON 并返回固定 Chat 响应；完成证据：完整客户端→网关→假上游→客户端链路通过。
- [x] `S1-048` **建立上游异常矩阵测试** — 实施：覆盖 400、401/403、429、500/502/503/504、非 JSON、超大 Body 和提前断开；完成证据：每个场景有稳定状态码和错误 code。
- [ ] `S1-049` **完成阶段 1 门禁并等待用户确认** — 实施：运行 gofmt、test、vet、必要的取消/泄漏检查，演示正常与异常链路，完成独立性自动与人工检查并同步文档；完成证据：所有验收项有真实记录，用户明确允许进入 S2。
- [ ] `S1-050` **形成阶段 1 可解释提交** — 实施：按 Conventional Commits 拆分并提交最小网关变化；完成证据：提交不含密钥/构建产物，能回答调用链、失败处理和未采用复杂抽象的原因。

## 5. S2：PostgreSQL 鉴权、Redis 分布式限流与 Exact Cache

### 5.1 本地基础设施与配置

- [x] `S2-001` **确认阶段 2 目标与非目标** — 实施：只引入 PostgreSQL 鉴权、Redis 限流和 exact cache，不提前实现 Retry/Fallback/SSE；完成证据：用户已确认进入 S2。
- [x] `S2-002` **定义 Compose 服务拓扑** — 实施：只包含 API 开发依赖的 PostgreSQL、Redis 及必要网络/卷；完成证据：不加入与当前范围无关的管理平台。
- [x] `S2-003` **固定 PostgreSQL 镜像版本** — 实施：使用明确版本而非浮动 latest；完成证据：不同机器拉取同一主/次版本。
- [x] `S2-004` **固定 Redis 镜像版本** — 实施：使用明确版本并启用项目所需持久化/启动参数；完成证据：版本与命令写入 Compose。
- [x] `S2-005` **配置基础设施健康检查** — 实施：为 PostgreSQL 和 Redis 设置健康命令、间隔、超时和重试；完成证据：依赖未就绪时状态明确。
- [x] `S2-006` **隔离本地持久化卷** — 实施：PostgreSQL/Redis 使用独立命名卷并记录保留与清理命令；完成证据：2026-07-18 唯一 Compose project 在 `down/up` 后两类标记均保留，`down --volumes` 后两个隔离卷均删除，未触碰默认项目卷。
- [x] `S2-007` **补充基础设施环境变量示例** — 实施：增加数据库 DSN、Redis 地址、密码/DB 和超时示例；完成证据：示例值不可用于真实环境。
- [x] `S2-008` **实现 PostgreSQL 配置加载与校验** — 实施：解析 DSN、连接池和连接超时；完成证据：缺失/非法配置在启动时给出不泄密错误。
- [x] `S2-009` **实现 Redis 配置加载与校验** — 实施：解析地址、认证、DB、拨号/读写超时和故障策略；完成证据：配置值有边界测试。
- [x] `S2-010` **编写 Compose 启停教程** — 实施：记录启动、查看健康、日志、停止与清理数据命令；完成证据：空机器按步骤可建立依赖。

### 5.2 GORM PostgreSQL 连接与 Schema

- [x] `S2-011` **选择并固定 GORM PostgreSQL 依赖** — 实施：固定 GORM 与官方 PostgreSQL Dialector，业务代码不直接调用 pgx；完成证据：`go.mod` 只把 GORM 两个模块列为 PostgreSQL 直接依赖。
- [x] `S2-012` **构造 PostgreSQL 连接池** — 实施：通过 GORM 获取 `database/sql`，设置最大打开连接、最大空闲连接、连接寿命和空闲时间；完成证据：配置有合理默认值并在启动时应用。
- [x] `S2-013` **实现启动 Ping** — 实施：在有界 Context 中验证数据库连通性；完成证据：不可达时启动失败但 DSN 不出现在日志。
- [x] `S2-014` **实现连接池关闭** — 实施：接入应用退出路径；完成证据：正常关闭释放连接且不阻塞无限时间。
- [x] `S2-015` **采用 GORM AutoMigrate** — 实施：API 在 PostgreSQL Ping 成功后同步当前模型；完成证据：启动调用链明确，不再存在独立 migration CLI、版本表和 SQL 文件。
- [x] `S2-016` **定义租户 GORM 模型** — 实施：定义稳定租户 ID、状态、创建/更新时间；完成证据：模型标签包含唯一标识、状态索引和检查约束。
- [x] `S2-017` **定义 API Key GORM 模型** — 实施：存储 Key ID、前缀/查找标识、哈希、租户、状态、过期时间和审计时间；完成证据：模型不存在明文 Key 字段。
- [x] `S2-018` **定义模型授权 GORM 模型** — 实施：表达租户允许访问的网关模型；完成证据：组合主键避免重复授权。
- [x] `S2-019` **用 GORM 标签建立索引与外键** — 实施：围绕 Key 查找、租户状态和模型授权定义索引及删除策略；完成证据：模型标签明确 RESTRICT/CASCADE 语义。
- [x] `S2-020` **验证 AutoMigrate 与约束** — 实施：在随机 schema 中执行两次 AutoMigrate 并检查表、索引、外键和检查约束；完成证据：PostgreSQL 17.10 一次性容器真实通过，schema 定向删除且容器无持久卷自动清理。
- [x] `S2-021` **明确破坏性 Schema 变更边界** — 实施：记录 AutoMigrate 不负责删列、回退或存量数据改写，复杂变更需要备份和显式升级方案；完成证据：`ARCHITECTURE.md` 和 `README.md` 与代码一致。

### 5.3 API Key 生命周期与认证

- [x] `S2-022` **定义 Model-Velo API Key 格式** — 实施：使用 `mvl_<locator>_<secret>`，包含可识别命名空间和 44 字节总随机源；完成证据：格式、长度、数据库字段与展示规则已写入 README/架构。
- [x] `S2-023` **实现安全随机 Key 生成** — 实施：`crypto/rand` 生成 locator、secret 和 UUID，失败时不降级；完成证据：各 4096 个随机 Key 和 UUID 均无重复，Key 通过格式、摘要和 HMAC 往返校验，UUID 符合 RFC 4122 v4，固定向量证明 Base64URL 内容包含 `_` 时仍可解析。
- [x] `S2-024` **实现不可逆 Key 哈希** — 实施：locator 使用 SHA-256 查找，secret 使用 Pepper HMAC-SHA-256 校验并记录版本；完成证据：认证无需恢复原始 Key，数据库不保存可直接使用的凭证。
- [x] `S2-025` **只在创建时返回明文 Key** — 实施：管理命令仅在事务成功后向标准输出展示一次，持久化模型只有 prefix/digest/hash；完成证据：查询和状态变更路径不包含明文 Key 字段。
- [x] `S2-026` **实现 Key 创建存储操作** — 实施：事务写入 Key 与租户关系；完成证据：真实 PostgreSQL 验证租户初始化、已有租户增发、重复 slug 和不存在租户语义。
- [x] `S2-027` **实现 Key 哈希查找操作** — 实施：使用唯一摘要索引定位并用常量时间 HMAC 比较；完成证据：真实 PostgreSQL 对照未知 locator 与正确 locator/错误 secret，均返回无效凭证。
- [x] `S2-028` **实现 Key 吊销与禁用** — 实施：更新状态、吊销时间和更新时间；完成证据：真实 PostgreSQL 在状态变更后立即拒绝认证，并验证永久吊销不能降级为禁用。
- [x] `S2-029` **实现 Key 过期判断** — 实施：Manager 注入时钟，创建与认证共享“到期时刻即失效”的判断并按 UTC 规范化；完成证据：固定时钟覆盖无期限、过去、恰好到期和不同时区的未来时间。
- [x] `S2-030` **建立受控 Key 初始化工具** — 实施：新增 `model-velo-admin`，支持租户初始化、Key 增发、禁用和吊销；完成证据：明文仅输出到当前标准输出，不写数据库、日志文件或脚本。
- [x] `S2-031` **解析 Bearer Authorization** — 实施：严格处理缺失、重复、错误 scheme、空 token、额外空白和非法分隔；完成证据：表驱动 Header 矩阵通过，Bearer scheme 大小写不敏感但 token 内容不被改写。
- [x] `S2-032` **实现认证中间件** — 实施：查询 Key、统一隐藏无效/禁用/吊销/过期/租户禁用状态并把最小身份写入 Go/Gin Context；完成证据：所有认证失败和数据库故障均未进入下游 Handler，成功路径的两个 Context 身份一致。
- [x] `S2-033` **定义认证错误响应** — 实施：401/403/503 使用统一错误外壳、request ID 和安全错误码；完成证据：无效、禁用、吊销、过期和禁用租户统一隐藏内部状态，数据库故障不泄露细节。
- [x] `S2-034` **实现模型级授权** — 实施：Chat 校验后查询租户和模型的组合授权；完成证据：可观察调用链测试证明未授权请求停在授权层，不进入 Limiter、Cache 或 Provider。
- [x] `S2-035` **测试租户身份 Context** — 实施：验证授权、Limiter、Cache 取得同一个认证 tenant ID；完成证据：可观察调用链测试逐层断言身份值，不通过全局变量传播。
- [x] `S2-036` **建立认证 PostgreSQL 集成测试** — 实施：一个综合用例覆盖有效、未知、错误 secret、禁用、吊销、过期、禁用租户和模型未授权；完成证据：PostgreSQL 17.10 随机 schema 真实通过并定向清理，可重复运行且不使用开发者常驻数据。

### 5.4 Redis 客户端与分布式限流

- [x] `S2-037` **选择并固定 Redis Client** — 实施：使用官方 `go-redis/v9 v9.21.0`，只调用单机 Client、Ping 和 Close；完成证据：直接依赖已锁定，未提前引入 Cluster、脚本或业务封装。
- [x] `S2-038` **构造 Redis Client 并启动 Ping** — 实施：配置连接池、拨号/读写/池等待超时，并按 required/optional 执行有界 Ping；完成证据：2026-07-17 在 Redis 8.8.0 容器验证成功 Ping，且不可达地址下 required 失败、optional 保留可恢复 Client。
- [x] `S2-039` **关闭 Redis Client** — 实施：幂等 Close 已接入应用退出；完成证据：真实 Client 连续 Close 两次均成功，Close 后 Ping 明确失败。
- [x] `S2-040` **定义客户端配额模型** — 实施：按环境、租户和规范化模型执行固定窗口请求配额，配置请求上限、`1s`–`24h` 窗口和边界；完成证据：配置单元测试与 README 口径一致。
- [x] `S2-041` **选择原子限流算法** — 实施：使用 Redis Lua 在脚本内完成读取、写入、递增、TTL 和服务端时间；完成证据：两个独立 go-redis Client 并发竞争同一 Key 时共享原子额度，没有应用侧 GET/SET 竞态。
- [x] `S2-042` **设计隔离的限流 Key** — 实施：Key 包含版本、环境及 tenant/model SHA-256，原始 API Key、tenant 和模型名均不入 Key；完成证据：Key 隔离与敏感输入测试通过。
- [x] `S2-043` **实现限流原子脚本** — 实施：一次执行返回允许、剩余额度、重置时间和 Retry-After，Go 侧校验数值；完成证据：真实 Redis 验证 2 次额度依次返回 remaining 1/0，第 3 次拒绝且窗口到期后恢复。
- [x] `S2-044` **实现限流 Middleware/Service 边界** — 实施：认证与模型授权后调用 Gin 无关的 Limiter，HTTP 层只映射决策；完成证据：429 测试证明拒绝后 Provider 调用数为零。
- [x] `S2-045` **明确 Redis 限流故障策略** — 实施：默认 fail-closed 返回 503，可选 fail-open 标记 bypass 后继续，取消始终传播；完成证据：配置、命令故障注入和取消测试通过，真实 Redis 断网仍留给集成门禁。
- [x] `S2-046` **返回标准限流响应 Header** — 实施：输出额度、剩余、Unix 秒重置和整数 Retry-After；完成证据：真实 Lua→Limiter→HTTP 测试逐次对照 Header，超额返回结构化 `429 rate_limit_exceeded` 且 Provider 不被调用。
- [x] `S2-047` **建立限流集成测试** — 实施：覆盖首次通过、耗尽、窗口恢复、租户隔离、不同模型和 Redis 命令故障；完成证据：Redis 8.8.0 容器用例与故障注入用例均通过，随机 namespace 定向清理且不使用 FLUSHDB。
- [ ] `S2-048` **完成限流并发 race 门禁** — 实施：两个独立 Client、200 个 goroutine 竞争 25 个额度时恰好成功 25 个；当前：普通真实 Redis 并发测试通过，但 `go test -race` 因 `CGO_ENABLED=0` 且缺少 GCC 无法运行。本项保留到阶段 2 门禁，不阻塞 Exact Cache 纵向切片。

### 5.5 Redis Exact Response Cache

- [x] `S2-049` **定义可缓存请求条件** — 实施：仅允许完成校验、认证、授权、限流的非流式请求进入 exact cache，`Cache-Control: no-store` 显式绕过；完成证据：Handler 在所有前置检查后才查询缓存，核心 HTTP 测试证明 no-store 不读写缓存。
- [x] `S2-050` **定义请求规范化规则** — 实施：递归排序 JSON 对象字段，保留数组顺序、数字文本及缺失/显式值差异，重复字段直接绕过；完成证据：字段重排同 Key，tenant/model/数字/路由差异不同 Key。
- [x] `S2-051` **设计租户隔离 Cache Key** — 实施：Key 包含 schema、环境、tenant/model 摘要、路由版本摘要和整个规范请求摘要；完成证据：测试证明原始提示词与 tenant 不出现在 Key。
- [x] `S2-052` **对 Cache Key 内容做稳定哈希** — 实施：规范表示及隔离维度使用 SHA-256 并带 `v1` schema；完成证据：固定 Key 向量测试可复现。
- [x] `S2-053` **实现 Cache TTL 配置** — 实施：默认 `5m`，允许 `1s`–`24h`，`0/off` 禁用，并校验路由版本；完成证据：默认、自定义、禁用和非法配置测试通过。
- [x] `S2-054` **实现 Cache Get 与故障降级** — 实施：命中返回完整合法 JSON；Redis 错误只记录 request ID 和安全错误并继续上游；完成证据：HTTP fake 故障测试返回成功 Provider 响应和 `BYPASS`。
- [x] `S2-055` **实现成功响应回填** — 实施：只保存 Provider Client 已确认完整合法的 2xx 非流式 JSON；完成证据：HTTP 核心测试证明 miss 回填、上游 5xx 不回填。
- [x] `S2-056` **表达缓存命中状态** — 实施：内部 `Result` 显式携带 `HIT/MISS/BYPASS`，HTTP 返回同名 Header；完成证据：核心测试分别验证三种状态，不依赖反向解析 Header 驱动业务。
- [x] `S2-057` **建立 Cache 集成测试** — 实施：真实 Redis 综合用例覆盖命中、未命中、TTL、租户隔离、参数差异、Redis 故障和错误不缓存；完成证据：Redis 8.8.0 一次性容器真实通过，假上游调用次数与 `HIT/MISS/BYPASS` 全部符合预期，容器已自动清理。
- [x] `S2-058` **验证认证→授权→限流→缓存顺序** — 实施：可观察 fake 逐段记录调用；完成证据：认证/授权/限流失败均在本层截断，命中顺序为认证→授权→限流→缓存，未命中再进入 Provider 和回填。
- [x] `S2-059` **同步阶段 2 文档与独立性检查** — 实施：文档记录数据结构、故障策略、缓存 Key 口径和参考概念；完成证据：`modelmux-clone-check 1.0` 按 12 行/80 token 阈值扫描 19 个生产 Go 文件、1763 条逻辑行，对 GoModel、Bifrost 及合并集的字面/近似重复度均为 0.00%，人工复核未发现独特命名、注释、错误文案或控制流复制。
- [ ] `S2-060` **完成阶段 2 门禁并等待用户确认** — 实施：单元、PostgreSQL/Redis 集成、gofmt、vet 和依赖故障门禁已通过；当前：`CGO_ENABLED=0` 且无 gcc/clang/zig，race 尚未执行，之后仍需用户明确允许进入 S3。

## 6. S3：有序路由、Circuit Breaker、Provider Queue、Key、Retry 与 Fallback

> 快速交付约束：先按 Router → Breaker → Queue → Key → Retry → Fallback 完成生产纵向链。下面分散列出的测试类 ID 不单独排期，统一复用少量合并用例；阶段 3 默认最多维护一个策略/状态测试文件和一个端到端故障文件，不为每个组件建立重复测试矩阵。

### 6.1 阶段契约、领域错误与配置

- [x] `S3-001` **确认阶段 3 目标与非目标** — 实施：实现非流式多候选可靠性，不提前输出 SSE 或启动 Usage Worker；完成证据：用户已明确允许进入 S3，本切片只接 Router 和当前 primary，不进入 SSE/Usage。
- [x] `S3-002` **定义 Provider 身份模型** — 实施：启动配置表达稳定 ID、显式协议、API Base 和可用模型；Router 只保留规划需要的 ID/模型集合；完成证据：运行时 Registry 和 Route Plan 使用稳定 ID，同一厂商可拥有多个实例。
- [x] `S3-003` **定义 Route Candidate** — 实施：Candidate 只表达 Provider ID、上游模型和稳定优先级；完成证据：协议归 Adapter Registry 所有，Candidate 不重复携带类型或 Provider Key。
- [x] `S3-004` **定义有序 Route Plan** — 实施：Plan 只包含请求模型和候选序列；租户身份留在鉴权/限流/缓存层，总 deadline 由 Orchestrator 持有；完成证据：顺序确定且不携带无效租户、协议或缓存版本字段。
- [x] `S3-005` **设计多 Provider 配置格式** — 实施：路由 JSON 表达任意数量的 Provider、厂商预设/API Base、多个模型、别名映射和有序候选，Key JSON 独立表达每 Provider 多 Key；完成证据：启动装配不再读取 `Providers[0]`，没有隐式单 Provider 回退，即使只配置一个 Provider 也必须显式声明路由。
- [x] `S3-006` **校验 Provider 配置唯一性** — 实施：拒绝缺失路由配置、重复 ID、空候选、未知 Provider/类型、重复模型规则和不支持的模型映射；完成证据：错误包含 Provider 或 Route 位置，坏配置在基础设施连接前阻止启动。
- [x] `S3-007` **校验时间与容量参数** — 实施：限制总超时、attempt 超时、重试数、队列容量和等待上限；完成证据：`TestStage3ConfigurationBoundaries` 统一覆盖零值、负值和 attempt 超时超过总预算等不可能组合。
- [x] `S3-008` **定义稳定内部错误类别** — 实施：`reliability.Category` 只保留生产链实际产生的本地、输入能力、无法表达的上游输出、401 Key、403 Key、上游限流、普通 4xx、模型不可用、5xx/协议、网络、超时、队列、Breaker 和取消；完成证据：HTTP 层只映射稳定 Failure，输入能力错误为 400，上游输出能力错误为 502。
- [x] `S3-009` **为内部错误附加安全元数据** — 实施：Failure 携带 Provider ID、候选、attempt、HTTP 状态、超时范围、Retry-After 和可 unwrap cause；完成证据：Error 文案不输出 cause、Key、URL 或请求内容，合并用例断言路由元数据。
- [x] `S3-010` **定义 Retry/Fallback/Breaker 判定结果** — 实施：`Signals` 独立表达 Retry、SwitchKey、Fallback、CountBreaker；完成证据：合并表覆盖普通 4xx、Key、429、指定 5xx、协议、网络、超时与取消。
- [x] `S3-011` **分类本地校验与授权错误** — 实施：本地/授权/租户限流类别不产生任何恢复信号，既有调用顺序在 Provider 前截断；完成证据：400/模型未授权不会取得 Breaker Permit。
- [x] `S3-012` **分类 401/403 Key 错误** — 实施：401/403 使用不同类别且都产生 SwitchKey/Fallback、不计 Breaker；401 永久停用 Key，403 只在当前请求排除 Key；完成证据：合并分类表和 Key 状态用例固定二者差异。
- [x] `S3-013` **分类 429** — 实施：产生 SwitchKey/Retry/Fallback 信号且不计 Breaker，实际 Key 按解析后的 `Retry-After` 冷却；完成证据：分类表和 Key 冷却恢复用例覆盖该链路。
- [x] `S3-014` **分类指定 5xx 与网络错误** — 实施：只有 500/502/503/504 和网络错误产生 Retry/Fallback/CountBreaker；完成证据：合并表证明 501 等其他 5xx 不被笼统计数。
- [x] `S3-015` **分类超时与客户端取消** — 实施：区分上游 attempt 超时、统一请求预算超时和 cancel，只有上游超时计 Breaker；完成证据：分类表和跨 Fallback 取消用例同时固定来源与策略。
- [x] `S3-016` **建立错误分类表驱动测试** — 实施：覆盖架构错误矩阵及包装后的 `errors.Is/As`；完成证据：`TestChatCompletionCircuitBreakerPolicy` 固定每个类别的 Retry、SwitchKey、Fallback 和 CountBreaker 信号。

### 6.2 Router 与 Provider Adapter 边界

- [x] `S3-017` **实现模型到候选的路由输入** — 实施：授权后以请求模型和启动配置生成候选集合；租户只属于授权、限流与缓存作用域；完成证据：Router 是纯内存规划，不执行网络调用。
- [x] `S3-018` **实现 primary 候选选择** — 实施：按 JSON 数组顺序确定第一候选并接入当前 Provider；完成证据：合并用例断言 primary 模型和真实上游请求。
- [x] `S3-019` **实现有序 fallback 候选生成** — 实施：按配置顺序生成并按 Provider+模型去重；完成证据：候选数组不依赖 map 遍历，合并用例固定 primary/secondary 顺序。
- [x] `S3-020` **处理无可用路由** — 实施：Router 返回稳定错误，HTTP 输出结构化 `503 route_unavailable`；完成证据：合并用例断言未知模型不增加上游调用。
- [x] `S3-021` **隔离缓存路由版本** — 实施：`MODEL_VELO_CACHE_ROUTE_VERSION` 只由 Exact Cache 持有，不再重复进入 Route Plan/Provider；完成证据：版本变化产生不同缓存 route digest，运行时路由结构没有重复版本字段。
- [x] `S3-022` **建立 Router 单元测试** — 实施：覆盖单候选、多候选、重复候选、未知模型和顺序稳定；完成证据：既有 Route Plan 合并用例使用纯内存 Router 和本地假上游固定顺序与无路由行为，不调用真实 Provider。
- [x] `S3-023` **在出现第二种实现时提取 Adapter 接口** — 实施：接口只表达鉴权方式和一次非流式 Chat 调用；完成证据：Attempt Executor 与唯一的多 Provider HTTP 装配入口都只依赖 Adapter Registry，不存在并行的 Provider Client 抽象或旧单 Provider 构造路径。
- [x] `S3-024` **实现 Adapter Registry** — 实施：按 Provider ID 和显式 `vendor`/`type` 构造 16 个厂商 Adapter；OpenAI、Mistral、DeepSeek、xAI、Zhipu、Groq、NVIDIA、Together 保留独立厂商类型，仅复用 OpenAI Chat wire codec 与 HTTP 边界；`custom/openai-compatible` 使用通用兼容 Adapter。Registry 不承担 fallback，也不保留旧 Client 别名、协议推断或 Adapter 内置默认 Key。
- [x] `S3-025` **保持 Adapter 错误标准化** — 实施：共享 HTTP 边界统一状态码、`Retry-After`、响应上限和 Chat 响应结构校验，原生响应归一化为 OpenAI Chat Completion；缺失 Usage 时省略而非伪造，无法表示的原生输出独立分类并 Fallback；完成证据：Orchestrator 不解析供应商 JSON，畸形 2xx 会反馈 Breaker。
- [x] `S3-026` **建立 Adapter 契约测试** — 实施：一个合并文件验证 16 个内置厂商及 custom-compatible 的具体 Adapter 身份、路径、鉴权、请求转换/透传和成功响应；共享 4xx/5xx、超时、取消、非法响应、断流与 Body 上限由兼容 HTTP 边界用例覆盖，能力不匹配由端到端 Fallback 用例覆盖。

### 6.3 Circuit Breaker 状态闭环

- [x] `S3-027` **定义 Breaker 三态与快照** — 实施：Closed/Open/HalfOpen 和只读 Snapshot 均不暴露内部可变引用；完成证据：合并用例逐态断言状态、计数和 Provider ID。
- [x] `S3-028` **定义 Breaker 配置** — 实施：阈值 1–1000、Open 1s–10m、HalfOpen 探测 1–100，默认 5/30s/1；完成证据：环境变量在连接基础设施前加载，非法组合启动失败。
- [x] `S3-029` **注入可控 Clock** — 实施：生产构造器使用 `time.Now`，测试构造器注入函数时钟；完成证据：Open/HalfOpen/恢复测试只推进时间变量，不 sleep。
- [x] `S3-030` **实现 Closed 调用前准入** — 实施：正常请求取得一次性 Permit；完成证据：HTTP 调用前 Allow、调用后 Complete，并以 defer Abandon 兜底。
- [x] `S3-031` **实现阈值触发 Open** — 实施：仅累计策略允许的连续失败，成功清零；完成证据：第二个计数失败达到测试阈值后立即 Open。
- [x] `S3-032` **实现 Open 快速拒绝** — 实施：返回带剩余 Open 时间的 breaker Failure；完成证据：HTTP 返回 `503 provider_circuit_open` 与 `Retry-After`，上游调用为零。
- [x] `S3-033` **实现 Open 到 HalfOpen 转换** — 实施：Open 到期在下一次 Allow/Snapshot 转入 HalfOpen；完成证据：fake clock 精确推进 10s 获得探测。
- [x] `S3-034` **限制 HalfOpen 探测并发** — 实施：显式记录 in-flight 探测并按配置拒绝超额请求；完成证据：首 Permit 未反馈时第二个探测被快速拒绝。
- [x] `S3-035` **实现成功反馈关闭 Breaker** — 实施：达到 HalfOpen 成功探测数后清零并回到 Closed；完成证据：HTTP 恢复请求成功且 Snapshot 为 Closed。
- [x] `S3-036` **实现失败反馈重新 Open** — 实施：HalfOpen 的可计数失败立即重新 Open 并重置冷却期；完成证据：fake clock 用例验证失败与下一轮恢复。
- [x] `S3-037` **忽略 Key 级 401/403** — 实施：Failure/Breaker 不计 Provider 故障；401 永久禁用实际 Key，403 不修改全局状态；完成证据：合并 Breaker 与 Key 状态用例固定策略。
- [x] `S3-038` **忽略 Key 级 429** — 实施：Failure/Breaker 不计 Provider 故障，实际选中的 Key 优先按上游 `Retry-After` 冷却，缺失时使用 30s；完成证据：分类表和 Key 状态用例证明不会污染 Provider Breaker。
- [x] `S3-039` **忽略客户端取消** — 实施：取消 Complete/Abandon 只释放探测许可，不修改失败计数；完成证据：合并用例断言取消后仍为 Closed。
- [x] `S3-040` **统计网络/指定 5xx 失败** — 实施：Breaker 只读取 `CountBreaker` 信号；完成证据：网络及 500/502/503/504 计数，401/429/501/cancel 不计数。
- [x] `S3-041` **保证每个准入结果只回写一次** — 实施：Permit 使用原子一次性标记，调用路径 defer Abandon；完成证据：重复 Complete 和 Abandon 后 Complete 均被拒绝。
- [ ] `S3-042` **建立 Breaker 并发测试** — 当前：64 个并发 HalfOpen 准入者下探测上限未被突破，确定性用例连续 20 次通过；`go test -race` 仍在编译阶段被本机 Go/race 工具链阻止，因此 race 完成证据不能勾选。
- [x] `S3-043` **暴露无敏感信息的 Breaker 快照** — 实施：快照只含 Provider ID、三态、失败/探测/拒绝计数和 Open 截止时间；完成证据：不含 Provider Key、请求或错误 cause，读取只持有短临界区。

### 6.4 Provider 有界队列

- [x] `S3-044` **定义 Provider 容量模型** — 实施：明确运行中并发、等待队列容量和最大等待时间；完成证据：配置与文档明确它是每进程 Provider 容量保护，与 Redis 租户配额分离。
- [x] `S3-045` **实现立即获取执行槽位** — 实施：容量可用时不进入等待；完成证据：channel 立即准入并返回原子一次性 Lease，合并用例验证重复释放失败。
- [x] `S3-046` **实现有界等待队列** — 实施：容量满时仅允许固定数量等待者；完成证据：CAS 预留 waiting 名额，超过容量立即返回 `queue_full`。
- [x] `S3-047` **实现队列等待超时** — 实施：受队列上限和请求总 deadline 的较早者控制；完成证据：同一 select 竞争 Queue Timer、Context 和槽位，短时限用例验证等待者被移除。
- [x] `S3-048` **传播等待期间客户端取消** — 实施：Context 取消立即退出并移除等待者；完成证据：合并用例验证取消分类、waiting 归零且 active 不变。
- [x] `S3-049` **保证所有路径释放槽位** — 实施：Attempt 取得 Lease 后立即 defer 释放，成功、错误、超时和取消路径共享同一清理边界；完成证据：重试耗尽与 Fallback 取消用例均断言所有 Provider 的 active/waiting 回到零，并发压力用例最终快照为零。
- [x] `S3-050` **实现按 Provider 隔离的 Queue Registry** — 实施：不同 Provider 使用独立容量；完成证据：合并用例同时占满两个 Provider，并验证 A 的拒绝不影响 B 的槽位。
- [x] `S3-051` **暴露 Queue 快照** — 实施：提供 active、waiting、capacity；完成证据：只读值快照还包含安全计数，不暴露 Key、请求或可变内部对象。
- [x] `S3-052` **建立 Queue 确定性单元测试** — 实施：覆盖立即获取、等待、满载、超时、取消、释放顺序；完成证据：复用 `chat_test.go` 单一合并用例，以状态轮询和有界短 Timer 避免长时间 sleep。
- [ ] `S3-053` **建立 Queue race/压力测试** — 当前：64 个 worker 共 1600 次并发获取/释放验证容量不超限、无死锁且最终无占位，确定性用例连续 20 次通过；`go test -race` 仍受本机工具链阻止，故保留未完成。

### 6.5 Provider Key 选择与健康状态

- [x] `S3-054` **定义 Key 内部身份与密文边界** — 实施：生产类型分离 Key ID/Secret，快照、Failure、String 和 GoString 只暴露稳定 ID；完成证据：安全格式化用例扫描不到 Secret，Attempt Trail 也只记录 Key ID。
- [x] `S3-055` **加载每 Provider 多 Key 配置** — 实施：统一由 `MODEL_VELO_PROVIDER_KEYS_JSON` 加载，Registry 在启动阶段拒绝重复 ID、空 Secret、未知或缺 Key Provider；完成证据：启动装配与合并 Key 边界用例覆盖合法和拒绝路径。
- [x] `S3-056` **实现并发安全的轮换策略** — 实施：原子游标轮换选择起点，以读写锁保护健康状态；完成证据：1600 次并发选择在 16 个 Key 间精确轮换且状态无损坏。
- [x] `S3-057` **跳过不可再次使用的 Key** — 实施：后续请求跳过 401 禁用 Key；当前请求额外排除已返回 401/403 的 Key，403 不影响后续请求；完成证据：选择器状态用例证明 401/403 的全局状态不同。
- [x] `S3-058` **实现 429 Key cooldown** — 实施：实际 429 优先采用上游 `Retry-After` 冷却选中 Key，缺失/非法时回退 30 秒，并把任何值限制在 24 小时内；完成证据：短冷却恢复以及并发反馈不缩短较长冷却的合并用例通过。
- [x] `S3-059` **解析 Retry-After 秒数格式** — 实施：只接受非负十进制秒数，超大值限制为 24 小时并向客户端回传规范秒数；完成证据：既有上游 HTTP 错误链路验证 `Retry-After: 17` 贯穿 Adapter、Failure 和响应。
- [x] `S3-060` **解析 Retry-After HTTP-date 格式** — 实施：通过 `http.ParseTime` 计算剩余时间，过去时间按立即可用处理，未来超大值限制为 24 小时；完成证据：固定 UTC 时钟用例覆盖未来、过去和 48 小时上限。
- [x] `S3-061` **记录 Key 成功反馈** — 实施：成功只清理选择时观察到的同一版本状态；完成证据：较旧的并发成功不能清除或缩短较新的 429 冷却，也不能恢复 401 禁用 Key。
- [x] `S3-062` **处理所有 Key 不可用** — 实施：返回 `key_exhausted` Failure 和结构化 `503 provider_keys_exhausted`，不会退回使用冷却或禁用 Key；完成证据：primary 唯一 Key 401 后只调用一次 primary，并由 secondary 完成 Fallback。
- [x] `S3-063` **建立 Key Selector 单元测试** — 实施：合并覆盖轮换、配置拒绝、401 禁用、403 请求内排除、429 冷却、冷却恢复和全耗尽；完成证据：断言和安全格式化均不输出 Secret。
- [ ] `S3-064` **建立 Key Selector race 测试** — 当前：1600 次并发选择与成功反馈无状态损坏、重复完成或越界，确定性用例连续 20 次通过；`go test -race` 仍受本机工具链阻止，故保留未完成。

### 6.6 有限 Retry 与时间预算

- [x] `S3-065` **定义 Retry Policy** — 实施：生产配置限制最大尝试、初始/最大退避、倍数、抖动、总预算和单次超时；完成证据：配置边界与确定性退避合并用例固定合法默认值和非法组合。
- [x] `S3-066` **明确 400 不 Retry** — 实施：Retry Policy 只读取恢复信号，本地和上游普通 400 没有 Retry/SwitchKey 信号，因此立即结束；完成证据：合并 HTTP 用例断言上游 400 时调用次数严格为一。
- [x] `S3-067` **实现 401/403 换 Key 路径** — 实施：两者均以零退避重新进入 Breaker→Queue→Key 并排除本请求已拒绝的 Key；只有 401 写入全局禁用状态；完成证据：端到端调用序列严格为 Key 1→Key 2，并分别断言 disabled/available 状态。
- [x] `S3-068` **实现 429 优先换 Key** — 实施：实际 Key 按 `Retry-After` 冷却，再以零退避选择其他可用 Key；完成证据：端到端调用序列严格为 Key 1→Key 2，Key 1 在成功响应后仍为 cooldown。
- [x] `S3-069` **实现指定 5xx 有限 Retry** — 实施：500/502/503/504 在最大次数和总预算内退避重试，每次重新准入并优先保持原 Key；完成证据：503→200 合并 HTTP 用例断言调用次数为二，普通 400 严格只调用一次。
- [x] `S3-070` **实现网络错误有限 Retry** — 实施：网络调用错误允许有限 Retry，本地构造和上游协议错误不 Retry；完成证据：网络失败→成功调用两次，持续网络失败严格在第三次停止。
- [x] `S3-071` **实现指数退避与上限** — 实施：按已完成 attempt 计算指数序列并在浮点放大前后限制最大退避；完成证据：固定随机源断言 100ms→200ms→250ms 上限。
- [x] `S3-072` **注入抖动随机源** — 实施：生产使用并发安全随机源施加可配置比例抖动，Policy 内部随机函数可在同包证据中固定；完成证据：确定性退避用例不依赖概率或 sleep。
- [x] `S3-073` **实现 Context-aware 退避等待** — 实施：使用 Timer/select，在退避期间响应总预算和客户端取消并安全停止 Timer；完成证据：已取消 Context 和不足预算都会立即拒绝等待，跨 Fallback 取消用例及时结束。
- [x] `S3-074` **建立请求总时间预算** — 实施：Cache miss 后由 Orchestrator 建立统一 deadline，所有 Retry 与 Fallback 共享；完成证据：第二候选执行中取消后不会进入第三候选，所有等待和调用均观察同一父 Context。
- [x] `S3-075` **建立单次 Attempt 超时** — 实施：每次上游 HTTP 调用创建子 deadline，父级总预算更早时自动优先生效；完成证据：既有上游超时用例和新的取消矩阵固定 attempt/父级停止行为。
- [x] `S3-076` **限制 Retry-After 等待** — 实施：只有 Key 存在明确冷却恢复时间、仍有 Retry 次数且等待小于剩余总预算时才释放资源后等待并重选；否则立即交给 Fallback 或最终错误；完成证据：现有合并用例证明短冷却后 Key 恢复，超出剩余预算的等待不会启动或耗尽 Context。
- [x] `S3-077` **记录安全的尝试元数据** — 实施：Trail 只记录真正上游调用的 attempt 序号、Provider/模型/Key ID、类别、状态码和耗时，并随成功结果或最终 Failure 汇总；完成证据：Key 冷却等待不占 attempt，合并用例断言一次真实调用只产生一条记录且结构不含 Secret/提示词。
- [x] `S3-078` **建立 Retry 策略测试矩阵** — 实施：合并覆盖错误类别、尝试上限、确定性退避、Retry-After、取消与总预算；完成证据：使用固定时钟/随机源与可取消假 Adapter，阶段 3 新增用例总耗时低于两秒。

### 6.7 Attempt Executor 与 Fallback Orchestrator

- [x] `S3-079` **实现单候选 Attempt Executor 骨架** — 实施：输入 candidate/context/request，输出标准结果；完成证据：组件只执行一个 Candidate，候选遍历留在 Orchestrator。
- [x] `S3-080` **固定 Attempt 内部执行顺序** — 实施：每次调用按 Breaker 准入→Queue→Key→Adapter→反馈执行，Retry 回到完整准入链；完成证据：Breaker Open/Queue 满均不调用 Adapter，401/403/429 与网络恢复用例固定重入顺序和调用次数。
- [x] `S3-081` **在 Attempt 中配对资源释放** — 实施：Breaker Permit、Queue Lease、attempt cancel、响应 Body 和退避 Timer 均在所属边界配对清理；完成证据：持续失败和候选取消后所有 Queue 快照 active/waiting 为零，Breaker 不保留取消状态。
- [x] `S3-082` **实现 Attempt 成功结果** — 实施：返回响应、实际 Provider/模型/Key ID 和尝试统计；完成证据：`AttemptResult`/`ExecutionResult` 可供 Cache/Usage 使用且不含 Secret。
- [x] `S3-083` **实现 Attempt 最终错误** — 实施：Retry 耗尽后保留最后一次安全 Failure，并附累计 attempt/fallback；完成证据：HTTP 层只接收最终 Failure，不读取执行组件内部状态。
- [x] `S3-084` **建立 Attempt 组件交互测试** — 实施：复用真实 Breaker/Queue/Selector 与可控假 Adapter，覆盖准入拒绝、满队列、换 Key、Retry、取消和成功；完成证据：合并用例固定 Adapter 调用次数、Key 状态及资源快照，不为测试额外抽象生产组件。
- [x] `S3-085` **实现 Fallback Orchestrator 外层循环** — 实施：依序执行候选，每个候选重新进入完整 Attempt；完成证据：503 场景 primary 完成后由 secondary 的独立 Registry 资源取得成功。
- [x] `S3-086` **按错误策略决定是否 Fallback** — 实施：普通 4xx/取消停止，明确模型不可用 400/404/422、能力不匹配和可恢复失败进入下一候选；Fallback 成功响应不写 Exact Cache；完成证据：合并 HTTP 用例同时断言调用次数与 Cache Store 次数。
- [x] `S3-087` **成功后立即停止候选循环** — 实施：返回首个成功结果；完成证据：primary 200 时 secondary 调用次数严格为零。
- [x] `S3-088` **保留统一总预算和取消传播** — 实施：每个候选读取同一父 Context 的剩余 deadline；完成证据：secondary 阻塞期间取消会立即结束且 tertiary 调用次数严格为零。
- [x] `S3-089` **建立端到端可靠性故障矩阵** — 实施：本地假 Adapter/Provider 覆盖 primary 成功、Retry 后成功、Fallback 成功、全失败、Breaker Open、Queue 满、Key 耗尽、能力不匹配和取消；完成证据：合并测试同时断言调用顺序、最终错误、Key 状态、缓存行为和资源归还。
- [ ] `S3-090` **完成阶段 3 门禁并等待用户确认** — 当前：2026-07-23 已完成 gofmt、全量测试、vet、故障矩阵、密钥扫描、字面/近似/人工独立性复查；用户已明确允许带着环境缺口进入 S4，但 `go test -race` 仍在测试执行前被本机 Go/race 工具链阻止，因此本项不能改写为完全通过。详细证据见 `STAGE3_GATE.md`。

## 7. S4：OpenAI-compatible SSE 与首 Chunk 提交边界

### 7.1 流式协议和 Adapter

- [x] `S4-001` **确认阶段 4 目标与非目标** — 实施：只增加 Chat SSE 与可靠性边界，不同时引入 Usage Worker；完成证据：用户于 2026-07-23 明确允许进入 S4。
- [x] `S4-002` **扩展 Chat 请求的 stream 分支** — 实施：公共校验完成后，`stream=true` 进入独立 SSE Handler，`false`/缺省保持原非流式 Cache 与 JSON 链；完成证据：合并 HTTP 用例分别通过，流式请求不读写 Exact Cache。
- [x] `S4-003` **定义流式 Adapter 最小能力** — 实施：可选 `StreamingAdapter` 返回由调用方关闭、受 Context 控制的 `ChatEventStream`；完成证据：接口只表达单次 `OpenStream`，不包含全局 Retry/Fallback。
- [x] `S4-004` **构造上游 SSE 请求** — 实施：兼容 Adapter 强制请求 JSON 的 `stream=true`，设置 `Accept: text/event-stream`、JSON Content-Type、Provider 鉴权和 request ID；完成证据：本地假上游精确断言并验证未知兼容字段仍保留。
- [x] `S4-005` **校验上游建流响应** — 实施：移交 Body 前检查状态码和 `text/event-stream` Content-Type；完成证据：503 保留可分类 HTTPError，非 SSE 的 2xx 返回 `ErrInvalidStream` 并关闭 Body。
- [x] `S4-006` **定义 SSE 事件读取器** — 实施：串行读取 `data:`、空行、注释/心跳和多行 data，忽略当前不使用的其他 SSE 字段；完成证据：基于 `bufio.Reader.ReadSlice` 的合并用例不依赖 Scanner 默认 token 上限。
- [x] `S4-007` **设置单事件/单行大小上限** — 实施：单行限制 1 MiB、单事件限制 2 MiB，分段读取期间持续检查；完成证据：无换行超长输入稳定返回 `ErrResponseTooLarge`。
- [x] `S4-008` **识别 OpenAI `[DONE]`** — 实施：只把完整 data 值 `[DONE]` 识别为终止事件，之后 `Next` 返回 EOF；完成证据：终止事件只返回一次且不携带普通 Chunk Data。
- [x] `S4-009` **校验最小 Chat Chunk** — 实施：事件读取器拒绝坏 JSON、错误信封和缺失 delta，只接受 choices/delta 或 usage-only 对象；完成证据：单候选流式 Attempt 只在该校验通过后返回 PreparedStream，坏首事件仍处于未提交状态。

### 7.2 首 Chunk 前 Retry/Fallback

- [x] `S4-010` **建立首 Chunk 缓冲** — 实施：`PrepareStream` 在服务端读取并保存首个有效内容事件，尚不触碰客户端 ResponseWriter；完成证据：成功返回 PreparedStream，Queue/Breaker/Key 所有权保持到显式 Finish。
- [x] `S4-011` **分类建流失败** — 实施：建流沿用稳定 Failure 分类，503、非 SSE 与网络错误分别进入现有 5xx、协议和网络策略；完成证据：合并用例验证 503 与非 SSE 的类别和 Breaker 反馈。
- [x] `S4-012` **分类首 Chunk 读取超时** — 实施：独立首事件计时器限制建流与首读，父 Context 继续承载总预算和取消；完成证据：慢上游在 100ms 边界被取消并归还 Queue，分类为 upstream timeout。
- [x] `S4-013` **分类首 Chunk 前 EOF/格式错误** — 实施：首内容前 EOF、`[DONE]` 或非法 Chunk 都转换为未提交协议失败；完成证据：三种边界均不返回 PreparedStream，并更新 Breaker。
- [x] `S4-014` **允许首 Chunk 前有限 Retry** — 实施：稳定策略允许时重新执行当前候选的完整 Breaker→Queue→Key→Adapter→首事件流程；完成证据：本地上游首次 503、第二次成功，Trail 保留两次调用且仅成功流继续持有资源。
- [x] `S4-015` **允许首 Chunk 前有序 Fallback** — 实施：当前候选 Retry 耗尽后由 `Orchestrator.OpenStream` 按 Route Plan 切换候选；完成证据：调用顺序固定为 primary、primary、secondary，最终 PreparedStream 携带 3 次 Attempt、1 次 Fallback 和完整 Trail。
- [x] `S4-016` **关闭失败的预提交响应流** — 实施：任一预提交失败都会取消上游 Context、关闭 Body、回写 Key/Breaker 并释放 Queue；完成证据：5xx、协议错误、超时和取消用例中资源均回到零，成功流只允许 Finish 一次。
- [x] `S4-017` **建立预提交故障测试** — 实施：覆盖 5xx、非 SSE、首 Chunk 超时、EOF/`[DONE]`、坏 JSON、父请求取消、Retry 和 Fallback；完成证据：HTTP 用例证明非法首事件返回 502 JSON 且未 Flush，primary 失败时客户端只收到 secondary SSE。

### 7.3 提交客户端 SSE 与后续边界

- [x] `S4-018` **在有效首 Chunk 后提交响应 Header** — 实施：`OpenStream` 成功后才设置 `text/event-stream`、no-cache、keep-alive 和禁用代理缓冲 Header；完成证据：坏首事件仍返回 JSON 502，正常流返回 SSE Header。
- [x] `S4-019` **确认 ResponseWriter 支持 Flush** — 实施：在调用上游前解开 Gin writer 并确认底层实现 `http.Flusher`；完成证据：无 Flush writer 返回结构化 `streaming_unavailable` 且上游调用为零。
- [x] `S4-020` **写出并 Flush 首 Chunk** — 实施：已验证 JSON 压缩为单行 `data: ...\n\n` 并立即 Flush；完成证据：真实 HTTP 客户端可在流结束前读到首事件。
- [x] `S4-021` **逐事件转发后续 Chunk** — 实施：Handler 串行执行 Next→单事件 Write→Flush；完成证据：多 Chunk 用例保持角色、内容和终止事件顺序。
- [x] `S4-022` **转发或处理心跳注释** — 实施：上游注释/心跳用于维持上游连接但不透传给客户端，只输出 OpenAI `data:` 事件；完成证据：含 heartbeat 的上游最终响应体不含注释，行为已写入 README。
- [x] `S4-023` **处理 `[DONE]` 后正常结束** — 实施：写出并 Flush `[DONE]` 后以成功结果 Finish，关闭 Body 并归还资源；完成证据：正常用例 Queue active/waiting 回到零。
- [x] `S4-024` **禁止提交后 Retry/Fallback** — 实施：首事件写出后的读取错误只 Finish 当前 PreparedStream；完成证据：primary 第二事件坏 JSON 时客户端只收到首事件，secondary 调用严格为零。
- [x] `S4-025` **记录提交后失败语义** — 实施：非取消的提交后失败只记录 request ID、Provider 和稳定 Category，不追加 JSON 错误；完成证据：断流响应保持已提交的 200 和部分 SSE 数据。
- [x] `S4-026` **传播客户端断开** — 实施：客户端 Context 直接控制成功上游流，断开会中止 Next、关闭 Body 并 Finish 为取消；完成证据：真实 HTTP 客户端取消后上游 Context Done，Queue 回到零。
- [x] `S4-027` **处理慢客户端背压** — 实施：每次最多持有一个已限制大小的事件，直接同步 Write/Flush，不创建 Chunk channel 或无界缓冲；完成证据：下一次 Next 只会在当前事件写完后发生。
- [x] `S4-028` **调整流式 Server 超时策略** — 实施：首事件验证成功后清除当前 SSE 响应继承的 Server 总写截止时间，每帧 Write/Flush 使用独立 15 秒截止时间并在成功后清除；后续事件静默上限复用当前 Provider 的 `attempt_timeout`，上游心跳会重置该计时器；完成证据：真实 HTTP 测试在第二个事件晚于 40ms Server WriteTimeout 时仍完整收到 `[DONE]`，无后续事件时按 upstream timeout 释放 Queue。

### 7.4 SSE 测试与门禁

- [x] `S4-029` **建立正常多 Chunk 测试** — 实施：假上游发送 heartbeat、首 Chunk、内容增量和 `[DONE]`；完成证据：客户端按序收到三个兼容事件且 ResponseRecorder 已 Flush。
- [x] `S4-030` **建立首 Chunk 前 fallback 测试** — 实施：primary 首事件为坏 JSON、secondary 返回正常流；完成证据：客户端响应只包含 secondary 首事件和 `[DONE]`。
- [x] `S4-031` **建立提交后失败测试** — 实施：primary 首 Chunk 成功后发送坏 Chunk；完成证据：HTTP 保持 200、只保留首事件、不追加 JSON 错误且 secondary 调用为零。
- [x] `S4-032` **建立客户端取消测试** — 实施：真实客户端读取首事件后取消 Context；完成证据：上游 Context 及时 Done，Queue active/waiting 回到零，取消不计 Breaker。
- [x] `S4-033` **建立 SSE 大小与格式攻击测试** — 实施：覆盖超长行、持续无换行输入、跨行超大事件、坏 UTF-8/JSON、伪 `[DONE]` 和 JSON 内容中的 `[DONE]`；完成证据：读取在 1 MiB 单行或 2 MiB 单事件边界停止，错误稳定且无 panic。
- [ ] `S4-034` **运行 SSE race/泄漏/故障测试** — 当前：8 路并发流、取消、fallback、Shutdown、空闲超时和资源归还已通过普通执行并连续 10 次稳定；`go test -race` 使用系统 Go 1.26.0 时在测试前因缺失 `runtime/race` 失败。已准备签名有效的便携 MinGW-w64，但执行第三方 GCC 需要用户单独明确授权，因此 race 仍未完成。
- [ ] `S4-035` **完成阶段 4 门禁并等待用户确认** — 当前：用户已经允许进入 S5，gofmt、全量测试、vet、故障矩阵、密钥扫描和独立性自动/人工复查已完成；仅 race 仍被系统 Go 缺失 `runtime/race` 阻止，详细证据见 `STAGE4_GATE.md`。

## 8. S5：Usage Event、Redis Stream 与 PostgreSQL 幂等 Worker

### 8.1 Usage 事件契约与请求生命周期

- [x] `S5-001` **确认阶段 5 目标与可靠性语义** — 实施：实现 at-least-once Usage 链路，明确不宣称 exactly-once；完成证据：用户明确允许进入 S5。
- [x] `S5-002` **定义 Usage Event schema 版本** — 实施：给事件结构增加版本，便于 Worker 兼容演进；完成证据：未知版本有明确处理。
- [x] `S5-003` **定义唯一事件 ID** — 实施：每个请求最终事件使用稳定、碰撞概率可忽略的 ID；完成证据：重投同一事件保持同 ID。
- [x] `S5-004` **记录 request ID、tenant ID 与 API Key ID** — 实施：沿认证 Context 获取稳定 ID，不保存或重新解析 Key Secret；完成证据：事件可按租户和 Key 聚合且不泄漏凭证。
- [x] `S5-005` **记录请求模型和实际路由** — 实施：分别保存 requested model 与实际 Provider/model；完成证据：fallback 后数据反映最终成功/失败候选。
- [x] `S5-006` **记录缓存与可靠性元数据** — 实施：表达 cache hit、attempt/retry/fallback 数量；完成证据：缓存命中不伪造 Provider 调用。
- [x] `S5-007` **记录 Token Usage** — 实施：保存 input/output/total token，并定义上游缺失时的 unknown 语义；完成证据：不把未知写成零后误导统计。
- [x] `S5-008` **定义终态枚举** — 实施：至少覆盖 success、cache_hit、failed、cancelled、stream_completed、stream_interrupted；完成证据：非法状态不能静默写入。
- [x] `S5-009` **记录错误类别而非敏感文案** — 实施：保存稳定 error category/code；完成证据：不写 Provider Key、提示词或任意上游 Body。
- [x] `S5-010` **记录开始、结束和延迟** — 实施：统一 UTC 时间与 duration 单位；完成证据：结束不早于开始，测试使用 fake clock。
- [x] `S5-011` **验证并序列化 Usage Event** — 实施：检查必填字段、长度和数值边界后生成稳定载荷；完成证据：坏事件在 API 内部被识别。
- [x] `S5-012` **建立请求生命周期 Collector** — 实施：请求开始时创建、各组件补充元数据、终态只 finalize 一次；完成证据：不是在各 Handler 随意拼事件。
- [x] `S5-013` **生成非流式成功事件** — 实施：包含真实 Usage 和路由结果；完成证据：端到端测试断言字段。
- [x] `S5-014` **生成缓存命中事件** — 实施：标记 cache_hit 且 Provider attempt 为零；完成证据：统计能区分缓存节省。
- [x] `S5-015` **生成最终失败事件** — 实施：记录最终安全错误类别与尝试统计；完成证据：中间 Retry 不生成多个终态事件。
- [x] `S5-016` **生成客户端取消事件** — 实施：区分排队、上游和流式阶段的取消；完成证据：不归因 Provider 故障。
- [x] `S5-017` **生成流式完成/中断事件** — 实施：`[DONE]` 与提交后断开使用不同状态；完成证据：已输出部分 Chunk 的失败可追踪。
- [ ] `S5-018` **保证每个请求至多一次 finalize** — 当前：Collector 使用互斥状态保证并发 finalize 只成功一次，普通并发测试通过；`go test -race` 在 `runtime/cgo` 编译前因 PATH 没有 GCC 失败，因此 race 证据仍保留。

### 8.2 Redis Stream Emitter

- [x] `S5-019` **在第二种 Emitter 出现时定义接口** — 实施：生产 Redis Emitter 与测试 fake 共用最小 `Emit(ctx,event)` 能力；完成证据：接口不包含 Worker/SQL 方法。
- [x] `S5-020` **定义 Stream 名称与环境隔离** — 实施：配置 stream key 和命名空间；完成证据：不同环境不会消费彼此事件。
- [x] `S5-021` **实现 Redis XADD** — 实施：把事件作为版本化载荷或明确字段写入；完成证据：成功返回 Redis entry ID。
- [x] `S5-022` **设置 Stream 保留策略** — 实施：主流不按长度裁掉 pending，数据库成功后事务执行 XACK+XDEL；dead-letter 独立限制长度；完成证据：正常流只保留积压，故障期间不因阈值静默丢未处理事件。
- [x] `S5-023` **定义 Emitter 失败策略** — 实施：Redis 不可用时不阻塞在线请求无限等待，明确日志/指标和丢失风险；完成证据：故障测试符合文档。
- [x] `S5-024` **限制 Emit 超时** — 实施：取请求剩余预算或独立短超时，避免 Usage 拖慢响应；完成证据：Redis 卡顿不会越过上限。
- [x] `S5-025` **建立 Emitter 单元与 Redis 集成测试** — 实施：覆盖成功、序列化失败、Redis 错误、独立超时和消费后清理；完成证据：真实 Stream 内容可解码且落库后从主流删除。

### 8.3 PostgreSQL Usage Schema

- [x] `S5-026` **创建 Usage GORM 模型** — 实施：保存事件字段、原始 Redis entry ID/版本和处理时间；完成证据：类型、非空和状态约束明确。
- [x] `S5-027` **为 event ID 建立唯一约束** — 实施：数据库作为幂等最终防线；完成证据：重复事件不能生成第二行。
- [x] `S5-028` **建立查询索引** — 实施：围绕 tenant+时间、request ID、provider/model 和状态建立必要索引；完成证据：不为未证明查询创建大量索引。
- [x] `S5-029` **编写 Usage Schema 同步测试** — 实施：在空库和已有 schema 上重复同步，并验证唯一约束和状态约束；完成证据：独立数据库可重复执行。

### 8.4 独立 Usage Worker

- [x] `S5-030` **创建 `model-velo-usage-worker` 入口** — 实施：与 API 分进程运行但共享稳定事件类型；完成证据：Worker 不启动 HTTP 模型调用链。
- [x] `S5-031` **加载 Worker 配置** — 实施：读取 Redis/PostgreSQL、group、consumer、batch、block、claim 和重试参数；完成证据：非法组合启动失败。
- [x] `S5-032` **幂等创建 Consumer Group** — 实施：处理 group 已存在和 stream 尚不存在；完成证据：重复启动不报致命错误。
- [x] `S5-033` **实现 XREADGROUP 批量阻塞读取** — 实施：限制 batch 和 block 时间并响应 Context；完成证据：空 Stream 不忙轮询。
- [x] `S5-034` **解码并校验 Stream 事件** — 实施：检查 schema 版本和必填字段；完成证据：坏消息不会进入正常 INSERT。
- [x] `S5-035` **实现 PostgreSQL 幂等写入** — 实施：按 event ID INSERT/UPSERT，重复投递视为已处理；完成证据：相同事件多次消费只有一行。
- [x] `S5-036` **保证事务成功后才 XACK** — 实施：数据库写失败保留 pending；完成证据：故障注入验证不提前 ack。
- [x] `S5-037` **处理数据库成功但 ACK 失败窗口** — 实施：重投依靠唯一 event ID 幂等；完成证据：不会重复计费/统计行。
- [x] `S5-038` **实现 pending 扫描与 XAUTOCLAIM** — 实施：认领超过 idle 阈值的其他 consumer 消息；完成证据：Worker 崩溃后事件可恢复。
- [x] `S5-039` **实现 Worker 有界重试退避** — 实施：数据库/Redis 暂时失败时 Context-aware 退避；完成证据：不忙循环且能关闭。
- [x] `S5-040` **处理毒消息** — 实施：记录投递次数，超过阈值转入明确 dead-letter/隔离路径再 ACK；完成证据：单坏消息不会永久阻塞批次。
- [x] `S5-041` **实现 Worker 优雅关闭** — 实施：停止读取、等待当前批次有界完成、关闭连接；完成证据：终止时不丢已提交未 ACK 的可恢复消息。
- [x] `S5-042` **增加 Worker 安全日志和指标** — 实施：记录读取、写入、重复、失败、pending、claim、dead-letter；完成证据：标签有界且不含事件敏感载荷。

### 8.5 Usage 故障验证与门禁

- [x] `S5-043` **建立 API→Stream→Worker→PostgreSQL 端到端测试** — 实施：覆盖成功、缓存、最终失败、取消和流式状态；完成证据：API 生命周期事件与真实 Redis→Worker→PostgreSQL 链路使用同一 schema，并分别通过合并端到端测试。
- [x] `S5-044` **建立崩溃窗口和重复投递测试** — 实施：模拟写库前崩溃、写库后 ACK 前崩溃、consumer 消失和毒消息；完成证据：真实依赖测试证明重复 event ID 只有一行、pending 可认领、毒消息可隔离。
- [ ] `S5-045` **完成阶段 5 门禁并等待用户确认** — 当前：Usage v2 真实 Redis/PostgreSQL 集成、故障测试、全量测试、vet、文档和独立性复查已完成；Worker race 在 `runtime/cgo` 编译前被 PATH 缺少 GCC 阻止，且尚未获得进入 S6 的用户确认。
- [x] `S5-046` **升级 Usage schema v2 并兼容 v1** — 实施：增加 API Key ID、usage 来源、详细 token、finish reason、TTFT 和有界 raw usage；完成证据：v1 事件仍可解码，未来未知版本仍被隔离。
- [x] `S5-047` **补齐流式 Usage 采集** — 实施：兼容流默认请求 `include_usage`，逐事件采集首 token 与末尾 usage-only Chunk；完成证据：HTTP 与 Collector 合并测试通过。
- [x] `S5-048` **实现成本快照和未知成本语义** — 实施：版本化生效窗口、整数 nanoUSD、Provider 报价优先、缓存零成本、重试 caveat；完成证据：找不到价格时数据库保持 NULL，不能用零伪装。
- [x] `S5-049` **实现隔离的 Usage 查询** — 实施：明细游标、汇总分组、时间序列、当前 tenant/API Key 强制过滤、查询上限和 no-store；完成证据：HTTP 拒绝跨 Key 查询，真实 PostgreSQL 跨租户隔离通过。
- [x] `S5-050` **实现保留期与历史重算** — 实施：Worker 有界分批清理，Admin 显式确认重算；完成证据：真实 PostgreSQL 删除与缺失成本重算通过。
- [x] `S5-051` **补齐多 Provider Usage 明细** — 实施：Anthropic、Gemini、Bedrock 和 OpenAI-compatible 映射可获得的缓存、推理、音频/图像 token；完成证据：统一事件与成本计算不依赖厂商私有表结构。
- [x] `S5-052` **执行 Usage v2 真实依赖门禁** — 实施：隔离 PostgreSQL 17.10 与 Redis 8.8.0 容器；完成证据：schema、详细成本、查询隔离、重算、保留期、幂等、pending 恢复、dead-letter 和 XACK+XDEL 全链通过。

## 9. S6：可观测性、CI、Benchmark、故障测试与求职展示

### 9.1 结构化日志与运行诊断

- [ ] `S6-001` **确认阶段 6 交付范围** — 实施：工程化已有能力，不借机新增未经验证的大功能；完成证据：用户明确允许进入 S6。
- [ ] `S6-002` **选择并统一结构化日志方案** — 实施：定义 JSON/文本开发模式、级别和时间格式；完成证据：API/Worker 不混用无结构输出。
- [ ] `S6-003` **定义请求公共日志字段** — 实施：request ID、tenant ID、模型、方法、路径、状态、耗时；完成证据：字段名稳定且 tenant 不从 Key 推断输出。
- [ ] `S6-004` **实现敏感信息脱敏规则** — 实施：过滤 Authorization、Provider Key、DSN 密码、Cookie、提示词和完整响应；完成证据：单元测试扫描日志内容。
- [ ] `S6-005` **记录请求生命周期日志** — 实施：请求结束时输出一次摘要，避免每层重复噪声；完成证据：成功、缓存、失败和取消可区分。
- [ ] `S6-006` **记录 Provider Attempt 日志** — 实施：只记录 Provider/Key ID、attempt、类别、耗时和结果；完成证据：Retry/Fallback 顺序可重建但无 Secret。
- [ ] `S6-007` **记录 Breaker/Queue 状态变更日志** — 实施：只在重要状态变化/拒绝时记录；完成证据：不会为每次准入刷屏。
- [ ] `S6-008` **记录 Usage Worker 生命周期日志** — 实施：启动、批次、重试、claim、dead-letter 和关闭；完成证据：故障定位不需要打印完整事件。
- [ ] `S6-009` **建立日志行为测试** — 实施：捕获输出验证字段、级别、request ID 和脱敏；完成证据：构造的假 Key/DSN/提示词不出现在日志。

### 9.2 Prometheus 指标

- [ ] `S6-010` **设计有界指标标签** — 实施：只使用状态、错误类别、Provider ID、模型等受控集合；完成证据：禁止 request ID、tenant ID、原始 URL/Key 作为 label。
- [ ] `S6-011` **暴露 API `/metrics`** — 实施：使用独立 registry/Handler 并明确是否需要访问保护；完成证据：抓取不经过聊天鉴权链路。
- [ ] `S6-012` **统计请求总数** — 实施：按路由、结果和流式模式计数；完成证据：测试请求使计数按预期增长。
- [ ] `S6-013` **统计请求延迟分布** — 实施：设置适合 LLM 请求的 histogram buckets；完成证据：单位固定且缓存/非缓存可区分。
- [ ] `S6-014` **统计当前在途请求** — 实施：进入增加、所有结束路径减少；完成证据：取消/panic 后 gauge 回零。
- [ ] `S6-015` **统计 Provider Attempt 与延迟** — 实施：按 Provider、结果、错误类别观测；完成证据：Retry 每次 attempt 可见。
- [ ] `S6-016` **统计 Retry 与 Fallback** — 实施：分别记录原因和结果；完成证据：一次请求的 retry/fallback 数与测试场景一致。
- [ ] `S6-017` **统计 Breaker 状态与拒绝** — 实施：状态 gauge、转换和 open reject counter；完成证据：HalfOpen 并发变化可观察。
- [ ] `S6-018` **统计 Queue 使用情况** — 实施：active、waiting、wait duration、timeout/reject；完成证据：压力测试下不超过容量。
- [ ] `S6-019` **统计 Auth、Rate Limit 与 Cache** — 实施：认证结果、限流拒绝、cache hit/miss/error/bypass；完成证据：故障降级路径有独立指标。
- [ ] `S6-020` **统计 Usage 数据链路** — 实施：XADD、消费、DB 写入、重复、pending、claim、dead-letter；完成证据：Worker 故障测试反映到指标。
- [ ] `S6-021` **建立指标单元测试** — 实施：使用测试 registry 收集并断言值/标签；完成证据：不会依赖全局 registry 导致测试污染。

### 9.3 必要的 Tracing

- [ ] `S6-022` **选择 OpenTelemetry 依赖与最小配置** — 实施：配置 service name、resource、sampler 和 exporter；完成证据：禁用 exporter 时应用仍可运行。
- [ ] `S6-023` **创建 HTTP 根 Span** — 实施：从入站请求提取 trace context 并关联 request ID；完成证据：状态和错误类别被记录。
- [ ] `S6-024` **为关键内部阶段创建子 Span** — 实施：Auth、Rate Limit、Cache、Route、Queue wait、Attempt；完成证据：不过度为普通函数创建碎片 Span。
- [ ] `S6-025` **为上游 Provider 创建 Client Span** — 实施：记录安全 URL 元数据、Provider 和状态；完成证据：不记录 Authorization 或请求 Body。
- [ ] `S6-026` **传播 Trace Context 到上游** — 实施：按标准 Header 注入且不信任任意客户端敏感 Header；完成证据：假上游能观察同一 trace。
- [ ] `S6-027` **追踪 Redis/PostgreSQL 关键调用** — 实施：限流、Cache、XADD、Worker 写库使用必要 Span；完成证据：不会产生高基数 SQL/Key 属性。
- [ ] `S6-028` **处理流式 Span 生命周期** — 实施：Span 在流真正完成/中断时结束，而不是 Header 提交时；完成证据：duration 覆盖完整 SSE。
- [ ] `S6-029` **建立 Trace 测试** — 实施：使用内存 exporter 断言父子关系、错误状态和脱敏；完成证据：测试不依赖外部 Collector。

### 9.4 GitHub Actions 与质量门禁

- [ ] `S6-030` **创建基础 CI Workflow** — 实施：在 push/PR 上使用固定 Go 版本和依赖缓存；完成证据：空缓存环境可运行。
- [ ] `S6-031` **在 CI 检查 gofmt** — 实施：格式不一致直接失败并显示文件；完成证据：不在 CI 静默改写源码。
- [ ] `S6-032` **在 CI 运行全部单元测试** — 实施：`go test ./...` 不访问付费公网 API；完成证据：假 Provider 覆盖上游。
- [ ] `S6-033` **在 CI 运行 `go vet`** — 实施：静态问题使 Job 失败；完成证据：命令和本地一致。
- [ ] `S6-034` **在 CI 运行 race 测试** — 实施：覆盖 Breaker、Queue、Key、SSE、Worker 并发包；完成证据：不因耗时随意跳过核心并发测试。
- [ ] `S6-035` **在 CI 提供 Redis/PostgreSQL 服务** — 实施：使用固定版本和健康等待；完成证据：集成测试不依赖开发者机器。
- [ ] `S6-036` **隔离并清理集成测试数据** — 实施：每次 Run 使用独立 DB/schema/key namespace；完成证据：并行或重跑不互相污染。
- [ ] `S6-037` **在 CI 验证 GORM Schema** — 实施：执行 AutoMigrate 幂等检查和数据库约束测试；完成证据：模型与真实 schema 不一致时失败。
- [ ] `S6-038` **增加依赖与漏洞检查** — 实施：固定工具版本，运行模块校验和 Go 漏洞扫描；完成证据：结果真实记录，不自动忽略高风险项。
- [ ] `S6-039` **增加密钥扫描** — 实施：检查提交历史/变更中的常见 Token、私钥和 DSN；完成证据：示例假 Key 不造成无法维护的误报。
- [ ] `S6-040` **保存失败诊断产物** — 实施：按需上传测试日志/覆盖率，不上传密钥和数据库数据；完成证据：失败可复盘。

### 9.5 可复现 Benchmark 与故障测试

- [ ] `S6-041` **定义 benchmark 问题而非只追 QPS** — 实施：分别测网关开销、Cache、Queue、Retry/Fallback 和 SSE；完成证据：每项有明确假设。
- [ ] `S6-042` **建立本地可控假 Provider** — 实施：支持固定延迟、响应大小、错误率、断流和限速；完成证据：benchmark 不调用真实付费 API。
- [ ] `S6-043` **定义标准请求载荷** — 实施：记录模型、消息/工具大小、响应 token/chunk 模式；完成证据：报告可重放相同负载。
- [ ] `S6-044` **定义并发和持续时间矩阵** — 实施：包含预热、并发档位、持续时间和重复次数；完成证据：不是只运行一次短压测。
- [ ] `S6-045` **记录硬件和软件环境** — 实施：CPU、内存、OS、Go/依赖版本和关键配置写入报告；完成证据：数字有上下文。
- [ ] `S6-046` **记录吞吐、延迟和错误率** — 实施：至少报告 QPS、p50/p95/p99、成功/错误和资源使用；完成证据：不只展示最好 QPS。
- [ ] `S6-047` **测量网关纯转发开销** — 实施：对比直连假 Provider 与经 Model-Velo；完成证据：差值和测试误差被说明。
- [ ] `S6-048` **测量 Cache 命中/未命中** — 实施：分别报告命中率、延迟和 Redis 故障降级；完成证据：数据集不会意外全命中。
- [ ] `S6-049` **测量 Queue 饱和与取消** — 实施：验证容量、等待延迟、拒绝率和取消释放；完成证据：无无限积压。
- [ ] `S6-050` **测量 Retry/Fallback 代价** — 实施：控制错误与延迟，观察总预算和尾延迟；完成证据：尝试数与报告一致。
- [ ] `S6-051` **测量 SSE 首 Chunk 与完整流延迟** — 实施：分别记录 time-to-first-chunk 和 total duration；完成证据：提交边界未被普通 HTTP 延迟掩盖。
- [ ] `S6-052` **建立 Redis 故障测试** — 实施：停机、超时、连接耗尽，验证 limiter/cache/emitter 各自策略；完成证据：行为与配置文档一致。
- [ ] `S6-053` **建立 PostgreSQL 故障测试** — 实施：停机、慢查询、连接池耗尽和事务失败；完成证据：Auth/Worker 不无限等待或错误 ACK。
- [ ] `S6-054` **建立 Provider 故障测试** — 实施：DNS/连接拒绝、5xx、429、慢首包、断流；完成证据：Retry/Breaker/Fallback/SSE 行为符合矩阵。
- [ ] `S6-055` **形成可复现报告** — 实施：提交命令、配置、原始摘要和结论限制；完成证据：不使用“高并发/生产可用”空泛结论。

### 9.6 容器、文档与求职展示

- [ ] `S6-056` **构建 API 多阶段镜像** — 实施：固定 builder/runtime、最小运行文件和非 root 用户；完成证据：镜像不含源码密钥和 Go 缓存。
- [ ] `S6-057` **构建 Usage Worker 镜像** — 实施：复用构建策略但保持独立入口；完成证据：API/Worker 可分别部署。
- [ ] `S6-058` **完善全栈 Compose/Profile** — 实施：按需启动 API、Worker、Redis、PostgreSQL 和可观测组件；完成证据：默认开发路径不过度启动全部组件。
- [ ] `S6-059` **完成 README Quickstart** — 实施：从依赖启动、GORM schema 同步、Key 创建到 curl 请求；完成证据：全新环境能复现成功链路。
- [ ] `S6-060` **完成配置参考** — 实施：列出每个变量、默认值、必填性、安全说明和阶段；完成证据：实现与文档自动/人工核对。
- [ ] `S6-061` **完成 API 与 SDK 示例** — 实施：提供 curl 和至少一种 OpenAI SDK 的非流式/SSE 示例；完成证据：示例可运行且使用假 Key。
- [ ] `S6-062` **完成错误与恢复策略文档** — 实施：说明 400、401/403、429、5xx、网络、取消、Retry/Fallback/Breaker；完成证据：与测试矩阵一致。
- [ ] `S6-063` **完成 Auth、Rate Limit 与 Cache 文档** — 实施：描述租户隔离、Key 哈希、限流故障策略和 Cache Key/TTL；完成证据：不泄露实现密钥。
- [ ] `S6-064` **完成 SSE 首 Chunk 文档** — 实施：说明提交前后 Retry/Fallback 边界与取消；完成证据：配有可运行故障演示。
- [ ] `S6-065` **完成 Usage 可靠性文档** — 实施：画出 XADD→Group→幂等 DB→XACK，说明 pending/毒消息；完成证据：明确 at-least-once。
- [ ] `S6-066` **更新架构图为已实现状态** — 实施：区分真实包/进程与概念模块；完成证据：图中不存在未实现却未标注的能力。
- [ ] `S6-067` **记录关键架构决策** — 实施：解释单体多进程、无 DDD/插件平台、Queue/Breaker/Fallback 边界等；完成证据：每项包含取舍和后果。
- [ ] `S6-068` **整理故障演示脚本** — 实施：可重复演示正常、限流、缓存、fallback、Breaker、SSE 中断和 Worker 恢复；完成证据：脚本不依赖付费 API。
- [ ] `S6-069` **形成真实简历项目描述** — 实施：只基于已完成代码和测量写技术栈、职责、难点和量化结果；完成证据：每条亮点能指向代码/测试/报告。
- [ ] `S6-070` **形成面试讲解材料** — 实施：准备请求链路、失败处理、并发控制、数据一致性和取舍问答；完成证据：不把 at-least-once 讲成 exactly-once。
- [ ] `S6-071` **执行最终字面重复度检查** — 实施：分别对 GoModel、Bifrost、合并集合运行固定工具/参数；完成证据：报告工具版本、阈值、结果区间且严格小于 10%。
- [ ] `S6-072` **执行最终归一化近似检查** — 实施：去空白/局部标识符差异后按 12 行或 80 token 阈值检测；完成证据：三种参考口径均严格小于 10%。
- [ ] `S6-073` **执行最终人工独立性复核** — 实施：检查控制流、独特命名、注释和错误字符串；完成证据：发现相似项通过重新设计解决而非稀释分母。
- [ ] `S6-074` **建立版本与变更说明** — 实施：整理 Conventional Commits、里程碑标签和真实 changelog；完成证据：每个版本可对应测试与文档状态。
- [ ] `S6-075` **完成最终演示门禁** — 实施：从干净环境启动全栈，运行正常/异常/SSE/Usage/故障演示并归档命令结果；完成证据：所有声明可复现，用户确认项目展示版本完成。

## 10. 每次勾选任务时的最小记录

每次完成纵向切片，交付说明保持精简，只记录：

1. 完成的 TODO ID、目标与非目标；
2. 实际修改文件和请求调用链；
3. 为核心行为新增或更新的测试，以及实际运行的 `gofmt`、`go test`、`go vet`；
4. 失败检查的真实根因和仍未覆盖的阶段门禁；
5. 是否查看参考项目，以及本次 TODO 状态变化。
