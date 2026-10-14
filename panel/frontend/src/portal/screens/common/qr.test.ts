import { describe, expect, it } from 'vitest'
import {
  alignmentPositions,
  byteCapacity,
  dataCodewords,
  dataCodewordsFor,
  encodeQr,
  formatBits,
  functionModules,
  maskHit,
  MAX_VERSION,
  rawDataModules,
  rsDivisor,
  rsRemainder,
  sizeOf,
  versionBits,
  type QrMatrix,
} from './qr'

// ---------------------------------------------------------------------------
// 测试向量：ISO/IEC 18004 附录 I（「01234567」1-M）、Thonky 教程（「HELLO WORLD」1-M）的纠错码字，
// 标准表里的格式信息（M 级 8 个掩码）与版本信息（7–10 版）、校正图形位置、字节模式容量；
// 再用下面的解码器把每个版本编出来的码解回原文（格式位 → 去掩码 → 之字形读码字 → 拆块验纠错 → 读字节）。
// ---------------------------------------------------------------------------

describe('qr · Reed-Solomon', () => {
  it('ISO 18004 Annex I: "01234567" at 1-M', () => {
    const data = [16, 32, 12, 86, 97, 128, 236, 17, 236, 17, 236, 17, 236, 17, 236, 17]
    expect(rsRemainder(data, rsDivisor(10))).toEqual([165, 36, 212, 193, 237, 54, 199, 135, 44, 85])
  })

  it('Thonky tutorial: "HELLO WORLD" at 1-M', () => {
    const data = [32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17]
    expect(rsRemainder(data, rsDivisor(10))).toEqual([196, 35, 39, 119, 235, 215, 231, 226, 93, 23])
  })
})

describe('qr · tables', () => {
  it('format information for level M, masks 0–7', () => {
    const want = ['101010000010010', '101000100100101', '101111001111100', '101101101001011', '100010111111001', '100000011001110', '100111110010111', '100101010100000']
    expect(want.map((_, mask) => formatBits(mask).toString(2).padStart(15, '0'))).toEqual(want)
  })

  it('version information for versions 7–10', () => {
    expect([7, 8, 9, 10].map((v) => versionBits(v).toString(2).padStart(18, '0'))).toEqual(['000111110010010100', '001000010110111100', '001001101010011001', '001010010011010011'])
  })

  it('alignment pattern centres', () => {
    expect(alignmentPositions(1)).toEqual([])
    expect(alignmentPositions(2)).toEqual([6, 18])
    expect(alignmentPositions(7)).toEqual([6, 22, 38])
    expect(alignmentPositions(10)).toEqual([6, 28, 50])
    expect(alignmentPositions(14)).toEqual([6, 26, 46, 66])
    expect(alignmentPositions(32)).toEqual([6, 34, 60, 86, 112, 138])
    expect(alignmentPositions(40)).toEqual([6, 30, 58, 86, 114, 142, 170])
  })

  it('codeword counts and byte-mode capacity at level M', () => {
    expect([1, 2, 3, 4, 5, 6, 7, 8, 9, 10].map((v) => Math.floor(rawDataModules(v) / 8))).toEqual([26, 44, 70, 100, 134, 172, 196, 242, 292, 346])
    expect([1, 2, 3, 4, 5, 6, 7, 8, 9, 10].map(dataCodewords)).toEqual([16, 28, 44, 64, 86, 108, 124, 154, 182, 216])
    expect([1, 2, 3, 4, 5, 6, 7, 8, 9, 10].map(byteCapacity)).toEqual([14, 26, 42, 62, 84, 106, 122, 152, 180, 213])
    expect(byteCapacity(40)).toBe(2331)
  })

  it('pads with 0xEC / 0x11 after the terminator', () => {
    // 「HELLO」字节模式 1 版：0100 00000101 01001000 … 0000 补齐，再 EC 11 交替
    expect(dataCodewordsFor(new TextEncoder().encode('HELLO'), 1)).toEqual([0x40, 0x54, 0x84, 0x54, 0xc4, 0xc4, 0xf0, 0xec, 0x11, 0xec, 0x11, 0xec, 0x11, 0xec, 0x11, 0xec])
  })
})

// ---------------------------------------------------------------------------
// 测试用解码器：只认本编码器的输出（M 级、字节模式）
// ---------------------------------------------------------------------------
const BLOCKS_M = [-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49]

