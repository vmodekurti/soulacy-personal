// Waiting for the gateway to come back.
//
// Restarting and upgrading both end the same way: this process is about to
// die, a replacement is starting, and the page has to reload once the new one
// answers. The only honest way to know when that is, is to ask.
//
// The upgrade path did not ask. It waited a flat five seconds and reloaded:
//
//   const res = await api.updates.upgrade()
//   setTimeout(() => window.location.reload(), 5000)
//
// The old process exits 250ms after replying, and the replacement then has to
// boot, open its database, and bind the port. When that took longer than five
// seconds — which on a real install it usually does — the reload hit a dead
// port and the browser said "Failed to fetch" on top of a message that had
// just said the upgrade succeeded. Both statements were true and together they
// read as a broken upgrade.
//
// App.svelte already polled /health properly for a plain restart. The upgrade
// buttons on two different pages each had their own five-second guess instead.
// So this is the one implementation, and it takes its probe and its clock as
// arguments so the waiting can be tested without a server or a real delay.

/** How long each caller is willing to wait. An upgrade has more to do than a
 *  restart — it boots a binary that has never run on this machine before, which
 *  may migrate a database on the way up — so it gets a longer budget. */
export const RESTART_BUDGET = { attempts: 60, intervalMs: 1000 }
export const UPGRADE_BUDGET = { attempts: 120, intervalMs: 1000 }

const realSleep = (ms) => new Promise((r) => setTimeout(r, ms))

/**
 * Poll `probe` until it resolves, then report success.
 *
 * @param {() => Promise<any>} probe        usually api.health
 * @param {object}   opts
 * @param {number}   opts.attempts          how many times to ask
 * @param {number}   opts.intervalMs        gap between asks
 * @param {(n:number, total:number) => void} [opts.onAttempt]  progress, for the spinner's caption
 * @param {(ms:number) => Promise<void>}     [opts.sleep]      injected so tests do not wait
 * @returns {Promise<{ok: boolean, attempts: number, waitedMs: number}>}
 *
 * Never throws and never rejects: a probe that fails is the expected case for
 * most of this loop's life. `ok:false` means the budget ran out, which is the
 * caller's cue to say so rather than spin forever.
 */
export async function waitForGateway(probe, opts = {}) {
  const {
    attempts = RESTART_BUDGET.attempts,
    intervalMs = RESTART_BUDGET.intervalMs,
    onAttempt,
    sleep = realSleep,
  } = opts

  for (let i = 1; i <= attempts; i++) {
    // Sleep BEFORE the first probe. The old process is still answering for a
    // moment after it replies — probing immediately would get a cheerful "ok"
    // from the very process that is about to exit, and we would reload into
    // the gap.
    await sleep(intervalMs)
    if (onAttempt) onAttempt(i, attempts)
    try {
      await probe()
      return { ok: true, attempts: i, waitedMs: i * intervalMs }
    } catch (_) {
      // Not back yet. This is the normal case, not an error worth surfacing.
    }
  }
  return { ok: false, attempts, waitedMs: attempts * intervalMs }
}

/** The caption under the spinner, so the wait does not look like a hang. */
export function waitingMessage(n, total, intervalMs = 1000) {
  const secs = Math.round((n * intervalMs) / 1000)
  if (n <= 3) return 'Waiting for the gateway to come back…'
  if (n < total * 0.75) return `Waiting for the gateway to come back… (${secs}s)`
  return `Still waiting after ${secs}s — the new version may be migrating data on first start.`
}

/** What to tell the user when the budget ran out. Says where to look, and does
 *  not claim the upgrade failed — the binary was replaced either way. */
export function timeoutMessage(kind, waitedMs) {
  const secs = Math.round(waitedMs / 1000)
  return kind === 'upgrade'
    ? `The upgrade was installed, but the gateway has not answered in ${secs}s. ` +
      'It may still be starting. Check the server logs, then reload this page.'
    : `The gateway did not come back within ${secs}s — check the server logs.`
}
