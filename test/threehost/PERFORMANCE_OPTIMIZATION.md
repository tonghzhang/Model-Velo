# Model-Velo 三机性能优化与测试手册

这份文件是接下来性能工作的唯一运行清单。测试拓扑固定为：

```text
k6 客户端机 -> Model-Velo 网关机 -> 假 LLM 上游机
```

不在同一台网关机上轮流运行其他网关，也不把不同机器、不同提交、不同运行参数产生的
数字放在一起比较。每一轮都保存 k6、三台机器资源、Prometheus、假上游调用数、
PostgreSQL/Redis/Usage 证据和最终 HTML。

## 1. 现在处于什么位置

第一轮诊断结果：

| 负载 | 实际 RPS | 成功率 | dropped | P95 | P99 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 500 RPS | 500.0 | 99.987% | 0 | 83.36 ms | 271.29 ms |
| 750 RPS | 749.5 | 99.959% | 0 | 230.12 ms | 353.18 ms |
| 1000 RPS | 995.0 | 97.044% | 485 | 726.23 ms | 1247.04 ms |
| 1500 RPS | 1460.3 | 44.536% | 3127 | 2934.41 ms | 4110.65 ms |

500 RPS、10 分钟耐久结果为 99.992% 成功率、P99 221.51 ms。客户端 CPU 最高
29.61%，因此第一轮的拐点不是客户端 CPU 已经跑满。

第一轮性能数字不能直接作为最终成绩，因为 Usage 链路存在两个正确性问题：

- Cache 状态使用大写 `HIT/MISS/BYPASS`，Usage Event 只接受小写，导致完成事件失败；
- Outbox Relay 重复发布 `published` 记录，Redis Stream 增长到约 607 万条，
  Outbox 留下约 64.8 万条记录，Worker 重复消费却没有成功落库。

当前修复把 Cache 状态统一为小写，并把链路调整成：

```text
请求开始 -> PostgreSQL Outbox pending
请求结束 -> Outbox ready
Worker 批量发布 -> Redis consumer group
Worker 批量落 usage_events + 删除 Outbox -> XACK + XDEL
```

下一步不是先调 Queue，而是直接用同一组 19 分钟负载验证这次修复。只有 Usage
对账正确、Stream 不再膨胀，后面的 RPS 和延迟数据才可信。

## 2. 最快的优化节奏

不要为每个猜测新建一个分支，也不要把十个参数一起修改。使用当前性能分支，每个可解释的
生产代码变化单独提交，每轮测试记录准确 commit：

1. `D1`：当前 Usage 修复，运行一次 19 分钟诊断；
2. 根据 `D1` 的阶段耗时和连接池指标，只选最大的一个瓶颈；
3. 修改这个瓶颈对应的代码或一个参数，运行 `D2` 19 分钟诊断；
4. 如果 `D2` 明显改善且没有转移成新瓶颈，停止继续试参数；
5. 用最终候选配置再跑一次 19 分钟确认；
6. 最后运行 1.5–2 小时完整套件和单独的 Redis 限流测试。

参数实验是条件测试，不是必跑清单。比如 Redis 没有等待和 timeout，就不试
`100 -> 200`；Provider Queue 没达到 256，就不试 `256 -> 512`。这样通常两到四轮
19 分钟测试就能得到有证据的优化结果，而不是做一下午无效排列组合。

每次代码修改后，先对修改过的 Go 文件执行 `gofmt -w`，再在开发机执行：

```bash
go test ./...
go vet ./...
```

最终版本额外执行：

```bash
go test -race ./...
```

## 3. 每轮测试不变的规则

### 3.1 三台服务器必须一致

- 三台服务器签出同一个 commit SHA；
- 使用同一个 `RUN_ID`，不要在三台机器分别运行 `date`；
- 网关参数变化后必须重建或重建容器，不能只修改 `.env`；
- 三台服务器时间必须同步；
- 同一轮不更换实例规格、地域、Docker 版本或网络拓扑；
- 正式运行时 Git worktree 应为 clean；
- `.env`、API Key 和指标 Token 不进入结果包。

三台机器分别检查：

```bash
git status --short
git rev-parse HEAD
timedatectl show -p NTPSynchronized --value
```

