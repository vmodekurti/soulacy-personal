import { describe, it, expect } from 'vitest'
import { looksLikeStaleAssetError } from './stalerecovery.js'

describe('stale asset recovery detection', () => {
  it('recognizes Vite dynamic import failures', () => {
    expect(looksLikeStaleAssetError(new Error('Failed to fetch dynamically imported module: http://localhost:18789/assets/Agents-DbNK-spe.js'))).toBe(true)
    expect(looksLikeStaleAssetError({ reason: { message: 'error loading dynamically imported module' } })).toBe(true)
  })

  it('ignores ordinary application errors', () => {
    expect(looksLikeStaleAssetError(new Error('provider returned http 502'))).toBe(false)
  })
})
