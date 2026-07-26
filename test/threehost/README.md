# 三台云服务器端到端测试

这套测试用于验证“客户端 → Model-Velo → 假 LLM 上游”的完整生产链路。它不是单元测试，
也不访问真实付费模型。

本目录采用了两个参考项目实际使用的测试思路：

- GoModel 的 AWS benchmark 先测直连 mock 基线，再测网关链路，并保存原始结果、摘要和
  宿主机资源数据；
- Bifrost 的 benchmark 同时区分固定 RPS 与固定并发，mock 请求支持延迟、流式和故障，
  每个请求写入唯一内容以避开缓存。

Model-Velo 的脚本和假上游均按自身接口独立实现，没有复制参考项目源码、目录或错误文案。

## 目标、非目标和验收条件

目标：

- 三台服务器只替换私网 IP 和 Model-Velo API Key 即可运行；
- 同一参数分别直连假上游和经过网关，得到端到端增量；
- 固定 RPS 与固定并发分开执行；
- 覆盖非流式、逐 Chunk SSE、Retry、Fallback、429、5xx、首事件错误和提交后断流；
- 覆盖容量拐点、目标 RPS、爬坡、突发、耐久、Payload、缓存、Queue 和可控故障；
- k6/streamload 摘要与三台主机元数据、容器资源、Prometheus、Usage 和日志能够落盘。

非目标：

- 结果不代表真实模型吞吐或供应商 SLA；
- 三机差值包含不同网络路径，不是纯粹的网关 CPU 开销；
- 本套脚本不自动修改云防火墙，也不把 PostgreSQL、Redis 或管理接口暴露到公网；
- k6 会缓冲完整响应，所以 k6 的 `stream_first_byte_ms` 只作粗略代理；完整套件使用
  Go `streamload` 逐行读取 SSE，区分响应头、首事件、首内容和 Chunk 间隔。
- 套件只运行 Model-Velo 和它自己的依赖，不下载、启动或比较其他网关。

一次有效测试至少满足：

1. `smoke` 的所有 checks 通过；
2. 固定 RPS 运行的 `dropped_iterations` 为 0，否则说明客户端机 VU 预分配不足；
3. 声称“可持续”的目标 RPS 点达到至少 99.9% 成功率；故障和过载用例不套用此门槛；
4. `reliability_success` 为 100%；
5. 网关机和假上游机各产生一个非空 stats JSONL 和 metadata 文件；
6. 报告同时记录 commit、三台机器规格、区域、网络、k6 参数和全部摘要，不能只写 QPS。

## 拓扑与防火墙

三台服务器应使用同一区域、同一私网，测试期间不要让其他业务共享它们。
网关机和假上游机需要 Linux、Git、Bash、OpenSSL 及 Docker Compose v2；客户端机需要
Linux、Git、Bash、k6、Go 和 Python 3。关闭逐 Chunk SSE 用例时客户端才可以不装 Go。
三台机器都应启用时间同步。

| 来源 | 目标 | TCP 端口 | 用途 |
| --- | --- | ---: | --- |
| 客户端机 | 网关机 | 8080 | k6 经过 Model-Velo |
| 客户端机 | 假上游机 | 9000 | 直连基线、健康检查和 reset |
| 网关机 | 假上游机 | 9000–9002 | 主链路、强制失败 Provider、Fallback Provider |
| 管理电脑 | 三台服务器 | 22 | SSH |

只在云安全组中允许上述私网来源。5432、6379、9091 不对另外两台机器开放。假上游没有管理
鉴权，9000–9002 不能暴露给整个公网。

## 1. 假上游机

签出与网关相同的 commit，然后启动三个容器。它们仍然位于同一台“假上游机”：

- `main:9000`：按请求的 `mock/*` 模型动态选择场景；
- `fail:9001`：所有请求固定返回 503；
- `fallback:9002`：所有请求固定成功。

```bash
git clone <model-velo-repository> model-velo
cd model-velo
git checkout <commit>

docker compose -f test/threehost/upstream.compose.yaml build main
docker compose -f test/threehost/upstream.compose.yaml up -d --no-build

curl http://127.0.0.1:9000/healthz
curl http://127.0.0.1:9001/healthz
curl http://127.0.0.1:9002/healthz
```

`upstream.compose.yaml` 只通过 `main` 构建一次
`model-velo-fake-upstream:local`，再复用该镜像启动三份进程，不需要为每个 Provider
构建不同镜像或手写一套服务。

## 2. 网关机

签出同一 commit，复制输入模板，并把三个 URL 改成假上游机的私网 IP：

