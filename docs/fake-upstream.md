# 可编程假 LLM 上游

`model-velo-fake-upstream` 是阶段 6 benchmark 与故障演示使用的
OpenAI-compatible 测试服务。它不访问真实 Provider，也不把模拟结果解释为真实模型性能。

## 启动

在假上游电脑上运行：

```bash
go run ./test/fakeupstream -addr 0.0.0.0:9000 -name primary
```

三台云服务器的完整启动、路由、k6 和资源采集流程见
[`test/threehost/README.md`](../test/threehost/README.md)。

健康检查和场景清单：

```bash
curl http://127.0.0.1:9000/healthz
curl http://127.0.0.1:9000/__admin/scenarios
```

Model-Velo 使用自定义 OpenAI-compatible Provider，并把 `base_url` 指向假上游主机：

```text
MODEL_VELO_ROUTING_JSON={"providers":[{"id":"mock-primary","type":"openai-compatible","vendor":"custom","base_url":"http://192.168.1.20:9000","models":["*"],"model_capabilities":{"*":["text"]}}],"routes":[{"model":"*","candidates":[{"provider":"mock-primary"}]}]}
MODEL_VELO_PROVIDER_KEYS_JSON={"providers":[{"provider_id":"mock-primary","keys":[{"id":"test","secret":"not-a-real-key"}]}]}
```

这个服务没有管理鉴权，只应监听测试网或防火墙允许的地址，不能直接暴露到公网。

## 场景

请求模型不是 `mock/` 前缀时，默认使用 `mock/instant`。模型名也可以直接选择场景：

| 模型 | 行为 |
| --- | --- |
| `mock/instant` | 立即返回确定性响应 |
| `mock/typical` | TTFT 200 ms，16 个流式内容 Chunk，间隔 20 ms |
| `mock/slow` | TTFT 1 s，64 个流式内容 Chunk，间隔 50 ms |
| `mock/jitter` | 按 request ID 确定性产生 25–175 ms 延迟 |
| `mock/spike-5` | 100 ms 基线，按 request ID 确定性产生 5% 的 2 s 长尾 |
| `mock/error-rate-10` | 按 request ID 确定性返回 10% HTTP 503 |
| `mock/payload-10k` | 返回 10 KiB assistant 文本 |
| `mock/payload-50k` | 返回 50 KiB assistant 文本 |
| `mock/error-400` | 返回 HTTP 400 |
| `mock/error-401` | 返回 HTTP 401 |
| `mock/error-429` | 返回 HTTP 429 和 `Retry-After: 1` |
| `mock/error-500` | 返回 HTTP 500 |
| `mock/error-503` | 返回 HTTP 503 |
| `mock/retry-2` | 相同 `X-Request-ID` 前两次返回 500，第三次成功 |
| `mock/sse-error` | 首个 SSE 事件携带错误对象 |
| `mock/sse-drop` | 输出第一个内容 Chunk 后断开 HTTP/1.1 连接 |

非流式请求等待同一场景的完整生成时间后一次性返回；流式请求分别模拟 TTFT 和
Chunk 间隔。`mock/retry-2` 的状态按 `X-Request-ID` 隔离，避免并发 k6 VU
互相消费对方的重试序列。可使用下面的接口清空尚未完成的序列：

```bash
curl -X POST http://127.0.0.1:9000/__admin/reset
```

`reset` 也会清空当前用例的统计。运行结束后读取：

```bash
curl http://127.0.0.1:9000/__admin/stats
```

返回值包含请求数、完成数、HTTP 错误数、流式请求/断流数、当前和峰值并发，以及各场景
计数。三机 runner 会在每个 case 前 reset、结束后保存 stats，因此能够区分网关接收的
请求数和实际放大到上游的 Attempt 数。

## 模拟两个 Provider

同一个二进制启动两份即可，不需要为每个模型写一个服务：

```bash
go run ./test/fakeupstream \
  -addr 0.0.0.0:9001 \
  -name primary \
  -scenario mock/error-503

go run ./test/fakeupstream \
  -addr 0.0.0.0:9002 \
  -name secondary \
  -scenario mock/instant
```

然后把两个地址配置成有序候选，即可稳定验证 primary 失败、secondary Fallback
成功。需要 TCP 延迟、reset、带宽限制时，在 Model-Velo 和这两个端口之间加入
Toxiproxy，不在假上游 Handler 中重复实现网络代理。

## 是否需要不同阶段的脚本

假上游不需要不同脚本，始终使用这个服务。负载客户端按目的拆分脚本，并共享
请求构造代码：

1. `smoke`：低并发验证状态码、响应格式和 SSE 完整性；
2. `load`：先直连假上游，再经过 Model-Velo，使用 `mock/instant`、`mock/typical`
   测网关开销和容量；
3. `profile`：执行升压、降压和突发 arrival-rate 曲线；
4. `fault`：在抖动、长尾、错误比例、Queue 和限流场景下继续记录状态分布；
5. `reliability`：使用 429、5xx、`mock/retry-2`、SSE 错误和双 Provider，
   断言 Retry、Fallback 与 SSE 提交边界；
6. `streamload`：逐行读取 SSE，单独统计响应头、首事件、首内容、总时长和 Chunk 间隔。

同一个 k6 脚本内部可以配置多个 `stages` 表示升压、稳定和降压；不要把每个并发阶段
复制成单独脚本。
