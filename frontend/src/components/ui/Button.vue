<script setup lang="ts">
import { computed } from "vue"
import { cn } from "@/lib/utils"

const props = withDefaults(
  defineProps<{
    variant?: "default" | "secondary" | "ghost" | "danger"
    size?: "default" | "sm" | "icon"
    disabled?: boolean
    class?: string
    type?: "button" | "submit" | "reset"
  }>(),
  {
    variant: "default",
    size: "default",
    type: "button",
  },
)

const classes = computed(() =>
  cn(
    "inline-flex select-none items-center justify-center gap-2 whitespace-nowrap rounded-md font-medium transition-colors disabled:pointer-events-none disabled:opacity-45",
    {
      "bg-foreground text-background hover:opacity-88": props.variant === "default",
      "border border-border-strong bg-surface-raised text-foreground hover:bg-surface-muted":
        props.variant === "secondary",
      "text-muted-foreground hover:bg-surface-muted hover:text-foreground": props.variant === "ghost",
      "bg-negative text-white hover:opacity-88": props.variant === "danger",
      "h-9 px-3.5 text-[13px]": props.size === "default",
      "h-8 px-3 text-xs": props.size === "sm",
      "size-8 p-0": props.size === "icon",
    },
    props.class,
  ),
)
</script>

<template>
  <button :type="type" :disabled="disabled" :class="classes">
    <slot />
  </button>
</template>
