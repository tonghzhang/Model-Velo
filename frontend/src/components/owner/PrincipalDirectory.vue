<script setup lang="ts">
import { Crown, Search, Shield, UserRoundCheck } from "lucide-vue-next"
import { computed, shallowRef } from "vue"
import type { DeepReadonly } from "vue"
import StatusBadge from "@/components/StatusBadge.vue"
import Badge from "@/components/ui/Badge.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import Input from "@/components/ui/Input.vue"
import { formatDateTime, relativeTime } from "@/lib/format"
import type { Principal } from "@/types"

const props = defineProps<{
  principals: readonly DeepReadonly<Principal>[]
}>()

const search = shallowRef("")
const filtered = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return props.principals.filter(
    (principal) =>
      !needle ||
      principal.name.toLowerCase().includes(needle) ||
      principal.key_prefix.toLowerCase().includes(needle) ||
      principal.roles.some((role) => role.includes(needle)),
  )
})

function roleTone(role: Principal["roles"][number]) {
  if (role === "owner") return "negative" as const
  if (role === "operator") return "accent" as const
  if (role === "billing") return "info" as const
  return "neutral" as const
}
</script>

<template>
  <section class="panel">
    <div class="flex h-14 items-center gap-3 border-b border-border px-3">
      <div class="relative w-80">
        <Search class="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input v-model="search" class="pl-8" placeholder="搜索名称、角色或 Key 前缀" />
      </div>
      <Badge tone="neutral">{{ filtered.length }} identities</Badge>
      <div class="ml-auto flex items-center gap-2 text-[10.5px] text-muted-foreground">
        <UserRoundCheck class="size-3.5 text-positive" />
        最后一个 active Owner 受后端保护，不能被移除
      </div>
    </div>

    <EmptyState
      v-if="!filtered.length"
      title="没有可见的管理员身份"
      description="当前 Admin Key 可能不具备 admin:read 权限。"
    />
    <table v-else class="data-table">
      <thead>
        <tr>
          <th>身份</th>
          <th>角色</th>
          <th>Key 前缀</th>
          <th>状态</th>
          <th>最近使用</th>
          <th>创建时间</th>
          <th>权限级别</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="principal in filtered" :key="principal.id">
          <td>
            <div class="flex items-center gap-2.5">
              <span
                class="grid size-7 place-items-center"
                :class="
                  principal.roles.includes('owner')
                    ? 'bg-negative-soft text-negative'
                    : 'bg-surface-muted text-muted-foreground'
                "
              >
                <Crown v-if="principal.roles.includes('owner')" class="size-3.5" />
                <Shield v-else class="size-3.5" />
              </span>
              <div>
                <div class="text-xs font-semibold">{{ principal.name }}</div>
                <div class="mt-0.5 font-mono text-[9.5px] text-muted-foreground">
                  {{ principal.id }}
                </div>
              </div>
            </div>
          </td>
          <td>
            <div class="flex gap-1">
              <Badge
                v-for="role in principal.roles"
                :key="role"
                :tone="roleTone(role)"
                class="font-mono"
              >
                {{ role }}
              </Badge>
            </div>
          </td>
          <td><Badge tone="neutral" class="font-mono">{{ principal.key_prefix }}</Badge></td>
          <td><StatusBadge :status="principal.status" /></td>
          <td class="text-muted-foreground">{{ relativeTime(principal.last_used_at) }}</td>
          <td class="tabular whitespace-nowrap text-muted-foreground">
            {{ formatDateTime(principal.created_at) }}
          </td>
          <td>
            <span
              class="text-[10.5px] font-semibold"
              :class="principal.roles.includes('owner') ? 'text-negative' : 'text-muted-foreground'"
            >
              {{ principal.roles.includes("owner") ? "全局最高权限" : "受限管理权限" }}
            </span>
          </td>
        </tr>
      </tbody>
    </table>
  </section>
</template>