function decode(m: QrMatrix): string {
  const n = m.size
  const version = (n - 17) / 4
  const at = (x: number, y: number) => m.modules[y]![x]!
  // 格式信息第一份：(8,0..5) (8,7) (8,8) (7,8) (5..0,8)
  const cells: Array<[number, number]> = [[8, 0], [8, 1], [8, 2], [8, 3], [8, 4], [8, 5], [8, 7], [8, 8], [7, 8], [5, 8], [4, 8], [3, 8], [2, 8], [1, 8], [0, 8]]
  const read = cells.reduce((acc, [x, y], i) => acc | (Number(at(x, y)) << i), 0)
  const mask = [0, 1, 2, 3, 4, 5, 6, 7].find((k) => formatBits(k) === read)
  if (mask === undefined) throw new Error('format bits do not match level M')
  // 第二份格式信息也要一致
  const second: Array<[number, number]> = [...[...Array(8).keys()].map((i): [number, number] => [n - 1 - i, 8]), ...[...Array(7).keys()].map((i): [number, number] => [8, n - 7 + i])]
  expect(second.reduce((acc, [x, y], i) => acc | (Number(at(x, y)) << i), 0)).toBe(read)
  expect(at(8, n - 8)).toBe(true)
  const fn = functionModules(version)
  const bits: number[] = []
  for (let right = n - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5
    for (let vert = 0; vert < n; vert++) {
      for (let j = 0; j < 2; j++) {
        const x = right - j
        const y = ((right + 1) & 2) === 0 ? n - 1 - vert : vert
        if (!fn[y]![x]) bits.push(Number(at(x, y) !== maskHit(mask, x, y)))
      }
    }
  }
  const raw = Math.floor(rawDataModules(version) / 8)
  const codewords = [...Array(raw).keys()].map((i) => bits.slice(i * 8, i * 8 + 8).reduce((a, b) => (a << 1) | b, 0))
  // 拆块：短块在前，长块多一个数据码字；先按列交织的数据，再按列交织的纠错
  const numBlocks = BLOCKS_M[version]!
  const eccLen = (raw - dataCodewords(version)) / numBlocks
  const numShort = numBlocks - (raw % numBlocks)
  const shortData = Math.floor(raw / numBlocks) - eccLen
  const blocks = [...Array(numBlocks).keys()].map((b) => ({ data: [] as number[], ecc: [] as number[], len: shortData + (b < numShort ? 0 : 1) }))
  let k = 0
  for (let i = 0; i <= shortData; i++) for (const b of blocks) if (i < b.len) b.data.push(codewords[k++]!)
  for (let i = 0; i < eccLen; i++) for (const b of blocks) b.ecc.push(codewords[k++]!)
  const divisor = rsDivisor(eccLen)
  for (const b of blocks) expect(rsRemainder(b.data, divisor)).toEqual(b.ecc)
  const data = blocks.flatMap((b) => b.data)
  const stream = data.flatMap((c) => [7, 6, 5, 4, 3, 2, 1, 0].map((i) => (c >>> i) & 1))
  const take = (len: number) => stream.splice(0, len).reduce((a, b) => (a << 1) | b, 0)
  expect(take(4)).toBe(0b0100)
  const count = take(version <= 9 ? 8 : 16)
  return new TextDecoder().decode(Uint8Array.from({ length: count }, () => take(8)))
}

describe('qr · round trip', () => {
  it('fills each version 1–40 to capacity and decodes back', () => {
    for (let v = 1; v <= MAX_VERSION; v++) {
      const text = Array.from({ length: byteCapacity(v) }, (_, i) => String.fromCharCode(33 + ((i * 7 + v) % 90))).join('')
      const m = encodeQr(text)!
      expect(m.version, `v${v}`).toBe(v)
      expect(m.size).toBe(sizeOf(v))
      expect(decode(m), `v${v}`).toBe(text)
    }
  })

  it('encodes UTF-8 and picks the smallest version that fits', () => {
    const url = 'https://sub.pandora.dev/s/q8Z0b1Xr6Kp2a3f9'
    const m = encodeQr(url)!
    expect(m.version).toBe(3)
    expect(decode(m)).toBe(url)
    const zh = '在 iPhone 或 iPad 上装 Shadowrocket → 打开这条链接 → 点「添加」'
    expect(decode(encodeQr(zh)!)).toBe(zh)
  })

  it('returns null when the text does not fit the largest version', () => {
    expect(encodeQr('x'.repeat(2332))).toBeNull()
    expect(encodeQr('x'.repeat(214), 10)).toBeNull()
  })
})
