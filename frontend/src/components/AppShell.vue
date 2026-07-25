<script setup lang="ts">
import {
  Activity,
  Bell,
  Boxes,
  ChartNoAxesCombined,
  CircleDollarSign,
  Gauge,
  KeyRound,
  Moon,
  Network,
  RefreshCw,
  Route,
  ScrollText,
  Search,
  Settings,
  Sun,
  X,
} from "lucide-vue-next"
import { computed, onMounted } from "vue"
import { RouterLink, RouterView, useRoute } from "vue-router"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import { useConsole } from "@/composables/useConsole"
import { relativeTime } from "@/lib/format"

const route = useRoute()
const { data, settings, isDemo, loading, error, lastUpdated, refresh } = useConsole()

const navigation = [
  { label: "运行总览", path: "/", icon: Gauge },
  { label: "请求流量", path: "/requests", icon: Activity },
  { label: "Providers", path: "/providers", icon: Boxes },
  { label: "模型与路由", path: "/models", icon: Route },
  { label: "租户与密钥", path: "/access", icon: KeyRound },
  { label: "配额", path: "/quotas", icon: ChartNoAxesCombined },
  { label: "成本定价", path: "/pricing", icon: CircleDollarSign },
  { label: "审计日志", path: "/audit", icon: ScrollText },
]

const isDark = computed(
  () =>
    settings.value.theme === "dark" ||
    (settings.value.theme === "system" &&
      window.matchMedia("(prefers-color-scheme: dark)").matches),
)

function toggleTheme() {
  settings.value.theme = isDark.value ? "light" : "dark"
}

onMounted(() => void refresh())
</script>

<template>
  <div class="min-h-screen bg-background">
    <aside
      class="fixed inset-y-0 left-0 z-30 flex w-[224px] flex-col border-r border-white/8 bg-[var(--sidebar)] text-[var(--sidebar-foreground)]"
    >
      <div class="flex h-16 items-center gap-3 border-b border-white/8 px-4">
        <div class="relative grid size-8 place-items-center overflow-hidden rounded-md bg-[#d29559] text-[#181a17]">
          <Network class="size-4.5" stroke-width="2.2" />
          <span class="absolute inset-x-0 bottom-0 h-[2px] bg-[#6ca58d]" />
        </div>
        <div class="min-w-0">
          <div class="text-[13.5px] font-semibold tracking-tight">Model-Velo</div>
          <div class="mt-0.5 text-[10px] font-medium tracking-[0.08em] text-[var(--sidebar-muted)] uppercase">
            Gateway Console
          </div>
        </div>
      </div>

      <div class="px-3 pt-4">
        <div
          class="mb-3 flex items-center justify-between rounded-md border border-white/8 bg-white/[0.035] px-3 py-2"
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
              <div class="truncate text-[10px] text-[var(--sidebar-muted)]">
                {{ isDemo ? "Local preview" : settings.baseUrl }}
              </div>
            </div>
          </div>
          <Badge v-if="isDemo" tone="accent" class="bg-[#3a2d21] text-[#d9a16c]">DEMO</Badge>
        </div>
      </div>

      <nav class="flex-1 px-3">
        <div class="mb-2 px-2 text-[9.5px] font-semibold tracking-[0.1em] text-[var(--sidebar-muted)] uppercase">
          Control plane
        </div>
        <RouterLink
          v-for="item in navigation"
          :key="item.path"
          :to="item.path"
          class="group mb-0.5 flex h-9 items-center gap-3 rounded-md px-2.5 text-[12.5px] text-[var(--sidebar-muted)] transition-colors hover:bg-[var(--sidebar-hover)] hover:text-[var(--sidebar-foreground)]"
          :class="{
            'bg-[var(--sidebar-hover)] !text-[var(--sidebar-foreground)]':
              route.path === item.path,
          }"
        >
          <component
            :is="item.icon"
            class="size-4"
            :class="{ 'text-[#d29559]': route.path === item.path }"
            stroke-width="1.8"
          />
          <span>{{ item.label }}</span>
          <span
            v-if="route.path === item.path"
            class="ml-auto h-4 w-0.5 rounded-full bg-[#d29559]"
          />
        </RouterLink>
      </nav>

      <div class="border-t border-white/8 p-3">
        <RouterLink
          to="/settings"
          class="flex h-9 items-center gap-3 rounded-md px-2.5 text-[12.5px] text-[var(--sidebar-muted)] transition-colors hover:bg-[var(--sidebar-hover)] hover:text-[var(--sidebar-foreground)]"
          :class="{
            'bg-[var(--sidebar-hover)] !text-[var(--sidebar-foreground)]':
              route.path === '/settings',
          }"
        >
          <Settings class="size-4" stroke-width="1.8" />
          控制台设置
        </RouterLink>
        <div class="mt-2 flex items-center justify-between px-2.5 py-2 text-[10px] text-[var(--sidebar-muted)]">
          <span>Console v0.1</span>
          <span v-if="data.runtime">Runtime r{{ data.runtime.version }}</span>
        </div>
      </div>
    </aside>

    <div class="min-h-screen pl-[224px]">
      <header
        class="sticky top-0 z-20 flex h-14 items-center justify-between border-b border-border bg-background/92 px-6 backdrop-blur"
      >
        <div class="flex items-center gap-2">
          <span class="text-[12px] font-medium text-muted-foreground">控制台</span>
          <span class="text-border-strong">/</span>
          <span class="text-[12px] font-semibold">{{ route.meta.title }}</span>
        </div>

        <div class="flex items-center gap-1">
          <button
            class="mr-2 flex h-8 w-52 items-center gap-2 rounded-md border border-border bg-surface px-2.5 text-left text-[11.5px] text-muted-foreground transition-colors hover:border-border-strong"
          >
            <Search class="size-3.5" />
            <span class="flex-1">搜索资源</span>
            <kbd class="rounded border border-border bg-surface-muted px-1 py-0.5 font-mono text-[9px]">⌘ K</kbd>
          </button>
          <div class="mr-2 hidden text-right xl:block">
            <div class="text-[10.5px] text-muted-foreground">最近同步</div>
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
          <div class="ml-2 grid size-7 place-items-center rounded-md bg-foreground text-[10px] font-bold text-background">
            MV
          </div>
        </div>
      </header>

      <div
        v-if="isDemo"
        class="flex h-8 items-center justify-center gap-2 border-b border-accent/20 bg-accent-soft text-[10.5px] text-accent"
      >
        <span class="size-1 rounded-full bg-accent" />
        当前展示内置演示数据；在“控制台设置”中填入凭据并关闭演示模式即可连接真实网关
      </div>

      <div
        v-if="error"
        class="mx-6 mt-4 flex items-center gap-3 border border-negative/30 bg-negative-soft px-3.5 py-2.5 text-xs text-negative"
      >
        <X class="size-4 shrink-0" />
        <span class="font-semibold">同步失败</span>
        <span class="text-negative/85">{{ error }}</span>
        <RouterLink to="/settings" class="ml-auto font-semibold underline underline-offset-2">
          检查连接
        </RouterLink>
      </div>

      <main class="px-6 pb-8">
        <RouterView v-slot="{ Component }">
          <Transition name="fade" mode="out-in">
            <component :is="Component" />
          </Transition>
        </RouterView>
      </main>
    </div>
  </div>
</template>
