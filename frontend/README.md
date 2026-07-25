# Model-Velo Console

独立的 Vue 3 管理控制台，用于查看 Model-Velo 的运行配置、Usage、Provider、路由、租户、API Key、配额、定价和审计记录。

## 本地运行

```bash
cd frontend
npm install
npm run dev
```

默认访问 `http://127.0.0.1:4173`。Vite 将 `/gateway/*` 代理到
`http://127.0.0.1:8080`；可以通过 `VITE_GATEWAY_PROXY` 修改目标。

```bash
VITE_GATEWAY_PROXY=http://127.0.0.1:9000 npm run dev
```

生产构建：

```bash
npm run build
```

## 认证边界

- `Admin Bearer Key` 调用 `/admin/v1/*`，读取 Runtime、定价、租户、API Key、配额、管理员和审计数据。
- `Gateway API Key` 调用 `/v1/models` 与 `/v1/usage/*`。后端会将 Usage 查询限制到当前 API Key，控制台不会展示不存在的跨 Key Usage 聚合。
- 两类凭据只保存在当前标签页的 `sessionStorage`；非敏感设置保存在 `localStorage`。

首次打开默认使用演示数据。进入“控制台设置”可切换到真实网关。

## 后端接口

控制台只调用现有 Go API，不要求后端接口变更：

- `GET /healthz`
- `GET /admin/v1/runtime`
- `GET /admin/v1/pricing`
- `GET /admin/v1/tenants`
- `GET /admin/v1/tenants/:id/keys`
- `GET /admin/v1/quotas`
- `GET /admin/v1/quota-windows`
- `GET /admin/v1/audit`
- `GET /admin/v1/principals`
- `GET /v1/models`
- `GET /v1/usage/events`
- `GET /v1/usage/summary`
- `GET /v1/usage/series`

当前交付的控制台以安全的只读观测链路为主；页面中的编辑入口保留为后续交互切片，不会在未确认的情况下修改控制面配置。
