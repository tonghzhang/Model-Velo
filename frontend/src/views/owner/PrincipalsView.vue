<script setup lang="ts">
import { LockKeyhole } from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import PrincipalDirectory from "@/components/owner/PrincipalDirectory.vue"
import Badge from "@/components/ui/Badge.vue"
import { useConsole } from "@/composables/useConsole"

const { data } = useConsole()
</script>

<template>
  <PageHeader title="管理员身份" description="查看 Owner、Operator、Billing 与 Auditor 的授权状态。">
    <Badge tone="negative"><LockKeyhole class="size-3" />owner managed</Badge>
  </PageHeader>

  <PrincipalDirectory :principals="data.principals" />

  <div class="mt-4 border border-border bg-surface px-4 py-3 text-[10.5px] leading-4 text-muted-foreground">
    当前页面保持只读，避免在没有完整确认流程时提供高风险身份变更。现有 Go API 继续负责创建、停用、角色更新与最后一个 Owner 保护。
  </div>
</template>
