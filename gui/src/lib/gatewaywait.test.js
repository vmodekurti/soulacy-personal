import { describe, it, expect, vi } from 'vitest'
import {
  waitForGateway, waitingMessage, timeoutMessage,
  RESTART_BUDGET, UPGRADE_BUDGET,
} from './gatewaywait.js'

// A fake clock. Every test here would otherwise take a real minute.
const fakeSleep = () => {
  const slept = []
  return { fn: async (ms) => { slept.push(ms) }, slept }
}

describe('waiting for the gateway', () => {
  it('reloads as soon as the gateway answers, not on a fixed timer', async () => {
    let calls = 0
    const probe = async () => { if (++calls < 4) throw new Error('ECONNREFUSED') }
    const clock = fakeSleep()

    const res = await waitForGateway(probe, { attempts: 60, intervalMs: 1000, sleep: clock.fn })

    expect(res.ok).toBe(true)
    expect(res.attempts).toBe(4)
    // It stopped asking the moment it got an answer.
    expect(calls).toBe(4)
  })

  // The bug this file exists for. A five-second timer reloads into a dead port
  // when the gateway takes six; the whole point of polling is that it does not
  // care how long the restart takes.
  it('keeps waiting well past the five seconds the old code allowed', async () => {
    let calls = 0
    const probe = async () => { if (++calls < 23) throw new Error('ECONNREFUSED') }
    const clock = fakeSleep()

    const res = await waitForGateway(probe, { attempts: 120, intervalMs: 1000, sleep: clock.fn })

    expect(res.ok).toBe(true)
    expect(res.waitedMs).toBe(23000)
  })

  // Probing immediately would reach the OLD process, which answers happily for
  // a moment after replying and then exits. We would reload straight into the
  // gap and show the user the very error we are trying to prevent.
  it('waits before the first probe rather than catching the dying process', async () => {
    const order = []
    const probe = async () => { order.push('probe') }
    const sleep = async () => { order.push('sleep') }

    await waitForGateway(probe, { attempts: 5, intervalMs: 1000, sleep })

    expect(order[0]).toBe('sleep')
  })

  it('gives up after its budget instead of spinning forever', async () => {
    const probe = async () => { throw new Error('ECONNREFUSED') }
    const clock = fakeSleep()

    const res = await waitForGateway(probe, { attempts: 7, intervalMs: 1000, sleep: clock.fn })

    expect(res.ok).toBe(false)
    expect(res.attempts).toBe(7)
    expect(clock.slept).toHaveLength(7)
  })

  it('never rejects — a refused connection is the expected case here', async () => {
    const probe = async () => { throw new Error('ECONNREFUSED') }
    await expect(
      waitForGateway(probe, { attempts: 2, intervalMs: 1, sleep: async () => {} }),
    ).resolves.toMatchObject({ ok: false })
  })

  it('reports progress so a long wait does not look like a hang', async () => {
    const seen = []
    let calls = 0
    const probe = async () => { if (++calls < 3) throw new Error('nope') }
    await waitForGateway(probe, {
      attempts: 10, intervalMs: 1000, sleep: async () => {},
      onAttempt: (n, total) => seen.push([n, total]),
    })
    expect(seen).toEqual([[1, 10], [2, 10], [3, 10]])
  })

  it('gives an upgrade longer than a restart, because it boots a new binary', () => {
    expect(UPGRADE_BUDGET.attempts).toBeGreaterThan(RESTART_BUDGET.attempts)
  })
})

describe('what the user is told', () => {
  it('does not put a number on the wait until it is worth mentioning', () => {
    expect(waitingMessage(1, 120)).not.toMatch(/\d+s/)
    expect(waitingMessage(30, 120)).toMatch(/30s/)
  })

  it('explains the delay once it is unusual, rather than just counting', () => {
    expect(waitingMessage(115, 120)).toMatch(/migrating/i)
  })

  // "Upgrade failed" would be a lie: the binary was replaced before the restart
  // was even attempted. The honest report is that it installed and has not come
  // back yet.
  it('does not claim a timed-out upgrade failed', () => {
    const msg = timeoutMessage('upgrade', 120000)
    expect(msg).toMatch(/installed/i)
    expect(msg).not.toMatch(/upgrade failed/i)
    expect(msg).toMatch(/logs/i)
  })

  it('tells a timed-out restart where to look', () => {
    expect(timeoutMessage('restart', 60000)).toMatch(/logs/i)
  })
})
