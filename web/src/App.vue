<script setup lang="ts">
import {computed, onMounted, onUnmounted, ref} from 'vue'
import ZenoglyphMark from '@zvm/web/components/ZenoglyphMark.vue'
import LatticeBackground from '@zvm/web/components/LatticeBackground.vue'
import Icon from './components/Icon.vue'
import StateChip from './components/StateChip.vue'
import ComponentRow from './components/ComponentRow.vue'
import ConnectPanel from './components/ConnectPanel.vue'
import {askChain, host, nomChain} from './chain'
import Elapsed from './components/Elapsed.vue'
import NextCheck from './components/NextCheck.vue'
import {lastRound, status, start, span, type Component, type State} from './api'

const s = computed(() => status.summary)

// Nothing in this component reads the clock. The things that move each second
// (the countdown, "12s ago", an ongoing incident's length) are their own small
// components, so a tick re-renders a few spans rather than every row.
onMounted(start)
onMounted(askChain)

const groups = computed(() => {
  const out: {name: string; items: Component[]}[] = []
  for (const c of s.value?.components ?? []) {
    let g = out.find((x) => x.name === c.group)
    if (!g) out.push((g = {name: c.group, items: []}))
    g.items.push(c)
  }
  return out
})

// The headline, the way status pages have converged on phrasing it: one down
// component is a partial outage, half of them is a major one.
const headline = computed(() => {
  const cs = s.value?.components ?? []
  const down = cs.filter((c) => c.status === 'down').length
  const degraded = cs.filter((c) => c.status === 'degraded').length
  if (!cs.length) return {state: 'unknown' as State, text: 'Waiting for the first check'}
  if (down && down >= cs.length / 2) return {state: 'down' as State, text: 'Major outage'}
  if (down) return {state: 'down' as State, text: down === 1 ? 'Partial outage' : `Partial outage · ${down} services down`}
  if (degraded) return {state: 'degraded' as State, text: 'Degraded performance'}
  return {state: 'operational' as State, text: 'All systems operational'}
})

const interval = computed(() => s.value?.interval ?? 300)

// The bar under the headline fills over each interval as one CSS animation,
// started at the right phase by a negative delay. It used to be a width set
// every second, which laid the banner out again every second; a transform runs
// on the compositor. The phase is recomputed with every new summary, so a
// laptop that slept comes back in step with the prober's grid.
const barPhase = computed(() => {
  void s.value
  return `-${((Date.now() / 1000) % interval.value).toFixed(2)}s`
})

const lastCheck = computed(() => (s.value ? lastRound() : 0))

const ongoing = computed(() => (s.value?.incidents ?? []).filter((i) => !i.resolved))
const past = computed(() => (s.value?.incidents ?? []).filter((i) => i.resolved))

// Thirty days of a flapping service is a long scroll on a phone, and what a
// visitor asks about is almost always the last few days.
const PAST_DAYS = 3
const allPast = ref(false)

// Past incidents grouped under the day they started, newest first.
const pastByDay = computed(() => {
  const out: {day: string; items: typeof past.value}[] = []
  for (const i of past.value) {
    const day = new Date(i.started * 1000).toLocaleDateString(undefined, {weekday: 'short', month: 'short', day: 'numeric'})
    let g = out.find((x) => x.day === day)
    if (!g) out.push((g = {day, items: []}))
    g.items.push(i)
  }
  return out
})

const time = (t: number) => new Date(t * 1000).toLocaleTimeString(undefined, {hour: '2-digit', minute: '2-digit'})

const theme = ref(document.documentElement.getAttribute('data-theme') ?? 'dark')
function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  document.documentElement.setAttribute('data-theme', theme.value)
  try {
    localStorage.setItem('zvm.theme', theme.value)
  } catch {
    /* storage can be blocked; the theme still applies for this tab */
  }
}

