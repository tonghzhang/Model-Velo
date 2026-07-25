# Model-Velo Console

独立的 Vue 3 网关控制台。前端借鉴成熟 API 网关产品的角色分层，但保持
Model-Velo 自己的权限模型与视觉语言：

- `/portal`：API 用户门户，只展示当前业务 API Key 的模型、Usage、成本与接入信息。
- `/admin`：管理工作台，面向 Operator、Billing 和 Auditor，展示平台用量、Provider、路由、租户、配额与审计。
- `/admin/system`：Owner 区，集中展示管理员身份、全局 Runtime 与版本化定价。

三个工作台共享基础组件和数据源，但不会把 Provider 路由、其他租户或管理员身份泄漏到用户门户。

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

- `Admin Bearer Key` 调用 `/admin/v1/*`，读取平台 Usage、Runtime、定价、租户、API Key、配额、管理员和审计数据。
- `Gateway API Key` 调用 `/v1/models` 与 `/v1/usage/*`。后端会将 Usage 查询限制到当前 API Key，控制台不会展示不存在的跨 Key Usage 聚合。
- 两类凭据只保存在当前标签页的 `sessionStorage`；非敏感设置保存在 `localStorage`。

首次打开默认使用演示数据。进入“连接与外观”可切换到真实网关。

管理接口按数据域独立加载。受限角色遇到无权限的接口时，仅对应页面显示权限提示，
不会因为一个 `403` 导致其他已授权页面同步失败。前端菜单不是安全边界，所有读写权限
仍由 Go API 校验。

## 后端接口

控制台调用以下 Go API：

- `GET /healthz`
- `GET /admin/v1/runtime`
- `GET /admin/v1/pricing`
- `GET /admin/v1/tenants`
- `GET /admin/v1/tenants/:id/keys`
- `GET /admin/v1/quotas`
- `GET /admin/v1/quota-windows`
- `GET /admin/v1/audit`
- `GET /admin/v1/principals`
- `GET /admin/v1/usage/events`
- `GET /admin/v1/usage/summary`
- `GET /admin/v1/usage/series`
- `GET /v1/models`
- `GET /v1/usage/events`
- `GET /v1/usage/summary`
- `GET /v1/usage/series`

当前交付的控制台以安全的只读观测链路为主；页面中的编辑入口保留为后续交互切片，不会在未确认的情况下修改控制面配置。
