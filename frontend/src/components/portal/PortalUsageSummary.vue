<script setup lang="ts">
import { computed } from "vue"
import type { DeepReadonly } from "vue"
import type { EChartsCoreOption } from "echarts/core"
import EChart from "@/components/EChart.vue"
import MetricCell from "@/components/MetricCell.vue"
import { compactNumber, formatDuration, formatUSD } from "@/lib/format"
import type { SeriesPoint, Totals } from "@/types"

const props = defineProps<{
  totals: DeepReadonly<Totals>
  series: readonly DeepReadonly<SeriesPoint>[]
}>()

const successRate = computed(() =>
  props.totals.requests
    ? (props.totals.successful_requests / props.totals.requests) * 100
    : 0,
)

const knownCostRate = computed(() =>
  props.totals.requests
    ? (props.totals.known_cost_requests / props.totals.requests) * 100
    : 0,
)

const chartOption = computed<EChartsCoreOption>(() => ({
  animationDuration: 420,
  color: ["#55947d", "#c0834b"],
  tooltip: {
    trigger: "axis",
    backgroundColor: "rgba(23, 25, 22, .95)",
    borderWidth: 0,
    textStyle: { color: "#f1f1ec", fontSize: 11 },
  },
  grid: { top: 20, right: 16, bottom: 28, left: 48 },
  xAxis: {
    type: "category",
    boundaryGap: false,
    data: props.series.map((point) => point.bucket.slice(5)),
    axisLine: { lineStyle: { color: "#596057" } },
    axisTick: { show: false },
    axisLabel: { color: "#858b81", fontSize: 10, interval: 1 },
  },
  yAxis: [
    {
      type: "value",
      axisLabel: {
        color: "#858b81",
        fontSize: 10,
        formatter: (value: number) => compactNumber(value),
      },
      splitLine: { lineStyle: { color: "rgba(128, 132, 124, .16)" } },
    },
    {
      type: "value",
      axisLabel: { show: false },
      splitLine: { show: false },
    },
  ],
  series: [
    {
      name: "请求",
      type: "line",
      smooth: 0.28,
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
            { offset: 0, color: "rgba(85, 148, 125, .25)" },
            { offset: 1, color: "rgba(85, 148, 125, 0)" },
          ],
        },
      },
      data: props.series.map((point) => point.totals.requests),
    },
    {
      name: "Token",
      type: "line",
      yAxisIndex: 1,
      smooth: 0.28,
      symbol: "none",
      lineStyle: { width: 1.5, type: "dashed" },
      data: props.series.map((point) => point.totals.total_tokens),
    },
  ],
}))
</script>

<template>
  <section>
    <div class="overview-metric-grid grid grid-cols-4 border border-border bg-surface">
      <MetricCell
        label="我的请求"
        :value="compactNumber(totals.requests)"
        :note="`${successRate.toFixed(2)}% 成功`"
      />
      <MetricCell
        label="使用 Token"
        :value="compactNumber(totals.total_tokens)"
        :note="`${compactNumber(totals.cache_saved_tokens)} 由缓存节省`"
      />
      <MetricCell
        label="已知成本"
        :value="formatUSD(totals.total_cost_usd)"
        :note="`${knownCostRate.toFixed(1)}% 请求已定价`"
      />
      <MetricCell
        label="平均延迟"
        :value="formatDuration(totals.average_latency_ms)"
        :note="`首 Token ${formatDuration(totals.average_first_token_ms)}`"
      />
    </div>

    <div class="panel mt-4">
      <div class="panel-header">
        <div>
          <div class="text-[13px] font-semibold">14 天用量轨迹</div>
          <div class="mt-0.5 text-[10.5px] text-muted-foreground">
            实线表示请求量，虚线表示 Token；仅统计当前业务 Key
          </div>
        </div>
        <div class="flex items-center gap-4 text-[10.5px] text-muted-foreground">
          <span class="flex items-center gap-1.5"><span class="size-2 bg-[#55947d]" />请求</span>
          <span class="flex items-center gap-1.5"><span class="size-2 bg-[#c0834b]" />Token</span>
        </div>
      </div>
      <div class="px-3 pb-1 pt-2">
        <EChart :option="chartOption" :height="260" />
      </div>
    </div>
  </section>
</template>
