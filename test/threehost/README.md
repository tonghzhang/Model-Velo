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
- 覆盖非流式、SSE、Retry、Fallback、429、5xx、首事件错误和提交后断流；
- k6 摘要与三台主机元数据、容器 CPU/内存采样能够落盘。

非目标：

- 结果不代表真实模型吞吐或供应商 SLA；
- 三机差值包含不同网络路径，不是纯粹的网关 CPU 开销；
- 本套脚本不自动修改云防火墙，也不把 PostgreSQL、Redis 或管理接口暴露到公网；
- k6 会缓冲完整响应，不提供逐 Chunk 间隔。`stream_first_byte_ms` 仅在 Model-Velo
  “收到首个合法 SSE 事件才提交响应头”的实现下作为 TTFT 代理值。

一次有效测试至少满足：

1. `smoke` 的所有 checks 通过；
2. 固定 RPS 运行的 `dropped_iterations` 为 0，否则说明客户端机 VU 预分配不足；
3. 正常负载的 `chat_success` 达到配置值，默认至少 99%；
4. `reliability_success` 为 100%；
5. 网关机和假上游机各产生一个非空 stats JSONL 和 metadata 文件；
6. 报告同时记录 commit、三台机器规格、区域、网络、k6 参数和全部摘要，不能只写 QPS。

## 拓扑与防火墙

三台服务器应使用同一区域、同一私网，测试期间不要让其他业务共享它们。
网关机和假上游机需要 Linux、Git、Bash、OpenSSL 及 Docker Compose v2；客户端机需要
Linux、Git、Bash 和 k6。三台机器都应启用时间同步。

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
docker compose -f test/threehost/upstream.compose.yaml up -d --build

curl http://127.0.0.1:9000/healthz
curl http://127.0.0.1:9001/healthz
curl http://127.0.0.1:9002/healthz
```

`upstream.compose.yaml` 复用一个镜像启动三份进程，不需要为每个 Provider 手写一套服务。

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
  --models "mock/instant,mock/typical,mock/slow,mock/retry-2,mock/error-400,mock/error-429,mock/error-503,mock/sse-error,mock/sse-drop,mock/fallback"
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
DURATION_SECONDS=900 \
SERVICES=gateway,usage-worker \
OUTPUT_FILE="test-results/threehost/$RUN_ID/gateway-stats.jsonl" \
bash test/threehost/collect-compose-stats.sh
```

假上游机：

```bash
RUN_ID=20260725T120000Z
DURATION_SECONDS=900 \
COMPOSE_FILE=test/threehost/upstream.compose.yaml \
SERVICES=main,fail,fallback \
OUTPUT_FILE="test-results/threehost/$RUN_ID/upstream-stats.jsonl" \
bash test/threehost/collect-compose-stats.sh
```

采集器按 `INTERVAL_SECONDS` 使用一次 `docker stats --no-stream` 批量记录所有目标容器的
CPU、内存、网络和块 I/O，同时单独记录服务到容器 ID 的映射、镜像 ID、Docker 版本和
宿主机信息；它不会执行 `docker compose config`，避免把 `.env` 密钥写入结果。

## 结果解释

对于同一个 case，先看 direct 是否稳定，再看 gateway：

- `http_req_duration`：客户端观察到的完整请求耗时；
- `http_req_waiting`：首字节时间；
- `stream_first_byte_ms`：流式请求的同一首字节值，仅在本拓扑下视为 TTFT 代理；
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
| `test/k6/reliability.js` | Retry、Fallback、错误归一化和 SSE 提交边界 |
| `test/k6/common.js` | 唯一请求、no-store、OpenAI 响应校验和共享指标 |
| `test/fakeupstream` | 确定性 OpenAI-compatible 假服务 |
| `upstream.compose.yaml` | 在一台上游机启动动态、失败和 Fallback 三个端口 |
| `prepare-gateway-env.sh` | 生成不入库的三机网关 `.env` |
| `run-client.sh` | 固定顺序运行并保存 k6 结果 |
| `collect-compose-stats.sh` | 在被测主机本地采集容器资源 |