```bash
git clone <model-velo-repository> model-velo
cd model-velo
git checkout <commit>

cp test/threehost/gateway.env.example test/threehost/gateway.env
editor test/threehost/gateway.env
bash test/threehost/prepare-gateway-env.sh
```

`prepare-gateway-env.sh` 会拒绝覆盖已有 `.env`，自动生成 PostgreSQL、Redis、Pepper、
控制面主密钥和指标 Token，并写入测试路由。需要有意识地重建测试环境时才使用
`FORCE=true`；重建 `.env` 后旧 API Key 会因 Pepper 改变而失效。

启动网关、Usage Worker 及其本机 PostgreSQL/Redis：

```bash
docker compose up -d --build gateway usage-worker
docker compose ps
curl http://127.0.0.1:8080/readyz
```

创建仅用于本次测试的租户与 API Key：

```bash
docker compose --profile tools run --rm admin bootstrap-tenant \
  --slug threehost \
  --name "Three Host Benchmark" \
  --label "k6" \
  --models "mock/instant,mock/typical,mock/slow,mock/jitter,mock/spike-5,mock/error-rate-10,mock/payload-10k,mock/payload-50k,mock/retry-2,mock/error-400,mock/error-429,mock/error-503,mock/sse-error,mock/sse-drop,mock/fallback"
```

保存命令只显示一次的 `api_key`。不要把它写入提交、日志或测试报告。根
`compose.yaml` 默认仍只绑定 `127.0.0.1`；本测试生成的 `.env` 明确设置
`MODEL_VELO_HTTP_BIND=0.0.0.0`，所以必须先限制云防火墙来源。

## 3. 客户端机

安装 k6，签出相同 commit，然后准备本地客户端配置：

```bash
git clone <model-velo-repository> model-velo
cd model-velo
git checkout <commit>

cp test/threehost/client.env.example test/threehost/client.env
editor test/threehost/client.env
```

填写网关私网 URL、假上游私网 URL、上一步生成的 API Key 和统一的 `RUN_ID`。
`client.env` 已被 `.gitignore` 排除。

默认运行顺序是：

1. smoke；
2. 直连和网关各一次低 RPS 预热；
3. 固定 RPS：非流式 direct → gateway，流式 direct → gateway；
4. 固定 VU：非流式 direct → gateway，流式 direct → gateway；
5. 可靠性断言。

```bash
bash test/threehost/run-client.sh
```

结果写入 `test-results/threehost/<RUN_ID>/`。每个 case 都有 k6 summary JSON 和文本日志；
`SAVE_RAW_METRICS=true` 时还会保存逐指标 JSON，文件会明显变大。

### 完整性能与故障套件

`run-client.sh` 适合先验证三机连通性。正式测试改用完整配置：

```bash
cp test/threehost/benchmark.env.example test/threehost/benchmark.env
editor test/threehost/benchmark.env
bash test/threehost/run-complete-client.sh
```

只需要填写 `GATEWAY_URL`、`UPSTREAM_URL`、`MODEL_VELO_API_KEY` 和统一的 `RUN_ID`。
默认约运行 1.5–2 小时，并依次产生：

1. smoke 和两条路径预热；
2. `1–256 VU × direct/gateway × 3` 的闭合模型容量阶梯；
3. `100–2000 RPS × gateway × 3` 的开放模型 SLO 阶梯；
4. `500 请求 × direct/gateway × 3` 的逐 Chunk SSE TTFT/总时长/间隔；
5. 200 B、10 KiB、50 KiB 请求和响应体的成对测试；
6. cache bypass、唯一 Key miss/write、共享 Key warm/hit；
7. 爬坡、突发、10% 503、延迟抖动、5% 长尾、Queue 过载；
8. 30 分钟耐久运行和已有的 Retry/Fallback/SSE 可靠性门禁。

每个 case 都在 `cases.tsv` 记录精确起止时间，运行前清空假上游统计，运行后保存
`*-upstream.json`。这样可以把客户端延迟、网关 Prometheus、容器 CPU/RSS 和真实上游
Attempt 数对齐，而不靠终端截图。错误和过载用例采用宽松阈值继续执行，非零退出码仍会
单独保存并出现在汇总中；只有 smoke 失败会立即停止整个套件。
runner 还会自动写入 `client-stats.jsonl`，用于判断高并发结果是否先被 k6 客户端 CPU
或内存限制。

需要缩短首次试跑时，可以在 `benchmark.env` 里关闭长用例：

```text
REPETITIONS=1
RUN_ENDURANCE=false
RUN_PAYLOAD=false
```

正式结果恢复模板默认值。完整套件不会修改网关参数；先用固定配置找到瓶颈，再针对已定位
的 Queue、Redis、连接池或 Worker 参数做单变量复测。

### 如何选择 RATE、VU 和预分配

