# Model-Velo 项目开发规则

本文件约束所有在 Model-Velo 仓库中进行的人工开发和 AI 辅助开发。目标不是一次性生成大量代码，而是让项目以可解释、可测试、可复盘的方式逐步成长为适合实习求职展示的 LLM Gateway。

## 1. 项目目标

Model-Velo 是一个使用 **Go、Gin、GORM、Redis、PostgreSQL** 实现的 OpenAI 兼容 LLM 网关。

项目首先完成一条最小可运行链路，再逐步增加鉴权、限流、缓存、路由、熔断、队列、Retry、Fallback、SSE 和异步 Usage 记录。每个阶段都必须能够独立运行、测试和解释。

本项目不是 GoModel 或 Bifrost 的 Fork，也不追求复刻它们的全部功能。

## 2. 只读参考项目

允许只读研究以下两个项目的公开实现：

### GoModel

- 本地源码：`D:\agent开源项目\GoModel-main\GoModel-main`
- GitHub：<https://github.com/ENTERPILOT/GoModel>
- 主要参考概念：显式分层、OpenAI 协议转换、Provider 能力接口、响应缓存、HTTP Client Retry、Circuit Breaker、Fallback、Usage/SSE observer。

### Bifrost

- 本地源码：`D:\agent开源项目\bifrost-dev\bifrost-dev`
- GitHub：<https://github.com/maximhq/bifrost>
- 主要参考概念：Provider Queue、Attempt Executor、Provider Key 选择与轮换、有序 Fallback、插件生命周期、分层限流、SSE 首 Chunk 检查。

参考项目一律只读。不得修改、格式化、移动或向其中写入任何文件。

## 3. 防止 AI Coding 失控

任何功能都按下面顺序完成：

1. 先说明本阶段的目标、非目标和验收条件。
2. 检查 Model-Velo 当前代码，只打开完成该功能必需的文件。
3. 如确有必要，只查看参考项目中与该问题直接相关的少量函数，不扫描后照搬整个模块。
4. 给出本次预计修改的文件和调用链；通常一次只完成一个纵向功能切片。
5. 实现能够满足验收条件的最小代码，不预埋尚未需要的抽象和配置。
6. 默认采用快速交付模式：先完成生产代码，只在安全边界、并发状态、数据持久化或外部协议这四类高风险位置增加保护；不要为了逐项勾选 TODO 重复验证同一件事。
7. 说明代码为什么这样写、实际验证结果、仍未覆盖的边界，然后停止在当前里程碑。

快速交付模式的测试上限：

- 一个纵向功能通常最多新增或扩展一个合并测试文件；已有测试能证明行为时不再新增。
- 不在 Handler、Service、存储和集成层重复验证同一控制流，不追求覆盖率数字或测试数量。
- 普通配置转发、简单字段映射、样板错误分支和已由端到端链路覆盖的代码，不单独补测试。
- 纯测试 TODO 只作为阶段末证据清单，不得被自动选择为下一项开发任务。
- 如果当前授权阶段只剩 race、集成环境或其他测试门禁，应记录缺口并停止，等待用户允许进入下一阶段；不得用继续写测试填充进度。
- 向用户讲解时默认聚焦生产代码、调用链和设计取舍；除非用户主动询问，不要求用户学习测试实现。

除非用户明确要求，不得自动进入下一个里程碑。不得以“以后可能需要”为理由提前创建大量接口、目录、配置项或空实现。

禁止以下行为：

- 一次生成完整网关或几十个未经验证的文件；
- 为了显得复杂而引入微服务、DDD、事件总线或通用插件平台；
- 创建没有第二个实现需求的抽象层；
- 隐藏失败的测试、跳过错误处理或伪造 benchmark；
- 把 README 中的功能宣称当成已经完成；
- 在没有测试证据时声称“生产可用”“高并发”或“零丢失”；
- 擅自修改用户已有代码或与当前阶段无关的文件。

## 4. 代码独立性与重合度上限

可以学习需求、调用顺序和架构取舍，不得复制参考项目的 Go 源码、测试、注释、错误文案、配置结构、SQL migration、目录布局或独特命名。

