# Model-Velo

Model-Velo 是一个使用 Go 构建的 LLM Gateway，为应用提供统一的模型访问入口、租户鉴权、可靠性治理和用量记录。

## 核心能力

- OpenAI Chat Completions、Responses、Embeddings 与 Anthropic Messages 兼容接口
- 非流式响应与 SSE 流式传输
- 多 Provider 路由、Key 选择、重试、Fallback、熔断和有界队列
- API Key 鉴权、模型授权、租户限流、配额与响应缓存
- PostgreSQL 持久化与 Redis Stream 异步 Usage 链路
- Prometheus 指标、结构化日志、健康检查与 OpenTelemetry Tracing

## 架构

```text
Client
  │
  ▼
Model-Velo Gateway ──► Provider APIs
  │
  ├──► Redis ──► Usage Worker
  │
  └──► PostgreSQL
```

## 快速启动

### 环境要求

- Docker
- Docker Compose

### 1. 配置环境变量

```bash
cp .env.example .env
```

编辑 `.env`，替换示例密码和密钥，并配置：

- `MODEL_VELO_PROVIDER_KEYS_JSON`：上游 Provider Key
- `MODEL_VELO_ROUTING_JSON`：Provider、模型能力与路由规则

### 2. 启动服务

```bash
docker compose up -d --build
docker compose ps
```

网关默认监听 `http://localhost:8080`，Usage Worker 指标端口默认为 `9091`。

### 3. 创建租户和 API Key

```bash
docker compose --profile tools run --rm admin bootstrap-tenant \
  --slug demo \
  --name "Demo" \
  --models "*"
```

命令只显示一次明文 `api_key`，请妥善保存。

### 4. 发送请求

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <api_key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "your-model",
    "messages": [
      {"role": "user", "content": "Hello"}
    ]
  }'
```

## HTTP 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/healthz` | 进程存活检查 |
| `GET` | `/readyz` | PostgreSQL 与 Redis 就绪检查 |
| `GET` | `/metrics` | Prometheus 指标 |
| `GET` | `/v1/models` | 模型列表 |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions |
| `POST` | `/v1/responses` | OpenAI Responses |
| `POST` | `/v1/embeddings` | OpenAI Embeddings |
| `POST` | `/v1/messages` | Anthropic Messages |
| `GET` | `/v1/usage/events` | Usage 明细 |
| `GET` | `/v1/usage/summary` | Usage 汇总 |
| `GET` | `/v1/usage/series` | Usage 时间序列 |

`/v1/*` 接口使用网关 API Key。OpenAI 兼容接口通过 `Authorization: Bearer <api_key>` 认证；Anthropic Messages 接口通过 `x-api-key: <api_key>` 认证。

## 本地开发

```bash
go run ./cmd/model-velo
go run ./cmd/model-velo-usage-worker
```

代码检查：

```bash
go test ./...
go vet ./...
```

停止 Docker 环境：

```bash
docker compose down
```
