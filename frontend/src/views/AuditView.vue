<script setup lang="ts">
import { computed, ref } from "vue"
import { ChevronDown, FileDiff, Search, ShieldCheck } from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import Input from "@/components/ui/Input.vue"
import { useConsole } from "@/composables/useConsole"
import { formatDateTime, shortID } from "@/lib/format"

const { data } = useConsole()
const search = ref("")

const filtered = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return data.value.audit.filter(
    (record) =>
      !needle ||
      record.action.toLowerCase().includes(needle) ||
      record.resource_id?.toLowerCase().includes(needle) ||
      record.request_id.toLowerCase().includes(needle),
  )
})

function actionTone(action: string) {
  if (action.includes("status")) return "warning" as const
  if (action.includes("create")) return "positive" as const
  if (action.includes("update")) return "info" as const
  return "neutral" as const
}
</script>

<template>
  <PageHeader title="审计日志" description="追踪控制面配置、租户、Key、配额和管理员变更。">
    <Button variant="secondary" size="sm">导出当前页</Button>
  </PageHeader>

  <section class="panel">
    <div class="flex h-14 items-center gap-2 border-b border-border px-3">
      <div class="relative w-80">
        <Search class="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="search" class="pl-8" placeholder="搜索操作、资源 ID 或请求 ID" />
      </div>
      <div class="relative">
        <select class="h-9 appearance-none rounded-md border border-border-strong bg-surface-raised pl-3 pr-8 text-[12px]">
          <option>全部资源</option>
          <option>Runtime</option>
          <option>Tenant</option>
          <option>API Key</option>
          <option>Quota</option>
        </select>
        <ChevronDown class="pointer-events-none absolute right-2.5 top-1/2 size-3 -translate-y-1/2" />
      </div>
      <div class="ml-auto flex items-center gap-2 text-[10.5px] text-muted-foreground">
        <ShieldCheck class="size-3.5 text-positive" />
        审计记录只读
      </div>
    </div>

    <EmptyState
      v-if="!filtered.length"
      title="没有审计记录"
      description="配置具备 audit:read 权限的 Admin Key 后可读取记录。"
    />
    <table v-else class="data-table">
      <thead>
        <tr>
          <th>ID</th>
          <th>时间</th>
          <th>操作</th>
          <th>资源</th>
          <th>管理员</th>
          <th>请求 ID</th>
          <th>来源 IP</th>
          <th>结果</th>
          <th />
        </tr>
      </thead>
      <tbody>
        <tr v-for="record in filtered" :key="record.id">
          <td class="tabular text-muted-foreground">#{{ record.id }}</td>
          <td class="tabular whitespace-nowrap">{{ formatDateTime(record.created_at) }}</td>
          <td><Badge :tone="actionTone(record.action)" class="font-mono">{{ record.action }}</Badge></td>
          <td>
            <div class="text-xs font-medium">{{ record.resource_type }}</div>
            <div class="mt-0.5 font-mono text-[9.5px] text-muted-foreground">{{ shortID(record.resource_id, 12) }}</div>
          </td>
          <td class="font-mono text-[10.5px]">{{ shortID(record.principal_id, 12) }}</td>
          <td class="font-mono text-[10.5px] text-muted-foreground">{{ shortID(record.request_id, 12) }}</td>
          <td class="font-mono text-[10.5px] text-muted-foreground">{{ record.remote_ip || "—" }}</td>
          <td><Badge :tone="record.outcome === 'success' ? 'positive' : 'negative'">{{ record.outcome }}</Badge></td>
          <td><Button variant="ghost" size="icon"><FileDiff class="size-3.5" /></Button></td>
        </tr>
      </tbody>
    </table>
    <div class="flex h-12 items-center justify-end border-t border-border px-4">
      <Button variant="secondary" size="sm" :disabled="filtered.length < 100">加载更早记录</Button>
    </div>
  </section>
</template>
