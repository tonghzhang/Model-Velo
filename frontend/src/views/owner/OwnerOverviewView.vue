<script setup lang="ts">
import { ShieldAlert } from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import RecentControlChanges from "@/components/owner/RecentControlChanges.vue"
import RolePermissionMatrix from "@/components/owner/RolePermissionMatrix.vue"
import SystemSnapshot from "@/components/owner/SystemSnapshot.vue"
import Badge from "@/components/ui/Badge.vue"
import { useConsole } from "@/composables/useConsole"

const { data } = useConsole()
</script>

<template>
  <PageHeader title="系统概览" description="最高权限身份、全局 Runtime 与价格版本的集中视图。">
    <Badge tone="negative"><ShieldAlert class="size-3" />Owner boundary</Badge>
  </PageHeader>

  <div class="owner-boundary mb-4">
    <div>
      <div class="text-xs font-semibold">最高权限区域</div>
      <div class="mt-1 text-[10.5px] leading-4 text-muted-foreground">
        此处只放置影响整个网关实例的资源。租户运营与日常故障处理仍在“管理工作台”完成。
      </div>
    </div>
    <div class="font-mono text-[9.5px] font-semibold tracking-[0.08em] text-negative uppercase">
      least privilege
    </div>
  </div>

  <SystemSnapshot :data="data" />

  <div class="mt-4 grid grid-cols-[minmax(0,1.25fr)_minmax(420px,0.9fr)] gap-4">
    <RolePermissionMatrix />
    <RecentControlChanges :records="data.audit" />
  </div>
</template>

<style scoped>
.owner-boundary {
  display: flex;
  min-height: 62px;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  border: 1px solid color-mix(in srgb, var(--negative) 28%, var(--border));
  border-left: 4px solid var(--workspace-accent);
  background:
    linear-gradient(90deg, color-mix(in srgb, var(--negative-soft) 65%, transparent), transparent 46%),
    var(--surface);
}
</style>
