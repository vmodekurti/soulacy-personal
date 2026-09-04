// Commercial distribution metadata. The future soulacy-commercial repository
// owns this module and the Teams/Scale implementations it describes.
import { editionCapabilities } from '../lib/edition.js'

export const commercialEditions = [
  {
    id: 'team', displayName: 'Teams', commercial: true, extends: 'personal',
    capabilities: [
      editionCapabilities.multiUser,
      editionCapabilities.workspaceAdmin,
      editionCapabilities.centralizedProviders,
    ],
  },
  {
    id: 'scale', displayName: 'Scale', commercial: true, extends: 'team',
    capabilities: [
      editionCapabilities.externalIdentity,
      editionCapabilities.auditReporting,
      editionCapabilities.distributedRuntime,
      editionCapabilities.highAvailability,
    ],
  },
]
