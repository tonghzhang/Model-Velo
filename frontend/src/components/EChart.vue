<script setup lang="ts">
import * as echarts from "echarts/core"
import { BarChart, LineChart, PieChart } from "echarts/charts"
import {
  DatasetComponent,
  GridComponent,
  LegendComponent,
  TooltipComponent,
} from "echarts/components"
import { CanvasRenderer } from "echarts/renderers"
import type { EChartsCoreOption } from "echarts/core"
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue"

echarts.use([
  BarChart,
  LineChart,
  PieChart,
  DatasetComponent,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  CanvasRenderer,
])

const props = defineProps<{
  option: EChartsCoreOption
  height?: number
}>()

const element = ref<HTMLDivElement | null>(null)
let chart: echarts.ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let themeObserver: MutationObserver | null = null

function render() {
  if (!element.value) return
  if (!chart) chart = echarts.init(element.value)
  chart.setOption(props.option, true)
}

watch(
  () => props.option,
  () => void nextTick(render),
  { deep: true },
)

onMounted(() => {
  render()
  resizeObserver = new ResizeObserver(() => chart?.resize())
  resizeObserver.observe(element.value!)
  themeObserver = new MutationObserver(() => {
    chart?.dispose()
    chart = null
    void nextTick(render)
  })
  themeObserver.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["class"],
  })
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  themeObserver?.disconnect()
  chart?.dispose()
})
</script>

<template>
  <div ref="element" class="w-full" :style="{ height: `${height || 280}px` }" />
</template>
