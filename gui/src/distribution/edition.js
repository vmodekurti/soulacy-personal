// Distribution composition root. The open-source repository registers no
// extensions here; this combined repository currently bundles Teams and Scale.
import { createEditionRegistry } from '../lib/edition.js'
import { commercialEditions } from '../editions/commercial.js'

export const editionRegistry = createEditionRegistry(commercialEditions)

export function resolveEdition(mode) {
  return editionRegistry.resolve(mode)
}

export function editionHas(mode, capability) {
  return resolveEdition(mode).capabilities.includes(capability)
}
