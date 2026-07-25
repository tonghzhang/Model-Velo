import type {
  APIKey,
  AuditRecord,
  ConsoleData,
  ConsoleSettings,
  PricingView,
  Principal,
  QuotaPolicy,
  QuotaWindow,
  RuntimeView,
  SeriesPoint,
  Tenant,
  Totals,
  UsageRecord,
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
): Promise<ConsoleData> {
  const next = structuredClone(empty)
  const health = await gatewayAPI.health(settings, signal)
  next.health = health.status === "ok" ? "healthy" : "degraded"

  if (settings.adminKey) {
    const [runtime, tenants, quotas, windows, pricing, audit, principals] = await Promise.all([
      gatewayAPI.runtime(settings, signal),
      gatewayAPI.tenants(settings, signal),
      gatewayAPI.quotas(settings, signal),
      gatewayAPI.quotaWindows(settings, signal),
      gatewayAPI.pricing(settings, signal),
      gatewayAPI.audit(settings, signal),
      gatewayAPI.principals(settings, signal),
    ])
    next.runtime = runtime
    next.tenants = tenants.items
    next.quotas = quotas.items
    next.quotaWindows = windows.items
    next.pricing = pricing
    next.audit = audit.items
    next.principals = principals.items
    const keyResponses = await Promise.all(
      next.tenants.map((tenant) => gatewayAPI.keys(settings, tenant.id, signal)),
    )
    next.keys = keyResponses.flatMap((response) => response.items)
  }

  if (settings.gatewayKey) {
    const [events, summary, series, models] = await Promise.all([
      gatewayAPI.usageEvents(settings, signal),
      gatewayAPI.usageSummary(settings, signal),
      gatewayAPI.usageSeries(settings, signal),
      gatewayAPI.models(settings, signal),
    ])
    next.usage = events.data
    next.totals = summary.totals
    next.series = series.data
    next.visibleModels = models.data.map((item) => item.id)
  }
  return next
}
