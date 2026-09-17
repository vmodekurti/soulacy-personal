// deployment.js — what this deployment can do, and what that means for a
// thing the user is about to install.
//
// The gateway reports its own limits at /doctor. These helpers turn that into
// the two answers a screen needs: a short summary of the deployment, and —
// before someone fills in a form that cannot work here — the reason it will
// not, with what to do instead.

/** Pull the deployment section out of a /doctor response. */
export function deploymentFrom(doctor) {
  const d = doctor && doctor.deployment
  if (!d || !Array.isArray(d.capabilities)) return null
  const byID = {}
  for (const c of d.capabilities) byID[c.id] = c
  return {
    platform: d.platform || 'unknown',
    platformKind: d.platform_kind || '',
    workspace: d.workspace || '',
    capabilities: d.capabilities,
    byID,
  }
}

/** Capabilities this deployment does not have. These are what a user needs told. */
export function limitsOf(report) {
  if (!report) return []
  return report.capabilities.filter(c => !c.available)
}

/**
 * Why a template cannot run here, if it cannot.
 *
 * A template declares what it needs (`requires: ['node_runtime']`). An unmet
 * requirement is returned with the deployment's own explanation and
 * workaround, so the screen never has to invent either. An unknown
 * requirement is not a blocker: an older gateway that does not report a
 * capability should not have every template greyed out on a guess.
 */
export function blockersFor(template, report) {
  if (!report || !template || !Array.isArray(template.requires)) return []
  const out = []
  for (const id of template.requires) {
    const cap = report.byID[id]
    if (cap && !cap.available) out.push(cap)
  }
  return out
}

/** A one-line summary for a header: "Docker · 4 of 6 available". */
export function summaryOf(report) {
  if (!report) return ''
  const total = report.capabilities.length
  const ok = report.capabilities.filter(c => c.available).length
  return `${report.platform} · ${ok} of ${total} available`
}
