import type {
  APIKey,
  AuditRecord,
  ConsoleData,
  ConsoleLoadResult,
  ConsoleSettings,
  DataScope,
  PlatformUsageRecord,
  PricingView,
  Principal,
  QuotaPolicy,
  QuotaWindow,
  RuntimeView,
  SeriesPoint,
  Tenant,
  Totals,
  UsageRecord,
  UsageSummary,
} from "@/types"

interface APIErrorShape {
  error?: {
    code?: string
    message?: string
    request_id?: string
  }
}

export class GatewayAPIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
    readonly requestID?: string,
  ) {
    super(message)
    this.name = "GatewayAPIError"
  }
}

function endpoint(baseUrl: string, path: string): string {
  return `${baseUrl.replace(/\/$/, "")}${path}`
}

async function request<T>(
  settings: ConsoleSettings,
  path: string,
  token?: string,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(endpoint(settings.baseUrl, path), {
    headers: token ? { Authorization: `Bearer ${token}` } : undefined,
    signal,
  })
  if (!response.ok) {
    let payload: APIErrorShape = {}
    try {
      payload = (await response.json()) as APIErrorShape
    } catch {
      // Keep the status-based fallback below.
    }
    throw new GatewayAPIError(
      payload.error?.message || `Gateway request failed (${response.status})`,
      response.status,
      payload.error?.code,
      payload.error?.request_id,
    )
  }
  return (await response.json()) as T
}

