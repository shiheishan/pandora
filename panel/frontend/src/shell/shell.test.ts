import { describe, expect, it } from 'vitest'
import { isChunkLoadError } from './ScreenFrame'

describe('isChunkLoadError', () => {
  it('recognizes failed dynamic imports in Chromium, Firefox and Safari', () => {
    expect(isChunkLoadError(new TypeError('Failed to fetch dynamically imported module: https://x/assets/a-1.js'))).toBe(true)
    expect(isChunkLoadError(new TypeError('error loading dynamically imported module: https://x/assets/a-1.js'))).toBe(true)
    expect(isChunkLoadError(new TypeError('Importing a module script failed.'))).toBe(true)
  })

  it('leaves ordinary render errors alone', () => {
    expect(isChunkLoadError(new Error('Cannot read properties of undefined'))).toBe(false)
    expect(isChunkLoadError('Failed to fetch dynamically imported module')).toBe(false)
    expect(isChunkLoadError(null)).toBe(false)
  })
})
