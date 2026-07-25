<script setup lang="ts">
import {
  Activity,
  Boxes,
  ChartNoAxesCombined,
  CircleDollarSign,
  Crown,
  Gauge,
  KeyRound,
  LayoutDashboard,
  Route,
  ScrollText,
  ServerCog,
  ShieldCheck,
  UserRoundCog,
  Users,
} from "lucide-vue-next"
import { computed } from "vue"
import { RouterLink } from "vue-router"
import type { WorkspaceLayer } from "@/types"

const props = defineProps<{
  layer: WorkspaceLayer
  routePath: string
}>()

const navigation = {
  portal: [
    { label: "用量概览", path: "/portal", icon: LayoutDashboard },
    { label: "请求记录", path: "/portal/usage", icon: Activity },
    { label: "可用模型", path: "/portal/models", icon: Boxes },
    { label: "API Key", path: "/portal/key", icon: KeyRound },
  ],
  admin: [
    { label: "运行总览", path: "/admin", icon: Gauge },
    { label: "平台用量", path: "/admin/usage", icon: Activity },
    { label: "Providers", path: "/admin/providers", icon: Boxes },
    { label: "模型与路由", path: "/admin/routes", icon: Route },
    { label: "租户与密钥", path: "/admin/tenants", icon: Users },
    { label: "配额策略", path: "/admin/quotas", icon: ChartNoAxesCombined },
    { label: "审计日志", path: "/admin/audit", icon: ScrollText },
  ],
  owner: [
    { label: "系统概览", path: "/admin/system", icon: Crown },
    { label: "管理员身份", path: "/admin/system/principals", icon: UserRoundCog },
    { label: "Runtime 配置", path: "/admin/system/runtime", icon: ServerCog },
    { label: "成本定价", path: "/admin/system/pricing", icon: CircleDollarSign },
  ],
}

const section = computed(() => {
  if (props.layer === "portal") {
    return { eyebrow: "API consumer", icon: UserRoundCog, items: navigation.portal }
  }
  if (props.layer === "owner") {
    return { eyebrow: "Owner boundary", icon: Crown, items: navigation.owner }
  }
  return { eyebrow: "Control plane", icon: ShieldCheck, items: navigation.admin }
})

function isActive(path: string) {
  return props.routePath === path
}
</script>

<template>
  <nav class="px-3">
    <div class="mb-2 flex items-center gap-1.5 px-2 text-[9.5px] font-semibold tracking-[0.1em] text-[var(--sidebar-muted)] uppercase">
      <component :is="section.icon" class="size-3" />
      {{ section.eyebrow }}
    </div>
    <RouterLink
      v-for="item in section.items"
      :key="item.path"
      :to="item.path"
      class="group mb-0.5 flex h-9 items-center gap-3 px-2.5 text-[12.5px] text-[var(--sidebar-muted)] transition-colors hover:bg-[var(--sidebar-hover)] hover:text-[var(--sidebar-foreground)]"
      :class="{
        'bg-[var(--sidebar-hover)] !text-[var(--sidebar-foreground)]': isActive(item.path),
      }"
    >
      <component
        :is="item.icon"
        class="size-4"
        :class="{ 'text-[var(--workspace-accent)]': isActive(item.path) }"
        stroke-width="1.8"
      />
      <span>{{ item.label }}</span>
      <span
        v-if="isActive(item.path)"
        class="ml-auto h-4 w-0.5 bg-[var(--workspace-accent)]"
      />
    </RouterLink>
  </nav>
</template>
