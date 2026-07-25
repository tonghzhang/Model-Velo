<script setup lang="ts">
import {
  CircleDollarSign,
  Clock3,
  DatabaseZap,
  ShieldAlert,
  Users,
} from "lucide-vue-next"
import { computed } from "vue"
import { RouterLink } from "vue-router"
import AdminUsageEventsTable from "@/components/admin/AdminUsageEventsTable.vue"
import AdminUsageFilters from "@/components/admin/AdminUsageFilters.vue"
import AdminUsageTrend from "@/components/admin/AdminUsageTrend.vue"
import UsageBreakdown from "@/components/admin/UsageBreakdown.vue"
import MetricCell from "@/components/MetricCell.vue"
import PageHeader from "@/components/PageHeader.vue"
import Button from "@/components/ui/Button.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import Skeleton from "@/components/ui/Skeleton.vue"
import { useAdminUsage } from "@/composables/useAdminUsage"
import { useConsole } from "@/composables/useConsole"
import {
  compactNumber,
  formatDuration,
  formatUSD,
  relativeTime,
} from "@/lib/format"

const { data, isDemo } = useConsole()
const {
  filters,
  snapshot,
  loading,
  error,
  errorStatus,
  configured,
  lastUpdated,
  refresh,
} = useAdminUsage()

const models = computed(() => [
  ...new Set(
    data.value.runtime?.document.providers.flatMap((provider) => provider.models) || [],
  ),
])

const successRate = computed(() => {
  if (!snapshot.value.totals.requests) return 0
  return (
    (snapshot.value.totals.successful_requests / snapshot.value.totals.requests) *
    100
  )
})

const cacheRate = computed(() => {
  if (!snapshot.value.totals.requests) return 0
  return (snapshot.value.totals.cache_hits / snapshot.value.totals.requests) * 100
})

function selectTenant(tenantID: string) {
  filters.tenantID = filters.tenantID === tenantID ? "" : tenantID
  void refresh()
}

</script>

<template>
  <PageHeader
    title="平台用量"
    description="跨租户查看请求趋势、Token、成本与最近请求；用户门户的 Key 级隔离保持不变。"
  >
    <div class="text-right">
      <div class="text-[9.5px] text-muted-foreground">数据口径</div>
      <div class="mt-0.5 text-[10.5px] font-semibold">
        {{ isDemo ? "仿真平台数据" : "Usage Event 落库" }}
        ·
        {{ lastUpdated ? relativeTime(lastUpdated.toISOString()) : "尚未同步" }}
      </div>
    </div>
  </PageHeader>

  <AdminUsageFilters
    v-model:days="filters.days"
    v-model:tenant="filters.tenantID"
    v-model:api-key="filters.apiKeyID"
    v-model:model="filters.model"
    :tenants="data.tenants"
    :keys="data.keys"
    :models="models"
    :loading="loading"
    @apply="refresh"
  />

  <section v-if="!configured" class="panel">
    <EmptyState
      title="需要 Admin Key"
      description="平台用量走 /admin/v1/usage/*，不需要也不会读取某个用户的 Gateway Key。"
    >
      <RouterLink to="/settings" class="mt-4">
        <Button size="sm">配置连接</Button>
      </RouterLink>
    </EmptyState>
  </section>

  <section
    v-else-if="error && !snapshot.totals.requests"
    class="panel border-negative/35"
  >
    <div class="flex min-h-52 flex-col items-center justify-center px-6 text-center">
      <div class="grid size-10 place-items-center bg-negative-soft text-negative">
        <ShieldAlert class="size-4.5" />
      </div>
      <div class="mt-3 text-sm font-semibold">
        {{ errorStatus === 403 ? "无权读取平台用量" : "平台用量读取失败" }}
      </div>
      <p class="mt-1 max-w-md text-xs leading-5 text-muted-foreground">{{ error }}</p>
      <Button class="mt-4" variant="secondary" size="sm" @click="refresh">重试</Button>
    </div>
  </section>

  <template v-else>
    <div
      v-if="error"
      class="mb-4 flex items-center gap-3 border border-negative/30 bg-negative-soft px-3.5 py-2.5 text-xs text-negative"
    >
      <ShieldAlert class="size-4 shrink-0" />
      <span>{{ error }}；当前保留上次成功数据。</span>
    </div>

    <div
      v-if="loading && !snapshot.totals.requests"
      class="overview-metric-grid grid grid-cols-5 border border-border bg-surface"
    >
      <div v-for="index in 5" :key="index" class="px-5 py-5">
        <Skeleton class="h-3 w-20" />
        <Skeleton class="mt-3 h-7 w-28" />
        <Skeleton class="mt-2 h-2.5 w-24" />
      </div>
    </div>
    <div
      v-else
      class="overview-metric-grid grid grid-cols-5 border border-border bg-surface"
    >
      <MetricCell
        label="平台请求"
        :value="compactNumber(snapshot.totals.requests)"
        :note="`${snapshot.tenantGroups.length} 个活跃租户`"
      >
        <Users class="size-3.5" />
      </MetricCell>
      <MetricCell
        label="成功率"
        :value="`${successRate.toFixed(2)}%`"
        tone="positive"
        :note="`${compactNumber(snapshot.totals.failed_requests)} 个失败请求`"
      >
        <DatabaseZap class="size-3.5" />
      </MetricCell>
      <MetricCell
        label="总 Token"
        :value="compactNumber(snapshot.totals.total_tokens)"
        :note="`${compactNumber(snapshot.totals.cache_saved_tokens)} 已节省`"
      />
      <MetricCell
        label="已知成本"
        :value="formatUSD(snapshot.totals.total_cost_usd)"
        :note="`${snapshot.totals.unknown_cost_requests} 个请求成本未知`"
      >
        <CircleDollarSign class="size-3.5" />
      </MetricCell>
      <MetricCell
        label="平均延迟"
        :value="formatDuration(snapshot.totals.average_latency_ms)"
        :note="`缓存命中 ${cacheRate.toFixed(1)}%`"
      >
        <Clock3 class="size-3.5" />
      </MetricCell>
    </div>

    <div
      class="overview-primary-grid mt-4 grid grid-cols-[minmax(0,1.55fr)_minmax(340px,0.85fr)] gap-4"
    >
      <AdminUsageTrend :series="snapshot.series" />
      <UsageBreakdown
        :tenant-groups="snapshot.tenantGroups"
        :key-groups="snapshot.keyGroups"
        :tenants="data.tenants"
        :keys="data.keys"
        :selected-tenant="filters.tenantID"
        @select-tenant="selectTenant"
      />
    </div>

    <AdminUsageEventsTable
      :records="snapshot.events"
      :tenants="data.tenants"
      :keys="data.keys"
      :loading="loading"
    />
  </template>
</template>