三个 `git rev-parse HEAD` 必须相同，NTP 应显示 `yes`。

### 3.2 RUN_ID 命名

建议格式：

```text
diag-usagefix-r1-20260727T230000Z
diag-authcache-r2-20260728T010000Z
diag-final-r1-20260728T030000Z
complete-final-r1-20260728T050000Z
ratelimit-final-r1-20260728T080000Z
```

`RUN_ID` 表示一次不可覆盖的实验。失败后重新运行也要换一个 ID，不能覆盖旧目录。

### 3.3 什么叫可比较

比较两轮时，至少以下项目相同：

- 服务器规格和地域；
- 网关进程数；
- 假上游镜像和模型场景；
- k6 脚本参数；
- Queue、Breaker、Retry、限流和连接池中没有被实验的参数；
- 测试持续时间；
- 采集器正常覆盖整个 case；
- 客户端 `dropped_iterations` 没有先被客户端资源限制。

## 4. 下一轮：Usage 修复后的 19 分钟诊断

这一轮编号为 `D1`。它必须使用新 commit、新 `RUN_ID` 和干净 PostgreSQL/Redis。
旧的 607 万条 Stream 数据不能和修复后的结果共用。

### 4.1 先发布准确 commit

修复代码提交并推送后，在开发机记录 SHA：

```bash
git status --short
git rev-parse HEAD
```

下面所有服务器都把 `REPLACE_WITH_COMMIT_SHA` 替换成这个 SHA。不要把
`<commit>` 原样复制到 Bash，因为尖括号会被 Shell 当成重定向。

### 4.2 假上游机

```bash
cd ~/model-velo
git fetch origin
COMMIT=REPLACE_WITH_COMMIT_SHA
git checkout "$COMMIT"

docker compose -f test/threehost/upstream.compose.yaml build main
docker compose -f test/threehost/upstream.compose.yaml up -d --no-build

curl http://127.0.0.1:9000/healthz
curl http://127.0.0.1:9001/healthz
curl http://127.0.0.1:9002/healthz
```

三个端口复用同一个 `model-velo-fake-upstream:local` 镜像，只是启动参数不同。

### 4.3 网关机：保留旧数据，创建全新测试卷

先停止旧 Compose 项目，但不要使用 `-v`：

```bash
cd ~/model-velo
git fetch origin
COMMIT=REPLACE_WITH_COMMIT_SHA
git checkout "$COMMIT"

unset COMPOSE_PROJECT_NAME
docker compose down
```

旧卷仍然保留。使用新的 Compose 项目名启动时，会自动创建新的 PostgreSQL/Redis 卷：

```bash
export COMPOSE_PROJECT_NAME=mv-perf-d1

docker compose up -d --build gateway usage-worker
docker compose ps
curl http://127.0.0.1:8080/readyz
curl http://127.0.0.1:9091/readyz
```

在新数据库创建测试租户和 API Key：

```bash
docker compose --profile tools run --rm admin bootstrap-tenant \
  --slug threehost \
  --name "Three Host Benchmark" \
  --label "k6" \
  --models "mock/instant,mock/typical,mock/slow,mock/jitter,mock/spike-5,mock/error-rate-10,mock/payload-10k,mock/payload-50k,mock/retry-2,mock/error-400,mock/error-429,mock/error-503,mock/sse-error,mock/sse-drop,mock/fallback"
```

保存只显示一次的 `api_key`，填入客户端 `test/threehost/benchmark.env`。不要重新运行
`prepare-gateway-env.sh`，否则会更换 Pepper 和其他密钥。

当前诊断配置保持：

```text
MODEL_VELO_POSTGRES_MAX_OPEN_CONNS=50
MODEL_VELO_POSTGRES_MAX_IDLE_CONNS=10
MODEL_VELO_REDIS_POOL_SIZE=100
MODEL_VELO_REDIS_MIN_IDLE_CONNS=10
MODEL_VELO_RATE_LIMIT_REQUESTS=1000000
MODEL_VELO_BREAKER_FAILURE_THRESHOLD=5
MODEL_VELO_QUEUE_MAX_IN_FLIGHT=256
MODEL_VELO_QUEUE_MAX_WAITING=2048
MODEL_VELO_QUEUE_WAIT_TIMEOUT=2s
MODEL_VELO_USAGE_BATCH_SIZE=50
MODEL_VELO_LOG_LEVEL=info
MODEL_VELO_OTEL_SAMPLE_RATIO=0
```

