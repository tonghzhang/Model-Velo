<script setup lang="ts">
import { computed, ref, watchEffect } from "vue"
import { ChevronRight, Clock3, KeyRound, Plus, Search, Users } from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import StatusBadge from "@/components/StatusBadge.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import Input from "@/components/ui/Input.vue"
import { useConsole } from "@/composables/useConsole"
import { formatDateTime, relativeTime, shortID } from "@/lib/format"

const { data } = useConsole()
const tab = ref<"tenants" | "keys">("tenants")
const search = ref("")
const selectedTenantID = ref("")

watchEffect(() => {
  if (!selectedTenantID.value && data.value.tenants.length) {
    selectedTenantID.value = data.value.tenants[0]!.id
  }
})

const selectedTenant = computed(() =>
  data.value.tenants.find((tenant) => tenant.id === selectedTenantID.value),
)
const tenantKeys = computed(() =>
  data.value.keys.filter((key) => key.tenant_id === selectedTenantID.value),
)
const filteredKeys = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return data.value.keys.filter(
    (key) =>
      !needle ||
      key.label.toLowerCase().includes(needle) ||
      key.key_prefix.toLowerCase().includes(needle),
  )
})
</script>

<template>
  <PageHeader title="租户与 API Keys" description="管理租户隔离、模型授权与网关访问凭据。">
    <Button size="sm">
      <Plus class="size-3.5" />
      {{ tab === "tenants" ? "新建租户" : "签发 API Key" }}
    </Button>
  </PageHeader>

  <div class="mb-3 flex items-center gap-1 border-b border-border">
    <button
      class="border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors"
      :class="tab === 'tenants' ? 'border-accent text-foreground' : 'border-transparent text-muted-foreground'"
      @click="tab = 'tenants'"
    >
      <span class="flex items-center gap-2"><Users class="size-3.5" />租户</span>
    </button>
    <button
      class="border-b-2 px-4 py-2.5 text-xs font-semibold transition-colors"
      :class="tab === 'keys' ? 'border-accent text-foreground' : 'border-transparent text-muted-foreground'"
      @click="tab = 'keys'"
    >
      <span class="flex items-center gap-2"><KeyRound class="size-3.5" />API Keys</span>
    </button>
  </div>

  <div
    v-if="tab === 'tenants' && data.tenants.length"
    class="grid min-h-[620px] grid-cols-[360px_minmax(0,1fr)] border border-border bg-surface"
  >
    <section class="border-r border-border">
      <div class="flex h-12 items-center justify-between border-b border-border px-4">
        <span class="text-xs font-semibold">租户目录</span>
        <Badge tone="neutral">{{ data.tenants.length }}</Badge>
      </div>
      <button
        v-for="tenant in data.tenants"
        :key="tenant.id"
        class="flex w-full items-center gap-3 border-b border-border px-4 py-3.5 text-left hover:bg-surface-muted"
        :class="{ 'bg-surface-muted': selectedTenantID === tenant.id }"
        @click="selectedTenantID = tenant.id"
      >
        <div class="grid size-8 shrink-0 place-items-center rounded-md bg-accent-soft text-[11px] font-bold text-accent">
          {{ tenant.display_name.slice(0, 2).toUpperCase() }}
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <span class="truncate text-xs font-semibold">{{ tenant.display_name }}</span>
            <StatusBadge :status="tenant.status" />
          </div>
          <div class="mt-1 font-mono text-[10px] text-muted-foreground">{{ tenant.slug }}</div>
        </div>
        <ChevronRight class="size-3.5 text-muted-foreground" />
      </button>
    </section>

    <section v-if="selectedTenant">
      <div class="flex min-h-20 items-center justify-between border-b border-border px-5">
        <div>
          <div class="flex items-center gap-2 text-sm font-semibold">
            {{ selectedTenant.display_name }}
            <StatusBadge :status="selectedTenant.status" />
          </div>
          <div class="mt-1 font-mono text-[10.5px] text-muted-foreground">
            {{ selectedTenant.id }} · version {{ selectedTenant.version }}
          </div>
        </div>
        <Button variant="secondary" size="sm">编辑租户</Button>
      </div>

      <div class="grid grid-cols-3 divide-x divide-border border-b border-border">
        <div class="px-5 py-4">
          <div class="eyebrow">授权模型</div>
          <div class="tabular mt-2 text-lg font-semibold">{{ selectedTenant.models.length }}</div>
        </div>
        <div class="px-5 py-4">
          <div class="eyebrow">API Keys</div>
          <div class="tabular mt-2 text-lg font-semibold">{{ tenantKeys.length }}</div>
        </div>
        <div class="px-5 py-4">
          <div class="eyebrow">最近更新</div>
          <div class="mt-2 text-xs font-semibold">{{ relativeTime(selectedTenant.updated_at) }}</div>
        </div>
      </div>

      <div class="p-5">
        <div class="mb-3 flex items-center justify-between">
          <div class="text-xs font-semibold">模型授权</div>
          <Button variant="ghost" size="sm">调整授权</Button>
        </div>
        <div class="flex min-h-14 flex-wrap items-center gap-2 border border-border bg-surface-raised p-3">
          <Badge v-for="model in selectedTenant.models" :key="model" tone="info" class="font-mono">
            {{ model }}
          </Badge>
        </div>

        <div class="mb-3 mt-6 flex items-center justify-between">
          <div class="text-xs font-semibold">API Keys</div>
          <Button variant="secondary" size="sm"><Plus class="size-3" />签发 Key</Button>
        </div>
        <div class="border border-border">
          <table class="data-table">
            <thead><tr><th>标签</th><th>前缀</th><th>状态</th><th>最近使用</th><th>过期时间</th></tr></thead>
            <tbody>
              <tr v-for="key in tenantKeys" :key="key.id">
                <td class="font-medium">{{ key.label }}</td>
                <td><Badge tone="neutral" class="font-mono">{{ key.key_prefix }}</Badge></td>
                <td><StatusBadge :status="key.status" /></td>
                <td class="text-muted-foreground">{{ relativeTime(key.last_used_at) }}</td>
                <td class="tabular text-muted-foreground">{{ key.expires_at ? formatDateTime(key.expires_at) : "永不过期" }}</td>
              </tr>
            </tbody>
          </table>
          <EmptyState
            v-if="!tenantKeys.length"
            title="该租户没有 API Key"
            description="签发后完整 Key 只会显示一次。"
          />
        </div>
      </div>
    </section>
  </div>

  <section v-else-if="tab === 'keys'" class="panel">
    <div class="flex h-14 items-center gap-3 border-b border-border px-3">
      <div class="relative w-72">
        <Search class="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="search" class="pl-8" placeholder="搜索标签或 Key 前缀" />
      </div>
      <Badge tone="neutral">{{ filteredKeys.length }} keys</Badge>
      <div class="ml-auto flex items-center gap-1.5 text-[10.5px] text-muted-foreground">
        <Clock3 class="size-3.5" />完整明文仅在签发响应中返回一次
      </div>
    </div>
    <table class="data-table">
      <thead><tr><th>标签</th><th>Key 前缀</th><th>所属租户</th><th>状态</th><th>最近使用</th><th>到期</th><th>ID</th></tr></thead>
      <tbody>
        <tr v-for="key in filteredKeys" :key="key.id">
          <td class="font-medium">{{ key.label }}</td>
          <td><Badge tone="accent" class="font-mono">{{ key.key_prefix }}</Badge></td>
          <td>{{ data.tenants.find((tenant) => tenant.id === key.tenant_id)?.display_name || key.tenant_id }}</td>
          <td><StatusBadge :status="key.status" /></td>
          <td class="text-muted-foreground">{{ relativeTime(key.last_used_at) }}</td>
          <td class="tabular text-muted-foreground">{{ key.expires_at ? formatDateTime(key.expires_at) : "永不过期" }}</td>
          <td class="font-mono text-[10px] text-muted-foreground">{{ shortID(key.id, 12) }}</td>
        </tr>
      </tbody>
    </table>
  </section>

  <section v-else class="panel">
    <EmptyState title="没有租户数据" description="配置 Admin Key 后可读取 /admin/v1/tenants。" />
  </section>
</template>
