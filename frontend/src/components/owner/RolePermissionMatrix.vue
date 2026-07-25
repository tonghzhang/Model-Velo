<script setup lang="ts">
import { Check, Minus } from "lucide-vue-next"

const columns = ["Runtime", "租户", "配额", "定价", "审计", "管理员"]
const roles = [
  {
    role: "owner",
    label: "Owner",
    note: "最高权限与身份治理",
    values: ["RW", "RW", "RW", "RW", "R", "RW"],
  },
  {
    role: "operator",
    label: "Operator",
    note: "网关配置与租户运维",
    values: ["RW", "RW", "RW", "R", "—", "—"],
  },
  {
    role: "billing",
    label: "Billing",
    note: "价格、配额与审计",
    values: ["—", "—", "RW", "RW", "R", "—"],
  },
  {
    role: "auditor",
    label: "Auditor",
    note: "控制面只读审阅",
    values: ["R", "R", "R", "R", "R", "R"],
  },
]
</script>

<template>
  <section class="panel min-w-0">
    <div class="panel-header">
      <div>
        <div class="text-[13px] font-semibold">角色权限矩阵</div>
        <div class="mt-0.5 text-[10.5px] text-muted-foreground">
          与 Go 后端现有 RBAC 一致；前端只负责可见性，接口仍是最终权限边界
        </div>
      </div>
      <div class="font-mono text-[9.5px] text-muted-foreground">R = read · W = write</div>
    </div>
    <div class="overflow-x-auto">
      <table class="data-table permission-table">
        <thead>
          <tr>
            <th>角色</th>
            <th v-for="column in columns" :key="column" class="text-center">{{ column }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in roles" :key="row.role">
            <td>
              <div class="flex items-center gap-3">
                <span class="role-rail" :data-role="row.role" />
                <div>
                  <div class="text-xs font-semibold">{{ row.label }}</div>
                  <div class="mt-0.5 text-[9.5px] text-muted-foreground">{{ row.note }}</div>
                </div>
              </div>
            </td>
            <td v-for="(value, index) in row.values" :key="columns[index]" class="text-center">
              <span v-if="value !== '—'" class="permission-value">
                <Check class="size-3" />
                {{ value }}
              </span>
              <Minus v-else class="mx-auto size-3 text-quiet-foreground" />
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.permission-table td {
  height: 58px;
}

.role-rail {
  width: 3px;
  height: 28px;
  background: #d07a68;
}

.role-rail[data-role="operator"] {
  background: #d29559;
}

.role-rail[data-role="billing"] {
  background: #6b9ca8;
}

.role-rail[data-role="auditor"] {
  background: #8d9488;
}

.permission-value {
  display: inline-flex;
  min-width: 38px;
  align-items: center;
  justify-content: center;
  gap: 3px;
  padding: 3px 6px;
  background: var(--positive-soft);
  color: var(--positive);
  font-family: var(--font-mono);
  font-size: 9.5px;
  font-weight: 700;
}
</style>