如果 `.env` 没有显式写 `MODEL_VELO_USAGE_BATCH_SIZE`，默认就是 50，不必为了这轮补写。

### 4.4 先启动三个监控

选择一个固定 ID，并把完全相同的字符串粘贴到三台机器。下面用：

```text
diag-usagefix-r1-20260727T230000Z
```

网关机终端一：

```bash
cd ~/model-velo
export COMPOSE_PROJECT_NAME=mv-perf-d1
RUN_ID=diag-usagefix-r1-20260727T230000Z

DURATION_SECONDS=1800 \
PROGRESS_SECONDS=10 \
OUTPUT_FILE="test-results/threehost/$RUN_ID/gateway-stats.jsonl" \
bash test/threehost/collect-compose-stats.sh
```

网关机终端二：

```bash
cd ~/model-velo
export COMPOSE_PROJECT_NAME=mv-perf-d1
RUN_ID=diag-usagefix-r1-20260727T230000Z

DURATION_SECONDS=1800 \
PROGRESS_SECONDS=10 \
OUTPUT_DIR="test-results/threehost/$RUN_ID/prometheus" \
bash test/threehost/collect-prometheus.sh
```

假上游机：

```bash
cd ~/model-velo
RUN_ID=diag-usagefix-r1-20260727T230000Z

DURATION_SECONDS=1800 \
PROGRESS_SECONDS=10 \
COMPOSE_FILE=test/threehost/upstream.compose.yaml \
SERVICES=main,fail,fallback \
OUTPUT_FILE="test-results/threehost/$RUN_ID/upstream-stats.jsonl" \
bash test/threehost/collect-compose-stats.sh
```

看到三个终端持续显示 `state=collecting` 后再启动客户端。监控脚本不需要在构建镜像时
传入 `RUN_ID`；它只用来命名和对齐本轮结果。

### 4.5 客户端运行 19 分钟诊断

```bash
cd ~/model-velo
git fetch origin
COMMIT=REPLACE_WITH_COMMIT_SHA
git checkout "$COMMIT"

cp -n test/threehost/benchmark.env.example test/threehost/benchmark.env
editor test/threehost/benchmark.env
```

至少填写：

```text
GATEWAY_URL=http://网关机私网IP:8080
UPSTREAM_URL=http://假上游机私网IP:9000
MODEL_VELO_API_KEY=刚刚生成的APIKey
RUN_ID=diag-usagefix-r1-20260727T230000Z
```

运行：

```bash
bash test/threehost/run-diagnostic-client.sh
```

该脚本固定运行：

1. smoke；
2. 100 RPS direct/gateway 预热；
3. 500、750、1000、1500 RPS，各 2 分钟；
4. 500 RPS、10 分钟耐久。

诊断脚本会把 k6 最大 VU 提高到 4096，并关闭 Payload、Cache、Fault、Queue overload
等非热路径 case，因此约 19 分钟。

### 4.6 客户端结束后收集最终证据

网关机第三个终端：

```bash
cd ~/model-velo
export COMPOSE_PROJECT_NAME=mv-perf-d1
RUN_ID=diag-usagefix-r1-20260727T230000Z

bash test/threehost/collect-run-evidence.sh "$RUN_ID"
```

这一步不能省略。它按本轮 `bench-$RUN_ID` 等待 Outbox 排空，并保存最终数据库、
Redis、日志、运行参数和 Worker 指标。

先检查：

```bash
cat "test-results/threehost/$RUN_ID/gateway-evidence/usage-drain.txt"
cat "test-results/threehost/$RUN_ID/gateway-evidence/usage-overview.csv"
cat "test-results/threehost/$RUN_ID/gateway-evidence/usage-outbox.csv"
cat "test-results/threehost/$RUN_ID/gateway-evidence/redis-usage.txt"
```

客户端完成且 evidence 已保存后，让监控自然结束；需要提前结束时使用 `Ctrl+C`，不要
关闭 SSH 窗口强杀进程。正式最终轮最好让状态成为 `completed`。

