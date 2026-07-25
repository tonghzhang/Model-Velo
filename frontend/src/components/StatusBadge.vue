<script setup lang="ts">
import Badge from "@/components/ui/Badge.vue"

const props = defineProps<{ status: string }>()

const labels: Record<string, string> = {
  success: "成功",
  cache_hit: "缓存命中",
  failed: "失败",
  cancelled: "已取消",
  stream_completed: "流完成",
  stream_interrupted: "流中断",
  active: "活跃",
  disabled: "已停用",
  revoked: "已吊销",
  healthy: "健康",
  degraded: "降级",
  unknown: "未知",
}

function tone() {
  if (["success", "cache_hit", "stream_completed", "active", "healthy"].includes(props.status)) {
    return "positive" as const
  }
  if (["failed", "stream_interrupted", "revoked", "degraded"].includes(props.status)) {
    return "negative" as const
  }
  if (["cancelled", "disabled"].includes(props.status)) return "warning" as const
  return "neutral" as const
}
</script>

<template>
  <Badge :tone="tone()">
    <span class="size-1 rounded-full bg-current" />
    {{ labels[status] || status }}
  </Badge>
</template>
