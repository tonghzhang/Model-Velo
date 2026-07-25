<script setup lang="ts">
import { CheckCircle2, Copy, KeyRound, LockKeyhole, Terminal } from "lucide-vue-next"
import { computed, shallowRef } from "vue"
import { RouterLink } from "vue-router"
import PageHeader from "@/components/PageHeader.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import { useConsole } from "@/composables/useConsole"

const { data, settings, isDemo } = useConsole()
const copied = shallowRef(false)

const maskedKey = computed(() => {
  if (isDemo.value) return "mv_live_7c4e••••••••••••"
  if (!settings.value.gatewayKey) return "未配置"
  return `${settings.value.gatewayKey.slice(0, 14)}••••••••`
})

const baseEndpoint = computed(() =>
  settings.value.baseUrl === "/gateway"
    ? `${window.location.origin}/gateway/v1`
    : `${settings.value.baseUrl.replace(/\/$/, "")}/v1`,
)

async function copyEndpoint() {
  await navigator.clipboard.writeText(baseEndpoint.value)
  copied.value = true
  window.setTimeout(() => {
    copied.value = false
  }, 1400)
}
</script>

<template>
  <PageHeader title="API Key" description="核对当前凭据作用域与 OpenAI-compatible 接入地址。">
    <Badge :tone="settings.gatewayKey || isDemo ? 'positive' : 'warning'">
      <CheckCircle2 v-if="settings.gatewayKey || isDemo" class="size-3" />
      {{ settings.gatewayKey || isDemo ? "凭据已连接" : "尚未配置" }}
    </Badge>
  </PageHeader>

  <div class="grid grid-cols-[minmax(0,1.4fr)_minmax(320px,0.7fr)] gap-4">
    <section class="panel">
      <div class="panel-header">
        <div>
          <div class="text-[13px] font-semibold">当前调用身份</div>
          <div class="mt-0.5 text-[10.5px] text-muted-foreground">完整密钥不会出现在页面或日志中</div>
        </div>
        <KeyRound class="size-4 text-accent" />
      </div>
      <div class="grid grid-cols-[150px_minmax(0,1fr)] text-xs">
        <div class="key-label">密钥</div>
        <div class="key-value font-mono">{{ maskedKey }}</div>
        <div class="key-label">API Base</div>
        <div class="key-value flex items-center gap-2 font-mono">
          <span class="truncate">{{ baseEndpoint }}</span>
          <Button variant="ghost" size="icon" :aria-label="copied ? '已复制' : '复制地址'" @click="copyEndpoint">
            <CheckCircle2 v-if="copied" class="size-3.5 text-positive" />
            <Copy v-else class="size-3.5" />
          </Button>
        </div>
        <div class="key-label">可用模型</div>
        <div class="key-value">{{ data.visibleModels.length }} 个网关模型</div>
        <div class="key-label">Usage 隔离</div>
        <div class="key-value text-positive">按 API Key 与租户强制过滤</div>
      </div>
    </section>

    <aside class="border border-info/30 bg-info-soft p-5">
      <LockKeyhole class="size-5 text-info" />
      <div class="mt-4 text-sm font-semibold">密钥由管理员签发</div>
      <p class="mt-2 text-xs leading-5 text-muted-foreground">
        当前后端没有面向终端用户的自助轮换接口，因此用户门户只显示连接状态，不伪造创建、撤销或充值功能。
      </p>
      <RouterLink
        to="/settings"
        class="mt-5 inline-flex text-xs font-semibold text-info underline underline-offset-4"
      >
        更换当前浏览器中的凭据
      </RouterLink>
    </aside>
  </div>

  <section class="panel mt-4">
    <div class="panel-header">
      <div>
        <div class="text-[13px] font-semibold">OpenAI-compatible 接入</div>
        <div class="mt-0.5 text-[10.5px] text-muted-foreground">客户端只需要 API Base、Key 和模型 ID</div>
      </div>
      <Terminal class="size-4 text-muted-foreground" />
    </div>
    <pre class="overflow-x-auto bg-[#171916] px-5 py-4 font-mono text-[11px] leading-6 text-[#dfe2d8]"><code><span class="text-[#7eb59f]">curl</span> {{ baseEndpoint }}/chat/completions \
  -H <span class="text-[#d3a06c]">"Authorization: Bearer $MODEL_VELO_API_KEY"</span> \
  -H <span class="text-[#d3a06c]">"Content-Type: application/json"</span> \
  -d <span class="text-[#b7c7db]">'{"model":"{{ data.visibleModels[0] || 'your-model' }}","messages":[{"role":"user","content":"Hello"}]}'</span></code></pre>
  </section>
</template>

<style scoped>
.key-label {
  display: flex;
  min-height: 52px;
  align-items: center;
  padding: 0 16px;
  border-right: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
  background: color-mix(in srgb, var(--surface-muted) 58%, transparent);
  color: var(--muted-foreground);
  font-size: 10.5px;
  font-weight: 650;
  letter-spacing: 0.05em;
  text-transform: uppercase;
}

.key-value {
  display: flex;
  min-width: 0;
  min-height: 52px;
  align-items: center;
  padding: 0 16px;
  border-bottom: 1px solid var(--border);
}

.key-label:nth-last-child(-n + 2),
.key-value:nth-last-child(-n + 2) {
  border-bottom: 0;
}
</style>
