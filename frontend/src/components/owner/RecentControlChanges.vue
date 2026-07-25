<script setup lang="ts">
import { ArrowRight } from "lucide-vue-next"
import { RouterLink } from "vue-router"
import Badge from "@/components/ui/Badge.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import { formatDateTime, shortID } from "@/lib/format"
import type { AuditRecord } from "@/types"

defineProps<{
  records: readonly AuditRecord[]
}>()
</script>

<template>
  <section class="panel">
    <div class="panel-header">
      <div>
        <div class="text-[13px] font-semibold">最近控制面变更</div>
        <div class="mt-0.5 text-[10.5px] text-muted-foreground">帮助 Owner 快速识别高权限操作</div>
      </div>
      <RouterLink
        to="/admin/audit"
        class="flex items-center gap-1 text-[11px] font-semibold text-accent hover:underline"
      >
        完整审计 <ArrowRight class="size-3" />
      </RouterLink>
    </div>
    <EmptyState v-if="!records.length" title="没有可见的审计记录" />
    <table v-else class="data-table">
      <thead>
        <tr>
          <th>时间</th>
          <th>操作</th>
          <th>资源</th>
          <th>管理员</th>
          <th>结果</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="record in records.slice(0, 6)" :key="record.id">
          <td class="tabular whitespace-nowrap">{{ formatDateTime(record.created_at) }}</td>
          <td><Badge tone="warning" class="font-mono">{{ record.action }}</Badge></td>
          <td>
            <div class="text-xs font-medium">{{ record.resource_type }}</div>
            <div class="mt-0.5 font-mono text-[9.5px] text-muted-foreground">
              {{ shortID(record.resource_id, 12) }}
            </div>
          </td>
          <td class="font-mono text-[10.5px]">{{ shortID(record.principal_id, 12) }}</td>
          <td>
            <Badge :tone="record.outcome === 'success' ? 'positive' : 'negative'">
              {{ record.outcome }}
            </Badge>
          </td>
        </tr>
      </tbody>
    </table>
  </section>
</template>
