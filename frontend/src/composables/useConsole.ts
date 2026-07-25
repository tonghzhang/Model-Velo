import { computed, readonly, ref, watch } from "vue"
import { demoData } from "@/data/demo"
import { loadRealConsoleData } from "@/lib/api"
import type { ConsoleData, ConsoleSettings, ThemeMode, Totals } from "@/types"

const SETTINGS_KEY = "model-velo-console.settings.v1"
const CREDENTIALS_KEY = "model-velo-console.credentials.v1"

const defaultSettings: ConsoleSettings = {
  baseUrl: "/gateway",
  adminKey: "",
  gatewayKey: "",
  demoMode: true,
  theme: "system",
}

const emptyTotals: Totals = Object.fromEntries(
  Object.keys(demoData.totals).map((key) => [key, key === "total_cost_usd" ? "0" : 0]),
) as unknown as Totals

const emptyData: ConsoleData = {
  health: "unknown",
  runtime: null,
  tenants: [],
  keys: [],
  usage: [],
  totals: emptyTotals,
  series: [],
  quotas: [],
  quotaWindows: [],
  pricing: null,
  audit: [],
  principals: [],
  visibleModels: [],
}

function readSettings(): ConsoleSettings {
  try {
    const stored = JSON.parse(localStorage.getItem(SETTINGS_KEY) || "{}") as Partial<ConsoleSettings>
    const credentials = JSON.parse(
      sessionStorage.getItem(CREDENTIALS_KEY) || "{}",
    ) as Pick<ConsoleSettings, "adminKey" | "gatewayKey">
    return { ...defaultSettings, ...stored, ...credentials }
  } catch {
    return { ...defaultSettings }
  }
}

const settings = ref<ConsoleSettings>(readSettings())
const data = ref<ConsoleData>(structuredClone(settings.value.demoMode ? demoData : emptyData))
const loading = ref(false)
const error = ref<string | null>(null)
const lastUpdated = ref<Date | null>(settings.value.demoMode ? new Date() : null)
let activeController: AbortController | null = null

function resolveTheme(mode: ThemeMode): "light" | "dark" {
  if (mode === "system") {
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"
  }
  return mode
}

function applyTheme() {
  document.documentElement.classList.toggle("dark", resolveTheme(settings.value.theme) === "dark")
}

applyTheme()
window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", applyTheme)

watch(
  settings,
  () => {
    const { adminKey, gatewayKey, ...persisted } = settings.value
    localStorage.setItem(SETTINGS_KEY, JSON.stringify(persisted))
    sessionStorage.setItem(CREDENTIALS_KEY, JSON.stringify({ adminKey, gatewayKey }))
    applyTheme()
  },
  { deep: true },
)

async function refresh() {
  activeController?.abort()
  const controller = new AbortController()
  activeController = controller
  loading.value = true
  error.value = null
  try {
    if (settings.value.demoMode) {
      await new Promise((resolve) => window.setTimeout(resolve, 280))
      data.value = structuredClone(demoData)
    } else {
      data.value = await loadRealConsoleData(
        settings.value,
        structuredClone(emptyData),
        controller.signal,
      )
    }
    lastUpdated.value = new Date()
  } catch (cause) {
    if (controller.signal.aborted) return
    error.value = cause instanceof Error ? cause.message : "无法读取网关数据"
  } finally {
    if (activeController === controller) loading.value = false
  }
}

function updateSettings(next: ConsoleSettings) {
  settings.value = { ...next }
  void refresh()
}

export function useConsole() {
  return {
    settings,
    data: readonly(data),
    loading: readonly(loading),
    error: readonly(error),
    lastUpdated: readonly(lastUpdated),
    isDemo: computed(() => settings.value.demoMode),
    refresh,
    updateSettings,
  }
}
