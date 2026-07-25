<script setup lang="ts">
import { computed, ref } from "vue"
import {
  ChevronDown,
  Copy,
  Filter,
  Radio,
  Search,
  X,
} from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import ProviderMark from "@/components/ProviderMark.vue"
import StatusBadge from "@/components/StatusBadge.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import Input from "@/components/ui/Input.vue"
import Skeleton from "@/components/ui/Skeleton.vue"
import { useConsole } from "@/composables/useConsole"
import {
  compactNumber,
  formatDateTime,
  formatDuration,
  formatUSD,
  shortID,
} from "@/lib/format"
import type { UsageRecord } from "@/types"

const { data, loading } = useConsole()
const search = ref("")
const status = ref("")
const provider = ref("")
const selected = ref<UsageRecord | null>(null)

const providers = computed(() =>
  [...new Set(data.value.usage.map((item) => item.provider_id).filter(Boolean))] as string[],
)

const filtered = computed(() =>
  data.value.usage.filter((item) => {
    const needle = search.value.trim().toLowerCase()
    const matchesSearch =
      !needle ||
      item.request_id.toLowerCase().includes(needle) ||
      item.requested_model.toLowerCase().includes(needle) ||
      item.event_id.toLowerCase().includes(needle)
    return (
      matchesSearch &&
      (!status.value || item.status === status.value) &&
      (!provider.value || item.provider_id === provider.value)
    )
  }),
)
</script>

