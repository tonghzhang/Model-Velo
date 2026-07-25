<script setup lang="ts">
import {
  Bell,
  Moon,
  Network,
  RefreshCw,
  Search,
  Settings,
  Sun,
  X,
} from "lucide-vue-next"
import { computed, onMounted, watch } from "vue"
import { RouterLink, RouterView, useRoute } from "vue-router"
import LayerAccessNotice from "@/components/shell/LayerAccessNotice.vue"
import SidebarNavigation from "@/components/shell/SidebarNavigation.vue"
import WorkspaceSwitcher from "@/components/shell/WorkspaceSwitcher.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import { useAdminUsage } from "@/composables/useAdminUsage"
import { useConsole } from "@/composables/useConsole"
import { relativeTime } from "@/lib/format"
import type { WorkspaceLayer } from "@/types"

const route = useRoute()
const {
  data,
  settings,
  scopeErrors,
  isDemo,
  loading: consoleLoading,
  error,
  lastUpdated,
  refresh: refreshConsole,
} = useConsole()
const { loading: usageLoading, refresh: refreshAdminUsage } = useAdminUsage()

const currentLayer = computed<WorkspaceLayer>(() => route.meta.layer || "portal")
const workspaceLabel = computed(() => {
  if (currentLayer.value === "portal") return "用户门户"
  if (currentLayer.value === "owner") return "Owner 区"
  return "管理工作台"
})
const shellClass = computed(() => `layer-${currentLayer.value}`)
const loading = computed(
  () =>
    consoleLoading.value ||
    (currentLayer.value === "admin" && usageLoading.value),
)
const isDark = computed(
  () =>
    settings.value.theme === "dark" ||
    (settings.value.theme === "system" &&
      window.matchMedia("(prefers-color-scheme: dark)").matches),
)

function toggleTheme() {
  settings.value.theme = isDark.value ? "light" : "dark"
}

function refresh() {
  if (currentLayer.value === "admin") {
    void Promise.all([refreshConsole(), refreshAdminUsage()])
    return
  }
  void refreshConsole()
}

onMounted(refresh)

watch(currentLayer, (layer, previous) => {
  if (layer === "admin" && previous !== "admin") void refreshAdminUsage()
})
</script>

