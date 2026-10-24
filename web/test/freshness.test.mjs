import assert from 'node:assert/strict'
import {test} from 'node:test'
import {monitoringFreshness, monitoringDeadline} from '../src/freshness.ts'

const at = 1_800_000_000
function snapshot() {
  return {monitoring: {state: 'fresh', checkedAt: at, freshUntil: at + 660, notifications: {state: 'unverified', attemptedAt: 0, acceptedAt: 0}}}
}

test('a retained operational copy expires without another fetch or round', () => {
  const copy = snapshot()
  assert.equal(monitoringFreshness(copy, (at + 659) * 1000).state, 'fresh')
  assert.equal(monitoringFreshness(copy, (at + 660) * 1000).state, 'stale')
  // Repeated successful 304 revalidations retain this exact same object.
  for (let i = 0; i < 100; i++) assert.equal(monitoringFreshness(copy, (at + 700 + i) * 1000).state, 'stale')
  assert.equal(monitoringFreshness({...copy, monitoring: {...copy.monitoring, checkedAt: at + 900, freshUntil: at + 1560}}, (at + 901) * 1000).state, 'fresh')
})

test('legacy and missing monitoring do not establish operational status', () => {
  assert.equal(monitoringFreshness(null, at * 1000).state, 'starting')
  assert.equal(monitoringFreshness({}, at * 1000).state, 'unknown')
})

test('unestablished server states and failed relay attempts are visible', () => {
  for (const state of ['starting', 'stale', 'stopped', 'failed']) {
    const s = snapshot();s.monitoring.state = state
    assert.equal(monitoringFreshness(s, at * 1000).state, state)
  }
  const failed = snapshot();failed.monitoring.notifications.state = 'failed'
  assert.equal(monitoringFreshness(failed, at * 1000).state, 'failed')
})

test('future, invalid and contradictory deadlines fail visibly', () => {
  for (const value of [NaN, Infinity, -1, 1.5, Number.MAX_SAFE_INTEGER + 1, at + 1]) {
    const s = snapshot();s.monitoring.checkedAt = value
    assert.equal(monitoringFreshness(s, at * 1000).state, 'unknown')
  }
  const reversed = snapshot();reversed.monitoring.freshUntil = at - 1
  assert.equal(monitoringFreshness(reversed, at * 1000).state, 'unknown')
  assert.equal(monitoringFreshness(snapshot(), NaN).state, 'unknown')
})

test('a pending relay expires independently of successful HTTP polls', () => {
  const s = snapshot();s.monitoring.notifications = {state: 'pending', attemptedAt: at, acceptedAt: 0}
  assert.equal(monitoringFreshness(s, (at + 29) * 1000).state, 'fresh')
  assert.equal(monitoringFreshness(s, (at + 30) * 1000).state, 'failed')
})

test('relay acceptance is distinct from configuration and clock validity', () => {
  for (const state of ['disabled', 'unverified', 'accepted']) {
    const s = snapshot();s.monitoring.notifications.state = state
    assert.equal(monitoringFreshness(s, at * 1000).state, 'fresh')
  }
  const future = snapshot();future.monitoring.notifications.acceptedAt = at + 1
  assert.equal(monitoringFreshness(future, at * 1000).state, 'unknown')
})

test('wall clock rollback and notification ETags cannot extend a sample', () => {
  const s = snapshot()
  const deadline = monitoringDeadline(s, at * 1000, 1000, null)
  assert.equal(deadline.until, 661000)
  s.monitoring.notifications.state = 'accepted'
  const repeated = monitoringDeadline(s, (at + 100) * 1000, 661000, deadline)
  assert.equal(repeated.until, deadline.until)
  assert.equal(monitoringFreshness(s, (at + 100) * 1000, {now: 661000, until: repeated.until}).state, 'stale')
})

test('an actually new sample establishes a new monotonic deadline', () => {
  const first = monitoringDeadline(snapshot(), at * 1000, 1000, null)
  const next = snapshot();next.monitoring.checkedAt += 900;next.monitoring.freshUntil += 900
  const renewed = monitoringDeadline(next, (at + 900) * 1000, 901000, first)
  assert.ok(renewed.until > first.until)
  assert.equal(monitoringFreshness(next, (at + 901) * 1000, {now: 902000, until: renewed.until}).state, 'fresh')
})
