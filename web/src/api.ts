// The one request this page makes, repeated. The API computes every number;
// this file only fetches it and keeps the last good copy when a fetch fails,
// because a status page that goes blank when its own API blips is reporting
// the wrong outage.

import {markRaw, reactive, ref} from 'vue'

export type State = 'operational' | 'degraded' | 'down' | 'unknown'

export interface Day {
  date: string
  checks: number
  degraded: number
  down: number
  uptime: number | null
  worst: State | 'none'
}

export interface Point {
  t: number
  latency: number
  status: State
}

export interface Component {
  id: string
  name: string
  group: string
  description: string
  status: State
  detail: string
  latency: number
  checkedAt: number
  uptime: number | null
  days: Day[]
}

export interface Incident {
  id: number
  component: string
  name: string
  started: number
  resolved: number | null
  worst: State
  detail: string
}

export interface Summary {
  generated: number
  interval: number
  days: number
  overall: State
  alerts: boolean
  components: Component[]
  incidents: Incident[] | null
}

export const status = reactive({
  summary: null as Summary | null,
  error: null as string | null,
  fetchedAt: 0,
})

// A copy served away from the gateway (GitHub Pages) sets VITE_GATEWAY to the
// gateway's origin and reads the live API there; the API answers any origin.
const GATEWAY = import.meta.env.VITE_GATEWAY as string | undefined
const BASE = GATEWAY ? `${GATEWAY}/status/api/` : `${import.meta.env.BASE_URL}api/`

// `no-cache` lets the browser keep the last body and revalidate it with the
// API's ETag, so a poll inside the same round costs a 304 and no JSON. The
// browser hides the 304 from us and hands back its stored copy, so the ETag
// is compared here too: an unchanged summary is not parsed again, and more to
// the point is not reassigned, which would re-render every row for nothing.
let etag = ''

export async function refresh() {
  try {
    const res = await fetch(`${BASE}summary`, {cache: 'no-cache', signal: AbortSignal.timeout(10_000)})
    if (!res.ok) throw new Error(`the status API answered ${res.status}`)
    const tag = res.headers.get('ETag') ?? ''
    if (!tag || tag !== etag || !status.summary) {
      // markRaw: the summary is replaced whole, never edited, so deep
      // reactivity over its ~350 day objects would be proxies nobody uses.
      status.summary = markRaw((await res.json()) as Summary)
      etag = tag
    }
    status.error = null
    status.fetchedAt = Date.now()
  } catch (err: any) {
    status.error = err?.name === 'TimeoutError' ? 'the status API did not answer' : String(err?.message ?? err)
  }
}

/** The latest round the page has seen, in unix seconds. */
export function lastRound(): number {
  return Math.max(0, ...(status.summary?.components ?? []).map((c) => c.checkedAt))
}

// Polling follows the prober's clock instead of a fixed 30 s. Rounds start on
// the wall-clock grid and take up to half a minute (a failed check waits 20 s
// for its retry), so the page asks every 5 s from just after the boundary until
// that round appears, then sleeps until the next boundary. A new round shows
// within seconds of landing instead of up to 30 s later, and a quiet page
// makes one request a minute at most. Hidden tabs do not poll at all.
let timer = 0
function schedule() {
  clearTimeout(timer)
  if (document.visibilityState !== 'visible') return
  const iv = status.summary?.interval ?? 300
  const t = Date.now() / 1000
  const boundary = Math.floor(t / iv) * iv
  const waiting = lastRound() < boundary && t - boundary < 120
  const wait = waiting ? 5 : Math.min(60, boundary + iv + 3 - t)
  timer = window.setTimeout(async () => {
    await refresh()
    schedule()
  }, Math.max(1, wait) * 1000)
}

export async function start() {
  document.addEventListener('visibilitychange', async () => {
    if (document.visibilityState !== 'visible') return clearTimeout(timer)
    await refresh()
    schedule()
  })
  await refresh()
  schedule()
}

// One component's last 24 hours, for its chart, fetched when a row is opened
// and again when a new round lands while it is open.
const recentTags = new Map<string, {etag: string; points: Point[]}>()
export async function fetchRecent(id: string): Promise<Point[]> {
  const res = await fetch(`${BASE}recent/${encodeURIComponent(id)}`, {cache: 'no-cache', signal: AbortSignal.timeout(10_000)})
  if (!res.ok) throw new Error(`the status API answered ${res.status}`)
  const tag = res.headers.get('ETag') ?? ''
  const hit = recentTags.get(id)
  if (hit && tag && hit.etag === tag) return hit.points
  const points = markRaw((await res.json()) as Point[])
  recentTags.set(id, {etag: tag, points})
  return points
}

// One clock for everything relative on the page, shared so the components
// that show a live time subscribe to it themselves: when the page root read
// it, every row re-rendered once a second. It stops while the tab is hidden.
export const now = ref(Date.now())
let clock = 0
function tick() {
  clearInterval(clock)
  now.value = Date.now()
  if (document.visibilityState === 'visible') clock = window.setInterval(() => (now.value = Date.now()), 1000)
}
tick()
document.addEventListener('visibilitychange', tick)

export const LABEL: Record<State | 'none', string> = {
  operational: 'Operational',
  degraded: 'Degraded',
  down: 'Down',
  unknown: 'No data',
  none: 'No data',
}

/**
 * What a day's bar reads as, by its service level rather than its single
 * worst check: one failed check in a day of 288 is 99.65% uptime, and red
 * for that said "outage" about a day that was nearly all up. At 100% it is
 * the day's worst state (operational, or degraded by slow checks); from 95%
 * up to 100% it is degraded; under 95% it is down.
 */
export function dayState(d: Day): State | 'none' {
  if (d.worst === 'none' || d.uptime == null || d.uptime >= 1) return d.worst
  return d.uptime >= 0.95 ? 'degraded' : 'down'
}

export function pct(u: number | null | undefined): string {
  if (u == null) return '—'
  if (u >= 1) return '100%'
  // Never round a bad day up to a clean one: 99.996% is not 100%.
  return `${(Math.floor(u * 10000) / 100).toFixed(2)}%`
}

export function ago(unix: number, now = Date.now()): string {
  const s = Math.max(0, Math.round(now / 1000 - unix))
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m ago`
  return `${Math.floor(s / 86400)}d ago`
}

export function span(seconds: number): string {
  const m = Math.round(seconds / 60)
  if (m < 60) return `${m} min`
  const h = Math.floor(m / 60)
  if (h < 48) return `${h} h ${m % 60} min`
  return `${Math.floor(h / 24)} d ${h % 24} h`
}
