<script setup lang="ts">
import { reactive, ref, watch } from "vue"
import {
  Check,
  Database,
  Eye,
  EyeOff,
  KeyRound,
  Laptop,
  Moon,
  PlugZap,
  Save,
  Sun,
  TriangleAlert,
} from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import Input from "@/components/ui/Input.vue"
import { useConsole } from "@/composables/useConsole"
import { gatewayAPI } from "@/lib/api"
import type { ConsoleSettings, ThemeMode } from "@/types"

const { settings, updateSettings } = useConsole()
const form = reactive<ConsoleSettings>({ ...settings.value })
const showAdminKey = ref(false)
const showGatewayKey = ref(false)
const testState = ref<"idle" | "testing" | "success" | "failed">("idle")
const testMessage = ref("")

watch(
  settings,
  (value) => Object.assign(form, value),
  { deep: true },
)

const themes: { value: ThemeMode; label: string; icon: typeof Sun }[] = [
  { value: "light", label: "浅色", icon: Sun },
  { value: "dark", label: "深色", icon: Moon },
  { value: "system", label: "跟随系统", icon: Laptop },
]

function save() {
  updateSettings({ ...form })
}

async function testConnection() {
  testState.value = "testing"
  testMessage.value = ""
  try {
    const result = await gatewayAPI.health(form)
    testState.value = result.status === "ok" ? "success" : "failed"
    testMessage.value = result.status === "ok" ? "Gateway /healthz 响应正常" : `状态：${result.status}`
  } catch (cause) {
    testState.value = "failed"
    testMessage.value = cause instanceof Error ? cause.message : "无法连接 Gateway"
  }
}
</script>