实现功能前先用 Model-Velo 自己的语言写验收条件；实现时根据 Go 标准库和依赖库的公开 API 独立编码。不要一边显示参考函数，一边逐行改名重写。

不得让生产 Go 源码与 GoModel、Bifrost 任一项目或两者合并后的代码重合度达到 10%。发布每个主要里程碑前必须检查以下两个指标，二者都要 **严格小于 10%**：

1. **字面重复度** = Model-Velo 中落入相同代码块的生产 Go 逻辑行数 ÷ Model-Velo 生产 Go 总逻辑行数。
2. **归一化近似重复度** = 去除空白、格式差异和局部标识符差异后，落入相似代码块的 Model-Velo 生产 Go 逻辑行数 ÷ Model-Velo 生产 Go 总逻辑行数。

检测口径：

- 对 GoModel、Bifrost 分别计算，并对两个参考集合并后再计算；重复的 Model-Velo 行在合并口径中只计一次。
- 默认只统计连续不少于 12 个非空逻辑行或不少于 80 个有效 token 的代码块，避免把普通 import、错误判断等语言惯用法误判为复制。
- 排除 `_test.go`、`vendor`、generated、UI、protobuf 生成代码、文档和示例；这些排除项本身同样不得从参考项目复制。
- 第三方依赖源码不计入 Model-Velo 自有代码。
- 除自动检测外，必须人工检查相同控制流、相同独特命名、相同注释和相同错误字符串。
- 如果工具只能给近似结果，应报告工具、版本、参数和结果区间，不得伪造精确百分比。
- 一旦任一指标达到 10%，当前里程碑不得完成；应重新设计和独立实现，而不是通过增加无意义代码稀释分母。

架构相似不等于代码复制。允许采用“Breaker + Queue + Attempt + Fallback”这一通用模式，但 Model-Velo 必须拥有自己的包边界、接口、数据结构、错误分类和测试。

## 5. 分阶段范围

### 阶段 1：最小网关

- Gin HTTP Server；
- `GET /healthz`；
- `POST /v1/chat/completions` 非流式请求；
- 一个 OpenAI-compatible 上游；
- 环境变量配置、请求超时、结构化错误和 request ID；
- 使用本地假上游完成测试，CI 不调用真实付费 API；
- `stream=true` 在尚未实现时返回明确的“不支持”错误。

阶段 1 不实现 Redis、PostgreSQL、Retry、Fallback、管理 UI 或多 Provider。它的目标只是证明一条最小请求链能够正确往返。

### 阶段 2：基础技术栈

- 使用 Docker Compose 提供 Redis 和 PostgreSQL；
- GORM PostgreSQL 模型与启动时 `AutoMigrate`；
- API Key 哈希存储与鉴权；
- Redis 分布式限流；
- Redis exact response cache；
- 明确缓存 Key、TTL、租户隔离和故障降级策略。

### 阶段 3：路由和可靠性

- 生成有序路由计划；
- Provider Circuit Breaker；
- Provider 有界队列；
- 单候选路由 Attempt Executor；
- Provider Key 选择；
- 按错误类别 Retry；
- Retry 耗尽后的有序 Fallback；
- 总超时、单次尝试超时和客户端取消传播。

### 阶段 4：SSE

- OpenAI-compatible SSE；
- 首 Chunk 提交边界；
- 首 Chunk 前允许 Retry/Fallback；
- 已输出 Chunk 后禁止切换 Provider；
- 客户端断开后取消队列等待、重试和上游请求。

### 阶段 5：Usage 数据链路

- 请求生命周期生成 Usage Event；
- Redis Stream 与 consumer group；
- 独立 Usage Worker；
- 幂等写入 PostgreSQL；
- MySQL 不属于本项目技术栈；
- 缓存命中、失败、取消和流式结束都有明确事件状态。

### 阶段 6：工程化展示

- Prometheus 指标和必要的 tracing；
- GitHub Actions；
- 可复现的 benchmark 与故障测试；
- 架构说明、运行教程、接口示例和求职项目亮点。

## 6. 实现原则

