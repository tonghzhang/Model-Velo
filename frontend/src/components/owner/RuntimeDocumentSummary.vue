<script setup lang="ts">
import { Activity, CircuitBoard, GitBranch, KeyRound, Waypoints } from "lucide-vue-next"
import type { DeepReadonly } from "vue"
import ProviderMark from "@/components/ProviderMark.vue"
import Badge from "@/components/ui/Badge.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import type { RuntimeView } from "@/types"

defineProps<{
  runtime: DeepReadonly<RuntimeView> | null
}>()
</script>

<template>
  <EmptyState
    v-if="!runtime"
    title="没有托管 Runtime 文档"
    description="Runtime 可能来自环境变量，或当前凭据不具备 runtime:read 权限。"
  />
  <template v-else>
    <section class="grid grid-cols-4 divide-x divide-border border border-border bg-surface">
      <div class="runtime-metric">
        <Activity class="size-4 text-positive" />
        <div><div class="eyebrow">Revision</div><div class="tabular mt-1 text-lg font-semibold">r{{ runtime.version }}</div></div>
      </div>
      <div class="runtime-metric">
        <Waypoints class="size-4 text-info" />
        <div><div class="eyebrow">Providers</div><div class="tabular mt-1 text-lg font-semibold">{{ runtime.document.providers.length }}</div></div>
      </div>
      <div class="runtime-metric">
        <GitBranch class="size-4 text-accent" />
        <div><div class="eyebrow">Routes</div><div class="tabular mt-1 text-lg font-semibold">{{ runtime.document.routes.length }}</div></div>
      </div>
      <div class="runtime-metric">
        <KeyRound class="size-4 text-warning" />
        <div>
          <div class="eyebrow">Provider keys</div>
          <div class="tabular mt-1 text-lg font-semibold">
            {{ runtime.document.providers.reduce((sum, provider) => sum + (provider.keys?.length || 0), 0) }}
          </div>
        </div>
      </div>
    </section>

    <section class="panel mt-4">
      <div class="panel-header">
        <div>
          <div class="text-[13px] font-semibold">Provider 运行边界</div>
          <div class="mt-0.5 text-[10.5px] text-muted-foreground">队列、Breaker 与 Retry 的托管配置摘要</div>
        </div>
        <Badge tone="neutral">schema v{{ runtime.document.schema_version }}</Badge>
      </div>
      <table class="data-table">
        <thead>
          <tr>
            <th>Provider</th>
            <th>协议</th>
            <th class="text-right">模型</th>
            <th class="text-right">Keys</th>
            <th class="text-right">最大并发</th>
            <th class="text-right">最大等待</th>
            <th class="text-right">Breaker 阈值</th>
            <th class="text-right">最大尝试</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="provider in runtime.document.providers" :key="provider.id">
            <td><ProviderMark :provider="provider.id" /></td>
            <td class="font-mono text-[10.5px] text-muted-foreground">{{ provider.protocol }}</td>
            <td class="tabular text-right">{{ provider.models.length }}</td>
            <td class="tabular text-right">{{ provider.keys?.length || 0 }}</td>
            <td class="tabular text-right">{{ provider.runtime.queue.max_in_flight ?? "默认" }}</td>
            <td class="tabular text-right">{{ provider.runtime.queue.max_waiting ?? "默认" }}</td>
            <td class="tabular text-right">
              <span class="inline-flex items-center justify-end gap-1.5">
                <CircuitBoard class="size-3 text-warning" />
                {{ provider.runtime.breaker.failure_threshold ?? "默认" }}
              </span>
            </td>
            <td class="tabular text-right">{{ provider.runtime.retry.max_attempts ?? "默认" }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <section class="panel mt-4">
      <div class="panel-header">
        <div>
          <div class="text-[13px] font-semibold">有序路由计划</div>
          <div class="mt-0.5 text-[10.5px] text-muted-foreground">候选顺序决定 Retry 耗尽后的 Fallback 路径</div>
        </div>
      </div>
      <div class="divide-y divide-border">
        <div
          v-for="route in runtime.document.routes"
          :key="route.model"
          class="grid min-h-16 grid-cols-[240px_minmax(0,1fr)] items-center gap-5 px-5 py-3"
        >
          <div class="font-mono text-xs font-semibold">{{ route.model }}</div>
          <div class="flex items-center gap-2">
            <template
              v-for="(candidate, index) in route.candidates"
              :key="`${candidate.provider}-${index}`"
            >
              <div class="flex items-center gap-2 border border-border bg-surface-raised px-3 py-2">
                <span class="grid size-4 place-items-center bg-surface-muted font-mono text-[9px] font-bold">
                  {{ index + 1 }}
                </span>
                <ProviderMark :provider="candidate.provider" />
                <span class="font-mono text-[9.5px] text-muted-foreground">
                  {{ candidate.upstream_model || route.model }}
                </span>
              </div>
              <span v-if="index < route.candidates.length - 1" class="text-muted-foreground">→</span>
            </template>
          </div>
        </div>
      </div>
    </section>
  </template>
</template>

<style scoped>
.runtime-metric {
  display: flex;
  min-height: 76px;
  align-items: flex-start;
  gap: 13px;
  padding: 16px 20px;
}
</style>
