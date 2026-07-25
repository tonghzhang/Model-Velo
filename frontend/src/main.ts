import { createApp } from "vue"
import { createRouter, createWebHistory } from "vue-router"
import App from "@/App.vue"
import "@/styles.css"

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: "/",
      component: () => import("@/views/OverviewView.vue"),
      meta: { title: "运行总览" },
    },
    {
      path: "/requests",
      component: () => import("@/views/RequestsView.vue"),
      meta: { title: "请求流量" },
    },
    {
      path: "/providers",
      component: () => import("@/views/ProvidersView.vue"),
      meta: { title: "Providers" },
    },
    {
      path: "/models",
      component: () => import("@/views/ModelsView.vue"),
      meta: { title: "模型与路由" },
    },
    {
      path: "/access",
      component: () => import("@/views/AccessView.vue"),
      meta: { title: "租户与密钥" },
    },
    {
      path: "/quotas",
      component: () => import("@/views/QuotasView.vue"),
      meta: { title: "配额" },
    },
    {
      path: "/pricing",
      component: () => import("@/views/PricingView.vue"),
      meta: { title: "成本定价" },
    },
    {
      path: "/audit",
      component: () => import("@/views/AuditView.vue"),
      meta: { title: "审计日志" },
    },
    {
      path: "/settings",
      component: () => import("@/views/SettingsView.vue"),
      meta: { title: "控制台设置" },
    },
  ],
})

router.afterEach((route) => {
  document.title = `${String(route.meta.title || "Console")} · Model-Velo`
})

createApp(App).use(router).mount("#app")