<template>
  <PageHeader title="请求流量" description="检索 Usage 事件，定位失败、回退与异常延迟。">
    <Button variant="secondary" size="sm">
      <Radio class="size-3.5 text-positive" />
      实时追踪
    </Button>
  </PageHeader>

  <section class="panel">
    <div class="flex min-h-14 items-center gap-2 border-b border-border px-3">
      <div class="relative w-72">
        <Search class="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="search" class="pl-8" placeholder="请求 ID、事件 ID 或模型" />
      </div>
      <div class="relative">
        <select
          v-model="status"
          class="h-9 appearance-none rounded-md border border-border-strong bg-surface-raised pl-3 pr-8 text-[12px]"
        >
          <option value="">全部状态</option>
          <option value="success">成功</option>
          <option value="cache_hit">缓存命中</option>
          <option value="stream_completed">流完成</option>
          <option value="failed">失败</option>
          <option value="cancelled">已取消</option>
          <option value="stream_interrupted">流中断</option>
        </select>
        <ChevronDown class="pointer-events-none absolute right-2.5 top-1/2 size-3 -translate-y-1/2" />
      </div>
      <div class="relative">
        <select
          v-model="provider"
          class="h-9 appearance-none rounded-md border border-border-strong bg-surface-raised pl-3 pr-8 text-[12px]"
        >
          <option value="">全部 Provider</option>
          <option v-for="item in providers" :key="item" :value="item">{{ item }}</option>
        </select>
        <ChevronDown class="pointer-events-none absolute right-2.5 top-1/2 size-3 -translate-y-1/2" />
      </div>
      <Button variant="ghost" size="sm">
        <Filter class="size-3.5" />
        更多筛选
      </Button>
      <div class="ml-auto text-[11px] text-muted-foreground">
        当前显示 <span class="tabular font-semibold text-foreground">{{ filtered.length }}</span> 个事件
      </div>
    </div>

    <div v-if="loading" class="space-y-px">
      <div v-for="index in 8" :key="index" class="flex h-12 items-center gap-6 px-4">
        <Skeleton class="h-5 w-16" />
        <Skeleton class="h-3 w-28" />
        <Skeleton class="h-5 w-28" />
        <Skeleton class="h-3 w-24" />
        <Skeleton class="ml-auto h-3 w-16" />
      </div>
    </div>
    <EmptyState
      v-else-if="!filtered.length"
      title="没有匹配的 Usage 事件"
      description="当前 API Key 作用域内没有符合筛选条件的请求。"
    />
    <div v-else class="overflow-x-auto">
      <table class="data-table">
        <thead>
          <tr>
            <th>状态</th>
            <th>开始时间</th>
            <th>请求 ID</th>
            <th>模型</th>
            <th>Provider</th>
            <th>缓存 / 流式</th>
            <th class="text-right">Token</th>
            <th class="text-right">成本</th>
            <th class="text-right">延迟</th>
            <th class="text-right">尝试</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="record in filtered"
            :key="record.event_id"
            class="cursor-pointer"
            @click="selected = record"
          >
            <td><StatusBadge :status="record.status" /></td>
            <td class="tabular whitespace-nowrap text-muted-foreground">
              {{ formatDateTime(record.started_at) }}
            </td>
            <td class="font-mono text-[11px]">{{ shortID(record.request_id) }}</td>
            <td>
              <Badge tone="neutral" class="font-mono font-medium">{{ record.requested_model }}</Badge>
            </td>
            <td><ProviderMark :provider="record.provider_id || 'unknown'" /></td>
            <td>
              <div class="flex items-center gap-1">
                <Badge :tone="record.cache_status === 'hit' ? 'positive' : 'neutral'">
                  {{ record.cache_status }}
                </Badge>
                <Badge v-if="record.stream" tone="info">SSE</Badge>
              </div>
            </td>
            <td class="tabular text-right">{{ compactNumber(record.usage?.total || 0) }}</td>
            <td class="tabular text-right">{{ record.cost ? formatUSD(record.cost.total_usd) : "—" }}</td>
            <td class="tabular text-right">{{ formatDuration(record.latency_ms) }}</td>
            <td class="tabular text-right">
              {{ record.attempts }}
              <span v-if="record.retries" class="ml-1 text-warning">+{{ record.retries }}R</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="flex h-12 items-center justify-between border-t border-border px-4 text-[11px] text-muted-foreground">
      <span>Usage 查询由后端强制限制在当前业务 API Key</span>
      <Button variant="secondary" size="sm" :disabled="filtered.length < 100">加载更多</Button>
    </div>
  </section>

  <Teleport to="body">
    <Transition name="fade">
      <div v-if="selected" class="fixed inset-0 z-50 flex justify-end bg-black/35" @click.self="selected = null">
        <aside class="h-full w-[470px] overflow-y-auto border-l border-border bg-surface-raised shadow-2xl">
          <div class="sticky top-0 z-10 flex h-14 items-center justify-between border-b border-border bg-surface-raised/95 px-5 backdrop-blur">
            <div>
              <div class="text-[13px] font-semibold">请求详情</div>
              <div class="mt-0.5 font-mono text-[10px] text-muted-foreground">{{ selected.event_id }}</div>
            </div>
            <Button variant="ghost" size="icon" @click="selected = null"><X class="size-4" /></Button>
          </div>

          <div class="p-5">
            <div class="flex items-center justify-between border-b border-border pb-5">
              <div>
                <StatusBadge :status="selected.status" />
                <div class="mt-3 font-mono text-base font-semibold">{{ selected.requested_model }}</div>
              </div>
              <div class="text-right">
                <div class="tabular text-xl font-semibold">{{ formatDuration(selected.latency_ms) }}</div>
                <div class="mt-1 text-[10px] text-muted-foreground">端到端延迟</div>
              </div>
            </div>

            <div class="grid grid-cols-2 gap-x-8 gap-y-4 border-b border-border py-5">
              <div>
                <div class="eyebrow">Provider</div>
                <div class="mt-1.5 text-xs"><ProviderMark :provider="selected.provider_id || 'unknown'" /></div>
              </div>
              <div>
                <div class="eyebrow">Upstream model</div>
                <div class="mt-1.5 font-mono text-[11px]">{{ selected.upstream_model || "—" }}</div>
              </div>
              <div>
                <div class="eyebrow">请求 ID</div>
                <div class="mt-1.5 flex items-center gap-1.5 font-mono text-[10.5px]">
                  {{ shortID(selected.request_id, 14) }} <Copy class="size-3 text-muted-foreground" />
                </div>
              </div>
              <div>
                <div class="eyebrow">API Key ID</div>
                <div class="mt-1.5 font-mono text-[10.5px]">{{ shortID(selected.api_key_id, 14) }}</div>
              </div>
              <div>
                <div class="eyebrow">开始</div>
                <div class="tabular mt-1.5 text-[11px]">{{ formatDateTime(selected.started_at) }}</div>
              </div>
              <div>
                <div class="eyebrow">首 Token</div>
                <div class="tabular mt-1.5 text-[11px]">
                  {{ selected.first_token_ms ? formatDuration(selected.first_token_ms) : "非流式" }}
                </div>
              </div>
            </div>

            <div class="border-b border-border py-5">
              <div class="mb-3 text-xs font-semibold">Token 与成本</div>
              <div class="grid grid-cols-4 divide-x divide-border border border-border bg-surface">
                <div class="p-3">
                  <div class="eyebrow">Input</div>
                  <div class="tabular mt-1.5 font-semibold">{{ compactNumber(selected.usage?.input || 0) }}</div>
                </div>
                <div class="p-3">
                  <div class="eyebrow">Output</div>
                  <div class="tabular mt-1.5 font-semibold">{{ compactNumber(selected.usage?.output || 0) }}</div>
                </div>
                <div class="p-3">
                  <div class="eyebrow">Total</div>
                  <div class="tabular mt-1.5 font-semibold">{{ compactNumber(selected.usage?.total || 0) }}</div>
                </div>
                <div class="p-3">
                  <div class="eyebrow">Cost</div>
                  <div class="tabular mt-1.5 font-semibold">
                    {{ selected.cost ? formatUSD(selected.cost.total_usd) : "未知" }}
                  </div>
                </div>
              </div>
            </div>

            <div class="py-5">
              <div class="mb-3 text-xs font-semibold">可靠性路径</div>
              <div class="space-y-2">
                <div class="flex items-center justify-between bg-surface px-3 py-2.5 text-xs">
                  <span class="text-muted-foreground">尝试次数</span><strong>{{ selected.attempts }}</strong>
                </div>
                <div class="flex items-center justify-between bg-surface px-3 py-2.5 text-xs">
                  <span class="text-muted-foreground">Retry / Fallback</span>
                  <strong>{{ selected.retries }} / {{ selected.fallbacks }}</strong>
                </div>
                <div class="flex items-center justify-between bg-surface px-3 py-2.5 text-xs">
                  <span class="text-muted-foreground">缓存状态</span>
                  <Badge :tone="selected.cache_status === 'hit' ? 'positive' : 'neutral'">
                    {{ selected.cache_status }}
                  </Badge>
                </div>
              </div>
            </div>
          </div>
        </aside>
      </div>
    </Transition>
  </Teleport>
</template>
