export interface Monitoring {
  state: string
  checkedAt: number
  freshUntil: number
  failure?: string
  notifications: {state: string; attemptedAt: number; acceptedAt: number}
}

export type MonitoringState = 'fresh' | 'starting' | 'stale' | 'failed' | 'stopped' | 'unknown'

export interface MonitoringDeadline {sample: string; until: number}

export function monitoringDeadline(summary: {monitoring?: Monitoring} | null, wallNow: number, monotonicNow: number, previous: MonitoringDeadline | null): MonitoringDeadline | null {
  const m = summary?.monitoring
  if (!m || !Number.isFinite(wallNow) || !Number.isFinite(monotonicNow) || !Number.isSafeInteger(m.checkedAt) || !Number.isSafeInteger(m.freshUntil)) return null
  const sample = `${m.checkedAt}:${m.freshUntil}`
  const until = monotonicNow + Math.max(0, m.freshUntil * 1000 - wallNow)
  return {sample, until: previous?.sample === sample ? Math.min(previous.until, until) : until}
}

export function monitoringFreshness(summary: {monitoring?: Monitoring} | null, now: number, clock?: {now: number; until: number}): {state: MonitoringState; text: string} {
  if (!summary) return {state: 'starting', text: 'Waiting for the first check'}
  const m = summary.monitoring
  if (!m) return {state: 'unknown', text: 'Monitoring freshness is unavailable'}
  if (m.state === 'starting') return {state: 'starting', text: 'Waiting for the first completed check'}
  if (m.state === 'stopped') return {state: 'stopped', text: 'Monitoring has stopped'}
  if (m.state === 'stale') return {state: 'stale', text: 'Monitoring data is stale'}
  if (m.state !== 'fresh' || m.failure) return {state: 'failed', text: 'Monitoring needs attention'}
  const seconds = Math.floor(now / 1000)
  if (!Number.isFinite(now) || !Number.isSafeInteger(m.checkedAt) || !Number.isSafeInteger(m.freshUntil) || m.checkedAt <= 0 || m.checkedAt > seconds || m.freshUntil <= m.checkedAt) {
    return {state: 'unknown', text: 'Monitoring time could not be verified'}
  }
  // Successful fetches and unchanged ETags never extend this producer deadline.
  if (m.freshUntil <= seconds) return {state: 'stale', text: 'Monitoring data is stale'}
  if (clock && (!Number.isFinite(clock.now) || !Number.isFinite(clock.until) || clock.now >= clock.until)) return {state: 'stale', text: 'Monitoring data is stale'}
  const n = m.notifications
  if (!n || !['disabled', 'unverified', 'pending', 'accepted', 'failed'].includes(n.state)) return {state: 'unknown', text: 'Notification state is unavailable'}
  if (n.state === 'failed' || (n.state === 'pending' && (!Number.isSafeInteger(n.attemptedAt) || n.attemptedAt <= 0 || seconds >= n.attemptedAt + 30))) {
    return {state: 'failed', text: 'The alert relay needs attention'}
  }
  if (n.attemptedAt > seconds || n.acceptedAt > seconds) return {state: 'unknown', text: 'Notification time could not be verified'}
  return {state: 'fresh', text: ''}
}
