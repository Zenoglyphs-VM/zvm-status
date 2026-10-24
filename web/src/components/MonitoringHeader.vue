<script setup lang="ts">
import {computed} from 'vue'
import {host, nomChain} from '../chain'
import {now, monotonicNow, status, span, type State} from '../api'
import {monitoringFreshness} from '../freshness'
import NextCheck from './NextCheck.vue'

// The expiry clock is local to this header; historical rows do not rerender
// once a second. Retained copies expire even when HTTP keeps returning them.
const freshness = computed(() => monitoringFreshness(status.summary, now.value, status.deadline ? {now: monotonicNow.value, until: status.deadline.until} : undefined))
const interval = computed(() => status.summary?.interval ?? 300)
const headline = computed(() => {
  if (freshness.value.state !== 'fresh') return {state: 'unknown' as State, text: freshness.value.text}
  const cs = status.summary?.components ?? []
  if (!cs.length) return {state: 'unknown' as State, text: 'Waiting for the first check'}
  const down = cs.filter((c) => c.status === 'down').length
  const degraded = cs.filter((c) => c.status === 'degraded').length
  if (down && down >= cs.length / 2) return {state: 'down' as State, text: 'Major outage'}
  if (down) return {state: 'down' as State, text: down === 1 ? 'Partial outage' : `Partial outage · ${down} services down`}
  if (degraded) return {state: 'degraded' as State, text: 'Degraded performance'}
  return {state: 'operational' as State, text: 'All systems operational'}
})
const phase = computed(() => {
  void status.summary
  return `-${((Date.now() / 1000) % interval.value).toFixed(2)}s`
})
const notification = computed(() => {
  switch (status.summary?.monitoring?.notifications?.state) {
    case 'disabled': return 'Email alerts are not configured.'
    case 'unverified': return 'Email is configured; relay acceptance has not been observed.'
    case 'pending': return freshness.value.state === 'failed' ? 'No relay acceptance was observed before the deadline.' : 'An alert relay attempt is pending.'
    case 'accepted': return 'The relay accepted an alert; recipient delivery is not verified.'
    case 'failed': return 'The latest alert relay attempt failed.'
    default: return ''
  }
})
</script>

<template>
  <section class="panel overflow-hidden rail-in" :class="`s-${headline.state}`" aria-live="polite">
    <div class="banner-wash px-5 py-6 sm:py-7 flex items-center gap-4">
      <span class="relative flex size-4 shrink-0">
        <span v-if="freshness.state === 'fresh'" class="ping absolute inset-0 rounded-full" style="background: var(--s)" />
        <span class="relative size-4 rounded-full" style="background: var(--s); box-shadow: 0 0 18px color-mix(in srgb, var(--s) 60%, transparent)" />
      </span>
      <div class="min-w-0">
        <h1 class="text-[21px] sm:text-[26px] font-semibold tracking-[-0.02em] leading-tight">{{ headline.text }}</h1>
        <p class="text-[13px] text-dim mt-1">{{ host }} · chain {{ nomChain }} · checked every {{ span(interval) }}</p>
        <p v-if="freshness.state === 'fresh'" class="sm:hidden text-[12.5px] text-dim mt-0.5">Next check in <NextCheck :interval="interval" class="text-fg" /></p>
        <p v-if="freshness.state !== 'fresh'" class="text-[13px] text-dim mt-1">Historical observations remain below. Current service health is unverified.</p>
        <p v-if="notification" class="text-[12px] text-dim mt-1">{{ notification }}</p>
      </div>
      <div v-if="freshness.state === 'fresh'" class="hidden sm:block ml-auto text-right text-[12px] text-dim">
        <div class="ledger">Next check</div>
        <NextCheck :interval="interval" class="text-[18px] text-fg" />
      </div>
    </div>
    <div v-if="freshness.state === 'fresh'" class="h-[3px] bg-sunk overflow-hidden">
      <div class="h-full countdown" :style="{animationDuration: `${interval}s`, animationDelay: phase, background: headline.state === 'operational' ? 'var(--plasma)' : 'var(--s)'}" />
    </div>
  </section>
</template>
