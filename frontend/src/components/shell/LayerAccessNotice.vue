<script setup lang="ts">
import { KeyRound, ShieldAlert } from "lucide-vue-next"
import { computed } from "vue"
import { RouterLink } from "vue-router"
import type { DataScope, WorkspaceLayer } from "@/types"

const props = defineProps<{
  layer: WorkspaceLayer
  demo: boolean
  gatewayKey: string
  adminKey: string
  scopeErrors: Partial<Record<DataScope, string>>
}>()

const notice = computed(() => {
  if (props.demo) return null
  if (props.layer === "portal" && !props.gatewayKey) {
    return {
      tone: "key",
      title: "用户门户尚未连接业务 API Key",
      detail: "填写 Gateway API Key 后，模型与 Usage 会按该 Key 的租户作用域加载。",
    }
  }
  if (props.layer !== "portal" && !props.adminKey) {
    return {
      tone: "key",
      title: props.layer === "owner" ? "Owner 区需要 Admin Bearer Key" : "管理工作台尚未连接 Admin Key",
      detail: "后台会继续由 Go API 校验每个数据域的权限；前端不会提升凭据能力。",
    }
  }
  if (props.layer === "owner" && props.scopeErrors.principals) {
    return {
      tone: "warning",
      title: "当前凭据不能完整读取 Owner 数据域",
      detail: `${props.scopeErrors.principals}。系统配置仍以各 Go API 的权限校验结果为准。`,
    }
  }
  return null
})
</script>

<template>
  <div
    v-if="notice"
    class="mx-6 mt-4 flex items-center gap-3 border px-3.5 py-2.5 text-xs"
    :class="
      notice.tone === 'warning'
        ? 'border-warning/30 bg-warning-soft text-warning'
        : 'border-info/30 bg-info-soft text-info'
    "
  >
    <ShieldAlert v-if="notice.tone === 'warning'" class="size-4 shrink-0" />
    <KeyRound v-else class="size-4 shrink-0" />
    <span class="font-semibold">{{ notice.title }}</span>
    <span class="text-current/80">{{ notice.detail }}</span>
    <RouterLink to="/settings" class="ml-auto shrink-0 font-semibold underline underline-offset-2">
      配置连接
    </RouterLink>
  </div>
</template>
