<script setup lang="ts">
import { computed } from "vue"

const props = defineProps<{
  provider: string
  compact?: boolean
}>()

const colors = [
  "#d58a45",
  "#5aa58c",
  "#6c91b2",
  "#b76f79",
  "#8b79ae",
  "#9c994b",
]

const color = computed(() => {
  let hash = 0
  for (let index = 0; index < props.provider.length; index += 1) {
    hash = (hash * 31 + props.provider.charCodeAt(index)) | 0
  }
  return colors[Math.abs(hash) % colors.length]
})
</script>

<template>
  <span class="inline-flex min-w-0 items-center gap-2">
    <span
      class="size-2 shrink-0 rounded-[2px]"
      :style="{ backgroundColor: color }"
      aria-hidden="true"
    />
    <span v-if="!compact" class="truncate font-medium">{{ provider || "unknown" }}</span>
  </span>
</template>