### 4.7 把三台结果合并到客户端

在客户端机执行，把主机别名改成自己的 SSH 地址：

```bash
RUN_ID=diag-usagefix-r1-20260727T230000Z
RESULT_DIR="test-results/threehost/$RUN_ID"

scp -r gateway-host:"~/model-velo/$RESULT_DIR/gateway-stats.jsonl" \
  "$RESULT_DIR/"
scp -r gateway-host:"~/model-velo/$RESULT_DIR/gateway-stats-metadata.txt" \
  "$RESULT_DIR/"
scp -r gateway-host:"~/model-velo/$RESULT_DIR/gateway-stats-status.json" \
  "$RESULT_DIR/"
scp -r gateway-host:"~/model-velo/$RESULT_DIR/prometheus" \
  "$RESULT_DIR/"
scp -r gateway-host:"~/model-velo/$RESULT_DIR/gateway-evidence" \
  "$RESULT_DIR/"
scp -r upstream-host:"~/model-velo/$RESULT_DIR/upstream-stats.jsonl" \
  "$RESULT_DIR/"
scp -r upstream-host:"~/model-velo/$RESULT_DIR/upstream-stats-metadata.txt" \
  "$RESULT_DIR/"
scp -r upstream-host:"~/model-velo/$RESULT_DIR/upstream-stats-status.json" \
  "$RESULT_DIR/"

python3 test/threehost/summarize.py "$RESULT_DIR"
tar -czf "$RUN_ID.tar.gz" -C test-results/threehost "$RUN_ID"
```

浏览器打开：

```text
test-results/threehost/<RUN_ID>/summary.html
```

交付分析时只需要 `<RUN_ID>.tar.gz`，不需要复制整个仓库。

## 5. D1 必须通过的门槛

### 5.1 正确性门槛

任何一项失败都先修正确性，不继续调 RPS：

| 检查项 | D1 期望 |
| --- | --- |
| `usage-drain.txt` | `state=complete`、`remaining_outbox=0` |
| Usage 对账 | 正常网关 case 的请求数与事件数接近 100% |
| `events` 与 `request_ids` | 相等，不应一条请求多条最终事件 |
| Worker duplicate | 健康新环境中应为 0 |
| Worker failed/dead-letter | 0 |
| Redis pending | 最终为 0 |
| Redis Stream | 测试后能被消费删除，不再持续增长到几十万、几百万 |
| 网关日志 | 没有 `usage cache status invalid` |
| 503 错误码 | 不再由 `usage_accounting_unavailable` 主导 |

对账不要求把 smoke、健康检查、被认证/限流提前拒绝的请求强行算成 Chat Usage Event。
以 `summary.html` 的 Usage reconciliation 口径为准。

### 5.2 第一阶段性能目标

这是当前硬件上的工程目标，不是对所有云服务器都成立的行业标准：

| 场景 | 合格目标 |
| --- | --- |
| 500 RPS、10 分钟 | 成功率不低于 99.9%，dropped 为 0，Usage 可排空 |
| 750 RPS | 成功率不低于 99.9%，dropped 为 0，P99 小于 500 ms |
| 1000 RPS | 观察拐点；争取成功率不低于 99%，P99 小于 1 s |
| 1500 RPS | 用于观察过载行为，不要求 100% 成功 |

修好持久化后实际做了更多正确工作，RPS 不一定立刻上升。只要 Usage 正确且阶段指标能明确
说明成本，就比“忽略落库后得到更高 QPS”更可信。

## 6. 看完 HTML 后如何决定改什么

先按下面顺序排除外部瓶颈，再看网关：

