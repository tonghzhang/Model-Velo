<script setup lang="ts">
import { Crown, DatabaseZap, KeyRound, Network } from "lucide-vue-next"
import type { DeepReadonly } from "vue"
import type { ConsoleData } from "@/types"

defineProps<{
  data: DeepReadonly<ConsoleData>
}>()
</script>

<template>
  <section class="grid grid-cols-4 divide-x divide-border border border-border bg-surface">
    <div class="snapshot-cell">
      <Crown class="size-4 text-[var(--workspace-accent)]" />
      <div>
        <div class="eyebrow">Active owners</div>
        <div class="tabular mt-1 text-xl font-semibold">
          {{ data.principals.filter((item) => item.status === "active" && item.roles.includes("owner")).length }}
        </div>
      </div>
    </div>
    <div class="snapshot-cell">
      <KeyRound class="size-4 text-info" />
      <div>
        <div class="eyebrow">Admin identities</div>
        <div class="tabular mt-1 text-xl font-semibold">{{ data.principals.length }}</div>
      </div>
    </div>
    <div class="snapshot-cell">
      <Network class="size-4 text-positive" />
      <div>
        <div class="eyebrow">Runtime revision</div>
        <div class="tabular mt-1 text-xl font-semibold">r{{ data.runtime?.version || 0 }}</div>
      </div>
    </div>
    <div class="snapshot-cell">
      <DatabaseZap class="size-4 text-accent" />
      <div>
        <div class="eyebrow">Pricing revision</div>
        <div class="tabular mt-1 text-xl font-semibold">v{{ data.pricing?.version || 0 }}</div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.snapshot-cell {
  display: flex;
  min-height: 84px;
  align-items: flex-start;
  gap: 14px;
  padding: 18px 20px;
}
</style>
