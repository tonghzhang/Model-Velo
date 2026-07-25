<script setup lang="ts">
import { computed } from "vue"
import type { DeepReadonly } from "vue"
import type { EChartsCoreOption } from "echarts/core"
import EChart from "@/components/EChart.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import { compactNumber } from "@/lib/format"
import type { SeriesPoint } from "@/types"

const props = defineProps<{
  series: readonly DeepReadonly<SeriesPoint>[]
}>()

const option = computed<EChartsCoreOption>(() => ({
  animationDuration: 420,
  color: ["#b97436", "#55947d", "#b85a58"],
  tooltip: {
    trigger: "axis",
    backgroundColor: "rgba(25, 27, 24, .95)",
    borderWidth: 0,
    textStyle: { color: "#f1f1ec", fontSize: 11 },
    axisPointer: { lineStyle: { color: "#777d72", type: "dashed" } },
  },
  grid: { top: 18, right: 18, bottom: 30, left: 48 },
  xAxis: {
    type: "category",
    boundaryGap: false,
    data: props.series.map((point) => point.bucket.slice(5)),
    axisLine: { lineStyle: { color: "#5b6058" } },
    axisTick: { show: false },
    axisLabel: { color: "#858b81", fontSize: 10 },
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
            { offset: 0, color: "rgba(185, 116, 54, .23)" },
            { offset: 1, color: "rgba(185, 116, 54, 0)" },
          ],
        },
      },
      data: props.series.map((point) => point.totals.requests),
    },
    {
      name: "缓存命中",
      type: "line",
      smooth: 0.28,
      symbol: "none",
      lineStyle: { width: 1.5 },
      data: props.series.map((point) => point.totals.cache_hits),
    },
    {
      name: "失败",
      type: "line",
      smooth: 0.28,
      symbol: "none",
      lineStyle: { width: 1.5 },
      data: props.series.map((point) => point.totals.failed_requests),
    },
  ],
}))
</script>

<template>
  <section class="panel min-w-0">
    <div class="panel-header">
      <div>
        <div class="text-[13px] font-semibold">平台请求趋势</div>
        <div class="mt-0.5 text-[10.5px] text-muted-foreground">
          当前筛选范围内的请求、缓存命中与失败
        </div>
      </div>
      <div class="flex items-center gap-4 text-[10.5px] text-muted-foreground">
        <span class="flex items-center gap-1.5">
          <span class="size-2 rounded-full bg-[#b97436]" />请求
        </span>
        <span class="flex items-center gap-1.5">
          <span class="size-2 rounded-full bg-[#55947d]" />缓存
        </span>
        <span class="flex items-center gap-1.5">
          <span class="size-2 rounded-full bg-[#b85a58]" />失败
        </span>
      </div>
    </div>
    <div v-if="series.length" class="px-3 pb-1 pt-2">
      <EChart :option="option" :height="405" />
    </div>
    <EmptyState
      v-else
      title="当前范围没有趋势数据"
      description="调整时间、租户或 API Key 筛选后重试。"
    />
  </section>
</template>