// Two views on one page: the status board at /status/, and how to point a
// wallet at the chain at /status/rpc/. The gateway serves index.html for any
// path under /status/, so the path alone picks the view.
const base = import.meta.env.BASE_URL
// The other apps are on the gateway; a copy served elsewhere links back to it.
const gw = (import.meta.env.VITE_GATEWAY as string | undefined) ?? ''
const page = /\/rpc\/?$/.test(location.pathname) ? 'rpc' : 'status'
// The wallet settings used to be a panel on the board, at #connect.
if (page === 'status' && location.hash === '#connect') location.replace(`${base}rpc/`)
if (page === 'rpc') document.title = 'RPC · ZVM Status'

// RPC and the explorer stand alone in the bar; every other app sits under
// the kind of thing it is, one menu each.
const apps = [
  {href: `${base}rpc/`, label: 'RPC'},
  {href: `${gw}/explorer/`, label: 'Explorer'},
]
const navGroups = [
  {
    id: 'markets',
    label: 'Markets',
    items: [
      {href: `${gw}/glyphswap/`, label: 'Glyphswap'},
      {href: `${gw}/karum/`, label: 'Karum'},
      {href: `${gw}/spark/`, label: 'Spark'},
    ],
  },
  {
    id: 'social',
    label: 'Social',
    items: [
      {href: `${gw}/zns/`, label: 'ZNS'},
      {href: `${gw}/zchan/`, label: 'zchan'},
      {href: `${gw}/mozaic/`, label: 'Mozaic'},
    ],
  },
  // A bot first and a page second, each of them.
  {
    id: 'telegram',
    label: 'Telegram',
    items: [
      {href: `${gw}/rain/`, label: 'Rain'},
      {href: `${gw}/bullet/`, label: 'Bullet'},
    ],
  },
]
// One menu open at a time; a tap anywhere outside the bar closes it.
const navOpen = ref<string | null>(null)
const navEl = ref<HTMLElement | null>(null)
const onDoc = (e: PointerEvent) => navEl.value?.contains(e.target as Node) || (navOpen.value = null)

// Below sm the links don't fit in the header, so they fold into a menu under
// it rather than disappearing, as they used to.
const menu = ref(false)
const onKey = (e: KeyboardEvent) => e.key === 'Escape' && ((menu.value = false), (navOpen.value = null))
onMounted(() => {
  window.addEventListener('keydown', onKey)
  document.addEventListener('pointerdown', onDoc)
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKey)
  document.removeEventListener('pointerdown', onDoc)
})
</script>

