<script setup lang="ts">
import { computed } from "vue"
import type { EChartsCoreOption } from "echarts/core"
import {
  ArrowRight,
  CheckCircle2,
  CircleAlert,
  Clock3,
  DatabaseZap,
  Server,
  ShieldCheck,
} from "lucide-vue-next"
import { RouterLink } from "vue-router"
import EChart from "@/components/EChart.vue"
import MetricCell from "@/components/MetricCell.vue"
import PageHeader from "@/components/PageHeader.vue"
import ProviderMark from "@/components/ProviderMark.vue"
import StatusBadge from "@/components/StatusBadge.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import Skeleton from "@/components/ui/Skeleton.vue"
import { useAdminUsage } from "@/composables/useAdminUsage"
import { useConsole } from "@/composables/useConsole"
import {
  compactNumber,
  formatDateTime,
  formatDuration,
  formatUSD,
  shortID,
} from "@/lib/format"

const { data, loading: consoleLoading, refresh: refreshConsole } = useConsole()
const {
  snapshot: platformUsage,
  loading: usageLoading,
  error: usageError,
  refresh: refreshUsage,
} = useAdminUsage()
const loading = computed(() => consoleLoading.value || usageLoading.value)

function refresh() {
  void Promise.all([refreshConsole(), refreshUsage()])
}

const successRate = computed(() => {
  if (!platformUsage.value.totals.requests) return 0
  return (
    (platformUsage.value.totals.successful_requests /
      platformUsage.value.totals.requests) *
    100
  )
})

const cacheRate = computed(() => {
  if (!platformUsage.value.totals.requests) return 0
  return (
    (platformUsage.value.totals.cache_hits / platformUsage.value.totals.requests) *
    100
  )
})

const chartOption = computed<EChartsCoreOption>(() => ({
  animationDuration: 450,
  color: ["#b97436", "#55947d", "#b85a58"],
  tooltip: {
    trigger: "axis",
    backgroundColor: "rgba(25, 27, 24, .94)",
    borderWidth: 0,
    textStyle: { color: "#f1f1ec", fontSize: 11 },
    axisPointer: { lineStyle: { color: "#777d72", type: "dashed" } },
  },
  grid: { top: 22, right: 14, bottom: 28, left: 46 },
  xAxis: {
    type: "category",
    boundaryGap: false,
    data: platformUsage.value.series.map((point) => point.bucket.slice(5)),
    axisLine: { lineStyle: { color: "#5b6058" } },
    axisTick: { show: false },
    axisLabel: { color: "#858b81", fontSize: 10, interval: 1 },
  },
  yAxis: {
    type: "value",
    axisLabel: {
      color: "#858b81",
      fontSize: 10,
      formatter: (value: number) => compactNumber(value),
    },
    splitLine: { lineStyle: { color: "rgba(128, 132, 124, .16)" } },
  },
  series: [
    {
      name: "请求",
      type: "line",
      smooth: 0.3,
      symbol: "none",
      lineStyle: { width: 2 },
      areaStyle: {
        color: {
          type: "linear",
          x: 0,
          y: 0,
          x2: 0,
          y2: 1,
          colorStops: [
            { offset: 0, color: "rgba(185, 116, 54, .26)" },
            { offset: 1, color: "rgba(185, 116, 54, 0)" },
          ],
        },
      },
      data: platformUsage.value.series.map((point) => point.totals.requests),
    },
    {
      name: "缓存命中",
      type: "line",
      smooth: 0.3,
      symbol: "none",
      lineStyle: { width: 1.5 },
      data: platformUsage.value.series.map((point) => point.totals.cache_hits),
    },
    {
      name: "失败",
      type: "line",
      smooth: 0.3,
      symbol: "none",
      lineStyle: { width: 1.5 },
      data: platformUsage.value.series.map((point) => point.totals.failed_requests),
    },
  ],
}))

const providerStats = computed(() => {
  const providerMap = new Map<string, { requests: number; latency: number; failures: number }>()
  for (const record of platformUsage.value.events) {
    const provider = record.provider_id || "unknown"
    const current = providerMap.get(provider) || { requests: 0, latency: 0, failures: 0 }
    current.requests += 1
    current.latency += record.latency_ms
    if (["failed", "stream_interrupted"].includes(record.status)) current.failures += 1
    providerMap.set(provider, current)
  }
  return [...providerMap.entries()]
    .map(([provider, stats]) => ({
      provider,
      requests: stats.requests,
      latency: stats.requests ? stats.latency / stats.requests : 0,
      state: stats.failures > 1 ? "attention" : "healthy",
    }))
    .sort((a, b) => b.requests - a.requests)
})

const tenantByID = computed(
  () => new Map(data.value.tenants.map((tenant) => [tenant.id, tenant])),
)

