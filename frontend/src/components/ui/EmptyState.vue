<script setup lang="ts">
import { Inbox } from "lucide-vue-next"

withDefaults(
  defineProps<{
    title?: string
    description?: string
  }>(),
  {
    title: "暂无数据",
    description: "调整筛选条件或稍后重试。",
  },
)
</script>

<template>
  <div class="flex min-h-52 flex-col items-center justify-center px-6 text-center">
    <div class="mb-3 grid size-9 place-items-center rounded-full border border-border bg-surface-muted">
      <Inbox class="size-4 text-muted-foreground" />
    </div>
    <div class="text-sm font-semibold">{{ title }}</div>
    <p class="mt-1 max-w-sm text-xs leading-5 text-muted-foreground">{{ description }}</p>
    <slot />
  </div>
</template>
