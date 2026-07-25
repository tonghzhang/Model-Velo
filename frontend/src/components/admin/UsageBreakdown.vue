<script setup lang="ts">
import { computed } from "vue"
import type { DeepReadonly } from "vue"
import Badge from "@/components/ui/Badge.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import { compactNumber, formatUSD, shortID } from "@/lib/format"
import type { APIKey, Tenant, UsageGroup } from "@/types"

const props = defineProps<{
  tenantGroups: readonly DeepReadonly<UsageGroup>[]
  keyGroups: readonly DeepReadonly<UsageGroup>[]
  tenants: readonly DeepReadonly<Tenant>[]
  keys: readonly DeepReadonly<APIKey>[]
  selectedTenant: string
}>()

const emit = defineEmits<{
  selectTenant: [tenantID: string]
}>()

const tenantByID = computed(
  () => new Map(props.tenants.map((tenant) => [tenant.id, tenant])),
)
const keyByID = computed(() => new Map(props.keys.map((key) => [key.id, key])))
const maxTenantRequests = computed(() =>
  Math.max(1, ...props.tenantGroups.map((group) => group.totals.requests)),
)
</script>

<template>
  <section class="panel">
    <div class="panel-header">
      <div>
        <div class="text-[13px] font-semibold">租户与 API Key 分布</div>
        <div class="mt-0.5 text-[10.5px] text-muted-foreground">
          定位流量来源；点击租户可下钻
        </div>
      </div>
      <Badge tone="neutral">{{ tenantGroups.length }} tenants</Badge>
    </div>

    <div v-if="tenantGroups.length" class="divide-y divide-border">
      <button
        v-for="group in tenantGroups.slice(0, 6)"
        :key="group.value"
        class="grid w-full grid-cols-[minmax(0,1fr)_68px_76px] items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-surface-muted"
        :class="{ 'bg-accent-soft': selectedTenant === group.value }"
        @click="emit('selectTenant', group.value)"
      >
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <span class="tenant-mark">T</span>
            <span class="truncate text-[11.5px] font-semibold">
              {{ tenantByID.get(group.value)?.display_name || group.value }}
            </span>
          </div>
          <div class="mt-2 h-1 bg-surface-muted">
            <div
              class="h-full bg-accent"
              :style="{
                width: `${Math.max(3, (group.totals.requests / maxTenantRequests) * 100)}%`,
              }"
            />
          </div>
        </div>
        <div class="text-right">
          <div class="tabular text-[11px] font-semibold">
            {{ compactNumber(group.totals.requests) }}
          </div>
          <div class="mt-0.5 text-[9.5px] text-muted-foreground">requests</div>
        </div>
        <div class="tabular text-right text-[11px] text-muted-foreground">
          {{ formatUSD(group.totals.total_cost_usd) }}
        </div>
      </button>
    </div>
    <EmptyState
      v-else
      title="没有租户用量"
      description="当前时间窗口内尚未形成 Usage 事件。"
    />

    <div class="border-t border-border bg-surface-muted/45 px-4 py-2">
      <span class="text-[9.5px] font-semibold tracking-[0.09em] text-muted-foreground uppercase">
        {{ selectedTenant ? "Selected tenant · API keys" : "Top API keys" }}
      </span>
    </div>
    <div v-if="keyGroups.length" class="divide-y divide-border">
      <div
        v-for="group in keyGroups.slice(0, 5)"
        :key="group.value"
        class="grid grid-cols-[minmax(0,1fr)_70px_72px] items-center gap-3 px-4 py-2.5"
      >
        <div class="min-w-0">
          <div class="truncate font-mono text-[10.5px] font-semibold">
            {{ keyByID.get(group.value)?.label || shortID(group.value, 10) }}
          </div>
          <div class="mt-0.5 truncate text-[9.5px] text-muted-foreground">
            {{ keyByID.get(group.value)?.key_prefix || shortID(group.value) }}
          </div>
        </div>
        <span class="tabular text-right text-[10.5px]">
          {{ compactNumber(group.totals.requests) }}
        </span>
        <span class="tabular text-right text-[10.5px] text-muted-foreground">
          {{ compactNumber(group.totals.total_tokens) }} tok
        </span>
      </div>
    </div>
    <div v-else class="px-4 py-5 text-center text-[10.5px] text-muted-foreground">
      当前范围没有 API Key 分组数据
    </div>
  </section>
</template>

<style scoped>
.tenant-mark {
  display: grid;
  width: 20px;
  height: 20px;
  flex: 0 0 auto;
  place-items: center;
  border: 1px solid color-mix(in srgb, var(--accent) 34%, var(--border));
  background: var(--accent-soft);
  color: var(--accent);
  font-family: var(--font-mono);
  font-size: 9px;
  font-weight: 800;
}
</style>