| 证据 | 判断 | 下一步 |
| --- | --- | --- |
| direct 也变慢或失败 | 假上游、网络或客户端问题 | 不改网关，先修测试环境 |
| 客户端 CPU 接近满载或 dropped 随客户端资源上涨 | k6 发不出目标流量 | 提升客户端规格或 VU，不能算网关失败 |
| 假上游 CPU 高、`max_active` 异常、direct 恶化 | 假上游成为瓶颈 | 提升上游机，保持场景不变重测 |
| `usage_begin/usage_finalize` P95/P99 高，PostgreSQL waits 增长 | Usage/数据库热路径 | 优先查事务、SQL 次数和连接池 |
| `authentication/authorization` 高，PostgreSQL waits 增长 | 每请求认证和授权查询成为瓶颈 | 做有界认证/授权缓存并明确撤销延迟 |
| `rate_limit` 高，Redis wait/timeout 增长 | Redis 热路径或池耗尽 | 先看 Redis CPU，再决定是否调池或代码 |
| `provider_queue` waiting 高且 active 达到 256 | Provider 并发准入限制 | 判断上游是否有余量，再试 Queue |
| `provider_call` 上升，direct 同时上升 | 上游服务时间增加 | 不归因于网关 |
| 网关 CPU 很高，但连接池和各等待阶段都低 | CPU、分配或锁竞争 | 跑本地 benchmark/pprof 后改代码 |
| Worker pending/lag 增长，网关请求仍正常 | Usage Worker 吞吐不足 | 看 batch、数据库写入和 Redis，而不是 Queue |
| 1500 RPS 延迟不断堆积但 CPU/上游已满 | 过载策略太宽松 | 缩短等待或减少 waiting，快速拒绝 |

关键指标包括：

```text
model_velo_request_stage_duration_seconds
model_velo_postgres_connections
model_velo_postgres_waits_total
model_velo_postgres_wait_duration_seconds_total
model_velo_redis_pool_connections
model_velo_redis_pool_events_total
model_velo_redis_pool_wait_duration_seconds_total
model_velo_provider_queue_active
model_velo_provider_queue_waiting
model_velo_usage_worker_events_total
model_velo_usage_worker_pending
process_cpu_seconds_total
process_resident_memory_bytes
go_memstats_heap_inuse_bytes
go_goroutines
```

不要只看某一张 CPU 图就下结论。至少要同时有客户端、网关、上游和请求阶段四类证据。

## 7. 条件式优化实验

每次只选择下面一个实验。修改 `.env` 后保留同一个 Compose 项目和 API Key，但换新的
`RUN_ID`。前一轮必须已经满足 `remaining_outbox=0`、Redis pending 为 0，才能复用环境。

重建网关和 Worker：

```bash
cd ~/model-velo
export COMPOSE_PROJECT_NAME=mv-perf-d1
editor .env
docker compose up -d --force-recreate gateway usage-worker
docker compose ps
curl http://127.0.0.1:8080/readyz
curl http://127.0.0.1:9091/readyz
```

然后重复“启动三个监控 → 更新客户端 `RUN_ID` → `run-diagnostic-client.sh` →
`collect-run-evidence.sh` → 合并结果”的流程。

### 7.1 PostgreSQL 连接池实验

只有在下面条件同时出现时才做：

- `model_velo_postgres_connections{state="in_use"}` 经常接近 50；
- `model_velo_postgres_waits_total` 和 wait duration 在高 RPS case 明显增长；
- PostgreSQL 容器 CPU、内存和连接数仍有余量。

候选值：

```text
MODEL_VELO_POSTGRES_MAX_OPEN_CONNS=100
MODEL_VELO_POSTGRES_MAX_IDLE_CONNS=25
```

验收不是“连接数更多”，而是：

- authentication、authorization、usage begin/finalize 延迟下降；
- 1000 RPS 成功率或 P99 改善；
- PostgreSQL CPU 没有被推到满载；
- Usage 仍然完整对账。

如果 wait 没下降或 PostgreSQL CPU/延迟更差，恢复 50/10。

### 7.2 Redis 连接池实验

只有 Redis 出现 `wait`、`timeout`、pending request 或 `rate_limit` 阶段明显升高时才做。

候选值：

```text
MODEL_VELO_REDIS_POOL_SIZE=200
MODEL_VELO_REDIS_MIN_IDLE_CONNS=20
```

如果 Redis 本身已经 CPU 满载，增加客户端连接只会让它更拥堵，此时不做这个实验。

### 7.3 Usage Worker 批量实验

只有网关请求正常，但 Worker pending/lag 增长、Outbox 排空慢，并且 PostgreSQL/Redis
还有余量时才做。

候选值：

```text
MODEL_VELO_USAGE_BATCH_SIZE=200
```

验收：

