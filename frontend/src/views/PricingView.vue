<script setup lang="ts">
import { computed } from "vue"
import { CircleDollarSign, Download, Edit3, Plus, TriangleAlert } from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import ProviderMark from "@/components/ProviderMark.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import { useConsole } from "@/composables/useConsole"
import { formatUSD } from "@/lib/format"

const { data } = useConsole()
const pricedModels = computed(() => new Set(data.value.pricing?.prices.map((item) => item.model)))
const runtimeModels = computed(
  () => new Set(data.value.runtime?.document.providers.flatMap((provider) => provider.models) || []),
)
const unpriced = computed(() => [...runtimeModels.value].filter((model) => !pricedModels.value.has(model)))
</script>

<template>
  <PageHeader title="成本定价" description="维护版本化价格快照；未知价格保持为空，不做估算。">
    <Button variant="secondary" size="sm"><Download class="size-3.5" />导出 JSON</Button>
    <Button size="sm"><Plus class="size-3.5" />添加价格</Button>
  </PageHeader>

  <div class="grid grid-cols-[1fr_1fr_1fr_1.5fr] divide-x divide-border border border-border bg-surface">
    <div class="px-5 py-4">
      <div class="eyebrow">Pricing version</div>
      <div class="tabular mt-2 text-xl font-semibold">v{{ data.pricing?.version || 0 }}</div>
    </div>
    <div class="px-5 py-4">
      <div class="eyebrow">价格条目</div>
      <div class="tabular mt-2 text-xl font-semibold">{{ data.pricing?.prices.length || 0 }}</div>
    </div>
    <div class="px-5 py-4">
      <div class="eyebrow">未定价模型</div>
      <div class="tabular mt-2 text-xl font-semibold" :class="unpriced.length ? 'text-warning' : ''">
        {{ unpriced.length }}
      </div>
    </div>
    <div class="flex items-center gap-3 px-5 py-4">
      <div class="grid size-8 shrink-0 place-items-center rounded-md bg-accent-soft text-accent">
        <CircleDollarSign class="size-4" />
      </div>
      <div>
        <div class="text-xs font-semibold">成本按 Usage Event 固化</div>
        <div class="mt-1 text-[10.5px] text-muted-foreground">历史事件保留当时的 pricing_version</div>
      </div>
    </div>
  </div>

  <div
    v-if="unpriced.length"
    class="mt-4 flex items-center gap-3 border border-warning/30 bg-warning-soft px-4 py-3 text-xs text-warning"
  >
    <TriangleAlert class="size-4 shrink-0" />
    <span class="font-semibold">检测到未定价模型</span>
    <span class="text-warning/85">{{ unpriced.join("、") }} 的请求成本将保持 NULL。</span>
  </div>

  <section class="panel mt-4">
    <div class="panel-header">
      <div>
        <div class="text-[13px] font-semibold">价格目录</div>
        <div class="mt-0.5 text-[10.5px] text-muted-foreground">单位：USD / 1M tokens</div>
      </div>
      <Badge tone="neutral">ETag version {{ data.pricing?.version || 0 }}</Badge>
    </div>
    <EmptyState
      v-if="!data.pricing?.prices.length"
      title="没有托管 Pricing"
      description="后端返回 pricing_not_managed，价格可能来自环境变量。"
    />
    <table v-else class="data-table">
      <thead>
        <tr>
          <th>Provider</th>
          <th>上游模型</th>
          <th>版本</th>
          <th class="text-right">Input</th>
          <th class="text-right">Output</th>
          <th class="text-right">Cached read</th>
          <th class="text-right">Cached write</th>
          <th>生效区间</th>
          <th />
        </tr>
      </thead>
      <tbody>
        <tr v-for="price in data.pricing.prices" :key="`${price.provider}-${price.model}-${price.version}`">
          <td><ProviderMark :provider="price.provider" /></td>
          <td><Badge tone="neutral" class="font-mono">{{ price.model }}</Badge></td>
          <td class="font-mono text-[10.5px] text-muted-foreground">{{ price.version }}</td>
          <td class="tabular text-right font-medium">{{ formatUSD(price.input_usd_per_million) }}</td>
          <td class="tabular text-right font-medium">{{ formatUSD(price.output_usd_per_million) }}</td>
          <td class="tabular text-right">{{ price.cached_read_usd_per_million ? formatUSD(price.cached_read_usd_per_million) : "—" }}</td>
          <td class="tabular text-right">{{ price.cached_write_usd_per_million ? formatUSD(price.cached_write_usd_per_million) : "—" }}</td>
          <td class="text-[10.5px] text-muted-foreground">
            {{ price.effective_from || "立即" }} → {{ price.effective_until || "持续" }}
          </td>
          <td class="text-right"><Button variant="ghost" size="icon"><Edit3 class="size-3.5" /></Button></td>
        </tr>
      </tbody>
    </table>
  </section>
</template>
