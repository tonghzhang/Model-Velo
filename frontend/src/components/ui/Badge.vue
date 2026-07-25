<script setup lang="ts">
import { computed } from "vue"
import { cn } from "@/lib/utils"

const props = withDefaults(
  defineProps<{
    tone?: "neutral" | "positive" | "warning" | "negative" | "info" | "accent"
    class?: string
  }>(),
  { tone: "neutral" },
)

const classes = computed(() =>
  cn(
    "inline-flex h-5 min-w-max shrink-0 items-center gap-1 whitespace-nowrap rounded px-1.5 text-[10.5px] font-semibold leading-none",
    {
      "bg-surface-muted text-muted-foreground": props.tone === "neutral",
      "bg-positive-soft text-positive": props.tone === "positive",
      "bg-warning-soft text-warning": props.tone === "warning",
      "bg-negative-soft text-negative": props.tone === "negative",
      "bg-info-soft text-info": props.tone === "info",
      "bg-accent-soft text-accent": props.tone === "accent",
    },
    props.class,
  ),
)
</script>

<template>
  <span :class="classes"><slot /></span>
</template>
