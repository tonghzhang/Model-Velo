<script setup lang="ts">
import { ArrowRight } from "lucide-vue-next"
import { RouterLink } from "vue-router"
import ProviderMark from "@/components/ProviderMark.vue"
import StatusBadge from "@/components/StatusBadge.vue"
import Badge from "@/components/ui/Badge.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import { compactNumber, formatDateTime, formatDuration, shortID } from "@/lib/format"
import type { UsageRecord } from "@/types"

defineProps<{
  records: readonly UsageRecord[]
}>()
</script>

<template>
  <section class="panel min-w-0">
    <div class="panel-header">
      <div>
        <div class="text-[13px] font-semibold">最近请求</div>
        <div class="mt-0.5 text-[10.5px] text-muted-foreground">当前凭据可见的最新 Usage 事件</div>
      </div>
      <RouterLink
        to="/portal/usage"
        class="flex items-center gap-1 text-[11px] font-semibold text-[var(--workspace-accent)] hover:underline"
      >
        查看全部 <ArrowRight class="size-3" />
      </RouterLink>
    </div>
    <EmptyState
      v-if="!records.length"
      title="还没有请求记录"
      description="使用当前 API Key 发起模型请求后，Usage 事件会显示在这里。"
    />
    <div v-else class="overflow-x-auto">
      <table class="data-table">
        <thead>
          <tr>
            <th>状态</th>
            <th>请求 ID</th>
            <th>模型</th>
            <th>Provider</th>
            <th class="text-right">Token</th>
            <th class="text-right">延迟</th>
            <th>时间</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="record in records.slice(0, 7)" :key="record.event_id">
            <td><StatusBadge :status="record.status" /></td>
            <td class="font-mono text-[10.5px] text-muted-foreground">
              {{ shortID(record.request_id) }}
            </td>
            <td>
              <Badge tone="neutral" class="font-mono">{{ record.requested_model }}</Badge>
            </td>
            <td><ProviderMark :provider="record.provider_id || 'unknown'" /></td>
            <td class="tabular text-right font-medium">
              {{ compactNumber(record.usage?.total || 0) }}
            </td>
            <td class="tabular text-right">{{ formatDuration(record.latency_ms) }}</td>
            <td class="tabular whitespace-nowrap text-muted-foreground">
              {{ formatDateTime(record.started_at) }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