- 遵循 KISS：先写清楚，再做抽象。
- HTTP Handler 只处理传输协议，不直接编写路由、Retry 或 SQL。
- Provider Adapter 只处理上游协议差异，不承担全局 Fallback。
- Fallback Orchestrator 对候选路由循环；每个候选必须重新进入 Breaker、Queue、Key 选择和 Retry。
- Circuit Breaker 必须同时有调用前准入和调用后结果回写。
- 400 不 Retry；401/403 不使用原 Key 重试；429 优先换 Key或尊重 `Retry-After`；指定 5xx 和网络失败才进行有限 Retry。
- 客户端取消必须通过 `context.Context` 传播到队列等待、退避等待和上游 HTTP 请求。
- 所有队列都有容量和等待上限；所有网络调用都有超时；Retry 和 Fallback 共享总时间预算。
- 缓存失败默认不能阻止正常上游请求；限流存储失败采用什么策略必须显式配置和测试。
- API Key、Provider Key 不写入日志，不提交到 Git；示例使用 `.env.example`。
- Usage 采用至少一次投递语义，数据库依靠唯一事件 ID 幂等，不能宣称 exactly-once。
- 当前 PostgreSQL schema 统一由 GORM 模型和 `AutoMigrate` 管理，不引入 `golang-migrate`、独立 migration CLI 或手写 SQL 文件；破坏性 schema 变更必须单独评审，不能让 `AutoMigrate` 自动删数据。

## 7. 代码质量和验证

验证按“日常纵向切片”和“阶段门禁”分层，不要求每个小 TODO 都建立一套独立测试矩阵。

日常纵向切片至少执行：

- 对修改的 Go 文件运行 `gofmt`；
- 交付前统一运行一次 `go test ./...`，不在每个 TODO 后反复运行和扩写测试；真实基础设施测试可以在未显式配置时跳过，但必须清楚显示跳过条件；
- 修改生产 Go 代码时运行 `go vet ./...`；
- 只有高风险功能才补“一条正常链路 + 一个最关键失败边界”，并只选择单元、HTTP 或真实集成中最贴近风险的一层；
- 文档、注释、命名和无行为变化的重构不新增测试文件，也不重复运行真实基础设施矩阵。

阶段门禁再集中执行：

- Redis/PostgreSQL 真实集成、并发竞争、故障恢复和资源清理；
- `go test -race ./...` 及必要的泄漏检查；
- 完整异常矩阵、独立性检查和可复现演示；
- benchmark 必须记录硬件、并发、请求体、持续时间和命令，不只给一个 QPS 数字。

阶段门禁优先复用本阶段已有的合并用例；同一阶段的多个测试类 TODO 可以共享一份证据，不为每个 ID 创建文件或独立测试函数。

如果 race、Docker 或外部基础设施被本机环境阻止，应记录缺口并保持对应门禁未完成；经用户允许后，可以继续同一阶段内与该缺口独立的功能，不得让工具环境长期阻塞生产功能推进。

测试失败时先解释根因，不得为了变绿删除断言或降低测试有效性。

## 8. Git 和讲解规则

- Commit 使用 Conventional Commits，例如 `feat(gateway): add non-stream chat proxy`。
- 一个提交只表达一个可解释变化，不提交密钥、构建产物和本地数据库。
- 每个里程碑都要能回答：解决了什么问题、请求怎么走、失败如何处理、为什么没有采用更复杂方案。
- 对参考项目的借鉴只记录概念、被查看的文件/函数和 Model-Velo 的独立取舍，不粘贴参考源码。
- 面试材料必须以真实代码和测试为依据，不夸大完成度。

## 9. 当前边界

用户已于 2026-07-23 明确允许进入阶段 4。阶段 3 的生产链与非 race 门禁已经完成，Breaker、Queue 和 Key 的 3 个 race 证据项仍因本机 Go/race 工具链阻塞而保留。阶段 4 已完成兼容上游 `StreamingAdapter`、有界 SSE 解析、首事件缓冲、候选内有限 Retry、候选间有序 Fallback，以及公开 `stream=true` 的 Header/Flush、`[DONE]`、提交后禁止切换 Provider 和客户端取消传播。流式 Handler 绕过 Exact Cache，并以同步写入保持有界背压。下一步处理长流 Server 超时与 SSE 格式攻击矩阵，再进行可执行的阶段 4 门禁；未经用户明确确认不得进入阶段 5 或实现 Usage Worker。