<template>
  <div class="min-h-screen flex flex-col">
    <LatticeBackground />

    <header class="sticky top-0 z-30 border-b border-line bg-bg/85 backdrop-blur-md">
      <div class="mx-auto max-w-[880px] px-4 py-2.5 flex items-center gap-3">
        <a :href="base" class="flex items-center gap-2.5 shrink-0" title="ZVM Status">
          <ZenoglyphMark :size="26" />
          <span class="font-semibold tracking-[-0.02em] text-[15.5px]">ZVM <span class="text-dim font-normal">Status</span></span>
        </a>
        <span class="grow" />
        <nav ref="navEl" class="hidden sm:flex items-center gap-0.5 text-[13.5px]">
          <a
            v-for="a in apps"
            :key="a.href"
            :href="a.href"
            :aria-current="page === 'rpc' && a.label === 'RPC' ? 'page' : undefined"
            class="px-2.5 py-1.5 rounded text-dim hover:text-fg hover:bg-raised transition-colors aria-[current=page]:text-fg aria-[current=page]:bg-raised"
          >{{ a.label }}</a>
          <div v-for="g in navGroups" :key="g.id" class="relative">
            <button
              class="px-2.5 py-1.5 rounded text-dim hover:text-fg hover:bg-raised transition-colors inline-flex items-center gap-1 aria-expanded:text-fg aria-expanded:bg-raised"
              :aria-expanded="navOpen === g.id"
              :aria-controls="`${g.id}-menu`"
              @click="navOpen = navOpen === g.id ? null : g.id"
            >
              {{ g.label }}
              <Icon name="down" :size="13" class="transition-transform" :class="navOpen === g.id && 'rotate-180'" />
            </button>
            <div
              v-if="navOpen === g.id"
              :id="`${g.id}-menu`"
              class="menu-in absolute right-0 top-full mt-1.5 min-w-[140px] grid p-1 rounded-md border border-line bg-bg shadow-lg"
            >
              <a
                v-for="a in g.items"
                :key="a.href"
                :href="a.href"
                class="px-2.5 py-1.5 rounded text-dim hover:text-fg hover:bg-raised transition-colors"
                @click="navOpen = null"
              >{{ a.label }}</a>
            </div>
          </div>
        </nav>
        <button class="btn btn-sm btn-quiet px-2" :title="`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`" @click="toggleTheme">
          <Icon :name="theme === 'dark' ? 'sun' : 'moon'" :size="15" />
          <span class="sr-only">Switch theme</span>
        </button>
        <button
          class="sm:hidden btn btn-sm btn-quiet px-2"
          :aria-expanded="menu"
          aria-controls="mobile-nav"
          :title="menu ? 'Close menu' : 'Open menu'"
          @click="menu = !menu"
        >
          <Icon :name="menu ? 'close' : 'menu'" :size="16" />
          <span class="sr-only">Menu</span>
        </button>
      </div>
      <nav v-if="menu" id="mobile-nav" class="sm:hidden border-t border-line menu-in">
        <div class="mx-auto max-w-[880px] px-2 py-1.5 grid">
          <a
            v-for="a in apps"
            :key="a.href"
            :href="a.href"
            :aria-current="page === 'rpc' && a.label === 'RPC' ? 'page' : undefined"
            class="px-2.5 py-2.5 rounded text-[14.5px] text-soft hover:text-fg hover:bg-raised transition-colors aria-[current=page]:text-fg aria-[current=page]:bg-raised"
            @click="menu = false"
          >{{ a.label }}</a>
          <!-- A phone has room to show the sub-items, so each group is a label
               over them rather than a second menu to open. -->
          <template v-for="g in navGroups" :key="g.id">
            <span class="px-2.5 pt-3 pb-1 text-[12px] uppercase tracking-[0.08em] text-dim">{{ g.label }}</span>
            <a
              v-for="a in g.items"
              :key="a.href"
              :href="a.href"
              class="pl-5 pr-2.5 py-2.5 rounded text-[14.5px] text-soft hover:text-fg hover:bg-raised transition-colors"
              @click="menu = false"
            >{{ a.label }}</a>
          </template>
        </div>
      </nav>
    </header>
    <!-- A tap outside the open menu closes it, the way every phone menu behaves. -->
    <div v-if="menu" class="sm:hidden fixed inset-0 z-20" @click="menu = false" />

    <!-- minmax(0, 1fr), not the implicit auto track: an auto track is as wide as its
         widest unbreakable row, which pushed the page to 546px on a phone. -->
    <main class="mx-auto w-full max-w-[880px] px-4 py-6 flex-1 grid grid-cols-[minmax(0,1fr)] gap-5 content-start">
      <!-- /status/rpc/: what to put in a wallet to reach this chain. -->
      <template v-if="page === 'rpc'">
        <section class="panel overflow-hidden rail-in">
          <div class="banner-wash px-5 py-6 sm:py-7">
            <h1 class="text-[21px] sm:text-[26px] font-semibold tracking-[-0.02em] leading-tight">RPC</h1>
            <p class="text-[13px] text-dim mt-1">
              {{ host }} · chain {{ nomChain }} · the endpoints and settings to add it to a wallet.
              <a :href="base" class="hover:underline" style="color: var(--nom-ink)">Is it up?</a>
            </p>
          </div>
        </section>
        <ConnectPanel />
      </template>

      <template v-else>
      <!-- The headline. -->
      <section class="panel overflow-hidden rail-in" :class="`s-${headline.state}`">
        <div class="banner-wash px-5 py-6 sm:py-7 flex items-center gap-4">
          <span class="relative flex size-4 shrink-0">
            <span class="ping absolute inset-0 rounded-full" style="background: var(--s)" />
            <span class="relative size-4 rounded-full live-dot" style="background: var(--s); box-shadow: 0 0 18px color-mix(in srgb, var(--s) 60%, transparent)" />
          </span>
          <div class="min-w-0">
            <h1 v-if="s" class="text-[21px] sm:text-[26px] font-semibold tracking-[-0.02em] leading-tight">{{ headline.text }}</h1>
            <div v-else class="skeleton h-6 w-full max-w-64" />
            <p class="text-[13px] text-dim mt-1">
              {{ host }} · chain {{ nomChain }}<span class="hidden sm:inline"> · checked every {{ span(interval) }}</span>
            </p>
            <!-- A phone has no room beside the headline, so the countdown goes under it. -->
            <p class="sm:hidden text-[12.5px] text-dim mt-0.5">
              Next check in <NextCheck :interval="interval" class="text-fg" /> · every {{ span(interval) }}
            </p>
          </div>
          <span class="grow" />
          <div class="hidden sm:block text-right text-[12px] text-dim">
            <div class="ledger">Next check</div>
            <NextCheck :interval="interval" class="text-[18px] text-fg" />
          </div>
        </div>
        <!-- How far through the current five minutes we are. -->
        <div class="h-[3px] bg-sunk overflow-hidden">
          <div
            class="h-full countdown"
            :style="{
              animationDuration: `${interval}s`,
              animationDelay: barPhase,
              background: headline.state === 'operational' ? 'var(--plasma)' : 'var(--s)',
            }"
          />
        </div>
      </section>

      <p v-if="status.error" class="card px-4 py-2.5 text-[13px] flex items-center gap-2 rail-in" style="color: var(--danger)">
        <Icon name="alert" :size="15" />
        <span>Could not refresh: {{ status.error }}.</span>
        <span v-if="s" class="text-dim">Showing the last good copy.</span>
      </p>

      <!-- What is wrong right now, above everything else. -->
      <section v-if="ongoing.length" class="panel rail-in s-down" style="border-color: color-mix(in srgb, var(--down) 40%, var(--line))">
        <div class="px-4 py-3 border-b border-line flex items-center gap-2">
          <span class="ledger" style="color: var(--down)">Ongoing</span>
        </div>
        <div v-for="i in ongoing" :key="i.id" class="px-4 py-3 border-b border-line last:border-b-0 flex flex-wrap items-center gap-x-3 gap-y-1">
          <StateChip :state="i.worst" />
          <span class="font-medium">{{ i.name }}</span>
          <span class="grow" />
          <span class="text-[12px] text-dim">since {{ time(i.started) }} · <Elapsed :t="i.started" kind="span" /></span>
          <span class="basis-full mono text-[12.5px] text-soft break-words">{{ i.detail }}</span>
        </div>
      </section>

      <!-- Loading: the shape of what is coming, not a spinner. -->
      <template v-if="!s">
        <section v-for="n in 3" :key="n" class="panel p-4 grid gap-4">
          <div class="skeleton h-3 w-28" />
          <div v-for="m in 3" :key="m" class="grid gap-2">
            <div class="skeleton h-3 w-44" />
            <div class="flex gap-[2px] h-9">
              <div v-for="k in 30" :key="k" class="flex-1 rounded-[3px] bg-sunk waiting" :style="{animationDelay: `${k * 40}ms`}" />
            </div>
          </div>
        </section>
      </template>

      <section v-for="(g, gi) in groups" :key="g.name" class="panel rail-in" :style="{animationDelay: `${80 + gi * 70}ms`}">
        <div class="px-4 py-2.5 border-b border-line flex items-center">
          <span class="ledger">{{ g.name }}</span>
          <span class="grow" />
          <span class="text-[11.5px] text-dim">{{ g.items.filter((c) => c.status === 'operational').length }}/{{ g.items.length }} operational</span>
        </div>
        <ComponentRow
          v-for="(c, ci) in g.items"
          :key="c.id"
          :c="c"
          :interval="s!.interval"
          :days="s!.days"
          :delay="120 + gi * 70 + ci * 50"
        />
      </section>

      <!-- Past incidents. -->
      <section v-if="s" class="panel rail-in" style="animation-delay: 360ms">
        <div class="px-4 py-2.5 border-b border-line flex items-center">
          <span class="ledger">Past incidents · {{ s.days }} days</span>
        </div>
        <p v-if="!pastByDay.length" class="px-4 py-5 text-[13px] text-dim">No incidents in the last {{ s.days }} days.</p>
        <div v-for="d in allPast ? pastByDay : pastByDay.slice(0, PAST_DAYS)" :key="d.day" class="px-4 py-3 border-b border-line last:border-b-0">
          <div class="text-[13px] font-medium mb-1">{{ d.day }}</div>
          <!-- On a phone: what and how long on the first line, then when and the
               detail in full. Cut to fit beside the rest, the detail was fifteen
               characters there. -->
          <div v-for="i in d.items" :key="i.id" class="py-1.5 text-[13px] flex flex-wrap items-center gap-x-3 gap-y-0.5">
            <StateChip :state="i.worst" />
            <span>{{ i.name }}</span>
            <span class="hidden sm:block flex-1 min-w-0 mono text-[12px] text-dim truncate" :title="i.detail">{{ i.detail }}</span>
            <span class="grow sm:hidden" />
            <span class="num text-[12px] text-dim"><span class="hidden sm:inline">{{ time(i.started) }}–{{ time(i.resolved!) }} · </span>{{ span(i.resolved! - i.started) }}</span>
            <span class="sm:hidden basis-full text-[12px] text-dim">
              <span class="num">{{ time(i.started) }}–{{ time(i.resolved!) }}</span> · <span class="mono break-words">{{ i.detail }}</span>
            </span>
          </div>
        </div>
        <button
          v-if="pastByDay.length > PAST_DAYS"
          class="w-full px-4 py-3 border-t border-line text-[13px] text-dim hover:text-fg hover:bg-raised transition-colors flex items-center justify-center gap-1.5"
          @click="allPast = !allPast"
        >
          {{ allPast ? 'Show fewer' : `Show ${pastByDay.length - PAST_DAYS} earlier ${pastByDay.length - PAST_DAYS === 1 ? 'day' : 'days'}` }}
          <Icon name="down" :size="14" class="transition-transform" :class="allPast ? 'rotate-180' : ''" />
        </button>
      </section>
      </template>
    </main>

    <!-- The header's backdrop, so the two bars read as one frame round the page. -->
    <footer class="border-t border-line mt-6 bg-bg/85 backdrop-blur-md pb-[env(safe-area-inset-bottom)]">
      <div class="mx-auto max-w-[880px] px-4 py-2.5 flex flex-wrap items-center gap-x-6 gap-y-1 text-[12px] text-dim">
        <div class="flex items-center gap-1.5">
          <span class="ledger">Last check</span>
          <span class="num">{{ lastCheck ? new Date(lastCheck * 1000).toLocaleString() : '—' }}</span>
        </div>
        <div class="flex items-center gap-1.5">
          <Icon name="mail" :size="13" />
          {{ s?.alerts ? 'Alerts emailed when a service goes down' : 'Alerts not configured — outages are recorded only' }}
        </div>
      </div>
    </footer>
  </div>
</template>
