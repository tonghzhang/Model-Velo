export function compactNumber(value: number): string {
  return new Intl.NumberFormat("zh-CN", {
    notation: value >= 10_000 ? "compact" : "standard",
    maximumFractionDigits: 1,
  }).format(value)
}

export function formatNumber(value: number): string {
  return new Intl.NumberFormat("zh-CN").format(value)
}

export function formatUSD(value: string | number): string {
  const number = typeof value === "string" ? Number(value) : value
  if (!Number.isFinite(number)) return "—"
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: number < 1 ? 3 : 2,
    maximumFractionDigits: number < 1 ? 4 : 2,
  }).format(number)
}

export function formatDuration(ms: number): string {
  if (ms < 1_000) return `${Math.round(ms)} ms`
  return `${(ms / 1_000).toFixed(ms >= 10_000 ? 1 : 2)} s`
}

export function formatDateTime(value?: string): string {
  if (!value) return "—"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return "—"
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date)
}

export function relativeTime(value?: string): string {
  if (!value) return "从未"
  const diff = new Date(value).getTime() - Date.now()
  const formatter = new Intl.RelativeTimeFormat("zh-CN", { numeric: "auto" })
  const abs = Math.abs(diff)
  if (abs < 60_000) return formatter.format(Math.round(diff / 1_000), "second")
  if (abs < 3_600_000) return formatter.format(Math.round(diff / 60_000), "minute")
  if (abs < 86_400_000) return formatter.format(Math.round(diff / 3_600_000), "hour")
  return formatter.format(Math.round(diff / 86_400_000), "day")
}

export function shortID(value?: string, head = 8): string {
  if (!value) return "—"
  return value.length > head + 4 ? `${value.slice(0, head)}…${value.slice(-4)}` : value
}
