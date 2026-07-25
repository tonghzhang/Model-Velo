<script setup lang="ts">
import { computed, ref, watchEffect } from "vue"
import {
  Activity,
  Braces,
  CircuitBoard,
  Clock3,
  ExternalLink,
  KeyRound,
  Plus,
  ServerCog,
  Waypoints,
} from "lucide-vue-next"
import PageHeader from "@/components/PageHeader.vue"
import ProviderMark from "@/components/ProviderMark.vue"
import Badge from "@/components/ui/Badge.vue"
import Button from "@/components/ui/Button.vue"
import EmptyState from "@/components/ui/EmptyState.vue"
import { useConsole } from "@/composables/useConsole"

const { data } = useConsole()
const selectedID = ref("")

watchEffect(() => {
  if (!selectedID.value && data.value.runtime?.document.providers.length) {
    selectedID.value = data.value.runtime.document.providers[0]!.id
  }
})

const selected = computed(() =>
  data.value.runtime?.document.providers.find((provider) => provider.id === selectedID.value),
)

function providerHealth(index: number) {
  return index === 3 ? "attention" : "healthy"
}
</script>

<template>
  <PageHeader title="Providers" description="查看上游协议、模型能力和可靠性运行参数。">
    <Button variant="secondary" size="sm">
      <Braces class="size-3.5" />
      查看 Runtime JSON
    </Button>
    <Button size="sm"><Plus class="size-3.5" />添加 Provider</Button>
  </PageHeader>

  <div
    v-if="data.runtime?.document.providers.length"
    class="grid min-h-[680px] grid-cols-[330px_minmax(0,1fr)] border border-border bg-surface"
  >
    <section class="border-r border-border">
      <div class="flex h-12 items-center justify-between border-b border-border px-4">
        <div class="text-xs font-semibold">已配置 Providers</div>
        <Badge tone="neutral">{{ data.runtime.document.providers.length }}</Badge>
      </div>
      <button
        v-for="(provider, index) in data.runtime.document.providers"
        :key="provider.id"
        class="flex w-full items-start gap-3 border-b border-border px-4 py-4 text-left transition-colors hover:bg-surface-muted"
        :class="{ 'bg-surface-muted': selectedID === provider.id }"
        @click="selectedID = provider.id"
      >
        <div class="mt-0.5 grid size-8 shrink-0 place-items-center rounded-md border border-border bg-surface-raised">
          <ProviderMark :provider="provider.id" compact />
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <span class="truncate text-xs font-semibold">{{ provider.id }}</span>
            <span
              class="size-1.5 rounded-full"
              :class="providerHealth(index) === 'healthy' ? 'bg-positive' : 'bg-warning'"
            />
          </div>
          <div class="mt-1 truncate font-mono text-[10px] text-muted-foreground">
            {{ provider.base_url }}
          </div>
          <div class="mt-2 flex items-center gap-1.5">
            <Badge tone="info">{{ provider.protocol }}</Badge>
            <Badge tone="neutral">{{ provider.models.length }} models</Badge>
            <Badge tone="neutral">{{ provider.keys?.length || 0 }} keys</Badge>
          </div>
        </div>
      </button>
    </section>

    <section v-if="selected" class="min-w-0">
      <div class="flex min-h-[88px] items-center justify-between border-b border-border px-6">
        <div>
          <div class="flex items-center gap-2">
            <ProviderMark :provider="selected.id" />
            <Badge tone="positive"><span class="size-1 rounded-full bg-current" />运行正常</Badge>
          </div>
          <div class="mt-2 flex items-center gap-2 font-mono text-[10.5px] text-muted-foreground">
            {{ selected.base_url }}
            <ExternalLink class="size-3" />
          </div>
        </div>
        <div class="flex gap-2">
          <Button variant="secondary" size="sm">连通性检查</Button>
          <Button size="sm">编辑配置</Button>
        </div>
      </div>

      <div class="grid grid-cols-4 divide-x divide-border border-b border-border">
        <div class="px-5 py-4">
          <div class="eyebrow">Protocol</div>
          <div class="mt-2 font-mono text-sm font-semibold">{{ selected.protocol }}</div>
        </div>
        <div class="px-5 py-4">
          <div class="eyebrow">Models</div>
          <div class="tabular mt-2 text-sm font-semibold">{{ selected.models.length }}</div>
        </div>
        <div class="px-5 py-4">
          <div class="eyebrow">Keys</div>
          <div class="tabular mt-2 text-sm font-semibold">{{ selected.keys?.length || 0 }}</div>
        </div>
        <div class="px-5 py-4">
          <div class="eyebrow">Max in-flight</div>
          <div class="tabular mt-2 text-sm font-semibold">
            {{ selected.runtime.queue.max_in_flight || "默认" }}
          </div>
        </div>
      </div>

      <div class="grid grid-cols-2 gap-4 p-5">
        <div class="border border-border">
          <div class="flex h-11 items-center gap-2 border-b border-border px-4">
            <CircuitBoard class="size-3.5 text-accent" />
            <span class="text-xs font-semibold">Circuit Breaker</span>
          </div>
          <dl class="grid grid-cols-2 gap-y-4 p-4 text-xs">
            <div>
              <dt class="text-muted-foreground">失败阈值</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.breaker.failure_threshold ?? "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">Open 持续时间</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.breaker.open_duration || "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">Half-open 探针</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.breaker.half_open_max_probes ?? "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">当前状态</dt>
              <dd class="mt-1 font-semibold text-positive">Closed</dd>
            </div>
          </dl>
        </div>

        <div class="border border-border">
          <div class="flex h-11 items-center gap-2 border-b border-border px-4">
            <Waypoints class="size-3.5 text-info" />
            <span class="text-xs font-semibold">Bounded Queue</span>
          </div>
          <dl class="grid grid-cols-2 gap-y-4 p-4 text-xs">
            <div>
              <dt class="text-muted-foreground">最大并发</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.queue.max_in_flight ?? "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">最大等待</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.queue.max_waiting ?? "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">等待超时</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.queue.wait_timeout || "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">当前占用</dt>
              <dd class="tabular mt-1 font-semibold text-positive">18 / 96</dd>
            </div>
          </dl>
        </div>

        <div class="border border-border">
          <div class="flex h-11 items-center gap-2 border-b border-border px-4">
            <Clock3 class="size-3.5 text-warning" />
            <span class="text-xs font-semibold">Retry & Timeout</span>
          </div>
          <dl class="grid grid-cols-2 gap-y-4 p-4 text-xs">
            <div>
              <dt class="text-muted-foreground">最大尝试</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.retry.max_attempts ?? "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">Attempt 超时</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.retry.attempt_timeout || "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">请求总超时</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.retry.request_timeout || "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">Backoff</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.retry.initial_backoff || "默认" }}
              </dd>
            </div>
          </dl>
        </div>

        <div class="border border-border">
          <div class="flex h-11 items-center gap-2 border-b border-border px-4">
            <Activity class="size-3.5 text-positive" />
            <span class="text-xs font-semibold">HTTP Pool</span>
          </div>
          <dl class="grid grid-cols-2 gap-y-4 p-4 text-xs">
            <div>
              <dt class="text-muted-foreground">Max idle</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.http.max_idle_connections ?? "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">每主机连接</dt>
              <dd class="tabular mt-1 font-semibold">
                {{ selected.runtime.http.max_connections_per_host ?? "默认" }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">连接复用率</dt>
              <dd class="tabular mt-1 font-semibold text-positive">96.8%</dd>
            </div>
            <div>
              <dt class="text-muted-foreground">TLS 错误</dt>
              <dd class="tabular mt-1 font-semibold">0</dd>
            </div>
          </dl>
        </div>
      </div>

      <div class="mx-5 mb-5 border border-border">
        <div class="flex h-11 items-center justify-between border-b border-border px-4">
          <div class="flex items-center gap-2">
            <ServerCog class="size-3.5" />
            <span class="text-xs font-semibold">模型与能力</span>
          </div>
          <span class="text-[10.5px] text-muted-foreground">由 Runtime 文档声明</span>
        </div>
        <table class="data-table">
          <thead><tr><th>模型</th><th>能力</th><th>路由使用</th></tr></thead>
          <tbody>
            <tr v-for="model in selected.models" :key="model">
              <td><Badge tone="neutral" class="font-mono">{{ model }}</Badge></td>
              <td>
                <div class="flex gap-1">
                  <Badge
                    v-for="capability in selected.model_capabilities?.[model] || ['chat']"
                    :key="capability"
                    tone="info"
                  >
                    {{ capability }}
                  </Badge>
                </div>
              </td>
              <td class="text-muted-foreground">
                {{
                  data.runtime?.document.routes.filter((route) =>
                    route.candidates.some((candidate) => candidate.provider === selected?.id),
                  ).length
                }}
                条网关路由
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
  <section v-else class="panel">
    <EmptyState title="没有托管 Runtime 配置" description="后端返回 runtime_not_managed，当前配置可能来自环境变量。">
      <div class="mt-4"><KeyRound class="size-4 text-muted-foreground" /></div>
    </EmptyState>
  </section>
</template>