<template>
  <PageHeader title="控制台设置" description="连接现有 Model-Velo API；凭据仅保留在当前浏览器标签页。">
    <Button size="sm" @click="save"><Save class="size-3.5" />保存并同步</Button>
  </PageHeader>

  <div class="grid grid-cols-[minmax(0,1fr)_320px] gap-4">
    <div class="space-y-4">
      <section class="panel">
        <div class="panel-header">
          <div>
            <div class="text-[13px] font-semibold">数据源</div>
            <div class="mt-0.5 text-[10.5px] text-muted-foreground">演示数据或真实 Gateway</div>
          </div>
          <Database class="size-4 text-muted-foreground" />
        </div>
        <div class="grid grid-cols-2 gap-3 p-4">
          <button
            class="flex items-start gap-3 rounded-md border p-4 text-left transition-colors"
            :class="form.demoMode ? 'border-accent bg-accent-soft' : 'border-border hover:border-border-strong'"
            @click="form.demoMode = true"
          >
            <span class="mt-0.5 grid size-5 place-items-center rounded-full border" :class="form.demoMode ? 'border-accent bg-accent text-white' : 'border-border-strong'">
              <Check v-if="form.demoMode" class="size-3" />
            </span>
            <span>
              <span class="block text-xs font-semibold">演示模式</span>
              <span class="mt-1 block text-[10.5px] leading-4 text-muted-foreground">使用内置数据完整预览界面，不发起网络请求。</span>
            </span>
          </button>
          <button
            class="flex items-start gap-3 rounded-md border p-4 text-left transition-colors"
            :class="!form.demoMode ? 'border-accent bg-accent-soft' : 'border-border hover:border-border-strong'"
            @click="form.demoMode = false"
          >
            <span class="mt-0.5 grid size-5 place-items-center rounded-full border" :class="!form.demoMode ? 'border-accent bg-accent text-white' : 'border-border-strong'">
              <Check v-if="!form.demoMode" class="size-3" />
            </span>
            <span>
              <span class="block text-xs font-semibold">真实网关</span>
              <span class="mt-1 block text-[10.5px] leading-4 text-muted-foreground">调用现有 /admin/v1 与 /v1 接口。</span>
            </span>
          </button>
        </div>
      </section>

      <section class="panel">
        <div class="panel-header">
          <div>
            <div class="text-[13px] font-semibold">Gateway 连接</div>
            <div class="mt-0.5 text-[10.5px] text-muted-foreground">本地开发默认通过 Vite 反向代理访问 :8080</div>
          </div>
          <PlugZap class="size-4 text-muted-foreground" />
        </div>
        <div class="space-y-5 p-5">
          <label class="block">
            <span class="mb-1.5 block text-[11px] font-semibold">Base URL</span>
            <Input v-model="form.baseUrl" placeholder="/gateway" />
            <span class="mt-1.5 block text-[10px] text-muted-foreground">
              推荐保持 /gateway；Vite 将请求代理到 VITE_GATEWAY_PROXY。
            </span>
          </label>

          <div class="grid grid-cols-2 gap-4">
            <label class="block">
              <span class="mb-1.5 flex items-center justify-between text-[11px] font-semibold">
                Admin Bearer Key
                <Badge tone="accent">控制面</Badge>
              </span>
              <div class="relative">
                <Input
                  v-model="form.adminKey"
                  :type="showAdminKey ? 'text' : 'password'"
                  class="pr-9 font-mono"
                  placeholder="mv_admin_••••••••"
                />
                <button
                  class="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground"
                  @click.prevent="showAdminKey = !showAdminKey"
                >
                  <EyeOff v-if="showAdminKey" class="size-3.5" />
                  <Eye v-else class="size-3.5" />
                </button>
              </div>
              <span class="mt-1.5 block text-[10px] leading-4 text-muted-foreground">
                读取 Runtime、租户、Key、配额、价格与审计记录。
              </span>
            </label>

            <label class="block">
              <span class="mb-1.5 flex items-center justify-between text-[11px] font-semibold">
                Gateway API Key
                <Badge tone="info">数据面</Badge>
              </span>
              <div class="relative">
                <Input
                  v-model="form.gatewayKey"
                  :type="showGatewayKey ? 'text' : 'password'"
                  class="pr-9 font-mono"
                  placeholder="mv_live_••••••••"
                />
                <button
                  class="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground"
                  @click.prevent="showGatewayKey = !showGatewayKey"
                >
                  <EyeOff v-if="showGatewayKey" class="size-3.5" />
                  <Eye v-else class="size-3.5" />
                </button>
              </div>
              <span class="mt-1.5 block text-[10px] leading-4 text-muted-foreground">
                读取 /v1/models 和仅属于该 Key 的 Usage 数据。
              </span>
            </label>
          </div>

          <div class="flex items-center gap-3 border-t border-border pt-4">
            <Button variant="secondary" size="sm" :disabled="testState === 'testing'" @click="testConnection">
              <PlugZap class="size-3.5" />
              {{ testState === "testing" ? "测试中…" : "测试连接" }}
            </Button>
            <span
              v-if="testMessage"
              class="text-[10.5px]"
              :class="testState === 'success' ? 'text-positive' : 'text-negative'"
            >
              {{ testMessage }}
            </span>
          </div>
        </div>
      </section>

      <section class="panel">
        <div class="panel-header">
          <div>
            <div class="text-[13px] font-semibold">外观</div>
            <div class="mt-0.5 text-[10.5px] text-muted-foreground">两套主题共享相同语义色与信息层级</div>
          </div>
        </div>
        <div class="grid grid-cols-3 gap-3 p-4">
          <button
            v-for="theme in themes"
            :key="theme.value"
            class="flex items-center gap-3 rounded-md border px-4 py-3 text-left"
            :class="form.theme === theme.value ? 'border-accent bg-accent-soft' : 'border-border'"
            @click="form.theme = theme.value"
          >
            <component :is="theme.icon" class="size-4" />
            <span class="text-xs font-semibold">{{ theme.label }}</span>
            <Check v-if="form.theme === theme.value" class="ml-auto size-3.5 text-accent" />
          </button>
        </div>
      </section>
    </div>

    <aside class="space-y-4">
      <section class="panel">
        <div class="panel-header"><div class="text-[13px] font-semibold">接口认证边界</div></div>
        <div class="divide-y divide-border">
          <div class="flex gap-3 px-4 py-4">
            <div class="grid size-8 shrink-0 place-items-center rounded-md bg-accent-soft text-accent">
              <KeyRound class="size-4" />
            </div>
            <div>
              <div class="text-xs font-semibold">Admin Key</div>
              <p class="mt-1 text-[10.5px] leading-4 text-muted-foreground">
                权限由 owner、operator、billing、auditor 角色映射；无权限接口会返回 403。
              </p>
            </div>
          </div>
          <div class="flex gap-3 px-4 py-4">
            <div class="grid size-8 shrink-0 place-items-center rounded-md bg-info-soft text-info">
              <Database class="size-4" />
            </div>
            <div>
              <div class="text-xs font-semibold">Gateway API Key</div>
              <p class="mt-1 text-[10.5px] leading-4 text-muted-foreground">
                Usage 查询由后端覆盖 api_key_id，只能看到当前 Key 自身数据，不能跨 Key 聚合。
              </p>
            </div>
          </div>
        </div>
      </section>

      <section class="border border-warning/30 bg-warning-soft p-4 text-warning">
        <div class="flex items-center gap-2 text-xs font-semibold">
          <TriangleAlert class="size-4" />凭据安全
        </div>
        <p class="mt-2 text-[10.5px] leading-4 text-warning/85">
          控制台不会把 Key 写入仓库、URL 或日志；凭据仅保留在当前标签页的 sessionStorage。生产部署应使用同源反向代理并启用 TLS。
        </p>
      </section>
    </aside>
  </div>
</template>