`LOAD_MODE=rate` 使用开放模型：无论网关变慢与否，都按目标 RPS 发起迭代，适合观察固定
流量下的排队、失败和延迟。`LOAD_MODE=vus` 使用闭合模型：固定并发用户完成一次请求后再发
下一次，适合观察并发连接和最大吞吐。二者回答的问题不同，不能把 `VUS=50` 写成
“50 RPS”。

固定 RPS 所需 VU 可先按下面的近似值配置：

```text
PRE_ALLOCATED_VUS >= RATE × 直连 p95 秒数 × 1.2
```

若 `dropped_iterations > 0`，先提高客户端机性能或 VU 预分配，不能把丢失的迭代归因于
网关。正式报告建议 `REPETITIONS=3`，并保持三台实例、区域、镜像和参数不变。

## 同时采集网关与假上游资源

在客户端开始前，在另外两台服务器的终端中运行采集器。`DURATION_SECONDS` 应覆盖预热、
所有 repetitions、冷却间隔和可靠性测试。

网关机：

```bash
RUN_ID=20260725T120000Z
DURATION_SECONDS=9000 \
OUTPUT_FILE="test-results/threehost/$RUN_ID/gateway-stats.jsonl" \
bash test/threehost/collect-compose-stats.sh
```

在网关机的第二个终端同时采集网关与 Usage Worker 指标：

```bash
RUN_ID=20260725T120000Z
DURATION_SECONDS=9000 \
OUTPUT_DIR="test-results/threehost/$RUN_ID/prometheus" \
bash test/threehost/collect-prometheus.sh
```

假上游机：

```bash
RUN_ID=20260725T120000Z
DURATION_SECONDS=9000 \
COMPOSE_FILE=test/threehost/upstream.compose.yaml \
SERVICES=main,fail,fallback \
OUTPUT_FILE="test-results/threehost/$RUN_ID/upstream-stats.jsonl" \
bash test/threehost/collect-compose-stats.sh
```

采集器按 `INTERVAL_SECONDS` 使用一次 `docker stats --no-stream` 批量记录所有目标容器的
CPU、内存、网络和块 I/O，同时单独记录服务到容器 ID 的映射、镜像 ID、Docker 版本和
宿主机信息；它不会执行 `docker compose config`，避免把 `.env` 密钥写入结果。

客户端套件结束后，在网关机保存一次可直接诊断的最终证据：

```bash
bash test/threehost/collect-run-evidence.sh 20260725T120000Z
```

它保存四个服务的末尾日志、容器/镜像、最终指标、Redis Stream/PENDING/Dead Letter，
并按 `bench-<RUN_ID>` 查询 Usage 的事件数、状态、模型、缓存、Attempts、Retries、
Fallbacks、平均/P95 延迟和 TTFT。查询前最多等待 240 秒，让本次 Outbox 和 Redis
Stream 排空，并把是否超时写入 `usage-drain.txt`。脚本不会输出 `.env` 或数据库/Redis
密码；`runtime-settings.txt` 只记录连接池、限流、Cache、Breaker、Queue、Retry、
Timeout 和 Worker 等安全标量，便于将结果对应回实际参数。

三个采集进程不必精确和客户端同时结束；`9000` 秒覆盖默认完整套件，多余的空闲采样不影响
按 case 时间戳分析。如果自定义套件超过 2.5 小时，相应增大 `DURATION_SECONDS`。

## 合并并交付结果

在客户端机上，把另外两台机器同一 `RUN_ID` 下的目录复制进客户端结果目录。目标子目录名
不限，只要保留文件名即可：

```bash
RUN_ID=20260725T120000Z
RESULT_DIR="test-results/threehost/$RUN_ID"

scp -r gateway-host:"~/model-velo/$RESULT_DIR/gateway-stats.jsonl" \
  "$RESULT_DIR/"
scp -r gateway-host:"~/model-velo/$RESULT_DIR/gateway-stats-metadata.txt" \
  "$RESULT_DIR/"
scp -r gateway-host:"~/model-velo/$RESULT_DIR/prometheus" \
  "$RESULT_DIR/"
scp -r gateway-host:"~/model-velo/$RESULT_DIR/gateway-evidence" \
  "$RESULT_DIR/"
scp -r upstream-host:"~/model-velo/$RESULT_DIR/upstream-stats.jsonl" \
  "$RESULT_DIR/"
scp -r upstream-host:"~/model-velo/$RESULT_DIR/upstream-stats-metadata.txt" \
  "$RESULT_DIR/"

python3 test/threehost/summarize.py "$RESULT_DIR"
tar -czf "$RUN_ID.tar.gz" -C test-results/threehost "$RUN_ID"
```

