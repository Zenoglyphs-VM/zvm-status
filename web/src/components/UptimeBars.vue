<script setup lang="ts">
// Thirty days, one bar each, oldest on the left. A bar's colour is the day's
// service level (dayState: under 100% is degraded, under 95% down); its
// tooltip says what happened and for how long.
import {computed, onUnmounted, ref} from 'vue'
import StateChip from './StateChip.vue'
import {dayState, pct, span, type Day} from '../api'

const props = defineProps<{days: Day[]; interval: number; delay?: number}>()

const hover = ref<number | null>(null)
const tip = computed(() => (hover.value == null ? null : props.days[hover.value]))

function dateLabel(d: string) {
  return new Date(`${d}T00:00:00Z`).toLocaleDateString(undefined, {month: 'short', day: 'numeric', timeZone: 'UTC'})
}

// The bar under the pointer is worked out from x across the whole strip, not
// from each bar's own enter event: that way a finger can tap a bar, or drag
// along the strip to read one day after another, and the 2px gaps between bars
// are not dead spots. Vertical drags still scroll the page (touch-action).
// A touch reading stays until the next tap elsewhere, since there is no hover
// to end it.
const strip = ref<HTMLElement | null>(null)
function onPoint(e: PointerEvent) {
  const el = strip.value
  if (!el || !props.days.length) return
  const r = el.getBoundingClientRect()
  const i = Math.floor(((e.clientX - r.left) / r.width) * props.days.length)
  hover.value = Math.min(props.days.length - 1, Math.max(0, i))
  if (e.pointerType !== 'mouse') document.addEventListener('pointerdown', dismiss, {capture: true})
}
function onLeave(e: PointerEvent) {
  if (e.pointerType === 'mouse') hover.value = null
}
function dismiss(e: PointerEvent) {
  if (strip.value?.contains(e.target as Node)) return
  hover.value = null
  document.removeEventListener('pointerdown', dismiss, {capture: true})
}
onUnmounted(() => document.removeEventListener('pointerdown', dismiss, {capture: true}))

// Left edge of the tooltip, kept inside the strip so the end bars' tips do not
// hang off the page on a phone.
const tipLeft = computed(() => {
  if (hover.value == null) return '0%'
  const x = ((hover.value + 0.5) / props.days.length) * 100
  return `clamp(0px, calc(${x}% - 110px), calc(100% - 220px))`
})
</script>

<template>
  <div class="relative">
    <div
      ref="strip"
      class="flex items-end gap-[2px] h-9 touch-pan-y cursor-default"
      role="img"
      :aria-label="`Daily status for the last ${days.length} days`"
      @pointermove="onPoint"
      @pointerdown="onPoint"
      @pointerleave="onLeave"
    >
      <div v-for="(d, i) in days" :key="d.date" class="flex-1 h-full flex items-end">
        <div
          class="w-full rounded-[3px] bar-rise transition-[filter,opacity] duration-150"
          :class="[`fill-${dayState(d)}`, hover != null && hover !== i ? 'opacity-55' : '']"
          :style="{height: '100%', animationDelay: `${(delay ?? 0) + i * 14}ms`}"
        />
      </div>
    </div>

    <div v-if="tip" class="pop absolute bottom-full mb-2 w-[220px] p-3 z-20 pointer-events-none text-[12.5px]" :style="{left: tipLeft}">
      <div class="flex items-center justify-between gap-2 mb-2">
        <span class="font-medium">{{ dateLabel(tip.date) }}</span>
        <StateChip :state="dayState(tip)" />
      </div>
      <template v-if="tip.checks">
        <div class="flex justify-between text-dim"><span>Uptime</span><span class="num text-fg">{{ pct(tip.uptime) }}</span></div>
        <div v-if="tip.down" class="flex justify-between text-dim"><span>Down</span><span class="num text-fg">≈ {{ span(tip.down * interval) }}</span></div>
        <div v-if="tip.degraded" class="flex justify-between text-dim"><span>Degraded</span><span class="num text-fg">≈ {{ span(tip.degraded * interval) }}</span></div>
        <div class="flex justify-between text-dim"><span>Checks</span><span class="num text-fg">{{ tip.checks }}</span></div>
      </template>
      <div v-else class="text-dim">Not checked on this day.</div>
    </div>

    <!-- The same thirty days as a table, for a screen reader. -->
    <table class="sr-only">
      <caption>Daily status</caption>
      <tr><th>Day</th><th>State</th><th>Uptime</th></tr>
      <tr v-for="d in days" :key="d.date"><td>{{ d.date }}</td><td>{{ dayState(d) }}</td><td>{{ pct(d.uptime) }}</td></tr>
    </table>
  </div>
</template>
