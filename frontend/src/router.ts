import { createRouter, createWebHistory } from "vue-router"
import type { WorkspaceLayer } from "@/types"

declare module "vue-router" {
  interface RouteMeta {
    title: string
    layer?: WorkspaceLayer
  }
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: "/",
      redirect: "/portal",
    },
    {
      path: "/portal",
      component: () => import("@/views/portal/PortalOverviewView.vue"),
      meta: { title: "用量概览", layer: "portal" },
    },
    {
      path: "/portal/usage",
      component: () => import("@/views/RequestsView.vue"),
      meta: { title: "请求记录", layer: "portal" },
    },
    {
      path: "/portal/models",
      component: () => import("@/views/portal/PortalModelsView.vue"),
      meta: { title: "可用模型", layer: "portal" },
    },
    {
      path: "/portal/key",
      component: () => import("@/views/portal/PortalKeyView.vue"),
      meta: { title: "API Key", layer: "portal" },
    },
    {
      path: "/admin",
      component: () => import("@/views/OverviewView.vue"),
      meta: { title: "运行总览", layer: "admin" },
    },
    {
      path: "/admin/providers",
      component: () => import("@/views/ProvidersView.vue"),
      meta: { title: "Providers", layer: "admin" },
    },
    {
      path: "/admin/usage",
      component: () => import("@/views/AdminUsageView.vue"),
      meta: { title: "平台用量", layer: "admin" },
    },
    {
      path: "/admin/routes",
      component: () => import("@/views/ModelsView.vue"),
      meta: { title: "模型与路由", layer: "admin" },
    },
    {
      path: "/admin/tenants",
      component: () => import("@/views/AccessView.vue"),
      meta: { title: "租户与密钥", layer: "admin" },
    },
    {
      path: "/admin/quotas",
      component: () => import("@/views/QuotasView.vue"),
      meta: { title: "配额策略", layer: "admin" },
    },
    {
      path: "/admin/audit",
      component: () => import("@/views/AuditView.vue"),
      meta: { title: "审计日志", layer: "admin" },
    },
    {
      path: "/admin/system",
      component: () => import("@/views/owner/OwnerOverviewView.vue"),
      meta: { title: "系统概览", layer: "owner" },
    },
    {
      path: "/admin/system/principals",
      component: () => import("@/views/owner/PrincipalsView.vue"),
      meta: { title: "管理员身份", layer: "owner" },
    },
    {
      path: "/admin/system/runtime",
      component: () => import("@/views/owner/RuntimeSystemView.vue"),
      meta: { title: "Runtime 配置", layer: "owner" },
    },
    {
      path: "/admin/system/pricing",
      component: () => import("@/views/PricingView.vue"),
      meta: { title: "成本定价", layer: "owner" },
    },
    {
      path: "/settings",
      component: () => import("@/views/SettingsView.vue"),
      meta: { title: "连接与外观" },
    },
    { path: "/requests", redirect: "/portal/usage" },
    { path: "/providers", redirect: "/admin/providers" },
    { path: "/models", redirect: "/admin/routes" },
    { path: "/access", redirect: "/admin/tenants" },
    { path: "/quotas", redirect: "/admin/quotas" },
    { path: "/pricing", redirect: "/admin/system/pricing" },
    { path: "/audit", redirect: "/admin/audit" },
    { path: "/:pathMatch(.*)*", redirect: "/portal" },
  ],
})

router.afterEach((route) => {
  document.title = `${route.meta.title || "Console"} · Model-Velo`
})