- Worker `stored` 能跟上本轮网关完成请求数；
- pending 和本轮 Outbox 在 240 秒内清零；
- duplicate、failed、dead-letter 仍为 0；
- PostgreSQL CPU 和事务延迟没有明显恶化。

不要一开始直接设到上限 1000。批量越大，单次事务更重，失败重试的工作量也更大。

### 7.4 Provider Queue 实验

`MODEL_VELO_QUEUE_MAX_IN_FLIGHT=256` 是每个 Provider、每个网关进程的上游并发数，
不是整个网关的总并发数。

只有 `provider_queue active` 达到 256、waiting 上升，而假上游仍有 CPU 和并发余量时，
才试：

```text
MODEL_VELO_QUEUE_MAX_IN_FLIGHT=512
```

`MAX_WAITING=2048` 不会提高吞吐，只决定过载时最多积压多少请求。若 1500 RPS 时吞吐已经
不再增加、P99 却涨到数秒，优先测试更快失败：

```text
MODEL_VELO_QUEUE_MAX_WAITING=512
```

如果仍然形成长尾，再单独测试：

```text
MODEL_VELO_QUEUE_WAIT_TIMEOUT=500ms
```

不要把 `MAX_IN_FLIGHT`、`MAX_WAITING` 和 timeout 同时改掉，否则无法判断是哪一个改变了
吞吐或尾延迟。

### 7.5 Breaker 和 Retry

Breaker 失败阈值 5 不影响健康 `mock/instant` 的吞吐，不作为 RPS 调优项。Retry 也不能
用来提高健康请求 RPS，它只会在失败场景增加 Attempt。

保持：

```text
MODEL_VELO_BREAKER_FAILURE_THRESHOLD=5
MODEL_VELO_RETRY_MAX_ATTEMPTS=3
```

它们只在完整套件的 10% 503、Retry、Fallback 和 SSE 故障测试里验收。

### 7.6 认证和授权代码优化

如果 D1 显示 authentication/authorization 是主要数据库成本，优先考虑带上限、带 TTL
的本地正向缓存，而不是先继续加数据库连接：

- Cache Key 不能保存明文 API Key；
- 缓存 Identity 和模型授权结果；
- 必须写清楚禁用/撤销 Key 的最大生效延迟；
- 负向结果使用更短 TTL 或不缓存；
- 容量有上限，避免租户/Key 数量导致内存无限增长；
- 认证失败、过期、撤销、租户禁用仍要有测试。

实现后先跑 `go test ./...` 和 `go vet ./...`，再运行新的 19 分钟 `D2`。面试时重点讲
“用阶段指标确认每请求数据库查询是瓶颈，以及如何处理缓存与撤销一致性的取舍”，不要只说
“加缓存提升性能”。

### 7.7 CPU 和内存代码优化

只有数据库、Redis、Queue 和上游都没有等待，而网关 CPU/GC 已经很高时才进入这一项。

先跑进程内回归 benchmark：

```bash
go test ./internal/httpapi \
  -run '^$' \
  -bench '^BenchmarkChatCompletions$' \
  -benchmem \
  -benchtime=10s \
  -count=5
```

记录中位 `ns/op`、`B/op`、`allocs/op`，再针对火焰图或 allocation profile 中最大的
位置修改。常见方向可能是请求体复制、JSON 编解码、日志字段和临时对象，但没有 profile
证据前不预设答案。

## 8. 每轮结果记录表

每拿到一轮 HTML，就补一行。不要只保存“最大 QPS”。

| Run ID | commit | 唯一变化 | 500 成功/P99 | 750 成功/P99 | 1000 成功/P99 | 500×10m | 网关 CPU max | PG wait | Redis wait | Queue wait | Usage 对账 | Outbox drain |
| --- | --- | --- | --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | --- |
| 第一轮 | 旧提交 | 修复前基线 | 99.987% / 271 ms | 99.959% / 353 ms | 97.044% / 1247 ms | 99.992% / 222 ms | 待补 | 待补 | 待补 | 待补 | 0% | 失败 |
| D1 |  | Usage 链路修复 |  |  |  |  |  |  |  |  |  |  |
| D2 |  |  |  |  |  |  |  |  |  |  |  |  |
| Final |  |  |  |  |  |  |  |  |  |  |  |  |

优化成立至少需要：

