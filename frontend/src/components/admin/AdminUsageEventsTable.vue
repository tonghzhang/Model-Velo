<script setup lang="ts">
import { computed } from "vue"
import type { DeepReadonly } from "vue"
import ProviderMark from "@/components/ProviderMark.vue"
import StatusBadge from "@/components/StatusBadge.vue"
import Badge from "@/components/ui/Badge.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import Skeleton from "@/components/ui/Skeleton.vue"
import {
  compactNumber,
  formatDateTime,
  formatDuration,
  shortID,
} from "@/lib/format"
import type { APIKey, PlatformUsageRecord, Tenant } from "@/types"

const props = defineProps<{
  records: readonly DeepReadonly<PlatformUsageRecord>[]
  tenants: readonly DeepReadonly<Tenant>[]
  keys: readonly DeepReadonly<APIKey>[]
  loading: boolean
}>()

const tenantByID = computed(
  () => new Map(props.tenants.map((tenant) => [tenant.id, tenant])),
)
const keyByID = computed(() => new Map(props.keys.map((key) => [key.id, key])))
</script>

<template>
  <section class="panel mt-4 min-w-0">
    <div class="panel-header">
      <div>
        <div class="text-[13px] font-semibold">跨租户请求明细</div>
        <div class="mt-0.5 text-[10.5px] text-muted-foreground">
          最近 100 条事件；按当前租户、API Key 与模型筛选
        </div>
      </div>
      <Badge tone="neutral">{{ records.length }} rows</Badge>
    </div>

    <div v-if="loading && !records.length" class="divide-y divide-border">
      <div
        v-for="index in 6"
        :key="index"
        class="flex h-[52px] items-center gap-6 px-4"
      >
        <Skeleton class="h-5 w-14" />
        <Skeleton class="h-3 w-28" />
        <Skeleton class="h-3 w-24" />
        <Skeleton class="h-5 w-28" />
        <Skeleton class="ml-auto h-3 w-16" />
      </div>
    </div>

    <div v-else-if="records.length" class="overflow-x-auto">
      <table class="data-table min-w-[1120px]">
        <thead>
          <tr>
            <th>状态</th>
            <th>租户 / 用户</th>
            <th>API Key</th>
            <th>请求 ID</th>
            <th>模型</th>
            <th>Provider</th>
            <th class="text-right">Token</th>
            <th class="text-right">延迟</th>
            <th>时间</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="record in records" :key="record.event_id">
            <td><StatusBadge :status="record.status" /></td>
            <td>
              <div class="flex items-center gap-2">
                <span class="tenant-dot" />
                <div class="min-w-0">
                  <div class="max-w-40 truncate text-[11px] font-semibold">
                    {{ tenantByID.get(record.tenant_id)?.display_name || record.tenant_id }}
                  </div>
                  <div class="mt-0.5 font-mono text-[9px] text-muted-foreground">
                    {{ tenantByID.get(record.tenant_id)?.slug || shortID(record.tenant_id) }}
                  </div>
                </div>
              </div>
            </td>
            <td>
              <div class="max-w-36 truncate font-mono text-[10.5px] font-medium">
                {{ keyByID.get(record.api_key_id || "")?.label || shortID(record.api_key_id) }}
              </div>
              <div class="mt-0.5 font-mono text-[9px] text-muted-foreground">
                {{ keyByID.get(record.api_key_id || "")?.key_prefix || shortID(record.api_key_id) }}
              </div>
            </td>
            <td class="font-mono text-[10.5px] text-muted-foreground">
              {{ shortID(record.request_id) }}
            </td>
            <td>
              <Badge tone="neutral" class="font-mono font-medium">
                {{ record.requested_model }}
              </Badge>
            </td>
            <td><ProviderMark :provider="record.provider_id || 'unknown'" /></td>
            <td class="tabular text-right font-medium">
              {{ compactNumber(record.usage?.total || 0) }}
            </td>
            <td class="tabular text-right">{{ formatDuration(record.latency_ms) }}</td>
            <td class="whitespace-nowrap text-[10.5px] text-muted-foreground">
              {{ formatDateTime(record.started_at) }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <EmptyState
      v-else
      title="当前范围没有请求"
      description="Usage Worker 落库后，请求会按租户和 API Key 出现在这里。"
    />
  </section>
</template>

<style scoped>
.tenant-dot {
  width: 7px;
  height: 7px;
  flex: 0 0 auto;
  border: 2px solid var(--accent);
  border-radius: 999px;
  box-shadow: 0 0 0 3px var(--accent-soft);
}
</style>
