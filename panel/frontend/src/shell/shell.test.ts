/**
 * [INPUT]: 依赖 vitest，依赖 ./ScreenFrame 的 isChunkLoadError
 * [OUTPUT]: 对外提供 shell 纯逻辑的单元测试
 * [POS]: shell 的单元测试：页面块加载失败的识别（三家浏览器的报错文案）；错误边界与 Suspense 的界面行为在浏览器里对假后端验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
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