</script>

<template>
  <PageHeader
    title="运行总览"
    description="控制面运行状态，以及跨租户的平台流量与可靠性信号。"
  >
    <div class="flex h-8 items-center border border-border bg-surface px-2.5 text-[11px] text-muted-foreground">
      近 14 天 · 平台口径
    </div>
    <Button variant="secondary" size="sm" :disabled="loading" @click="refresh">
      刷新数据
    </Button>
  </PageHeader>

  <div
    v-if="usageError"
    class="mb-4 flex items-center gap-3 border border-negative/30 bg-negative-soft px-3.5 py-2.5 text-xs text-negative"
  >
    <CircleAlert class="size-4 shrink-0" />
    <span>平台用量读取失败：{{ usageError }}</span>
    <RouterLink to="/admin/usage" class="ml-auto font-semibold underline underline-offset-2">
      查看详情
    </RouterLink>
  </div>

  <template v-if="loading && !platformUsage.totals.requests">
    <div class="overview-metric-grid grid grid-cols-5 border border-border bg-surface">
      <div v-for="index in 5" :key="index" class="px-5 py-5">
        <Skeleton class="h-3 w-20" />
        <Skeleton class="mt-3 h-7 w-28" />
        <Skeleton class="mt-2 h-2.5 w-24" />
      </div>
    </div>
  </template>
  <div v-else class="overview-metric-grid grid grid-cols-5 border border-border bg-surface">
    <MetricCell
      label="平台请求"
      :value="compactNumber(platformUsage.totals.requests)"
      :note="`${platformUsage.tenantGroups.length} 个活跃租户`"
    />
    <MetricCell
      label="成功率"
      :value="`${successRate.toFixed(2)}%`"
      tone="positive"
      :note="`${compactNumber(platformUsage.totals.failed_requests)} 个失败请求`"
    />
    <MetricCell
      label="总 Token"
      :value="compactNumber(platformUsage.totals.total_tokens)"
      tone="neutral"
      :note="`${compactNumber(platformUsage.totals.cache_saved_tokens)} 已节省`"
    />
    <MetricCell
      label="已知成本"
      :value="formatUSD(platformUsage.totals.total_cost_usd)"
      tone="positive"
      :note="`${platformUsage.totals.unknown_cost_requests} 个请求成本未知`"
    />
    <MetricCell
      label="平均延迟"
      :value="formatDuration(platformUsage.totals.average_latency_ms)"
      tone="positive"
      :note="`TTFT ${formatDuration(platformUsage.totals.average_first_token_ms)}`"
    />
  </div>

  <div class="overview-primary-grid mt-4 grid grid-cols-[minmax(0,1.75fr)_minmax(280px,0.75fr)] gap-4">
    <section class="panel min-w-0">
      <div class="panel-header">
        <div>
          <div class="text-[13px] font-semibold">请求趋势</div>
          <div class="mt-0.5 text-[10.5px] text-muted-foreground">请求、缓存命中与失败量</div>
        </div>
        <div class="flex items-center gap-4 text-[10.5px] text-muted-foreground">
          <span class="flex items-center gap-1.5"><span class="size-2 rounded-full bg-[#b97436]" />请求</span>
          <span class="flex items-center gap-1.5"><span class="size-2 rounded-full bg-[#55947d]" />缓存</span>
          <span class="flex items-center gap-1.5"><span class="size-2 rounded-full bg-[#b85a58]" />失败</span>
        </div>
      </div>
      <div class="px-3 pb-1 pt-2">
        <EChart :option="chartOption" :height="268" />
      </div>
    </section>

    <section class="panel">
      <div class="panel-header">
        <div>
          <div class="text-[13px] font-semibold">系统状态</div>
          <div class="mt-0.5 text-[10.5px] text-muted-foreground">核心依赖与运行配置</div>
        </div>
        <StatusBadge :status="data.health" />
      </div>
      <div class="divide-y divide-border">
        <div class="flex items-center gap-3 px-4 py-3.5">
          <div class="grid size-8 place-items-center rounded-md bg-positive-soft text-positive">
            <Server class="size-4" />
          </div>
          <div class="min-w-0 flex-1">
            <div class="text-xs font-semibold">Gateway API</div>
            <div class="mt-0.5 text-[10.5px] text-muted-foreground">healthz 正常响应</div>
          </div>
          <CheckCircle2 class="size-4 text-positive" />
        </div>
        <div class="flex items-center gap-3 px-4 py-3.5">
          <div class="grid size-8 place-items-center rounded-md bg-info-soft text-info">
            <DatabaseZap class="size-4" />
          </div>
          <div class="min-w-0 flex-1">
            <div class="text-xs font-semibold">Runtime config</div>
            <div class="mt-0.5 text-[10.5px] text-muted-foreground">
              {{ data.runtime ? `版本 ${data.runtime.version}` : "未启用托管配置" }}
            </div>
          </div>
          <Badge tone="neutral">{{ data.runtime?.document.providers.length || 0 }} providers</Badge>
        </div>
        <div class="flex items-center gap-3 px-4 py-3.5">
          <div class="grid size-8 place-items-center rounded-md bg-accent-soft text-accent">
            <ShieldCheck class="size-4" />
          </div>
          <div class="min-w-0 flex-1">
            <div class="text-xs font-semibold">响应缓存</div>
            <div class="mt-0.5 text-[10.5px] text-muted-foreground">Exact cache 命中率</div>
          </div>
          <span class="tabular text-xs font-semibold">{{ cacheRate.toFixed(1) }}%</span>
        </div>
        <div class="flex items-center gap-3 px-4 py-3.5">
          <div class="grid size-8 place-items-center rounded-md bg-warning-soft text-warning">
            <Clock3 class="size-4" />
          </div>
          <div class="min-w-0 flex-1">
            <div class="text-xs font-semibold">重试与回退</div>
            <div class="mt-0.5 text-[10.5px] text-muted-foreground">当前查询窗口</div>
          </div>
          <span class="tabular text-xs font-semibold">
            {{ platformUsage.totals.retries }} / {{ platformUsage.totals.fallbacks }}
          </span>
        </div>
      </div>
    </section>
  </div>

  <div class="overview-secondary-grid mt-4 grid grid-cols-[minmax(0,1.5fr)_minmax(320px,0.8fr)] gap-4">
    <section class="panel min-w-0">
      <div class="panel-header">
        <div>
          <div class="text-[13px] font-semibold">最近请求</div>
          <div class="mt-0.5 text-[10.5px] text-muted-foreground">跨租户 Usage 事件</div>
        </div>
        <RouterLink
          to="/admin/usage"
          class="flex items-center gap-1 text-[11px] font-semibold text-accent hover:underline"
        >
          查看全部 <ArrowRight class="size-3" />
        </RouterLink>
      </div>
      <div class="overflow-x-auto">
        <table class="data-table">
          <thead>
            <tr>
              <th>状态</th>
              <th>租户</th>
              <th>请求 ID</th>
              <th>模型</th>
              <th>Provider</th>
              <th class="text-right">Token</th>
              <th class="text-right">延迟</th>
              <th class="overview-time">时间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="record in platformUsage.events.slice(0, 6)" :key="record.event_id">
              <td><StatusBadge :status="record.status" /></td>
              <td>
                <span class="max-w-32 truncate text-[10.5px] font-semibold">
                  {{ tenantByID.get(record.tenant_id)?.display_name || shortID(record.tenant_id) }}
                </span>
              </td>
              <td class="font-mono text-[11px] text-muted-foreground">
                {{ shortID(record.request_id) }}
              </td>
              <td>
                <Badge tone="neutral" class="font-mono font-medium">
                  {{ record.requested_model }}
                </Badge>
              </td>
              <td><ProviderMark :provider="record.provider_id || 'unknown'" /></td>
              <td class="tabular text-right font-medium">{{ compactNumber(record.usage?.total || 0) }}</td>
              <td class="tabular text-right">{{ formatDuration(record.latency_ms) }}</td>
              <td class="overview-time whitespace-nowrap text-muted-foreground">
                {{ formatDateTime(record.started_at) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section class="panel">
      <div class="panel-header">
        <div>
          <div class="text-[13px] font-semibold">Provider 观测</div>
          <div class="mt-0.5 text-[10.5px] text-muted-foreground">基于最近事件的轻量摘要</div>
        </div>
        <CircleAlert class="size-4 text-muted-foreground" />
      </div>
      <div class="divide-y divide-border">
        <div
          v-for="provider in providerStats"
          :key="provider.provider"
          class="grid grid-cols-[minmax(0,1fr)_58px_66px] items-center gap-3 px-4 py-3"
        >
          <ProviderMark :provider="provider.provider" />
          <span class="tabular text-right text-[11px] text-muted-foreground">
            {{ provider.requests }} req
          </span>
          <div class="text-right">
            <span
              class="inline-flex items-center gap-1 text-[10.5px] font-semibold"
              :class="provider.state === 'healthy' ? 'text-positive' : 'text-warning'"
            >
              <span class="size-1.5 rounded-full bg-current" />
              {{ provider.state === "healthy" ? "正常" : "关注" }}
            </span>
            <div class="tabular mt-0.5 text-[9.5px] text-muted-foreground">
              {{ formatDuration(provider.latency) }}
            </div>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
