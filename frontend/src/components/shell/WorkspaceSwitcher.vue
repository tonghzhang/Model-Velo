<script setup lang="ts">
import { Crown, ShieldCheck, UserRound } from "lucide-vue-next"
import { RouterLink } from "vue-router"
import type { WorkspaceLayer } from "@/types"

defineProps<{
  current: WorkspaceLayer
}>()

const workspaces = [
  {
    id: "portal",
    label: "用户门户",
    caption: "个人用量",
    path: "/portal",
    icon: UserRound,
  },
  {
    id: "admin",
    label: "管理工作台",
    caption: "网关运维",
    path: "/admin",
    icon: ShieldCheck,
  },
  {
    id: "owner",
    label: "Owner 区",
    caption: "系统权限",
    path: "/admin/system",
    icon: Crown,
  },
] satisfies {
  id: WorkspaceLayer
  label: string
  caption: string
  path: string
  icon: typeof UserRound
}[]
</script>

<template>
  <div class="workspace-switcher" aria-label="切换工作台">
    <RouterLink
      v-for="workspace in workspaces"
      :key="workspace.id"
      :to="workspace.path"
      class="workspace-link"
      :class="{ 'workspace-link-active': current === workspace.id }"
    >
      <span class="workspace-rail" :data-layer="workspace.id" />
      <component :is="workspace.icon" class="size-3.5 shrink-0" stroke-width="1.8" />
      <span class="min-w-0 flex-1">
        <span class="block truncate text-[11.5px] font-semibold">{{ workspace.label }}</span>
        <span class="mt-0.5 block truncate text-[9.5px] text-[var(--sidebar-muted)]">
          {{ workspace.caption }}
        </span>
      </span>
    </RouterLink>
  </div>
</template>

<style scoped>
.workspace-switcher {
  display: grid;
  gap: 2px;
  margin: 0 12px;
  padding: 4px;
  border: 1px solid rgb(255 255 255 / 8%);
  background: rgb(255 255 255 / 2.5%);
}

.workspace-link {
  position: relative;
  display: flex;
  min-height: 44px;
  align-items: center;
  gap: 10px;
  padding: 6px 9px 6px 12px;
  color: var(--sidebar-muted);
  transition:
    color 140ms ease,
    background-color 140ms ease;
}

.workspace-link:hover {
  background: var(--sidebar-hover);
  color: var(--sidebar-foreground);
}

.workspace-link-active {
  background: var(--sidebar-hover);
  color: var(--sidebar-foreground);
}

.workspace-rail {
  position: absolute;
  inset-block: 7px;
  left: 0;
  width: 2px;
  background: #6ca58d;
  opacity: 0.35;
}

.workspace-rail[data-layer="admin"] {
  background: #d29559;
}

.workspace-rail[data-layer="owner"] {
  background: #d07a68;
}

.workspace-link-active .workspace-rail {
  opacity: 1;
}
</style>
