<script setup lang="ts">
import { computed } from "vue"
import { cn } from "@/lib/utils"

const model = defineModel<string | number>()
const props = defineProps<{
  class?: string
  placeholder?: string
  type?: string
  disabled?: boolean
}>()

const classes = computed(() =>
  cn(
    "h-9 w-full rounded-md border border-border-strong bg-surface-raised px-3 text-[13px] text-foreground placeholder:text-quiet-foreground transition-colors hover:border-muted-foreground focus:border-accent disabled:opacity-50",
    props.class,
  ),
)
</script>

<template>
  <input
    v-model="model"
    :class="classes"
    :type="type || 'text'"
    :placeholder="placeholder"
    :disabled="disabled"
  />
</template>
