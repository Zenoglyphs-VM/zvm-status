<script setup lang="ts">
import {ref, watch} from 'vue'
import Icon from './Icon.vue'
import StateChip from './StateChip.vue'
import UptimeBars from './UptimeBars.vue'
import LatencyChart from './LatencyChart.vue'
import Elapsed from './Elapsed.vue'
import {fetchRecent, pct, type Component, type Point} from '../api'

const props = defineProps<{c: Component; interval: number; days: number; delay: number}>()

const open = ref(false)

// Flash the row once when its state changes while the page is open — the same
// pink landing a new block gets in the explorer.
const changed = ref(false)
watch(
  () => props.c.status,
  (s, prev) => {
    if (!prev || s === prev) return
    changed.value = false
    requestAnimationFrame(() => (changed.value = true))
  },
)

const latencyKey = ref(0)
watch(
  () => props.c.latency,
  () => latencyKey.value++,
)

// The day of checks behind the chart is not in the summary: it is fetched when
// the row is opened, and again when a round lands while it is open.
const recent = ref<Point[] | null>(null)
const recentError = ref('')
async function loadRecent() {
  try {
    recent.value = await fetchRecent(props.c.id)
    recentError.value = ''
  } catch (err: any) {
    recentError.value = err?.name === 'TimeoutError' ? 'the status API did not answer' : String(err?.message ?? err)
  }
}
watch(
  () => [open.value, props.c.checkedAt] as const,
  ([isOpen]) => isOpen && loadRecent(),
)
</script>

<template>
  <div class="px-4 py-3.5 border-b border-line last:border-b-0 rail-in" :class="changed ? 'just-changed' : ''" :style="{animationDelay: `${delay}ms`}">
    <button class="row-head w-full grid grid-cols-[auto_minmax(0,1fr)_auto_auto] items-center gap-x-3 text-left group" :aria-expanded="open" @click="open = !open">
      <span class="relative flex size-2.5 shrink-0" :class="`s-${c.status}`">
        <span v-if="c.status !== 'operational' && c.status !== 'unknown'" class="ping absolute inset-0 rounded-full" style="background: var(--s)" />
        <span class="relative size-2.5 rounded-full" :class="c.status === 'operational' ? 'live-dot' : ''" style="background: var(--s)" />
      </span>
      <span class="min-w-0 flex items-center gap-3">
        <span class="min-w-0 grow">
          <span class="font-medium text-[14.5px]">{{ c.name }}</span>
          <span class="hidden sm:block text-[12.5px] text-dim truncate">{{ c.description }}</span>
        </span>
        <span class="hidden sm:block shrink-0 text-right text-[12px] text-dim">
          <span v-if="c.checkedAt" :key="latencyKey" class="num tick">{{ c.latency }} ms</span>
          <Elapsed v-if="c.checkedAt" :t="c.checkedAt" class="block" />
        </span>
      </span>
      <StateChip :state="c.status" />
      <Icon name="down" :size="15" class="text-dim transition-transform duration-200 group-hover:text-fg" :class="open ? 'rotate-180' : ''" />
      <!-- A phone has no room beside the name for the description, which was cut
           to fifteen characters there, or for the latency, which was dropped.
           Both go on the lines under it instead, at full width. -->
      <span class="sm:hidden col-start-2 col-span-3 text-[12.5px] text-dim leading-snug mt-0.5">{{ c.description }}</span>
      <span v-if="c.checkedAt" class="sm:hidden col-start-2 col-span-3 text-[12px] text-dim mt-0.5">
        <span :key="latencyKey" class="num tick">{{ c.latency }} ms</span> · checked <Elapsed :t="c.checkedAt" />
      </span>
    </button>

    <p v-if="c.status !== 'operational' && c.detail" class="mt-2 ml-5.5 text-[12.5px] mono break-words" :class="`s-${c.status}`" style="color: var(--s)">
      {{ c.detail }}
    </p>

    <div class="mt-3">
      <UptimeBars :days="c.days" :interval="interval" :delay="delay" />
      <div class="flex justify-between items-center mt-1.5 text-[11.5px] text-dim">
        <span class="shrink-0">{{ days }} days ago</span>
        <span class="h-px grow min-w-3 mx-3 bg-line" />
        <span class="shrink-0"><span class="num text-soft">{{ pct(c.uptime) }}</span> uptime</span>
        <span class="h-px grow min-w-3 mx-3 bg-line" />
        <span class="shrink-0">Today</span>
      </div>
    </div>

    <div v-if="open" class="mt-4 card p-3 rail-in">
      <p v-if="recentError && !recent" class="text-[12.5px] py-6 text-center" style="color: var(--danger)">Could not load the last day: {{ recentError }}.</p>
      <LatencyChart v-else :points="recent" />
      <p v-if="c.status === 'operational' && c.detail" class="mt-2 text-[12px] text-dim">Last check: <span class="mono break-words">{{ c.detail }}</span></p>
    </div>
  </div>
</template>
