export type ThemeMode = "light" | "dark" | "system"

export interface ConsoleSettings {
  baseUrl: string
  adminKey: string
  gatewayKey: string
  demoMode: boolean
  theme: ThemeMode
}

export interface ProviderRuntime {
  breaker: {
    failure_threshold?: number
    open_duration?: string
    half_open_max_probes?: number
  }
  queue: {
    max_in_flight?: number
    max_waiting?: number
    wait_timeout?: string
  }
  retry: {
    max_attempts?: number
    initial_backoff?: string
    max_backoff?: string
    backoff_multiplier?: number
    jitter_ratio?: number
    request_timeout?: string
    attempt_timeout?: string
  }
  http: {
    max_idle_connections?: number
    max_idle_connections_per_host?: number
    max_connections_per_host?: number
  }
}

export interface ProviderSpec {
  id: string
  protocol: string
  base_url: string
  models: string[]
  model_capabilities?: Record<string, string[]>
  keys?: { id: string; secret?: string }[]
  runtime: ProviderRuntime
}

export interface RouteSpec {
  model: string
  candidates: { provider: string; upstream_model?: string }[]
}

export interface RuntimeView {
  version: number
  document: {
    schema_version: number
    providers: ProviderSpec[]
    routes: RouteSpec[]
  }
}

export interface Tenant {
  id: string
  slug: string
  display_name: string
  status: "active" | "disabled"
  models: string[]
  version: number
  created_at: string
  updated_at: string
}

export interface APIKey {
  id: string
  tenant_id: string
  label: string
  key_prefix: string
  status: "active" | "disabled" | "revoked"
  expires_at?: string
  last_used_at?: string
  revoked_at?: string
  created_at: string
  updated_at: string
}

export interface TokenView {
  input: number
  output: number
  total: number
  input_details?: {
    text?: number
    audio?: number
    image?: number
    cached_read?: number
    cached_write?: number
  }
  output_details?: {
    text?: number
    audio?: number
    reasoning?: number
    accepted_prediction?: number
    rejected_prediction?: number
  }
}

export interface CostView {
  input_nano_usd?: number
  output_nano_usd?: number
  total_nano_usd: number
  total_usd: string
  currency: string
  source: string
  pricing_version?: string
  caveat?: string
}

export type RequestStatus =
  | "success"
  | "cache_hit"
  | "failed"
  | "cancelled"
  | "stream_completed"
  | "stream_interrupted"

export interface UsageRecord {
  schema_version: number
  event_id: string
  request_id: string
  api_key_id?: string
  requested_model: string
  provider_id?: string
  upstream_model?: string
  cache_status: string
  stream: boolean
  attempts: number
  retries: number
  fallbacks: number
  usage?: TokenView
  usage_source: "unknown" | "provider" | "cache_replay"
  usage_caveat?: string
  cost?: CostView
  cost_caveat?: string
  finish_reason?: string
  status: RequestStatus
  error_category?: string
  error_code?: string
  started_at: string
  ended_at: string
  latency_ms: number
  first_token_ms?: number
}

export interface Totals {
  requests: number
  successful_requests: number
  failed_requests: number
  cache_hits: number
  streamed_requests: number
  input_tokens: number
  uncached_input_tokens: number
  input_text_tokens: number
  input_audio_tokens: number
  input_image_tokens: number
  cached_read_tokens: number
  cached_write_tokens: number
  output_tokens: number
  output_text_tokens: number
  output_audio_tokens: number
  reasoning_tokens: number
  accepted_prediction_tokens: number
  rejected_prediction_tokens: number
  total_tokens: number
  billed_tokens: number
  cache_saved_tokens: number
  known_cost_requests: number
  unknown_cost_requests: number
  total_cost_nano_usd: number
  total_cost_usd: string
  average_latency_ms: number
  average_first_token_ms: number
  attempts: number
  retries: number
  fallbacks: number
}

export interface SeriesPoint {
  bucket: string
  totals: Totals
}

export interface QuotaPolicy {
  id: string
  tenant_id: string
  gateway_model: string
  period: "minute" | "hour" | "day" | "month"
  request_limit?: number
  token_limit?: number
  budget_usd?: string
  overage_policy: "deny" | "allow" | "alert"
  enabled: boolean
  version: number
  created_at: string
  updated_at: string
}

export interface QuotaWindow {
  policy_id: string
  tenant_id: string
  gateway_model: string
  period: string
  window_start: string
  requests_settled: number
  requests_reserved: number
  tokens_settled: number
  tokens_reserved: number
  cost_settled_usd: string
  cost_reserved_usd: string
  updated_at: string
}

export interface UsagePrice {
  provider: string
  model: string
  version: string
  effective_from?: string
  effective_until?: string
  input_usd_per_million: string
  output_usd_per_million: string
  cached_read_usd_per_million?: string
  cached_write_usd_per_million?: string
  audio_input_usd_per_million?: string
  audio_output_usd_per_million?: string
  image_input_usd_per_million?: string
  reasoning_output_usd_per_million?: string
}

export interface PricingView {
  version: number
  prices: UsagePrice[]
}

export interface AuditRecord {
  id: number
  principal_id: string
  action: string
  resource_type: string
  resource_id?: string
  request_id: string
  remote_ip?: string
  before?: unknown
  after?: unknown
  outcome: string
  created_at: string
}

export interface Principal {
  id: string
  name: string
  key_prefix: string
  status: "active" | "disabled"
  roles: ("owner" | "operator" | "billing" | "auditor")[]
  last_used_at?: string
  created_at: string
  updated_at: string
}

export interface ConsoleData {
  health: "healthy" | "degraded" | "unknown"
  runtime: RuntimeView | null
  tenants: Tenant[]
  keys: APIKey[]
  usage: UsageRecord[]
  totals: Totals
  series: SeriesPoint[]
  quotas: QuotaPolicy[]
  quotaWindows: QuotaWindow[]
  pricing: PricingView | null
  audit: AuditRecord[]
  principals: Principal[]
  visibleModels: string[]
}
