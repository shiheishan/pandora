/**
 * [INPUT]: 依赖 vitest，依赖 ./download 的 saveFile / filenameFromDisposition / toCsv
 * [OUTPUT]: 对外提供 download.ts 的单元测试
 * [POS]: core/download 的单元测试：文件名取自响应头或回退、CSV 转义与防公式注入、对象 URL 用后回收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it, vi } from 'vitest'
import { filenameFromDisposition, saveFile, toCsv } from './download'

describe('filenameFromDisposition', () => {
  it('reads quoted and bare filenames and falls back when the header is missing', () => {
    expect(filenameFromDisposition('attachment; filename="gift-codes-3f9a1c2b.csv"', 'x.csv')).toBe('gift-codes-3f9a1c2b.csv')
    expect(filenameFromDisposition('attachment; filename=a.csv', 'x.csv')).toBe('a.csv')
    expect(filenameFromDisposition(null, 'gift-codes-3f9a1c2b.csv')).toBe('gift-codes-3f9a1c2b.csv')
    expect(filenameFromDisposition('inline', 'x.csv')).toBe('x.csv')
  })
})

describe('toCsv', () => {
  it('prefixes a BOM, escapes quotes and commas, and neutralizes formulas', async () => {
    const bytes = new Uint8Array(
      await toCsv([
        ['优惠码', '备注'],
        ['VIP8K2', 'a,"b"'],
        ['=HYPERLINK()', '-1'],
      ]).arrayBuffer(),
    )
    // Blob.text() 解码时会吞掉 BOM，所以按字节查
    expect([...bytes.slice(0, 3)]).toEqual([0xef, 0xbb, 0xbf])
    expect(new TextDecoder().decode(bytes)).toBe('优惠码,备注\r\nVIP8K2,"a,""b"""\r\n\'=HYPERLINK(),\'-1\r\n')
  })
})

describe('saveFile', () => {
  it('clicks a download link and revokes the object URL afterwards', async () => {
    vi.useFakeTimers()
    const env = { createObjectURL: vi.fn(() => 'blob:1'), revokeObjectURL: vi.fn(), click: vi.fn() }
    saveFile(new Blob(['x']), 'a.csv', env)
    expect(env.click).toHaveBeenCalledWith('blob:1', 'a.csv')
    expect(env.revokeObjectURL).not.toHaveBeenCalled()
    vi.runAllTimers()
    expect(env.revokeObjectURL).toHaveBeenCalledWith('blob:1')
    vi.useRealTimers()
  })
})
