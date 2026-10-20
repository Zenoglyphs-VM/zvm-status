<script setup lang="ts">
// Response time over the last 24 hours, one series, so no legend: the heading
// names it. A down check has no meaningful latency (it is whatever the timeout
// was), so it is not plotted as a value; it is a red tick on the baseline and
// the line breaks across it.
import {computed, onUnmounted, ref} from 'vue'
import StateChip from './StateChip.vue'
import type {Point} from '../api'

// null while the day is still being fetched.
const props = defineProps<{points: Point[] | null}>()
const pts = computed(() => props.points ?? [])

const W = 720
const H = 120
const PAD = {l: 4, r: 4, t: 10, b: 18}

// The window ends when the points were fetched. A computed of Date.now() alone
// has no dependencies and so never recomputes: the chart's left edge would stay
// where it was when the row was first opened while new points came in.
const t0 = computed(() => {
  void props.points
  return Date.now() / 1000 - 86400
})

const yMax = computed(() => {
  const vals = pts.value.filter((p) => p.status !== 'down').map((p) => p.latency)
  const m = Math.max(50, ...vals)
  // A round ceiling, so the one axis label is a number a person would say.
  const step = m > 1000 ? 500 : m > 200 ? 100 : 50
  return Math.ceil((m * 1.1) / step) * step
})

const x = (t: number) => PAD.l + ((t - t0.value) / 86400) * (W - PAD.l - PAD.r)
const y = (ms: number) => PAD.t + (1 - ms / yMax.value) * (H - PAD.t - PAD.b)

// Break the line at every down check, and wherever checks are missing for more
// than two intervals — a gap drawn as a straight line would claim data it
// does not have.
const segments = computed(() => {
  const out: string[] = []
  let cur: string[] = []
  let prevT = 0
  for (const p of pts.value) {
    if (p.status === 'down' || (prevT && p.t - prevT > 900)) {
      if (cur.length > 1) out.push(cur.join(' '))
      cur = []
    }
    if (p.status !== 'down') cur.push(`${cur.length ? 'L' : 'M'}${x(p.t).toFixed(1)},${y(p.latency).toFixed(1)}`)
    prevT = p.t
  }
  if (cur.length > 1) out.push(cur.join(' '))
  return out
})

const downs = computed(() => pts.value.filter((p) => p.status === 'down'))

const hover = ref<Point | null>(null)
const svg = ref<SVGSVGElement | null>(null)

// A mouse hovers; a finger taps, or drags sideways to scrub (the chart takes
// horizontal movement and leaves vertical to the page, via touch-action). A
// touch reading stays up after the finger lifts, until the next tap elsewhere,
// because a tooltip that vanishes on lift is gone before it can be read.
function onMove(e: PointerEvent) {
  const el = svg.value
  if (!el || !pts.value.length) return
  const r = el.getBoundingClientRect()
  const t = t0.value + ((e.clientX - r.left) / r.width) * 86400
  let best = pts.value[0]
  for (const p of pts.value) if (Math.abs(p.t - t) < Math.abs(best.t - t)) best = p
  hover.value = best
  if (e.pointerType !== 'mouse') document.addEventListener('pointerdown', dismiss, {capture: true})
}
function onLeave(e: PointerEvent) {
  if (e.pointerType === 'mouse') hover.value = null
}
function dismiss(e: PointerEvent) {
  if (svg.value?.contains(e.target as Node)) return
  hover.value = null
  document.removeEventListener('pointerdown', dismiss, {capture: true})
}
onUnmounted(() => document.removeEventListener('pointerdown', dismiss, {capture: true}))

const hoverLeft = computed(() => (hover.value ? `${(x(hover.value.t) / W) * 100}%` : '0'))
const hoverTime = computed(() =>
  hover.value ? new Date(hover.value.t * 1000).toLocaleTimeString(undefined, {hour: '2-digit', minute: '2-digit'}) : '',
)
const median = computed(() => {
  const v = pts.value.filter((p) => p.status !== 'down').map((p) => p.latency).sort((a, b) => a - b)
  return v.length ? v[Math.floor(v.length / 2)] : null
})
</script>

<template>
  <div>
    <div class="flex items-baseline justify-between mb-1.5">
      <span class="ledger">Response time · last 24 h</span>
      <span v-if="median != null" class="text-[12px] text-dim">median <span class="num text-fg">{{ median }} ms</span></span>
    </div>
    <div v-if="!points" class="skeleton h-[120px] !rounded-[4px] mb-[17px]" />
    <div v-else-if="pts.length < 2" class="text-[12.5px] text-dim py-6 text-center">Not enough checks yet to draw a day.</div>
    <div v-else class="relative" @pointerleave="onLeave">
      <svg
        ref="svg"
        :viewBox="`0 0 ${W} ${H}`"
        class="w-full h-[120px] overflow-visible touch-pan-y"
        preserveAspectRatio="none"
        @pointermove="onMove"
        @pointerdown="onMove"
      >
        <!-- Recessive grid: a baseline and one line at the labelled maximum. -->
        <line :x1="PAD.l" :x2="W - PAD.r" :y1="y(0)" :y2="y(0)" stroke="var(--line-strong)" stroke-width="1" vector-effect="non-scaling-stroke" />
        <line :x1="PAD.l" :x2="W - PAD.r" :y1="y(yMax)" :y2="y(yMax)" stroke="var(--line)" stroke-dasharray="3 4" stroke-width="1" vector-effect="non-scaling-stroke" />
        <g class="wipe" style="transform-box: fill-box">
          <path v-for="(d, i) in segments" :key="i" :d="d" fill="none" stroke="var(--ok)" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke" />
        </g>
        <rect v-for="p in downs" :key="p.t" :x="x(p.t) - 2" :y="y(0) - 10" width="4" height="10" rx="1" fill="var(--down)" />
        <line v-if="hover" :x1="x(hover.t)" :x2="x(hover.t)" :y1="PAD.t" :y2="y(0)" stroke="var(--dim)" stroke-width="1" vector-effect="non-scaling-stroke" />
      </svg>
      <span class="absolute left-1 top-0 -translate-y-1/2 text-[10.5px] num text-dim bg-surface px-1">{{ yMax }} ms</span>
      <div class="flex justify-between text-[10.5px] text-dim num mt-0.5"><span>24 h ago</span><span>now</span></div>
      <div
        v-if="hover"
        class="pop absolute top-0 -translate-y-full -translate-x-1/2 px-3 py-2 text-[12px] pointer-events-none whitespace-nowrap z-20"
        :style="{left: `clamp(80px, ${hoverLeft}, calc(100% - 80px))`}"
      >
        <div class="flex items-center gap-2">
          <span class="num">{{ hoverTime }}</span>
          <StateChip :state="hover.status" />
        </div>
        <div v-if="hover.status !== 'down'" class="num mt-1">{{ hover.latency }} ms</div>
      </div>
    </div>
  </div>
</template>
