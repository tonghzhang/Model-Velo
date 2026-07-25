<script setup lang="ts">
import { Filter, RefreshCw } from "lucide-vue-next"
import { computed } from "vue"
import type { DeepReadonly } from "vue"
import Button from "@/components/ui/Button.vue"
import type { APIKey, Tenant } from "@/types"

const props = defineProps<{
  tenants: readonly DeepReadonly<Tenant>[]
  keys: readonly DeepReadonly<APIKey>[]
  models: readonly string[]
  loading: boolean
}>()

const emit = defineEmits<{
  apply: []
}>()

const days = defineModel<7 | 14 | 30>("days", { required: true })
const tenantID = defineModel<string>("tenant", { required: true })
const apiKeyID = defineModel<string>("apiKey", { required: true })
const model = defineModel<string>("model", { required: true })

const visibleKeys = computed(() =>
  props.keys.filter((key) => !tenantID.value || key.tenant_id === tenantID.value),
)
</script>

<template>
  <div
    class="mb-4 flex flex-wrap items-end gap-3 border border-border bg-surface px-4 py-3"
  >
    <div class="mr-1 flex h-9 items-center gap-2 text-xs font-semibold">
      <Filter class="size-3.5 text-accent" />
      查询范围
    </div>

    <label class="filter-field">
      <span>时间窗口</span>
      <select v-model="days">
        <option :value="7">近 7 天</option>
        <option :value="14">近 14 天</option>
        <option :value="30">近 30 天</option>
      </select>
    </label>

    <label class="filter-field min-w-48">
      <span>租户 / 用户</span>
      <select v-model="tenantID">
        <option value="">全部租户</option>
        <option v-for="tenant in tenants" :key="tenant.id" :value="tenant.id">
          {{ tenant.display_name }}
        </option>
      </select>
    </label>

    <label class="filter-field min-w-44">
      <span>API Key</span>
      <select v-model="apiKeyID">
        <option value="">全部 API Key</option>
        <option v-for="key in visibleKeys" :key="key.id" :value="key.id">
          {{ key.label }}
        </option>
      </select>
    </label>

    <label class="filter-field min-w-44">
      <span>模型</span>
      <select v-model="model">
        <option value="">全部模型</option>
        <option v-for="item in models" :key="item" :value="item">
          {{ item }}
        </option>
      </select>
    </label>

    <Button
      class="ml-auto"
      variant="secondary"
      size="sm"
      :disabled="loading"
      @click="emit('apply')"
    >
      <RefreshCw class="size-3.5" :class="{ 'animate-spin': loading }" />
      应用筛选
    </Button>
  </div>
</template>

<style scoped>
.filter-field {
  display: grid;
  gap: 4px;
}

.filter-field > span {
  color: var(--muted-foreground);
  font-size: 10px;
  font-weight: 650;
}

.filter-field select {
  height: 32px;
  appearance: none;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: var(--surface-raised);
  padding: 0 28px 0 10px;
  color: var(--foreground);
  font-size: 11.5px;
  outline: none;
}

.filter-field select:focus {
  border-color: var(--accent);
}
</style>
