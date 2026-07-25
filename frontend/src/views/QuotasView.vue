<script setup lang="ts">
import { computed } from "vue"
import { BellRing, ChevronDown, Gauge, Plus, ShieldBan } from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import { useConsole } from "@/composables/useConsole"
import { compactNumber, formatUSD } from "@/lib/format"

const { data } = useConsole()

const policyRows = computed(() =>
  data.value.quotas.map((policy) => {
    const window = data.value.quotaWindows.find((item) => item.policy_id === policy.id)
    const requestUse =
      policy.request_limit && window
        ? ((window.requests_settled + window.requests_reserved) / policy.request_limit) * 100
        : null
    const tokenUse =
      policy.token_limit && window
        ? ((window.tokens_settled + window.tokens_reserved) / policy.token_limit) * 100
        : null
    const costUse =
      policy.budget_usd && window
        ? ((Number(window.cost_settled_usd) + Number(window.cost_reserved_usd)) /
            Number(policy.budget_usd)) *
          100
        : null
    return {
      policy,
      window,
      usage: Math.max(requestUse || 0, tokenUse || 0, costUse || 0),
      requestUse,
      tokenUse,
      costUse,
    }
  }),
)

const warningCount = computed(() => policyRows.value.filter((item) => item.usage >= 70).length)
</script>

<template>
  <PageHeader title="配额" description="按租户与模型控制请求量、Token 和预算窗口。">
    <div class="relative">
      <select class="h-8 appearance-none rounded-md border border-border bg-surface pl-3 pr-8 text-[11px]">
        <option>全部租户</option>
      </select>
      <ChevronDown class="pointer-events-none absolute right-2.5 top-1/2 size-3 -translate-y-1/2" />
    </div>
    <Button size="sm"><Plus class="size-3.5" />新建策略</Button>
  </PageHeader>

  <div class="grid grid-cols-4 divide-x divide-border border border-border bg-surface">
    <div class="px-5 py-4">
      <div class="eyebrow">启用策略</div>
      <div class="tabular mt-2 text-xl font-semibold">{{ data.quotas.filter((item) => item.enabled).length }}</div>
      <div class="mt-1 text-[10.5px] text-muted-foreground">共 {{ data.quotas.length }} 条</div>
    </div>
    <div class="px-5 py-4">
      <div class="eyebrow">接近阈值</div>
      <div class="tabular mt-2 text-xl font-semibold" :class="warningCount ? 'text-warning' : ''">{{ warningCount }}</div>
      <div class="mt-1 text-[10.5px] text-muted-foreground">利用率超过 70%</div>
    </div>
    <div class="px-5 py-4">
      <div class="eyebrow">已结算请求</div>
      <div class="tabular mt-2 text-xl font-semibold">
        {{ compactNumber(data.quotaWindows.reduce((sum, item) => sum + item.requests_settled, 0)) }}
      </div>
      <div class="mt-1 text-[10.5px] text-muted-foreground">当前窗口累计</div>
    </div>
    <div class="px-5 py-4">
      <div class="eyebrow">已结算成本</div>
      <div class="tabular mt-2 text-xl font-semibold">
        {{ formatUSD(data.quotaWindows.reduce((sum, item) => sum + Number(item.cost_settled_usd), 0)) }}
      </div>
      <div class="mt-1 text-[10.5px] text-muted-foreground">不含未知价格请求</div>
    </div>
  </div>

  <section class="panel mt-4">
    <div class="panel-header">
      <div>
        <div class="text-[13px] font-semibold">策略与当前窗口</div>
        <div class="mt-0.5 text-[10.5px] text-muted-foreground">预留量与已结算量共同计入配额判断</div>
      </div>
      <div class="flex items-center gap-4 text-[10.5px] text-muted-foreground">
        <span class="flex items-center gap-1.5"><span class="size-2 bg-positive" />0–69%</span>
        <span class="flex items-center gap-1.5"><span class="size-2 bg-warning" />70–89%</span>
        <span class="flex items-center gap-1.5"><span class="size-2 bg-negative" />90%+</span>
      </div>
    </div>

    <EmptyState
      v-if="!policyRows.length"
      title="没有配额策略"
      description="配置 Admin Key 后可读取 /admin/v1/quotas。"
    />
    <div v-else class="divide-y divide-border">
      <article
        v-for="row in policyRows"
        :key="row.policy.id"
        class="grid grid-cols-[minmax(220px,1.1fr)_minmax(300px,1.4fr)_120px_100px] items-center gap-6 px-5 py-4"
      >
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <span class="truncate text-xs font-semibold">
              {{
                data.tenants.find((tenant) => tenant.id === row.policy.tenant_id)?.display_name ||
                row.policy.tenant_id
              }}
            </span>
            <Badge :tone="row.policy.enabled ? 'positive' : 'neutral'">
              {{ row.policy.enabled ? "启用" : "停用" }}
            </Badge>
          </div>
          <div class="mt-1.5 flex items-center gap-2">
            <Badge tone="neutral" class="font-mono">{{ row.policy.gateway_model }}</Badge>
            <span class="text-[10.5px] text-muted-foreground">每 {{ row.policy.period }}</span>
          </div>
        </div>

        <div>
          <div class="mb-2 flex items-center justify-between text-[10.5px]">
            <span class="text-muted-foreground">最高利用率</span>
            <span class="tabular font-semibold">{{ row.usage.toFixed(1) }}%</span>
          </div>
          <div class="h-1.5 overflow-hidden rounded-full bg-surface-muted">
            <div
              class="h-full rounded-full transition-all"
              :class="row.usage >= 90 ? 'bg-negative' : row.usage >= 70 ? 'bg-warning' : 'bg-positive'"
              :style="{ width: `${Math.min(row.usage, 100)}%` }"
            />
          </div>
          <div class="mt-2 flex gap-4 text-[9.5px] text-muted-foreground">
            <span v-if="row.requestUse !== null">请求 {{ row.requestUse.toFixed(1) }}%</span>
            <span v-if="row.tokenUse !== null">Token {{ row.tokenUse.toFixed(1) }}%</span>
            <span v-if="row.costUse !== null">预算 {{ row.costUse.toFixed(1) }}%</span>
          </div>
        </div>

        <div>
          <div class="eyebrow">Overage</div>
          <div class="mt-1.5 flex items-center gap-1.5 text-xs font-semibold">
            <ShieldBan v-if="row.policy.overage_policy === 'deny'" class="size-3.5 text-negative" />
            <BellRing v-else-if="row.policy.overage_policy === 'alert'" class="size-3.5 text-warning" />
            <Gauge v-else class="size-3.5 text-info" />
            {{ row.policy.overage_policy }}
          </div>
        </div>

        <div class="text-right">
          <div class="eyebrow">Version</div>
          <div class="tabular mt-1.5 text-xs font-semibold">v{{ row.policy.version }}</div>
        </div>
      </article>
    </div>
  </section>
</template>
