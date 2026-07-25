<script setup lang="ts">
import { Boxes, Braces, CheckCircle2, Search, Sparkles } from "lucide-vue-next"
import { computed, shallowRef } from "vue"
import PageHeader from "@/components/PageHeader.vue"
import Badge from "@/components/ui/Badge.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import Input from "@/components/ui/Input.vue"
import { useConsole } from "@/composables/useConsole"

const { data } = useConsole()
const search = shallowRef("")

const models = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return data.value.visibleModels
    .filter((model) => !needle || model.toLowerCase().includes(needle))
    .map((model) => ({
      id: model,
      family: model.split("-").slice(0, 2).join("-"),
      capabilities: model.includes("embedding")
        ? ["embeddings"]
        : model.includes("audio")
          ? ["chat", "audio"]
          : ["chat", "stream"],
    }))
})
</script>

<template>
  <PageHeader title="可用模型" description="仅展示当前 API Key 已获授权的网关模型名称。">
    <Badge tone="info"><CheckCircle2 class="size-3" />后端授权结果</Badge>
  </PageHeader>

  <section class="panel">
    <div class="flex h-14 items-center gap-3 border-b border-border px-3">
      <div class="relative w-80">
        <Search class="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="search" class="pl-8" placeholder="搜索模型名称" />
      </div>
      <Badge tone="neutral">{{ models.length }} models</Badge>
      <div class="ml-auto text-[10.5px] text-muted-foreground">
        Provider 与上游路由属于管理工作台，不在用户门户披露
      </div>
    </div>

    <EmptyState
      v-if="!models.length"
      title="当前 Key 没有可用模型"
      description="请联系租户管理员调整模型授权，或检查 Gateway API Key。"
    />
    <div v-else class="model-catalog">
      <article v-for="model in models" :key="model.id" class="model-row">
        <div class="model-glyph">
          <Braces v-if="model.capabilities.includes('embeddings')" class="size-4" />
          <Sparkles v-else class="size-4" />
        </div>
        <div class="min-w-0 flex-1">
          <div class="font-mono text-[12px] font-semibold">{{ model.id }}</div>
          <div class="mt-1 text-[10.5px] text-muted-foreground">模型族 · {{ model.family }}</div>
        </div>
        <div class="flex gap-1.5">
          <Badge
            v-for="capability in model.capabilities"
            :key="capability"
            :tone="capability === 'stream' ? 'positive' : 'info'"
          >
            {{ capability }}
          </Badge>
        </div>
        <div class="flex items-center gap-1.5 text-[10.5px] font-semibold text-positive">
          <span class="size-1.5 rounded-full bg-positive" />
          可调用
        </div>
      </article>
    </div>
  </section>

  <div class="mt-4 flex items-start gap-3 border border-border bg-surface px-4 py-3">
    <Boxes class="mt-0.5 size-4 text-muted-foreground" />
    <div>
      <div class="text-xs font-semibold">模型 ID 是稳定的网关契约</div>
      <div class="mt-1 text-[10.5px] leading-4 text-muted-foreground">
        调用时使用上方模型 ID；Provider 选择、Retry 和 Fallback 由 Model-Velo 在控制面统一处理。
      </div>
    </div>
  </div>
</template>

<style scoped>
.model-catalog {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.model-row {
  display: grid;
  min-height: 82px;
  grid-template-columns: 34px minmax(0, 1fr) auto 62px;
  align-items: center;
  gap: 14px;
  padding: 13px 16px;
  border-bottom: 1px solid var(--border);
}

.model-row:nth-child(odd) {
  border-right: 1px solid var(--border);
}

.model-row:hover {
  background: color-mix(in srgb, var(--surface-muted) 55%, transparent);
}

.model-glyph {
  display: grid;
  width: 34px;
  height: 34px;
  place-items: center;
  background: var(--info-soft);
  color: var(--info);
}
</style>
