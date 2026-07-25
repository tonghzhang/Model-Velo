<script setup lang="ts">
import { computed, ref } from "vue"
import { ArrowRight, Check, GitBranch, Plus, Search, ShieldQuestion } from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import ProviderMark from "@/components/ProviderMark.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import Input from "@/components/ui/Input.vue"
import { useConsole } from "@/composables/useConsole"

const { data } = useConsole()
const search = ref("")

const routes = computed(() =>
  (data.value.runtime?.document.routes || []).filter((route) =>
    route.model.toLowerCase().includes(search.value.trim().toLowerCase()),
  ),
)

function isVisible(model: string) {
  return data.value.visibleModels.includes(model)
}
</script>

<template>
  <PageHeader title="模型与路由" description="网关模型到 Provider 候选链的有序映射。">
    <Button size="sm"><Plus class="size-3.5" />新增路由</Button>
  </PageHeader>

  <section class="panel">
    <div class="flex h-14 items-center gap-3 border-b border-border px-3">
      <div class="relative w-72">
        <Search class="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="search" class="pl-8" placeholder="搜索网关模型" />
      </div>
      <Badge tone="neutral">{{ routes.length }} routes</Badge>
      <div class="ml-auto flex items-center gap-2 text-[10.5px] text-muted-foreground">
        <ShieldQuestion class="size-3.5" />
        “业务可见”由当前业务 API Key 的模型授权决定
      </div>
    </div>

    <EmptyState v-if="!routes.length" title="没有匹配的路由" />
    <div v-else class="divide-y divide-border">
      <article
        v-for="route in routes"
        :key="route.model"
        class="grid grid-cols-[260px_minmax(0,1fr)_120px] items-center gap-5 px-5 py-4 hover:bg-surface-muted/50"
      >
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <span class="truncate font-mono text-[12px] font-semibold">{{ route.model }}</span>
            <Badge v-if="isVisible(route.model)" tone="positive"><Check class="size-2.5" />业务可见</Badge>
            <Badge v-else tone="neutral">未授权</Badge>
          </div>
          <div class="mt-1.5 text-[10.5px] text-muted-foreground">
            {{ route.candidates.length }} 个候选 · 按顺序回退
          </div>
        </div>

        <div class="flex min-w-0 items-center">
          <template v-for="(candidate, index) in route.candidates" :key="`${candidate.provider}-${index}`">
            <div class="min-w-0 border border-border bg-surface-raised px-3 py-2">
              <div class="flex items-center gap-2">
                <span class="grid size-4 place-items-center rounded-sm bg-surface-muted font-mono text-[9px] font-bold">
                  {{ index + 1 }}
                </span>
                <ProviderMark :provider="candidate.provider" />
              </div>
              <div class="mt-1 truncate font-mono text-[9.5px] text-muted-foreground">
                {{ candidate.upstream_model || route.model }}
              </div>
            </div>
            <ArrowRight v-if="index < route.candidates.length - 1" class="mx-2 size-3.5 shrink-0 text-muted-foreground" />
          </template>
        </div>

        <div class="flex justify-end">
          <Badge :tone="route.candidates.length > 1 ? 'accent' : 'neutral'">
            <GitBranch class="size-3" />
            {{ route.candidates.length > 1 ? "可回退" : "单路由" }}
          </Badge>
        </div>
      </article>
    </div>
  </section>
</template>
