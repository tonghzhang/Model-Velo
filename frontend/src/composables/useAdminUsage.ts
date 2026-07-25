import { computed, reactive, readonly, shallowRef, watch } from "vue"
import { demoData } from "@/data/demo"
import {
  GatewayAPIError,
  gatewayAPI,
  type AdminUsageQuery,
} from "@/lib/api"
import type {
  AdminUsageSnapshot,
  PlatformUsageRecord,
  Totals,
  UsageGroup,
} from "@/types"
import { useConsole } from "./useConsole"

export interface AdminUsageFilters {
  days: 7 | 14 | 30
  tenantID: string
  apiKeyID: string
  model: string
}

const filters = reactive<AdminUsageFilters>({
  days: 14,
  tenantID: "",
  apiKeyID: "",
  model: "",
})

const demoAssignments = [
  { tenantID: "tenant_acme", apiKeyID: "key_deploy_prod" },
  { tenantID: "tenant_research", apiKeyID: "key_research_jobs" },
  { tenantID: "tenant_platform", apiKeyID: "key_mobile_preview" },
  { tenantID: "tenant_acme", apiKeyID: "key_console_team" },
] as const

const demoTenantWeights = new Map([
  ["tenant_acme", 0.52],
  ["tenant_research", 0.31],
  ["tenant_platform", 0.17],
])

const demoKeyWeights = new Map([
  ["key_deploy_prod", 0.31],
  ["key_console_team", 0.21],
  ["key_research_jobs", 0.31],
  ["key_mobile_preview", 0.17],
])

function scaledTotals(source: Totals, ratio: number): Totals {
  const result = {} as Totals
  for (const [name, value] of Object.entries(source)) {
    if (name === "total_cost_usd") {
      result[name] = (Number(value) * ratio).toFixed(6)
      continue
    }
    if (name === "average_latency_ms" || name === "average_first_token_ms") {
      result[name] = ratio > 0 ? Number(value) : 0
      continue
    }
    result[name as keyof Totals] = Math.round(Number(value) * ratio) as never
  }
  return result
}

function demoPlatformEvents(): PlatformUsageRecord[] {
  return demoData.usage.map((event, index) => {
    const assignment = demoAssignments[index % demoAssignments.length]!
    return {
      ...event,
      tenant_id: assignment.tenantID,
      api_key_id: assignment.apiKeyID,
    }
  })
}

function demoGroups(weights: Map<string, number>): UsageGroup[] {
  return [...weights.entries()].map(([value, ratio]) => ({
    value,
    totals: scaledTotals(demoData.totals, ratio),
  }))
}

function createDemoSnapshot(): AdminUsageSnapshot {
  const keyWeight = filters.apiKeyID ? demoKeyWeights.get(filters.apiKeyID) || 0 : null
  const tenantWeight = filters.tenantID
    ? demoTenantWeights.get(filters.tenantID) || 0
    : null
  const ratio = keyWeight ?? tenantWeight ?? 1
  const events = demoPlatformEvents().filter(
    (event) =>
      (!filters.tenantID || event.tenant_id === filters.tenantID) &&
      (!filters.apiKeyID || event.api_key_id === filters.apiKeyID) &&
      (!filters.model || event.requested_model === filters.model),
  )
  const keyGroups = demoGroups(demoKeyWeights).filter((group) => {
    if (!filters.tenantID) return true
    const key = demoData.keys.find((item) => item.id === group.value)
    return key?.tenant_id === filters.tenantID
  })
  return {
    events,
    totals: scaledTotals(demoData.totals, ratio),
    series: demoData.series
      .slice(filters.days === 7 ? -7 : undefined)
      .map((point) => ({ ...point, totals: scaledTotals(point.totals, ratio) })),
    tenantGroups: demoGroups(demoTenantWeights),
    keyGroups,
  }
}

function createEmptySnapshot(): AdminUsageSnapshot {
  return {
    events: [],
    totals: scaledTotals(demoData.totals, 0),
    series: [],
    tenantGroups: [],
    keyGroups: [],
  }
}

const snapshot = shallowRef<AdminUsageSnapshot>(createDemoSnapshot())
const loading = shallowRef(false)
const error = shallowRef<string | null>(null)
const errorStatus = shallowRef<number | null>(null)
const lastUpdated = shallowRef<Date | null>(new Date())
let activeController: AbortController | null = null
let snapshotMode: "demo" | "real" | "empty" = "demo"

function requestWindow(): AdminUsageQuery {
  const end = new Date()
  const start = new Date(end.getTime() - filters.days * 86_400_000)
  return {
    start: start.toISOString(),
    end: end.toISOString(),
    tenantID: filters.tenantID,
    apiKeyID: filters.apiKeyID,
    model: filters.model,
  }
}

async function refresh() {
  const { settings } = useConsole()
  activeController?.abort()
  const controller = new AbortController()
  activeController = controller
  loading.value = true
  error.value = null
  errorStatus.value = null
  try {
    if (settings.value.demoMode) {
      await new Promise((resolve) => window.setTimeout(resolve, 220))
      snapshot.value = createDemoSnapshot()
      snapshotMode = "demo"
      lastUpdated.value = new Date()
      return
    }
    if (!settings.value.adminKey) {
      snapshot.value = createEmptySnapshot()
      snapshotMode = "empty"
      return
    }
    if (snapshotMode !== "real") snapshot.value = createEmptySnapshot()
    const selected = requestWindow()
    const platform = { start: selected.start, end: selected.end }
    const keyScope = {
      ...platform,
      tenantID: selected.tenantID,
    }
    const [events, summary, series, tenants, keys] = await Promise.all([
      gatewayAPI.adminUsageEvents(settings.value, selected, controller.signal),
      gatewayAPI.adminUsageSummary(settings.value, selected, undefined, controller.signal),
      gatewayAPI.adminUsageSeries(settings.value, selected, controller.signal),
      gatewayAPI.adminUsageSummary(
        settings.value,
        platform,
        "tenant",
        controller.signal,
      ),
      gatewayAPI.adminUsageSummary(
        settings.value,
        keyScope,
        "api_key",
        controller.signal,
      ),
    ])
    controller.signal.throwIfAborted()
    snapshot.value = {
      events: events.data,
      totals: summary.totals,
      series: series.data,
      tenantGroups: tenants.groups || [],
      keyGroups: keys.groups || [],
    }
    snapshotMode = "real"
    lastUpdated.value = new Date()
  } catch (cause) {
    if (controller.signal.aborted) return
    if (cause instanceof GatewayAPIError) {
      errorStatus.value = cause.status
      error.value =
        cause.status === 403 ? "当前管理员角色没有 usage:read 权限" : cause.message
      return
    }
    error.value = cause instanceof Error ? cause.message : "平台用量读取失败"
  } finally {
    if (activeController === controller) loading.value = false
  }
}

const { data, settings } = useConsole()

watch(
  () => filters.tenantID,
  (tenantID) => {
    if (!filters.apiKeyID) return
    const selectedKey = data.value.keys.find((key) => key.id === filters.apiKeyID)
    if (tenantID && selectedKey?.tenant_id !== tenantID) filters.apiKeyID = ""
  },
)

export function useAdminUsage() {
  return {
    filters,
    snapshot: readonly(snapshot),
    loading: readonly(loading),
    error: readonly(error),
    errorStatus: readonly(errorStatus),
    lastUpdated: readonly(lastUpdated),
    configured: computed(() => settings.value.demoMode || Boolean(settings.value.adminKey)),
    refresh,
  }
}