<template>
  <div class="min-h-screen bg-background" :class="shellClass">
    <aside
      class="fixed inset-y-0 left-0 z-30 flex w-[236px] flex-col border-r border-white/8 bg-[var(--sidebar)] text-[var(--sidebar-foreground)]"
    >
      <div class="flex h-16 items-center gap-3 border-b border-white/8 px-4">
        <div class="brand-mark">
          <Network class="size-4.5" stroke-width="2.2" />
          <span class="absolute inset-x-0 bottom-0 h-[2px] bg-[#6ca58d]" />
        </div>
        <div class="min-w-0">
          <div class="text-[13.5px] font-semibold tracking-tight">Model-Velo</div>
          <div class="mt-0.5 text-[10px] font-medium tracking-[0.08em] text-[var(--sidebar-muted)] uppercase">
            Gateway control
          </div>
        </div>
      </div>

      <div class="py-3">
        <WorkspaceSwitcher :current="currentLayer" />
      </div>

      <div class="px-3 pb-4">
        <div
          class="flex items-center justify-between border border-white/8 bg-white/[0.025] px-3 py-2"
        >
          <div class="flex min-w-0 items-center gap-2">
            <span
              class="size-1.5 shrink-0 rounded-full"
              :class="data.health === 'healthy' ? 'bg-[#67b798]' : 'bg-[#d58a45]'"
            />
            <div class="min-w-0">
              <div class="truncate text-[11.5px] font-medium">
                {{ isDemo ? "Demo workspace" : "Gateway" }}
              </div>
              <div class="truncate text-[9.5px] text-[var(--sidebar-muted)]">
                {{ isDemo ? "Local preview" : settings.baseUrl }}
              </div>
            </div>
          </div>
          <Badge v-if="isDemo" tone="accent" class="bg-[#3a2d21] text-[#d9a16c]">DEMO</Badge>
        </div>
      </div>

      <SidebarNavigation
        class="min-h-0 flex-1 overflow-y-auto"
        :layer="currentLayer"
        :route-path="route.path"
      />

      <div class="border-t border-white/8 p-3">
        <RouterLink
          to="/settings"
          class="flex h-9 items-center gap-3 px-2.5 text-[12.5px] text-[var(--sidebar-muted)] transition-colors hover:bg-[var(--sidebar-hover)] hover:text-[var(--sidebar-foreground)]"
          :class="{
            'bg-[var(--sidebar-hover)] !text-[var(--sidebar-foreground)]':
              route.path === '/settings',
          }"
        >
          <Settings class="size-4" stroke-width="1.8" />
          连接与外观
        </RouterLink>
        <div class="mt-2 flex items-center justify-between px-2.5 py-2 text-[9.5px] text-[var(--sidebar-muted)]">
          <span>Console v0.2</span>
          <span v-if="data.runtime">Runtime r{{ data.runtime.version }}</span>
        </div>
      </div>
    </aside>

    <div class="min-h-screen pl-[236px]">
      <header
        class="sticky top-0 z-20 flex h-14 items-center justify-between border-b border-border bg-background/92 px-6 backdrop-blur"
      >
        <div class="flex items-center gap-2">
          <span class="workspace-dot" />
          <span class="text-[11.5px] font-semibold text-[var(--workspace-accent)]">
            {{ workspaceLabel }}
          </span>
          <span class="text-border-strong">/</span>
          <span class="text-[12px] font-semibold">{{ route.meta.title }}</span>
        </div>

        <div class="flex items-center gap-1">
          <button
            class="mr-2 flex h-8 w-52 items-center gap-2 border border-border bg-surface px-2.5 text-left text-[11.5px] text-muted-foreground transition-colors hover:border-border-strong"
          >
            <Search class="size-3.5" />
            <span class="flex-1">搜索当前工作台</span>
            <kbd class="border border-border bg-surface-muted px-1 py-0.5 font-mono text-[9px]">⌘ K</kbd>
          </button>
          <div class="mr-2 hidden text-right xl:block">
            <div class="text-[10px] text-muted-foreground">最近同步</div>
            <div class="tabular text-[10.5px] font-medium">
              {{ lastUpdated ? relativeTime(lastUpdated.toISOString()) : "尚未同步" }}
            </div>
          </div>
          <Button
            variant="ghost"
            size="icon"
            :disabled="loading"
            aria-label="刷新数据"
            @click="refresh"
          >
            <RefreshCw class="size-3.5" :class="{ 'animate-spin': loading }" />
          </Button>
          <Button variant="ghost" size="icon" aria-label="通知">
            <Bell class="size-3.5" />
          </Button>
          <Button variant="ghost" size="icon" aria-label="切换主题" @click="toggleTheme">
            <Sun v-if="isDark" class="size-3.5" />
            <Moon v-else class="size-3.5" />
          </Button>
          <div class="identity-mark">
            {{ currentLayer === "portal" ? "U" : currentLayer === "owner" ? "O" : "A" }}
          </div>
        </div>
      </header>

      <div
        v-if="isDemo"
        class="flex h-8 items-center justify-center gap-2 border-b border-accent/20 bg-accent-soft text-[10.5px] text-accent"
      >
        <span class="size-1 rounded-full bg-accent" />
        演示模式已启用；三个工作台使用同一组仿真数据，并按页面职责隔离展示范围
      </div>

      <LayerAccessNotice
        :layer="currentLayer"
        :demo="isDemo"
        :gateway-key="settings.gatewayKey"
        :admin-key="settings.adminKey"
        :scope-errors="scopeErrors"
      />

      <div
        v-if="error"
        class="mx-6 mt-4 flex items-center gap-3 border border-negative/30 bg-negative-soft px-3.5 py-2.5 text-xs text-negative"
      >
        <X class="size-4 shrink-0" />
        <span class="font-semibold">连接失败</span>
        <span class="text-negative/85">{{ error }}</span>
        <RouterLink to="/settings" class="ml-auto font-semibold underline underline-offset-2">
          检查连接
        </RouterLink>
      </div>

      <main class="px-6 pb-8">
        <RouterView v-slot="{ Component, route: viewRoute }">
          <component :is="Component" :key="viewRoute.fullPath" />
        </RouterView>
      </main>
    </div>
  </div>
</template>

<style scoped>
.brand-mark {
  position: relative;
  display: grid;
  width: 32px;
  height: 32px;
  place-items: center;
  overflow: hidden;
  background: #d29559;
  color: #181a17;
}

.workspace-dot {
  width: 7px;
  height: 7px;
  border: 2px solid var(--workspace-accent);
  border-radius: 999px;
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--workspace-accent) 14%, transparent);
}

.identity-mark {
  display: grid;
  width: 28px;
  height: 28px;
  margin-left: 8px;
  place-items: center;
  background: var(--workspace-accent);
  color: #151713;
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 800;
}
</style>