function query(params: Record<string, string | number | boolean | undefined>): string {
  const values = new URLSearchParams()
  for (const [name, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") values.set(name, String(value))
  }
  const encoded = values.toString()
  return encoded ? `?${encoded}` : ""
}

export interface AdminUsageQuery {
  start?: string
  end?: string
  tenantID?: string
  apiKeyID?: string
  model?: string
}

export const gatewayAPI = {
  health(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<{ status: string }>(settings, "/healthz", undefined, signal)
  },
  runtime(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<RuntimeView>(settings, "/admin/v1/runtime", settings.adminKey, signal)
  },
  tenants(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<{ items: Tenant[] }>(
      settings,
      "/admin/v1/tenants",
      settings.adminKey,
      signal,
    )
  },
  keys(settings: ConsoleSettings, tenantID: string, signal?: AbortSignal) {
    return request<{ items: APIKey[] }>(
      settings,
      `/admin/v1/tenants/${encodeURIComponent(tenantID)}/keys`,
      settings.adminKey,
      signal,
    )
  },
  quotas(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<{ items: QuotaPolicy[] }>(
      settings,
      "/admin/v1/quotas",
      settings.adminKey,
      signal,
    )
  },
  quotaWindows(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<{ items: QuotaWindow[] }>(
      settings,
      "/admin/v1/quota-windows?limit=500",
      settings.adminKey,
      signal,
    )
  },
  pricing(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<PricingView>(settings, "/admin/v1/pricing", settings.adminKey, signal)
  },
  audit(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<{ items: AuditRecord[] }>(
      settings,
      "/admin/v1/audit?limit=100",
      settings.adminKey,
      signal,
    )
  },
  principals(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<{ items: Principal[] }>(
      settings,
      "/admin/v1/principals",
      settings.adminKey,
      signal,
    )
  },
  adminUsageEvents(
    settings: ConsoleSettings,
    params: AdminUsageQuery,
    signal?: AbortSignal,
  ) {
    return request<{ data: PlatformUsageRecord[]; next_cursor?: string }>(
      settings,
      `/admin/v1/usage/events${query({
        start: params.start,
        end: params.end,
        tenant_id: params.tenantID,
        api_key_id: params.apiKeyID,
        model: params.model,
        limit: 100,
      })}`,
      settings.adminKey,
      signal,
    )
  },
  adminUsageSummary(
    settings: ConsoleSettings,
    params: AdminUsageQuery,
    groupBy?: "tenant" | "api_key",
    signal?: AbortSignal,
  ) {
    return request<UsageSummary>(
      settings,
      `/admin/v1/usage/summary${query({
        start: params.start,
        end: params.end,
        tenant_id: params.tenantID,
        api_key_id: params.apiKeyID,
        model: params.model,
        group_by: groupBy,
      })}`,
      settings.adminKey,
      signal,
    )
  },
  adminUsageSeries(
    settings: ConsoleSettings,
    params: AdminUsageQuery,
    signal?: AbortSignal,
  ) {
    return request<{ data: SeriesPoint[] }>(
      settings,
      `/admin/v1/usage/series${query({
        start: params.start,
        end: params.end,
        tenant_id: params.tenantID,
        api_key_id: params.apiKeyID,
        model: params.model,
        interval: "day",
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
      })}`,
      settings.adminKey,
      signal,
    )
  },
  usageEvents(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<{ data: UsageRecord[]; next_cursor?: string }>(
      settings,
      `/v1/usage/events${query({ limit: 100 })}`,
      settings.gatewayKey,
      signal,
    )
  },
  usageSummary(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<{ totals: Totals }>(
      settings,
      "/v1/usage/summary",
      settings.gatewayKey,
      signal,
    )
  },
  usageSeries(settings: ConsoleSettings, signal?: AbortSignal) {
    const end = new Date()
    const start = new Date(end.getTime() - 14 * 86_400_000)
    return request<{ data: SeriesPoint[] }>(
      settings,
      `/v1/usage/series${query({
        start: start.toISOString(),
        end: end.toISOString(),
        interval: "day",
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
      })}`,
      settings.gatewayKey,
      signal,
    )
  },
  models(settings: ConsoleSettings, signal?: AbortSignal) {
    return request<{ data: { id: string }[] }>(
      settings,
      "/v1/models",
      settings.gatewayKey,
      signal,
    )
  },
}

export async function loadRealConsoleData(
  settings: ConsoleSettings,
  empty: ConsoleData,
  signal?: AbortSignal,
): Promise<ConsoleLoadResult> {
  const next = structuredClone(empty)
  const scopeErrors: Partial<Record<DataScope, string>> = {}

  function capture(scope: DataScope, reason: unknown) {
    if (reason instanceof GatewayAPIError) {
      scopeErrors[scope] =
        reason.status === 403 ? "当前凭据没有该数据域的读取权限" : reason.message
      return
    }
    scopeErrors[scope] = reason instanceof Error ? reason.message : "数据读取失败"
  }

  const health = await gatewayAPI.health(settings, signal)
  next.health = health.status === "ok" ? "healthy" : "degraded"

  if (settings.adminKey) {
    const [runtime, tenants, quotas, windows, pricing, audit, principals] =
      await Promise.allSettled([
        gatewayAPI.runtime(settings, signal),
        gatewayAPI.tenants(settings, signal),
        gatewayAPI.quotas(settings, signal),
        gatewayAPI.quotaWindows(settings, signal),
        gatewayAPI.pricing(settings, signal),
        gatewayAPI.audit(settings, signal),
        gatewayAPI.principals(settings, signal),
      ] as const)
    signal?.throwIfAborted()
    if (runtime.status === "rejected") capture("runtime", runtime.reason)
    if (tenants.status === "rejected") capture("tenants", tenants.reason)
    if (quotas.status === "rejected") capture("quotas", quotas.reason)
    if (windows.status === "rejected") capture("quotaWindows", windows.reason)
    if (pricing.status === "rejected") capture("pricing", pricing.reason)
    if (audit.status === "rejected") capture("audit", audit.reason)
    if (principals.status === "rejected") capture("principals", principals.reason)

    if (runtime.status === "fulfilled") next.runtime = runtime.value
    if (tenants.status === "fulfilled") next.tenants = tenants.value.items
    if (quotas.status === "fulfilled") next.quotas = quotas.value.items
    if (windows.status === "fulfilled") next.quotaWindows = windows.value.items
    if (pricing.status === "fulfilled") next.pricing = pricing.value
    if (audit.status === "fulfilled") next.audit = audit.value.items
    if (principals.status === "fulfilled") next.principals = principals.value.items

    if (next.tenants.length) {
      const keyResponses = await Promise.allSettled(
        next.tenants.map((tenant) => gatewayAPI.keys(settings, tenant.id, signal)),
      )
      signal?.throwIfAborted()
      next.keys = keyResponses.flatMap((response) =>
        response.status === "fulfilled" ? response.value.items : [],
      )
      const rejected = keyResponses.find(
        (response): response is PromiseRejectedResult => response.status === "rejected",
      )
      if (rejected) capture("keys", rejected.reason)
    }
  }

  if (settings.gatewayKey) {
    const [events, summary, series, models] = await Promise.allSettled([
      gatewayAPI.usageEvents(settings, signal),
      gatewayAPI.usageSummary(settings, signal),
      gatewayAPI.usageSeries(settings, signal),
      gatewayAPI.models(settings, signal),
    ] as const)
    signal?.throwIfAborted()
    if (events.status === "rejected") capture("usage", events.reason)
    if (summary.status === "rejected") capture("usage", summary.reason)
    if (series.status === "rejected") capture("usage", series.reason)
    if (models.status === "rejected") capture("models", models.reason)

    if (events.status === "fulfilled") next.usage = events.value.data
    if (summary.status === "fulfilled") next.totals = summary.value.totals
    if (series.status === "fulfilled") next.series = series.value.data
    if (models.status === "fulfilled") {
      next.visibleModels = models.value.data.map((item) => item.id)
    }
  }
  return { data: next, scopeErrors }
}