- 正确性门槛不退化；
- 相同负载下成功率、P95/P99、资源或可持续时间中至少一项明确改善；
- 没有把瓶颈从网关悄悄转移到客户端或假上游；
- 新结果至少能由第二轮或最终完整套件复现；
- 运行配置和 commit 都能从结果包中查到。

## 9. 最终 19 分钟确认

选定代码和参数后，使用新 `RUN_ID`：

```text
diag-final-r1-20260728T030000Z
```

完整重复第 4 节的 19 分钟流程。最终确认轮应使用：

- clean worktree；
- 最终 commit；
- 最终 `.env`；
- 已排空的 Usage；
- 三个状态正常的监控；
- 新的结果目录。

最终确认至少达到第 5 节正确性门槛，并且关键性能改善与上一轮方向一致。若某个改动只在
一次运行中好看，第二次消失，就不能作为面试中的优化结果。

## 10. 最终完整性能与故障套件

19 分钟诊断稳定后，才运行完整套件。它大约 1.5–2 小时，默认包含：

1. smoke 和 direct/gateway 预热；
2. 1–256 VU 闭合模型容量阶梯；
3. 100–2000 RPS 开放模型 SLO 阶梯；
4. 非流式与逐 Chunk SSE；
5. 200 B、10 KiB、50 KiB Payload；
6. Cache bypass、miss/write、warm/hit；
7. 爬坡和突发；
8. 10% 503、抖动、5% 长尾；
9. Queue 过载；
10. 30 分钟耐久；
11. Retry、Fallback、错误映射和 SSE 提交边界。

客户端重新从模板准备正式配置，恢复默认三次 repetitions：

```bash
cd ~/model-velo
cp test/threehost/benchmark.env.example test/threehost/benchmark.env
editor test/threehost/benchmark.env
bash test/threehost/run-complete-client.sh
```

至少填写：

```text
GATEWAY_URL=http://网关机私网IP:8080
UPSTREAM_URL=http://假上游机私网IP:9000
MODEL_VELO_API_KEY=测试APIKey
RUN_ID=complete-final-r1-20260728T050000Z
```

网关机和上游机采集命令与第 4 节相同，只把：

```text
DURATION_SECONDS=1800
```

改成：

```text
DURATION_SECONDS=9000
```

客户端完成后仍必须执行：

```bash
bash test/threehost/collect-run-evidence.sh complete-final-r1-20260728T050000Z
```

再按第 4.7 节合并文件并重新生成 `summary.html`。

完整套件的最终验收：

- smoke 全部通过；
- direct 路径稳定；
- 容量与 RPS 曲线存在清楚拐点，而不是随机抖动；
- Cache hit 确实减少上游调用；
- Retry/Fallback 的上游 Attempt 数与预期一致；
- SSE 首 Content、Chunk 间隔和断流行为正确；
- Queue 过载时错误有界，不出现无限排队；
- 30 分钟耐久无吞吐持续下降、内存持续增长或 Usage 积压；
- Usage 对账完整；
- 三台资源证据覆盖所有 case；
- HTML 不再报告关键 evidence 缺失。

## 11. Redis 限流独立测试

主容量测试把限流设为每分钟 1,000,000，避免 429 污染容量曲线。最终再单独测试限流，
并使用独立 `RUN_ID`。

网关 `.env` 临时设置：

```text
MODEL_VELO_RATE_LIMIT_REQUESTS=6000
MODEL_VELO_RATE_LIMIT_WINDOW=1m
```

重建网关：

```bash
export COMPOSE_PROJECT_NAME=mv-perf-d1
docker compose up -d --force-recreate gateway
curl http://127.0.0.1:8080/readyz
```

使用与第 4 节相同的三个监控命令，新 `RUN_ID` 统一设置为
`ratelimit-final-r1-20260728T080000Z`，`DURATION_SECONDS=600`。

客户端：

