// repairverdict.js — turn an /studio/apply-repair response into one honest
// sentence.
//
// The server now distinguishes three outcomes, and collapsing them back into
// "applied / not applied" in the UI would throw away the thing that makes
// self-heal trustworthy:
//
//   promoted  — replayed against the failing run's real output and it held up
//   unproven  — validated and applied, but there was no evidence to replay
//   rejected  — replayed and it did NOT hold up, so the change was rolled back
//
// The old UI said "Run Live again to confirm" for every one of these, which was
// wrong twice over: it understated a proven fix and it hid a rejected one.

/** Node label for a message, falling back to the proposal when the server's record is thin. */
function nodeName(res, proposal) {
  const a = (res && res.attempt) || {}
  return a.node_id || (proposal && proposal.node_id) || 'the step'
}

/**
 * repairVerdict returns the sentence to show for one apply-repair response.
 * Returns '' when there is nothing meaningful to say (no response at all).
 */
export function repairVerdict(res, proposal) {
  if (!res) return ''
  const attempt = res.attempt || {}
  const ver = res.verification || {}
  const node = nodeName(res, proposal)

  // Refused outright — a security-class failure is never "repaired" by editing
  // the workflow, so this is a deliberate no, not a malfunction.
  if (res.failure && res.failure.security) {
    return `Not repaired: ${res.failure.summary || 'this failure class is never fixed by changing the workflow.'}`
  }

  if (res.applied) {
    if (attempt.promoted && ver.evidence_seeded) {
      return `Adjusted “${node}” and replayed the failing run against it — the fix holds.`
    }
    if (attempt.promoted) {
      return `Adjusted “${node}” and replayed it in the sandbox — the fix holds.`
    }
    // Applied on the structural check alone.
    return `Adjusted “${node}”. Not yet proven: ${ver.note || 'there was no record of the failing run to replay against.'} Run Live again to confirm.`
  }

  // Not applied. Separate "we disproved it" from "it never matched".
  if (attempt.validated) {
    const why = attempt.reason || 'the replay did not meet the expected outcome.'
    return `Reverted “${node}”: the change was tested against the failing run and did not fix it — ${why}`
  }
  if (attempt.reason) return `Could not apply the adjustment: ${attempt.reason}`
  return 'Could not apply the adjustment.'
}

/**
 * repairTone maps a response to a UI severity so the caller can pick a style
 * without re-deriving the outcome: 'ok' | 'warn' | 'error' | ''.
 */
export function repairTone(res) {
  if (!res) return ''
  if (res.failure && res.failure.security) return 'error'
  if (res.applied) return res.attempt && res.attempt.promoted ? 'ok' : 'warn'
  return 'error'
}

/**
 * repairProofLabel is the short badge next to an applied repair. Empty when the
 * repair was not applied — there is no proof to describe.
 */
export function repairProofLabel(res) {
  if (!res || !res.applied) return ''
  const attempt = res.attempt || {}
  const ver = res.verification || {}
  if (attempt.promoted && ver.evidence_seeded) return 'proven against the failing run'
  if (attempt.promoted) return 'proven in sandbox'
  return 'unproven'
}
