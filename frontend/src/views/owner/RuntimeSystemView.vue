<script setup lang="ts">
import { ServerCog } from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import RuntimeDocumentSummary from "@/components/owner/RuntimeDocumentSummary.vue"
import Badge from "@/components/ui/Badge.vue"
import { useConsole } from "@/composables/useConsole"

const { data } = useConsole()
</script>

<template>
  <PageHeader title="Runtime 配置" description="审阅全局 Provider、可靠性参数与有序路由文档。">
    <Badge tone="accent"><ServerCog class="size-3" />managed document</Badge>
  </PageHeader>

  <RuntimeDocumentSummary :runtime="data.runtime" />
</template>