```bash
set -a
. test/threehost/benchmark.env
set +a

export RUN_ID=ratelimit-final-r1-20260728T080000Z
export RUN_CAPACITY=false
export RUN_WARMUP=false
export RUN_RATE_SWEEP=false
export RUN_STREAM_DETAIL=false
export RUN_PAYLOAD=false
export RUN_CACHE=false
export RUN_RAMP=false
export RUN_BURST=false
export RUN_FAULT=false
export RUN_QUEUE_OVERLOAD=false
export RUN_ENDURANCE=false
export RUN_RELIABILITY=false
export RUN_RATE_LIMIT=true
export RATE_LIMIT_TEST_RATE=200
export RATE_LIMIT_TEST_DURATION=2m
export PRE_ALLOCATED_VUS=256
export MAX_VUS=1024

bash test/threehost/run-complete-client.sh -
```

注意：如果 `benchmark.env` 里已经写了旧 `RUN_ID`，上面是在 source 之后重新 export，
所以会使用新的限流 Run ID。

验收：

- 同一 tenant + model 出现明确的 200/429；
- `model_velo_rate_limit_decisions_total` 与 HTTP 结果一致；
- 429 不调用假上游；
- 429 不触发 Retry；
- Redis 无 pool timeout；
- 测试完成后 Usage/Outbox 能排空。

完成后把限流恢复：

```text
MODEL_VELO_RATE_LIMIT_REQUESTS=1000000
```

并重新创建 gateway，防止后续测试意外继续使用低阈值。

## 12. 面试时怎么讲

不要背“用了 k6、Redis、Prometheus，所以高性能”。按真实调查过程讲：

### 12.1 一分钟版本

> 我把压测拆成三台机器：一台 k6、一台网关、一台可控制延迟和错误的假 LLM 上游。
> 先用 direct 请求测客户端到上游的基线，再用相同请求经过网关，这样能排除压测机和
> 假上游先跑满。第一轮在 500 到 750 RPS 比较稳定，1000 RPS 开始出现拐点，但我没有
> 直接把 Queue 调大，因为 Usage 证据显示事件根本没有落库，Redis Stream 反而增长到
> 约 607 万条。最后定位到 Cache 状态大小写不一致，以及 Outbox Relay 重复发布
> published 记录。修复后我用同一组 19 分钟负载复测，对比成功率、P99、阶段耗时、
> PostgreSQL/Redis 等待和 Usage 对账，再根据最大的阶段瓶颈做下一步优化。

### 12.2 为什么不是直接追最大 RPS

可以说：

> LLM 网关不能只看一个 QPS。除了吞吐，我同时看固定到达率下的成功率、P95/P99、
> dropped iterations、过载时是否快速失败、SSE 首 Token、Retry 是否放大上游调用，
> 以及 Usage 是否最终完整落库。否则关闭计费或让请求在后台堆积，也能做出一个很高但
> 没意义的数字。

### 12.3 为什么 Queue 不是越大越好

可以说：

> Queue 的 in-flight 是每个 Provider 的并发准入，不是网关总并发。waiting 只允许更多
> 请求排队，不会增加上游吞吐。如果上游或数据库已经满了，继续增大 waiting 只会把 P99
> 拉长。我只在 active 达到上限、上游还有余量时增加 in-flight；如果吞吐不再增长，就
> 缩短等待，让过载请求尽快失败。

### 12.4 为什么保留 Usage 的 fail-closed

可以说：

> 第一轮最容易得到高数字的办法是 Usage 写失败时继续放请求，但这会造成计费缺失。
> 我保留了请求开始时的持久化边界，优化的是多余事务、重复发布和 Worker 批量落库。
> 性能测试同时做请求数与 Usage Event 对账，确保吞吐提升不是靠丢数据换来的。

### 12.5 最终数据怎么填

最终只说结果中已经验证的数字：

```text
修复前最高稳定点：
修复后最高稳定点：
500 RPS / 10 分钟成功率：
750 RPS P99：
1000 RPS 成功率和 P99：
网关 CPU 峰值：
PostgreSQL wait 变化：
Redis wait 变化：
Usage 对账比例：
Outbox 排空时间：
最终优化的 commit：
```

最后说明测试边界：

> 这些结果来自固定规格的三台云服务器和确定性假上游，用于比较 Model-Velo 自己的版本
> 变化。我没有在同一硬件上运行其他网关，所以不会声称绝对快于某个项目；我能证明的是
> 测试口径可复现、瓶颈有证据、优化前后能对账。

这比引用其他项目 README 里的 QPS 更可信，也更容易在追问时解释清楚。
