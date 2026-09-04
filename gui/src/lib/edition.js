// Stable browser-side edition contract. Product pages ask for capabilities;
// they do not compare plan names. A commercial distribution can add or replace
// descriptors at its composition root without forking the application shell.

export const editionCapabilities = Object.freeze({
  multiUser: 'multi_user',
  workspaceAdmin: 'workspace_admin',
  centralizedProviders: 'centralized_providers',
  externalIdentity: 'external_identity',
  auditReporting: 'audit_reporting',
  distributedRuntime: 'distributed_runtime',
  highAvailability: 'high_availability',
})

export const personalEdition = Object.freeze({
  id: 'personal',
  displayName: 'Personal',
  commercial: false,
  extends: null,
  capabilities: Object.freeze([]),
})

export function createEditionRegistry(extensions = []) {
  const descriptors = new Map([[personalEdition.id, personalEdition]])
  for (const candidate of extensions) {
    if (!candidate?.id || !candidate?.displayName) throw new Error('edition id and displayName are required')
    if (descriptors.has(candidate.id)) throw new Error(`edition ${candidate.id} is already registered`)
    if (candidate.extends && !descriptors.has(candidate.extends)) {
      throw new Error(`edition ${candidate.id} extends unknown edition ${candidate.extends}`)
    }
    const parentCapabilities = candidate.extends
      ? descriptors.get(candidate.extends).capabilities
      : []
    descriptors.set(candidate.id, Object.freeze({
      ...candidate,
      capabilities: Object.freeze([...new Set([...parentCapabilities, ...(candidate.capabilities || [])])]),
    }))
  }
  return Object.freeze({
    resolve(mode) {
      return descriptors.get(String(mode || '').trim().toLowerCase()) || personalEdition
    },
    ids() { return Object.freeze([...descriptors.keys()]) },
  })
}
