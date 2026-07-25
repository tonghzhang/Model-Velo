<script setup lang="ts">
import { ArrowRight, Boxes, KeyRound, ShieldCheck } from "lucide-vue-next"
import { RouterLink } from "vue-router"
import PageHeader from "@/components/PageHeader.vue"
import PortalUsageSummary from "@/components/portal/PortalUsageSummary.vue"
import RecentUsageTable from "@/components/portal/RecentUsageTable.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import Skeleton from "@/components/ui/Skeleton.vue"
import { useConsole } from "@/composables/useConsole"

const { data, loading, refresh } = useConsole()
</script>

<template>
  <PageHeader title="用量概览" description="查看当前 API Key 的请求、Token、成本与可用模型。">
    <Badge tone="positive"><ShieldCheck class="size-3" />租户隔离</Badge>
    <Button variant="secondary" size="sm" :disabled="loading" @click="refresh">刷新</Button>
  </PageHeader>

  <div class="mb-4 grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-5 border-y border-border bg-surface/50 px-4 py-3">
    <div>
      <div class="text-[11px] font-semibold">当前访问范围</div>
      <div class="mt-0.5 text-[10.5px] text-muted-foreground">
        Usage 与模型列表由后端绑定到当前业务 API Key，不包含其他租户数据。
      </div>
    </div>
    <RouterLink to="/portal/models" class="context-link">
      <Boxes class="size-3.5 text-info" />
      <span><strong>{{ data.visibleModels.length }}</strong> 个模型</span>
      <ArrowRight class="size-3" />
    </RouterLink>
    <RouterLink to="/portal/key" class="context-link">
      <KeyRound class="size-3.5 text-accent" />
      凭据与接入
      <ArrowRight class="size-3" />
    </RouterLink>
  </div>

  <div v-if="loading && !data.totals.requests" class="space-y-4">
    <div class="grid grid-cols-4 border border-border bg-surface">
      <div v-for="index in 4" :key="index" class="border-r border-border px-5 py-5 last:border-r-0">
        <Skeleton class="h-3 w-20" />
        <Skeleton class="mt-3 h-7 w-28" />
        <Skeleton class="mt-2 h-2.5 w-24" />
      </div>
    </div>
    <Skeleton class="h-[330px] w-full" />
  </div>
  <template v-else>
    <PortalUsageSummary :totals="data.totals" :series="data.series" />
    <RecentUsageTable class="mt-4" :records="data.usage" />
  </template>
</template>
