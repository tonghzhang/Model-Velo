<script setup lang="ts">
defineProps<{
  label: string
  value: string
  note?: string
  trend?: string
  tone?: "positive" | "negative" | "neutral"
}>()
</script>

<template>
  <div class="min-w-0 px-5 py-4">
    <div class="eyebrow">{{ label }}</div>
    <div class="mt-2 flex items-baseline gap-2">
      <span class="tabular truncate text-[23px] font-semibold tracking-[-0.035em]">{{ value }}</span>
      <span
        v-if="trend"
        class="tabular text-[11px] font-semibold"
        :class="{
          'text-positive': tone === 'positive',
          'text-negative': tone === 'negative',
          'text-muted-foreground': !tone || tone === 'neutral',
        }"
      >
        {{ trend }}
      </span>
    </div>
    <div v-if="note" class="mt-1 truncate text-[11px] text-muted-foreground">{{ note }}</div>
  </div>
</template>