把最后的 `<RUN_ID>.tar.gz` 给分析者即可。`summary.md` 是人读摘要，`summary.json` 保留
全部机器可读 case、分位数、状态计数、直连差值、逐 Chunk SSE、资源、Prometheus 和
Usage 证据。原始 `*.log`、`*-summary.json`、`*-stream.json`、`*-upstream.json` 和
带时间戳采样也都保留；下一轮可以据此定位是客户端 VU、网关 CPU/Queue/Redis、Usage
Worker、上游放大还是长尾问题，再改对应代码。
汇总还会把正常网关 case 的实际 HTTP 请求数和落库 Usage Event 数对账；smoke、
reliability 和独立限流用例因包含非 Chat 请求或前置拒绝，不纳入该等式。

不要把 `benchmark.env`、根 `.env`、API Key、私钥或整个仓库一起打包。

### Redis 限流单独复测

主容量测试使用很高的限流上限，避免限流污染容量曲线。限流吞吐必须使用同一网关程序、
单独的配置运行：

1. 在网关 `.env` 把 `MODEL_VELO_RATE_LIMIT_REQUESTS` 调成明确的测试阈值并重启
   `gateway`；不要重新生成全部密钥；
2. 用新的 `RUN_ID` 复制 `benchmark.env`，把其他 `RUN_*` 设为 `false`，仅保留
   `RUN_RATE_LIMIT=true`；
3. 将 `RATE_LIMIT_TEST_RATE` 设置为高于每秒折算阈值的流量，执行完整 runner；
4. 再运行 `collect-run-evidence.sh`，确认 200/429 数量与
   `model_velo_rate_limit_decisions_total` 一致。

它是一组独立结果，不能和不限流容量曲线混在同一个 `RUN_ID` 中。

## 结果解释

对于同一个 case，先看 direct 是否稳定，再看 gateway：

- `http_req_duration`：客户端观察到的完整请求耗时；
- `http_req_waiting`：首字节时间；
- `stream_first_byte_ms`：流式请求的同一首字节值，仅在本拓扑下视为 TTFT 代理；
- `first_content`：`streamload` 实际解析到首个非空 `delta.content` 的时间；
- `inter_chunk`：相邻 SSE 事件的间隔分布；
- `iterations` 与 `iteration_duration`：完整业务迭代；
- `dropped_iterations`：固定 RPS 时客户端没有足够 VU 发起的迭代；
- `chat_success`：状态码和 OpenAI 响应/SSE 终止标志共同校验的成功率；
- `reliability_success`：预期 Retry、Fallback 和错误映射全部满足的比例。

三机 direct 路径是“客户端 → 假上游”，gateway 路径是“客户端 → 网关 → 假上游”。
两者差值包含额外网络跳数、认证、限流、路由、Queue、Retry 准入、Usage 和网关 HTTP
处理。它适合回答真实部署拓扑的端到端成本，但不能单独证明某段 Go 代码耗时多少；纯进程
回归继续使用 `docs/benchmark.md` 中的本地 Go benchmark。

## 文件职责

| 文件 | 职责 |
| --- | --- |
| `test/k6/smoke.js` | 上游健康、网关就绪、非流式和 SSE 最小链路 |
| `test/k6/load.js` | 参数化固定 RPS/固定 VU 负载，不按并发阶段复制脚本 |
| `test/k6/profile.js` | 爬坡和突发 arrival-rate/VU 曲线 |
| `test/k6/fault.js` | 可配置预期状态集合的故障、Queue 和限流压力 |
| `test/k6/reliability.js` | Retry、Fallback、错误归一化和 SSE 提交边界 |
| `test/k6/common.js` | 唯一请求、no-store、OpenAI 响应校验和共享指标 |
| `test/fakeupstream` | 确定性 OpenAI-compatible 假服务 |
| `test/streamload` | 不缓冲响应的 Go SSE 负载器 |
| `upstream.compose.yaml` | 在一台上游机启动动态、失败和 Fallback 三个端口 |
| `prepare-gateway-env.sh` | 生成不入库的三机网关 `.env` |
| `run-client.sh` | 固定顺序运行并保存 k6 结果 |
| `run-complete-client.sh` | 执行完整矩阵、保存 case 时间与上游计数 |
| `collect-host-stats.sh` | 自动采集客户端 CPU、内存、Load 和负载进程 RSS |
| `collect-compose-stats.sh` | 在被测主机本地采集容器资源 |
| `collect-prometheus.sh` | 连续采集网关和 Worker 的低基数指标 |
| `collect-run-evidence.sh` | 保存日志、Usage、Redis 和最终运行状态 |
| `summarize.py` | 合并三机证据并生成 Markdown/JSON 诊断摘要 |
