# 生产运维说明

## 启动顺序

1. 从 `.env.example` 复制 `.env`，替换所有密码、Pepper、Provider Key 和主密钥。
2. `MODEL_VELO_API_KEY_PEPPER` 与 `MODEL_VELO_ADMIN_KEY_PEPPER` 必须不同。
3. 用 `openssl rand -base64 32` 生成 `MODEL_VELO_CONTROL_MASTER_KEY`。该密钥用于 AES-256-GCM 加密数据库中的 Provider Key；丢失后托管配置无法恢复。
4. 运行 `docker compose up --build -d gateway usage-worker`。
5. 执行 `docker compose --profile tools run --rm admin bootstrap-admin --name root`，只保存一次输出的管理员 Key。
6. 用该管理员 Key 调用 `/admin/v1/runtime`、`/admin/v1/pricing` 与 `/admin/v1/quotas`。首次写入使用 `If-Match: "0"`，后续使用 GET 返回的 `ETag`。
7. 用 `POST /admin/v1/tenants` 创建业务租户、模型授权和第一把模型 API Key；CLI `bootstrap-tenant` 仍可用于离线恢复。

## 探针

- `GET /healthz` 只表示进程仍可响应，不访问外部依赖。
- `GET /readyz` 同时检查 PostgreSQL 和 Redis；任一不可用返回 503，并只公开 `ok/unavailable`，不泄漏连接信息。
- API 的 `GET /metrics` 提供请求、Provider、可靠性和额度指标。Worker 默认在 `:9091` 提供独立 `/healthz`、`/readyz`、`/metrics`，包含读取、认领、当前 consumer-group pending、幂等重复、死信和 outbox relay 计数。
- 设置 `MODEL_VELO_METRICS_TOKEN` 后两个 `/metrics` 都必须使用 Bearer Token。

## 管理面安全边界

- Model-Velo 本身不终止公网 TLS；生产部署必须放在受信任的 TLS 反向代理/Ingress 后，限制 `/admin/v1` 和 `/metrics` 的网络来源，不能把明文 HTTP 管理端口直接暴露到公网。
- 管理员 Key 使用独立前缀、独立 Pepper、独立数据表，不能调用模型接口；模型 API Key 也不能调用管理接口。
- 内置角色为 `owner`、`operator`、`billing`、`auditor`。最后一个活动 owner 不能被禁用或移除 owner 角色。
- Provider Key 仅出现在写请求和 AES-GCM 密文中。GET、审计记录、日志、错误和指标只包含 Key ID。
- 管理写入使用版本化 `If-Match`，先完成完整运行时构造，再提交数据库并原子切换。其他 API 实例按刷新间隔装载相同活动版本。
- 审计表记录操作者、request ID、来源 IP、动作和脱敏前后快照；业务变更和成功审计在同一事务中提交。数据库账户还应通过 PostgreSQL 权限禁止应用之外的 UPDATE/DELETE 审计表操作。

## Usage 与额度恢复

- 在线请求先写 `usage_outbox` pending 行，失败时不调用 Provider。
- 完整事件写入 outbox 后尝试投递 Redis Stream；Redis 不可用时 Worker 会从 PostgreSQL 重投。published 记录在 Usage 入库删除前也会周期性重发，避免 Redis 消息提前消失形成永久缺口。
- 超过 `MODEL_VELO_USAGE_PENDING_TIMEOUT` 的 pending 生命周期会生成 `request_lifecycle_interrupted` 事件，明确标记 Usage 未知。
- 额度在调用 Provider 前预留，在响应结束后按真实 Usage 结算。进程退出留下的预留会在 TTL 后按保守估算入账；迟到结算可把估算校正为真实值。
- Token 预留对文本使用请求 JSON 字节数作为保守上界；如果请求没有输出上限，使用 `MODEL_VELO_QUOTA_DEFAULT_MAX_OUTPUT_TOKENS`。这会偏保守，但不会把模型相关 tokenizer 猜测成精确值。

## 密钥轮换

- API/Admin Pepper 不是在线轮换密钥；更换会使对应现有 Key 失效，应通过双实例迁移窗口轮换。
- Provider Key 可在运行时文档中增删。提交时省略已有 Key 的 `secret` 会保留原密文中的值；新 Key 必须提供 secret。
- 控制面主密钥轮换需要离线解密并重加密全部活动版本，当前没有自动轮换命令，必须作为单独受审变更执行。
